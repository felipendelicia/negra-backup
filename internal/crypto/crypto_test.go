// internal/crypto/crypto_test.go
package crypto_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	plaintext := "super-secret-connection-string"

	ciphertext, err := crypto.Encrypt(key, plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext)

	decrypted, err := crypto.Decrypt(key, ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncrypt_DifferentEachTime(t *testing.T) {
	key := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	c1, err := crypto.Encrypt(key, "hello")
	require.NoError(t, err)
	c2, err := crypto.Encrypt(key, "hello")
	require.NoError(t, err)
	assert.NotEqual(t, c1, c2)
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1 := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	key2 := "aabbccddeeff00112233445566778899aabbccddeeff001122334455667788aa"

	ciphertext, err := crypto.Encrypt(key1, "secret")
	require.NoError(t, err)

	_, err = crypto.Decrypt(key2, ciphertext)
	require.Error(t, err)
}

func TestDecrypt_InvalidBase64(t *testing.T) {
	key := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	_, err := crypto.Decrypt(key, "not-base64!!!")
	require.Error(t, err)
}
