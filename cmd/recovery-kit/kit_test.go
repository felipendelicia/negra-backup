// cmd/recovery-kit/kit_test.go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverKey stands in for the installation's ENCRYPTION_KEY.
const serverKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// otherKey is what a row encrypted by some other installation looks like.
const otherKey = "aabbccddeeff00112233445566778899aabbccddeeff001122334455667788aa"

const kitPass = "correct horse battery staple"

var (
	testAgentID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	testDestID  = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	testJobID   = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	testRunID   = uuid.MustParse("44444444-4444-4444-4444-444444444444")
	testNow     = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
)

func mustEncrypt(t *testing.T, key, plaintext string) string {
	t.Helper()
	c, err := crypto.Encrypt(key, plaintext)
	require.NoError(t, err)
	return c
}

func ptr[T any](v T) *T { return &v }

// sampleSource builds one agent, one destination, one encrypted job and one
// successful run — the smallest installation that still exercises every link the
// kit has to preserve.
func sampleSource(t *testing.T) exportSource {
	t.Helper()
	return exportSource{
		Database:      "postgres://db.internal:5432/nat_backup",
		Hostname:      "backup-server",
		SchemaVersion: 5,
		RunsPerJob:    25,
		Agents: []models.Agent{{
			ID: testAgentID, Name: "web-01", OS: "linux", Arch: "amd64",
			Version: "1.2.3", CreatedAt: testNow,
		}},
		Destinations: []rawDestination{{
			ID: testDestID, Name: "wasabi", Type: "s3",
			Config: mustEncrypt(t, serverKey,
				`{"bucket":"nat-backups","region":"us-east-1","access_key":"AKIAEXAMPLE","secret_key":"s3cr3t-key","prefix":"prod/"}`),
			CreatedAt: testNow,
		}},
		Jobs: []models.BackupJob{{
			ID: testJobID, AgentID: testAgentID, Name: "etc-nightly", Enabled: true,
			Type:                 models.JobTypeFiles,
			Source:               json.RawMessage(`{"paths":["/etc","/srv/app"]}`),
			StorageDestinationID: testDestID,
			ScheduleCron:         "0 3 * * *",
			RetentionDays:        30,
			Compression:          models.CompressionZstd,
			Encrypt:              true,
			EncryptPassphrase:    ptr(mustEncrypt(t, serverKey, "el-secreto-del-backup-ñ")),
			CreatedAt:            testNow,
		}},
		Runs: []rawRun{{
			ID: testRunID, JobID: testJobID, StartedAt: testNow, FinishedAt: ptr(testNow),
			Status: "success", SizeBytes: ptr(int64(4096)), FileCount: ptr(120),
			StoragePath: ptr("etc-nightly-44444444.tar.zstd.enc"),
			Checksum:    ptr("deadbeef"),
		}},
	}
}

// ── building ──────────────────────────────────────────────────────────────────

func TestBuildKit_RecoversSecretsAndLinks(t *testing.T) {
	k := buildKit(sampleSource(t), serverKey, testNow)

	require.Empty(t, k.Problems, "a clean database must produce a clean kit")
	require.Len(t, k.Jobs, 1)
	require.Len(t, k.StorageDestinations, 1)
	require.Len(t, k.Runs, 1)

	job := k.Jobs[0]
	// The one thing the kit exists for.
	assert.Equal(t, "el-secreto-del-backup-ñ", job.EncryptPassphrase)
	assert.Equal(t, passRecovered, job.PassphraseStatus)
	assert.Equal(t, "web-01", job.AgentName)
	assert.Equal(t, "wasabi", job.StorageDestinationName)
	assert.Equal(t, "s3", job.StorageDestinationType)
	assert.Equal(t, []any{"/etc", "/srv/app"}, job.Source["paths"])

	dest := k.StorageDestinations[0]
	assert.Equal(t, configRecovered, dest.ConfigStatus)
	assert.Equal(t, "s3cr3t-key", dest.Config["secret_key"])
	assert.Equal(t, "nat-backups", dest.Config["bucket"])

	// A run has to be enough on its own to answer "which file, where, opened with
	// what".
	run := k.Runs[0]
	assert.Equal(t, "etc-nightly-44444444.tar.zstd.enc", run.StoragePath)
	assert.Equal(t, "deadbeef", run.Checksum)
	assert.Equal(t, "etc-nightly", run.JobName)
	assert.Equal(t, "wasabi", run.StorageDestinationName)
	assert.True(t, run.Encrypted)

	assert.Equal(t, kitVersion, k.KitVersion)
	assert.NotEmpty(t, k.Warning)
	assert.Equal(t, "postgres://db.internal:5432/nat_backup", k.Source.Database)
}

