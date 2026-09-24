package main

// The package-manager caches in home: how big each one is, and emptying one on request.
//
//   GET    /cleanup/tool-caches        → the rows the Settings "Machine" tab shows
//   DELETE /cleanup/tool-caches/{name} → empty that cache
//
// These are not Agent Fleet's own leftovers (cleanup_cache.go counts those). They belong to
// go, npm, uv and pip, grow without bound, and were measured at 34G (go-build) and 41G
// (npm) in one home (docs/log/116). All of them are regenerable: emptying one costs the next
// build or install a slower first run and nothing else.
//
// Manual only, never on a timer (docs/log/116): neither Go nor npm can evict by age, so the
// only cleanup is "all of it", and when that happens is the person's call.
//
// The walk touches hundreds of thousands of files, so it runs only when the Console asks
// (a button, not on opening the tab) and the answer is held briefly.

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// toolCache is one cache the Machine tab offers to empty.
type toolCache struct {
	Name string
	// env names the variable that moves the cache, if set in the Agent's environment;
	// envSub is appended to it (npm's cache variable names the parent of _cacache).
	env, envSub string
	// rel is the default location under home.
	rel string
	// users says whether a running process could be using this cache (argv as read from
	// /proc). Emptying it under a running build or install can fail that run.
	users func(argv []string) bool
}

var toolCaches = []toolCache{
	{Name: "go-build", env: "GOCACHE", rel: ".cache/go-build", users: argvRuns("go")},
	{Name: "npm", env: "npm_config_cache", envSub: "_cacache", rel: ".npm/_cacache", users: argvRunsNode("npm", "npx")},
	{Name: "uv", env: "UV_CACHE_DIR", rel: ".cache/uv", users: argvRuns("uv", "uvx")},
	{Name: "pip", env: "PIP_CACHE_DIR", rel: ".cache/pip", users: argvRunsPip},
}

func (c toolCache) dir() string {
	if v := os.Getenv(c.env); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, c.envSub)
	}
	return filepath.Join(homeDir(), c.rel)
}

func findToolCache(name string) (toolCache, bool) {
	for _, c := range toolCaches {
		if c.Name == name {
			return c, true
		}
	}
	return toolCache{}, false
}

// argvRuns matches a process whose program is one of names.
func argvRuns(names ...string) func([]string) bool {
	return func(argv []string) bool {
		if len(argv) == 0 {
			return false
		}
		base := filepath.Base(argv[0])
		for _, n := range names {
			if base == n {
				return true
			}
		}
		return false
	}
}

// argvRunsNode also matches the node-run form: /proc shows npm as `node /…/bin/npm-cli.js`
// (or `node /…/bin/npm`), not as npm.
func argvRunsNode(names ...string) func([]string) bool {
	direct := argvRuns(names...)
	return func(argv []string) bool {
		if direct(argv) {
			return true
		}
		if len(argv) < 2 || filepath.Base(argv[0]) != "node" {
			return false
		}
		script := strings.TrimSuffix(filepath.Base(argv[1]), ".js")
		for _, n := range names {
			if script == n || script == n+"-cli" {
				return true
			}
		}
		return false
	}
}

// argvRunsPip matches pip and `python -m pip`.
func argvRunsPip(argv []string) bool {
	if argvRuns("pip", "pip3")(argv) {
		return true
	}
	for i := 1; i+1 < len(argv); i++ {
		if argv[i] == "-m" && argv[i+1] == "pip" {
			return strings.HasPrefix(filepath.Base(argv[0]), "python")
		}
	}
	return false
}

// procRoot is /proc; a variable so tests can point it at a fake tree.
var procRoot = "/proc"

// cacheUsers returns the pids of processes that could be using c. Only this container's
// processes are visible, which is exactly the set that shares this home.
func cacheUsers(c toolCache) []int {
	ents, _ := os.ReadDir(procRoot)
	var pids []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		if c.users(strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")) {
			pids = append(pids, pid)
		}
	}
	return pids
}

type toolCacheRow struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
	// Busy lists the pids that could be using the cache right now; the Console disables the
	// button and a DELETE answers 409 while it is non-empty.
	Busy []int `json:"busy,omitempty"`
	usagePlace
}

