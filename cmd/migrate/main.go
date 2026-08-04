// cmd/migrate/main.go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/felipendelicia/nat-backup/internal/db"
)

const usage = `Usage: migrate <command>

Commands:
  up        Apply all pending migrations.
  down      Roll back the most recent migration.
  version   Print the current schema version and whether it is dirty.
`

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL not set")
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "up":
		if err := db.RunMigrations(url); err != nil {
			log.Fatalf("migration failed: %v", err)
		}
		fmt.Println("migrations applied")

	case "down":
		// One step at a time on purpose: rolling everything back from a typo is not
		// a mistake worth making convenient.
		if err := db.RollbackMigration(url); err != nil {
			log.Fatalf("rollback failed: %v", err)
		}
		fmt.Println("rolled back one migration")

	case "version":
		version, dirty, err := db.MigrationVersion(url)
		if err != nil {
			log.Fatalf("version check failed: %v", err)
		}
		if dirty {
			fmt.Printf("version %d (DIRTY — a migration failed partway and must be resolved manually)\n", version)
			os.Exit(1)
		}
		fmt.Printf("version %d\n", version)

	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}
