package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"fmt"
	"log"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations-pg/*.sql
var pgMigrationFS embed.FS

// PGURLFromEnv returns the Postgres DSN for the metadata store, or "" to fall back
// to SQLite. AF_DATABASE_URL wins (on-prem passes a full URL). Otherwise, when
// AF_DB_HOST is set (the ECS/RDS path), the URL is composed from parts so the
// password can arrive separately as an SSM/Secrets-Manager-injected env var rather
// than being baked into a plaintext URL.
func PGURLFromEnv() string {
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

// OpenPostgres opens the Postgres MetadataStore (P3-7 stage 3a; the RDS backend for a
// redeployable ECS Control Plane whose state must survive task replacement). It
// reuses the shared SQL with ?→$n rebinding and the consolidated pg schema.
// No legacy hook: fresh Postgres deployments start at the post-P3-2 schema.
func OpenPostgres(url string) (*SQL, error) {
	// LookupEnv, not envOr: AF_DB_PASSWORD_SECRET_KEY="" is a meaningful value — it
	// says the secret string IS the password, rather than JSON to pick a field out of.
	// envOr cannot express that, since it treats empty and unset alike.
	key, ok := os.LookupEnv("AF_DB_PASSWORD_SECRET_KEY")
	if !ok {
		key = "password" // the RDS-managed shape
	}
	return openPostgresWith(url, newDBPasswordSource(os.Getenv("AF_DB_PASSWORD_SECRET_ARN"), key))
}

// openPostgresWith is OpenPostgres with the password source injected, so the
// rotation behaviour can be driven from a test.
func openPostgresWith(dsn string, src *dbPasswordSource) (*SQL, error) {
	cc, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	// The DSN's password is the bootstrap value; see store_postgres_secret.go.
	src.seed(cc.Password)
	// BeforeConnect runs on a per-connection copy of the config, so every physical
	// connection the pool opens picks up whatever the source holds NOW. Pooled
	// connections that are already up are untouched — Postgres does not re-auth an
	// established session, so a rotation costs nothing until the pool grows.
	base := stdlib.GetConnector(*cc, stdlib.OptionBeforeConnect(func(_ context.Context, c *pgx.ConnConfig) error {
		c.Password = src.current()
		return nil
	}))
	db := sql.OpenDB(&refreshingConnector{Connector: base, src: src})
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &SQL{db: &sqlDB{DB: db, rb: rebindDollar}, dialect: "postgres", mfs: pgMigrationFS, mdir: "migrations-pg"}, nil
}

// refreshingConnector turns "the password rotated under us" from an outage into a
// retry nobody sees. database/sql does not retry a failed Connect — it hands the
// error straight to whoever asked for a connection, which on 2026-09-01 meant
// every /api/* call returning 500 — so the recovery has to happen inside Connect.
type refreshingConnector struct {
	driver.Connector
	src *dbPasswordSource

	mu     sync.Mutex
	logged time.Time // rate-limits logDBUnavailable
}

func (c *refreshingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err == nil {
		return conn, nil
	}
	if !isPgAuthFailure(err) {
		c.reportUnavailable(err) // host down, TLS, DNS — not ours to fix, but not silent either
		return nil, err
	}
	// AWSCURRENT is the answer outside a rotation; AWSPENDING is the answer inside
	// the setSecret→finishSecret window. Try each at most once.
	for _, stage := range []string{stageCurrent, stagePending} {
		tried := c.src.current()
		if _, ok := c.src.refresh(ctx, stage, tried); !ok {
			continue
		}
		conn, retryErr := c.Connector.Connect(ctx)
		if retryErr == nil {
			log.Printf("%s: postgres refused the injected password; %s had a newer one and the connection succeeded", logDBRefreshed, stage)
			return conn, nil
		}
		if !isPgAuthFailure(retryErr) {
			c.reportUnavailable(retryErr)
			return nil, retryErr
		}
	}
	c.reportUnavailable(err)
	return nil, err
}

// reportUnavailable is the line an operator can alarm on. It is rate-limited
// because a broken database produces one of these per connection attempt, and the
// first one is the only one that carries information.
func (c *refreshingConnector) reportUnavailable(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.logged.IsZero() && time.Since(c.logged) < time.Minute {
		return
	}
	c.logged = time.Now()
	log.Printf("%s: cannot open a postgres connection: %v", logDBUnavailable, err)
}
