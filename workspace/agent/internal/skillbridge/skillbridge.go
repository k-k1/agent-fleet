// Package skillbridge は作業コピー内のスキルを claude ⇄ codex の両規約ディレクトリ
// （.claude/skills ⇄ .codex/skills）へ**マーカー付きコピー**として双方向同期する
// （docs/50 §8）。SKILL.md の形式は両 CLI で互換（0.145 実測）なので、置き場所さえ
// 揃えればどちらのエージェントからも呼べる — その置き場所合わせをユーザーにやらせず、
// セッション起動時に agent が橋渡しする。
//
// シンボリックリンクは使わない（利用者要件）。コピーの管理規約:
//   - コピーには marker ファイル（.af-skill-bridge — 中身は元スキルの repo 相対パス）
//     を同梱し、これが「agent-fleet が作った・消してよい」印。実体（マーカー無し）が
//     既に居る名前には決して触らない — ネイティブのスキルが常に勝つ。
//   - マーカー付きコピーは同期のたび作り直す（安い・確実）。元が消えたら剪定する。
//   - マーカー付きをソースとしては扱わない（ブリッジのブリッジを作らない — ループ防止）。
//   - 作ったコピーは git の**リポジトリローカル** exclude（$GIT_DIR/info/exclude —
//     コミットされない・worktree 共通）へ番兵コメント付きブロックで登録し、status を
//     汚さない。ブロックは同期のたび現状で書き直す。ユーザーの実スキル（マーカー無し）
//     は登録しない — 未コミットの新規スキルが status から消えたら困る。
//   - 全て best-effort: ブリッジの失敗でセッション起動を止めない。
//
// 呼び出しシームは claude / codex の起動直前（BuildLaunch・managed の thread 再確立）。
// 冪等・軽量なので再接続のたびに走って構わない。
package skillbridge

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Marker is the file that stamps a bridged copy (content: source repo-relative path).
const Marker = ".af-skill-bridge"

const maxFileBytes = 10 << 20 // 常識外れの巨大アセットはコピーしない（1 ファイル上限）

var conventions = []string{
	filepath.Join(".claude", "skills"),
	filepath.Join(".codex", "skills"),
}

// Sync bridges skills both ways inside dir. Returns (copied, pruned) where copied
// counts NEWLY bridged skills (refreshes of existing copies are silent) — so a
// steady-state re-run reports (0, 0).
func Sync(dir string) (copied, pruned int) {
	if dir == "" {
		return 0, 0
	}
	c1, p1 := syncOneWay(dir, conventions[0], conventions[1])
	c2, p2 := syncOneWay(dir, conventions[1], conventions[0])
	updateExclude(dir)
	return c1 + c2, p1 + p2
}

// syncOneWay mirrors real skills from srcRel into dstRel (both repo-relative).
func syncOneWay(dir, srcRel, dstRel string) (copied, pruned int) {
	src := filepath.Join(dir, srcRel)
	dst := filepath.Join(dir, dstRel)

	// 剪定＋作り直し: dst 側にある「この方向のマーカー付きコピー」を処理する。
	if ents, err := os.ReadDir(dst); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			origin := readMarker(filepath.Join(dst, e.Name()))
			if origin == "" || !strings.HasPrefix(filepath.ToSlash(origin), filepath.ToSlash(srcRel)+"/") {
				continue // マーカー無し（実体）や他方向のコピーには触らない
			}
			srcSkill := filepath.Join(dir, filepath.FromSlash(origin))
			if isRealSkill(srcSkill) {
				// 元が生きている → 内容追随のため作り直す（新規には数えない）。
				_ = os.RemoveAll(filepath.Join(dst, e.Name()))
				_ = copySkill(srcSkill, filepath.Join(dst, e.Name()), origin)
			} else {
				if os.RemoveAll(filepath.Join(dst, e.Name())) == nil {
					pruned++
				}
			}
		}
	}

	ents, err := os.ReadDir(src)
	if err != nil {
		return copied, pruned
	}
	for _, e := range ents {
		if !e.IsDir() || !isRealSkill(filepath.Join(src, e.Name())) {
			continue // SKILL.md を持たない dir と、マーカー付き（＝ブリッジ産）はソースにしない
		}
		to := filepath.Join(dst, e.Name())
		if _, err := os.Lstat(to); err == nil {
			continue // 実体でも既存コピー（↑で作り直し済み）でも、居る名前には触らない
		}
		origin := filepath.ToSlash(filepath.Join(srcRel, e.Name()))
		if copySkill(filepath.Join(src, e.Name()), to, origin) == nil {
			copied++
		}
	}
	return copied, pruned
}

// isRealSkill: SKILL.md を持ち、かつマーカーを持たない（＝ユーザー/CLI の実スキル）。
func isRealSkill(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, Marker))
	return err != nil
}

func readMarker(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, Marker))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
}

// copySkill copies a skill dir (regular files only — no symlinks in, none out) and
// stamps the marker. On any error the partial copy is removed.
func copySkill(from, to, origin string) error {
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // symlink 等はコピーしない
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFileBytes {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
	if err == nil {
		err = os.WriteFile(filepath.Join(to, Marker), []byte(origin+"\n"), 0o644)
	}
	if err != nil {
		_ = os.RemoveAll(to)
	}
	return err
}

const excludeBegin = "# >>> agent-fleet skill-bridge（docs/50 §8・自動管理 — 手で編集しない）"
const excludeEnd = "# <<< agent-fleet skill-bridge"

// updateExclude rewrites our sentinel block in $GIT_DIR/info/exclude to list exactly
// the bridged copies that exist right now（無ければブロックごと消す）。git repo で
// なければ何もしない（SVN 作業コピーでは unversioned に見えるが許容 — docs/50 §8）。
func updateExclude(dir string) {
	lines := []string{}
	for _, conv := range conventions {
		ents, err := os.ReadDir(filepath.Join(dir, conv))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() && readMarker(filepath.Join(dir, conv, e.Name())) != "" {
				lines = append(lines, "/"+filepath.ToSlash(conv)+"/"+e.Name()+"/")
			}
		}
	}
	sort.Strings(lines)

	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude").Output()
	if err != nil {
		return
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return
	}
	prev, _ := os.ReadFile(path)
	kept := []string{}
	inBlock := false
	for _, ln := range strings.Split(string(prev), "\n") {
		switch {
		case strings.TrimSpace(ln) == excludeBegin:
			inBlock = true
		case strings.TrimSpace(ln) == excludeEnd:
			inBlock = false
		case !inBlock:
			kept = append(kept, ln)
		}
	}
	// 末尾の空行を整えてからブロックを付け直す。
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(lines) > 0 {
		kept = append(kept, excludeBegin)
		kept = append(kept, lines...)
		kept = append(kept, excludeEnd)
	}
	body := strings.Join(kept, "\n")
	if body != "" {
		body += "\n"
	}
	if body == string(prev) {
		return // 無変更なら書かない（他人の設定ファイルを書く作法）
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = os.WriteFile(path, []byte(body), 0o644)
}
