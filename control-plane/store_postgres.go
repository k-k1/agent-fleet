package main

import (
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations-pg/*.sql
var pgMigrationFS embed.FS

// pgURLFromEnv returns the Postgres DSN for the metadata store, or "" to fall back
// to SQLite. AF_DATABASE_URL wins (on-prem passes a full URL). Otherwise, when
// AF_DB_HOST is set (the ECS/RDS path), the URL is composed from parts so the
// password can arrive separately as an SSM/Secrets-Manager-injected env var rather
// than being baked into a plaintext URL.
func pgURLFromEnv() string {
	if u := os.Getenv("AF_DATABASE_URL"); u != "" {
		return u
	}
	host := os.Getenv("AF_DB_HOST")
	if host == "" {
		return ""
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		url.QueryEscape(envOr("AF_DB_USER", "afadmin")),
		url.QueryEscape(os.Getenv("AF_DB_PASSWORD")),
		host, envOr("AF_DB_PORT", "5432"),
		envOr("AF_DB_NAME", "agentfleet"),
		envOr("AF_DB_SSLMODE", "require"))
}

// openPostgres opens the Postgres MetadataStore (P3-7 段3a; the RDS backend for a
// redeployable ECS Control Plane whose state must survive task replacement). It
// reuses the shared sqlStore with ?→$n rebinding and the consolidated pg schema.
// No legacy hook: fresh Postgres deployments start at the post-P3-2 schema.
func openPostgres(url string) (*sqlStore, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &sqlStore{db: &sqlDB{DB: db, rb: rebindDollar}, dialect: "postgres", mfs: pgMigrationFS, mdir: "migrations-pg"}, nil
}
