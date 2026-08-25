package main

// エージェントメモリの版管理（docs/39 ④ / ADR 0022 決定 4）— restore。
//
// 履歴は書き換えない。restore は「その時点の内容を live へ書き戻し、結果を新しい commit
// として積む」操作であり、適用前に **pre-restore snapshot を必ず取る**ので、巻き戻しの
// 巻き戻しが常に可能になる（★2）。
//
//	rev 解決 ──▶ ① pre-restore snapshot（現在の live を保全）
//	          ──▶ ② staging を rev の内容へ（scope 限定の checkout）
//	          ──▶ ③ staging → live（allowlist 内だけの上書き + 消滅分の削除）
//	          ──▶ ④ restore snapshot（AF-Trigger: restore・戻し元を trailer に記録）
//
// ③ が本ファイルの肝で、「repo に入れてはいけないものを読まない」（★1・memory_roots.go）
// の**逆向き**——「allowlist の外へ書かない・消さない」——を担保する。live 側の列挙は
// memoryCollect（= allowlist とシンボリックリンク不追従が効いた経路）しか使わず、書き込み
// 先も 1 セグメントずつ検査して途中にシンボリックリンクがあれば拒否する。

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// memoryUserErr は「呼び出し側の入力が悪い」種類の失敗。REST 層が status と安定コードへ
// そのまま写せるよう、エンジン側で分類まで済ませておく。
type memoryUserErr struct {
	Status int
	Code   string
	Msg    string
}

func (e *memoryUserErr) Error() string { return e.Msg }

