package storage

import (
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
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	errCh := make(chan error, 1)

	go func() {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			pw.CloseWithError(fmt.Errorf("create form file: %w", err))
			errCh <- err
			return
		}
		if _, err := io.Copy(fw, r); err != nil {
			pw.CloseWithError(fmt.Errorf("copy to form: %w", err))
			errCh <- err
			return
		}
		mw.Close()
		pw.Close()
		errCh <- nil
	}()

	url := fmt.Sprintf("%s/api/upload/%s", b.cfg.ServerURL, b.cfg.RunID)
	req, err := http.NewRequest(http.MethodPost, url, pr)
	if err != nil {
		pw.CloseWithError(err)
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	// ContentLength unknown for streaming — chunked encoding (ContentLength = -1 is default)

	resp, err := b.client.Do(req)
	if writeErr := <-errCh; writeErr != nil && err == nil {
		err = writeErr
	}
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
