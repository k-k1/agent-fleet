package memoryx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// memoryRestoreSeed は「v1 → 変更 → v2」の 2 世代を作り、両 rev を返す。
// v2 では claude 側で ①上書き ②新規追加 ③削除 の 3 種類が起きているので、restore が
// 3 方向すべてを戻せるかを 1 本のテストで見られる。
func memoryRestoreSeed(t *testing.T, cfg, slug string) (v1, v2 string) {
	t.Helper()
	mem := filepath.Join(cfg, "projects", slug, "memory")

	first, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !first.Committed {
		t.Fatalf("seed v1: %+v err=%v", first, err)
	}

	memoryWrite(t, filepath.Join(mem, "a.md"), "rewritten\n")               // ① 上書き
	memoryWrite(t, filepath.Join(mem, "added.md"), "added later\n")         // ② 追加
	if err := os.Remove(filepath.Join(mem, "nested", "b.md")); err != nil { // ③ 削除
		t.Fatal(err)
	}
	second, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !second.Committed {
		t.Fatalf("seed v2: %+v err=%v", second, err)
	}
	return first.Rev, second.Rev
}

func memoryReadLive(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// プロジェクト単位の restore（docs/log/39 ④）: 上書き・追加・削除の 3 方向が戻り、
// pre-restore snapshot と restore commit が履歴に積まれ、他の kind には触らない。
func TestMemoryRestoreProjectScope(t *testing.T) {
	home, cfg, slug := memoryTestEnv(t)
	v1, _ := memoryRestoreSeed(t, cfg, slug)
	mem := filepath.Join(cfg, "projects", slug, "memory")

	// codex 側も v2 の後に動かしておく（プロジェクト scope が越境しないことの確認材料）。
	codexIndex := filepath.Join(home, ".codex", "memories", "MEMORY.md")
	memoryWrite(t, codexIndex, "codex moved on\n")

	before := memoryCommitCount(t)
	res, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, v1, "", time.Now())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// live が v1 の内容に戻っていること（3 方向すべて）。
	if got := memoryReadLive(t, filepath.Join(mem, "a.md")); got != "first\n" {
		t.Errorf("overwritten file not restored: %q", got)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "nested", "b.md")); got != "nested\n" {
		t.Errorf("deleted file not restored: %q", got)
	}
	if _, err := os.Stat(filepath.Join(mem, "added.md")); !os.IsNotExist(err) {
		t.Errorf("file added after the target rev survived the restore (err=%v)", err)
	}

	// 履歴は書き換えず 2 件積む（pre-restore + restore）。
	if n := memoryCommitCount(t); n != before+2 {
		t.Fatalf("commits %d → %d, want +2 (pre-restore + restore)", before, n)
	}
	if res.PreRestore == "" || res.Rev == "" || !res.Committed {
		t.Fatalf("restore result: %+v", res)
	}
	if res.From != v1 {
		t.Errorf("From = %q, want %q", res.From, v1)
	}
	list, err := memoryListSnapshots(4, "")
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Rev != res.Rev || list[0].Trigger != memoryTriggerRestore {
		t.Errorf("newest snapshot = %+v, want the restore commit", list[0])
	}
	if list[1].Rev != res.PreRestore || list[1].Trigger != memoryTriggerPreRestore {
		t.Errorf("second snapshot = %+v, want the pre-restore commit", list[1])
	}
	// 戻し元と適用範囲が trailer から復元できる（監査・「巻き戻しの巻き戻し」の手掛かり）。
	body, err := memoryGitRun("log", "-1", "--pretty=%B", res.Rev)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "AF-Restore-Rev: "+v1) ||
		!strings.Contains(body, "AF-Restore-Scope: claude/projects/"+slug) {
		t.Errorf("restore trailers missing:\n%s", body)
	}
	if !strings.HasPrefix(body, "restore: ") {
		t.Errorf("restore commit subject should say restore:\n%s", body)
	}

	// scope 外（codex）は一切巻き戻らない。
	if got := memoryReadLive(t, codexIndex); got != "codex moved on\n" {
		t.Errorf("project-scoped restore touched the codex root: %q", got)
	}
	// pre-restore が「戻す前の live」を確かに保全している。
	if d, err := memoryDiff("", res.PreRestore, ""); err != nil || !strings.Contains(d, "codex moved on") {
		t.Errorf("pre-restore snapshot did not capture the live state: err=%v\n%s", err, d)
	}
}