func memoryErrf(status int, code, format string, args ...any) error {
	return &memoryUserErr{Status: status, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// memoryRestoreScope は復元範囲。docs/39 の `{all | projects: [slug...]}` に、
// codex のような Scopes=false のルートを丸ごと指すための kinds を足したもの。
type memoryRestoreScope struct {
	All      bool     `json:"all"`
	Kinds    []string `json:"kinds"`
	Projects []string `json:"projects"`
}

// memoryScopeTarget は解決済みの復元単位。Repo は bare repo 内の prefix、Rel は
// root.Dir からの相対（"" = ルート全体）。
type memoryScopeTarget struct {
	Root memoryRoot
	Repo string
	Rel  string
}

// memoryApplyOpts は適用の変種。既定（ゼロ値）は docs/39 ④ そのままの「内容だけ採る」で、
// Adopt はそこに**系譜の付け替え**を足す（= 移設。取り込んだ履歴をこの環境の履歴にする）。
// 取り込んだ内容を live へ書く手順は 1 バイトも変えない — 変わるのは main がどの系譜を
// 指すかだけなので、allowlist 由来の防御（★1 の裏返し）はそのまま効く。
type memoryApplyOpts struct {
	// Adopt=true: 適用後に main を from の系譜へ付け替える。元の main は退避 ref
	// （refs/premigrate/<ts>）へ逃がすので、履歴が消えることはない。
	Adopt bool
}

// memoryRestoreResult は restore 1 回の結果。pre-restore と restore の 2 つの rev を
// 返すので、UI は「戻した」だけでなく「戻す前の状態はここ」も提示できる。
type memoryRestoreResult struct {
	From       string             `json:"from"`                 // 復元元 snapshot（解決後の sha）
	PreRestore string             `json:"preRestore,omitempty"` // 直前に積んだ保全 snapshot
	Rev        string             `json:"rev,omitempty"`        // restore commit
	Committed  bool               `json:"committed"`            // false = 既に同じ内容だった
	Scopes     []string           `json:"scopes"`               // 適用した repo 内 prefix
	Written    []string           `json:"written"`              // live へ書いたパス（repo 内表記）
	Deleted    []string           `json:"deleted"`              // live から消したパス（同上）
	Projects   []memoryProjectRef `json:"projects"`             // restore commit が触ったプロジェクト
	Busy       bool               `json:"busy"`                 // 対象 kind に実行中セッションがあった
	// 以下は移設（Adopt）のときだけ。Replaced は退避した元 main の sha、ReplacedRef は
	// その退避先。UI が「移設前の履歴はここにある」と示せないと、入れ替えが取り返しの
	// つかない操作に見えてしまう。
	Adopted     bool   `json:"adopted,omitempty"`
	Replaced    string `json:"replaced,omitempty"`
	ReplacedRef string `json:"replacedRef,omitempty"`
}

// memoryRestore は docs/39 ④ の手順をそのまま実行する。now はテストが決定的に検証
// できるよう呼び出し側から渡す（snapshot と同じ流儀）。
func memoryRestore(sc memoryRestoreScope, rev, at string, now time.Time) (memoryRestoreResult, error) {
	// snapshot と staging / index を共有するので、自動 snapshot ループとは相互排他。
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	res := memoryRestoreResult{Scopes: []string{}, Written: []string{}, Deleted: []string{}, Projects: []memoryProjectRef{}}
	if err := memoryEnsureRepo(); err != nil {
		return res, err
	}
	if !memoryHasCommits() {
		return res, memoryErrf(http.StatusNotFound, errCodeMemoryNoSnapshots, "no snapshots yet")
	}
	from, err := memoryResolveRev(rev, at)
	if err != nil {
		return res, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRev, "%s", err.Error())
	}
	return memoryApplyRevLocked(sc, from, memoryTriggerRestore, nil, now, memoryApplyOpts{})
}

// memoryApplyRev は「解決済みの commit の内容を scope 単位で live へ書き戻す」共通経路。
// restore（履歴上の時点へ戻す）と import の apply（取り込んだ系譜の内容を採る）は、
// 出どころの commit が違うだけで手順は同一なので、契機と trailer だけを引数で受ける。
func memoryApplyRev(sc memoryRestoreScope, from, trigger string, extraTrailers []string, now time.Time, opts memoryApplyOpts) (memoryRestoreResult, error) {
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()
	if err := memoryEnsureRepo(); err != nil {
		return memoryRestoreResult{}, err
	}
	return memoryApplyRevLocked(sc, from, trigger, extraTrailers, now, opts)
}

// memoryApplyRevLocked は memorySnapshotMu を握った状態の本体（docs/39 ④ の手順そのもの）。
func memoryApplyRevLocked(sc memoryRestoreScope, from, trigger string, extraTrailers []string, now time.Time, opts memoryApplyOpts) (memoryRestoreResult, error) {
	res := memoryRestoreResult{From: from, Scopes: []string{}, Written: []string{}, Deleted: []string{}, Projects: []memoryProjectRef{}}
	targets, err := memoryResolveScope(sc)
	if err != nil {
		return res, err
	}
	busy := memoryBusyKinds()
	for _, t := range targets {
		res.Scopes = append(res.Scopes, t.Repo)
		if busy[t.Root.Kind] {
			// 実行中セッションがあっても止めない（docs/39 ④-5: 既定は続行可）。後から
			// 書かれた分は restore 後の新しい snapshot として履歴に現れるだけで追跡できる。
			res.Busy = true
		}
	}

	// ① 現在の live を必ず保全する。ここで失敗したら何も壊さずに引き返す。
	pre, err := memorySnapshotLocked(memoryTriggerPreRestore, now, "AF-Restore-Rev: "+from)
	if err != nil {
		return res, fmt.Errorf("pre-restore snapshot: %w", err)
	}
	// 無変更で積まなかった場合は直近 snapshot が既に「戻す前の状態」なので、そちらを返す。
	// UI がいつでも「巻き戻しの巻き戻し」先を示せる（★2）。
	res.PreRestore = pre.Rev
	if !pre.Committed {
		if head, herr := memoryGitRun("rev-parse", memoryBranch); herr == nil {
			res.PreRestore = head
		}
	}

	// ② staging を rev の内容へ（scope 単位）。rev にその prefix が無ければ「当時は空」
	//    なので、消したままにするのが正しい復元になる。
	staging := memoryStagingDir()
	for _, t := range targets {
		dir := filepath.Join(staging, filepath.FromSlash(t.Repo))
		if err := os.RemoveAll(dir); err != nil {
			return res, fmt.Errorf("reset staging %s: %w", t.Repo, err)
		}
		listed, lerr := memoryGitRun("ls-tree", "-r", "--name-only", from, "--", t.Repo)
		if lerr != nil {
			return res, fmt.Errorf("list %s at %s: %w", t.Repo, from[:8], lerr)
		}
		if strings.TrimSpace(listed) == "" {
			continue
		}
		if _, err := memoryGitRun("checkout", from, "--", t.Repo); err != nil {
			return res, fmt.Errorf("checkout %s at %s: %w", t.Repo, from[:8], err)
		}
	}

	// ③ staging → live。allowlist の内側だけを書き、scope 内で消えたものだけを消す。
	for _, t := range targets {
		written, deleted, err := memoryApplyScopeToLive(t, staging)
		res.Written = append(res.Written, written...)
		res.Deleted = append(res.Deleted, deleted...)
		if err != nil {
			return res, fmt.Errorf("apply %s: %w", t.Repo, err)
		}
	}
	sort.Strings(res.Written)
	sort.Strings(res.Deleted)

	// ③.5 移設（Adopt）: ここで初めて main を from の系譜へ付け替える。live を書き終えた
	//     後に置くのは、①〜③ のどこで失敗しても履歴が動かないようにするため（失敗した
	//     移設が「履歴だけ入れ替わって中身は古い」状態を作らない）。元の main は退避 ref
	//     へ逃がすので、入れ替え前の履歴は repo に残り続ける（gc の対象にもならない）。
	if opts.Adopt {
		prev, _ := memoryGitRun("rev-parse", "--verify", "--quiet", memoryBranch)
		if prev != "" {
			ref := "refs/premigrate/" + now.UTC().Format("20060102T150405Z")
			if _, err := memoryGitRun("update-ref", ref, prev); err != nil {
				return res, fmt.Errorf("stash the replaced lineage: %w", err)
			}
			res.Replaced, res.ReplacedRef = prev, ref
		}
		if _, err := memoryGitRun("update-ref", "refs/heads/"+memoryBranch, from); err != nil {
			return res, fmt.Errorf("adopt the imported lineage: %w", err)
		}
		res.Adopted = true
	}

	// ④ 適用後の live を restore commit として積む。ここは live を読み直すので、
	//    「実際に何が起きたか」が履歴の側で確定する（③ の結果を信用しない）。
	trailers := []string{"AF-Restore-Rev: " + from}
	for _, t := range targets {
		trailers = append(trailers, "AF-Restore-Scope: "+t.Repo)
	}
	if res.Replaced != "" {
		trailers = append(trailers, "AF-Premigrate-Rev: "+res.Replaced)
	}
	trailers = append(trailers, extraTrailers...)
	done, err := memorySnapshotLocked(trigger, now, trailers...)
	if err != nil {
		return res, fmt.Errorf("restore snapshot: %w", err)
	}
	res.Committed, res.Rev, res.Projects = done.Committed, done.Rev, done.Projects

	// 移設は「内容が同じでも起きた事実」を残す。付け替え後の live は取り込んだ head と
	// 一致するのが普通なので、★8 の無変更 skip をそのまま通すと**系譜を入れ替えた記録が
	// どこにも残らない**（退避 ref だけになる）。ここだけ空 commit を許し、どの系譜を
	// いつ採用し何と入れ替えたかを trailer で履歴に刻む。
	if opts.Adopt && !done.Committed {
		msg := memoryCommitMessage(trigger, now, nil, nil, trailers)
		if _, err := memoryGitRun("commit", "--quiet", "--no-verify", "--allow-empty", "-m", msg); err != nil {
			return res, fmt.Errorf("record the migration: %w", err)
		}
		rev, err := memoryGitRun("rev-parse", memoryBranch)
		if err != nil {
			return res, fmt.Errorf("record the migration: %w", err)
		}
		res.Committed, res.Rev = true, rev
	}
	return res, nil
}

// memoryResolveScope は要求スコープを repo 内 prefix の集合へ落とす。宣言テーブルに
// 無い kind・この環境に無いルート・不正な slug はここで弾く。
func memoryResolveScope(sc memoryRestoreScope) ([]memoryScopeTarget, error) {
	roots := memoryRoots()
	if len(roots) == 0 {
		return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "no memory roots are active")
	}
	byKind := map[string]memoryRoot{}
	for _, r := range roots {
		byKind[r.Kind] = r
	}
	var out []memoryScopeTarget
	whole := map[string]bool{} // ルート全体を復元する kind
	add := func(t memoryScopeTarget) {
		for _, e := range out {
			if e.Repo == t.Repo {
				return
			}
		}
		out = append(out, t)
	}

	if sc.All {
		for _, r := range roots {
			whole[r.Kind] = true
			add(memoryScopeTarget{Root: r, Repo: r.RepoPrefix})
		}
	}
	for _, k := range sc.Kinds {
		r, ok := byKind[k]
		if !ok {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "unknown memory kind %q", k)
		}
		whole[r.Kind] = true
		add(memoryScopeTarget{Root: r, Repo: r.RepoPrefix})
	}
	for _, slug := range sc.Projects {
		// プロジェクト粒度を持つのは claude だけ（codex は区分がファイル内エントリ）。
		r, ok := byKind["claude"]
		if !ok || !r.Scopes {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "project scope is not available")
		}
		if !memorySlugSafe(slug) {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "invalid project %q", slug)
		}
		if whole[r.Kind] {
			continue // ルート全体に含まれるので個別指定は無視する
		}
		add(memoryScopeTarget{Root: r, Repo: r.RepoPrefix + "/" + slug, Rel: slug})
	}
	if len(out) == 0 {
		return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "scope must select at least one root or project")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out, nil
}

