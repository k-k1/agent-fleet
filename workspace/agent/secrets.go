package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// secrets is the single encrypted store for every provider credential the
// Workspace holds: git tokens, the Claude OAuth token, and Bitbucket's refresh
// creds. It replaces the former plaintext files (~/.git-credentials,
// claude-oauth-token, bitbucket.json). At rest it is AES-256-GCM sealed with a
// per-user subkey the Control Plane injects as AF_SECRET_KEY (derived from the
// deployment master key; CP never stores plaintext). With no key (dev) the same
// store is written as plaintext JSON — one code path, encryption is just the
// seal. git reads it on demand via the `workspace-agent cred` helper, so no
// plaintext credential file is ever written to the bind-mounted disk. (A3)

type gitEntry struct {
	User  string `json:"user"`
	Token string `json:"token"`
	Login string `json:"login,omitempty"` // cached real provider account (resolved from the API)
}

type secretsData struct {
	Git       map[string]gitEntry `json:"git"`       // host -> https cred
	Claude    string              `json:"claude"`    // CLAUDE_CODE_OAUTH_TOKEN
	Bitbucket *bitbucketCreds     `json:"bitbucket"` // OAuth refresh creds (bitbucket.org)
	Opencode  map[string]string   `json:"opencode"`  // provider env var name -> API key (injected for opencode sessions)
}

// agentSecretKey returns the 32-byte per-user key from AF_SECRET_KEY (hex), or
// nil when unset/invalid (dev: store is plaintext JSON).
func agentSecretKey() []byte {
	h := os.Getenv("AF_SECRET_KEY")
	if h == "" {
		return nil
	}
	b, err := hex.DecodeString(strings.TrimSpace(h))
	if err != nil || len(b) != 32 {
		log.Printf("WARNING: AF_SECRET_KEY is set but not 32-byte hex — storing secrets in PLAINTEXT")
		return nil
	}
	return b
}

func secretsPath() string {
	name := "secrets.json"
	if agentSecretKey() != nil {
		name = "secrets.enc"
	}
	return filepath.Join(homeDir(), ".config", "agent-fleet", name)
}

func loadSecrets() (*secretsData, error) {
	s := &secretsData{Git: map[string]gitEntry{}}
	b, err := os.ReadFile(secretsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if key := agentSecretKey(); key != nil {
		if b, err = aesOpen(key, b); err != nil {
			return s, fmt.Errorf("decrypt secrets: %w", err)
		}
	}
	if err := json.Unmarshal(b, s); err != nil {
		return s, err
	}
	if s.Git == nil {
		s.Git = map[string]gitEntry{}
	}
	return s, nil
}

func (s *secretsData) save() error {
	p := secretsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if key := agentSecretKey(); key != nil {
		if b, err = aesSeal(key, b); err != nil {
			return err
		}
	}
	return os.WriteFile(p, b, 0o600)
}

func aesSeal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil // nonce prepended
}

func aesOpen(key, ct []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ct) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, ct[:ns], ct[ns:], nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ensureCredHelper makes `workspace-agent cred` the sole global git credential
// helper for every host, clearing any inherited/legacy helpers (the old `store`
// and the per-host bitbucket helper). Idempotent.
func ensureCredHelper() error {
	// --unset-all exits 5 when the key is absent; that is not an error here.
	_ = exec.Command("git", "config", "--global", "--unset-all", "credential.helper").Run()
	_ = exec.Command("git", "config", "--global", "--unset-all", "credential.https://bitbucket.org.helper").Run()
	if out, err := exec.Command("git", "config", "--global", "credential.helper", "!workspace-agent cred").CombinedOutput(); err != nil {
		return fmt.Errorf("git config credential.helper: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runCredHelper implements the git credential helper protocol backed by the
// encrypted store. git calls `workspace-agent cred get` with `host=...` on
// stdin; we emit username/password, refreshing Bitbucket's token on the fly.
func runCredHelper(args []string) {
	if len(args) == 0 || args[0] != "get" {
		return // store/erase: nothing to do
	}
	host := credHelperHost(os.Stdin)
	s, err := loadSecrets()
	if err != nil {
		return // emit nothing: git falls through / prompts
	}
	if host == "bitbucket.org" && s.Bitbucket != nil {
		c := *s.Bitbucket
		if time.Now().Unix() >= c.Expiry-120 { // refresh within 2 min of expiry
			if nc, rerr := refreshBitbucket(c); rerr == nil {
				c = nc
				s.Bitbucket = &c
				_ = s.save()
			}
		}
		fmt.Printf("username=x-token-auth\npassword=%s\n", c.AccessToken)
		return
	}
	if e, ok := s.Git[host]; ok {
		fmt.Printf("username=%s\npassword=%s\n", e.User, e.Token)
	}
}

// credHelperHost reads the credential protocol input (key=value lines until a
// blank line) and returns the requested host.
func credHelperHost(r *os.File) string {
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		if strings.HasPrefix(line, "host=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "host="))
		}
	}
	return ""
}

// migrateLegacySecrets folds any pre-A3 plaintext files into the store on start
// and deletes them, so the bind-mounted disk no longer holds plaintext. Runs
// every start; a no-op once migrated.
func migrateLegacySecrets() {
	s, err := loadSecrets()
	if err != nil {
		log.Printf("secrets migration: load failed: %v", err)
		return
	}
	home := homeDir()
	gcp := filepath.Join(home, ".git-credentials")
	bjp := filepath.Join(home, ".config", "agent-fleet", "bitbucket.json")
	ctp := filepath.Join(home, ".config", "agent-fleet", "claude-oauth-token")

	changed := false
	if data, err := os.ReadFile(gcp); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			if u, err := url.Parse(line); err == nil && u.Host != "" {
				pw, _ := u.User.Password()
				s.Git[u.Host] = gitEntry{User: u.User.Username(), Token: pw}
				changed = true
			}
		}
	}
	if b, err := os.ReadFile(bjp); err == nil {
		var c bitbucketCreds
		if json.Unmarshal(b, &c) == nil && c.AccessToken != "" {
			s.Bitbucket = &c
			changed = true
		}
	}
	if b, err := os.ReadFile(ctp); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			s.Claude = t
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := s.save(); err != nil {
		log.Printf("secrets migration: save failed (keeping legacy files): %v", err)
		return
	}
	for _, p := range []string{gcp, bjp, ctp} {
		_ = os.Remove(p)
	}
	_ = ensureCredHelper()
	log.Printf("secrets: migrated legacy plaintext credentials into %s", filepath.Base(secretsPath()))
}
