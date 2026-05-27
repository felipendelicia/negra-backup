// internal/notify/email.go
package notify

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"

	"github.com/felipendelicia/nat-backup/internal/models"
)

type EmailSender struct {
	cfg models.EmailNotificationConfig
}

func NewEmailSender(cfg models.EmailNotificationConfig) *EmailSender {
	return &EmailSender{cfg: cfg}
}

func (e *EmailSender) SendFailureAlert(jobName, agentName, runID, errMsg string) error {
	subject := fmt.Sprintf("[nat-backup] Backup FAILED: %s", jobName)
	body := fmt.Sprintf(
		"Backup job failed.\n\nJob: %s\nAgent: %s\nRun ID: %s\n\nError:\n%s",
		jobName, agentName, runID, errMsg,
	)
	return e.send(subject, body)
}

func (e *EmailSender) send(subject, body string) error {
	msg := []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		e.cfg.From,
		strings.Join(e.cfg.To, ", "),
		subject,
		body,
	))
	addr := fmt.Sprintf("%s:%d", e.cfg.SMTPHost, e.cfg.SMTPPort)
	var auth smtp.Auth
	if e.cfg.Username != "" {
		auth = smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, e.cfg.SMTPHost)
	}
	if e.cfg.TLS {
		return e.sendTLS(addr, auth, msg)
	}
	return smtp.SendMail(addr, auth, e.cfg.From, e.cfg.To, msg)
}

func (e *EmailSender) sendTLS(addr string, auth smtp.Auth, msg []byte) error {
	host, _, _ := net.SplitHostPort(addr)
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(e.cfg.From); err != nil {
		return fmt.Errorf("smtp MAIL: %w", err)
	}
	for _, to := range e.cfg.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("smtp RCPT: %w", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	return w.Close()
}
