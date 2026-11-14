// Package credbind computes the non-secret bindings a launch record and a ledger row carry (ADR
// 0138 D5b): an opaque name for one credential instance that stays the same across a token
// refresh and changes when a login is replaced or a key is new.
//
// A plain digest of a key is an offline verifier: a short or guessable key, an e-mail address,
// or an endpoint plus a key can be recovered by hashing candidates against the published value,
// and the records travel to the Control Plane and the Console. So every binding is an HMAC under
// a random key that exists only in this Workspace's Agent config directory (denylisted from the
// file browser with the rest of it), with the kind of binding and its slot in the message for
// domain separation. Without that key no binding is produced at all: an unproven binding is
// honest, a weaker one would not be.
package credbind

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

const (
	keyFile = "route-binding.key"
	genFile = "route-generations.json"
	keyLen  = 32
)

var (
	keyMu  sync.Mutex
	keyVal []byte
	keyDir string // the directory keyVal was read from; a test that moves HOME reads again
)

// ResetForTest forgets the cached binding key, so the next binding reads the key file again.
func ResetForTest() {
	keyMu.Lock()
	keyVal, keyDir = nil, ""
	keyMu.Unlock()
}

// bindingKey returns the Workspace's binding key, creating it on first use. nil when it can be
// neither read nor created.
func bindingKey() []byte {
	dir := paths.AgentConfigDir()
	keyMu.Lock()
	defer keyMu.Unlock()
	if keyVal != nil && keyDir == dir {
		return keyVal
	}
	p := filepath.Join(dir, keyFile)
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		b, err = createKey(dir, p)
	}
	if err != nil || len(b) != keyLen {
		log.Printf("credbind: no binding key (%v); bindings stay unproven", err)
		return nil
	}
	keyVal, keyDir = b, dir
	return keyVal
}

// createKey writes a new key with O_EXCL, so two Agents racing on one home agree on one key: the
// loser reads the winner's.
func createKey(dir, p string) ([]byte, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	b := make([]byte, keyLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return os.ReadFile(p)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = os.Remove(p)
		return nil, err
	}
	return b, f.Close()
}

// mac is the keyed digest of one binding: 16 hex digits of HMAC-SHA256 over the binding's kind,
// its slot and its value, NUL-separated. "" without a key.
func mac(kind, slot, value string) string {
	k := bindingKey()
	if k == nil {
		return ""
	}
	h := hmac.New(sha256.New, k)
	h.Write([]byte("af-route-binding/v1\x00" + kind + "\x00" + slot + "\x00" + value))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func join(slot, digest string) string {
	if digest == "" {
		return ""
	}
	return slot + "@" + digest
}

// Key is the binding of an API key held in slot (an environment variable name, or a CLI's own
// key slot such as "codex-api-key"): "<slot>@<digest>". The slot keeps two copies of one key
// apart (codex's own copy and the shared OPENAI_API_KEY are both openai-api, ADR 0138 D2). ""
// for an empty key.
func Key(slot, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return join(slot, mac("key", slot, key))
}

// Account is the binding of a login whose credential names its account: "<login>@<digest>",
// login naming the login (normally its route id). A token refresh keeps the account and so the
// binding; a sign-in to another account changes it. Never pass a token here: tokens rotate. ""
// for an empty id.
func Account(login, accountID string) string {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ""
	}
	return join(login, mac("account", login, accountID))
}

// Endpoint is the binding of a connection to a member-set endpoint with its key: repointing the
// endpoint or replacing the key is another connection.
func Endpoint(route, url, key string) string {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if url == "" {
		return ""
	}
	return join(route, mac("endpoint", route, url+"\x00"+strings.TrimSpace(key)))
}

// Named is the binding of a connection whose name is neither secret nor credential-derived (the
// deployment engine's catalogue key, whose token is minted per session): "<route>@<name>", the
// same in every Workspace, since the deployment's engine is one connection for all of them.
func Named(route, name string) string { return route + "@" + name }

// Generation is the binding of a login whose credential carries no account id at all (D5b's
// fallback): the login's generation, which the Agent moves (Bump) whenever one of its own flows
// replaces or removes the login. A token refresh writes no generation and keeps the binding. A
// login made by hand outside the Agent's flows is not seen, and keeps the generation it found.
//
// "" (unproven) whenever the state cannot be trusted: unreadable or malformed, a login change
// begun (Begin) and not yet bumped, which survives a restart, or a Bump of this login that failed
// to persist in this process. Reading such a state as generation zero, or as the generation
// before the change, would hand a replaced login its predecessor's binding.
func Generation(login string) string {
	genMu.Lock()
	defer genMu.Unlock()
	if genPoisoned[login] || !markerAbsent(login) {
		return ""
	}
	st, ok := readGenerations()
	if !ok {
		return ""
	}
	if st.Epoch == "" {
		// No state yet, or a state that was lost: start a fresh epoch and persist it before any
		// generation is served, so a lost state can never reissue a predecessor's binding.
		epoch, err := randomToken()
		if err != nil {
			return ""
		}
		st = genState{Epoch: epoch, Logins: map[string]string{}}
		if err := writeGenerations(st); err != nil {
			log.Printf("credbind: starting the generation state: %v; bindings stay unproven", err)
			return ""
		}
	}
	tok := st.Logins[login]
	return join(login, mac("generation", login, st.Epoch+"\x00"+tok))
}

