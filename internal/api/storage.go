// internal/api/storage.go
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type createStorageRequest struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// validateStorageConfig decodes the raw config into the model for the given type and
// checks the fields the backend needs. Without this, a config whose keys don't match
// the Go json tags is stored happily and only fails at upload time, long after the
// user has been told the destination was created.
func validateStorageConfig(destType string, raw json.RawMessage) error {
	dec := func(v any) error {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(v); err != nil {
			return fmt.Errorf("invalid %s config: %w", destType, err)
		}
		return nil
	}
	switch destType {
	case models.StorageTypeLocal:
		var c models.LocalStorageConfig
		if err := dec(&c); err != nil {
			return err
		}
		if c.Path == "" {
			return errors.New("local config requires path")
		}
	case models.StorageTypeS3:
		var c models.S3StorageConfig
		if err := dec(&c); err != nil {
			return err
		}
		if c.Bucket == "" || c.Region == "" || c.AccessKey == "" || c.SecretKey == "" {
			return errors.New("s3 config requires bucket, region, access_key and secret_key")
		}
	case models.StorageTypeSFTP:
		var c models.SFTPStorageConfig
		if err := dec(&c); err != nil {
			return err
		}
		if c.Host == "" || c.User == "" || c.Path == "" {
			return errors.New("sftp config requires host, user and path")
		}
		if c.Port <= 0 || c.Port > 65535 {
			return errors.New("sftp config requires a valid port")
		}
		if c.Password == "" && c.PrivateKey == "" {
			return errors.New("sftp config requires either password or private_key")
		}
	default:
		return fmt.Errorf("unknown storage type %q", destType)
	}
	return nil
}

func (s *Server) handleListStorage(w http.ResponseWriter, r *http.Request) {
	dests := []models.StorageDestination{}
	// Config column intentionally excluded from list (contains secrets)
	if err := s.db.Select(&dests, `SELECT id, name, type, created_at FROM storage_destinations ORDER BY created_at DESC`); err != nil {
		log.Printf("handleListStorage: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	respond(w, http.StatusOK, dests)
}

func (s *Server) handleCreateStorage(w http.ResponseWriter, r *http.Request) {
	var req createStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" || req.Type == "" {
		respondError(w, http.StatusBadRequest, "name and type required")
		return
	}
	if err := validateStorageConfig(req.Type, req.Config); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	encConfig, err := crypto.Encrypt(s.cfg.EncryptionKey, string(req.Config))
	if err != nil {
		log.Printf("handleCreateStorage encrypt: %v", err)
		respondError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	var dest models.StorageDestination
	err = s.db.QueryRowx(
		`INSERT INTO storage_destinations (name, type, config) VALUES ($1, $2, $3) RETURNING id, name, type, created_at`,
		req.Name, req.Type, encConfig,
	).StructScan(&dest)
	if err != nil {
		log.Printf("handleCreateStorage: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	respond(w, http.StatusCreated, dest)
}

func (s *Server) handleUpdateStorage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req createStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// An absent or empty config means "keep the stored credentials". Encrypting it
	// blindly used to overwrite them with "{}" and break every job using this
	// destination, with no way to recover the originals.
	var encConfig *string
	if len(req.Config) > 0 && !bytes.Equal(bytes.TrimSpace(req.Config), []byte("{}")) {
		if err := validateStorageConfig(req.Type, req.Config); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		enc, err := crypto.Encrypt(s.cfg.EncryptionKey, string(req.Config))
		if err != nil {
			log.Printf("handleUpdateStorage encrypt: %v", err)
			respondError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		encConfig = &enc
	}
	result, err := s.db.Exec(
		`UPDATE storage_destinations SET name=$1, type=$2, config=COALESCE($3, config) WHERE id=$4`,
		req.Name, req.Type, encConfig, id,
	)
	if err != nil {
		log.Printf("handleUpdateStorage: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteStorage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	result, err := s.db.Exec(`DELETE FROM storage_destinations WHERE id=$1`, id)
	if err != nil {
		log.Printf("handleDeleteStorage: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
