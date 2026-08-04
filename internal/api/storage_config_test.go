// internal/api/storage_config_test.go
package api

import (
	"encoding/json"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the wire contract between the web UI and the Go models. The UI
// used to send access_key_id/secret_access_key for S3 and username/string-port for
// SFTP; Go discards unknown keys, so both destinations saved successfully and then
// failed on every upload with empty credentials.
func TestValidateStorageConfig_RejectsLegacyUIFieldNames(t *testing.T) {
	tests := []struct {
		name     string
		destType string
		config   string
	}{
		{
			name:     "s3 with the old access_key_id/secret_access_key names",
			destType: models.StorageTypeS3,
			config:   `{"bucket":"b","region":"us-east-1","access_key_id":"AK","secret_access_key":"SK"}`,
		},
		{
			name:     "sftp with username instead of user",
			destType: models.StorageTypeSFTP,
			config:   `{"host":"h","port":22,"username":"u","password":"p","path":"/backups"}`,
		},
		{
			name:     "sftp with port as a string",
			destType: models.StorageTypeSFTP,
			config:   `{"host":"h","port":"22","user":"u","password":"p","path":"/backups"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, validateStorageConfig(tc.destType, json.RawMessage(tc.config)))
		})
	}
}

func TestValidateStorageConfig_AcceptsCurrentUIPayloads(t *testing.T) {
	tests := []struct {
		name     string
		destType string
		config   string
	}{
		{"local", models.StorageTypeLocal, `{"path":"/backups"}`},
		{"s3", models.StorageTypeS3, `{"bucket":"b","region":"us-east-1","access_key":"AK","secret_key":"SK"}`},
		{"s3 with prefix and endpoint", models.StorageTypeS3, `{"bucket":"b","region":"r","access_key":"AK","secret_key":"SK","prefix":"backups/","endpoint":"https://s3.example.com"}`},
		{"sftp with password", models.StorageTypeSFTP, `{"host":"h","port":22,"user":"u","password":"p","path":"/backups"}`},
		{"sftp with private key", models.StorageTypeSFTP, `{"host":"h","port":22,"user":"u","private_key":"KEY","path":"/backups"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, validateStorageConfig(tc.destType, json.RawMessage(tc.config)))
		})
	}
}

func TestValidateStorageConfig_RequiresCredentialsAndKnownType(t *testing.T) {
	// Missing credentials must be rejected up front rather than at upload time.
	assert.Error(t, validateStorageConfig(models.StorageTypeS3, json.RawMessage(`{"bucket":"b","region":"r"}`)))
	assert.Error(t, validateStorageConfig(models.StorageTypeSFTP, json.RawMessage(`{"host":"h","port":22,"user":"u","path":"/b"}`)))
	assert.Error(t, validateStorageConfig(models.StorageTypeSFTP, json.RawMessage(`{"host":"h","port":0,"user":"u","password":"p","path":"/b"}`)))
	assert.Error(t, validateStorageConfig(models.StorageTypeLocal, json.RawMessage(`{}`)))
	assert.Error(t, validateStorageConfig("gdrive", json.RawMessage(`{"path":"/x"}`)))
}
