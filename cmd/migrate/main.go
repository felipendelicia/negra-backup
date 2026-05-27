// cmd/migrate/main.go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/felipendelicia/nat-backup/internal/db"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL not set")
	}
	if len(os.Args) < 2 || os.Args[1] != "up" {
		fmt.Fprintln(os.Stderr, "Usage: migrate up")
		os.Exit(1)
	}
	if err := db.RunMigrations(url); err != nil {
		log.Fatalf("migration failed: %v", err)
	}
	fmt.Println("migrations applied")
}
