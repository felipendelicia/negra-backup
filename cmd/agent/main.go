package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	agentInternal "github.com/felipendelicia/nat-backup/cmd/agent/internal"
	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/storage"
	"github.com/felipendelicia/nat-backup/internal/ws"
	"github.com/gorilla/websocket"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(version)
			return
		case "install":
			if err := install(); err != nil {
				log.Fatalf("install failed: %v", err)
			}
			fmt.Println("agent installed as system service")
			return
		}
	}

	cfgPath := "agent.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := agentInternal.LoadAgentConfig(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	agent := &Agent{cfg: cfg, runCtxs: make(map[string]context.CancelFunc)}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	for {
		log.Printf("connecting to %s...", cfg.ServerURL)
		if err := agent.connect(); err != nil {
			log.Printf("connection error: %v — retrying in 10s", err)
		}

		select {
		case <-quit:
			fmt.Println("agent stopped")
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// Agent holds the running agent state.
type Agent struct {
	cfg       agentInternal.AgentConfig
	mu        sync.Mutex
	conn      *websocket.Conn
	runCtxsMu sync.Mutex
	runCtxs   map[string]context.CancelFunc
}

func (a *Agent) writeJSON(v any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn == nil {
		return fmt.Errorf("not connected")
	}
	return a.conn.WriteJSON(v)
}

func (a *Agent) connect() error {
	wsServerURL := toWSURL(a.cfg.ServerURL)
	conn, _, err := websocket.DefaultDialer.Dial(wsServerURL+"/ws/agent", nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.conn = nil
		a.mu.Unlock()
		conn.Close()
	}()

	hello := ws.AgentMessage{
		Type:    ws.MsgTypeHello,
		APIKey:  a.cfg.APIKey,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Version: version,
	}
	if err := a.writeJSON(hello); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	log.Println("connected to server")

	// Heartbeat goroutine — exits when ctx is cancelled (connect returns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := a.writeJSON(ws.AgentMessage{Type: ws.MsgTypeHeartbeat}); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		var msg ws.ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("unmarshal server message: %v", err)
			continue
		}

		switch msg.Type {
		case ws.MsgTypeRunJob:
			if msg.Job != nil {
				ctx, cancel := context.WithCancel(context.Background())
				a.runCtxsMu.Lock()
				a.runCtxs[msg.RunID] = cancel
				a.runCtxsMu.Unlock()
				go func() {
					defer func() {
						a.runCtxsMu.Lock()
						delete(a.runCtxs, msg.RunID)
						a.runCtxsMu.Unlock()
						cancel()
					}()
					a.executeJob(ctx, msg.RunID, *msg.Job, msg.StorageType, msg.StorageConfig, msg.Passphrase)
				}()
			}
		case ws.MsgTypeCancelJob:
			a.runCtxsMu.Lock()
			if cancel, ok := a.runCtxs[msg.RunID]; ok {
				log.Printf("cancelling run %s", msg.RunID)
				cancel()
			}
			a.runCtxsMu.Unlock()

		case ws.MsgTypeUpdateAgent:
			log.Println("update requested by server")
			go func() {
				if err := selfUpdate(); err != nil {
					log.Printf("self-update failed: %v", err)
				}
			}()
		}
	}
}

