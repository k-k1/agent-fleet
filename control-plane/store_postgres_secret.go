package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/jackc/pgx/v5/pgconn"
)

// Keeping the Postgres password current while the deployment runs.
//
// 🔥 2026-09-01 14:10 JST, the acrt deployment served 500 on every /api/* for fifteen
// minutes. Nothing had been deployed. RDS rotated its managed master password
// (Secrets Manager `rds!db-…`, RotationRules.AutomaticallyAfterDays = 7) and the
// running CP task went on presenting the old one:
//
//	failed SASL auth: FATAL: password authentication failed for user "afadmin" (SQLSTATE 28P01)
//
// The mechanism is that **ECS resolves a task definition's `secrets` exactly once,
// at task start**. An env var is a snapshot; a rotating password is not a value you
// can snapshot. So the env var stays — it is how the process boots without an AWS
// round trip, and it is the whole story on-prem — but it is demoted to a bootstrap
// hint. The truth is whatever Secrets Manager says at the moment a connection is
// opened, which is what this file goes and asks.
//
// Everything here is inert unless AF_DB_PASSWORD_SECRET_ARN is set: no client is
// built, no API is called, and the password is the one that came in the DSN. That
// is deliberate — compose, on-prem, SQLite and the tests must behave exactly as
// they did before.
const (
	stageCurrent = "AWSCURRENT"
	// AWSPENDING covers the seconds-long hole in the middle of a rotation. The
	// rotation function calls setSecret (the DATABASE now has the new password)
	// before finishSecret (AWSCURRENT now points at it), so in between, the label
	// everyone reads is the one that no longer works and the one that works has no
	// label yet. Asking for AWSPENDING is how you get through that window.
	//
	// AWSPREVIOUS is deliberately NOT consulted: it is the other direction — a
	// password the database has already stopped accepting.
	stagePending = "AWSPENDING"
)

// Log markers. These are a contract with the CloudWatch metric filter in
// deploy/aws/ecs/cfn/30-ingress.yaml (CpDbUnavailableFilter): grep for the marker
// there before renaming one here. The 2026-09-01 outage was invisible from
// outside — /healthz stayed 200 throughout — so the only thing that can raise a
// hand is a line in this log.
const (
	logDBUnavailable = "DB_UNAVAILABLE"           // could not open a connection; this is the outage
	logDBRefreshed   = "DB_CREDENTIALS_REFRESHED" // recovered by re-reading the secret; informational
	logDBSecretFail  = "DB_SECRET_REFRESH_FAILED" // could not read the secret (IAM?); the safety net is gone
)

// secretsGetter is the one Secrets Manager call this needs, as an interface so the
// tests do not reach for AWS.
type secretsGetter interface {
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// dbPasswordSource holds the password the Postgres pool authenticates with and
// knows how to go and get a fresher one. Safe for concurrent use: every new
// physical connection reads it, and several may fail at once when a rotation lands.
type dbPasswordSource struct {
	arn string // "" = static password, nothing below ever runs
	key string // JSON field inside the secret; "" = the secret is the password

	mu     sync.Mutex
	pw     string
	last   map[string]time.Time // per-stage throttle
	minGap time.Duration
	warned time.Time // rate-limits logDBSecretFail
	client secretsGetter

	// fetch is swapped out in tests. Production wiring is fetchFromSecretsManager.
	fetch func(ctx context.Context, arn, stage string) (string, error)
}

// newDBPasswordSource builds the source. arn empty is the normal case everywhere
// except ECS+RDS, and makes every method below a no-op.
func newDBPasswordSource(arn, key string) *dbPasswordSource {
	s := &dbPasswordSource{
		arn:    strings.TrimSpace(arn),
		key:    key,
		last:   map[string]time.Time{},
		minGap: 5 * time.Second,
	}
	s.fetch = s.fetchFromSecretsManager
	return s
}

// seed records the password the DSN was built with. It is what the pool uses until
// something fails, so a deployment whose IAM does not allow GetSecretValue still
// boots and still works — it just loses the safety net (and says so in the log).
func (s *dbPasswordSource) seed(pw string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pw = pw
}

func (s *dbPasswordSource) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pw
}

