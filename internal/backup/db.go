package backup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// DBDumper runs a database dump and writes output to an io.Writer.
type DBDumper struct {
	dbType     string
	connString string
}

// NewDBDumper creates a DBDumper for the given database type and connection string.
func NewDBDumper(dbType, connString string) *DBDumper {
	return &DBDumper{dbType: dbType, connString: connString}
}

// DumpResult contains metadata from a DB dump.
type DumpResult struct {
	SizeBytes int64
}

// Dump runs the appropriate dump tool and writes output to w.
func (d *DBDumper) Dump(w io.Writer) (DumpResult, error) {
	switch d.dbType {
	case "postgres":
		return d.dumpPostgres(w)
	case "mysql":
		return d.dumpMySQL(w)
	case "sqlite":
		return d.dumpSQLite(w)
	case "mongodb":
		return d.dumpMongoDB(w)
	default:
		return DumpResult{}, fmt.Errorf("unknown db type: %s", d.dbType)
	}
}

func (d *DBDumper) dumpPostgres(w io.Writer) (DumpResult, error) {
	cmd := exec.Command("pg_dump", "--no-password", "--", d.connString)
	cmd.Stdout = w
	errPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return DumpResult{}, fmt.Errorf("pg_dump start: %w", err)
	}

	errBuf, _ := io.ReadAll(errPipe)
	if err := cmd.Wait(); err != nil {
		return DumpResult{}, fmt.Errorf("pg_dump: %w — %s", err, errBuf)
	}
	return DumpResult{}, nil
}

func (d *DBDumper) dumpMySQL(w io.Writer) (DumpResult, error) {
	cmd := exec.Command("mysqldump", "--single-transaction", "--routines", "--triggers",
		d.connString)
	cmd.Stdout = w
	errPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return DumpResult{}, fmt.Errorf("mysqldump start: %w", err)
	}
	errBuf, _ := io.ReadAll(errPipe)
	if err := cmd.Wait(); err != nil {
		return DumpResult{}, fmt.Errorf("mysqldump: %w — %s", err, errBuf)
	}
	return DumpResult{}, nil
}

func (d *DBDumper) dumpSQLite(w io.Writer) (DumpResult, error) {
	// Reject suspiciously flag-like paths
	if strings.HasPrefix(d.connString, "-") {
		return DumpResult{}, fmt.Errorf("invalid sqlite path: %q", d.connString)
	}
	exec.Command("sqlite3", d.connString, "PRAGMA wal_checkpoint(FULL)").Run() // best-effort; ignore error

	f, err := os.Open(d.connString)
	if err != nil {
		return DumpResult{}, fmt.Errorf("open sqlite: %w", err)
	}
	defer f.Close()

	n, err := io.Copy(w, f)
	if err != nil {
		return DumpResult{}, fmt.Errorf("copy sqlite: %w", err)
	}
	return DumpResult{SizeBytes: n}, nil
}

func (d *DBDumper) dumpMongoDB(w io.Writer) (DumpResult, error) {
	cmd := exec.Command("mongodump", "--uri", d.connString, "--archive")
	cmd.Stdout = w
	errPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return DumpResult{}, fmt.Errorf("mongodump start: %w", err)
	}
	errBuf, _ := io.ReadAll(errPipe)
	if err := cmd.Wait(); err != nil {
		return DumpResult{}, fmt.Errorf("mongodump: %w — %s", err, errBuf)
	}
	return DumpResult{}, nil
}
