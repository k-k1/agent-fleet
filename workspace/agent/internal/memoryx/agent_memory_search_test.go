package memoryx

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func memSearchNames(hits []agentMemHit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Name)
	}
	return out
}

func memSearchSave(t *testing.T, c agentMemCaller, name, desc, body string, at time.Time) {
	t.Helper()
	if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: desc, Body: body}, at); err != nil {
		t.Fatal(err)
	}
}

// memSeedMany stores n memories as one commit instead of n saves. A save costs five git
// processes (ls-files, add, commit, rev-parse, gc --auto), which on a loaded host made a
// 500-memory fixture take a minute. The files, staging copies and primed scan cache are what
// n saves leave behind; the save path itself is covered by the tests that call agentMemSave
// and TestMemSeedManyMatchesSave pins the two together. Test-only: it assumes a fresh store,
// unused names and a test that never reads history (no AF-* commit trailers, `add -A` sweeps
// the whole store, and no tomb, secret-scan or project.json step runs).
func memSeedMany(t *testing.T, c agentMemCaller, n int, req func(i int) agentMemSaveReq, at func(i int) time.Time) {
	t.Helper()
	if err := memoryEnsureRepo(); err != nil {
		t.Fatal(err)
	}
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()
	for i := 0; i < n; i++ {
		r := req(i)
		r.Scope = agentMemDefaultScope(r.Scope, c)
		if err := agentMemValidate(&r); err != nil {
			t.Fatal(err)
		}
		rel, err := agentMemScopeDir(r.Scope, c)
		if err != nil {
			t.Fatal(err)
		}
		stamp := at(i).UTC().Format(time.RFC3339)
		data := agentMemRender(agentMemEntry{
			Name: r.Name, Scope: r.Scope, Description: r.Description, Type: r.Type, Kinds: r.Kinds,
			Revision: 1, AuthorKind: c.Kind, AuthorSession: c.Session, Created: stamp, Updated: stamp, Body: r.Body,
		})
		repoRel := rel + "/" + r.Name + ".md"
		if _, err := agentMemCheckDir(rel, true); err != nil {
			t.Fatal(err)
		}
		if err := agentMemWriteFile(filepath.Join(agentMemDir(), filepath.FromSlash(repoRel)), data); err != nil {
			t.Fatal(err)
		}
		stg := filepath.Join(memoryStagingDir(), agentMemRepoPrefix, filepath.FromSlash(repoRel))
		memoryMkdirAll(t, filepath.Dir(stg))
		if err := os.WriteFile(stg, data, 0o600); err != nil {
			t.Fatal(err)
		}
		agentMemPrimeScan(data, r.Scope, r.Name)
	}
	if _, err := memoryGitRun("add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("commit", "--quiet", "--no-verify", "-m", "agent-memory: seed"); err != nil {
		t.Fatal(err)
	}
}

