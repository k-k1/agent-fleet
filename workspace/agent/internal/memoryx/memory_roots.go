package memoryx

// エージェントメモリの版管理（docs/log/39 / ADR 0022）— ルート宣言と live→staging コピー。
//
// 「エージェントメモリ」の版管理対象になるローカル実体は 2 つだけで（docs/log/39 の棚卸し）、
// ここではそれを宣言テーブルとして持つ。将来 opencode などが上流でメモリを実装したら
// memoryRoots() に 1 行足すだけで snapshot / rollback / export の全機能が付く。
//
// ★1（巻き込み事故）の要: 対象は allowlist の glob だけで決まる。deny 方式にしない。
// live ツリー（transcript 883MB・.credentials.json・settings.json・codex の派生状態
// sqlite / 自前 .git）を repo に入れる経路をコード上作らないのがこのファイルの責務で、
// memory_snapshot_test.go がそれを実データ配置で検証する。

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// memoryRoot は版管理する 1 つのメモリルートの宣言。
type memoryRoot struct {
	Kind string // "claude" | "codex" — セッション kind と一致させる（busy 判定が引く）
	// Label は Console 表示名。i18n はフロント側なので、ここは種別名そのもの。
	Label string
	// Dir は live の絶対パス（呼び出しの都度解決する — テストが HOME / CLAUDE_CONFIG_DIR
	// を差し替えるためキャッシュしない）。
	Dir string
	// RepoPrefix は bare repo 内での名前空間。kind ごとに分ける（docs/log/39 ①）。
	RepoPrefix string
	// Include は Dir からの相対パスに対する allowlist glob。`**` は 0 個以上のセグメントに
	// マッチする。これに合致しないファイルは決して読まない。
	Include []string
	// Exclude は Include 内の除外。codex の自前 .git（統合パイプラインの差分ベースライン）は
	// 絶対に触らない・repo にも入れない。
	Exclude []string
	// Scopes はディレクトリ粒度の部分ロールバックが可能か（claude=true, codex=false）。
	// codex はプロジェクト区分がファイル**内**のエントリなのでディレクトリ粒度が存在しない。
	Scopes bool
	// RequireDir が true のルートは live dir が存在するときだけ有効になる。codex の
	// memories 機能は既定 OFF なので、有効化されていない環境ではルート自体が現れない。
	RequireDir bool
}

// memoryRootDecls は宣言テーブルそのもの（存在検知の前）。テストが全件を見られるよう
// memoryRoots() と分けてある。
func memoryRootDecls() []memoryRoot {
	return []memoryRoot{
		{
			Kind:       "claude",
			Label:      "Claude Code",
			Dir:        filepath.Join(claude.ConfigDir(), "projects"),
			RepoPrefix: "claude/projects",
			// projects/<slug>/memory/** だけ。同じ階層に転がっている <sid>.jsonl
			// （transcript）と、親の .credentials.json / settings.json は構造的に外れる。
			Include: []string{"*/memory/**"},
			Scopes:  true,
		},
		{
			Kind:  "codex",
			Label: "Codex",
			// 有効化配線（codex.setMemories）と同じ定義を使う。ここがズレると
			// 「有効化したのにルートが現れない」という追いにくい壊れ方をする。
			Dir:        codex.MemoriesDir(),
			RepoPrefix: "codex",
			Include:    []string{"**"},
			// .git は codex の統合パイプラインが差分ベースラインに使う（上流 PR #18982）。
			// phase2_workspace_diff.md は統合の中間生成物。memories_1.sqlite は Dir の
			// 外（~/.codex/）なので構造的に対象外だが、将来の移動に備えて明示しておく。
			Exclude:    []string{".git/**", "phase2_workspace_diff.md", "*.sqlite", "*.sqlite-*"},
			Scopes:     false,
			RequireDir: true,
		},
	}
}

