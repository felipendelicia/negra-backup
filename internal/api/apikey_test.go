// internal/api/apikey_test.go
package api

import (
	"strings"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// The generated key must round-trip through the parser, and only its secret half
// may be hashed: hashing the whole key would make the identifier unusable for the
// indexed lookup that replaced the full-table bcrypt scan.
func TestGenerateAPIKey(t *testing.T) {
	plain, keyID, hash, err := generateAPIKey()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(plain, models.APIKeyPrefix))

	parsedID, secret, ok := models.ParseAPIKey(plain)
	require.True(t, ok, "generated key must be parseable")
	assert.Equal(t, keyID, parsedID)

	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)),
		"hash must verify against the secret half")
	assert.Error(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)),
		"hash must not cover the whole key, only the secret")
}

func TestGenerateAPIKey_IsUnique(t *testing.T) {
	seenIDs := make(map[string]bool)
	seenKeys := make(map[string]bool)
	for i := 0; i < 50; i++ {
		plain, keyID, _, err := generateAPIKey()
		require.NoError(t, err)
		assert.False(t, seenIDs[keyID], "duplicate key id")
		assert.False(t, seenKeys[plain], "duplicate key")
		seenIDs[keyID] = true
		seenKeys[plain] = true
	}
}
