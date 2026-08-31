package main

// パス参照の解決（POST /fs/resolve）。
//
// エージェントの返信は成果物の場所をインラインコードで書く（`docs/log/65.md`、`_act-parts/`、
// `/home/dev/repos/x/README.md`）。Console のミラーはそれを「実在するときだけ」リンクに
// するので、描画のたびに「この文字列はどのファイルを指していて、それは在るのか」を
// 訊きにくる。その 1 問に答えるのがこのハンドラ。
//
// なぜ Console 側の fs/tree 総当たりではなくサーバー側で解決するのか:
//
//   - 基準が 1 つではない。相対パスは原則そのターンの cwd 基準だが、エージェントは
//     リポジトリルート基準でも書く（cwd がサブフォルダのとき特に）。「cwd → リポジトリ
//     ルート」の順に当てるフォールバックは、当たり外れを 1 往復で確定できるここでやる方が
//     素直で、Console が候補ごとにディレクトリ一覧を引くより遥かに安い。
//   - リポジトリルートを本当に知っているのはこちら側。Console は cwd の "repos/<名前>"
//     を正規表現で切り出すしかないが、ここは .git を上へ辿って WT でもサブフォルダ起動でも
//     正しい根を出せる（.git は WT ではファイル、通常の clone ではディレクトリ）。
//   - home の外の読める場所（scratch / ロール別 docs マウント）は safeBrowsePath が
//     すでに知っている。Console にはその知識が無く、絶対パスをリポジトリ相対と読み違える。
//   - 返すのは当たりだけ。ディレクトリ一覧（node_modules なら数千件）を存在確認のために
//     ブラウザへ運ばない。
//
// 契約: refs は「パスそのもの」。`:12` のような行番号や末尾スラッシュは Console 側が
// 落としてから送る（行番号はペインを開くのに Console 自身が要る情報でもある）。存在した
// ものだけが resolved に載り、載らなかった ref は「無い」と読む。path は Console の fs API
// が使う形（home 配下は browse-root 相対、scratch / docs マウントは絶対）。

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const (
	fsResolveMaxBody = 64 * 1024
	// 1 文書（1 ターンの本文）が挙げてくるパスの上限。これを超える分は黙って捨てる:
	// リンクが付かないだけで、本文はそのまま読める。
	fsResolveMaxRefs = 64
	fsResolveMaxRef  = 512
	// cwd から .git を探して上る段数の上限。browse root で止まるので保険。
	fsResolveMaxWalk = 24
)

type fsResolveRequest struct {
	Cwd  string   `json:"cwd"`  // そのターンの作業ディレクトリ（絶対でも browse 相対でも可）
	Refs []string `json:"refs"` // 本文に書かれていたパス文字列
}

type fsResolveEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "file" | "dir"
}

func handleFSResolve(w http.ResponseWriter, r *http.Request) {
	var req fsResolveRequest
	if serr := httpx.DecodeStrictJSON(r, &req, fsResolveMaxBody); serr != nil {
		httpx.WriteErr(w, serr.Status, serr.Code, serr.Message)
		return
	}
	refs := req.Refs
	if len(refs) > fsResolveMaxRefs {
		refs = refs[:fsResolveMaxRefs]
	}
	cwd, repo := fsResolveBases(req.Cwd)
	out := map[string]fsResolveEntry{}
	for _, ref := range refs {
		if e, ok := fsResolveRef(ref, cwd, repo); ok {
			out[ref] = e
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"resolved": out})
}

// fsResolveBases turns the request's cwd into the two absolute bases a relative reference
// is tried against: the working directory itself, and the repository root it sits in.
// An unusable cwd yields empty bases — absolute references still resolve.
func fsResolveBases(cwd string) (cwdFull, repoFull string) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", ""
	}
	full, _, ok := safeBrowsePath(cwd)
	if !ok {
		return "", ""
	}
	if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
		return "", ""
	}
	return full, fsRepoRoot(full)
}

// fsRepoRoot walks up from dir to the working copy's root — the directory holding .git,
// which is a DIRECTORY in a normal clone and a FILE in a worktree. Bounded by the browse
// root, so it can never answer with somebody's home or /. "" when dir is not in a repo.
func fsRepoRoot(dir string) string {
	root := browseRoot()
	rroot, err := filepath.EvalSymlinks(root)
	if err != nil {
		rroot = root
	}
	cur := dir
	for i := 0; i < fsResolveMaxWalk; i++ {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		if cur == root || cur == rroot {
			return ""
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
	return ""
}

// fsResolveRef answers what one written reference points at, trying the plausible readings
// in order and taking the first that exists:
//
//	~/x        → home. One reading only; the user wrote where they meant.
//	/x         → the real absolute path first (that is what an agent citing a file writes),
//	             then repository-root-relative (`/docs/a.md` — how repository Markdown links
//	             its own tree).
//	x, a/b     → the turn's cwd first (the overwhelmingly common case), then the repository
//	             root — which is what a reply written from a subfolder, or one quoting a
//	             path out of a repo-root-relative document, actually means.
//
// Every candidate goes through safeBrowsePath, so the denylist, the browse root and the
// read-only roots decide what may be answered about at all.
func fsResolveRef(ref, cwdFull, repoFull string) (fsResolveEntry, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > fsResolveMaxRef || strings.ContainsRune(ref, 0) {
		return fsResolveEntry{}, false
	}
	var cands []string
	switch {
	case strings.HasPrefix(ref, "~/"):
		cands = append(cands, filepath.Join(homeDir(), ref[2:]))
	case filepath.IsAbs(ref):
		cands = append(cands, filepath.Clean(ref))
		if repoFull != "" {
			cands = append(cands, filepath.Join(repoFull, ref))
		}
	default:
		if cwdFull != "" {
			cands = append(cands, filepath.Join(cwdFull, ref))
		}
		if repoFull != "" && repoFull != cwdFull {
			cands = append(cands, filepath.Join(repoFull, ref))
		}
	}
	for _, cand := range cands {
		full, rel, ok := safeBrowsePath(cand)
		if !ok || !fsQueryResolvedOK(cand, full) {
			continue
		}
		fi, err := os.Stat(full)
		if err != nil {
			continue
		}
		typ := "file"
		if fi.IsDir() {
			typ = "dir"
		}
		return fsResolveEntry{Path: rel, Type: typ}, true
	}
	return fsResolveEntry{}, false
}
