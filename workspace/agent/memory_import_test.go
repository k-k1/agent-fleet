package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// memoryLiveOrEmpty は live 側のメモリファイルの中身（無ければ ""）。存在の有無自体を
// 主張したい箇所で使う（memoryReadLive は無いと落ちる）。
func memoryLiveOrEmpty(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// import の本命: **別環境の bundle を持ち込み、選んだプロジェクトだけ置き換え、
// それを restore で元に戻せる**こと（docs/log/39 P3 の出口条件）。
//
// 1 テスト内で HOME / CLAUDE_CONFIG_DIR を差し替えて 2 環境を演じる。同じ repo 名なら
// slug はパス由来で一致する、という前提（docs/log/39 ⑤ slug 互換性）もこの形で確認できる。
func TestMemoryImportBundleRoundTrip(t *testing.T) {
	share := t.TempDir() // 環境をまたいで持ち回るファイル置き場

	// --- 環境 A: 特徴のある内容で snapshot し、bundle を書き出す ---
	_, cfgA, slug := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "from env A\n")
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "only-a.md"), "only in A\n")
	if res, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil || !res.Committed {
		t.Fatalf("env A snapshot: %+v err=%v", res, err)
	}
	src, err := memoryExportBundle()
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	bundle := filepath.Join(share, "af-memory.bundle")
	if err := memoryCopyFile(src, bundle); err != nil {
		t.Fatalf("stash bundle: %v", err)
	}

	// --- 環境 B: 別の内容を持つ独立した環境 ---
	_, cfgB, _ := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgB, slug, "a.md"), "from env B\n")
	if res, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil || !res.Committed {
		t.Fatalf("env B snapshot: %+v err=%v", res, err)
	}

	pv, err := memoryImportPrepare(bundle, "af-memory.bundle", time.Now())
	if err != nil {
		t.Fatalf("import prepare: %v", err)
	}
	if pv.Format != memoryFormatBundle || pv.Head == "" || pv.Snapshots < 1 {
		t.Fatalf("preview = %+v", pv)
	}
	if !strings.HasPrefix(pv.Ref, "refs/imports/") {
		t.Errorf("import must land on an independent lineage, got ref %q", pv.Ref)
	}
	if len(pv.Rejected) != 0 || len(pv.Unavailable) != 0 || len(pv.Secrets) != 0 {
		t.Errorf("clean bundle should have nothing to flag: %+v", pv)
	}
	var found *memoryTreeProject
	for i := range pv.Projects {
		if pv.Projects[i].Slug == slug {
			found = &pv.Projects[i]
		}
	}
	if found == nil {
		t.Fatalf("imported projects = %+v, want %s", pv.Projects, slug)
	}
	// 取り込んだだけでは live に触れない（適用は明示操作）。
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env B\n" {
		t.Fatalf("prepare must not touch live memory, got %q", got)
	}
	// ローカル履歴は main のまま（graft しない）。
	if before := memoryCommitCount(t); before != 1 {
		t.Fatalf("import added %d commits to the local lineage", before-1)
	}

	// --- 選択適用: claude のこのプロジェクトだけを置き換える ---
	res, err := memoryImportApply(pv.ImportID, memoryRestoreScope{Projects: []string{slug}}, time.Now(), memoryApplyOpts{})
	if err != nil {
		t.Fatalf("import apply: %v", err)
	}
	if !res.Committed || res.PreRestore == "" {
		t.Fatalf("apply result = %+v", res)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env A\n" {
		t.Fatalf("a.md after import = %q", got)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "only-a.md")); got != "only in A\n" {
		t.Fatalf("only-a.md was not brought in: %q", got)
	}
	// scope 外（codex）は 1 バイトも動かない。
	if got := memoryLiveOrEmpty(t, filepath.Join(os.Getenv("HOME"), ".codex", "memories", "MEMORY.md")); got != "codex index\n" {
		t.Fatalf("codex memory touched by a project-scoped import: %q", got)
	}
	// 契機が履歴に残る（一覧の先頭が import）。
	list, err := memoryListSnapshots(10, "")
	if err != nil || len(list) == 0 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if list[0].Trigger != memoryTriggerImport {
		t.Errorf("newest snapshot trigger = %q, want import", list[0].Trigger)
	}

	// --- 取り込みも巻き戻せる: pre-restore 時点へ戻すと環境 B の内容に復帰する ---
	back, err := memoryRestore(memoryRestoreScope{Projects: []string{slug}}, res.PreRestore, "", time.Now())
	if err != nil {
		t.Fatalf("restore after import: %v", err)
	}
	if !back.Committed {
		t.Fatalf("restore after import committed nothing: %+v", back)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env B\n" {
		t.Fatalf("a.md after undo = %q", got)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "only-a.md")); got != "" {
		t.Fatalf("only-a.md should be gone after undo: %q", got)
	}
}

