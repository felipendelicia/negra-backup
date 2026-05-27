package storage

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// LocalBackend uploads files to the nat-backup server via HTTP multipart.
type LocalBackend struct {
	cfg    LocalConfig
	client *http.Client
}

func NewLocalBackend(cfg LocalConfig) *LocalBackend {
	return &LocalBackend{cfg: cfg, client: &http.Client{}}
}

func (b *LocalBackend) Upload(filename string, r io.Reader, size int64) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(fw, r); err != nil {
		return fmt.Errorf("copy to form: %w", err)
	}
	mw.Close()

	url := fmt.Sprintf("%s/api/upload/%s", b.cfg.ServerURL, b.cfg.RunID)
	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed %d: %s", resp.StatusCode, body)
	}

	return nil
}