func TestBuildKit_UndecryptablePassphraseIsReportedNotSwallowed(t *testing.T) {
	src := sampleSource(t)
	// Encrypted with a key this installation does not have.
	src.Jobs[0].EncryptPassphrase = ptr(mustEncrypt(t, otherKey, "unreachable"))

	k := buildKit(src, serverKey, testNow)

	require.Len(t, k.Jobs, 1)
	assert.Empty(t, k.Jobs[0].EncryptPassphrase)
	assert.Equal(t, passUndecryptable, k.Jobs[0].PassphraseStatus)
	require.Len(t, k.Problems, 1)
	assert.Contains(t, k.Problems[0], "etc-nightly")

	// And the whole export still succeeds: one broken row must not cost the
	// operator every other passphrase.
	assert.Len(t, k.StorageDestinations, 1)
	assert.Equal(t, configRecovered, k.StorageDestinations[0].ConfigStatus)
}

func TestBuildKit_UndecryptableDestinationConfigIsReported(t *testing.T) {
	src := sampleSource(t)
	src.Destinations[0].Config = mustEncrypt(t, otherKey, `{"bucket":"x"}`)

	k := buildKit(src, serverKey, testNow)

	assert.Equal(t, configUndecryptable, k.StorageDestinations[0].ConfigStatus)
	require.Len(t, k.Problems, 1)
	assert.Contains(t, k.Problems[0], "wasabi")
	// The passphrase still came out: that is the part that cannot be reissued.
	assert.Equal(t, "el-secreto-del-backup-ñ", k.Jobs[0].EncryptPassphrase)
}

func TestBuildKit_EncryptedJobWithoutStoredPassphrase(t *testing.T) {
	src := sampleSource(t)
	src.Jobs[0].EncryptPassphrase = nil

	k := buildKit(src, serverKey, testNow)

	assert.Equal(t, passMissing, k.Jobs[0].PassphraseStatus)
	require.Len(t, k.Problems, 1)
	assert.Contains(t, k.Problems[0], "cannot be recovered")
}

func TestBuildKit_KeepsPassphraseWhenEncryptionWasTurnedOff(t *testing.T) {
	// Turning encryption off does not decrypt the archives written while it was on.
	// Dropping the passphrase here would lose every one of them.
	src := sampleSource(t)
	src.Jobs[0].Encrypt = false

	k := buildKit(src, serverKey, testNow)

	assert.Equal(t, "el-secreto-del-backup-ñ", k.Jobs[0].EncryptPassphrase)
	assert.Equal(t, passRecoveredOff, k.Jobs[0].PassphraseStatus)
	assert.Empty(t, k.Problems)
}

func TestBuildKit_JobPointingAtMissingDestination(t *testing.T) {
	src := sampleSource(t)
	src.Destinations = nil

	k := buildKit(src, serverKey, testNow)

	require.Len(t, k.Problems, 1)
	assert.Contains(t, k.Problems[0], "was not exported")
}

func TestBuildKit_DirtySchemaIsRecorded(t *testing.T) {
	src := sampleSource(t)
	src.SchemaDirty = true

	k := buildKit(src, serverKey, testNow)

	require.NotEmpty(t, k.Problems)
	assert.Contains(t, k.Problems[0], "dirty")
}

// ── sealing ───────────────────────────────────────────────────────────────────