// memorySlugSafe は claude のプロジェクト slug が「単一のパスセグメント」であることを見る。
// slug は絶対パスの "/" を "-" に潰したものなので先頭が "-" になるのが普通 — git へは
// `--` の後ろのパスとしてしか渡さないため、ここではパス脱出だけを塞ぐ。
func memorySlugSafe(s string) bool {
	if s == "" || s == "." || len(s) > 255 {
		return false
	}
	if strings.Contains(s, "..") || strings.ContainsAny(s, "/\\\x00") {
		return false
	}
	return true
}

// memoryRelInScope は root.Dir 相対のパス rel が scope（同じく root.Dir 相対・"" は全体）
// の内側かを返す。
func memoryRelInScope(scope, rel string) bool {
	if scope == "" {
		return true
	}
	return rel == scope || strings.HasPrefix(rel, scope+"/")
}

// memoryApplyScopeToLive は staging の scope 配下を live へ反映する（rsync --delete 相当）。
//
// 「望ましい状態」は staging 側の列挙、「今の状態」は memoryCollect（allowlist 経由）で
// 取る。削除候補が allowlist を通ったファイルに限られるのはこの非対称のおかげで、
// メモリ以外（transcript・資格情報）を消す経路が構造的に存在しない。
func memoryApplyScopeToLive(t memoryScopeTarget, stagingRoot string) (written, deleted []string, err error) {
	written, deleted = []string{}, []string{}
	src := filepath.Join(stagingRoot, filepath.FromSlash(t.Repo))

	// 望ましい状態: staging 側の実ファイル（repo 由来だが、live へ書く前にもう一度
	// allowlist を通す — repo に何かが紛れていても live を汚さないため）。
	desired := map[string]string{}
	werr := filepath.WalkDir(src, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			if os.IsNotExist(e) && p == src {
				return nil // rev に存在しなかった scope = 「全部消す」
			}
			return e
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		sub, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		rel := path.Join(t.Rel, filepath.ToSlash(sub))
		if !memoryAllowed(t.Root, rel) {
			return nil
		}
		desired[rel] = p
		return nil
	})
	if werr != nil {
		return written, deleted, werr
	}

	// 今の live のうち scope 内かつ復元後に存在しないもの = 削除。
	for _, f := range memoryCollect(t.Root) {
		if !memoryRelInScope(t.Rel, f.Rel) {
			continue
		}
		if _, keep := desired[f.Rel]; keep {
			continue
		}
		if rerr := os.Remove(f.Abs); rerr != nil && !os.IsNotExist(rerr) {
			return written, deleted, rerr
		}
		memoryPruneEmptyDirs(t.Root.Dir, f.Abs)
		deleted = append(deleted, t.Root.RepoPrefix+"/"+f.Rel)
	}

	// 内容が変わるものだけ書く（mtime を無用に動かすと自動 snapshot の契機判定が濁る）。
	rels := make([]string, 0, len(desired))
	for rel := range desired {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		dst, perr := memoryPrepareDest(t.Root.Dir, rel)
		if perr != nil {
			return written, deleted, perr
		}
		same, serr := memorySameContent(desired[rel], dst)
		if serr != nil {
			return written, deleted, serr
		}
		if same {
			continue
		}
		if cerr := memoryCopyFile(desired[rel], dst); cerr != nil {
			return written, deleted, cerr
		}
		written = append(written, t.Root.RepoPrefix+"/"+rel)
	}
	return written, deleted, nil
}

