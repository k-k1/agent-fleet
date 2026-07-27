package main

// エージェントメモリの版管理（docs/39 / ADR 0022）— REST（P1: roots / snapshots / diff）。
//
// ⚠️ ここに足したパスは control-plane/routes.go にも同じものを登録する（CP は明示許可
// リスト方式で、片側漏れ = Console から 404）。

import (
	"net/http"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// memoryRootView は roots API が返す 1 ルート分。
type memoryRootView struct {
	Kind     string             `json:"kind"`
	Label    string             `json:"label"`
	Scopes   bool               `json:"scopes"` // プロジェクト粒度の部分ロールバックが可能か
	Files    int                `json:"files"`
	Bytes    int64              `json:"bytes"`
	Modified string             `json:"modified,omitempty"` // 最新 mtime（RFC3339）
	Projects []memoryProjectRef `json:"projects"`           // claude のみ（scopes=false は空）
}

// handleMemoryRoots はこの環境で有効なメモリルートと、その中身の概況を返す。
// codex は ~/.codex/memories が存在するときだけ現れる（memories 機能は既定 OFF）。
func handleMemoryRoots(w http.ResponseWriter, r *http.Request) {
	roots := memoryRoots()
	views := make([]memoryRootView, 0, len(roots))
	for _, root := range roots {
		v := memoryRootView{Kind: root.Kind, Label: root.Label, Scopes: root.Scopes, Projects: []memoryProjectRef{}}
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
	out := map[string]any{"roots": views, "auto": memoryAutoEnabled()}
	if head := memoryHeadTime(); !head.IsZero() {
		out["lastSnapshot"] = head.Format(time.RFC3339)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleMemorySnapshots は snapshot 履歴を新しい順に返す（?limit=&before=）。
func handleMemorySnapshots(w http.ResponseWriter, r *http.Request) {
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

// handleMemorySnapshotCreate は手動 snapshot（docs/39 ②）。変更が無ければ committed=false
// を返すだけで、空コミットは積まない。
func handleMemorySnapshotCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Trigger string `json:"trigger"`
	}
	if r.ContentLength > 0 && !httpx.DecodeJSON(w, r, &body) {
		return
	}
	// 契機は API から任意の文字列を刻ませない（履歴の意味が壊れる）。手動経路は manual 固定。
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

// handleMemoryDiff は 2 時点間の unified diff を返す（?from=&to=&at=&path=）。
// from 省略で「to が入れた変更」、path 省略で全体。at は「その時刻以前の直近 snapshot」。
func handleMemoryDiff(w http.ResponseWriter, r *http.Request) {
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
