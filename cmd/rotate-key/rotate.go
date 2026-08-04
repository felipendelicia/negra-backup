// cmd/rotate-key/rotate.go
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/jmoiron/sqlx"
)

// valueState is what happened — or, in dry-run mode, what would happen — to a
// single stored value.
type valueState string

const (
	// stateRotated: decrypted with the old key and re-encrypted with the new one.
	stateRotated valueState = "rotated"
	// stateAlreadyRotated: the value already decrypts with the new key. A previous
	// run got this far, so re-running is a no-op instead of an error.
	stateAlreadyRotated valueState = "already encrypted with the new key"
	// statePlaintext: the value decrypts with neither key but is recognisably an
	// unencrypted legacy value (see target.legacyPlaintext). Left untouched: this
	// tool rotates keys, it does not retro-encrypt rows.
	statePlaintext valueState = "plaintext (legacy row, left untouched)"
	// stateEmpty: NULL is filtered out by the query, but an empty string is not.
	stateEmpty valueState = "empty"
	// stateUndecryptable: decrypts with neither key and is not a legacy plaintext
	// value. Either corrupt or encrypted with some third key; either way this tool
	// cannot rotate it and refuses to guess.
	stateUndecryptable valueState = "UNDECRYPTABLE"
)

// stateOrder fixes the order states are printed in, so the summary of two runs
// can be diffed. Map iteration order would not.
var stateOrder = []valueState{
	stateRotated,
	stateAlreadyRotated,
	statePlaintext,
	stateEmpty,
	stateUndecryptable,
}

type recryptResult struct {
	State valueState
	// NewCiphertext is set only when State is stateRotated. Every other state
	// leaves the stored value alone.
	NewCiphertext string
	// Plaintext is the decrypted value when State is stateRotated. It is kept in
	// memory for the post-write verification pass and never printed or logged.
	Plaintext string
}

// recryptValue moves one stored value from oldKey to newKey.
//
// It never returns a new ciphertext it has not proven to be readable: the value
// is decrypted with the old key, re-encrypted with the new one, and then
// decrypted again with the new key and compared byte for byte with the original
// plaintext. A value that survives that round-trip is safe to write; anything
// else returns an error and must abort the whole rotation, because a half-rotated
// database has no single key that opens it.
//
// legacyPlaintext, when non-nil, reports whether a value that decrypts with
// neither key is a legitimate unencrypted value rather than corruption. Columns
// that have always held ciphertext pass nil, so anything unreadable there is
// reported as stateUndecryptable instead of being waved through.
func recryptValue(stored, oldKey, newKey string, legacyPlaintext func(string) bool) (recryptResult, error) {
	if stored == "" {
		return recryptResult{State: stateEmpty}, nil
	}

	plaintext, err := crypto.Decrypt(oldKey, stored)
	if err != nil {
		// Not readable with the old key. Before calling it broken, check the two
		// benign explanations: it is already rotated, or it predates encryption.
		if _, newErr := crypto.Decrypt(newKey, stored); newErr == nil {
			return recryptResult{State: stateAlreadyRotated}, nil
		}
		if legacyPlaintext != nil && legacyPlaintext(stored) {
			return recryptResult{State: statePlaintext}, nil
		}
		return recryptResult{State: stateUndecryptable}, nil
	}

	ciphertext, err := crypto.Encrypt(newKey, plaintext)
	if err != nil {
		return recryptResult{}, fmt.Errorf("re-encrypt with the new key: %w", err)
	}

	// Proof, not assumption: read the value back the way the server will.
	roundTrip, err := crypto.Decrypt(newKey, ciphertext)
	if err != nil {
		return recryptResult{}, fmt.Errorf("verify re-encrypted value: %w", err)
	}
	if roundTrip != plaintext {
		return recryptResult{}, fmt.Errorf("re-encrypted value does not decrypt back to the original")
	}

	return recryptResult{State: stateRotated, NewCiphertext: ciphertext, Plaintext: plaintext}, nil
}

// isJSONObject matches internal/notify.DecryptSettingsConfig's test for a legacy
// unencrypted row: the column holds the config JSON verbatim.
func isJSONObject(s string) bool {
	var obj map[string]any
	return json.Unmarshal([]byte(s), &obj) == nil
}

// target is one encrypted column. Table and column names are compile-time
// constants from this file only — they are interpolated into SQL, never taken
// from input.
type target struct {
	table           string
	column          string
	legacyPlaintext func(string) bool
	note            string
}

