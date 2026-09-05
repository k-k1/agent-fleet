package memoryx

// Version management for agent memory (docs/log/39 / ADR 0022) — the REST side
// (P1: roots / snapshots / diff, P2: tree / restore / settings, P3: export / import).
//
// A path added here must be registered in control-plane/routes.go as well: the CP works from
// an explicit allowlist, so missing one side means a 404 from the Console.

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// memoryRootView is one root as returned by the roots API.
type memoryRootView struct {
	Kind     string             `json:"kind"`
	Label    string             `json:"label"`
	Scopes   bool               `json:"scopes"` // can it be rolled back per project
	Files    int                `json:"files"`
	Bytes    int64              `json:"bytes"`
	Modified string             `json:"modified,omitempty"` // newest mtime (RFC3339)
	Busy     bool               `json:"busy"`               // this kind has a running session
	Projects []memoryProjectRef `json:"projects"`           // claude only (empty when scopes=false)
	// Toggleable / Enabled is the ON/OFF of the agent writing memory at all (docs/log/39 P4).
	// Carried on active roots too: once codex has created a workspace it drops out of
	// inactive, so without it here the UI loses the way back to turning it off again.
	Toggleable bool `json:"toggleable,omitempty"`
	Enabled    bool `json:"enabled,omitempty"`
}

// memoryWriteErr maps a memoryUserErr (a failure caused by the input) to a stable code and
// everything else to fallback (500). The 500 side is logged as well: the Console's i18n folds
// the response message into generic wording, so the log is the only place the failure that
// actually happened here can be traced afterwards.
func memoryWriteErr(w http.ResponseWriter, err error, fallback string) {
	var ue *memoryUserErr
	if errors.As(err, &ue) {
		httpx.WriteErr(w, ue.Status, ue.Code, ue.Msg)
		return
	}
	log.Printf("memory: %s: %v", fallback, err)
	httpx.WriteErr(w, http.StatusInternalServerError, fallback, err.Error())
}

// HandleMemoryRoots returns the memory roots active in this environment and a summary of what
// is in them. codex appears only when ~/.codex/memories exists (the memories feature is off by
// default).
func HandleMemoryRoots(w http.ResponseWriter, r *http.Request) {
	roots := memoryRoots()
	busy := memoryBusyKinds()
	views := make([]memoryRootView, 0, len(roots))
	for _, root := range roots {
		v := memoryRootView{
			Kind: root.Kind, Label: root.Label, Scopes: root.Scopes,
			Busy: busy[root.Kind], Projects: []memoryProjectRef{},
		}
		if root.Kind == "codex" {
			// Turning it off leaves the existing md in place and still under version
			// management (codex just stops updating it). A history with no gap in it is
			// the correct outcome.
			v.Toggleable, v.Enabled = true, codex.MemoriesEnabled()
		}
		var newest time.Time
		seen := map[string]bool{}
		for _, f := range memoryCollect(root) {
			v.Files++
			v.Bytes += f.Size
			if t := time.Unix(f.MTime, 0); t.After(newest) {
				newest = t
			}
			if !root.Scopes {
				continue
			}
			if slug, ok := memoryScopeSlug(root.RepoPrefix + "/" + f.Rel); ok && !seen[slug] {
				seen[slug] = true
				v.Projects = append(v.Projects, memoryProjectRef{Slug: slug, Display: memorySlugDisplay(slug)})
			}
		}
		if !newest.IsZero() {
			v.Modified = newest.Format(time.RFC3339)
		}
		views = append(views, v)
	}
	out := memoryRootsWire{
		Roots: views,
		// Roots that are declared but not currently active (codex memories not enabled,
		// and so on), each with its reason. Dropping them silently leaves the Console
		// unable to show either why one is missing or how to enable it (docs/log/39 P4).
		Inactive: memoryInactiveRoots(),
		Auto:     memoryAutoEnabled(),
		// locked = operations stopped it via AF_MEMORY_SNAPSHOT (the UI toggle cannot undo that).
		AutoLocked: memoryAutoLocked(),
	}
	if head := memoryHeadTime(); !head.IsZero() {
		out.LastSnapshot = head.Format(time.RFC3339)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleMemorySettings is the ON/OFF toggle for automatic snapshots (docs/log/39 resolution #1).
// A forced OFF from the environment variable cannot be overridden.
func HandleMemorySettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Auto *bool `json:"auto"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Auto == nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "auto is required")
		return
	}
	if err := memorySetAuto(*body.Auto); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemorySnapshotFailed, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"auto": memoryAutoEnabled(), "autoLocked": memoryAutoLocked()})
}

// HandleMemorySnapshots returns the snapshot history, newest first (?limit=&before=).
func HandleMemorySnapshots(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	list, err := memoryListSnapshots(limit, r.URL.Query().Get("before"))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

// HandleMemorySnapshotCreate is the manual snapshot (docs/log/39 item 2). With nothing changed
// it just returns committed=false; no empty commit is stacked.
func HandleMemorySnapshotCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Trigger string `json:"trigger"`
	}
	if r.ContentLength > 0 && !httpx.DecodeJSON(w, r, &body) {
		return
	}
	// The trigger is not an arbitrary string the API may stamp in (that would destroy what the
	// history means). The manual path is fixed to manual.
	if body.Trigger != "" && body.Trigger != memoryTriggerManual {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "trigger must be \"manual\"")
		return
	}
	res, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemorySnapshotFailed, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// HandleMemoryDiff returns the unified diff between two points in time (?from=&to=&at=&path=).
// Omitting from means "the change to introduced", omitting path means the whole tree, and at
// means "the latest snapshot at or before that time".
func HandleMemoryDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !memoryHasCommits() {
		httpx.WriteErr(w, http.StatusNotFound, errCodeMemoryNoSnapshots, "no snapshots yet")
		return
	}
	to, err := memoryResolveRev(q.Get("to"), q.Get("at"))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRev, err.Error())
		return
	}
	from := ""
	if v := q.Get("from"); v != "" {
		if from, err = memoryResolveRev(v, ""); err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRev, err.Error())
			return
		}
	}
	path := q.Get("path")
	if !memoryPathSafe(path) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadPath, "path must be inside a declared memory root")
		return
	}
	diff, err := memoryDiff(from, to, path)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryDiffFailed, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "path": path, "diff": diff})
}

// HandleMemoryTree returns what was in there at a given point (?rev=|at=). The scope picker of
// a restore reads this: offering today's roots as the choices would leave an already-deleted
// project unselectable, and "put back the memory I deleted by mistake" — the whole point — would
// not work (memory_restore.go).
func HandleMemoryTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rev := q.Get("rev")
	if rev == "" {
		rev = q.Get("to") // also accept the diff API's spelling
	}
	sha, kinds, projects, err := memoryTreeAt(rev, q.Get("at"))
	if err != nil {
		memoryWriteErr(w, err, errCodeMemoryDiffFailed)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"rev": sha, "kinds": kinds, "projects": projects})
}

// HandleMemoryRestore rolls back to a given point (docs/log/39 item 4). The history is not
// rewritten: it stacks three things — a pre-restore snapshot, the application to live, and the
// restore commit.
func HandleMemoryRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rev   string             `json:"rev"`
		At    string             `json:"at"`
		Scope memoryRestoreScope `json:"scope"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	res, err := memoryRestore(body.Scope, body.Rev, body.At, time.Now())
	if err != nil {
		memoryWriteErr(w, err, errCodeMemoryRestoreFailed)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// HandleMemoryImport takes in a bundle / tar.gz, imports it into refs/imports/<id> as an
// independent lineage and returns a preview (docs/log/39 item 5). At this point live is not
// touched at all — what gets applied is the user's decision, made from the preview.
func HandleMemoryImport(w http.ResponseWriter, r *http.Request) {
	if err := memoryEnsureRepo(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryImportFailed, err.Error())
		return
	}
	if err := os.MkdirAll(memoryWorkDir(), 0o700); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryImportFailed, err.Error())
		return
	}
	// Take it in as a stream rather than loading the whole thing into memory (the ★3 size
	// limit is enforced here too).
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "expected multipart/form-data")
		return
	}
	max := memoryImportMaxBytes()
	tmp, name := "", ""
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, perr.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		f, cerr := os.CreateTemp(memoryWorkDir(), "upload-*")
		if cerr != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryImportFailed, cerr.Error())
			return
		}
		n, werr := io.Copy(f, io.LimitReader(part, max+1))
		_ = f.Close()
		if werr != nil || n > max {
			_ = os.Remove(f.Name())
			if n > max {
				httpx.WriteErr(w, http.StatusRequestEntityTooLarge, errCodeMemoryTooLarge, "file exceeds the import size limit")
			} else {
				httpx.WriteErr(w, http.StatusInternalServerError, errCodeMemoryImportFailed, "upload failed")
			}
			return
		}
		tmp, name = f.Name(), filepath.Base(part.FileName())
		break
	}
	if tmp == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest, "no file part in the request")
		return
	}
	defer os.Remove(tmp)

	pv, err := memoryImportPrepare(tmp, name, time.Now())
	if err != nil {
		memoryWriteErr(w, err, errCodeMemoryImportFailed)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pv)
}

