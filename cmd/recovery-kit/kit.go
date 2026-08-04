// cmd/recovery-kit/kit.go
//
// The kit itself: what goes in it, how it is sealed, and how it is read back.
package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
)

// kitVersion is the version of the JSON document below. It is written into every
// kit so a future reader can tell what it is looking at before parsing it.
const kitVersion = 1

// Member names inside the kit archive. The instructions come first so a partial
// read, or `tar -t`, shows the human-readable file before the data.
const (
	instructionsMember = "recovery-kit/RECOVERY-INSTRUCTIONS.txt"
	documentMember     = "recovery-kit/recovery-kit.json"
)

// maxPayloadBytes caps what open() will decompress into memory. A kit is a few
// kilobytes; anything near this is either corrupt or a decompression bomb.
const maxPayloadBytes = 128 << 20

// kitWarning is stored inside the document itself, so the warning survives being
// copied, renamed, or pasted somewhere else — not only in the tool's output.
const kitWarning = "DECRYPTED, THIS FILE CONTAINS EVERY BACKUP PASSPHRASE AND EVERY " +
	"STORAGE CREDENTIAL OF THIS INSTALLATION IN PLAINTEXT. Anyone holding it can read " +
	"and delete every backup. Keep it offline and encrypted; never commit it, never " +
	"leave it on the backup server."

// Passphrase recovery outcomes, recorded per job so a kit is honest about what it
// could not read instead of silently exporting an empty string.
const (
	passRecovered       = "recovered"
	passRecoveredOff    = "recovered (job encryption is currently OFF; archives made while it was ON still need this passphrase)"
	passNotSet          = "not set (this job is not encrypted)"
	passMissing         = "MISSING - the job is encrypted but the database holds no passphrase"
	passUndecryptable   = "UNDECRYPTABLE - ENCRYPTION_KEY does not open the stored value"
	configRecovered     = "recovered"
	configUndecryptable = "UNDECRYPTABLE - ENCRYPTION_KEY does not open the stored value"
)

// Kit is the document an operator reads at 3 AM. Field order is the order it is
// marshalled in, so the warning and the metadata come before the payload.
type Kit struct {
	KitVersion int       `json:"kit_version"`
	Warning    string    `json:"warning"`
	ReadMe     string    `json:"read_me"`
	ExportedAt time.Time `json:"exported_at"`
	Source     KitSource `json:"source"`
	// Problems lists everything that could not be exported cleanly. An empty list
	// is the only acceptable state for a kit you intend to rely on.
	Problems            []string                `json:"problems"`
	Agents              []KitAgent              `json:"agents"`
	StorageDestinations []KitStorageDestination `json:"storage_destinations"`
	Jobs                []KitJob                `json:"jobs"`
	Runs                []KitRun                `json:"runs"`
}

// KitSource records where the kit came from, without leaking how to connect to it.
type KitSource struct {
	// Database is the connection string with credentials and query parameters
	// stripped: enough to tell two installations apart, useless as a credential.
	Database      string `json:"database"`
	SchemaVersion uint   `json:"schema_version"`
	SchemaDirty   bool   `json:"schema_dirty"`
	Hostname      string `json:"hostname"`
	ToolVersion   string `json:"tool_version"`
	RunsPerJob    int    `json:"runs_per_job"`
}

type KitAgent struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

type KitStorageDestination struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	// Config is the decrypted destination config: bucket, region and the access
	// keys. Paste it verbatim into the "config" field of a --from-storage spec.
	Config       map[string]any `json:"config"`
	ConfigStatus string         `json:"config_status"`
	CreatedAt    time.Time      `json:"created_at"`
}

type KitJob struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Type      string `json:"type"`
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	// Source is what was backed up: {"paths":[...]} for file jobs, or
	// {"conn_string":"..."} for database jobs. It is stored unencrypted in the
	// database and a connection string usually carries a password.
	Source                 map[string]any `json:"source"`
	StorageDestinationID   string         `json:"storage_destination_id"`
	StorageDestinationName string         `json:"storage_destination_name"`
	StorageDestinationType string         `json:"storage_destination_type"`
	ScheduleCron           string         `json:"schedule_cron"`
	RetentionDays          int            `json:"retention_days"`
	Compression            string         `json:"compression"`
	Encrypt                bool           `json:"encrypt"`
	// EncryptPassphrase is THE reason this kit exists: the plaintext passphrase
	// every archive of this job was encrypted with.
	EncryptPassphrase string    `json:"encrypt_passphrase"`
	PassphraseStatus  string    `json:"passphrase_status"`
	CreatedAt         time.Time `json:"created_at"`
}

