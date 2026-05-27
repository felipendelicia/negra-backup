package storage

import (
	"fmt"
	"io"
	"net"
	"path/filepath"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPBackend uploads files to a remote server via SFTP.
type SFTPBackend struct {
	cfg models.SFTPStorageConfig
}

func NewSFTPBackend(cfg models.SFTPStorageConfig) (*SFTPBackend, error) {
	return &SFTPBackend{cfg: cfg}, nil
}

func (b *SFTPBackend) Upload(filename string, r io.Reader, size int64) error {
	addr := fmt.Sprintf("%s:%d", b.cfg.Host, b.cfg.Port)

	var authMethods []ssh.AuthMethod
	if b.cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(b.cfg.PrivateKey))
		if err != nil {
			return fmt.Errorf("parse private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if b.cfg.Password != "" {
		authMethods = append(authMethods, ssh.Password(b.cfg.Password))
	}

	sshCfg := &ssh.ClientConfig{
		User:            b.cfg.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec — known hosts support is a TODO
		Timeout:         30e9,
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, sshCfg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("ssh handshake: %w", err)
	}

	sshClient := ssh.NewClient(sshConn, chans, reqs)
	defer sshClient.Close()

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return fmt.Errorf("sftp client: %w", err)
	}
	defer sftpClient.Close()

	remotePath := filepath.Join(b.cfg.Path, filename)
	sftpClient.MkdirAll(filepath.Dir(remotePath))

	dst, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("sftp create %s: %w", remotePath, err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, r); err != nil {
		return fmt.Errorf("sftp write: %w", err)
	}

	return nil
}
