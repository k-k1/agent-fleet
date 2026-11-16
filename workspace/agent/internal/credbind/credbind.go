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
// fallback): the login's generation, which the Agent moves (Op.Finish) whenever one of its own flows
// replaces or removes the login. A token refresh writes no generation and keeps the binding. A
// login made by hand outside the Agent's flows is not seen, and keeps the generation it found.
//
// "" (unproven) whenever the state cannot be trusted: unreadable or malformed, a login change
// begun (Begin) and not yet finished, which survives a restart, or a bump of this login that
// failed to persist in this process. Reading such a state as generation zero, or as the generation
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
	// replaced login's, so it is not served until a bump succeeds. The durable half is the pending
	// marker Begin wrote before the change, which a failed Finish leaves in place.
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

// pendingPrefix names the pending markers in the Agent config directory: one file per login and
// operation, "<prefix><login>.<op id>", written by Begin before the Agent changes the login and
// removed by that operation's Finish. Owning the marker per operation is what keeps one
// completed change from clearing the protection of another still in flight (a device login left
// open while a disconnect completes).
const pendingPrefix = "route-generation-pending."

func markerPath(login, op string) string {
	return filepath.Join(paths.AgentConfigDir(), pendingPrefix+login+"."+op)
}

// markersOf lists login's pending markers by operation id. ok=false when the directory cannot
// be read: nothing is then proven absent.
func markersOf(login string) (ops []string, ok bool) {
	ents, err := os.ReadDir(paths.AgentConfigDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	prefix := pendingPrefix + login + "."
	for _, e := range ents {
		if n := e.Name(); strings.HasPrefix(n, prefix) {
			ops = append(ops, strings.TrimPrefix(n, prefix))
		}
	}
	return ops, true
}

// markerAbsent reports that login has no pending marker at all; one that cannot be checked is
// not proven absent.
func markerAbsent(login string) bool {
	ops, ok := markersOf(login)
	return ok && len(ops) == 0
}

// Durability seams: the protocol is ordered by fsync, and a test injects failures or records
// the order through these.
var (
	syncFile = func(f *os.File) error { return f.Sync() }
	syncDir  = func(dir string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
	removeFile = os.Remove
)

// opMu serialises the operations' transactions: a Begin's registration and marker creation, and
// a Finish's whole bump-then-clean-up. Without it a Begin landing between a Finish's look at the
// live set and its scan of the markers reads as an earlier process's orphan and loses its
// protection while its CLI may still write, and two Finishes interleaving could let one clear a
// marker that a later failed bump left. Under it, a marker a Finish clears is always superseded
// by the generation that same Finish has just made durable.
//
// liveOps are the operations begun in this process and not yet finished. A marker of an operation
// not in it belongs to an earlier Agent process, or to one whose own bump failed; a completed
// change may clear it, because the generation it wrote in the same transaction is fresh and
// supersedes whatever that operation did.
var (
	opMu    sync.Mutex
	liveOps = map[string]bool{}
)

// Op is one credential change the Agent is making: begun durably before it starts, finished
// when it ends, whatever its outcome.
type Op struct {
	id     string
	logins []string
	once   sync.Once
}

// Begin records, durably and before anything changes, that the Agent is about to replace or
// remove each login. Until this operation's Finish has persisted a new generation, the logins'
// generation is unproven, across an Agent restart and a machine crash too: a change whose new
// generation could not be persisted can never be read back as the old one. The marker's file and
// its directory are synced before Begin returns. An error means nothing could be recorded, and
// the caller must not change the login: proceeding would leave a replaced login nothing to tell
// it from its predecessor but process memory.
func Begin(logins ...string) (*Op, error) {
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	op := &Op{id: id, logins: logins}
	dir := paths.AgentConfigDir()
	opMu.Lock()
	defer opMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	liveOps[id] = true
	fail := func(err error) (*Op, error) {
		for _, l := range logins {
			_ = removeFile(markerPath(l, id))
		}
		delete(liveOps, id)
		return nil, err
	}
	for _, l := range logins {
		f, err := os.OpenFile(markerPath(l, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fail(err)
		}
		if err := syncFile(f); err != nil {
			f.Close()
			return fail(err)
		}
		if err := f.Close(); err != nil {
			return fail(err)
		}
	}
	if err := syncDir(dir); err != nil {
		return fail(err)
	}
	return op, nil
}

// Finish ends the operation, whatever its outcome: it moves the logins to a new generation, and
// only once that is durable removes this operation's markers and those left by an earlier Agent
// process, then syncs the directory. Markers of other operations still live in this process stay,
// so their logins stay unproven until they finish too. A change that never happened (a flow
// cancelled before the CLI wrote anything) still moves the generation: a spurious new binding
// reads as a replacement and is refused, which is the safe direction. When the new generation
// cannot be persisted, every marker stays and the logins stay unproven. Safe on nil and to call
// more than once.
func (o *Op) Finish() {
	if o == nil {
		return
	}
	o.once.Do(func() {
		opMu.Lock()
		defer opMu.Unlock()
		defer delete(liveOps, o.id)
		if !bump(o.logins...) {
			return
		}
		dir := paths.AgentConfigDir()
		live := liveOps // read under opMu: no Begin can register between here and the scan
		removed := false
		for _, l := range o.logins {
			ops, _ := markersOf(l)
			for _, id := range ops {
				if id != o.id && live[id] {
					continue // another change of this login is still in flight
				}
				if err := removeFile(markerPath(l, id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
					log.Printf("credbind: clearing a pending mark of %s: %v; its binding stays unproven", l, err)
					continue
				}
				removed = true
			}
		}
		if removed {
			if err := syncDir(dir); err != nil {
				log.Printf("credbind: syncing the config directory after clearing pending marks: %v", err)
			}
		}
	})
}

// bump moves each login to a new generation and reports whether that is durable. It is not
// exported on purpose: the poison mark below lives in process memory, and the removal of the
// state that follows a failed persist fails with the persist when the directory itself is the
// problem, so a restart would serve the replaced login's old generation. Only the pending marker
// Begin wrote before the change survives that, hence Op.Finish is the one way in. An unreadable
// state is rebuilt under a new epoch (every login's generation moves); a state that cannot be
// written leaves these logins unproven until a bump succeeds.
func bump(logins ...string) bool {
	genMu.Lock()
	defer genMu.Unlock()
	if err := bumpLocked(logins); err != nil {
		log.Printf("credbind: bump %v: %v; their bindings stay unproven", logins, err)
		for _, l := range logins {
			genPoisoned[l] = true
		}
		// Best effort, for this process and for a restart that can write: the next read starts a
		// fresh epoch. When this removal fails as well, only the pending marker protects a restart.
		if rerr := os.Remove(filepath.Join(paths.AgentConfigDir(), genFile)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			log.Printf("credbind: removing the generation state after a failed bump: %v", rerr)
		}
		return false
	}
	for _, l := range logins {
		delete(genPoisoned, l)
	}
	return true
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
	// Ordered for a machine crash: the new state's bytes, then its name, then the directory
	// entry, so nothing that follows (a pending marker's removal) can outlive it.
	_, werr := tmp.Write(b)
	if werr == nil {
		werr = syncFile(tmp)
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), filepath.Join(dir, genFile))
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return werr
	}
	return syncDir(dir)
}