func TestSealOpen_RoundTrip(t *testing.T) {
	k := buildKit(sampleSource(t), serverKey, testNow)

	var buf bytes.Buffer
	require.NoError(t, seal(&buf, k, kitPass))

	// The sealed bytes must not contain any secret in the clear.
	assert.NotContains(t, buf.String(), "el-secreto-del-backup")
	assert.NotContains(t, buf.String(), "s3cr3t-key")

	got, instructions, err := open(bytes.NewReader(buf.Bytes()), kitPass)
	require.NoError(t, err)

	require.Len(t, got.Jobs, 1)
	assert.Equal(t, "el-secreto-del-backup-ñ", got.Jobs[0].EncryptPassphrase)
	assert.Equal(t, "s3cr3t-key", got.StorageDestinations[0].Config["secret_key"])
	assert.Equal(t, k.ExportedAt, got.ExportedAt)
	assert.Contains(t, instructions, "nat-backup-agent restore")
}

// awkwardPassphrases are the shapes that break naive text handling. A kit that
// mangles one byte of a passphrase silently destroys every archive it was supposed
// to rescue.
var awkwardPassphrases = []string{
	"simple",
	"ñandú çedilla — 日本語",
	"with\nnewlines\tand\ttabs",
	`{"json":"looking"}`,
	`quotes "and" \backslashes\`,
	strings.Repeat("long", 2048),
	" leading and trailing ",
}

// TestKitDocument_PassphrasesSurviveJSON exercises the encoding layer, which is
// the only place in the pipeline where a byte can be reinterpreted — tar and the
// AEAD are binary-transparent. It is kept separate from the sealing test because
// key derivation costs 600k PBKDF2 iterations per call.
func TestKitDocument_PassphrasesSurviveJSON(t *testing.T) {
	for _, pass := range awkwardPassphrases {
		src := sampleSource(t)
		src.Jobs[0].EncryptPassphrase = ptr(mustEncrypt(t, serverKey, pass))

		body, err := marshalKit(buildKit(src, serverKey, testNow))
		require.NoError(t, err)

		var got Kit
		require.NoError(t, json.Unmarshal(body, &got))
		require.Equal(t, pass, got.Jobs[0].EncryptPassphrase)
	}
}

func TestSealOpen_PassphraseSurvivesExactly(t *testing.T) {
	src := sampleSource(t)
	pass := "ñandú \"quoted\"\ttab\nnewline \\ 日本語 "
	src.Jobs[0].EncryptPassphrase = ptr(mustEncrypt(t, serverKey, pass))

	var buf bytes.Buffer
	require.NoError(t, seal(&buf, buildKit(src, serverKey, testNow), kitPass))

	got, _, err := open(bytes.NewReader(buf.Bytes()), kitPass)
	require.NoError(t, err)
	require.Equal(t, pass, got.Jobs[0].EncryptPassphrase)
}

func TestOpen_WrongPassphrase(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, seal(&buf, buildKit(sampleSource(t), serverKey, testNow), kitPass))

	_, _, err := open(bytes.NewReader(buf.Bytes()), "not the passphrase")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrong passphrase")
}

func TestOpen_TamperedKitIsRejected(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, seal(&buf, buildKit(sampleSource(t), serverKey, testNow), kitPass))

	sealed := buf.Bytes()
	// Past the 32-byte salt and the 4-byte chunk length: inside the AEAD payload.
	sealed[len(sealed)-1] ^= 0xFF

	_, _, err := open(bytes.NewReader(sealed), kitPass)
	require.Error(t, err)
}

func TestOpen_NotAKit(t *testing.T) {
	// A plain zstd archive is exactly what an operator might point at by mistake.
	_, _, err := open(bytes.NewReader([]byte{0x28, 0xB5, 0x2F, 0xFD, 0x00}), kitPass)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an encrypted recovery kit")
}

func TestOpen_EmptyFile(t *testing.T) {
	_, _, err := open(bytes.NewReader(nil), kitPass)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty or truncated")
}

func TestOpen_NoPassphrase(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, seal(&buf, buildKit(sampleSource(t), serverKey, testNow), kitPass))

	_, _, err := open(bytes.NewReader(buf.Bytes()), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "passphrase is required")
}

// TestSeal_OpensWithTheAgentRestoreCommand is the promise the embedded
// instructions make: the kit is an ordinary nat-backup archive, so an operator
// with nothing but the agent binary can extract it.
func TestSeal_OpensWithTheAgentRestoreCommand(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, seal(&buf, buildKit(sampleSource(t), serverKey, testNow), kitPass))

	dest := t.TempDir()
	res, err := backup.Restore(bytes.NewReader(buf.Bytes()), dest, kitPass)
	require.NoError(t, err)
	assert.True(t, res.Encrypted)
	assert.Equal(t, 2, res.FileCount)

	doc, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(documentMember)))
	require.NoError(t, err)
	var k Kit
	require.NoError(t, json.Unmarshal(doc, &k))
	assert.Equal(t, "el-secreto-del-backup-ñ", k.Jobs[0].EncryptPassphrase)

	txt, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(instructionsMember)))
	require.NoError(t, err)
	assert.Contains(t, string(txt), "RECOVERY, STEP BY STEP")
}

// ── masking ───────────────────────────────────────────────────────────────────

func TestMasked_HidesSecretsAndKeepsBearings(t *testing.T) {
	k := buildKit(sampleSource(t), serverKey, testNow)
	m := k.masked()

	body, err := json.Marshal(m)
	require.NoError(t, err)
	text := string(body)

	assert.NotContains(t, text, "el-secreto-del-backup")
	assert.NotContains(t, text, "s3cr3t-key")
	assert.NotContains(t, text, "AKIAEXAMPLE")

	// Masking is useless if it also hides what the operator needs to navigate.
	assert.Contains(t, text, "nat-backups")
	assert.Contains(t, text, "us-east-1")
	assert.Contains(t, text, "/etc")
	assert.Contains(t, text, "etc-nightly-44444444.tar.zstd.enc")

	// The original is untouched: inspect masks a copy and keeps the real one.
	assert.Equal(t, "el-secreto-del-backup-ñ", k.Jobs[0].EncryptPassphrase)
	assert.Equal(t, "s3cr3t-key", k.StorageDestinations[0].Config["secret_key"])
}

func TestMasked_UnknownKeysAreMaskedByDefault(t *testing.T) {
	// Deny by default: a credential field added to a backend tomorrow is masked
	// from the day it is added, not the day someone remembers this file.
	src := sampleSource(t)
	src.Destinations[0].Config = mustEncrypt(t, serverKey,
		`{"bucket":"b","some_future_token":"tok-abc123","nested":{"deep":"x"}}`)

	m := buildKit(src, serverKey, testNow).masked()
	cfg := m.StorageDestinations[0].Config

	assert.Equal(t, "b", cfg["bucket"])
	assert.Equal(t, "***masked (10 chars)***", cfg["some_future_token"])
	assert.Equal(t, "***masked***", cfg["nested"])
}

func TestMasked_DatabaseConnectionStringIsASecret(t *testing.T) {
	src := sampleSource(t)
	src.Jobs[0].Type = models.JobTypePostgres
	src.Jobs[0].Source = json.RawMessage(`{"conn_string":"postgres://u:p4ss@h/db"}`)

	m := buildKit(src, serverKey, testNow).masked()

	body, err := json.Marshal(m)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "p4ss")
}

// ── checks ────────────────────────────────────────────────────────────────────

func levels(checks []check) map[string]checkLevel {
	out := make(map[string]checkLevel, len(checks))
	for _, c := range checks {
		out[c.Name] = c.Level
	}
	return out
}

func TestCheckKit_HealthyKitPasses(t *testing.T) {
	k := buildKit(sampleSource(t), serverKey, testNow)

	checks := checkKit(k, testNow.Add(time.Hour))

	assert.Equal(t, checkPass, worstLevel(checks))
	for _, c := range checks {
		assert.NotEqual(t, checkFail, c.Level, c.Name+": "+c.Detail)
	}
}

func TestCheckKit_MissingPassphraseFails(t *testing.T) {
	src := sampleSource(t)
	src.Jobs[0].EncryptPassphrase = nil
	k := buildKit(src, serverKey, testNow)

	checks := checkKit(k, testNow)

	assert.Equal(t, checkFail, worstLevel(checks))
	assert.Equal(t, checkFail, levels(checks)["backup passphrases"])
	assert.Equal(t, checkFail, levels(checks)["export problems"])
}

func TestCheckKit_OrphanDestinationFails(t *testing.T) {
	k := buildKit(sampleSource(t), serverKey, testNow)
	k.StorageDestinations = nil
	k.Problems = nil

	checks := checkKit(k, testNow)

	assert.Equal(t, checkFail, worstLevel(checks))
	assert.Equal(t, checkFail, levels(checks)["job -> destination links"])
}

func TestCheckKit_StaleKitWarns(t *testing.T) {
	k := buildKit(sampleSource(t), serverKey, testNow)

	checks := checkKit(k, testNow.Add(staleAfter+24*time.Hour))

	assert.Equal(t, checkWarn, worstLevel(checks))
	assert.Equal(t, checkWarn, levels(checks)["export date"])
}

func TestCheckKit_UnknownVersionFails(t *testing.T) {
	k := buildKit(sampleSource(t), serverKey, testNow)
	k.KitVersion = 99

	checks := checkKit(k, testNow)

	assert.Equal(t, checkFail, levels(checks)["kit version"])
}

// ── instructions ──────────────────────────────────────────────────────────────

func TestInstructions_AreUsableWithoutThisRepository(t *testing.T) {
	k := buildKit(sampleSource(t), serverKey, testNow)
	txt := instructionsFor(k)

	// Everything an operator needs to type, in the file itself.
	for _, want := range []string{
		"nat-backup-agent restore",
		"--from-storage",
		"--dry-run",
		"NAT_BACKUP_PASSPHRASE",
		"storage_path",
		"encrypt_passphrase",
		"sha256sum",
		documentMember,
	} {
		assert.Contains(t, txt, want)
	}

	// And the warning, so a copy of the text alone still says what it is.
	assert.Contains(t, txt, "PLAINTEXT SECRETS")
	// No secret ever reaches the instructions.
	assert.NotContains(t, txt, "el-secreto-del-backup")
	assert.NotContains(t, txt, "s3cr3t-key")
}

func TestInstructions_ListProblems(t *testing.T) {
	src := sampleSource(t)
	src.Jobs[0].EncryptPassphrase = nil

	txt := instructionsFor(buildKit(src, serverKey, testNow))

	assert.Contains(t, txt, "PROBLEMS RECORDED AT EXPORT TIME")
	assert.Contains(t, txt, "etc-nightly")
}

// ── CLI plumbing ──────────────────────────────────────────────────────────────

func TestCheckPassphrase_RejectsWeakOnlyWhenCreating(t *testing.T) {
	_, err := checkPassphrase("short", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least")

	// An old kit must open with whatever it was sealed with.
	got, err := checkPassphrase("short", false)
	require.NoError(t, err)
	assert.Equal(t, "short", got)

	_, err = checkPassphrase("", false)
	require.Error(t, err)
}

func TestRedactDatabaseURL(t *testing.T) {
	assert.Equal(t, "postgres://db:5432/nat_backup",
		redactDatabaseURL("postgres://user:hunter2@db:5432/nat_backup?sslmode=disable&password=hunter2"))
	assert.Equal(t, "(unparsed connection string, redacted)",
		redactDatabaseURL("host=db user=u password=hunter2 dbname=nat_backup"))
}

func TestWriteKit_RefusesToLeaveAnUnreadableKitBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kit.enc")
	k := buildKit(sampleSource(t), serverKey, testNow)

	sum, size, err := writeKit(path, k, kitPass)
	require.NoError(t, err)
	assert.Len(t, sum, 64)
	assert.Positive(t, size)

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "a file full of secrets must not be readable by others")

	// No temporary files left behind.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	got, _, err := open(f, kitPass)
	require.NoError(t, err)
	assert.Equal(t, "el-secreto-del-backup-ñ", got.Jobs[0].EncryptPassphrase)
}

func TestProveReadable_CatchesAKitThatDoesNotMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kit.enc")
	k := buildKit(sampleSource(t), serverKey, testNow)

	_, _, err := writeKit(path, k, kitPass)
	require.NoError(t, err)

	// Same file, but claiming it should contain a different passphrase.
	tampered := buildKit(sampleSource(t), serverKey, testNow)
	tampered.Jobs[0].EncryptPassphrase = "something else"

	err = proveReadable(path, tampered, kitPass)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "round trip")
}
