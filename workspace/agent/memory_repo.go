package main

// エージェントメモリの版管理（docs/log/39 / ADR 0022）— bare repo とその git 実行環境。
//
// repo は claude 専用マウント内の bare（/var/lib/af/claude/af-memory.git）。このマウントは
// recreate / clean-home を生き残る最も強い保証を持つため、codex 分の履歴も同居させる。
// live ツリーには .git を置かない（エージェント自身に repo を見せない・claude のメモリ
// 列挙に .git を混ぜない）。staging も同じマウントに置く — EFS 越しのクロスデバイス
// コピーを避け、bare repo の index と staging の内容が食い違わないようにするため。
//
// ★5: ユーザーの ~/.gitconfig（署名設定等）を一切継がない。GIT_CONFIG_GLOBAL /
// GIT_CONFIG_SYSTEM を /dev/null に落とし、identity は専用のものを env で固定する。

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
)

// memoryBranch は snapshot を積む唯一のブランチ。import は refs/imports/<ts>（P3）へ
// 独立系譜として入るので、ここを見れば「この環境の履歴」だけが並ぶ。
const memoryBranch = "main"

func memoryRepoDir() string    { return filepath.Join(claude.ConfigDir(), "af-memory.git") }
func memoryStagingDir() string { return filepath.Join(claude.ConfigDir(), "af-memory.staging") }

// memoryGit は repo 専用の git コマンドを組む。work-tree は staging 固定で、cwd も
// そこに置く — pathspec を渡さない `git add -A` が staging 全体だけを見るようにするため。
func memoryGit(args ...string) *exec.Cmd {
	_ = os.MkdirAll(memoryStagingDir(), 0o700) // cwd に使うので読み取り系でも要る
	cmd := exec.Command("git", args...)
	cmd.Dir = memoryStagingDir()
	cmd.Env = append(os.Environ(),
		"GIT_DIR="+memoryRepoDir(),
		"GIT_WORK_TREE="+memoryStagingDir(),
		// ユーザー設定を継がない（署名・hooksPath・core.autocrlf 等の巻き添えを断つ）。
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=af-memory",
		"GIT_AUTHOR_EMAIL=af-memory@agent-fleet.local",
		"GIT_COMMITTER_NAME=af-memory",
		"GIT_COMMITTER_EMAIL=af-memory@agent-fleet.local",
	)
	return cmd
}

// memoryGitRun は git を実行し、trim した stdout を返す。失敗時は stderr を error に畳む
// （gitx.Run と同じ流儀だが、こちらは GIT_DIR 隔離環境を使うため独立している）。
func memoryGitRun(args ...string) (string, error) {
	out, err := memoryGit(args...).Output()
	s := strings.TrimSpace(string(out))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				err = fmt.Errorf("%v: %s", err, msg)
			}
		}
	}
	return s, err
}

