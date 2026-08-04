// cmd/recovery-kit/main.go
//
// recovery-kit exports everything needed to open a nat-backup archive without the
// nat-backup server.
//
// The server stores two secrets encrypted with ENCRYPTION_KEY: the passphrase each
// archive was encrypted with (backup_jobs.encrypt_passphrase) and the credentials
// of the storage destination the archives live in (storage_destinations.config).
// Nothing else in the system holds a copy. If the server's disk dies, every .enc
// file in the bucket becomes permanently unreadable - years of backups turned into
// noise, with no recovery path.
//
// This tool closes that hole. It produces a single self-contained file, sealed with
// a passphrase the operator chooses (NOT ENCRYPTION_KEY - the point is to survive
// losing that), containing the passphrases, the credentials, the map from stored
// file to job, and a plain-text runbook explaining how to use them. It is the key
// ring, not a backup: see docs/disaster-recovery.md.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/felipendelicia/nat-backup/internal/db"
	"github.com/jmoiron/sqlx"
	"golang.org/x/term"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// passphraseEnvVar is the way to supply the kit passphrase non-interactively.
// A --passphrase flag is accepted too, but it is visible in `ps` to every user on
// the machine and lands in the shell history.
const passphraseEnvVar = "NAT_BACKUP_KIT_PASSPHRASE"

// minPassphraseLen applies when creating a kit, never when opening one: an old kit
// must always open, whatever it was sealed with. A four-character passphrase in
// front of every secret an installation has is not a kit, it is a liability.
const minPassphraseLen = 12

// defaultRunsPerJob is how many recent successful runs are recorded per job. Runs
// are the map from "file in my bucket" to "job, and therefore passphrase"; a few
// dozen covers the retention window of a normal installation without bloating the
// document a human has to read.
const defaultRunsPerJob = 25

const usage = `Usage: recovery-kit <command> [flags]

Exports the secrets needed to open nat-backup archives without the server, sealed
with a passphrase you choose. The kit contains every backup passphrase and every
storage credential in plaintext once decrypted - treat it as the most sensitive
file in the installation.

Commands:
  export    Read the database and write a new kit.
  inspect   Decrypt a kit and print its contents (secrets masked by default).
  verify    Check that a kit is intact, decryptable and complete. Prints no secrets.

Run 'recovery-kit <command> -h' for the flags of each command.

Environment:
  %s     Passphrase for the kit. Preferred over --passphrase,
                                which is visible in the process list.
  DATABASE_URL                  PostgreSQL connection string. export only.
  ENCRYPTION_KEY                The server's 64-hex-character key. export only.

Examples:
  export DATABASE_URL=... ENCRYPTION_KEY=...
  recovery-kit export --out ~/nat-backup-kit-$(date +%%F).enc
  recovery-kit verify --file ~/nat-backup-kit-2026-08-04.enc
  recovery-kit inspect --file ~/nat-backup-kit-2026-08-04.enc --show-secrets

Recovery procedure: docs/disaster-recovery.md
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, passphraseEnvVar)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "export":
		err = runExport(os.Args[2:])
	case "inspect":
		err = runInspect(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Fprintf(os.Stdout, usage, passphraseEnvVar)
		return
	default:
		fmt.Fprintf(os.Stderr, "recovery-kit: unknown command %q\n\n", os.Args[1])
		fmt.Fprintf(os.Stderr, usage, passphraseEnvVar)
		os.Exit(2)
	}

	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "recovery-kit: %v\n", err)
		os.Exit(1)
	}
}

// ── export ────────────────────────────────────────────────────────────────────

const exportUsage = `Usage: recovery-kit export --out <file> [flags]

Reads every job, storage destination and recent run from the database, decrypts the
passphrases and credentials with ENCRYPTION_KEY, and writes them to <file> sealed
with your own passphrase.

Flags:
  --out <file>          Where to write the kit. Required. Refuses to overwrite
                        unless --force. Created with mode 0600.
  --force               Replace an existing file at --out.
  --runs-per-job <n>    Recent successful runs recorded per job (default %d).
                        0 records none.
  --passphrase <str>    Passphrase to seal the kit with. Prefer $%s:
                        a flag is visible to every user via the process list.
                        With neither, you are prompted (twice, hidden).