// 取り込み先が**まだ空のワークスペース**でも適用できること。live のルート
// （<CLAUDE_CONFIG_DIR>/projects）は claude が一度起動して初めて出来るので、
// 「新しい環境を立てて、真っ先に前の環境のメモリを持ち込む」という本命の使い方では
// ルート自体が存在しない。ここを作らずに書きに行くと ENOENT で適用だけが失敗する。
func TestMemoryImportAppliesWhenLiveRootMissing(t *testing.T) {
	share := t.TempDir()

	// --- 環境 A: 持ち出す側 ---
	_, cfgA, slug := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "from env A\n")
	if res, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil || !res.Committed {
		t.Fatalf("env A snapshot: %+v err=%v", res, err)
	}
	src, err := memoryExportBundle()
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	bundle := filepath.Join(share, "af-memory.bundle")
	if err := memoryCopyFile(src, bundle); err != nil {
		t.Fatalf("stash bundle: %v", err)
	}

	// --- 環境 B: 起動直後のワークスペース（projects/ がまだ無い） ---
	homeB := t.TempDir()
	cfgB := filepath.Join(homeB, "claude-config")
	memoryMkdirAll(t, cfgB)
	t.Setenv("HOME", homeB)
	t.Setenv("CLAUDE_CONFIG_DIR", cfgB)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(homeB, "sessions"))
	if _, err := os.Stat(filepath.Join(cfgB, "projects")); !os.IsNotExist(err) {
		t.Fatalf("precondition: projects/ must not exist yet (err=%v)", err)
	}

	pv, err := memoryImportPrepare(bundle, "af-memory.bundle", time.Now())
	if err != nil {
		t.Fatalf("import prepare: %v", err)
	}
	res, err := memoryImportApply(pv.ImportID, memoryRestoreScope{Projects: []string{slug}}, time.Now(), memoryApplyOpts{})
	if err != nil {
		t.Fatalf("import apply into an empty workspace: %v", err)
	}
	if !res.Committed || len(res.Written) == 0 {
		t.Fatalf("apply result = %+v", res)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env A\n" {
		t.Fatalf("a.md after import = %q", got)
	}
	if st, err := os.Stat(filepath.Join(cfgB, "projects")); err != nil || !st.IsDir() {
		t.Fatalf("projects/ was not created: %v", err)
	} else if st.Mode().Perm() != 0o700 {
		t.Errorf("projects/ mode = %v, want 0700", st.Mode().Perm())
	}
}