// memoryEnsureRepo は bare repo と staging を用意する（冪等）。
func memoryEnsureRepo() error {
	if err := os.MkdirAll(memoryStagingDir(), 0o700); err != nil {
		return err
	}
	dir := memoryRepoDir()
	if st, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil && !st.IsDir() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	// init だけは GIT_DIR/GIT_WORK_TREE を効かせず、パス指定で作る。
	cmd := exec.Command("git", "init", "--bare", "--quiet", "-b", memoryBranch, dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("init af-memory repo: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// memoryHasCommits は HEAD が指す commit が既にあるか（初回 snapshot 前は false）。
func memoryHasCommits() bool {
	_, err := memoryGitRun("rev-parse", "--verify", "--quiet", memoryBranch+"^{commit}")
	return err == nil
}

// memoryProjectRef は claude のプロジェクト 1 件（slug と ★6 の整形表示名）。
type memoryProjectRef struct {
	Slug    string `json:"slug"`
	Display string `json:"display"`
}

// memorySnapshotInfo は 1 件の snapshot（= commit）の要約。一覧 API がそのまま返す形。
type memorySnapshotInfo struct {
	Rev      string             `json:"rev"`
	Short    string             `json:"short"`
	At       string             `json:"at"`       // RFC3339（author date）
	Subject  string             `json:"subject"`  // 1 行目
	Trigger  string             `json:"trigger"`  // auto | manual | pre-restore | restore | import
	Kinds    []string           `json:"kinds"`    // 変更のあった kind（claude / codex）
	Projects []memoryProjectRef `json:"projects"` // 変更のあった claude プロジェクト
	Files    int                `json:"files"`    // 変更ファイル数
}

// git log の解析用セパレータ。レコード境界に \x1e、フィールド境界に \x1f を使い、
// メモリ本文（md）に現れ得ない制御文字で確実に割る。
const (
	memoryRecSep = "\x1e"
	memoryFldSep = "\x1f"
)

// memoryListSnapshots は新しい順に snapshot を返す。before が非空なら、その RFC3339
// 時刻「以前」の直近から並べる（日時指定 UI の下敷き）。
func memoryListSnapshots(limit int, before string) ([]memorySnapshotInfo, error) {
	// 入力検証は repo の有無より先に行う（履歴ゼロでも不正な before は 400 で返す）。
	if before != "" {
		if _, err := time.Parse(time.RFC3339, before); err != nil {
			return nil, fmt.Errorf("before must be RFC3339")
		}
	}
	if !memoryHasCommits() {
		return []memorySnapshotInfo{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	args := []string{"log", memoryBranch, "-n", strconv.Itoa(limit),
		"--pretty=format:" + memoryRecSep + "%H" + memoryFldSep + "%aI" + memoryFldSep + "%s" + memoryFldSep + "%(trailers:key=AF-Trigger,valueonly)",
		"--name-only"}
	if before != "" {
		args = append(args, "--before="+before)
	}
	out, err := memoryGitRun(args...)
	if err != nil {
		return nil, err
	}
	list := []memorySnapshotInfo{}
	for _, rec := range strings.Split(out, memoryRecSep) {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		head, rest, _ := strings.Cut(rec, "\n")
		fields := strings.Split(head, memoryFldSep)
		if len(fields) < 4 || fields[0] == "" {
			continue
		}
		info := memorySnapshotInfo{
			Rev: fields[0], At: fields[1], Subject: fields[2],
			Trigger: strings.TrimSpace(fields[3]),
		}
		if len(info.Rev) >= 8 {
			info.Short = info.Rev[:8]
		}
		info.Kinds, info.Projects, info.Files = memorySummarizePaths(strings.Split(rest, "\n"))
		list = append(list, info)
	}
	return list, nil
}

// memorySummarizePaths は変更パス列を「kind 一覧 / claude プロジェクト一覧 / 件数」へ畳む。
func memorySummarizePaths(paths []string) (kinds []string, projects []memoryProjectRef, files int) {
	projects = []memoryProjectRef{}
	kinds = []string{}
	seenKind, seenSlug := map[string]bool{}, map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		files++
		if kind, _, ok := strings.Cut(p, "/"); ok && !seenKind[kind] {
			seenKind[kind] = true
			kinds = append(kinds, kind)
		}
		if slug, ok := memoryScopeSlug(p); ok && !seenSlug[slug] {
			seenSlug[slug] = true
			projects = append(projects, memoryProjectRef{Slug: slug, Display: memorySlugDisplay(slug)})
		}
	}
	return kinds, projects, files
}

// memoryHeadTime は最新 snapshot の author 時刻（commit が無ければゼロ値）。
func memoryHeadTime() time.Time {
	out, err := memoryGitRun("log", "-1", "--pretty=format:%aI", memoryBranch)
	if err != nil || out == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, out)
	if err != nil {
		return time.Time{}
	}
	return t
}

// memoryResolveRev は rev（sha / ref）または at（RFC3339）を snapshot の sha に解決する。
// at は「その時刻以前の直近 snapshot」— 日時指定ロールバックの意味論（docs/log/39 ③）。
func memoryResolveRev(rev, at string) (string, error) {
	switch {
	case rev != "":
		if !memoryRevSafe(rev) {
			return "", fmt.Errorf("invalid rev")
		}
		sha, err := memoryGitRun("rev-parse", "--verify", "--quiet", rev+"^{commit}")
		if err != nil || sha == "" {
			return "", fmt.Errorf("unknown rev %q", rev)
		}
		return sha, nil
	case at != "":
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			return "", fmt.Errorf("at must be RFC3339")
		}
		sha, err := memoryGitRun("rev-list", "-1", "--before="+at, memoryBranch)
		if err != nil || sha == "" {
			return "", fmt.Errorf("no snapshot at or before %s", at)
		}
		return sha, nil
	}
	return "", fmt.Errorf("rev or at required")
}

// memoryRevSafe は rev 文字列がオプション注入やパス脱出に使えない形かを見る。
// git のリビジョン指定は表現力が高いので、受け口を英数と限られた記号に絞る。
func memoryRevSafe(rev string) bool {
	if rev == "" || len(rev) > 200 || strings.HasPrefix(rev, "-") {
		return false
	}
	for _, r := range rev {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '/', r == '.', r == '~', r == '^':
		default:
			return false
		}
	}
	return !strings.Contains(rev, "..")
}

// memoryPathSafe は diff / restore のパススコープが repo 内の宣言済み prefix に収まるか。
func memoryPathSafe(p string) bool {
	if p == "" {
		return true // 省略 = 全体
	}
	if strings.HasPrefix(p, "-") || strings.Contains(p, "..") || filepath.IsAbs(p) {
		return false
	}
	for _, r := range memoryRootDecls() {
		if p == r.RepoPrefix || strings.HasPrefix(p, r.RepoPrefix+"/") {
			return true
		}
	}
	return false
}

// memoryDiff は 2 時点間の unified diff を返す。from が空なら「その commit が入れた
// 変更」= 親との差分（初回 snapshot は親が無いので空ツリーとの差分）。
//
// 比較の両端は必ず commit を明示する。`git diff <rev>` は「rev と作業ツリー」の差分に
// なってしまい、staging の中身（= live の現在）が混ざる — 履歴閲覧としては誤りなので、
// `<rev>^!` のような親依存の略記も使わない。
func memoryDiff(from, to, path string) (string, error) {
	if !memoryPathSafe(path) {
		return "", fmt.Errorf("invalid path scope")
	}
	base := from
	if base == "" {
		parent, err := memoryGitRun("rev-parse", "--verify", "--quiet", to+"^{commit}^")
		if err != nil || parent == "" {
			// 親なし（初回 snapshot）— 空ツリーを相手にする。
			empty, eerr := memoryGitRun("hash-object", "-t", "tree", "/dev/null")
			if eerr != nil || empty == "" {
				return "", fmt.Errorf("resolve empty tree: %v", eerr)
			}
			base = empty
		} else {
			base = parent
		}
	}
	args := []string{"diff", "--no-color", "--find-renames", base, to}
	if path != "" {
		args = append(args, "--", path)
	}
	return memoryGitRun(args...)
}