Environment (required):
  DATABASE_URL          PostgreSQL connection string.
  ENCRYPTION_KEY        The server's key, 64 hex characters. Same value as .env.

The kit is sealed with AES-256-GCM under a PBKDF2-SHA256 key derived from YOUR
passphrase - not from ENCRYPTION_KEY, which the kit exists to survive the loss of.
Lose the kit passphrase and the kit is as unrecoverable as the thing it protects.
`

func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintf(os.Stderr, exportUsage, defaultRunsPerJob, passphraseEnvVar) }

	out := fs.String("out", "", "path of the kit to write")
	force := fs.Bool("force", false, "overwrite an existing file")
	runsPerJob := fs.Int("runs-per-job", defaultRunsPerJob, "recent successful runs recorded per job")
	passphrase := fs.String("passphrase", "", "passphrase to seal the kit with")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *out == "" {
		fs.Usage()
		return errors.New("--out is required")
	}
	if *runsPerJob < 0 {
		return errors.New("--runs-per-job cannot be negative")
	}
	if !*force {
		if _, err := os.Stat(*out); err == nil {
			return fmt.Errorf("%s already exists; pass --force to replace it", *out)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", *out, err)
		}
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is not set")
	}
	encKey, err := readEncryptionKey()
	if err != nil {
		return err
	}

	pass, err := resolvePassphrase(*passphrase, true)
	if err != nil {
		return err
	}

	pool, err := db.Connect(databaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	src, err := loadSource(context.Background(), pool, databaseURL, *runsPerJob)
	if err != nil {
		return err
	}

	kit := buildKit(src, encKey, time.Now())

	sum, size, err := writeKit(*out, kit, pass)
	if err != nil {
		return err
	}

	printExportSummary(os.Stdout, *out, kit, sum, size)
	return nil
}

// writeKit seals the kit, proves it opens again, and only then puts it where the
// operator asked for it.
//
// The order matters. The kit is written to a temporary file in the same directory,
// re-read from disk and decrypted with the same passphrase, and renamed over the
// target only if that succeeds. A kit that cannot be opened is worse than no kit,
// because the operator believes they have one; and with --force the file being
// replaced may be the last good copy.
func writeKit(path string, k Kit, passphrase string) (checksum string, size int64, err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".recovery-kit-*.tmp")
	if err != nil {
		return "", 0, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	// Secrets never touch the disk world-readable, not even for the instant
	// between creation and chmod: CreateTemp already makes the file 0600, this
	// only pins it against a permissive umask on odd platforms.
	if err = tmp.Chmod(0o600); err != nil {
		return "", 0, fmt.Errorf("chmod %s: %w", tmpName, err)
	}

	sum := sha256.New()
	if err = seal(io.MultiWriter(tmp, sum), k, passphrase); err != nil {
		return "", 0, err
	}
	if err = tmp.Sync(); err != nil {
		return "", 0, fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err = tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("close %s: %w", tmpName, err)
	}

	fi, err := os.Stat(tmpName)
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", tmpName, err)
	}

	if err = proveReadable(tmpName, k, passphrase); err != nil {
		return "", 0, err
	}

	if err = os.Rename(tmpName, path); err != nil {
		return "", 0, fmt.Errorf("move kit into place: %w", err)
	}
	return hex.EncodeToString(sum.Sum(nil)), fi.Size(), nil
}

// proveReadable re-opens a freshly written kit and checks it decrypts to what went
// in. Verification, not optimism: this is the only moment where finding out the
// kit is broken is free.
func proveReadable(path string, want Kit, passphrase string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("reopen kit for verification: %w", err)
	}
	defer f.Close()

	got, instructions, err := open(f, passphrase)
	if err != nil {
		return fmt.Errorf("the kit just written does not open again (nothing was saved): %w", err)
	}
	if instructions == "" {
		return errors.New("the kit just written carries no instructions (nothing was saved)")
	}
	if len(got.Jobs) != len(want.Jobs) || len(got.StorageDestinations) != len(want.StorageDestinations) {
		return errors.New("the kit just written does not read back identically (nothing was saved)")
	}
	for i, j := range want.Jobs {
		if got.Jobs[i].EncryptPassphrase != j.EncryptPassphrase {
			return fmt.Errorf("passphrase of job %q does not survive the round trip (nothing was saved)", j.Name)
		}
	}
	return nil
}

func printExportSummary(w io.Writer, path string, k Kit, checksum string, size int64) {
	fmt.Fprintf(w, "\nWrote %s (%d bytes, mode 0600)\n", path, size)
	fmt.Fprintf(w, "  sha256    %s\n", checksum)
	fmt.Fprintf(w, "  contents  %d job(s), %d storage destination(s), %d run(s), %d agent(s)\n",
		len(k.Jobs), len(k.StorageDestinations), len(k.Runs), len(k.Agents))

	encrypted, withPass := 0, 0
	for _, j := range k.Jobs {
		if j.Encrypt {
			encrypted++
			if j.EncryptPassphrase != "" {
				withPass++
			}
		}
	}
	fmt.Fprintf(w, "  passphrases recovered  %d of %d encrypted job(s)\n", withPass, encrypted)
	fmt.Fprintf(w, "  verified  the kit was re-read from disk and decrypted successfully\n")

	if len(k.Problems) > 0 {
		fmt.Fprintf(w, "\n!! THIS KIT IS INCOMPLETE - %d problem(s):\n", len(k.Problems))
		problems := append([]string(nil), k.Problems...)
		sort.Strings(problems)
		for _, p := range problems {
			fmt.Fprintf(w, "   - %s\n", p)
		}
		fmt.Fprintf(w, "   Fix these and export again. What is missing above cannot be recovered later.\n")
	}

	fmt.Fprint(w, `
