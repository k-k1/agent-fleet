// Package cloudbridge is the provider-neutral pull of a member's cloud profiles from the
// Control Plane (ADR 0107 "What carries over from AWS"): a per-membership bridge token,
// a pull at Agent boot and every PollInterval, each under a lock the backend names, and a
// cache of the last list the CP gave, bound to the token, for when the CP cannot be
// asked.
//
// A backend (internal/awsx; internal/gcpx for ADR 0107) supplies the profile type, the
// lock, and what applying a list means (the managed block of ~/.aws/config, gcloud
// configurations). This package must import no backend.
package cloudbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// PollInterval matches the other CP-backed pulls: an edit in Settings lands without
// anyone asking, and the wrappers pull on demand for the "use it right now" case.
const PollInterval = 5 * time.Minute

// Conflict is a profile name two or more Settings labels map to.
type Conflict struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

// List is the CP's answer: the exported profiles, and the names it left out because two
// labels collide.
type List[P any] struct {
	Profiles  []P        `json:"profiles"`
	Conflicts []Conflict `json:"conflicts"`
}

// Bridge is one backend's pull. P is its profile as the CP sends it.
type Bridge[P any] struct {
	// Path is the CP route ("/internal/aws-profiles").
	Path string
	// TokenEnv names the variable that holds the bridge token ("AF_AWS_PROFILES_TOKEN").
	TokenEnv string
	// What names the profiles in error messages ("AWS profiles").
	What string
	// CacheFile is the cache's file name under paths.AgentStateDir().
	CacheFile string
	// OwnerLabel separates this backend's cache digest from any other use of the token.
	OwnerLabel string
	// Target names what Apply writes, for the message when the cache cannot be saved.
	Target string
	// ErrOff reports that this deployment injects no bridge.
	ErrOff error
}

// Configured reports whether the deployment injects the bridge: a CP URL and a token.
func (b *Bridge[P]) Configured() bool {
	return os.Getenv("AF_CP_BASE_URL") != "" && os.Getenv(b.TokenEnv) != ""
}

// Fetch pulls the member's profiles from the CP.
func (b *Bridge[P]) Fetch() (List[P], error) {
	base := cpurl.Request()
	token := os.Getenv(b.TokenEnv)
	if base == "" || token == "" {
		return List[P]{}, b.ErrOff
	}
	req, err := http.NewRequest(http.MethodGet, base+b.Path, nil)
	if err != nil {
		return List[P]{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return List[P]{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return List[P]{}, fmt.Errorf("CP %s API error (%d): %s", b.What, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var wire List[P]
	if err := json.Unmarshal(body, &wire); err != nil {
		return List[P]{}, fmt.Errorf("CP %s response is not JSON: %w", b.What, err)
	}
	return wire, nil
}

// Pulled is the list one Pull chose, and where it came from.
type Pulled[P any] struct {
	List[P]
	// Have is true when a list was chosen (from the CP or the cache).
	Have bool
	// Fetched is true when the list came from the CP on this call, even if applying it
	// then failed: fresh answers must not be swapped for the cache.
	Fetched bool
	// FromCache is true when the CP could not be asked and the cached list was applied.
	FromCache bool
}

// Pull pulls and applies under lock. When the CP cannot be reached it applies the last
// list the CP gave (the cache) instead, so a CP blip never takes a working profile away;
// without a cache nothing is applied and the fetch error is returned.
//
// Everything runs under the one lock across processes: fetching the list, choosing it
// (the CP's answer, or the cache when the CP cannot be asked), saving the cache and
// applying. Otherwise two runs can commit out of order: one that fetched (or read the
// cache) earlier could apply its older list after a newer one. Every run asks the CP
// itself; parallel runs queue their fetches one after another. That costs latency (one CP
// round trip per run, up to the 10 s timeout each while the CP is down), and it is kept
// that way on purpose: every shortcut tried here (reusing another run's fetch, a cooldown
// after a failure) could serve a list from before a Settings edit.
func (b *Bridge[P]) Pull(lock func() (func(), error), apply func(List[P]) error) (Pulled[P], error) {
	if !b.Configured() {
		return Pulled[P]{}, b.ErrOff
	}
	unlock, err := lock()
	if err != nil {
		return Pulled[P]{}, err
	}
	defer unlock()
	l, err := b.Fetch()
	if err != nil {
		cached, ok := b.Cached()
		if !ok {
			return Pulled[P]{}, err
		}
		p := Pulled[P]{List: cached, Have: true}
		if aerr := apply(cached); aerr != nil {
			return p, fmt.Errorf("%v; applying the last copy also failed: %w", err, aerr)
		}
		p.FromCache = true
		return p, err
	}
	p := Pulled[P]{List: l, Have: true, Fetched: true}
	// The cache is saved BEFORE the list is applied, and the list is only applied once it
	// is: the cache is then never older than what was applied, so a later offline apply
	// cannot put an older list back (fail closed: a cache that cannot be saved leaves
	// everything as it is, and an older cache is removed).
	if serr := b.SaveCache(l); serr != nil {
		_ = os.Remove(b.CachePath())
		return p, fmt.Errorf("could not save the Settings cache (%w); %s was left as it is", serr, b.Target)
	}
	return p, apply(l)
}

// Poll runs pull once at Agent boot and then every PollInterval. The first pull is in the
// goroutine too: boot must not wait on the CP.
func Poll(pull func(why string)) {
	go func() {
		pull("agent boot")
		for range time.Tick(PollInterval) {
			pull("poll")
		}
	}()
}

// CachePath keeps the last list the CP sent (non-secret), so the wrapper's checks and
// the offline apply still have something to go on when the CP cannot be reached. It lives
// in the Agent's state directory, not in the cloud's own: the user may make that
// read-only or link it elsewhere, and a cache that can be neither replaced nor removed
// would be applied stale.
func (b *Bridge[P]) CachePath() string {
	return filepath.Join(paths.AgentStateDir(), b.CacheFile)
}

type cacheFile[P any] struct {
	// Owner is a digest of the bridge token the list was fetched with. The token is per
	// membership, so a cache left in a restored or shared home by another membership is
	// never applied here.
	Owner     string     `json:"owner"`
	Profiles  []P        `json:"profiles"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// owner is the digest the cache is bound to ("" when there is no bridge token).
func (b *Bridge[P]) owner() string {
	tok := os.Getenv(b.TokenEnv)
	if tok == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(b.OwnerLabel + "\x00" + tok))
	return hex.EncodeToString(sum[:])
}

// SaveCache writes l as the last list the CP gave, bound to the current token.
func (b *Bridge[P]) SaveCache(l List[P]) error {
	data, err := json.Marshal(cacheFile[P]{Owner: b.owner(), Profiles: l.Profiles, Conflicts: l.Conflicts})
	if err != nil {
		return err
	}
	path := b.CachePath()
	if old, rerr := os.ReadFile(path); rerr == nil && string(old) == string(data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// Cached is the last list the CP gave: saved whenever a fetch succeeds, before it is
// applied, and bound to this membership; ok is false when there is none for it.
func (b *Bridge[P]) Cached() (List[P], bool) {
	data, err := os.ReadFile(b.CachePath())
	if err != nil {
		return List[P]{}, false
	}
	var c cacheFile[P]
	if json.Unmarshal(data, &c) != nil || c.Owner == "" || c.Owner != b.owner() {
		return List[P]{}, false
	}
	return List[P]{Profiles: c.Profiles, Conflicts: c.Conflicts}, true
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.af-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