// 全体 restore と「巻き戻しの巻き戻し」（★2）: pre-restore rev へもう一度 restore すれば
// 元の状態に戻る。日時指定（at）も同じ restore に解決する。
func TestMemoryRestoreAllScopeIsReversible(t *testing.T) {
	home, cfg, slug := memoryTestEnv(t)
	memoryRestoreSeed(t, cfg, slug)
	mem := filepath.Join(cfg, "projects", slug, "memory")
	codexIndex := filepath.Join(home, ".codex", "memories", "MEMORY.md")

	// v1 の時刻より後・v2 より前を狙えないので、ここは rev ではなく「今」で全体を撮り直し、
	// その時刻を at 指定の狙点にする。
	memoryWrite(t, codexIndex, "codex v3\n")
	v3, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v3.Committed {
		t.Fatalf("v3: %+v err=%v", v3, err)
	}
	at, err := memoryGitRun("log", "-1", "--pretty=%aI", v3.Rev)
	if err != nil {
		t.Fatal(err)
	}

	// さらに live を動かしてから、at 指定で v3 へ全体 restore。
	memoryWrite(t, filepath.Join(mem, "a.md"), "v4\n")
	memoryWrite(t, codexIndex, "codex v4\n")
	back, err := memoryRestore(memoryRestoreScope{All: true}, "", at, time.Now())
	if err != nil {
		t.Fatalf("restore all: %v", err)
	}
	if back.From != v3.Rev {
		t.Fatalf("at resolved to %s, want %s", back.From, v3.Rev)
	}
	if got := memoryReadLive(t, codexIndex); got != "codex v3\n" {
		t.Errorf("codex root not restored by all-scope: %q", got)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "a.md")); got != "rewritten\n" {
		t.Errorf("claude root not restored by all-scope: %q", got)
	}
	if len(back.Scopes) != 2 {
		t.Errorf("all-scope should cover both roots, got %v", back.Scopes)
	}

	// 巻き戻しの巻き戻し: pre-restore rev へ戻せば v4 の状態が返ってくる。
	undo, err := memoryRestore(memoryRestoreScope{All: true}, back.PreRestore, "", time.Now())
	if err != nil {
		t.Fatalf("undo restore: %v", err)
	}
	if !undo.Committed {
		t.Errorf("undo restore did not change anything: %+v", undo)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "a.md")); got != "v4\n" {
		t.Errorf("undo did not bring back the pre-restore state: %q", got)
	}
	if got := memoryReadLive(t, codexIndex); got != "codex v4\n" {
		t.Errorf("undo did not bring back the pre-restore codex state: %q", got)
	}
}

// ★1 の裏返し: restore は allowlist の外へ書かない・消さない。live に置かれた
// シンボリックリンクの先（資格情報）を書き換えないこと、メモリ以外のファイル
// （transcript・settings・資格情報）が一切消えないことを見る。
func TestMemoryRestoreNeverTouchesNonMemoryFiles(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	v1, _ := memoryRestoreSeed(t, cfg, slug)
	proj := filepath.Join(cfg, "projects", slug)
	mem := filepath.Join(proj, "memory")
	creds := filepath.Join(cfg, ".credentials.json")

	// 復元先そのものを、allowlist の外を指すシンボリックリンクにすり替える。
	// リンク越しに書けば資格情報が壊れる（そうなってはいけない）。
	outside := filepath.Join(cfg, "outside.md")
	memoryWrite(t, outside, "outside content\n")
	if err := os.Remove(filepath.Join(mem, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mem, "a.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, v1, "", time.Now()); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if got := memoryReadLive(t, outside); got != "outside content\n" {
		t.Errorf("restore wrote through a symlink: %q", got)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "a.md")); got != "first\n" {
		t.Errorf("symlink was not replaced by the restored file: %q", got)
	}
	// メモリ以外は 1 つも消えない。
	for _, p := range []string{
		creds,
		filepath.Join(cfg, "settings.json"),
		filepath.Join(cfg, "af-usage.json"),
		filepath.Join(proj, "abcd-1234.jsonl"),
		filepath.Join(proj, "subagents", "agent-1.jsonl"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("restore removed a non-memory file %s: %v", p, err)
		}
	}
	if got := memoryReadLive(t, creds); got != `{"token":"SECRET"}` {
		t.Errorf("credentials were modified: %q", got)
	}
	// 資格情報の中身は restore 後の履歴にも入っていない。
	if blobs, _ := memoryGitRun("grep", "-I", "-l", "SECRET", memoryBranch); strings.TrimSpace(blobs) != "" {
		t.Errorf("credential contents reachable in repo: %s", blobs)
	}
}