// The seed must leave what a save leaves: the same file and staging bytes, and a primed scan
// cache. Each variant runs in its own environment, so the bytes are compared across stores.
func TestMemSeedManyMatchesSave(t *testing.T) {
	at := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	req := agentMemSaveReq{Name: "same", Description: "about builds", Type: "project", Body: "body\nmore"}
	read := func(seed bool) (file, staged []byte) {
		t.Helper()
		agentMemTestEnv(t)
		c := agentMemCallerT(t, "claude-main")
		runs := agentMemScanRuns.Load()
		if seed {
			memSeedMany(t, c, 1, func(int) agentMemSaveReq { return req }, func(int) time.Time { return at })
		} else if _, err := agentMemSave(c, req, at); err != nil {
			t.Fatal(err)
		}
		rel, err := agentMemScopeDir(agentMemDefaultScope("", c), c)
		if err != nil {
			t.Fatal(err)
		}
		rel += "/same.md"
		if file, err = os.ReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
		if staged, err = os.ReadFile(filepath.Join(memoryStagingDir(), agentMemRepoPrefix, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
		before := agentMemScanRuns.Load()
		if _, err := agentMemListIndex(c, 0); err != nil {
			t.Fatal(err)
		}
		if n := agentMemScanRuns.Load() - before; n != 0 {
			t.Errorf("seed=%v: %d files rescanned on the first read (scan runs since start: %d)", seed, n, before-runs)
		}
		return file, staged
	}
	var f1, s1, f2, s2 []byte
	t.Run("save", func(t *testing.T) { f1, s1 = read(false) })
	t.Run("seed", func(t *testing.T) { f2, s2 = read(true) })
	if string(f1) != string(f2) || string(s1) != string(s2) {
		t.Errorf("seed differs from save:\nsave file:\n%s\nseed file:\n%s", f1, f2)
	}
}

// Recency and relevance disagree on this fixture: the newest memory only mentions the word in
// its body, the oldest has it in the name.
func TestAgentMemorySearchRanksByFieldThenTerms(t *testing.T) {
	agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	memSearchSave(t, c, "deploy-notes", "how the rollout goes", "nothing special", now)
	memSearchSave(t, c, "misc-a", "deploy checklist", "x", now.Add(time.Hour))
	memSearchSave(t, c, "misc-b", "other", "mentions deploy once", now.Add(2*time.Hour))
	hits, fb, err := agentMemSearchRanked(c, "deploy", 0)
	if err != nil || fb != "" {
		t.Fatalf("err=%v fallback=%q", err, fb)
	}
	if got, want := memSearchNames(hits), []string{"deploy-notes", "misc-a", "misc-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v (name > description > body)", got, want)
	}
	// A cut at the limit keeps the best, not the newest.
	hits, _, _ = agentMemSearchRanked(c, "deploy", 1)
	if len(hits) != 1 || hits[0].Name != "deploy-notes" {
		t.Fatalf("limit 1 = %v", memSearchNames(hits))
	}
}

func TestAgentMemorySearchTiesUsePinThenUsesThenRecency(t *testing.T) {
	agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	memSearchSave(t, c, "a-old", "tie word", "x", now)
	memSearchSave(t, c, "b-used", "tie word", "x", now.Add(time.Hour))
	memSearchSave(t, c, "c-mid", "tie word", "x", now.Add(2*time.Hour))
	memSearchSave(t, c, "d-new", "tie word", "x", now.Add(3*time.Hour))
	if _, err := agentMemPin(agentMemPinReq{Scope: "project", Project: c.Project.ID, Name: "a-old", Pinned: true}, now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	agentMemRecordUses(c, agentMemEntry{Name: "b-used", Scope: "project"})
	hits, _, err := agentMemSearchRanked(c, "tie", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := memSearchNames(hits), []string{"a-old", "b-used", "d-new", "c-mid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v (pinned, used, then newest first)", got, want)
	}
}

func TestAgentMemorySearchFallsBackToAnyTerm(t *testing.T) {
	agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	memSearchSave(t, c, "one", "alpha only", "x", now)
	memSearchSave(t, c, "two", "alpha beta", "line with gamma", now.Add(time.Hour))
	// Every word present in one memory: no fallback.
	hits, fb, _ := agentMemSearchRanked(c, "alpha beta", 0)
	if fb != "" || len(hits) != 1 || hits[0].Name != "two" || hits[0].MatchedTerms != nil {
		t.Fatalf("and answer: fb=%q hits=%+v", fb, hits)
	}
	// "zeta" is nowhere: the AND is empty, the OR answers, best-matching first.
	hits, fb, err := agentMemSearchRanked(c, "alpha beta zeta", 0)
	if err != nil || fb != "any" {
		t.Fatalf("fallback = %q, err=%v", fb, err)
	}
	if got := memSearchNames(hits); !reflect.DeepEqual(got, []string{"two", "one"}) {
		t.Fatalf("order = %v", got)
	}
	if !reflect.DeepEqual(hits[0].MatchedTerms, []string{"alpha", "beta"}) || !reflect.DeepEqual(hits[1].MatchedTerms, []string{"alpha"}) {
		t.Fatalf("matchedTerms = %v / %v", hits[0].MatchedTerms, hits[1].MatchedTerms)
	}
	// A single unmatched word has nothing to fall back to.
	if hits, fb, _ = agentMemSearchRanked(c, "zeta", 0); len(hits) != 0 || fb != "" {
		t.Fatalf("zeta: fb=%q hits=%v", fb, hits)
	}
}

// More matched words beat a heavier field: the older body-only memory matches two words (score
// 2), the newer name-only one matches one (score 4), and limit 1 must keep the former.
func TestAgentMemorySearchMatchedCountBeatsFieldWeight(t *testing.T) {
	agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	memSearchSave(t, c, "two-body", "plain", "alpha and beta here", now)
	memSearchSave(t, c, "alpha", "plain", "x", now.Add(time.Hour))
	hits, fb, _ := agentMemSearchRanked(c, "alpha beta absent", 1)
	if fb != "any" || len(hits) != 1 || hits[0].Name != "two-body" {
		t.Fatalf("fb=%q hits=%v, want two-body", fb, memSearchNames(hits))
	}
}

func TestAgentMemoryScoreTakesBestFieldOnlyAndTermsAreDistinct(t *testing.T) {
	e := agentMemEntry{Name: "alpha", Description: "alpha", Body: "alpha"}
	if s := agentMemScore(e, []string{"alpha"}); s.score != agentMemWeightName || len(s.matched) != 1 {
		t.Errorf("score = %+v, want the name weight once", s)
	}
	if got := agentMemSearchTerms("Alpha  alpha ALPHA beta"); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Errorf("terms = %v", got)
	}
}

func TestAgentMemorySearchJapaneseWithoutSpaces(t *testing.T) {
	agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	memSearchSave(t, c, "jp", "ビルドの注意", "メモリが少ないので並列数を絞る", now)
	hits, _, _ := agentMemSearchRanked(c, "並列数", 0)
	if len(hits) != 1 || hits[0].Name != "jp" || len(hits[0].Snippets) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestAgentMemorySearchCountsUsesOnlyForReturnedHits(t *testing.T) {
	agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	memSearchSave(t, c, "wanted", "needle", "x", now)
	memSearchSave(t, c, "other", "needle", "x", now.Add(time.Hour))
	if hits, _, _ := agentMemSearchRanked(c, "needle", 1); len(hits) != 1 || hits[0].Name != "other" {
		t.Fatalf("hits = %v", hits)
	}
	rel, err := agentMemScopeDir(agentMemScopeProject, c)
	if err != nil {
		t.Fatal(err)
	}
	if u := agentMemLoadUsage(rel); u["other"] != 1 || u["wanted"] != 0 {
		t.Errorf("uses = %v, want only the returned hit counted", u)
	}
}

// TestAgentMemorySearchLatency states the cost at the size ADR 0108 measured (469 files). It
// logs the figure; it asserts results only, since a wall-clock limit would fail on a loaded host.
func TestAgentMemorySearchLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 500 memories")
	}
	agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	body := strings.Repeat("line of ordinary memory text about builds and tests\n", 40)
	memSeedMany(t, c, 500, func(i int) agentMemSaveReq {
		return agentMemSaveReq{Name: fmt.Sprintf("mem-%03d", i), Description: fmt.Sprintf("memory %d about builds", i), Body: body + fmt.Sprintf("unique%d", i)}
	}, func(i int) time.Time { return now.Add(time.Duration(i) * time.Minute) })
	// memSeedMany primes the scan cache as a save does, so the first read does not rescan.
	t0 := time.Now()
	runs := agentMemScanRuns.Load()
	if _, err := agentMemListIndex(c, 0); err != nil {
		t.Fatal(err)
	}
	if n := agentMemScanRuns.Load() - runs; n != 0 {
		t.Fatalf("%d files rescanned on the first read after seeding", n)
	}
	t.Logf("index of 500 (untouched path, first read): %v", time.Since(t0))
	for _, q := range []string{"builds", "builds", "builds tests ordinary", "builds nosuchword"} {
		start := time.Now()
		hits, fb, err := agentMemSearchRanked(c, q, 0)
		d := time.Since(start)
		if err != nil || len(hits) != 20 {
			t.Fatalf("%q: %d hits, err=%v", q, len(hits), err)
		}
		t.Logf("500 memories, %q: %v (fallback=%q)", q, d, fb)
	}
}

