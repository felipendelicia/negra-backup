package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

const (
	saltSize   = 32
	chunkSize  = 64 * 1024 // 64KB
	pbkdf2Iter = 600_000
)

// deriveKey derives a 32-byte AES key from passphrase + salt using PBKDF2-SHA256.
func deriveKey(passphrase string, salt []byte) []byte {
	return pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iter, 32, sha256.New)
}

// EncryptStream reads from r, encrypts with passphrase using chunk-based AES-256-GCM, writes to w.
func EncryptStream(r io.Reader, w io.Writer, passphrase string) error {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("rand salt: %w", err)
	}
	if _, err := w.Write(salt); err != nil {
		return fmt.Errorf("write salt: %w", err)
	}

	key := deriveKey(passphrase, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("new gcm: %w", err)
	}

	buf := make([]byte, chunkSize)
	nonce := make([]byte, gcm.NonceSize())

	for {
		n, readErr := io.ReadFull(r, buf)
		if n == 0 && readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return fmt.Errorf("read chunk: %w", readErr)
		}
		if n == 0 {
			break
		}

		if _, rerr := rand.Read(nonce); rerr != nil {
			return fmt.Errorf("rand nonce: %w", rerr)
		}

		ciphertext := gcm.Seal(nonce, nonce, buf[:n], nil)

		lenBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBuf, uint32(len(ciphertext)))
		if _, werr := w.Write(lenBuf); werr != nil {
			return fmt.Errorf("write chunk len: %w", werr)
		}
		if _, werr := w.Write(ciphertext); werr != nil {
			return fmt.Errorf("write chunk: %w", werr)
		}

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	return nil
}

// DecryptStream reads encrypted data from r, decrypts with passphrase, writes plaintext to w.
func DecryptStream(r io.Reader, w io.Writer, passphrase string) error {
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(r, salt); err != nil {
		return fmt.Errorf("read salt: %w", err)
	}

	key := deriveKey(passphrase, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("new gcm: %w", err)
	}

	lenBuf := make([]byte, 4)
	for {
		_, err := io.ReadFull(r, lenBuf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read chunk len: %w", err)
		}

		chunkLen := binary.BigEndian.Uint32(lenBuf)
		chunk := make([]byte, chunkLen)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return fmt.Errorf("read chunk: %w", err)
		}

		nonceSize := gcm.NonceSize()
		if len(chunk) < nonceSize {
			return fmt.Errorf("chunk too small")
		}
		nonce, ciphertext := chunk[:nonceSize], chunk[nonceSize:]

		plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
		if err != nil {
			return fmt.Errorf("decrypt chunk: %w", err)
		}

		if _, err := w.Write(plaintext); err != nil {
			return fmt.Errorf("write plaintext: %w", err)
		}
	}

	return nil
}