type KitRun struct {
	ID         string     `json:"id"`
	JobID      string     `json:"job_id"`
	JobName    string     `json:"job_name"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Status     string     `json:"status"`
	SizeBytes  *int64     `json:"size_bytes"`
	FileCount  *int       `json:"file_count"`
	// StoragePath is the file name inside the destination — what you pass to
	// `nat-backup-agent restore --name`.
	StoragePath string `json:"storage_path"`
	// Checksum is the SHA-256 of the stored archive, when the run recorded one.
	Checksum               string `json:"checksum"`
	StorageDestinationID   string `json:"storage_destination_id"`
	StorageDestinationName string `json:"storage_destination_name"`
	StorageDestinationType string `json:"storage_destination_type"`
	// Encrypted mirrors the job's flag at export time: whether this archive needs
	// the job's passphrase to open.
	Encrypted   bool   `json:"encrypted"`
	Compression string `json:"compression"`
}

// ── Building ──────────────────────────────────────────────────────────────────

// rawDestination is a storage_destinations row exactly as stored: config is the
// AES ciphertext, not JSON.
type rawDestination struct {
	ID        uuid.UUID `db:"id"`
	Name      string    `db:"name"`
	Type      string    `db:"type"`
	Config    string    `db:"config"`
	CreatedAt time.Time `db:"created_at"`
}

// rawRun is a backup_runs row, restricted to the columns that identify a stored
// artifact.
type rawRun struct {
	ID          uuid.UUID  `db:"id"`
	JobID       uuid.UUID  `db:"job_id"`
	StartedAt   time.Time  `db:"started_at"`
	FinishedAt  *time.Time `db:"finished_at"`
	Status      string     `db:"status"`
	SizeBytes   *int64     `db:"size_bytes"`
	FileCount   *int       `db:"file_count"`
	StoragePath *string    `db:"storage_path"`
	Checksum    *string    `db:"checksum"`
}

// exportSource is everything read from the database, before any decryption. It is
// a plain struct so buildKit can be tested without a database.
type exportSource struct {
	Agents        []models.Agent
	Destinations  []rawDestination
	Jobs          []models.BackupJob
	Runs          []rawRun
	Database      string
	Hostname      string
	SchemaVersion uint
	SchemaDirty   bool
	RunsPerJob    int
}

// buildKit turns the encrypted rows into the plaintext document.
//
// It never fails as a whole because one value is unreadable: a single corrupt row
// must not cost the operator the passphrases of every other job. Anything that
// could not be decrypted is recorded in Problems and in the per-entry status
// field, so the kit says what it is missing instead of pretending it is complete.
func buildKit(src exportSource, encKey string, now time.Time) Kit {
	k := Kit{
		KitVersion: kitVersion,
		Warning:    kitWarning,
		ReadMe:     "Step-by-step recovery instructions are in RECOVERY-INSTRUCTIONS.txt, inside this same kit.",
		ExportedAt: now.UTC(),
		Source: KitSource{
			Database:      src.Database,
			SchemaVersion: src.SchemaVersion,
			SchemaDirty:   src.SchemaDirty,
			Hostname:      src.Hostname,
			ToolVersion:   version,
			RunsPerJob:    src.RunsPerJob,
		},
		Problems:            []string{},
		Agents:              []KitAgent{},
		StorageDestinations: []KitStorageDestination{},
		Jobs:                []KitJob{},
		Runs:                []KitRun{},
	}

	if src.SchemaDirty {
		k.Problems = append(k.Problems,
			"the database schema is marked dirty: a migration failed halfway, so these rows may be inconsistent")
	}

	agentNames := make(map[uuid.UUID]string, len(src.Agents))
	for _, a := range src.Agents {
		agentNames[a.ID] = a.Name
		k.Agents = append(k.Agents, KitAgent{
			ID:        a.ID.String(),
			Name:      a.Name,
			OS:        a.OS,
			Arch:      a.Arch,
			Version:   a.Version,
			CreatedAt: a.CreatedAt.UTC(),
		})
	}

	type destInfo struct{ name, typ string }
	dests := make(map[uuid.UUID]destInfo, len(src.Destinations))
	for _, d := range src.Destinations {
		dests[d.ID] = destInfo{name: d.Name, typ: d.Type}

		entry := KitStorageDestination{
			ID:           d.ID.String(),
			Name:         d.Name,
			Type:         d.Type,
			ConfigStatus: configRecovered,
			CreatedAt:    d.CreatedAt.UTC(),
		}

		switch plaintext, err := decryptStored(encKey, d.Config); {
		case err != nil:
			entry.ConfigStatus = configUndecryptable
			entry.Config = map[string]any{"_undecryptable": err.Error()}
			k.Problems = append(k.Problems, fmt.Sprintf(
				"storage destination %q (%s): config could not be decrypted (%v) - the credentials for this destination are NOT in this kit",
				d.Name, d.ID, err))
		default:
			cfg, perr := decodeJSONObject(plaintext)
			if perr != nil {
				// Exported verbatim rather than dropped: an operator can still read
				// a credential out of a malformed blob, and cannot read one that
				// was thrown away.
				entry.Config = map[string]any{"_unparsed": plaintext}
				k.Problems = append(k.Problems, fmt.Sprintf(
					"storage destination %q (%s): config decrypted but is not a JSON object (%v); it is exported verbatim under \"_unparsed\"",
					d.Name, d.ID, perr))
				break
			}
			entry.Config = cfg
		}

		k.StorageDestinations = append(k.StorageDestinations, entry)
	}

	type jobInfo struct {
		name        string
		encrypt     bool
		compression string
		destID      uuid.UUID
	}
	jobs := make(map[uuid.UUID]jobInfo, len(src.Jobs))
	for _, j := range src.Jobs {
		jobs[j.ID] = jobInfo{name: j.Name, encrypt: j.Encrypt, compression: j.Compression, destID: j.StorageDestinationID}

		entry := KitJob{
			ID:                     j.ID.String(),
			Name:                   j.Name,
			Enabled:                j.Enabled,
			Type:                   j.Type,
			AgentID:                j.AgentID.String(),
			AgentName:              agentNames[j.AgentID],
			StorageDestinationID:   j.StorageDestinationID.String(),
			StorageDestinationName: dests[j.StorageDestinationID].name,
			StorageDestinationType: dests[j.StorageDestinationID].typ,
			ScheduleCron:           j.ScheduleCron,
			RetentionDays:          j.RetentionDays,
			Compression:            j.Compression,
			Encrypt:                j.Encrypt,
			CreatedAt:              j.CreatedAt.UTC(),
		}

		if source, err := decodeJSONObject(string(j.Source)); err != nil {
			entry.Source = map[string]any{"_unparsed": string(j.Source)}
		} else {
			entry.Source = source
		}

		if _, ok := dests[j.StorageDestinationID]; !ok {
			k.Problems = append(k.Problems, fmt.Sprintf(
				"job %q (%s) points at storage destination %s, which was not exported",
				j.Name, j.ID, j.StorageDestinationID))
		}

		// The passphrase is decrypted whatever the encrypt flag currently says.
		// Toggling encryption off does not decrypt the archives that were written
		// while it was on, and throwing the passphrase away here would lose them.
		switch {
		case j.EncryptPassphrase == nil || *j.EncryptPassphrase == "":
			if j.Encrypt {
				entry.PassphraseStatus = passMissing
				k.Problems = append(k.Problems, fmt.Sprintf(
					"job %q (%s) is encrypted but has no stored passphrase; its archives cannot be recovered from this kit",
					j.Name, j.ID))
			} else {
				entry.PassphraseStatus = passNotSet
			}
		default:
			pass, err := decryptStored(encKey, *j.EncryptPassphrase)
			switch {
			case err != nil:
				entry.PassphraseStatus = passUndecryptable
				k.Problems = append(k.Problems, fmt.Sprintf(
					"job %q (%s): passphrase could not be decrypted (%v) - its archives are NOT recoverable with this kit",
					j.Name, j.ID, err))
			case j.Encrypt:
				entry.EncryptPassphrase = pass
				entry.PassphraseStatus = passRecovered
			default:
				entry.EncryptPassphrase = pass
				entry.PassphraseStatus = passRecoveredOff
			}
		}

		k.Jobs = append(k.Jobs, entry)
	}

	for _, r := range src.Runs {
		info := jobs[r.JobID]
		entry := KitRun{
			ID:                     r.ID.String(),
			JobID:                  r.JobID.String(),
			JobName:                info.name,
			StartedAt:              r.StartedAt.UTC(),
			Status:                 r.Status,
			SizeBytes:              r.SizeBytes,
			FileCount:              r.FileCount,
			StoragePath:            derefString(r.StoragePath),
			Checksum:               derefString(r.Checksum),
			StorageDestinationID:   info.destID.String(),
			StorageDestinationName: dests[info.destID].name,
			StorageDestinationType: dests[info.destID].typ,
			Encrypted:              info.encrypt,
			Compression:            info.compression,
		}
		if r.FinishedAt != nil {
			f := r.FinishedAt.UTC()
			entry.FinishedAt = &f
		}
		k.Runs = append(k.Runs, entry)
	}

	return k
}

// decryptStored opens one value stored with ENCRYPTION_KEY.
func decryptStored(encKey, stored string) (string, error) {
	if encKey == "" {
		return "", fmt.Errorf("no encryption key configured")
	}
	return crypto.Decrypt(encKey, stored)
}

// decodeJSONObject parses a JSON object preserving numbers exactly as written:
// json.Number rather than float64, so a port or a byte count comes back out the
// way it went in.
func decodeJSONObject(s string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]any{}, nil
	}
	return m, nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ── Masking ───────────────────────────────────────────────────────────────────

// publicConfigKeys is an allow-list, not a deny-list. Masking works by naming the
// handful of keys that are known not to be secret and hiding everything else, so a
// storage backend that grows a new credential field is masked from the day it is
// added rather than the day someone remembers to update this file.
var publicConfigKeys = map[string]bool{
	"bucket":   true,
	"region":   true,
	"endpoint": true,
	"prefix":   true,
	"path":     true,
	"paths":    true,
	"host":     true,
	"port":     true,
	"user":     true,
}

// masked returns a copy of the kit with every secret replaced by a placeholder.
// The receiver is untouched: inspect prints the masked copy and keeps the real one
// for --show-secrets in the same process.
func (k Kit) masked() Kit {
	out := k

	out.StorageDestinations = make([]KitStorageDestination, len(k.StorageDestinations))
	for i, d := range k.StorageDestinations {
		d.Config = maskObject(d.Config)
		out.StorageDestinations[i] = d
	}

	out.Jobs = make([]KitJob, len(k.Jobs))
	for i, j := range k.Jobs {
		j.Source = maskObject(j.Source)
		if j.EncryptPassphrase != "" {
			j.EncryptPassphrase = maskString(j.EncryptPassphrase)
		}
		out.Jobs[i] = j
	}

	return out
}

func maskObject(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for key, v := range m {
		if publicConfigKeys[key] {
			out[key] = v
			continue
		}
		out[key] = maskAny(v)
	}
	return out
}

func maskAny(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if t == "" {
			return ""
		}
		return maskString(t)
	case bool:
		return t
	default:
		return "***masked***"
	}
}

func maskString(s string) string {
	return fmt.Sprintf("***masked (%d chars)***", len([]rune(s)))
}

// ── Sealing and opening ───────────────────────────────────────────────────────

// seal writes the kit as tar -> zstd -> AES-256-GCM, keyed by a passphrase the
// operator chose.
//
// The passphrase, not ENCRYPTION_KEY: the whole point of the kit is to survive the
// loss of that key, so sealing it with the very secret it exists to replace would
// be circular. internal/crypto takes a 64-hex-character key with no derivation
// step, which is not something a person holds; internal/backup derives the key
// from a passphrase with PBKDF2-SHA256 at 600k iterations over a random 32-byte
// salt, which is exactly the shape of this problem.
//
// The format is the same one the agent writes for a backup archive, so the kit can
// also be opened with `nat-backup-agent restore --file <kit>` by anyone who has
// the agent binary and nothing else — no server, no database, no recovery-kit.
func seal(w io.Writer, k Kit, passphrase string) error {
	if passphrase == "" {
		return fmt.Errorf("seal: passphrase required")
	}

	doc, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return fmt.Errorf("encode kit document: %w", err)
	}
	doc = append(doc, '\n')

	var payload bytes.Buffer
	enc, err := zstd.NewWriter(&payload)
	if err != nil {
		return fmt.Errorf("zstd writer: %w", err)
	}
	tw := tar.NewWriter(enc)

	members := []struct {
		name string
		body []byte
	}{
		{instructionsMember, []byte(instructionsFor(k))},
		{documentMember, doc},
	}
	for _, m := range members {
		hdr := &tar.Header{
			Name:     m.name,
			Mode:     0o600,
			Size:     int64(len(m.body)),
			ModTime:  k.ExportedAt,
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write tar header %s: %w", m.name, err)
		}
		if _, err := tw.Write(m.body); err != nil {
			return fmt.Errorf("write tar member %s: %w", m.name, err)
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close zstd: %w", err)
	}

	if err := backup.EncryptStream(&payload, w, passphrase); err != nil {
		return fmt.Errorf("encrypt kit: %w", err)
	}
	return nil
}

// open reverses seal. It returns the document and the instructions text.
//
// Everything happens in memory: a tool whose job is to hold every secret of an
// installation must not scatter plaintext copies across the filesystem as a side
// effect of being run.
func open(r io.Reader, passphrase string) (Kit, string, error) {
	var k Kit

	head := make([]byte, 4)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return k, "", fmt.Errorf("read kit: %w", err)
	}
	head = head[:n]
	if len(head) < 2 {
		return k, "", fmt.Errorf("file is empty or truncated: not a recovery kit")
	}
	if !backup.IsEncrypted(head) {
		return k, "", fmt.Errorf("file is not an encrypted recovery kit (it starts with a plain archive header)")
	}
	if passphrase == "" {
		return k, "", fmt.Errorf("kit is encrypted: a passphrase is required")
	}

	payload := &cappedBuffer{limit: maxPayloadBytes}
	if err := backup.DecryptStream(io.MultiReader(bytes.NewReader(head), r), payload, passphrase); err != nil {
		return k, "", fmt.Errorf("decrypt kit (wrong passphrase?): %w", err)
	}

	dec, err := zstd.NewReader(bytes.NewReader(payload.buf.Bytes()))
	if err != nil {
		return k, "", fmt.Errorf("kit payload is not zstd: %w", err)
	}
	defer dec.Close()

	var (
		docRaw       []byte
		instructions string
		seen         []string
	)
	tr := tar.NewReader(dec)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return k, "", fmt.Errorf("read kit archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, maxPayloadBytes))
		if err != nil {
			return k, "", fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		seen = append(seen, hdr.Name)
		switch hdr.Name {
		case documentMember:
			docRaw = body
		case instructionsMember:
			instructions = string(body)
		}
	}

	if docRaw == nil {
		return k, "", fmt.Errorf("kit contains no %s (members: %s)", documentMember, strings.Join(seen, ", "))
	}

	jd := json.NewDecoder(bytes.NewReader(docRaw))
	jd.UseNumber()
	if err := jd.Decode(&k); err != nil {
		return k, "", fmt.Errorf("parse %s: %w", documentMember, err)
	}

	return k, instructions, nil
}

// cappedBuffer is a bytes.Buffer that refuses to grow past a limit, so a hostile
// or corrupt kit cannot expand into all available memory.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		return 0, fmt.Errorf("kit payload exceeds %d bytes", c.limit)
	}
	return c.buf.Write(p)
}

// ── Checks ────────────────────────────────────────────────────────────────────

type checkLevel string

const (
	checkPass checkLevel = "PASS"
	checkWarn checkLevel = "WARN"
	checkFail checkLevel = "FAIL"
)

type check struct {
	Level  checkLevel
	Name   string
	Detail string
}

// staleAfter is how long a kit is assumed to still describe the installation.
// Adding or editing a job invalidates it immediately; this is only the backstop
// for the operator who forgot.
const staleAfter = 90 * 24 * time.Hour

// checkKit answers the only question verify exists to answer: if the server were
// gone right now, would this file actually get the backups back?
//
// It reports nothing sensitive — counts, names and statuses only, never a
// passphrase or a credential — so it is safe to run on a screen with someone
// watching, or from cron.
func checkKit(k Kit, now time.Time) []check {
	var out []check

	if k.KitVersion == kitVersion {
		out = append(out, check{checkPass, "kit version", fmt.Sprintf("%d", k.KitVersion)})
	} else {
		out = append(out, check{checkFail, "kit version",
			fmt.Sprintf("%d, but this tool writes and understands %d", k.KitVersion, kitVersion)})
	}

	age := now.Sub(k.ExportedAt)
	switch {
	case k.ExportedAt.IsZero():
		out = append(out, check{checkFail, "export date", "missing"})
	case age < -time.Hour:
		out = append(out, check{checkWarn, "export date",
			fmt.Sprintf("%s is in the future; check the clock on the machine that exported it",
				k.ExportedAt.Format(time.RFC3339))})
	case age > staleAfter:
		out = append(out, check{checkWarn, "export date",
			fmt.Sprintf("%s (%d days old) - re-export it", k.ExportedAt.Format(time.RFC3339), int(age.Hours()/24))})
	default:
		out = append(out, check{checkPass, "export date",
			fmt.Sprintf("%s (%d days old)", k.ExportedAt.Format(time.RFC3339), int(age.Hours()/24))})
	}

	if len(k.Problems) == 0 {
		out = append(out, check{checkPass, "export problems", "none recorded"})
	} else {
		out = append(out, check{checkFail, "export problems",
			fmt.Sprintf("%d recorded at export time; run inspect to read them", len(k.Problems))})
	}

	if len(k.Jobs) == 0 {
		out = append(out, check{checkWarn, "jobs", "the kit contains no jobs"})
	} else {
		out = append(out, check{checkPass, "jobs", fmt.Sprintf("%d", len(k.Jobs))})
	}

	if len(k.StorageDestinations) == 0 {
		out = append(out, check{checkWarn, "storage destinations", "the kit contains no destinations"})
	} else {
		out = append(out, check{checkPass, "storage destinations", fmt.Sprintf("%d", len(k.StorageDestinations))})
	}

	// The load-bearing check: an encrypted job whose passphrase did not make it
	// into the kit is a set of archives nobody will ever open again.
	var missing []string
	encrypted := 0
	for _, j := range k.Jobs {
		if !j.Encrypt {
			continue
		}
		encrypted++
		if j.EncryptPassphrase == "" {
			missing = append(missing, j.Name)
		}
	}
	switch {
	case encrypted == 0:
		out = append(out, check{checkPass, "backup passphrases", "no job uses encryption"})
	case len(missing) > 0:
		out = append(out, check{checkFail, "backup passphrases",
			fmt.Sprintf("%d of %d encrypted job(s) have no passphrase: %s",
				len(missing), encrypted, strings.Join(missing, ", "))})
	default:
		out = append(out, check{checkPass, "backup passphrases",
			fmt.Sprintf("all %d encrypted job(s) carry one", encrypted)})
	}

	destIDs := make(map[string]bool, len(k.StorageDestinations))
	var emptyCfg []string
	for _, d := range k.StorageDestinations {
		destIDs[d.ID] = true
		if len(d.Config) == 0 || d.ConfigStatus != configRecovered {
			emptyCfg = append(emptyCfg, d.Name)
		}
	}
	if len(emptyCfg) > 0 {
		out = append(out, check{checkFail, "storage credentials",
			fmt.Sprintf("%d destination(s) have no usable config: %s", len(emptyCfg), strings.Join(emptyCfg, ", "))})
	} else if len(k.StorageDestinations) > 0 {
		out = append(out, check{checkPass, "storage credentials",
			fmt.Sprintf("all %d destination(s) decrypted", len(k.StorageDestinations))})
	}

	var orphanJobs []string
	jobIDs := make(map[string]bool, len(k.Jobs))
	for _, j := range k.Jobs {
		jobIDs[j.ID] = true
		if !destIDs[j.StorageDestinationID] {
			orphanJobs = append(orphanJobs, j.Name)
		}
	}
	if len(orphanJobs) > 0 {
		out = append(out, check{checkFail, "job -> destination links",
			fmt.Sprintf("%d job(s) reference a destination missing from the kit: %s",
				len(orphanJobs), strings.Join(orphanJobs, ", "))})
	} else if len(k.Jobs) > 0 {
		out = append(out, check{checkPass, "job -> destination links", "every job resolves"})
	}

	orphanRuns := 0
	for _, r := range k.Runs {
		if !jobIDs[r.JobID] {
			orphanRuns++
		}
	}
	switch {
	case len(k.Runs) == 0:
		out = append(out, check{checkWarn, "runs", "no runs recorded; you will have to list the destination by hand to find archives"})
	case orphanRuns > 0:
		out = append(out, check{checkWarn, "runs",
			fmt.Sprintf("%d of %d run(s) reference a job missing from the kit", orphanRuns, len(k.Runs))})
	default:
		out = append(out, check{checkPass, "runs", fmt.Sprintf("%d, all resolving to a job", len(k.Runs))})
	}

	return out
}

func worstLevel(checks []check) checkLevel {
	worst := checkPass
	for _, c := range checks {
		if c.Level == checkFail {
			return checkFail
		}
		if c.Level == checkWarn {
			worst = checkWarn
		}
	}
	return worst
}

// ── Embedded instructions ─────────────────────────────────────────────────────

// instructionsFor renders the plain-text runbook that ships inside every kit.
//
// It is deliberately self-contained and assumes nothing: not this repository, not
// the server, not even the recovery-kit binary. The one thing it does assume is a
// nat-backup-agent binary, which is also the thing that produced the archives.
func instructionsFor(k Kit) string {
	var b strings.Builder

	fmt.Fprintf(&b, `NAT-BACKUP RECOVERY KIT
=======================

Exported : %s
By       : recovery-kit %s on %s
Source   : %s
Contents : %d job(s), %d storage destination(s), %d run(s), %d agent(s)
Problems : %d

*** THIS KIT CONTAINS PLAINTEXT SECRETS ***
recovery-kit.json, next to this file, holds every backup passphrase and every
storage credential of this installation IN THE CLEAR. Anyone who has these two
files can read, and delete, every backup that was ever made. Once you are done
recovering, delete the extracted copy. Keep only the sealed kit, offline.


WHY THIS EXISTS
---------------
The nat-backup server keeps two things encrypted in its PostgreSQL database: the
passphrase each backup archive was encrypted with, and the credentials of the
storage destination the archives were uploaded to. Both are sealed with the
server's ENCRYPTION_KEY.

Lose that database and that key, and every .enc archive in your bucket becomes
permanently unreadable - there is no recovery, no reset, no support path. This
kit is a copy of those secrets, sealed with a passphrase YOU chose, so the
archives can still be opened when the server is gone.


WHAT YOU NEED TO RECOVER A BACKUP
---------------------------------
  1. This kit, and the passphrase it was sealed with.
  2. The archive itself, still sitting in S3/SFTP/disk.
  3. A nat-backup-agent binary (any recent build).

You do NOT need: the server, the database, ENCRYPTION_KEY, or this repository.


HOW TO READ THIS KIT
--------------------
  With the recovery-kit tool:

      recovery-kit inspect --file <kit> --show-secrets

  Without it - the kit is an ordinary nat-backup archive (tar + zstd +
  AES-256-GCM with PBKDF2-SHA256), so the agent opens it:

      nat-backup-agent restore --file <kit> --dest ./kit-out
      cat ./kit-out/%s


RECOVERY, STEP BY STEP
----------------------
  1. FIND THE ARCHIVE.
     In recovery-kit.json, look under "runs". Each entry names one stored file:
       "storage_path"             the file name inside the destination
       "checksum"                 SHA-256 of that file, when it was recorded
       "job_id" / "job_name"      which job produced it
       "storage_destination_id"   where it lives
     If the run you need is older than what the kit carries, list the destination
     directly (aws s3 ls, sftp, ls) - the file names follow
     "<job name>-<run id>.tar.<zstd|gz>[.enc]".

  2. FIND THE DESTINATION.
     Under "storage_destinations", take the entry with that id and write a small
     spec file, copying its "type" and its "config" object verbatim:

         cat > /tmp/dest.json <<'EOF'
         {"type":"s3","config":{"bucket":"...","region":"...",
          "access_key":"...","secret_key":"...","prefix":"..."}}
         EOF
         chmod 600 /tmp/dest.json

  3. FIND THE PASSPHRASE.
     Under "jobs", the job with that id has "encrypt_passphrase". That is the
     passphrase the archive was encrypted with. Check "passphrase_status": it
     must say "recovered".

  4. DOWNLOAD AND RESTORE, in one step:

         export NAT_BACKUP_PASSPHRASE='<encrypt_passphrase>'
         nat-backup-agent restore \
             --from-storage /tmp/dest.json \
             --name '<storage_path>' \
             --dest /srv/restore

     Run it once with --dry-run first: it lists what is inside without writing
     anything, which is how you confirm the passphrase is right before committing
     to a full extraction.

     If you already downloaded the file by other means (aws s3 cp, sftp get, a
     copy on disk), skip the spec file entirely:

         export NAT_BACKUP_PASSPHRASE='<encrypt_passphrase>'
         nat-backup-agent restore --file ./archive.tar.zst.enc --dest /srv/restore

  5. VERIFY, if the run recorded a checksum:

         sha256sum ./archive.tar.zst.enc     # must equal "checksum"

  6. CLEAN UP. Delete /tmp/dest.json and the extracted copy of this kit. They are
     plaintext secrets.


DATABASE JOBS (postgres / mysql / mongodb / sqlite)
---------------------------------------------------
The archive holds a dump, not a live database. Inside it there is a single file
named "nat-backup-dump-<random>" - the agent's temporary file, no extension.
Restore it to disk as above, then load it with the matching client:

    psql "$NEW_URL" < /srv/restore/nat-backup-dump-*        # postgres
    mysql -u ... -p db < /srv/restore/nat-backup-dump-*     # mysql
    mongorestore --archive=/srv/restore/nat-backup-dump-*   # mongodb

SQLite is not dumped at all: the agent copies the .db file verbatim, so what
comes out of the restore already IS the database. Rename it and use it.

The job's "source" field records the connection string the dump was taken from.


IF THE STORAGE CREDENTIALS NO LONGER WORK
-----------------------------------------
The credentials in this kit are the ones that were valid at export time. If they
were rotated or revoked, get the file out of the bucket by any other means you
have, then restore it with --file. Only step 4 depends on the credentials; the
passphrase in step 3 is what actually makes the archive readable, and that one
cannot be reissued by anybody.


WHAT THIS KIT IS NOT
--------------------
  - It is not a backup. It contains no archive data whatsoever: it is the key
    ring, and it is worthless if the archives themselves are gone.
  - It does not contain the nat-backup server's own database. To rebuild the
    server as it was, restore a pg_dump of it; this kit only rescues the
    archives.
  - It does not contain agent API keys, the admin password, JWT_SECRET,
    ENCRYPTION_KEY, or the SMTP/notification settings.
  - It is a snapshot. Jobs created or edited after %s are not in here.
`,
		k.ExportedAt.Format(time.RFC3339),
		k.Source.ToolVersion,
		k.Source.Hostname,
		k.Source.Database,
		len(k.Jobs), len(k.StorageDestinations), len(k.Runs), len(k.Agents),
		len(k.Problems),
		documentMember,
		k.ExportedAt.Format(time.RFC3339),
	)

	if len(k.Problems) > 0 {
		b.WriteString("\n\nPROBLEMS RECORDED AT EXPORT TIME\n")
		b.WriteString("--------------------------------\n")
		b.WriteString("The kit is INCOMPLETE. Read these before you rely on it:\n\n")
		problems := append([]string(nil), k.Problems...)
		sort.Strings(problems)
		for _, p := range problems {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
	}

	return b.String()
}