// encryptedColumns lists every column encrypted with ENCRYPTION_KEY. Verified
// against every crypto.Encrypt / crypto.Decrypt call site in the tree:
//
//	internal/api/storage.go   → storage_destinations.config
//	internal/api/jobs.go      → backup_jobs.encrypt_passphrase
//	internal/api/settings.go  → notification_settings.config
//	internal/scheduler        → reads the first two
//	internal/notify           → reads the first and third
//
// backup_jobs.source also holds secrets (database connection strings) but is
// stored in plaintext JSONB, so it is unaffected by a key rotation.
var encryptedColumns = []target{
	{
		table:           "storage_destinations",
		column:          "config",
		legacyPlaintext: isJSONObject,
		note:            "S3/SFTP credentials",
	},
	{
		table:  "backup_jobs",
		column: "encrypt_passphrase",
		// This column has held ciphertext since it existed, and a passphrase is
		// arbitrary text, so there is no shape that would let us recognise a
		// plaintext one. Anything unreadable here is a genuine problem.
		legacyPlaintext: nil,
		note:            "backup archive passphrases",
	},
	{
		table:           "notification_settings",
		column:          "config",
		legacyPlaintext: isJSONObject,
		note:            "SMTP credentials — may be plaintext on old installs",
	},
}

type rowPlan struct {
	ID     string
	Result recryptResult
}

type tablePlan struct {
	Target target
	Rows   []rowPlan
}

func (p tablePlan) counts() map[valueState]int {
	c := make(map[valueState]int, len(stateOrder))
	for _, r := range p.Rows {
		c[r.Result.State]++
	}
	return c
}

func (p tablePlan) rotatable() []rowPlan {
	var out []rowPlan
	for _, r := range p.Rows {
		if r.Result.State == stateRotated {
			out = append(out, r)
		}
	}
	return out
}

type options struct {
	OldKey string
	NewKey string
	// Commit writes. False — the default — rolls the transaction back after the
	// plan has been built and verified, so a dry-run exercises the exact same code
	// path as the real thing.
	Commit bool
	// SkipUndecryptable leaves unreadable rows exactly as they are instead of
	// aborting. They are already unusable with either key, so refusing to rotate
	// the other 99% because of them is the worse failure.
	SkipUndecryptable bool
}

// rotate re-encrypts every value in every encrypted column, inside one
// transaction, or changes nothing at all.
//
// The sequence is deliberate:
//
//  1. Read and lock every row, and compute the full plan in memory. Nothing is
//     written until every single value has been proven to survive the round-trip
//     through the new key.
//  2. Write the plan.
//  3. Read every written row back and decrypt it with the new key, still inside
//     the transaction, and compare it with the plaintext from step 1.
//  4. Only then COMMIT.
//
// Any failure at any point returns an error, the deferred Rollback fires, and the
// database is untouched.
//
// The returned count is the number of values actually committed: zero for a dry
// run, and zero for a run that found nothing to do.
func rotate(ctx context.Context, pool *sqlx.DB, opts options, out io.Writer) (int, error) {
	// SERIALIZABLE, not the default READ COMMITTED: rows inserted by a server that
	// was left running would otherwise slip in between the plan and the commit and
	// stay encrypted with the old key. Here that raises a serialization failure,
	// which rolls back — the safe direction.
	tx, err := pool.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the transaction has committed

	plans, err := buildPlans(ctx, tx, opts)
	if err != nil {
		return 0, err
	}

	printPlans(out, plans)

	if err := checkPlans(plans, opts.SkipUndecryptable); err != nil {
		return 0, err
	}

	total := 0
	for _, p := range plans {
		total += len(p.rotatable())
	}

	if !opts.Commit {
		fmt.Fprintf(out, "\nDRY RUN: %d value(s) would be re-encrypted. Nothing was written.\n", total)
		fmt.Fprintf(out, "Re-run with --commit to apply.\n")
		// Explicit rollback rather than relying on the defer, so the line above is
		// the truth and not a hope.
		if err := tx.Rollback(); err != nil {
			return 0, fmt.Errorf("rollback dry run: %w", err)
		}
		return 0, nil
	}

	if total == 0 {
		fmt.Fprintf(out, "\nNothing to rotate. No changes written.\n")
		return 0, tx.Rollback()
	}

	if err := applyPlans(ctx, tx, plans); err != nil {
		return 0, err
	}
	if err := verifyPlans(ctx, tx, plans, opts.NewKey); err != nil {
		return 0, fmt.Errorf("post-write verification failed, rolling back: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	fmt.Fprintf(out, "\nCOMMITTED: %d value(s) re-encrypted with the new key.\n", total)
	return total, nil
}

// buildPlans reads and locks every candidate row and decides what to do with it,
// without writing anything.
func buildPlans(ctx context.Context, tx *sqlx.Tx, opts options) ([]tablePlan, error) {
	plans := make([]tablePlan, 0, len(encryptedColumns))

	for _, t := range encryptedColumns {
		// FOR UPDATE holds the rows for the duration of the transaction so nothing
		// can rewrite a value between the plan and the write.
		//nolint:gosec // table/column names are constants declared in this file
		query := fmt.Sprintf(
			`SELECT id::text, %s FROM %s WHERE %s IS NOT NULL ORDER BY id FOR UPDATE`,
			t.column, t.table, t.column)

		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("read %s.%s: %w", t.table, t.column, err)
		}

		plan := tablePlan{Target: t}
		for rows.Next() {
			var id, stored string
			if err := rows.Scan(&id, &stored); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan %s.%s: %w", t.table, t.column, err)
			}
			res, err := recryptValue(stored, opts.OldKey, opts.NewKey, t.legacyPlaintext)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("%s.%s row %s: %w", t.table, t.column, id, err)
			}
			plan.Rows = append(plan.Rows, rowPlan{ID: id, Result: res})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read %s.%s: %w", t.table, t.column, err)
		}
		rows.Close()

		plans = append(plans, plan)
	}

	return plans, nil
}