type toolCacheUsage struct {
	Caches []toolCacheRow `json:"caches"`
	// Truncated = a walk hit its entry cap; the figures are lower bounds.
	Truncated  bool   `json:"truncated,omitempty"`
	MeasuredAt string `json:"measured_at"`
}

const (
	toolCacheUsageTTL = 30 * time.Second
	// Per cache: the measured go-build at 34G was well under this.
	toolCacheMaxEntries = 2_000_000
)

var toolCacheUsageCache struct {
	mu  sync.Mutex
	at  time.Time
	val *toolCacheUsage
}

// handleToolCacheUsage (GET /cleanup/tool-caches).
func handleToolCacheUsage(w http.ResponseWriter, r *http.Request) {
	toolCacheUsageCache.mu.Lock()
	defer toolCacheUsageCache.mu.Unlock()
	now := time.Now()
	if toolCacheUsageCache.val == nil || now.Sub(toolCacheUsageCache.at) > toolCacheUsageTTL {
		toolCacheUsageCache.val = measureToolCaches(now)
		toolCacheUsageCache.at = now
	}
	httpx.WriteJSON(w, http.StatusOK, toolCacheUsageCache.val)
}

func measureToolCaches(now time.Time) *toolCacheUsage {
	u := &toolCacheUsage{MeasuredAt: now.UTC().Format(time.RFC3339), Caches: []toolCacheRow{}}
	for _, c := range toolCaches {
		dir := c.dir()
		if _, err := os.Stat(dir); err != nil {
			continue // not installed or never used: no row
		}
		budget := toolCacheMaxEntries
		row := toolCacheRow{Name: c.Name, Busy: cacheUsers(c), usagePlace: placeOf(dir)}
		row.Bytes, row.Files = walkSize(dir, &budget)
		u.Truncated = u.Truncated || budget <= 0
		u.Caches = append(u.Caches, row)
	}
	return u
}

// handleDeleteToolCache (DELETE /cleanup/tool-caches/{name}) empties one cache.
//
// The directory itself stays and only its contents go: with $AF_WS_SCRATCH it is a symlink
// into the task disk (entrypoint.sh), and removing the link would move the cache back onto
// home. The busy check runs again here rather than trusting the survey: a build may have
// started since.
func handleDeleteToolCache(w http.ResponseWriter, r *http.Request) {
	c, ok := findToolCache(r.PathValue("name"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_cache", "unknown cache: "+r.PathValue("name"))
		return
	}
	if pids := cacheUsers(c); len(pids) > 0 {
		httpx.WriteErr(w, http.StatusConflict, "cache_in_use", c.Name+" is in use by pid "+joinInts(pids))
		return
	}
	bytes, files, err := emptyDir(c.dir())
	toolCacheUsageCache.mu.Lock()
	toolCacheUsageCache.val = nil
	toolCacheUsageCache.mu.Unlock()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toolCacheEmptied{Name: c.Name, Bytes: bytes, Files: files})
}

// toolCacheEmptied is what emptying a cache took.
type toolCacheEmptied struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}

// emptyDir removes everything inside dir (following dir itself if it is a symlink) and
// reports what it held. A missing dir is already empty.
func emptyDir(dir string) (bytes int64, files int, err error) {
	real, err := filepath.EvalSymlinks(dir)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	budget := toolCacheMaxEntries
	bytes, files = walkSize(real, &budget)
	ents, err := os.ReadDir(real)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range ents {
		p := filepath.Join(real, e.Name())
		rerr := os.RemoveAll(p)
		if rerr != nil {
			// A tool may leave read-only directories (Go's module cache does), and RemoveAll
			// cannot unlink inside one: open the rest of the tree up and try once more.
			_ = filepath.WalkDir(p, func(q string, d os.DirEntry, werr error) error {
				if werr == nil && d.IsDir() {
					_ = os.Chmod(q, 0o700)
				}
				return nil
			})
			rerr = os.RemoveAll(p)
		}
		if rerr != nil && err == nil {
			err = rerr
		}
	}
	return bytes, files, err
}

func joinInts(xs []int) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ", ")
}