func agentMemDropScanCache() {
	agentMemScanCache.Range(func(k, _ any) bool { agentMemScanCache.Delete(k); return true })
}

// The first read after N saves finds every verdict cached: counted in scans, not seconds.
func TestAgentMemoryFirstReadAfterSavesDoesNotRescan(t *testing.T) {
	agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		memSearchSave(t, c, fmt.Sprintf("mem-%02d", i), "about builds", "body", now.Add(time.Duration(i)*time.Minute))
	}
	before := agentMemScanRuns.Load()
	if _, err := agentMemListIndex(c, 0); err != nil {
		t.Fatal(err)
	}
	if hits, _, err := agentMemSearchRanked(c, "builds", 0); err != nil || len(hits) != 20 {
		t.Fatalf("%d hits, err=%v", len(hits), err)
	}
	if n := agentMemScanRuns.Load() - before; n != 0 {
		t.Fatalf("%d files rescanned on the first read after saving them", n)
	}
}

// A cold cache (a new process) is filled before the read lock is taken: with a writer holding
// agentMemMu the read scans every file and only then waits, and finishes without scanning again.
// Each reader is its own subtest, so dropping the warm-up from one of them is caught.
func TestAgentMemoryColdScanRunsOutsideReadLock(t *testing.T) {
	const n = 12
	readers := map[string]func(c agentMemCaller) (int, error){
		"index": func(c agentMemCaller) (int, error) {
			ix, err := agentMemListIndex(c, 0)
			return len(ix.Entries), err
		},
		"search": func(c agentMemCaller) (int, error) {
			hits, _, err := agentMemSearchRanked(c, "builds", 0)
			return len(hits), err
		},
		"member-search": func(agentMemCaller) (int, error) {
			out, err := agentMemMemberSearch("", "", "builds", 0)
			return len(out.Hits), err
		},
	}
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			agentMemTestEnv(t)
			c := agentMemCallerT(t, "claude-main")
			now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
			for i := 0; i < n; i++ {
				memSearchSave(t, c, fmt.Sprintf("mem-%02d", i), "about builds", "body", now.Add(time.Duration(i)*time.Minute))
			}
			agentMemDropScanCache()
			before := agentMemScanRuns.Load()

			agentMemMu.Lock() // stands in for a write in progress
			locked := true
			defer func() {
				if locked {
					agentMemMu.Unlock()
				}
			}()
			type result struct {
				n   int
				err error
			}
			done := make(chan result, 1)
			go func() {
				got, err := read(c)
				done <- result{got, err}
			}()
			deadline := time.Now().Add(60 * time.Second)
			for agentMemScanRuns.Load()-before < n {
				if time.Now().After(deadline) {
					t.Fatalf("only %d of %d files scanned while the write lock was held", agentMemScanRuns.Load()-before, n)
				}
				time.Sleep(5 * time.Millisecond)
			}
			select {
			case <-done:
				t.Fatal("read returned while the write lock was held")
			default:
			}
			agentMemMu.Unlock()
			locked = false
			select {
			case r := <-done:
				if r.err != nil || r.n != n {
					t.Fatalf("%d results, err=%v, want %d", r.n, r.err, n)
				}
			case <-time.After(60 * time.Second):
				t.Fatal("read did not finish after the lock was released")
			}
			if got := agentMemScanRuns.Load() - before; got != n {
				t.Fatalf("%d scans for %d files", got, n)
			}
		})
	}
}
