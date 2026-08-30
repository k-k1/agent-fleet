package main

// エージェントメモリの版管理（docs/log/39 ⑤ / ADR 0022 決定 5）— import。
//
//	受領（multipart）──▶ 検証 ──▶ refs/imports/<id>/* として取り込む ──▶ preview
//	                                                                    │
//	                          プロジェクト/kind を選んで「置き換え = 新 commit」◀┘
//
// 設計の芯は 2 つ:
//
//   - **独立系譜として受け入れる**。bundle は `refs/imports/<id>/*` へ fetch し、ローカルの
//     main には graft しない。tar.gz も同じ形に揃える（展開 → 専用 index で write-tree →
//     commit-tree → 同じ ref 空間）ので、preview も apply も経路が 1 本になる。
//     取り込まなかった側もローカル履歴に残るので「取り込んで後悔」が起きない。
//
//   - **適用は 3-way merge ではなく選択置き換え**。.md の意味的衝突は機械で解決できない
//     （ADR 0022 決定 5）。選んだ範囲だけを restore と同じ経路で live へ書き、結果を
//     AF-Trigger: import の commit として積む。つまり import も巻き戻せる。
//
// ★3（import は外部入力）: サイズ上限・tar の traversal 防御・allowlist 外エントリの拒否・
// bundle verify 必須。加えて live へ書く段は restore と同じ memoryApplyScopeToLive を通る
// ので、repo に何が入っていようと allowlist の外へは 1 バイトも書かれない。

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	memoryImportDefaultMax = 64 << 20 // 受領サイズ上限の既定（docs/log/39 ★3）
	memoryImportMaxEntries = 20000    // tar のエントリ数上限
	memoryImportMaxFile    = 8 << 20  // tar の 1 ファイル上限
	memoryImportKeepRefs   = 10       // 保持する取り込み系譜の本数

	// 適用の仕方（REST の mode）。replace = 選んだ範囲の内容だけ採る（既定・履歴は自分の
	// まま）。migrate = 履歴ごと入れ替える（移設・範囲は全体固定）。
	memoryImportModeReplace = "replace"
	memoryImportModeMigrate = "migrate"
)