// 削除で空になったディレクトリは畳むが、メモリ以外が残っている枝では必ず止まる。
// （`memory/` の中身は拡張子に関係なく全部が管理対象 — allowlist はパスで決まる。
// 非メモリが同居するのは `memory/` の外側＝プロジェクト直下の transcript 等。）
func TestMemoryRestorePrunesEmptyDirsOnly(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	proj := filepath.Join(cfg, "projects", slug)
	mem := filepath.Join(proj, "memory")

	v1, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v1.Committed {
		t.Fatalf("v1: %+v err=%v", v1, err)
	}
	// v1 以降に増えたサブディレクトリは、戻すと空になるので畳まれる。
	memoryWrite(t, filepath.Join(mem, "onlymem", "c.md"), "c\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, v1.Rev, "", time.Now()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mem, "onlymem")); !os.IsNotExist(err) {
		t.Errorf("emptied memory dir was not pruned (err=%v)", err)
	}
	if _, err := os.Stat(mem); err != nil {
		t.Errorf("memory dir with remaining files was pruned: %v", err)
	}

	// メモリが 1 つも無い時点へ戻すと memory/ ごと畳むが、transcript がいる
	// プロジェクト直下では必ず止まる（os.Remove は空ディレクトリしか消せない）。
	if err := os.RemoveAll(mem); err != nil {
		t.Fatal(err)
	}
	empty, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !empty.Committed {
		t.Fatalf("empty snapshot: %+v err=%v", empty, err)
	}
	memoryWrite(t, filepath.Join(mem, "back.md"), "back\n")
	if _, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, empty.Rev, "", time.Now()); err != nil {
		t.Fatalf("restore to empty: %v", err)
	}
	if _, err := os.Stat(mem); !os.IsNotExist(err) {
		t.Errorf("fully emptied memory dir was not pruned (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "abcd-1234.jsonl")); err != nil {
		t.Errorf("pruning climbed past the memory dir and hit the transcript: %v", err)
	}
}

// tree API: 「その時点に何が入っていたか」。今の live から消えたプロジェクトも当時の
// snapshot からは選べる（= 誤って消したメモリを戻せる）。
func TestMemoryTreeAtListsHistoricalProjects(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	v1, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v1.Committed {
		t.Fatalf("v1: %+v err=%v", v1, err)
	}
	// live からプロジェクトのメモリを丸ごと消して撮り直す。
	if err := os.RemoveAll(filepath.Join(cfg, "projects", slug, "memory")); err != nil {
		t.Fatal(err)
	}
	v2, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v2.Committed {
		t.Fatalf("v2: %+v err=%v", v2, err)
	}

	sha, kinds, projects, err := memoryTreeAt(v1.Rev, "")
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if sha != v1.Rev {
		t.Errorf("tree rev = %s, want %s", sha, v1.Rev)
	}
	if len(projects) != 1 || projects[0].Slug != slug || projects[0].Files != 3 {
		t.Fatalf("tree projects = %+v", projects)
	}
	if projects[0].Display != "demo" || projects[0].Bytes <= 0 {
		t.Errorf("tree project detail = %+v", projects[0])
	}
	if len(kinds) != 2 || kinds[0].Kind != "claude" || !kinds[0].Scopes || kinds[1].Scopes {
		t.Fatalf("tree kinds = %+v", kinds)
	}
	// 現時点（v2）では claude 側が空になっている。
	_, kinds2, projects2, err := memoryTreeAt(v2.Rev, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects2) != 0 || len(kinds2) != 1 || kinds2[0].Kind != "codex" {
		t.Fatalf("tree at v2 = %+v / %+v", kinds2, projects2)
	}

	// そして v1 へ戻せばメモリが返ってくる（本命のユースケース）。
	if _, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, v1.Rev, "", time.Now()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := memoryReadLive(t, filepath.Join(cfg, "projects", slug, "memory", "MEMORY.md")); got == "" {
		t.Error("deleted project memory was not restored")
	}
}