*** THIS FILE IS AS SENSITIVE AS EVERY BACKUP YOU HAVE ***
Decrypted, it contains every backup passphrase and every storage credential in
plaintext. Whoever holds it and its passphrase can read - and delete - every
archive you own.

DO NOT keep it on the nat-backup server (it dies with the thing it protects).
DO NOT commit it to a repository, paste it in a chat, or e-mail it.
DO NOT store it in the same S3 bucket as the backups, with the same credentials.

DO keep it in a password manager as a file attachment, or on an encrypted USB
stick in a physically separate place, or in a sealed envelope in a safe. Keep at
least two copies, in two places. Store the kit passphrase somewhere OTHER than
the kit, and make sure someone else can find both if you cannot.

Re-export whenever you add or edit a job, and verify it on a schedule:
  recovery-kit verify --file <kit>
`)
}

// ── inspect ───────────────────────────────────────────────────────────────────

const inspectUsage = `Usage: recovery-kit inspect --file <kit> [flags]

Decrypts a kit and prints it as JSON on stdout. Credentials and passphrases are
masked unless --show-secrets is given.

Flags:
  --file <path>         Kit to read. Required.
  --show-secrets        Print the passphrases and credentials in the clear.
                        Everything you need for a recovery, and everything an
                        attacker needs to take one from you.
  --instructions        Print the recovery runbook embedded in the kit instead of
                        the JSON. Never contains secrets.
  --passphrase <str>    Kit passphrase. Prefer $%s.

The output goes to stdout so it can be piped or redirected; the summary and the
warnings go to stderr, and stay out of the file you redirect into.
`

func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintf(os.Stderr, inspectUsage, passphraseEnvVar) }

	file := fs.String("file", "", "kit to read")
	showSecrets := fs.Bool("show-secrets", false, "print secrets in the clear")
	instructions := fs.Bool("instructions", false, "print the embedded runbook instead of the JSON")
	passphrase := fs.String("passphrase", "", "kit passphrase")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *file == "" {
		fs.Usage()
		return errors.New("--file is required")
	}

	kit, runbook, err := openKitFile(*file, *passphrase)
	if err != nil {
		return err
	}

	if *instructions {
		fmt.Print(runbook)
		return nil
	}

	doc := kit
	if !*showSecrets {
		doc = kit.masked()
	}
	body, err := marshalKit(doc)
	if err != nil {
		return err
	}

	if *showSecrets {
		fmt.Fprintf(os.Stderr,
			"!! PRINTING SECRETS IN THE CLEAR: every backup passphrase and storage credential\n"+
				"   of this installation is in the output below. Do not leave it on screen, in a\n"+
				"   scrollback buffer, or in a file you forget to delete.\n\n")
	} else {
		fmt.Fprintf(os.Stderr,
			"Secrets are masked. Re-run with --show-secrets to print them.\n\n")
	}

	fmt.Println(string(body))

	fmt.Fprintf(os.Stderr, "\nExported %s - %d job(s), %d destination(s), %d run(s)\n",
		kit.ExportedAt.Format(time.RFC3339), len(kit.Jobs), len(kit.StorageDestinations), len(kit.Runs))
	if len(kit.Problems) > 0 {
		fmt.Fprintf(os.Stderr, "!! %d problem(s) recorded at export time; see \"problems\" above.\n", len(kit.Problems))
	}
	return nil
}

// ── verify ────────────────────────────────────────────────────────────────────

const verifyUsage = `Usage: recovery-kit verify --file <kit> [flags]

