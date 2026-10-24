package egressagent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
)

// Config is the agent's wiring. FromEnv fills it from the environment.
type Config struct {
	PolicyURL string // the CP's GET /internal/egress/policy
	IngestURL string // the CP's POST /internal/egress
	Token     string // AF_EGRESS_TOKEN, the bearer both routes check

	AllowFile string   // the dstdomain file Squid's af_allowed ACLs read
	ModeFile  string   // the include holding the log-only / enforce decisions
	ReloadCmd []string // run after either file changed; empty = do not reload

	// GatewayID names this gateway in every event id, so two gateways never produce the
	// same id. It must stay the same across restarts of the agent on one host, or a resend
	// after a lost state file would be stored twice: FromEnv takes the machine id.
	GatewayID string

	LogPath   string // Squid's access_log in LogFormat
	StatePath string // committed offsets, by log file identity

	PolicyEvery time.Duration
	ShipEvery   time.Duration
	Client      *http.Client
}

// FromEnv reads the agent's configuration. The URL and token variables are the ones the
// own forward proxy uses, so a deployment configures the CP side once.
func FromEnv() Config {
	return Config{
		PolicyURL:   envx.Or("AF_EGRESS_POLICY_URL", ""),
		IngestURL:   envx.Or("AF_EGRESS_INGEST_URL", ""),
		Token:       envx.Or("AF_EGRESS_TOKEN", ""),
		AllowFile:   envx.Or("AF_EGRESS_SQUID_ALLOW_FILE", "/etc/squid/af-allow.txt"),
		ModeFile:    envx.Or("AF_EGRESS_SQUID_MODE_FILE", "/etc/squid/af-mode.conf"),
		ReloadCmd:   strings.Fields(envx.Or("AF_EGRESS_SQUID_RELOAD", "squid -k reconfigure")),
		GatewayID:   envx.Or("AF_EGRESS_GATEWAY_ID", machineID()),
		LogPath:     envx.Or("AF_EGRESS_SQUID_LOG", "/var/log/squid/af-egress.log"),
		StatePath:   envx.Or("AF_EGRESS_AGENT_STATE", "/var/lib/af-egress-agent/state.json"),
		PolicyEvery: 30 * time.Second,
		ShipEvery:   5 * time.Second,
	}
}

// machineID is the host's systemd machine id, or its host name where there is none.
func machineID() string {
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	h, _ := os.Hostname()
	return h
}

// requestTimeout bounds one CP request; a hung CP must not wedge either loop.
const requestTimeout = 15 * time.Second

// maxShipBackoff caps the wait between passes while the CP keeps refusing reports.
const maxShipBackoff = time.Minute

// Run starts both loops and returns when ctx ends, or at once on a configuration error.
func Run(ctx context.Context, cfg Config) error {
	if cfg.PolicyURL == "" || cfg.IngestURL == "" || cfg.Token == "" {
		return errors.New("AF_EGRESS_POLICY_URL, AF_EGRESS_INGEST_URL and AF_EGRESS_TOKEN are required")
	}
	if cfg.GatewayID == "" {
		return errors.New("no gateway id: set AF_EGRESS_GATEWAY_ID (no /etc/machine-id or host name)")
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: requestTimeout}
	}
	st, err := loadState(cfg.StatePath)
	if err != nil {
		return err
	}
	// Fail at start when the offset cannot be stored: otherwise every pass would POST a
	// batch, fail to commit it, and POST it again, forever.
	if err := os.MkdirAll(filepath.Dir(cfg.StatePath), 0o755); err != nil {
		return err
	}
	if err := st.save(cfg.StatePath); err != nil {
		return fmt.Errorf("state %s is not writable: %w", cfg.StatePath, err)
	}
	pol := newPolicySync(&cfg)
	pol.loadFromDisk()
	sh := &shipper{cfg: &cfg, pol: pol, state: st}

	done := make(chan struct{})
	go func() {
		defer close(done)
		pol.loop(ctx)
	}()
	sh.loop(ctx)
	<-done
	return nil
}

func (s *policySync) loop(ctx context.Context) {
	t := time.NewTicker(s.cfg.PolicyEvery)
	defer t.Stop()
	for {
		if err := s.sync(ctx); err != nil && ctx.Err() == nil {
			log.Printf("egress-agent: policy not updated, keeping the last one that worked: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *shipper) loop(ctx context.Context) {
	wait := s.cfg.ShipEvery
	for {
		err := s.pass(ctx)
		switch {
		case err == nil:
			wait = s.cfg.ShipEvery
		case errors.Is(err, errShipFailed):
			wait = min(wait*2, maxShipBackoff)
		default:
			log.Printf("egress-agent: reading the log: %v", err)
			wait = min(wait*2, maxShipBackoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