// スコープの検証: 未知の kind・不正な slug・空スコープは弾く。
func TestMemoryResolveScopeRejectsBadInput(t *testing.T) {
	memoryTestEnv(t)
	for _, sc := range []memoryRestoreScope{
		{},
		{Kinds: []string{"opencode"}},
		{Projects: []string{"../escape"}},
		{Projects: []string{"a/b"}},
		{Projects: []string{""}},
	} {
		if _, err := memoryResolveScope(sc); err == nil {
			t.Errorf("scope %+v was accepted", sc)
		}
	}
	// 全体指定はプロジェクト指定を飲み込む（重複した prefix を 2 度適用しない）。
	targets, err := memoryResolveScope(memoryRestoreScope{All: true, Projects: []string{"-home-dev-repos-demo"}})
	if err != nil || len(targets) != 2 {
		t.Fatalf("all+projects = %+v err=%v", targets, err)
	}
}

// REST 越しの往復（ルート登録・応答形・エラーコード）。CP 側の登録漏れは別途
// control-plane のテストで見るが、Agent 側はここで固定する。
func TestMemoryRestoreAPI(t *testing.T) {
	h := memoryAPIHandler(t)
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	slug := "-home-dev-repos-demo"
	v1, _ := memoryRestoreSeed(t, cfg, slug)

	// tree: 選択肢は「その時点」の中身から出る。
	w := smokeDo(t, h, "GET", "/agents/memory/tree?rev="+v1, "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("tree: %d %s", w.Code, w.Body.String())
	}
	var tree struct {
		Rev      string              `json:"rev"`
		Kinds    []memoryTreeKind    `json:"kinds"`
		Projects []memoryTreeProject `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tree); err != nil {
		t.Fatalf("tree decode: %v (%s)", err, w.Body.String())
	}
	if tree.Rev != v1 || len(tree.Projects) != 1 || tree.Projects[0].Slug != slug {
		t.Fatalf("tree = %+v", tree)
	}

	// restore: プロジェクト指定で往復する。
	w = smokeDo(t, h, "POST", "/agents/memory/restore", "smoke-token",
		`{"rev":"`+v1+`","scope":{"projects":["`+slug+`"]}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	var res memoryRestoreResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || !res.Committed || res.Rev == "" {
		t.Fatalf("restore result: %+v err=%v (%s)", res, err, w.Body.String())
	}
	if len(res.Deleted) != 1 || !strings.HasSuffix(res.Deleted[0], "/added.md") {
		t.Errorf("deleted = %v", res.Deleted)
	}
	if len(res.Written) != 2 {
		t.Errorf("written = %v", res.Written)
	}
	if res.Busy {
		t.Errorf("no session is running, busy should be false: %+v", res)
	}
	if got := memoryReadLive(t, filepath.Join(cfg, "projects", slug, "memory", "a.md")); got != "first\n" {
		t.Errorf("live not restored through the API: %q", got)
	}

	// 入力の検証: 安定コードで 400 / 404 を返す。
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{"rev":"` + v1 + `"}`, http.StatusBadRequest, errCodeMemoryBadScope},
		{`{"rev":"` + v1 + `","scope":{"kinds":["opencode"]}}`, http.StatusBadRequest, errCodeMemoryBadScope},
		{`{"rev":"` + v1 + `","scope":{"projects":["../escape"]}}`, http.StatusBadRequest, errCodeMemoryBadScope},
		{`{"rev":"nope","scope":{"all":true}}`, http.StatusBadRequest, errCodeMemoryBadRev},
		{`{"scope":{"all":true}}`, http.StatusBadRequest, errCodeMemoryBadRev},
		{`{"rev":"--upload-pack=evil","scope":{"all":true}}`, http.StatusBadRequest, errCodeMemoryBadRev},
	} {
		w := smokeDo(t, h, "POST", "/agents/memory/restore", "smoke-token", c.body)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("restore %s: %d %s (want %d %s)", c.body, w.Code, w.Body.String(), c.code, c.want)
		}
	}
}

// snapshot が 1 件も無いうちは restore / tree とも 404（安定コード）で返す。
func TestMemoryRestoreBeforeAnySnapshot(t *testing.T) {
	h := memoryAPIHandler(t)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/agents/memory/restore", `{"rev":"HEAD","scope":{"all":true}}`},
		{"GET", "/agents/memory/tree?rev=HEAD", ""},
	} {
		w := smokeDo(t, h, c.method, c.path, "smoke-token", c.body)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), errCodeMemoryNoSnapshots) {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// 自動 snapshot の UI トグル（docs/log/39 決着 #1）: 設定は claude マウント内に永続し、
// 環境変数の強制 OFF はトグルより強い。
func TestMemoryAutoToggle(t *testing.T) {
	h := memoryAPIHandler(t)
	readAuto := func() (auto, locked bool) {
		t.Helper()
		w := smokeDo(t, h, "GET", "/agents/memory/roots", "smoke-token", "")
		var out struct {
			Auto   bool `json:"auto"`
			Locked bool `json:"autoLocked"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("roots decode: %v (%s)", err, w.Body.String())
		}
		return out.Auto, out.Locked
	}

	if auto, locked := readAuto(); !auto || locked {
		t.Fatalf("default should be auto=on unlocked, got %v/%v", auto, locked)
	}
	if w := smokeDo(t, h, "PUT", "/agents/memory/settings", "smoke-token", `{"auto":false}`); w.Code != http.StatusOK {
		t.Fatalf("turn off: %d %s", w.Code, w.Body.String())
	}
	if auto, _ := readAuto(); auto {
		t.Error("toggle off did not persist")
	}
	if memoryAutoEnabled() {
		t.Error("the snapshot loop would still run after the toggle was turned off")
	}
	if w := smokeDo(t, h, "PUT", "/agents/memory/settings", "smoke-token", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("missing auto: %d %s", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "PUT", "/agents/memory/settings", "smoke-token", `{"auto":true}`); w.Code != http.StatusOK {
		t.Fatalf("turn on: %d %s", w.Code, w.Body.String())
	}
	if auto, _ := readAuto(); !auto {
		t.Error("toggle on did not persist")
	}

	// 運用側の強制 OFF は UI から戻せない。
	t.Setenv("AF_MEMORY_SNAPSHOT", "off")
	if auto, locked := readAuto(); auto || !locked {
		t.Fatalf("env override should force auto off and mark it locked, got %v/%v", auto, locked)
	}
	smokeDo(t, h, "PUT", "/agents/memory/settings", "smoke-token", `{"auto":true}`)
	if auto, _ := readAuto(); auto {
		t.Error("UI toggle overrode the operator's AF_MEMORY_SNAPSHOT=off")
	}
}

func TestMemoryP2RoutesRegistered(t *testing.T) {
	mux := buildMux()
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/agents/memory/tree", "GET /agents/memory/tree"},
		{"POST", "/agents/memory/restore", "POST /agents/memory/restore"},
		{"PUT", "/agents/memory/settings", "PUT /agents/memory/settings"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if _, pattern := mux.Handler(req); pattern != c.want {
			t.Errorf("%s %s resolved to %q, want %q", c.method, c.path, pattern, c.want)
		}
	}
}
