package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/felipendelicia/nat-backup/internal/storage"
)

// passphraseEnvVar is the preferred way to supply a passphrase non-interactively:
// unlike --passphrase it does not show up in the process list or the shell history.
const passphraseEnvVar = "NAT_BACKUP_PASSPHRASE"

const restoreUsage = `Usage: nat-backup-agent restore [flags]

Restores a backup archive produced by this agent. Encryption and compression are
detected from the file itself, so the archive can be recovered with nothing but the
file and the passphrase — no server and no database required.

Source (exactly one):
  --file <path>              Restore from a local archive file.
  --from-storage <spec>      Download from a storage destination first (needs --name).
                             <spec> is either inline JSON or the path to a JSON file:
                               {"type":"s3","config":{"bucket":"...","region":"...",
                                "access_key":"...","secret_key":"...","prefix":"..."}}
                             type is one of: local, s3, sftp. config matches the same
                             shape the server stores for that destination.
  --name <filename>          Archive name inside the storage destination.

Flags:
  --dest <dir>               Destination directory (required).
  --passphrase <str>         Passphrase for encrypted archives. Prefer %s
                             in the environment: flags are visible to other users via
                             the process list.
  --dry-run                  List what would be restored without writing anything.

Passphrase resolution order: --passphrase, then $%s, then an
interactive prompt. NOTE: the prompt echoes what you type, so on a shared screen use
the environment variable instead.

Examples:
  nat-backup-agent restore --file backup.tar.zst.enc --dest /tmp/out --passphrase secret
  NAT_BACKUP_PASSPHRASE=secret nat-backup-agent restore --file b.tar.zst.enc --dest /tmp/out
  nat-backup-agent restore --file backup.tar.gz --dest /tmp/out --dry-run
  nat-backup-agent restore --from-storage ./s3.json --name job-run.tar.zst.enc --dest /srv/restore
`

// storageSpec is the JSON accepted by --from-storage. It mirrors what the server
// keeps for a storage destination, so an operator can paste that config straight in.
type storageSpec struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, restoreUsage, passphraseEnvVar, passphraseEnvVar)
	}

	file := fs.String("file", "", "path to a local archive file")
	fromStorage := fs.String("from-storage", "", "storage destination spec (inline JSON or path to a JSON file)")
	name := fs.String("name", "", "archive filename inside the storage destination")
	dest := fs.String("dest", "", "destination directory")
	passphrase := fs.String("passphrase", "", "passphrase for encrypted archives")
	dryRun := fs.Bool("dry-run", false, "list contents without writing anything")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *dest == "" {
		fs.Usage()
		return errors.New("--dest is required")
	}
	switch {
	case *file == "" && *fromStorage == "":
		fs.Usage()
		return errors.New("one of --file or --from-storage is required")
	case *file != "" && *fromStorage != "":
		return errors.New("--file and --from-storage are mutually exclusive")
	case *fromStorage != "" && *name == "":
		return errors.New("--from-storage requires --name")
	}

	src, label, err := openArchive(*file, *fromStorage, *name)
	if err != nil {
		return err
	}
	defer src.Close()

	// Peeking before the restore lets us prompt only when the archive really is
	// encrypted, and the buffered reader is then handed to Restore unchanged so no
	// bytes are lost — the storage readers are not seekable.
	br := bufio.NewReader(src)
	head, err := br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read %s: %w", label, err)
	}

	pass := *passphrase
	if backup.IsEncrypted(head) && pass == "" {
		pass, err = resolvePassphrase()
		if err != nil {
			return err
		}
	}

	opts := backup.RestoreOptions{
		DryRun: *dryRun,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
		},
	}
	if *dryRun {
		opts.OnEntry = func(e backup.RestoreEntry) {
			fmt.Printf("%s %10d  %s%s\n", e.Mode, e.Size, e.Name, linkSuffix(e))
		}
	}

	fmt.Printf("restoring %s -> %s\n", label, *dest)
	res, err := backup.RestoreWithOptions(br, *dest, pass, opts)
	if err != nil {
		return err
	}

	verb := "restored"
	if *dryRun {
		verb = "would restore"
	}
	fmt.Printf("%s %d files (%d dirs, %d bytes) — %s%s\n",
		verb, res.FileCount, res.DirCount, res.TotalBytes,
		res.Compression, encryptedSuffix(res.Encrypted))
	if res.Skipped > 0 {
		fmt.Printf("skipped %d unsupported entries\n", res.Skipped)
	}
	return nil
}

func linkSuffix(e backup.RestoreEntry) string {
	if e.Linkname == "" {
		return ""
	}
	return " -> " + e.Linkname
}

func encryptedSuffix(encrypted bool) string {
	if encrypted {
		return ", encrypted"
	}
	return ", not encrypted"
}

// openArchive resolves the archive source into a stream plus a label for messages.
func openArchive(file, fromStorage, name string) (io.ReadCloser, string, error) {
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return nil, "", fmt.Errorf("open %s: %w", file, err)
		}
		return f, file, nil
	}

	spec, err := loadStorageSpec(fromStorage)
	if err != nil {
		return nil, "", err
	}
	backend, err := storage.NewBackend(spec.Type, spec.Config, "", "", "")
	if err != nil {
		return nil, "", fmt.Errorf("storage backend: %w", err)
	}
	rc, err := backend.Download(name)
	if err != nil {
		return nil, "", fmt.Errorf("download %s: %w", name, err)
	}
	return rc, fmt.Sprintf("%s:%s", spec.Type, name), nil
}

// loadStorageSpec accepts either inline JSON or a path to a JSON file.
func loadStorageSpec(v string) (storageSpec, error) {
	var spec storageSpec

	raw := []byte(v)
	if !strings.HasPrefix(strings.TrimSpace(v), "{") {
		b, err := os.ReadFile(v)
		if err != nil {
			return spec, fmt.Errorf("read storage spec %s: %w", v, err)
		}
		raw = b
	}

	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, fmt.Errorf("parse storage spec: %w", err)
	}
	if spec.Type == "" {
		return spec, errors.New(`storage spec: "type" is required (local, s3 or sftp)`)
	}
	if len(spec.Config) == 0 {
		return spec, errors.New(`storage spec: "config" is required`)
	}
	return spec, nil
}

// resolvePassphrase reads the passphrase for an encrypted archive.
//
// The environment variable comes first because it keeps the secret out of the
// process list. The interactive prompt echoes the input: turning the terminal echo
// off needs golang.org/x/term, which is not a declared dependency of this module.
func resolvePassphrase() (string, error) {
	if v := os.Getenv(passphraseEnvVar); v != "" {
		return v, nil
	}

	fmt.Fprintf(os.Stderr,
		"archive is encrypted. Enter passphrase (it will be visible; set %s to avoid this): ",
		passphraseEnvVar)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	pass := strings.TrimRight(line, "\r\n")
	if pass == "" {
		return "", fmt.Errorf("archive is encrypted: provide --passphrase or set %s", passphraseEnvVar)
	}
	return pass, nil
}