// memoryPrepareDest は書き込み先の絶対パスを返し、途中のディレクトリを作る。
// 経路上にシンボリックリンクがあれば拒否する — allowlist の内側に外向きのリンクを
// 置かれた状態で restore すると、live の外（資格情報等）を上書きしかねないため（★1 の裏返し）。
func memoryPrepareDest(rootDir, rel string) (string, error) {
	// ルート自体がまだ無い環境がある: claude を一度も起動していないワークスペース（起動
	// 直後に別環境のメモリを import するのが正にその状況）には <config>/projects が無い。
	// 以降の段は 1 段ずつ Mkdir して経路の symlink を検査するので、MkdirAll はここだけ。
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return "", err
	}
	segs := strings.Split(rel, "/")
	cur := rootDir
	for _, s := range segs[:len(segs)-1] {
		cur = filepath.Join(cur, s)
		st, err := os.Lstat(cur)
		switch {
		case err == nil && st.Mode()&os.ModeSymlink != 0:
			return "", fmt.Errorf("refusing to write through symlink %s", cur)
		case err == nil && !st.IsDir():
			return "", fmt.Errorf("%s is not a directory", cur)
		case err == nil:
		case os.IsNotExist(err):
			if mkerr := os.Mkdir(cur, 0o700); mkerr != nil {
				return "", mkerr
			}
		default:
			return "", err
		}
	}
	dst := filepath.Join(cur, segs[len(segs)-1])
	if st, err := os.Lstat(dst); err == nil && st.Mode()&os.ModeSymlink != 0 {
		// リンク越しに書かず、リンクそのものを実ファイルで置き換える。
		if rerr := os.Remove(dst); rerr != nil {
			return "", rerr
		}
	}
	return dst, nil
}

