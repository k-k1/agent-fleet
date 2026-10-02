package cloudbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type testProfile struct {
	Name string `json:"name"`
}

var errOff = errors.New("off")

func testBridge() *Bridge[testProfile] {
	return &Bridge[testProfile]{Path: "/internal/test-profiles", TokenEnv: "AF_TEST_PROFILES_TOKEN", What: "test profiles",
		CacheFile: "test-settings.json", OwnerLabel: "af-test-profiles-cache/v1", Target: "the test config", ErrOff: errOff}
}

func noLock() (func(), error) { return func() {}, nil }

// The cache is written before the list is applied and is what a CP outage applies; a
// cache bound to another token (another membership in a shared home) is never applied.
func TestPullCachesThenFallsBackToTheCacheOfThisToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_CP_INTERNAL_URL", "")
	up := true
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up || r.URL.Path != "/internal/test-profiles" || r.Header.Get("Authorization") != "Bearer tok-a" {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"profiles":[{"name":"prod"}],"conflicts":[{"name":"dup","labels":["Dup","dup"]}]}`))
	}))
	defer cp.Close()
	t.Setenv("AF_CP_BASE_URL", cp.URL)
	t.Setenv("AF_TEST_PROFILES_TOKEN", "tok-a")
	b := testBridge()

	var applied []List[testProfile]
	apply := func(l List[testProfile]) error {
		if _, ok := b.Cached(); !ok {
			t.Error("applied before the cache was saved")
		}
		applied = append(applied, l)
		return nil
	}
	p, err := b.Pull(noLock, apply)
	if err != nil || !p.Fetched || p.FromCache || len(applied) != 1 || applied[0].Profiles[0].Name != "prod" || p.Conflicts[0].Name != "dup" {
		t.Fatalf("online pull = %+v, %v (applied %v)", p, err, applied)
	}
	// The cache file keeps the format and the owner digest it had in awsx.
	var doc struct {
		Owner    string        `json:"owner"`
		Profiles []testProfile `json:"profiles"`
	}
	data, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".local", "state", "agent-fleet", "test-settings.json"))
	sum := sha256.Sum256([]byte("af-test-profiles-cache/v1\x00tok-a"))
	if json.Unmarshal(data, &doc) != nil || doc.Owner != hex.EncodeToString(sum[:]) || len(doc.Profiles) != 1 {
		t.Fatalf("cache = %s", data)
	}

	up = false
	p, err = b.Pull(noLock, apply)
	if err == nil || !p.FromCache || p.Fetched || len(applied) != 2 {
		t.Fatalf("offline pull = %+v, %v", p, err)
	}

	t.Setenv("AF_TEST_PROFILES_TOKEN", "tok-b")
	p, err = b.Pull(noLock, apply)
	if err == nil || p.Have || len(applied) != 2 {
		t.Fatalf("another token's cache was applied: %+v, %v", p, err)
	}

	t.Setenv("AF_TEST_PROFILES_TOKEN", "")
	if _, err := b.Pull(noLock, apply); !errors.Is(err, errOff) {
		t.Fatalf("no token = %v, want the bridge off", err)
	}
}