func (a *Agent) executeJob(ctx context.Context, runID string, job models.BackupJob, storageType string, storageConfig json.RawMessage, passphrase string) {
	log.Printf("executing job %s (run %s)", job.ID, runID)

	var err error
	var result backup.BackupResult
	var buf bytes.Buffer

	switch job.Type {
	case models.JobTypeFiles:
		var src models.FilesSource
		if err := json.Unmarshal(job.Source, &src); err != nil {
			a.sendFailure(runID, "parse source: "+err.Error())
			return
		}

		cfg := backup.FilesConfig{
			Paths:       src.Paths,
			Compression: job.Compression,
			Encrypt:     job.Encrypt,
			Passphrase:  passphrase,
			OnProgress: func(pct int, file string) {
				if ctx.Err() != nil {
					return
				}
				a.writeJSON(ws.AgentMessage{
					Type:        ws.MsgTypeJobProgress,
					RunID:       runID,
					Percent:     pct,
					CurrentFile: file,
				})
			},
		}
		result, err = backup.BackupFiles(cfg, &buf)
		if err == nil && ctx.Err() != nil {
			a.sendFailure(runID, "cancelled")
			return
		}

	case models.JobTypePostgres, models.JobTypeMySQL, models.JobTypeSQLite, models.JobTypeMongoDB:
		var src models.DBSource
		if err := json.Unmarshal(job.Source, &src); err != nil {
			a.sendFailure(runID, "parse source: "+err.Error())
			return
		}

		if ctx.Err() != nil {
			a.sendFailure(runID, "cancelled")
			return
		}

		tmpFile, tmpErr := os.CreateTemp("", "nat-backup-dump-*")
		if tmpErr != nil {
			a.sendFailure(runID, "temp file: "+tmpErr.Error())
			return
		}
		defer os.Remove(tmpFile.Name())

		dumper := backup.NewDBDumper(job.Type, src.ConnectionString)
		if _, dumpErr := dumper.Dump(tmpFile); dumpErr != nil {
			tmpFile.Close()
			a.sendFailure(runID, "dump: "+dumpErr.Error())
			return
		}
		tmpFile.Close()

		if ctx.Err() != nil {
			a.sendFailure(runID, "cancelled")
			return
		}

		result, err = backup.BackupFiles(backup.FilesConfig{
			Paths:       []string{tmpFile.Name()},
			Compression: job.Compression,
			Encrypt:     job.Encrypt,
			Passphrase:  passphrase,
		}, &buf)

	default:
		a.sendFailure(runID, "unsupported job type: "+job.Type)
		return
	}

	if err != nil {
		log.Printf("job %s failed: %v", job.ID, err)
		a.sendFailure(runID, err.Error())
		return
	}

	if ctx.Err() != nil {
		a.sendFailure(runID, "cancelled")
		return
	}

	// Upload to storage
	filename := fmt.Sprintf("%s-%s.tar.%s", job.Name, runID, job.Compression)
	if job.Encrypt {
		filename += ".enc"
	}

	// Determine storage backend from job dispatch
	destType := storageType
	destConfig := storageConfig
	if destType == "" {
		// Fallback to local if server didn't provide storage info
		destType = models.StorageTypeLocal
	}

	backend, err := storage.NewBackend(destType, destConfig, runID, a.cfg.ServerURL, a.cfg.APIKey)
	if err != nil {
		a.sendFailure(runID, "storage backend: "+err.Error())
		return
	}

	if err := backend.Upload(filename, &buf, int64(buf.Len())); err != nil {
		a.sendFailure(runID, "upload: "+err.Error())
		return
	}

	if err := a.writeJSON(ws.AgentMessage{
		Type:        ws.MsgTypeJobDone,
		RunID:       runID,
		Status:      "success",
		SizeBytes:   result.SizeBytes,
		FileCount:   result.FileCount,
		StoragePath: filename,
	}); err != nil {
		log.Printf("send job_done: %v", err)
	}
	log.Printf("job %s completed", job.ID)
}

func (a *Agent) sendFailure(runID, errMsg string) {
	if err := a.writeJSON(ws.AgentMessage{
		Type:  ws.MsgTypeJobFailed,
		RunID: runID,
		Error: errMsg,
	}); err != nil {
		log.Printf("send job_failed: %v", err)
	}
}

func toWSURL(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "ws://" + serverURL
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		u.Scheme = "ws"
	}
	return u.String()
}

func install() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, _ = filepath.Abs(exePath)

	if runtime.GOOS == "windows" {
		return installWindows(exePath)
	}
	return installLinux(exePath)
}

func installLinux(exePath string) error {
	unit := fmt.Sprintf(`[Unit]
Description=nat-backup Agent
After=network.target

[Service]
ExecStart=%s /etc/nat-backup/agent.yaml
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`, exePath)

	if err := os.MkdirAll("/etc/nat-backup", 0755); err != nil {
		return fmt.Errorf("mkdir /etc/nat-backup: %w", err)
	}
	if err := os.WriteFile("/etc/systemd/system/nat-backup-agent.service", []byte(unit), 0644); err != nil {
		return fmt.Errorf("write service file: %w", err)
	}
	fmt.Println("systemd service written. Run: systemctl enable --now nat-backup-agent")
	return nil
}

func installWindows(exePath string) error {
	fmt.Printf("To install as Windows Service, run as Administrator:\n")
	fmt.Printf("  sc create nat-backup-agent binPath= \"%s agent.yaml\" start= auto\n", exePath)
	fmt.Printf("  sc start nat-backup-agent\n")
	return nil
}