// memorySameContent は復元先が既に同じ内容かを見る（無ければ false）。
func memorySameContent(src, dst string) (bool, error) {
	a, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	b, err := os.Lstat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !b.Mode().IsRegular() || a.Size() != b.Size() {
		return false, nil
	}
	ab, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	bb, err := os.ReadFile(dst)
	if err != nil {
		return false, nil
	}
	return string(ab) == string(bb), nil
}

// memoryPruneEmptyDirs は削除で空になったディレクトリを rootDir の手前まで畳む。
// os.Remove はディレクトリが空のときしか成功しないので、他のもの（transcript 等）が
// 残っている枝には触れない。
func memoryPruneEmptyDirs(rootDir, removed string) {
	dir := filepath.Dir(removed)
	for strings.HasPrefix(dir, rootDir+string(os.PathSeparator)) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// memoryTreeEntryKind は tree API が返す 1 kind 分の概況。
type memoryTreeKind struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Scopes bool   `json:"scopes"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
}

// memoryTreeProject は tree API が返す 1 プロジェクト分。
type memoryTreeProject struct {
	Slug    string `json:"slug"`
	Display string `json:"display"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

// memoryTreeAt は「その時点に何が入っていたか」を返す。restore の選択 UI はこれを見る:
// 今は消えているプロジェクトも当時の snapshot には居るので、現在の roots を選択肢に
// 使うと「誤って消したメモリを戻す」という本命のユースケースが成立しない。
func memoryTreeAt(rev, at string) (string, []memoryTreeKind, []memoryTreeProject, error) {
	if !memoryHasCommits() {
		return "", nil, nil, memoryErrf(http.StatusNotFound, errCodeMemoryNoSnapshots, "no snapshots yet")
	}
	sha, err := memoryResolveRev(rev, at)
	if err != nil {
		return "", nil, nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRev, "%s", err.Error())
	}
	kinds, projects, err := memoryTreeOfRev(sha)
	return sha, kinds, projects, err
}

// memoryTreeOfRev は解決済み commit のツリーを kind 別 / プロジェクト別に畳む。
// import の preview も同じ形（「取り込んだ系譜には何が入っているか」）を要るのでここに置く。
func memoryTreeOfRev(sha string) ([]memoryTreeKind, []memoryTreeProject, error) {
	// --long は "<mode> blob <sha> <size>\t<path>" を返す（size で当時の容量が出せる）。
	out, err := memoryGitRun("ls-tree", "-r", "--long", sha)
	if err != nil {
		return nil, nil, err
	}
	decls := map[string]memoryRoot{}
	for _, r := range memoryRootDecls() {
		decls[r.Kind] = r
	}
	kinds := []memoryTreeKind{}
	projects := []memoryTreeProject{}
	kindIdx, projIdx := map[string]int{}, map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		meta, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var size int64
		if fields := strings.Fields(meta); len(fields) >= 4 {
			for _, r := range fields[3] {
				if r < '0' || r > '9' {
					size = 0
					break
				}
				size = size*10 + int64(r-'0')
			}
		}
		kind, _, _ := strings.Cut(p, "/")
		i, seen := kindIdx[kind]
		if !seen {
			d := decls[kind]
			label := d.Label
			if label == "" {
				label = kind
			}
			kinds = append(kinds, memoryTreeKind{Kind: kind, Label: label, Scopes: d.Scopes})
			i = len(kinds) - 1
			kindIdx[kind] = i
		}
		kinds[i].Files++
		kinds[i].Bytes += size
		if slug, ok := memoryScopeSlug(p); ok {
			j, seen := projIdx[slug]
			if !seen {
				projects = append(projects, memoryTreeProject{Slug: slug, Display: memorySlugDisplay(slug)})
				j = len(projects) - 1
				projIdx[slug] = j
			}
			projects[j].Files++
			projects[j].Bytes += size
		}
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Display < projects[j].Display })
	return kinds, projects, nil
}
