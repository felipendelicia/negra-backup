// cmd/server/main.go
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/felipendelicia/nat-backup/internal/api"
	"github.com/felipendelicia/nat-backup/internal/config"
	"github.com/felipendelicia/nat-backup/internal/db"
	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/felipendelicia/nat-backup/internal/scheduler"
	"golang.org/x/crypto/bcrypt"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := db.RunMigrations(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	pool, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	// Create default admin user if not exists
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("bcrypt: %v", err)
	}
	pool.Exec(
		`INSERT INTO admin_users (username, password_hash) VALUES ('admin', $1) ON CONFLICT (username) DO NOTHING`,
		string(hash),
	)

	// Mark all agents offline at startup — hub is empty, DB must match.
	if _, err := pool.Exec(`UPDATE agents SET status='offline'`); err != nil {
		log.Printf("reset agent status: %v", err)
	}

	// Runs left mid-flight by the previous process have no one to finish them: the
	// agent that was executing them is gone along with the hub. Without this they
	// stay 'running' forever, keep the UI offering a Cancel that does nothing, and
	// are skipped by retention, which only sweeps terminal states.
	if res, err := pool.Exec(
		`UPDATE backup_runs SET status='failed', finished_at=NOW(),
		        error_message='Server restarted while this run was in progress'
		 WHERE status='running'`); err != nil {
		log.Printf("reset stale runs: %v", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("marked %d stale running runs as failed", n)
	}

	srv, hub, agentHandler := api.NewServerWithStatic(pool, cfg)

	// Tee log output to the browser console hub
	log.SetOutput(io.MultiWriter(os.Stderr, srv.GetConsoleHub()))

	// The notifier reads its configuration per alert rather than being frozen with
	// whatever was in the database at boot. Snapshotting it here meant a fresh
	// install never sent anything and UI changes needed a restart to take effect.
	notifier := notify.NewDBNotifierWithKey(pool, cfg.EncryptionKey)
	agentHandler.SetFailureNotifier(notifier)

	// Start daily retention cleanup.
	retention := notify.NewRetentionCleaner(pool, cfg.EncryptionKey)
	retention.StartDailySchedule()

	sched := scheduler.New(pool, hub, cfg.EncryptionKey)
	// The same notifier for failures the SERVER detects. Until this line the only
	// alert in the system came from an agent's `job_failed` message, so an offline
	// agent, an unreadable storage destination or an undecryptable passphrase became
	// a `failed` run that nobody was told about.
	sched.SetNotifier(notifier)
	srv.SetScheduler(sched)
	sched.Start()
	defer sched.Stop()

	// The dead-man's switch: the half of the problem no run-level notification can
	// reach. A backup that fails sends an email; a backup that never runs sends
	// nothing at all, and that silence is what this watches for.
	deadman := notify.NewDeadManSwitch(pool, notifier, notify.DeadManConfigFromEnv())
	deadman.Start()
	defer deadman.Stop()

	addr := ":" + cfg.Port
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		fmt.Printf("nat-backup-server listening on %s\n", addr)
		if cfg.TLSEnabled {
			if err := httpSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != http.ErrServerClosed {
				log.Fatalf("tls server: %v", err)
			}
		} else {
			if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
				log.Fatalf("server: %v", err)
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
	fmt.Println("server stopped")
}
