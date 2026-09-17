package afdb

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// AsyncOp tracks the state of an async database operation (install or start).
type AsyncOp struct {
	mu        sync.Mutex
	OpState   string // "installing" | "starting" | ""
	LastError string
}

var asyncOps sync.Map // "engine-major" → *AsyncOp

// GetAsyncOp returns (creating if absent) the AsyncOp for (engine, major).
func GetAsyncOp(engine, major string) *AsyncOp {
	v, _ := asyncOps.LoadOrStore(instanceKey(engine, major), &AsyncOp{})
	return v.(*AsyncOp)
}

// Get reads the current state atomically.
func (a *AsyncOp) Get() (state, lastError string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.OpState, a.LastError
}

// Set writes state and lastError atomically.
func (a *AsyncOp) Set(state, lastError string) {
	a.mu.Lock()
	a.OpState = state
	a.LastError = lastError
	a.mu.Unlock()
}

// StartIfIdle atomically sets the operation state to initialState when idle.
// Returns true if the state was set (caller should launch the goroutine),
// false if an operation is already in flight.
func (a *AsyncOp) StartIfIdle(initialState string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.OpState == "installing" || a.OpState == "starting" {
		return false
	}
	a.OpState = initialState
	a.LastError = ""
	return true
}

// EngineStatus is the JSON shape returned by GET /env/databases.
// All fields are always present (no omitempty) so the Console type can
// treat every key as required without truthy guards on the sender side.
type EngineStatus struct {
	Engine     string          `json:"engine"`
	Major      string          `json:"major"`
	Installed  bool            `json:"installed"`
	State      string          `json:"state"`
	Version    string          `json:"version"`
	RSSBytes   int64           `json:"rssBytes"`
	Port       int             `json:"port"`
	Datadir    string          `json:"datadir"`
	Databases  []DatabaseEntry `json:"databases"`
	LastUsedAt time.Time       `json:"lastUsedAt"`
	LastError  string          `json:"lastError"`
}

// DatabaseEntry is one database that exists on this engine, with the URLs that
// reach it. There is one per working copy (decision 3′).
//
// The URLs belong to the entry, not to the engine: the Agent runs from its own
// directory, so a single engine-level URL could only ever name the Agent's own
// database — a name no session uses and that nothing creates. The first live run
// of the card offered exactly that, and the copied URL answered
// "database af_dev_… does not exist".
type DatabaseEntry struct {
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	URLSocket string `json:"urlSocket"`
	URLTCP    string `json:"urlTcp"`
}

// isEngineInstalled reports whether the engine binary is present on disk.
func isEngineInstalled(engine, major string) bool {
	var binPath string
	if engine == "mysql" {
		binPath = filepath.Join(mysqlRoot(major), "bin", "mysqld")
	} else {
		binPath = filepath.Join(postgresRoot(major), "bin", "postgres")
	}
	_, err := os.Stat(binPath)
	return err == nil
}

// engineRoot returns the install root for the given engine and major.
func engineRoot(engine, major string) string {
	if engine == "mysql" {
		return mysqlRoot(major)
	}
	return postgresRoot(major)
}

// BuildEngineStatus assembles the EngineStatus for one (engine, major) combination.
func BuildEngineStatus(engine, major string) EngineStatus {
	op := GetAsyncOp(engine, major)
	opState, lastError := op.Get()

	var inst *Instance
	_ = withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return nil
		}
		if i, ok := r.Instances[instanceKey(engine, major)]; ok {
			cp := *i
			inst = &cp
		}
		return nil
	})

	status := EngineStatus{
		Engine:    engine,
		Major:     major,
		Installed: isEngineInstalled(engine, major),
		LastError: lastError,
		Databases: []DatabaseEntry{},
	}

	// State priority: async op in flight > running > error from last op > stopped > absent
	if opState != "" {
		status.State = opState
		return status
	}

	if inst == nil {
		if lastError != "" {
			status.State = "error"
		} else {
			status.State = "absent"
		}
		return status
	}

	status.Port = inst.Port
	status.Datadir = inst.Datadir
	status.LastUsedAt = inst.LastUsedAt

	running := isInstanceRunning(inst)
	if running {
		status.State = "running"
		status.Databases = databaseEntries(inst, engine, major)
		status.Version = instanceVersion(inst)
		status.RSSBytes = rssForInstance(inst)
	} else if lastError != "" {
		status.State = "error"
	} else {
		status.State = "stopped"
	}

	return status
}

