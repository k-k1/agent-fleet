// Package secrets は Workspace の暗号化資格情報ストア（docs/23 残① Wave B で
// package main から抽出）。git トークン・Claude OAuth トークン・Bitbucket の
// リフレッシュ資格情報・opencode の API キーを単一ストアに保持する。
// ディスク上のフォーマット（secrets.enc / secrets.json）は抽出前と同一。
//
// The store is the single encrypted home for every provider credential the
// Workspace holds: git tokens, the Claude OAuth token, and Bitbucket's refresh
// creds. It replaces the former plaintext files (~/.git-credentials,
// claude-oauth-token, bitbucket.json). At rest it is AES-256-GCM sealed with a
// per-user subkey the Control Plane injects as AF_SECRET_KEY (derived from the
// deployment master key; CP never stores plaintext). With no key (dev) the same
// store is written as plaintext JSON — one code path, encryption is just the
// seal. git reads it on demand via the `workspace-agent cred` helper, so no
// plaintext credential file is ever written to the bind-mounted disk. (A3)
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

type GitEntry struct {
	User  string `json:"user"`
	Token string `json:"token"`
	Login string `json:"login,omitempty"` // cached real provider account/handle (resolved from the API)
	Email string `json:"email,omitempty"` // cached account email (resolved from the API)
}

// GitIdentity is a provider's explicit commit identity (user.name / user.email),
// decoupled from the credential entry so it works for either connect method (token or
// OAuth). Empty fields fall back to the resolved account (Login/Email).
type GitIdentity struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// BitbucketCreds are the Bitbucket OAuth refresh creds (access tokens expire in
// ~2h; the cred helper refreshes on demand — git_oauth.go in package main).
type BitbucketCreds struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Expiry       int64  `json:"expiry"` // unix seconds
	Key          string `json:"key"`
	Secret       string `json:"secret"`
	Account      string `json:"account,omitempty"` // cached real Bitbucket handle (resolved from the API)
	Email        string `json:"email,omitempty"`   // cached account email (resolved from the API)
}

type Data struct {
	Git         map[string]GitEntry    `json:"git"`                   // host -> https cred
	GitIdentity map[string]GitIdentity `json:"gitIdentity,omitempty"` // host -> explicit commit identity
	Claude      string                 `json:"claude"`                // CLAUDE_CODE_OAUTH_TOKEN
	Bitbucket   *BitbucketCreds        `json:"bitbucket"`             // OAuth refresh creds (bitbucket.org)
	Opencode    map[string]string      `json:"opencode"`              // provider env var name -> API key (injected for opencode sessions)
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

// Path is the on-disk location of the store (encrypted or plaintext by key presence).
func Path() string {
	name := "secrets.json"
	if agentSecretKey() != nil {
		name = "secrets.enc"
	}
	return filepath.Join(paths.AgentConfigDir(), name)
}

func Load() (*Data, error) {
	s := &Data{Git: map[string]GitEntry{}}
	b, err := os.ReadFile(Path())
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
		s.Git = map[string]GitEntry{}
	}
	return s, nil
}

func (s *Data) Save() error {
	p := Path()
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