// 移設（mode=migrate）: bundle が運んできた**履歴ごと**この環境の履歴にする。
// 既定の適用は最新ツリーしか使わないので、相手の過去は refs/imports に埋もれたままだった
// （10 本を超えると刈られる）。移設後は相手の各 snapshot が履歴一覧に並び、**その途中の
// 時点へ巻き戻せる**ことまでを見る。入れ替えた元の履歴は退避 ref に残る。
func TestMemoryImportMigrateAdoptsLineage(t *testing.T) {
	share := t.TempDir()

	// --- 環境 A: 2 世代の履歴を作って bundle を書き出す ---
	_, cfgA, slug := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "A1\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "A2\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	countA := memoryCommitCount(t)
	if countA < 2 {
		t.Fatalf("env A history = %d commits, want >= 2", countA)
	}
	src, err := memoryExportBundle()
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	bundle := filepath.Join(share, "af-memory.bundle")
	if err := memoryCopyFile(src, bundle); err != nil {
		t.Fatalf("stash bundle: %v", err)
	}

	// --- 環境 B: 自分の履歴を 1 つ持つ環境へ移設する ---
	_, cfgB, _ := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgB, slug, "a.md"), "from env B\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	localHead, err := memoryGitRun("rev-parse", memoryBranch)
	if err != nil {
		t.Fatal(err)
	}

	pv, err := memoryImportPrepare(bundle, "af-memory.bundle", time.Now())
	if err != nil {
		t.Fatalf("import prepare: %v", err)
	}
	res, err := memoryImportApply(pv.ImportID, memoryRestoreScope{}, time.Now(), memoryApplyOpts{Adopt: true})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 系譜が入れ替わり、元の main は退避 ref に残っている（履歴は消さない）。
	if !res.Adopted || res.Replaced != localHead || res.ReplacedRef == "" {
		t.Fatalf("migrate result = %+v (local head was %s)", res, localHead)
	}
	if got, err := memoryGitRun("rev-parse", "--verify", "--quiet", res.ReplacedRef); err != nil || got != localHead {
		t.Fatalf("replaced lineage was not stashed: %q %v", got, err)
	}
	if _, err := memoryGitRun("merge-base", "--is-ancestor", pv.Head, memoryBranch); err != nil {
		t.Fatalf("main does not descend from the imported head: %v", err)
	}
	// 入れ替え前の main は新しい系譜には含まれない（内容の混合はしていない）。
	if err := memoryGitRun2(t, "merge-base", "--is-ancestor", localHead, memoryBranch); err == nil {
		t.Errorf("migrate must not graft the local lineage onto main")
	}

	// 相手の履歴が「この環境の履歴」として一覧に出る（範囲は全体固定なので kind も跨ぐ）。
	list, err := memoryListSnapshots(50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < countA {
		t.Fatalf("history after migrate = %d entries, want >= %d (the imported lineage)", len(list), countA)
	}
	if list[0].Trigger != memoryTriggerImport {
		t.Errorf("newest snapshot trigger = %q, want import", list[0].Trigger)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "A2\n" {
		t.Fatalf("live after migrate = %q, want the imported head", got)
	}

	// **本命**: 相手の履歴の途中（1 世代前）へ巻き戻せる。
	older := list[len(list)-1].Rev // 取り込んだ系譜の最初の snapshot
	if _, err := memoryRestore(memoryRestoreScope{All: true}, older, "", time.Now()); err != nil {
		t.Fatalf("restore to an imported point: %v", err)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "A1\n" {
		t.Fatalf("a.md after rolling back into the imported history = %q, want A1", got)
	}
}

// memoryGitRun2 は「失敗を期待する」git 呼び出し用の薄い包み（t を取るのは呼び出し側の
// 意図を読みやすくするためだけ）。
func memoryGitRun2(t *testing.T, args ...string) error {
	t.Helper()
	_, err := memoryGitRun(args...)
	return err
}

// tar.gz の取り込み（★3 外部入力）: traversal・allowlist 外・通常ファイル以外は
// **書かずに rejected へ落とす**。適用されるのは許可された md だけ。
func TestMemoryImportTarRejectsHostileEntries(t *testing.T) {
	share := t.TempDir()
	_, cfg, slug := memoryTestEnv(t)
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}

	ok := "claude/projects/" + slug + "/memory/imported.md"
	archive := filepath.Join(share, "hostile.tar.gz")
	memoryWriteTarGz(t, archive, []memoryTarEntry{
		{Name: "manifest.json", Body: `{"format":"af-memory-tar","version":1}`},
		{Name: ok, Body: "imported\n"},
		{Name: "../../../etc/passwd", Body: "root:x:0:0\n"},
		{Name: "claude/projects/" + slug + "/abcd.jsonl", Body: `{"type":"user"}`},
		{Name: "claude/projects/" + slug + "/../../.credentials.json", Body: `{"token":"SECRET"}`},
		{Name: "codex/.git/config", Body: "[core]\n"},
		{Name: "claude/projects/" + slug + "/memory/link.md", Link: "/etc/passwd"},
	})

	pv, err := memoryImportPrepare(archive, "hostile.tar.gz", time.Now())
	if err != nil {
		t.Fatalf("import prepare: %v", err)
	}
	if pv.Format != memoryFormatTar {
		t.Fatalf("format = %q", pv.Format)
	}
	for _, want := range []string{"../../../etc/passwd", "codex/.git/config"} {
		if !containsString(pv.Rejected, want) {
			t.Errorf("%q should have been rejected: %v", want, pv.Rejected)
		}
	}
	if len(pv.Rejected) != 5 {
		t.Errorf("rejected = %v, want the 5 hostile entries", pv.Rejected)
	}
	// 取り込んだツリーは許可された 1 件だけ。
	files, err := memoryGitRun("ls-tree", "-r", "--name-only", pv.Head)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(files) != ok {
		t.Fatalf("imported tree = %q, want only %q", files, ok)
	}

	res, err := memoryImportApply(pv.ImportID, memoryRestoreScope{Projects: []string{slug}}, time.Now(), memoryApplyOpts{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Committed {
		t.Fatalf("apply committed nothing: %+v", res)
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfg, slug, "imported.md")); got != "imported\n" {
		t.Fatalf("imported.md = %q", got)
	}
	// 敵対エントリはどこにも書かれていない。
	if _, err := os.Stat(filepath.Join(cfg, ".credentials.json")); err == nil {
		if b, _ := os.ReadFile(filepath.Join(cfg, ".credentials.json")); !strings.Contains(string(b), `{"token":"SECRET"}`) {
			t.Error("credentials file was overwritten by the import")
		}
	}
	if _, err := os.Lstat(memoryProjectMemPath(cfg, slug, "link.md")); err == nil {
		t.Error("a symlink entry was materialised into the live tree")
	}
	// 置き換え方式なので、tar に無かった既存メモリは消える（3-way merge をしない帰結）。
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfg, slug, "a.md")); got != "" {
		t.Errorf("import should replace the selected project, a.md = %q", got)
	}
}

