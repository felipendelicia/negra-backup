package backup_test

import (
	"bytes"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecryptStream_RoundTrip(t *testing.T) {
	passphrase := "test-passphrase-for-backup"
	plaintext := []byte("this is a test backup content that is long enough to matter")

	var encrypted bytes.Buffer
	err := backup.EncryptStream(bytes.NewReader(plaintext), &encrypted, passphrase)
	require.NoError(t, err)

	assert.NotEqual(t, plaintext, encrypted.Bytes())

	var decrypted bytes.Buffer
	err = backup.DecryptStream(&encrypted, &decrypted, passphrase)
	require.NoError(t, err)

	assert.Equal(t, plaintext, decrypted.Bytes())
}

func TestEncryptStream_WrongPassphrase(t *testing.T) {
	plaintext := []byte("secret data")

	var encrypted bytes.Buffer
	require.NoError(t, backup.EncryptStream(bytes.NewReader(plaintext), &encrypted, "correct-pass"))

	var decrypted bytes.Buffer
	err := backup.DecryptStream(&encrypted, &decrypted, "wrong-pass")
	require.Error(t, err)
}

func TestEncryptStream_EmptyInput(t *testing.T) {
	var encrypted bytes.Buffer
	err := backup.EncryptStream(bytes.NewReader([]byte{}), &encrypted, "pass")
	require.NoError(t, err)
}