// checkPlans is the gate between "we know what we would do" and doing it.
func checkPlans(plans []tablePlan, skipUndecryptable bool) error {
	var bad []string
	for _, p := range plans {
		for _, r := range p.Rows {
			if r.Result.State == stateUndecryptable {
				bad = append(bad, fmt.Sprintf("%s.%s id=%s", p.Target.table, p.Target.column, r.ID))
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	if skipUndecryptable {
		return nil
	}
	return fmt.Errorf(
		"%d value(s) decrypt with neither the old nor the new key: %v\n"+
			"  Nothing was written. Either OLD_ENCRYPTION_KEY is wrong, or these rows are corrupt.\n"+
			"  Verify the old key first. If the rows really are unrecoverable they are already\n"+
			"  unusable, and --skip-undecryptable will rotate everything else and leave them as-is",
		len(bad), bad)
}

func applyPlans(ctx context.Context, tx *sqlx.Tx, plans []tablePlan) error {
	for _, p := range plans {
		//nolint:gosec // table/column names are constants declared in this file
		query := fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE id = $2`, p.Target.table, p.Target.column)
		for _, r := range p.rotatable() {
			res, err := tx.ExecContext(ctx, query, r.Result.NewCiphertext, r.ID)
			if err != nil {
				return fmt.Errorf("update %s.%s id=%s: %w", p.Target.table, p.Target.column, r.ID, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("update %s.%s id=%s: %w", p.Target.table, p.Target.column, r.ID, err)
			}
			if n != 1 {
				return fmt.Errorf("update %s.%s id=%s affected %d rows, expected 1",
					p.Target.table, p.Target.column, r.ID, n)
			}
		}
	}
	return nil
}

// verifyPlans re-reads what was just written and decrypts it with the new key,
// still inside the uncommitted transaction. This is the last chance to find out
// that something is wrong while rolling back is still free.
func verifyPlans(ctx context.Context, tx *sqlx.Tx, plans []tablePlan, newKey string) error {
	for _, p := range plans {
		//nolint:gosec // table/column names are constants declared in this file
		query := fmt.Sprintf(`SELECT %s FROM %s WHERE id = $1`, p.Target.column, p.Target.table)
		for _, r := range p.rotatable() {
			var stored string
			if err := tx.QueryRowContext(ctx, query, r.ID).Scan(&stored); err != nil {
				return fmt.Errorf("re-read %s.%s id=%s: %w", p.Target.table, p.Target.column, r.ID, err)
			}
			plaintext, err := crypto.Decrypt(newKey, stored)
			if err != nil {
				return fmt.Errorf("%s.%s id=%s does not decrypt with the new key: %w",
					p.Target.table, p.Target.column, r.ID, err)
			}
			if plaintext != r.Result.Plaintext {
				return fmt.Errorf("%s.%s id=%s decrypts to a different value than before",
					p.Target.table, p.Target.column, r.ID)
			}
		}
	}
	return nil
}

func printPlans(out io.Writer, plans []tablePlan) {
	for _, p := range plans {
		fmt.Fprintf(out, "\n%s.%s  (%s)\n", p.Target.table, p.Target.column, p.Target.note)
		if len(p.Rows) == 0 {
			fmt.Fprintf(out, "  no rows\n")
			continue
		}
		fmt.Fprintf(out, "  %d row(s) scanned\n", len(p.Rows))

		counts := p.counts()
		for _, state := range stateOrder {
			if counts[state] > 0 {
				fmt.Fprintf(out, "    %-40s %d\n", string(state)+":", counts[state])
			}
		}

		// Row ids are printed only for the states an operator has to look at.
		// "rotated" is the boring majority and would bury them.
		for _, r := range p.Rows {
			switch r.Result.State {
			case stateAlreadyRotated, statePlaintext, stateEmpty, stateUndecryptable:
				fmt.Fprintf(out, "      id=%s  %s\n", r.ID, r.Result.State)
			}
		}
	}
}