// AccountOrGeneration is Account when the login names its account, else Generation.
func AccountOrGeneration(login, accountID string) string {
	if strings.TrimSpace(accountID) != "" {
		return Account(login, accountID)
	}
	return Generation(login)
}

var (
	genMu sync.Mutex
	// genPoisoned holds the logins whose Bump failed to persist: their generation on disk is the
	// replaced login's, so it is not served until a Bump succeeds. The durable half is the pending
	// marker Begin wrote before the change, which a failed Bump leaves in place.
	genPoisoned = map[string]bool{}
)

// genState is route-generations.json. A login's generation is a random token, never a counter,
// and the state carries a random epoch chosen when it is first written, so a state that is lost,
// rebuilt after corruption or started afresh can never hand out a value an earlier state used:
// every login's generation moves with the epoch, the never-bumped ones included.
type genState struct {
	Epoch  string            `json:"epoch,omitempty"`
	Logins map[string]string `json:"logins"`
}

// readGenerations reads the state: ok=false when it exists but cannot be read or parsed, or
// carries no epoch. A missing file is the empty state with no epoch, which Generation and Bump
// replace with a fresh one before using it.
func readGenerations() (genState, bool) {
	st := genState{Logins: map[string]string{}}
	b, err := os.ReadFile(filepath.Join(paths.AgentConfigDir(), genFile))
	if errors.Is(err, fs.ErrNotExist) {
		return st, true
	}
	if err != nil {
		return st, false
	}
	var got genState
	if json.Unmarshal(b, &got) != nil || got.Logins == nil || got.Epoch == "" {
		return st, false
	}
	return got, true
}

func randomToken() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// pendingPrefix names a login's pending marker in the Agent config directory: written by Begin
// before the Agent changes the login, removed by the Bump that records the change.
const pendingPrefix = "route-generation-pending."

func markerPath(login string) string {
	return filepath.Join(paths.AgentConfigDir(), pendingPrefix+login)
}

// markerAbsent reports that login has no pending marker; a marker that cannot be checked is not
// proven absent.
func markerAbsent(login string) bool {
	_, err := os.Stat(markerPath(login))
	return errors.Is(err, fs.ErrNotExist)
}

// Begin records, durably and before anything changes, that the Agent is about to replace or
// remove each login. Until the Bump that records the change succeeds, the login's generation is
// unproven, across an Agent restart too: a change whose new generation could not be persisted
// can then never be read back as the old one. An error means nothing could be recorded,
// and the caller must not change the login: proceeding would leave a replaced login nothing to
// tell it from its predecessor but process memory.
//
// A flow that is begun and then abandoned leaves its marker, so the login's generation stays
// unproven until its next completed change: unknown is the safe answer when it cannot be told
// whether the file was replaced.
func Begin(logins ...string) error {
	dir := paths.AgentConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, l := range logins {
		f, err := os.OpenFile(markerPath(l), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Bump moves each login to a new generation. Called by the Agent's own login, key and disconnect
// handlers when they replace or remove the credential, never on a refresh. An unreadable state is
// rebuilt under a new epoch (every login's generation moves); a state that cannot be written
// leaves these logins unproven until a Bump succeeds.
func Bump(logins ...string) {
	genMu.Lock()
	defer genMu.Unlock()
	if err := bumpLocked(logins); err != nil {
		log.Printf("credbind: bump %v: %v; their bindings stay unproven", logins, err)
		for _, l := range logins {
			genPoisoned[l] = true
		}
		// The state on disk now holds the replaced logins' generations. The pending marker Begin
		// wrote keeps them unproven across a restart; removing the state as well makes the next
		// read start a fresh epoch for a caller that did not Begin.
		if rerr := os.Remove(filepath.Join(paths.AgentConfigDir(), genFile)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			log.Printf("credbind: removing the generation state after a failed bump: %v", rerr)
		}
		return
	}
	for _, l := range logins {
		delete(genPoisoned, l)
		// The change is recorded; a marker that cannot be removed leaves the login unproven,
		// never wrong.
		if err := os.Remove(markerPath(l)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("credbind: clearing the pending mark of %s: %v; its binding stays unproven", l, err)
		}
	}
}

func bumpLocked(logins []string) error {
	st, ok := readGenerations()
	if !ok || st.Epoch == "" {
		epoch, err := randomToken()
		if err != nil {
			return err
		}
		st = genState{Epoch: epoch, Logins: map[string]string{}}
	}
	for _, l := range logins {
		tok, err := randomToken()
		if err != nil {
			return err
		}
		st.Logins[l] = tok
	}
	return writeGenerations(st)
}

// writeGenerations replaces the state file atomically (temp file and rename).
func writeGenerations(st genState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	dir := paths.AgentConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, genFile+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), filepath.Join(dir, genFile))
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}
