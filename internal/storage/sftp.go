package storage

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SFTPBackend uploads files to a remote server via SFTP.
type SFTPBackend struct {
	cfg models.SFTPStorageConfig
}

func NewSFTPBackend(cfg models.SFTPStorageConfig) (*SFTPBackend, error) {
	return &SFTPBackend{cfg: cfg}, nil
}

// hostKeyCallback pins the server identity. This used to be
// ssh.InsecureIgnoreHostKey(), which accepts any host key and makes every backup
// upload trivially interceptable. Prefer the key pinned in the destination config;
// otherwise fall back to the agent's known_hosts. Never accept an unknown host.
func (b *SFTPBackend) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if b.cfg.HostKey != "" {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(b.cfg.HostKey))
		if err != nil {
			return nil, fmt.Errorf("parse host_key: %w", err)
		}
		return ssh.FixedHostKey(pub), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("no host_key configured and cannot locate known_hosts: %w", err)
	}
	khPath := path.Join(home, ".ssh", "known_hosts")
	if _, err := os.Stat(khPath); err != nil {
		return nil, fmt.Errorf(
			"no host_key configured and %s is unreadable: set host_key on the storage destination", khPath)
	}
	cb, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", khPath, err)
	}
	return cb, nil
}

// connect dials the server and returns a live sftp client plus a cleanup func.
func (b *SFTPBackend) connect() (*sftp.Client, func(), error) {
	addr := net.JoinHostPort(b.cfg.Host, fmt.Sprintf("%d", b.cfg.Port))

	var authMethods []ssh.AuthMethod
	if b.cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(b.cfg.PrivateKey))
		if err != nil {
			return nil, nil, fmt.Errorf("parse private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if b.cfg.Password != "" {
		authMethods = append(authMethods, ssh.Password(b.cfg.Password))
	}
	if len(authMethods) == 0 {
		return nil, nil, fmt.Errorf("sftp: no password or private key configured")
	}

	hostKeyCB, err := b.hostKeyCallback()
	if err != nil {
		return nil, nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User:            b.cfg.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCB,
		Timeout:         30 * time.Second,
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, sshCfg)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("ssh handshake: %w", err)
	}
	sshClient := ssh.NewClient(sshConn, chans, reqs)

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, nil, fmt.Errorf("sftp client: %w", err)
	}

	return sftpClient, func() {
		sftpClient.Close()
		sshClient.Close()
	}, nil
}

func (b *SFTPBackend) Upload(filename string, r io.Reader, size int64) error {
	client, cleanup, err := b.connect()
	if err != nil {
		return err
	}
	defer cleanup()

	// Use path.Join for POSIX-style remote paths (cross-platform safe)
	remotePath := path.Join(b.cfg.Path, filename)
	if err := client.MkdirAll(path.Dir(remotePath)); err != nil {
		// Non-fatal if dir already exists; log but proceed
		log.Printf("sftp mkdir %s: %v", path.Dir(remotePath), err)
	}

	// Upload to a temp name and rename, so an interrupted transfer never leaves a
	// truncated file sitting under the final name looking like a valid backup.
	tmpPath := remotePath + ".partial"
	dst, err := client.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("sftp create %s: %w", tmpPath, err)
	}
	if _, err := io.Copy(dst, r); err != nil {
		dst.Close()
		client.Remove(tmpPath)
		return fmt.Errorf("sftp write: %w", err)
	}
	// Close flushes; discarding its error reports a truncated upload as success.
	if err := dst.Close(); err != nil {
		client.Remove(tmpPath)
		return fmt.Errorf("sftp close %s: %w", tmpPath, err)
	}
	if err := client.Chmod(tmpPath, 0o600); err != nil {
		log.Printf("sftp chmod %s: %v", tmpPath, err)
	}
	if err := client.Rename(tmpPath, remotePath); err != nil {
		client.Remove(tmpPath)
		return fmt.Errorf("sftp rename into %s: %w", remotePath, err)
	}

	return nil
}

// sftpReadCloser keeps the ssh/sftp connection alive for as long as the caller is
// reading, and tears it down on Close.
type sftpReadCloser struct {
	io.ReadCloser
	cleanup func()
}

func (r *sftpReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cleanup()
	return err
}

func (b *SFTPBackend) Download(filename string) (io.ReadCloser, error) {
	client, cleanup, err := b.connect()
	if err != nil {
		return nil, err
	}

	remotePath := path.Join(b.cfg.Path, filename)
	f, err := client.Open(remotePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("sftp open %s: %w", remotePath, err)
	}
	return &sftpReadCloser{ReadCloser: f, cleanup: cleanup}, nil
}

func (b *SFTPBackend) Delete(filename string) error {
	client, cleanup, err := b.connect()
	if err != nil {
		return err
	}
	defer cleanup()

	remotePath := path.Join(b.cfg.Path, filename)
	if err := client.Remove(remotePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("sftp delete %s: %w", remotePath, err)
	}
	return nil
}
