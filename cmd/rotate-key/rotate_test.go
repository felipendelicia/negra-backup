// cmd/rotate-key/rotate_test.go
package main

import (
	"strings"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	oldKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	newKey = "aabbccddeeff00112233445566778899aabbccddeeff001122334455667788aa"
	// A key that is neither: stands in for "encrypted by some other installation".
	thirdKey = "1111111111111111111111111111111111111111111111111111111111111111"
)

func TestRecryptValue_RotatesAndVerifies(t *testing.T) {
	plaintext := `{"bucket":"backups","access_key":"AKIA","secret_key":"s3cr3t"}`
	stored, err := crypto.Encrypt(oldKey, plaintext)
	require.NoError(t, err)

	res, err := recryptValue(stored, oldKey, newKey, isJSONObject)
	require.NoError(t, err)
	require.Equal(t, stateRotated, res.State)
	assert.Equal(t, plaintext, res.Plaintext)
	assert.NotEqual(t, stored, res.NewCiphertext)

	// The whole point: the new value opens with the new key...
	got, err := crypto.Decrypt(newKey, res.NewCiphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)

	// ...and no longer with the old one.
	_, err = crypto.Decrypt(oldKey, res.NewCiphertext)
	assert.Error(t, err)
}

func TestRecryptValue_RoundTripPreservesExactBytes(t *testing.T) {
	// Passphrases are arbitrary text. A rotation that mangles one byte silently
	// destroys every archive it protects.
	for _, plaintext := range []string{
		"simple",
		"ñandú çedilla — 日本語",
		`{"nested":{"json":true}}`,
		"with\nnewlines\tand\ttabs",
		strings.Repeat("long", 4096),
		" ",
	} {
		stored, err := crypto.Encrypt(oldKey, plaintext)
		require.NoError(t, err)

		res, err := recryptValue(stored, oldKey, newKey, nil)
		require.NoError(t, err)
		require.Equal(t, stateRotated, res.State)

		got, err := crypto.Decrypt(newKey, res.NewCiphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, got)
	}
}

func TestRecryptValue_AlreadyRotatedIsSkipped(t *testing.T) {
	// Re-running after a partial or completed rotation must be a no-op, not an
	// error and certainly not a double encryption.
	stored, err := crypto.Encrypt(newKey, `{"smtp_host":"smtp.example.com"}`)
	require.NoError(t, err)

	res, err := recryptValue(stored, oldKey, newKey, isJSONObject)
	require.NoError(t, err)
	assert.Equal(t, stateAlreadyRotated, res.State)
	assert.Empty(t, res.NewCiphertext, "an already-rotated value must not be rewritten")
}

func TestRecryptValue_PlaintextLegacyRow(t *testing.T) {
	// notification_settings.config predates encryption on old installs;
	// internal/notify.DecryptSettingsConfig reads those rows verbatim.
	stored := `{"smtp_host":"smtp.example.com","password":"hunter2"}`

	res, err := recryptValue(stored, oldKey, newKey, isJSONObject)
	require.NoError(t, err)
	assert.Equal(t, statePlaintext, res.State)
	assert.Empty(t, res.NewCiphertext, "a plaintext row must be left exactly as it is")
}

func TestRecryptValue_DefaultEmptyJSONObjectIsPlaintext(t *testing.T) {
	// Both config columns default to '{}' in the schema.
	res, err := recryptValue("{}", oldKey, newKey, isJSONObject)
	require.NoError(t, err)
	assert.Equal(t, statePlaintext, res.State)
}

func TestRecryptValue_PlaintextWithoutPredicateIsUndecryptable(t *testing.T) {
	// backup_jobs.encrypt_passphrase has always been ciphertext and a passphrase
	// has no recognisable shape, so a readable value there is a red flag, not a
	// legacy row to wave through.
	res, err := recryptValue(`{"smtp_host":"smtp.example.com"}`, oldKey, newKey, nil)
	require.NoError(t, err)
	assert.Equal(t, stateUndecryptable, res.State)
}

func TestRecryptValue_WrongOldKey(t *testing.T) {
	// The most likely operator mistake: OLD_ENCRYPTION_KEY is not the key the data
	// was actually encrypted with. It must be caught, never guessed past.
	stored, err := crypto.Encrypt(thirdKey, "some-passphrase")
	require.NoError(t, err)

	res, err := recryptValue(stored, oldKey, newKey, isJSONObject)
	require.NoError(t, err)
	assert.Equal(t, stateUndecryptable, res.State)
	assert.Empty(t, res.NewCiphertext)
}

func TestRecryptValue_CorruptCiphertext(t *testing.T) {
	stored, err := crypto.Encrypt(oldKey, "some-passphrase")
	require.NoError(t, err)
	corrupt := "A" + stored[1:] // flip the base64, break the GCM tag

	res, err := recryptValue(corrupt, oldKey, newKey, nil)
	require.NoError(t, err)
	assert.Equal(t, stateUndecryptable, res.State)
}

