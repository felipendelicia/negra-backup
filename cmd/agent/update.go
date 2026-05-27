package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

const githubReleasesBase = "https://github.com/felipendelicia/negra-backup/releases/latest/download"

// selfUpdate downloads the latest agent binary from GitHub Releases,
// replaces the current executable, and re-execs into it.
func selfUpdate() error {
	binaryName := fmt.Sprintf("nat-backup-agent-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	url := githubReleasesBase + "/" + binaryName

	log.Printf("self-update: downloading %s", url)

	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("eval symlinks: %w", err)
	}

	tmpPath := exePath + ".update"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write: %w", err)
	}
	f.Close()

	// Atomically replace: rename over the current binary.
	// On Linux this works even while the old binary is running.
	if err := os.Rename(tmpPath, exePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace binary: %w", err)
	}

	log.Printf("self-update: replaced %s — restarting", exePath)

	// Re-exec: replaces current process image with new binary.
	// syscall.Exec never returns on success.
	if err := syscall.Exec(exePath, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	return nil // unreachable
}