// memoryRoots は今この環境で有効なルートを返す（RequireDir のものは live dir 存在時のみ）。
func memoryRoots() []memoryRoot {
	var out []memoryRoot
	for _, r := range memoryRootDecls() {
		if r.RequireDir {
			if st, err := os.Stat(r.Dir); err != nil || !st.IsDir() {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// memoryInactiveRoot は「宣言はされているが今この環境では有効でない」ルートの説明
// （docs/log/39 P4）。RequireDir のルートを黙って落とすと、Console 側は「codex のメモリが
// 出てこない」理由を示せず、利用者は有効化の導線にも辿り着けない。
type memoryInactiveRoot struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Reason string `json:"reason"` // "codex_memories_disabled" | "codex_memories_pending" | "absent"
	// Toggleable は Console から有効化/無効化できるか。true のとき Enabled が現在値。
	Toggleable bool `json:"toggleable"`
	Enabled    bool `json:"enabled"`
}

// memoryInactiveRoots は memoryRoots() が落としたルートを理由付きで返す。
func memoryInactiveRoots() []memoryInactiveRoot {
	active := map[string]bool{}
	for _, r := range memoryRoots() {
		active[r.Kind] = true
	}
	out := []memoryInactiveRoot{}
	for _, r := range memoryRootDecls() {
		if active[r.Kind] {
			continue
		}
		v := memoryInactiveRoot{Kind: r.Kind, Label: r.Label, Reason: "absent"}
		if r.Kind == "codex" {
			v.Toggleable = true
			v.Enabled = codex.MemoriesEnabled()
			// 有効化しても ~/.codex/memories は次に codex が走るまで生えない。
			// 「設定が効いていない」と「まだ作られていない」は別物なので区別する。
			if v.Enabled {
				v.Reason = "codex_memories_pending"
			} else {
				v.Reason = "codex_memories_disabled"
			}
		}
		out = append(out, v)
	}
	return out
}

// memoryRootByKind は kind でルートを引く（REST の scope 解決用）。
func memoryRootByKind(kind string) (memoryRoot, bool) {
	for _, r := range memoryRoots() {
		if r.Kind == kind {
			return r, true
		}
	}
	return memoryRoot{}, false
}

// memoryAllowed は Dir からの相対パス rel（スラッシュ区切り）が版管理対象かを返す。
// allowlist が唯一の判定で、Exclude は Include 内の絞り込みにしか使わない。
func memoryAllowed(r memoryRoot, rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || rel == ".." {
		return false
	}
	matched := false
	for _, p := range r.Include {
		if memoryGlobMatch(p, rel) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, p := range r.Exclude {
		if memoryGlobMatch(p, rel) {
			return false
		}
	}
	return true
}

// memoryGlobMatch は `**`（0 個以上のセグメント）を解する glob マッチ。セグメント内の
// ワイルドカードは path.Match に委ねる。path/filepath の Match は `**` を扱えず、
// `*` がセパレータを跨がないため、この薄いラッパを持つ。
func memoryGlobMatch(pattern, name string) bool {
	return memoryGlobSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func memoryGlobSegs(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// `**` は 0 個以上のセグメントを飲む。末尾なら残り全部にマッチ。
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(seg); i++ {
				if memoryGlobSegs(pat[1:], seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

// memoryFile は収集した 1 ファイルの座標（一覧 API とコピーの両方が使う）。
type memoryFile struct {
	Rel   string // root.Dir からの相対パス（スラッシュ区切り）
	Abs   string
	Size  int64
	MTime int64 // Unix 秒
}

// memoryCollect は root 配下の allowlist 合致ファイルを列挙する。シンボリックリンクは
// 追わない — allowlist の外（transcript や資格情報）へ抜ける経路を塞ぐため、リンクは
// ファイルとしてもディレクトリとしても無視する（★1）。
func memoryCollect(r memoryRoot) []memoryFile {
	var out []memoryFile
	root := r.Dir
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 読めない枝は黙って飛ばす（live は他プロセスが書き換え続けている）。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// シンボリックリンクは種別を問わず不採用（リンク先が allowlist 外でも辿れてしまう）。
		if d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// 配下ごと除外されるディレクトリ（codex の .git）は降りる前に刈る。
			// "<dir>/**" 形式の Exclude を、その dir 自体にマッチさせて判定する。
			for _, ex := range r.Exclude {
				if base, ok := strings.CutSuffix(ex, "/**"); ok && memoryGlobMatch(base, rel) {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !memoryAllowed(r, rel) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		out = append(out, memoryFile{Rel: rel, Abs: p, Size: info.Size(), MTime: info.ModTime().Unix()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

// memorySyncToStaging は root の allowlist 合致ファイルを staging/<RepoPrefix>/ 以下へ
// 複製する。live 側を消したファイルが snapshot に残らないよう、先に prefix ごと消して
// から書き直す（対象は数百 KB なので実質無料・docs/log/39）。
func memorySyncToStaging(r memoryRoot, stagingRoot string) (int, error) {
	dst := filepath.Join(stagingRoot, filepath.FromSlash(r.RepoPrefix))
	if err := os.RemoveAll(dst); err != nil {
		return 0, fmt.Errorf("staging reset %s: %w", r.RepoPrefix, err)
	}
	files := memoryCollect(r)
	if len(files) == 0 {
		return 0, nil
	}
	for _, f := range files {
		out := filepath.Join(dst, filepath.FromSlash(f.Rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return 0, err
		}
		if err := memoryCopyFile(f.Abs, out); err != nil {
			// live は動いているので、消えた/読めなくなったファイルは飛ばす。
			continue
		}
	}
	return len(files), nil
}

func memoryCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// memoryScopeSlug は claude の repo 内パス（claude/projects/<slug>/memory/...）から
// プロジェクト slug を取り出す。scope を持たないルートのパスでは ok=false。
func memoryScopeSlug(repoPath string) (string, bool) {
	const prefix = "claude/projects/"
	if !strings.HasPrefix(repoPath, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(repoPath, prefix)
	slug, _, found := strings.Cut(rest, "/")
	if !found || slug == "" {
		return "", false
	}
	return slug, true
}

// memorySlugDisplay は claude の slug（絶対パスの "/" を "-" に潰したもの）を人が読める
// 名前へ整形する（★6）。~/repos 配下の実ディレクトリから逆引きできれば確実なので
// まずそれを試し、駄目なら "-repos-" 以降を採る。
func memorySlugDisplay(slug string) string {
	root := gitx.ReposRoot()
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			if strings.ReplaceAll(filepath.Join(root, e.Name()), "/", "-") == slug {
				return e.Name()
			}
		}
	}
	if i := strings.LastIndex(slug, "-repos-"); i >= 0 {
		if name := slug[i+len("-repos-"):]; name != "" {
			return name
		}
	}
	return strings.TrimPrefix(slug, "-")
}