Checks that a kit is intact, decryptable and complete enough to recover with. Prints
counts and check results only - never a passphrase or a credential - so it is safe
to run in front of other people or from cron.

Flags:
  --file <path>         Kit to check. Required.
  --passphrase <str>    Kit passphrase. Prefer $%s.

Exit status: 0 if every check passed (warnings included), 1 if any check FAILED or
the kit could not be decrypted. Run it on a schedule: a kit nobody has opened since
the day it was made is a kit nobody knows still works.
`

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintf(os.Stderr, verifyUsage, passphraseEnvVar) }

	file := fs.String("file", "", "kit to check")
	passphrase := fs.String("passphrase", "", "kit passphrase")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *file == "" {
		fs.Usage()
		return errors.New("--file is required")
	}

	sum, size, err := fileDigest(*file)
	if err != nil {
		return err
	}

	kit, runbook, err := openKitFile(*file, *passphrase)
	if err != nil {
		return err
	}

	fmt.Printf("%s\n", *file)
	fmt.Printf("  sha256 %s (%d bytes)\n\n", sum, size)

	checks := checkKit(kit, time.Now())
	if runbook == "" {
		checks = append(checks, check{checkFail, "embedded instructions", "missing"})
	} else {
		checks = append(checks, check{checkPass, "embedded instructions",
			fmt.Sprintf("%d bytes", len(runbook))})
	}

	for _, c := range checks {
		fmt.Printf("  %-4s %-26s %s\n", c.Level, c.Name, c.Detail)
	}

	switch worstLevel(checks) {
	case checkFail:
		fmt.Printf("\nFAILED. This kit is not something to rely on - export a new one.\n")
		return errors.New("verification failed")
	case checkWarn:
		fmt.Printf("\nOK, with warnings. The kit decrypts and its contents are consistent.\n")
	default:
		fmt.Printf("\nOK. The kit decrypts and its contents are consistent.\n")
	}
	return nil
}

// ── shared plumbing ───────────────────────────────────────────────────────────

func openKitFile(path, passphraseFlag string) (Kit, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return Kit{}, "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	pass, err := resolvePassphrase(passphraseFlag, false)
	if err != nil {
		return Kit{}, "", err
	}
	return open(f, pass)
}

func marshalKit(k Kit) ([]byte, error) {
	body, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode kit: %w", err)
	}
	return body, nil
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// readEncryptionKey validates the key the same way internal/config does, before
// anything connects to the database: a bad key found halfway through is a bad key
// found too late.
func readEncryptionKey() (string, error) {
	v := os.Getenv("ENCRYPTION_KEY")
	if v == "" {
		return "", errors.New("ENCRYPTION_KEY is not set (it is the key the stored secrets are sealed with)")
	}
	if len(v) != 64 {
		return "", fmt.Errorf("ENCRYPTION_KEY must be 64 hex characters (32 bytes), got %d", len(v))
	}
	if _, err := hex.DecodeString(v); err != nil {
		return "", fmt.Errorf("ENCRYPTION_KEY must be valid hex: %w", err)
	}
	return v, nil
}

// resolvePassphrase reads the passphrase that seals the kit.
//
// The environment variable is preferred over the flag because a flag is visible in
// `ps` to every user on the host and is written to the shell history. confirm is
// set when creating a kit: a typo in a passphrase nobody has typed twice is only
// discovered on the day it matters.
func resolvePassphrase(flagValue string, confirm bool) (string, error) {
	if flagValue != "" {
		return checkPassphrase(flagValue, confirm)
	}
	if v := os.Getenv(passphraseEnvVar); v != "" {
		return checkPassphrase(v, confirm)
	}

	pass, err := promptPassphrase("Passphrase for the recovery kit: ")
	if err != nil {
		return "", err
	}
	if pass == "" {
		return "", fmt.Errorf("no passphrase given: use --passphrase or set %s", passphraseEnvVar)
	}
	if confirm {
		again, err := promptPassphrase("Repeat it: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("the two passphrases do not match")
		}
	}
	return checkPassphrase(pass, confirm)
}

// checkPassphrase enforces a minimum length when creating a kit and never when
// opening one: an existing kit must open with whatever it was sealed with.
func checkPassphrase(pass string, creating bool) (string, error) {
	if pass == "" {
		return "", fmt.Errorf("passphrase is empty; use --passphrase or set %s", passphraseEnvVar)
	}
	if creating && len([]rune(pass)) < minPassphraseLen {
		return "", fmt.Errorf(
			"the kit passphrase must be at least %d characters (got %d): it is the only thing "+
				"standing between this file and every secret of the installation",
			minPassphraseLen, len([]rune(pass)))
	}
	return pass, nil
}

func promptPassphrase(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read passphrase: %w", err)
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	}

	// Piped stdin: nothing to hide, and no terminal to hide it on.
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ── database ──────────────────────────────────────────────────────────────────

const (
	agentsQuery = `SELECT id, name, os, arch, version, status, created_at
	               FROM agents ORDER BY name, id`

	destinationsQuery = `SELECT id, name, type, config, created_at
	                     FROM storage_destinations ORDER BY name, id`

	jobsQuery = `SELECT id, agent_id, name, enabled, type, source, storage_destination_id,
	                    schedule_cron, retention_days, compression, encrypt, encrypt_passphrase,
	                    created_at, updated_at
	             FROM backup_jobs ORDER BY name, id`

	// Only successful runs that actually produced a file: those are the ones that
	// map a name in a bucket to a job, and therefore to a passphrase. A failed run
	// has nothing to recover.
	runsQuery = `SELECT id, job_id, started_at, finished_at, status, size_bytes, file_count,
	                    storage_path, checksum
	             FROM (
	               SELECT r.*, ROW_NUMBER() OVER (
	                        PARTITION BY r.job_id ORDER BY r.started_at DESC, r.id DESC
	                      ) AS rn
	               FROM backup_runs r
	               WHERE r.status = 'success'
	                 AND r.storage_path IS NOT NULL
	                 AND r.storage_path <> ''
	             ) ranked
	             WHERE rn <= $1
	             ORDER BY job_id, started_at DESC`
)

// loadSource reads every row the kit is built from. Explicit column lists rather
// than SELECT *: a column added by a later migration must not change what this
// tool exports without someone deciding it should.
func loadSource(ctx context.Context, pool *sqlx.DB, databaseURL string, runsPerJob int) (exportSource, error) {
	src := exportSource{
		Database:   redactDatabaseURL(databaseURL),
		Hostname:   hostname(),
		RunsPerJob: runsPerJob,
	}

	if err := pool.SelectContext(ctx, &src.Agents, agentsQuery); err != nil {
		return src, fmt.Errorf("read agents: %w", err)
	}
	if err := pool.SelectContext(ctx, &src.Destinations, destinationsQuery); err != nil {
		return src, fmt.Errorf("read storage destinations: %w", err)
	}
	if err := pool.SelectContext(ctx, &src.Jobs, jobsQuery); err != nil {
		return src, fmt.Errorf("read backup jobs: %w", err)
	}
	if runsPerJob > 0 {
		if err := pool.SelectContext(ctx, &src.Runs, runsQuery, runsPerJob); err != nil {
			return src, fmt.Errorf("read backup runs: %w", err)
		}
	}

	// A dirty or unknown schema version is recorded, not fatal: the secrets are
	// still worth exporting, and the kit says what it was taken from.
	if v, dirty, err := db.MigrationVersion(databaseURL); err == nil {
		src.SchemaVersion, src.SchemaDirty = v, dirty
	}

	return src, nil
}

// redactDatabaseURL keeps enough of the connection string to tell two
// installations apart and drops everything that could be a credential - the
// userinfo, and the query string, where libpq accepts `password=`.
func redactDatabaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparsed connection string, redacted)"
	}
	return fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, u.Path)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