// databaseEntries lists the instance's databases, sorted by name, each with the
// URLs that reach it. Only called while the instance is running: a stopped
// engine has no port and no URL to hand out.
func databaseEntries(inst *Instance, engine, major string) []DatabaseEntry {
	names := make([]string, 0, len(inst.Databases))
	for name := range inst.Databases {
		names = append(names, name)
	}
	sort.Strings(names)

	pw := readPass(passPath(engine, major))
	out := make([]DatabaseEntry, 0, len(names))
	for _, name := range names {
		e := DatabaseEntry{Name: name, Dir: inst.Databases[name]}
		if engine == "mysql" {
			e.URLSocket = buildMySQLURL(inst, name, pw, false)
			e.URLTCP = buildMySQLURL(inst, name, pw, true)
		} else {
			e.URLSocket = buildURL(inst, name, pw, false)
			e.URLTCP = buildURL(inst, name, pw, true)
		}
		out = append(out, e)
	}
	return out
}

// HandleDatabasesGet handles GET /env/databases.
func HandleDatabasesGet(w http.ResponseWriter, r *http.Request) {
	pg := BuildEngineStatus("postgres", DefaultMajor)
	my := BuildEngineStatus("mysql", DefaultMySQLMajor)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"engines": []EngineStatus{pg, my},
	})
}

// HandleDatabasesAction handles POST /env/databases/{engine}/{action}.
func HandleDatabasesAction(w http.ResponseWriter, r *http.Request) {
	engine := r.PathValue("engine")
	action := r.PathValue("action")

	if engine != "postgres" && engine != "mysql" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_engine", "engine must be postgres or mysql")
		return
	}
	if action != "start" && action != "stop" && action != "reset" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_action", "action must be start, stop, or reset")
		return
	}

	major := defaultMajorForEngine(engine)

	switch action {
	case "start":
		if engine == "mysql" {
			if err := checkMemoryGate(); err != nil {
				httpx.WriteErr(w, http.StatusBadRequest, "memory_gate", err.Error())
				return
			}
		}
		op := GetAsyncOp(engine, major)
		// StartIfIdle is atomic: both the in-flight check and the state write happen
		// under the same mutex, preventing two concurrent requests from both launching
		// a goroutine.
		initialState := "starting"
		if !isEngineInstalled(engine, major) {
			initialState = "installing"
		}
		if !op.StartIfIdle(initialState) {
			// Already in flight.
			httpx.WriteJSON(w, http.StatusOK, map[string]any{
				"status": BuildEngineStatus(engine, major),
			})
			return
		}
		go func() {
			// Phase 1: install binary if not present.
			if !isEngineInstalled(engine, major) {
				root := engineRoot(engine, major)
				var installErr error
				if engine == "mysql" {
					installErr = ensureInstalledMySQL(root, major)
				} else {
					installErr = ensureInstalled(root, major)
				}
				if installErr != nil {
					op.Set("", installErr.Error())
					return
				}
			}
			// Phase 2: start the server (ensureInstalled inside ensureUp is a fast no-op now).
			op.Set("starting", "")
			_, err := ensureUp(engine, major, false)
			if err != nil {
				op.Set("", err.Error())
			} else {
				op.Set("", "")
			}
		}()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status": BuildEngineStatus(engine, major),
		})

	case "stop":
		purge := r.URL.Query().Get("purge") == "1"
		if err := stopInstance(engine, major, purge); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "stop_failed", err.Error())
			return
		}
		// Clear any previous error so GET no longer reports state=error after success.
		GetAsyncOp(engine, major).Set("", "")
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status": BuildEngineStatus(engine, major),
		})

	case "reset":
		// `db` names WHICH database to reset, and the caller has to say. The Agent
		// cannot infer it: ResolveDir() would answer with the Agent's own directory,
		// so an unqualified reset dropped and re-created a database no session uses
		// (af_dev_… — the same mistake the engine-level URL made before it became a
		// per-database list). The card sends the name from the row the member pressed.
		dbName := r.URL.Query().Get("db")
		if dbName == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "db_required",
				"db=<name> is required; it names the database to reset")
			return
		}
		if err := validateExplicitDB(dbName); err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_db", err.Error())
			return
		}
		if err := resetDB(engine, major, dbName); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "reset_failed", err.Error())
			return
		}
		GetAsyncOp(engine, major).Set("", "")
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status": BuildEngineStatus(engine, major),
		})
	}
}