// memoryImportIDRe は importId の形（生成側と同じ）。apply は URL/本文から来た値を
// ref 名に使うので、ここを通ったものしか git へ渡さない。
var memoryImportIDRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z(-[0-9]+)?$`)

func memoryImportMaxBytes() int64 {
	if v := strings.TrimSpace(os.Getenv("AF_MEMORY_IMPORT_MAX")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return memoryImportDefaultMax
}

// memoryImportPreview は取り込んだ系譜の概況。Console はこれを見せて適用範囲を選ばせる。
type memoryImportPreview struct {
	ImportID  string              `json:"importId"`
	Format    string              `json:"format"` // bundle | tar
	Ref       string              `json:"ref"`    // refs/imports/<id>/<name>
	Head      string              `json:"head"`   // 取り込んだ先頭 commit
	HeadTs    string              `json:"headTs,omitempty"`
	Snapshots int                 `json:"snapshots"` // 系譜に含まれる commit 数
	Kinds     []memoryTreeKind    `json:"kinds"`
	Projects  []memoryTreeProject `json:"projects"`
	// Unavailable はこの環境に無い kind（例: codex memories 未有効）。選ばせない。
	Unavailable []string `json:"unavailable"`
	// Rejected は allowlist 外だったため取り込まなかった / 適用されないパス。
	Rejected []string `json:"rejected"`
	// Secrets は取り込み内容の secret スキャン結果（情報提供。import は本人のデータを
	// 持ち込む操作なのでブロックはしない — 生値は含まない）。
	Secrets []memorySecretFinding `json:"secrets"`
	// SecretScanFailed はスキャン自体の失敗（「検出なし」と区別する。import は
	// ブロックしない方針なのでエラーにはせず、事実だけ返す）。
	SecretScanFailed bool `json:"secretScanFailed,omitempty"`
}

// memoryImportPrepare は受領物を検証して refs/imports/<id>/* に取り込み、preview を返す。
// src は保存済みの一時ファイル、name は元のファイル名（形式の推定に使う補助）。
func memoryImportPrepare(src, name string, now time.Time) (memoryImportPreview, error) {
	var pv memoryImportPreview
	if err := memoryEnsureRepo(); err != nil {
		return pv, err
	}
	format, err := memoryDetectFormat(src, name)
	if err != nil {
		return pv, err
	}
	id, err := memoryNewImportID(now)
	if err != nil {
		return pv, err
	}
	pv.ImportID, pv.Format = id, format

	switch format {
	case memoryFormatBundle:
		pv.Ref, err = memoryImportBundle(src, id)
	default:
		pv.Ref, pv.Rejected, err = memoryImportTar(src, id, now)
	}
	if err != nil {
		return pv, err
	}

	head, err := memoryGitRun("rev-parse", "--verify", "--quiet", pv.Ref+"^{commit}")
	if err != nil || head == "" {
		return pv, memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "the uploaded file contains no memory history")
	}
	pv.Head = head
	if ts, terr := memoryGitRun("log", "-1", "--pretty=format:%aI", head); terr == nil {
		pv.HeadTs = ts
	}
	if c, cerr := memoryGitRun("rev-list", "--count", head); cerr == nil {
		pv.Snapshots, _ = strconv.Atoi(c)
	}
	kinds, projects, terr := memoryTreeOfRev(head)
	if terr != nil {
		return pv, terr
	}
	pv.Kinds, pv.Projects = kinds, projects

	// この環境で受け皿になるルートが無い kind は選ばせない（codex memories 未有効の環境）。
	active := map[string]bool{}
	for _, r := range memoryRoots() {
		active[r.Kind] = true
	}
	pv.Unavailable = []string{}
	for _, k := range kinds {
		if !active[k.Kind] {
			pv.Unavailable = append(pv.Unavailable, k.Kind)
		}
	}
	// bundle は中身を選別できないので、allowlist 外のパスはここで洗い出して見せる
	// （適用段でも memoryApplyScopeToLive が弾くが、事前に分かる方が親切）。
	if format == memoryFormatBundle {
		pv.Rejected = memoryRejectedPaths(head)
	}
	if pv.Rejected == nil {
		pv.Rejected = []string{}
	}
	if pv.Secrets, err = memoryScanRevTree(head); err != nil {
		pv.SecretScanFailed = true // 失敗を「検出なし」に見せない
	}
	if pv.Secrets == nil {
		pv.Secrets = []memorySecretFinding{}
	}
	memoryPruneImportRefs(memoryImportKeepRefs)
	_, _ = memoryGitRun("gc", "--auto", "--quiet") // ★8
	return pv, nil
}

// memoryDetectFormat は中身のマジックで形式を決める（拡張子は信用しない）。
func memoryDetectFormat(src, name string) (string, error) {
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case strings.HasPrefix(string(head), "# v2 git bundle"), strings.HasPrefix(string(head), "# v3 git bundle"):
		return memoryFormatBundle, nil
	case n >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return memoryFormatTar, nil
	}
	return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport,
		"unsupported file %q: expected a git bundle or a .tar.gz produced by export", filepath.Base(name))
}

// memoryNewImportID は refs/imports/<id> の id を作る。同一秒の衝突は連番で避ける。
func memoryNewImportID(now time.Time) (string, error) {
	base := now.UTC().Format("20060102T150405Z")
	for i := 0; i < 100; i++ {
		id := base
		if i > 0 {
			id = base + "-" + strconv.Itoa(i+1)
		}
		out, err := memoryGitRun("for-each-ref", "--format=%(refname)", "refs/imports/"+id+"/")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			return id, nil
		}
	}
	return "", errors.New("could not allocate an import id")
}

// memoryImportBundle は bundle を検証して refs/imports/<id>/* へ fetch する。
// verify を必須にするのは ★3（外部入力）— 壊れた/切り詰められた bundle をここで落とす。
func memoryImportBundle(src, id string) (string, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	if _, err := memoryGitRun("bundle", "verify", abs); err != nil {
		return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "git bundle verify failed: %v", err)
	}
	if _, err := memoryGitRun("fetch", "--no-write-fetch-head", abs,
		"+refs/heads/*:refs/imports/"+id+"/*"); err != nil {
		return "", fmt.Errorf("fetch bundle: %w", err)
	}
	refs, err := memoryGitRun("for-each-ref", "--format=%(refname)", "refs/imports/"+id+"/")
	if err != nil {
		return "", err
	}
	list := []string{}
	for _, r := range strings.Split(refs, "\n") {
		if r = strings.TrimSpace(r); r != "" {
			list = append(list, r)
		}
	}
	if len(list) == 0 {
		return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "the bundle carries no branches")
	}
	// main（= snapshot を積む唯一のブランチ）を優先し、無ければ先頭を採る。
	want := "refs/imports/" + id + "/" + memoryBranch
	for _, r := range list {
		if r == want {
			return r, nil
		}
	}
	sort.Strings(list)
	return list[0], nil
}

// memoryImportTar は tar.gz を検証しつつ展開し、bundle と同じ ref 空間へ commit する。
// 展開先は work dir で、live にも staging にも触れない。
func memoryImportTar(src, id string, now time.Time) (ref string, rejected []string, err error) {
	dir := filepath.Join(memoryWorkDir(), "import-"+id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(dir) // 展開物は commit したら用済み
	rejected, err = memoryExtractTar(src, dir)
	if err != nil {
		return "", rejected, err
	}
	// 専用 index に対して add/write-tree する。staging と bare repo の index を汚さない
	// ため、GIT_INDEX_FILE と GIT_WORK_TREE をこの操作だけ差し替える。
	idx := filepath.Join(memoryWorkDir(), "import-"+id+".index")
	defer os.Remove(idx)
	run := func(args ...string) (string, error) {
		cmd := memoryGit(args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+idx, "GIT_WORK_TREE="+dir)
		out, rerr := cmd.Output()
		s := strings.TrimSpace(string(out))
		if rerr != nil {
			var ee *exec.ExitError
			if errors.As(rerr, &ee) && len(ee.Stderr) > 0 {
				rerr = fmt.Errorf("%v: %s", rerr, strings.TrimSpace(string(ee.Stderr)))
			}
		}
		return s, rerr
	}
	if _, err := run("add", "-A", "."); err != nil {
		return "", rejected, fmt.Errorf("stage imported tree: %w", err)
	}
	tree, err := run("write-tree")
	if err != nil || tree == "" {
		return "", rejected, fmt.Errorf("write imported tree: %w", err)
	}
	msg := "import: " + now.UTC().Format(time.RFC3339) + " (tar)\n\nAF-Trigger: " + memoryTriggerImport + "\n"
	commit, err := run("commit-tree", tree, "-m", msg)
	if err != nil || commit == "" {
		return "", rejected, fmt.Errorf("commit imported tree: %w", err)
	}
	ref = "refs/imports/" + id + "/" + memoryBranch
	if _, err := memoryGitRun("update-ref", ref, commit); err != nil {
		return "", rejected, err
	}
	return ref, rejected, nil
}

// memoryExtractTar は tar.gz を dst へ展開する。allowlist に合致しない・traversal する・
// 通常ファイル以外のエントリは**書かずに rejected へ落とす**（cleanup_archive.go の guard と同型）。
func memoryExtractTar(src, dst string) ([]string, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "not a gzip archive: %v", err)
	}
	defer gr.Close()

	decls := memoryRootDecls()
	rejected := []string{}
	var total int64
	tr := tar.NewReader(gr)
	for i := 0; ; i++ {
		if i > memoryImportMaxEntries {
			return rejected, memoryErrf(http.StatusRequestEntityTooLarge, errCodeMemoryTooLarge, "archive has too many entries")
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return rejected, memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "corrupt archive: %v", err)
		}
		name := filepath.ToSlash(strings.TrimPrefix(h.Name, "./"))
		if h.Typeflag == tar.TypeDir {
			continue // 中身のあるファイルの親として都度作る
		}
		if h.Typeflag != tar.TypeReg {
			rejected = append(rejected, name) // symlink / hardlink / device は受け付けない
			continue
		}
		if name == "manifest.json" {
			continue // 自己記述。版管理対象ではないので取り込まない
		}
		if !memoryImportPathAllowed(decls, name) {
			rejected = append(rejected, name)
			continue
		}
		if h.Size > memoryImportMaxFile {
			rejected = append(rejected, name)
			continue
		}
		total += h.Size
		if total > memoryImportMaxBytes() {
			return rejected, memoryErrf(http.StatusRequestEntityTooLarge, errCodeMemoryTooLarge, "archive contents exceed the import size limit")
		}
		out := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return rejected, err
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return rejected, err
		}
		if _, err := io.CopyN(w, tr, h.Size); err != nil && err != io.EOF {
			_ = w.Close()
			return rejected, err
		}
		if err := w.Close(); err != nil {
			return rejected, err
		}
	}
	sort.Strings(rejected)
	return rejected, nil
}

// memoryImportPathAllowed は repo 内パスが宣言済みルートの allowlist に収まるか。
// 判定に使うのは memoryRootDecls（この環境で有効なルートではない）— codex を有効化して
// いない環境でも codex 分を**取り込む**ことはでき、live へ書く段で初めて弾かれる形にする。
func memoryImportPathAllowed(decls []memoryRoot, repoPath string) bool {
	if repoPath == "" || strings.HasPrefix(repoPath, "/") || strings.Contains(repoPath, "..") ||
		strings.HasPrefix(repoPath, ".git/") || strings.Contains(repoPath, "/.git/") {
		return false
	}
	for _, r := range decls {
		if !strings.HasPrefix(repoPath, r.RepoPrefix+"/") {
			continue
		}
		return memoryAllowed(r, strings.TrimPrefix(repoPath, r.RepoPrefix+"/"))
	}
	return false
}

// memoryRejectedPaths は rev のツリーのうち allowlist に収まらないパス（bundle 用）。
func memoryRejectedPaths(rev string) []string {
	out, err := memoryGitRun("ls-tree", "-r", "--name-only", rev)
	if err != nil {
		return []string{}
	}
	decls := memoryRootDecls()
	rejected := []string{}
	for _, p := range strings.Split(out, "\n") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if !memoryImportPathAllowed(decls, p) {
			rejected = append(rejected, p)
		}
	}
	return rejected
}

// memoryPruneImportRefs は古い取り込み系譜を落とす（★8 repo 肥大の抑制）。
// 適用済みの内容は main 側の import commit として残るので、ref を消しても失われない。
func memoryPruneImportRefs(keep int) {
	out, err := memoryGitRun("for-each-ref", "--format=%(refname)", "refs/imports/")
	if err != nil {
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	byID := map[string][]string{}
	for _, ref := range strings.Split(out, "\n") {
		ref = strings.TrimSpace(ref)
		rest, ok := strings.CutPrefix(ref, "refs/imports/")
		if !ok {
			continue
		}
		id, _, ok := strings.Cut(rest, "/")
		if !ok || id == "" {
			continue
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
		byID[id] = append(byID[id], ref)
	}
	if len(ids) <= keep {
		return
	}
	sort.Strings(ids) // id は UTC タイムスタンプなので辞書順 = 時刻順
	for _, id := range ids[:len(ids)-keep] {
		for _, ref := range byID[id] {
			_, _ = memoryGitRun("update-ref", "-d", ref)
		}
	}
}

// memoryImportApply は取り込んだ系譜から選んだ範囲だけを live へ適用する。
// 実体は restore と同じ経路（= pre-restore snapshot を取り、allowlist の内側だけ書き、
// 結果を commit する）で、契機だけ import になる — つまり取り込みも巻き戻せる。
//
// opts.Adopt=true は**移設**（docs/log/39 ⑤-移設）: 内容だけでなく履歴も引き継ぐ。bundle は
// 相手の全 snapshot を運んでいるのに、既定の適用では最新ツリーしか使わず、運んできた
// 過去は refs/imports に埋もれたまま（10 本を超えると刈られる）だった。移設は main を
// その系譜へ付け替えるので、相手の履歴がそのまま「この環境の履歴」になり、一覧・差分・
// 巻き戻しの既存機能が全部そのまま効く。
func memoryImportApply(importID string, sc memoryRestoreScope, now time.Time, opts memoryApplyOpts) (memoryRestoreResult, error) {
	var res memoryRestoreResult
	if !memoryImportIDRe.MatchString(importID) {
		return res, memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "invalid importId")
	}
	ref, err := memoryImportRef(importID)
	if err != nil {
		return res, err
	}
	sha, err := memoryGitRun("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || sha == "" {
		return res, memoryErrf(http.StatusNotFound, errCodeMemoryBadImport, "import %s is no longer available", importID)
	}
	trailers := []string{"AF-Import-Id: " + importID, "AF-Import-Ref: " + ref}
	if opts.Adopt {
		// 移設の範囲は**全体で固定**する。一部だけ置き換えると、履歴（相手の系譜）と
		// live（自分と相手の混在）が食い違い、以後の巻き戻しが何を意味するのか説明
		// できなくなる。範囲を選びたい場合は既定の適用（履歴は自分のまま）を使う。
		sc = memoryRestoreScope{All: true}
		trailers = append(trailers, "AF-Import-Mode: migrate")
	}
	return memoryApplyRev(sc, sha, memoryTriggerImport, trailers, now, opts)
}

// memoryImportRef は importId に対応する ref を引く（main 優先）。
func memoryImportRef(importID string) (string, error) {
	out, err := memoryGitRun("for-each-ref", "--format=%(refname)", "refs/imports/"+importID+"/")
	if err != nil {
		return "", err
	}
	list := []string{}
	for _, r := range strings.Split(out, "\n") {
		if r = strings.TrimSpace(r); r != "" {
			list = append(list, r)
		}
	}
	if len(list) == 0 {
		return "", memoryErrf(http.StatusNotFound, errCodeMemoryBadImport, "import %s is no longer available", importID)
	}
	want := "refs/imports/" + importID + "/" + memoryBranch
	for _, r := range list {
		if r == want {
			return r, nil
		}
	}
	sort.Strings(list)
	return list[0], nil
}
