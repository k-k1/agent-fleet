package projcfg

// ignore.go — docs/56 §7.5 操作A（「無視に追加」の安全・取り返しがつく方；「追跡から
// 外す」は操作B・P2）。Idempotent line append to .git/info/exclude (default — not
// committed, "取り返しがつく") or .gitignore (committed, affects every colleague).
// No marker comment (docs/57 憲章2) and no commit (憲章9) — this only ever touches
// the ignore file itself.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// Ignore targets (docs/56 §7.5's table).
const (
	IgnoreExclude   = "exclude"   // .git/info/exclude — uncommitted; common dir, so it affects the parent clone AND every linked worktree (docs/56 §2.4 実測)
	IgnoreGitignore = "gitignore" // .gitignore — committed; the whole team
)

// GitCommonDir resolves dir's git COMMON directory — the actual `.git` a linked
// worktree shares with its parent clone. `.git/info/exclude` lives here, which is
// why it is NOT a per-worktree setting (docs/56 §2.4 / §7.5, measured).
func GitCommonDir(dir string) (string, error) {
	return gitx.Run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
}

// AddIgnorePattern appends pattern (a repo-relative path, e.g. ".mcp.json") to
// dir's ignore file for where, unless an identical line is already present
// (docs/56 §7.5: "既に同じパターンがあれば足さない"). Creates the file if it does
// not exist yet; preserves everything else in it, including a missing trailing
// newline convention becoming present (never removed).
func AddIgnorePattern(dir, where, pattern string) error {
	path, err := IgnoreFilePath(dir, where)
	if err != nil {
		return err
	}
	return appendLineIfAbsent(path, pattern)
}

// HasIgnorePattern reports whether pattern is already an exact line in dir's
// ignore file for where — the same "already covered" check AddIgnorePattern uses
// internally, exposed so a caller (docs/56's plan step) can preview the outcome
// without writing.
func HasIgnorePattern(dir, where, pattern string) (bool, error) {
	path, err := IgnoreFilePath(dir, where)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == pattern {
			return true, nil
		}
	}
	return false, nil
}

// IgnoreFilePath resolves the ignore file dir's AddIgnorePattern(where) would
// write to.
func IgnoreFilePath(dir, where string) (string, error) {
	switch where {
	case IgnoreExclude:
		common, err := GitCommonDir(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(common, "info", "exclude"), nil
	case IgnoreGitignore:
		return filepath.Join(dir, ".gitignore"), nil
	default:
		return "", fmt.Errorf("unknown ignore target: %q", where)
	}
}

func appendLineIfAbsent(path, line string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil // already covered — do not duplicate
		}
	}
	body := string(b)
	if len(body) > 0 && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += line + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