// 形式の判定は中身のマジックで行う（拡張子は信用しない）。壊れた入力は 400。
func TestMemoryImportRejectsUnknownFormat(t *testing.T) {
	share := t.TempDir()
	memoryTestEnv(t)
	junk := filepath.Join(share, "notes.bundle")
	memoryWrite(t, junk, "just some text, not a bundle\n")
	_, err := memoryImportPrepare(junk, "notes.bundle", time.Now())
	var ue *memoryUserErr
	if err == nil || !errors.As(err, &ue) || ue.Code != errCodeMemoryBadImport {
		t.Fatalf("import of junk: err=%v", err)
	}
	// apply の importId も検証される（ref 名として git へ渡るため）。
	if _, err := memoryImportApply("../../evil", memoryRestoreScope{All: true}, time.Now(), memoryApplyOpts{}); err == nil {
		t.Error("apply accepted a traversal-shaped importId")
	}
}

// REST 越しの往復（multipart 受領 → preview → apply）。CP は body を素通しするので、
// ここが通れば Console からの経路も通る。
func TestMemoryImportAPI(t *testing.T) {
	share := t.TempDir()
	_, cfgA, slug := memoryTestEnv(t)
	memoryWrite(t, memoryProjectMemPath(cfgA, slug, "a.md"), "from env A\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	src, err := memoryExportBundle()
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(share, "af-memory.bundle")
	if err := memoryCopyFile(src, bundle); err != nil {
		t.Fatal(err)
	}

	_, cfgB, _ := memoryTestEnv(t)
	t.Setenv("AGENT_TOKEN", "smoke-token")
	h := httpx.RequireToken(buildMux())
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}

	body, ctype := memoryMultipart(t, "af-memory.bundle", bundle)
	req := httptest.NewRequest("POST", "/agents/memory/import", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer smoke-token")
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	var pv memoryImportPreview
	if err := json.Unmarshal(w.Body.Bytes(), &pv); err != nil {
		t.Fatalf("preview decode: %v (%s)", err, w.Body.String())
	}
	if pv.ImportID == "" || len(pv.Projects) == 0 {
		t.Fatalf("preview = %+v", pv)
	}

	apply, _ := json.Marshal(map[string]any{
		"importId": pv.ImportID,
		"scope":    map[string]any{"projects": []string{slug}},
	})
	w = smokeDo(t, h, "POST", "/agents/memory/import/apply", "smoke-token", string(apply))
	if w.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	if got := memoryLiveOrEmpty(t, memoryProjectMemPath(cfgB, slug, "a.md")); got != "from env A\n" {
		t.Fatalf("a.md after API import = %q", got)
	}
	// 不正な importId は 400（ref 名に使う値なので必ず検証する）。
	if w := smokeDo(t, h, "POST", "/agents/memory/import/apply", "smoke-token", `{"importId":"x/../y","scope":{"all":true}}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad importId: %d %s", w.Code, w.Body.String())
	}
	// 中身の無いリクエストは 400。
	if w := smokeDo(t, h, "POST", "/agents/memory/import", "smoke-token", ""); w.Code != http.StatusBadRequest {
		t.Errorf("import without a file: %d %s", w.Code, w.Body.String())
	}
}

// memoryTarEntry は組み立てるアーカイブの 1 エントリ（Link 非空 = シンボリックリンク）。
type memoryTarEntry struct {
	Name string
	Body string
	Link string
}

func memoryWriteTarGz(t *testing.T, path string, entries []memoryTarEntry) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		h := &tar.Header{Name: e.Name, Mode: 0o600, Size: int64(len(e.Body)), Typeflag: tar.TypeReg}
		if e.Link != "" {
			h = &tar.Header{Name: e.Name, Mode: 0o777, Linkname: e.Link, Typeflag: tar.TypeSymlink}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.Link == "" {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func memoryMultipart(t *testing.T, name, path string) ([]byte, string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}