func TestRecryptValue_NotBase64(t *testing.T) {
	res, err := recryptValue("not-base64!!!", oldKey, newKey, nil)
	require.NoError(t, err)
	assert.Equal(t, stateUndecryptable, res.State)
}

func TestRecryptValue_Empty(t *testing.T) {
	res, err := recryptValue("", oldKey, newKey, isJSONObject)
	require.NoError(t, err)
	assert.Equal(t, stateEmpty, res.State)
	assert.Empty(t, res.NewCiphertext)
}

func TestRecryptValue_InvalidNewKeyErrors(t *testing.T) {
	// A bad new key must surface as an error that aborts the run, not as a state
	// the caller might decide to skip.
	stored, err := crypto.Encrypt(oldKey, "secret")
	require.NoError(t, err)

	_, err = recryptValue(stored, oldKey, "tooshort", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "re-encrypt")
}

func TestRecryptValue_InvalidOldKeyDoesNotRotate(t *testing.T) {
	stored, err := crypto.Encrypt(oldKey, "secret")
	require.NoError(t, err)

	res, err := recryptValue(stored, "tooshort", newKey, nil)
	require.NoError(t, err)
	assert.Equal(t, stateUndecryptable, res.State)
}

func TestIsJSONObject(t *testing.T) {
	assert.True(t, isJSONObject(`{}`))
	assert.True(t, isJSONObject(`{"a":1}`))
	assert.False(t, isJSONObject(`[1,2]`), "a JSON array is not a config object")
	assert.False(t, isJSONObject(`"string"`))
	assert.False(t, isJSONObject(`not json`))
	assert.False(t, isJSONObject(``))
}

func TestCheckPlans_AbortsOnUndecryptable(t *testing.T) {
	plans := []tablePlan{{
		Target: encryptedColumns[0],
		Rows: []rowPlan{
			{ID: "11111111-1111-1111-1111-111111111111", Result: recryptResult{State: stateRotated}},
			{ID: "22222222-2222-2222-2222-222222222222", Result: recryptResult{State: stateUndecryptable}},
		},
	}}

	err := checkPlans(plans, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "22222222-2222-2222-2222-222222222222")

	assert.NoError(t, checkPlans(plans, true), "--skip-undecryptable proceeds with the rest")
}

func TestCheckPlans_CleanPlanPasses(t *testing.T) {
	plans := []tablePlan{{
		Target: encryptedColumns[0],
		Rows: []rowPlan{
			{ID: "11111111-1111-1111-1111-111111111111", Result: recryptResult{State: stateRotated}},
			{ID: "22222222-2222-2222-2222-222222222222", Result: recryptResult{State: statePlaintext}},
			{ID: "33333333-3333-3333-3333-333333333333", Result: recryptResult{State: stateAlreadyRotated}},
		},
	}}
	require.NoError(t, checkPlans(plans, false))
}

func TestTablePlan_RotatableOnlyReturnsRotatedRows(t *testing.T) {
	plan := tablePlan{
		Target: encryptedColumns[1],
		Rows: []rowPlan{
			{ID: "a", Result: recryptResult{State: stateRotated, NewCiphertext: "x"}},
			{ID: "b", Result: recryptResult{State: statePlaintext}},
			{ID: "c", Result: recryptResult{State: stateEmpty}},
			{ID: "d", Result: recryptResult{State: stateRotated, NewCiphertext: "y"}},
		},
	}
	rotatable := plan.rotatable()
	require.Len(t, rotatable, 2)
	assert.Equal(t, "a", rotatable[0].ID)
	assert.Equal(t, "d", rotatable[1].ID)

	counts := plan.counts()
	assert.Equal(t, 2, counts[stateRotated])
	assert.Equal(t, 1, counts[statePlaintext])
	assert.Equal(t, 1, counts[stateEmpty])
}

// The list of encrypted columns is the tool's contract with the schema. If a new
// encrypted column is added and not listed here, rotation silently leaves it
// behind and the installation breaks after the key is swapped.
func TestEncryptedColumns_CoversEveryKnownEncryptedColumn(t *testing.T) {
	want := map[string]bool{
		"storage_destinations.config":    false,
		"backup_jobs.encrypt_passphrase": false,
		"notification_settings.config":   false,
	}
	for _, c := range encryptedColumns {
		key := c.table + "." + c.column
		_, known := want[key]
		require.True(t, known, "unexpected target %s — update this test", key)
		want[key] = true
	}
	for k, covered := range want {
		assert.True(t, covered, "%s is encrypted but not rotated", k)
	}
}