// refresh re-reads the password at `stage` and returns it when it is something
// other than `tried` — the value the caller just failed to authenticate with.
//
// ok=false means "nothing new to try", and the caller should give up rather than
// spin: no secret is configured, the throttle window is still open, the store
// errored, or it handed back the very password that just failed.
func (s *dbPasswordSource) refresh(ctx context.Context, stage, tried string) (string, bool) {
	if s.arn == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Another connection hit the same wall a moment ago and already fixed it. Use
	// its answer instead of making a second identical API call — a rotation makes
	// every pooled connection fail at once, and the throttle below would otherwise
	// turn that stampede into "all but the first one give up".
	if s.pw != tried {
		return s.pw, true
	}
	if t, seen := s.last[stage]; seen && time.Since(t) < s.minGap {
		return "", false
	}
	s.last[stage] = time.Now()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pw, err := s.fetch(ctx, s.arn, stage)
	if err != nil {
		s.logFetchFailureLocked(stage, err)
		return "", false
	}
	if pw == tried {
		return "", false // the store agrees with the password that just failed
	}
	s.pw = pw
	return pw, true
}

// logFetchFailureLocked keeps the failure loud the first time and once a minute
// after that. AccessDenied here is the quiet way this whole mechanism disappears —
// see the CpTaskRole warning in deploy/aws/ecs/cfn/20-platform.yaml.
func (s *dbPasswordSource) logFetchFailureLocked(stage string, err error) {
	if !s.warned.IsZero() && time.Since(s.warned) < time.Minute {
		return
	}
	s.warned = time.Now()
	log.Printf("%s: secrets manager %s %s: %v", logDBSecretFail, s.arn, stage, err)
}

func (s *dbPasswordSource) fetchFromSecretsManager(ctx context.Context, arn, stage string) (string, error) {
	if s.client == nil {
		region := regionFromSecretARN(arn)
		if region == "" {
			return "", fmt.Errorf("cannot read a region out of %q", arn)
		}
		cfg, err := awsConfigFor(ctx, region)
		if err != nil {
			return "", err
		}
		s.client = secretsmanager.NewFromConfig(cfg)
	}
	out, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId:     aws.String(arn),
		VersionStage: aws.String(stage),
	})
	if err != nil {
		return "", err
	}
	if out.SecretString == nil {
		return "", errors.New("secret has no string value")
	}
	return dbPasswordFromSecret(*out.SecretString, s.key)
}

// dbPasswordFromSecret pulls the password out of the secret's payload. RDS-managed
// secrets are JSON ({"username":…,"password":…}); key "" means the secret IS the
// password, which is what a hand-rolled deployment is likely to have.
func dbPasswordFromSecret(secret, key string) (string, error) {
	if key == "" {
		return secret, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(secret), &m); err != nil {
		return "", fmt.Errorf("secret is not JSON, so it has no %q field: %w", key, err)
	}
	v, ok := m[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("secret has no non-empty string field %q", key)
	}
	return v, nil
}

// regionFromSecretARN reads the region out of
// arn:aws:secretsmanager:<region>:<account>:secret:<name>-<suffix>. Taking it from
// the ARN rather than from AF_ECS_REGION keeps this correct on a deployment whose
// database lives in another region, and needs no new parameter.
func regionFromSecretARN(arn string) string {
	p := strings.SplitN(arn, ":", 6)
	if len(p) < 6 || p[0] != "arn" || p[2] != "secretsmanager" || p[3] == "" {
		return ""
	}
	return p[3]
}

// isPgAuthFailure reports whether err is Postgres refusing the credentials, as
// opposed to the host being down or the TLS handshake failing. pgx wraps the
// server's ErrorResponse in *pgconn.ConnectError (and, with fallbacks, in a joined
// error), both of which errors.As walks.
//
//	28P01 invalid_password
//	28000 invalid_authorization_specification
func isPgAuthFailure(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "28P01" || pgErr.Code == "28000"
}
