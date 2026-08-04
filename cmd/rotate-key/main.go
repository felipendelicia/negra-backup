// cmd/rotate-key/main.go
//
// rotate-key re-encrypts every secret nat-backup stores at rest from one
// ENCRYPTION_KEY to another.
//
// ENCRYPTION_KEY is not an ordinary setting: it is the AES-256-GCM key that
// storage credentials, backup passphrases and SMTP credentials are sealed with.
// Editing it in .env does not rotate anything — it makes the existing rows
// unreadable and the installation stops working. This tool does the only thing
// that actually rotates the key: decrypt with the old one, re-encrypt with the
// new one, in a single transaction, and only after proving every value survives
// the trip.
//
// Run it with the server stopped and a fresh database backup. See
// docs/key-rotation.md.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/felipendelicia/nat-backup/internal/db"
)

const usage = `Usage: rotate-key [--commit] [--skip-undecryptable]

Re-encrypts every value stored with ENCRYPTION_KEY from the old key to the new one.

Environment (required):
  DATABASE_URL          PostgreSQL connection string.
  OLD_ENCRYPTION_KEY    The key currently in .env. 64 hex characters.
  NEW_ENCRYPTION_KEY    The key you are moving to. 64 hex characters.
                        Generate one with: openssl rand -hex 32

The keys are read from the environment on purpose. A command-line flag would be
visible in 'ps' to every user on the machine and would be written to your shell
history.

Flags:
  --dry-run              Report what would change and write nothing. THE DEFAULT.
  --commit               Actually write. Required for any change to be made.
  --skip-undecryptable   Leave values that decrypt with neither key untouched
                         instead of aborting. Use only after confirming the old
                         key is correct: those rows are already unreadable.

Stop the server and back up the database before running with --commit.
Full procedure: docs/key-rotation.md
`

func main() {
	commit := flag.Bool("commit", false, "write the re-encrypted values (default: dry run)")
	dryRun := flag.Bool("dry-run", true, "report only, write nothing")
	skipUndecryptable := flag.Bool("skip-undecryptable", false, "leave unreadable values untouched instead of aborting")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	if flag.NArg() > 0 {
		fatalf("unexpected argument %q\n\n%s", flag.Arg(0), usage)
	}

	dryRunSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "dry-run" {
			dryRunSet = true
		}
	})
	if *commit && dryRunSet && *dryRun {
		fatalf("--commit and --dry-run contradict each other; pass only one")
	}
	// Writing requires --commit and nothing else. '--dry-run=false' on its own is
	// not enough: destroying data must never be the consequence of unsetting
	// something.
	write := *commit

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fatalf("DATABASE_URL is not set")
	}
	oldKey, err := readKey("OLD_ENCRYPTION_KEY")
	if err != nil {
		fatalf("%v", err)
	}
	newKey, err := readKey("NEW_ENCRYPTION_KEY")
	if err != nil {
		fatalf("%v", err)
	}
	if oldKey == newKey {
		fatalf("OLD_ENCRYPTION_KEY and NEW_ENCRYPTION_KEY are identical; there is nothing to rotate")
	}

	pool, err := db.Connect(databaseURL)
	if err != nil {
		fatalf("database: %v", err)
	}
	defer pool.Close()

	out := os.Stdout
	fmt.Fprintf(out, "nat-backup encryption key rotation\n")
	if write {
		fmt.Fprintf(out, "mode: COMMIT — changes will be written\n")
	} else {
		fmt.Fprintf(out, "mode: DRY RUN — nothing will be written\n")
	}

	opts := options{
		OldKey:            oldKey,
		NewKey:            newKey,
		Commit:            write,
		SkipUndecryptable: *skipUndecryptable,
	}
	written, err := rotate(context.Background(), pool, opts, out)
	if err != nil {
		fatalf("rotation aborted, database unchanged: %v", err)
	}

	printReminders(out, written > 0)
}

// readKey validates the same way internal/config does, before anything touches
// the database: a bad key found halfway through is a bad key found too late.
func readKey(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s is not set", name)
	}
	if len(v) != 64 {
		return "", fmt.Errorf("%s must be 64 hex characters (32 bytes), got %d", name, len(v))
	}
	if _, err := hex.DecodeString(v); err != nil {
		return "", fmt.Errorf("%s must be valid hex: %w", name, err)
	}
	return v, nil
}

// printReminders closes every run with what the tool did not do. changedData is
// false for a dry run and for a run that found nothing to rotate: telling an
// operator to swap the key in .env when the database was never touched would
// break the installation.
func printReminders(out *os.File, changedData bool) {
	if changedData {
		fmt.Fprint(out, `
NEXT STEP — the database and .env now disagree:
  Set ENCRYPTION_KEY in .env to the value of NEW_ENCRYPTION_KEY, then start the
  server. Until you do, every job dispatch and every notification will fail.
`)
	}

	fmt.Fprint(out, `
THIS TOOL DOES NOT ROTATE:
  JWT_SECRET      Change it in .env and restart. No data migration is needed;
                  every browser session is signed out and has to log in again.
  ADMIN_PASSWORD  Change it in .env, then delete the stored user so the server
                  recreates it — the startup INSERT is ON CONFLICT DO NOTHING,
                  so editing the variable alone changes nothing:
                    DELETE FROM admin_users WHERE username = 'admin';
                  and restart the server.

NOT AFFECTED:
  Agent API keys are bcrypt hashes in agents.api_key, not encrypted with
  ENCRYPTION_KEY. Rotating it leaves every agent connected and working.
  backup_jobs.source (database connection strings) is stored in plaintext and is
  likewise untouched — rotating this key does not protect it.
`)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "rotate-key: "+format+"\n", args...)
	os.Exit(1)
}