// HandleMemoryImportApply applies only the chosen projects / kinds of an imported lineage to
// live (replacement = a new commit; no 3-way merge, ADR 0022 decision 5).
// mode="migrate" is a relocation: not just the content but the HISTORY too is swapped for the
// imported lineage (always over the whole scope). The two are split by one key instead of a
// second route because adding a REST path walks into the known trap of forgetting to register
// it in the CP's allowlist (the note at the top of this file).
func HandleMemoryImportApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ImportID string             `json:"importId"`
		Scope    memoryRestoreScope `json:"scope"`
		Mode     string             `json:"mode"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Mode != "" && body.Mode != memoryImportModeReplace && body.Mode != memoryImportModeMigrate {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMemoryBadRequest,
			"mode must be \""+memoryImportModeReplace+"\" or \""+memoryImportModeMigrate+"\"")
		return
	}
	opts := memoryApplyOpts{Adopt: body.Mode == memoryImportModeMigrate}
	res, err := memoryImportApply(body.ImportID, body.Scope, time.Now(), opts)
	if err != nil {
		memoryWriteErr(w, err, errCodeMemoryImportFailed)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// memoryRootsWire — the GET /memory/roots response (the Console's `RootsPayload`,
// console/src/features/settings/memory/memoryTypes.ts).
//
// was: map[string]any{"roots":…, "inactive":…, "auto":…, "autoLocked":…} plus "lastSnapshot",
// which was added only when head was non-zero.
//
// lastSnapshot is the only conditional key, so it is the only one that carries omitempty. That
// is faithful because the value is always a non-empty RFC3339 string: "present but empty" is a
// state it cannot reach, so omitempty can only drop the "absent" case. (Put omitempty on a key
// whose value can be the empty string and it disappears while present.) The other four keys are
// unconditional and get none.
type memoryRootsWire struct {
	Roots        []memoryRootView     `json:"roots"`
	Inactive     []memoryInactiveRoot `json:"inactive"`
	Auto         bool                 `json:"auto"`
	AutoLocked   bool                 `json:"autoLocked"`
	LastSnapshot string               `json:"lastSnapshot,omitempty"`
}
