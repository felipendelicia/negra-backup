// cmd/server/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
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

func main() {
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

	handler, hub, agentHandler := api.NewServer(pool, cfg)

	// Wire email notifier if notification settings exist in DB.
	var emailSender *notify.EmailSender
	var nsRaw []byte
	if err := pool.QueryRow(`SELECT config FROM notification_settings WHERE type='email' LIMIT 1`).Scan(&nsRaw); err == nil {
		var emailCfg models.EmailNotificationConfig
		if json.Unmarshal(nsRaw, &emailCfg) == nil {
			emailSender = notify.NewEmailSender(emailCfg)
		}
	}
	if emailSender != nil {
		agentHandler.SetFailureNotifier(emailSender)
	}

	// Start daily retention cleanup.
	retention := notify.NewRetentionCleaner(pool, emailSender)
	retention.StartDailySchedule()

	sched := scheduler.New(pool, hub)
	sched.Start()
	defer sched.Stop()

	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		fmt.Printf("nat-backup-server listening on %s\n", addr)
		if cfg.TLSEnabled {
			if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != http.ErrServerClosed {
				log.Fatalf("tls server: %v", err)
			}
		} else {
			if err := srv.ListenAndServe(); err != http.ErrServerClosed {
				log.Fatalf("server: %v", err)
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	fmt.Println("server stopped")
}
