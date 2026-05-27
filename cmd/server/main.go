// cmd/server/main.go
package main

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/felipendelicia/nat-backup/internal/models"
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

	srv, hub, agentHandler := api.NewServerWithStatic(pool, cfg)

	// Tee log output to the browser console hub
	log.SetOutput(io.MultiWriter(os.Stderr, srv.GetConsoleHub()))

	// Wire email notifier if notification settings exist in DB.
	var emailSender *notify.EmailSender
	var nsRaw []byte
	if err := pool.QueryRow(`SELECT config FROM notification_settings WHERE type='email' LIMIT 1`).Scan(&nsRaw); err != nil {
		if err != sql.ErrNoRows {
			log.Printf("loading notification settings: %v", err)
		}
	} else {
		var emailCfg models.EmailNotificationConfig
		if err := json.Unmarshal(nsRaw, &emailCfg); err != nil {
			log.Printf("parsing notification settings: %v", err)
		} else {
			emailSender = notify.NewEmailSender(emailCfg)
		}
	}
	if emailSender != nil {
		agentHandler.SetFailureNotifier(emailSender)
	}

	// Start daily retention cleanup.
	retention := notify.NewRetentionCleaner(pool, emailSender)
	retention.StartDailySchedule()

	sched := scheduler.New(pool, hub, cfg.EncryptionKey)
	srv.SetScheduler(sched)
	sched.Start()
	defer sched.Stop()

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
