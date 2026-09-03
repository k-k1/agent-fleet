package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// 新しい worktree を切ったとき、起点が「親のローカル base」ではなく「origin/<base> の
// 先端」になること。ローカルブランチを動かすものはこの製品に一つも無い（自動 fetch は
// origin/* しか更新しない）ので、これが無いと古いクローンから切ったセッションが静かに
// 何週間も前の基点で始まる。
//
// 直し方の要は「親を触らない」こと: 親を pull --ff-only すると、親で作業中のセッションの
// 足元でファイルが入れ替わる（無関係なファイルが dirty でも FF は成功してしまう）。
// なのでこの関数は**新しい worktree の中だけ**を進める。
func TestFastForwardNewWorktreeToOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	// origin（bare）+ それを push した上流の作業コピー + 親クローン。
	origin := filepath.Join(home, "origin.git")
	runIntegrationGit(t, home, "init", "--quiet", "--bare", "-b", "main", origin)
	up := filepath.Join(home, "up")
	gitInit(t, up)
	runIntegrationGit(t, up, "remote", "add", "origin", origin)
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")

	parent := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	runIntegrationGit(t, home, "clone", "--quiet", origin, parent)

	// クローンしたあとに origin が進む（＝親のローカル main は古いまま）。
	commitIntegrationFile(t, up, "newer")
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")
	tip := gitRev(t, up, "HEAD")
	stale := gitRev(t, parent, "main")
	if tip == stale {
		t.Fatal("fixture is wrong: origin did not advance past the parent's local main")
	}

	wt, err := gitx.EnsureWorktree(parent, "main", "temp/fresh", "wip-fresh")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree: %v", err)
	}
	if got := gitRev(t, wt, "HEAD"); got != stale {
		t.Fatalf("worktree started at %s, want the parent's local main %s (fixture)", got, stale)
	}

	gitx.FastForwardNewWorktreeToOrigin(wt, "main")

	if got := gitRev(t, wt, "HEAD"); got != tip {
		t.Errorf("worktree HEAD = %s, want origin's tip %s", got, tip)
	}
	// ★ 親は動かないこと。ここが「親を FF する」案との違いで、親で走っている
	// セッションの作業コピーを勝手に進めないための一線。
	if got := gitRev(t, parent, "main"); got != stale {
		t.Errorf("the parent's local main moved to %s; it must stay at %s", got, stale)
	}
	if got := gitRev(t, parent, "HEAD"); got != stale {
		t.Errorf("the parent's HEAD moved to %s; it must stay at %s", got, stale)
	}
	// 新しいブランチに upstream が付いていないこと（explicit refspec の pull は
	// 追跡を設定しない）。付くと ↑↓ バッジの意味が変わり、以後の pull が base を
	// 作業ブランチへ流し込む。
	if out, err := gitx.Run(wt, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		t.Errorf("temp/fresh gained an upstream (%s); it must stay untracked", out)
	}
}

// ローカル base に未 push のコミットがある（＝分岐している）ときは、そのローカルの
// 仕事こそが利用者の意図した起点なので、黙って origin へ倒さない。
func TestFastForwardNewWorktreeKeepsDivergedLocalBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	origin := filepath.Join(home, "origin.git")
	runIntegrationGit(t, home, "init", "--quiet", "--bare", "-b", "main", origin)
	up := filepath.Join(home, "up")
	gitInit(t, up)
	runIntegrationGit(t, up, "remote", "add", "origin", origin)
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")

	parent := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	runIntegrationGit(t, home, "clone", "--quiet", origin, parent)

	commitIntegrationFile(t, up, "remote-side")
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")
	commitIntegrationFile(t, parent, "local-only") // 親のローカルにだけある仕事
	local := gitRev(t, parent, "main")

	wt, err := gitx.EnsureWorktree(parent, "main", "temp/diverged", "wip-diverged")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree: %v", err)
	}
	gitx.FastForwardNewWorktreeToOrigin(wt, "main")

	if got := gitRev(t, wt, "HEAD"); got != local {
		t.Errorf("worktree HEAD = %s, want the local base %s (a diverged base must not be replaced)", got, local)
	}
}

// origin にそのブランチが無い（ローカル専用）／リモートそのものが無いときは、
// 何も起きず起動も壊れないこと。
func TestFastForwardNewWorktreeWithoutOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent) // remote 無し
	wt, err := gitx.EnsureWorktree(parent, "main", "temp/local", "wip-local")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree: %v", err)
	}
	before := gitRev(t, wt, "HEAD")
	gitx.FastForwardNewWorktreeToOrigin(wt, "main")
	gitx.FastForwardNewWorktreeToOrigin(wt, "")                // base 不明
	gitx.FastForwardNewWorktreeToOrigin(wt, "--upload-pack=x") // 引数に化ける名前
	if got := gitRev(t, wt, "HEAD"); got != before {
		t.Errorf("HEAD moved to %s without an origin; want %s", got, before)
	}
}

func gitRev(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitx.Run(dir, "rev-parse", ref)
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, dir, err)
	}
	return out
}

// 配線まで通しての確認: POST /sessions の worktree-then-start が、実際に origin の
// 先端で始まること。ヘルパ単体が正しくても呼ばれていなければ意味がないので、
// base 未指定（＝親の現在ブランチを起点にする経路）でここを踏む。
func TestCreateSessionWorktreeStartsAtOriginTip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))

	origin := filepath.Join(home, "origin.git")
	runIntegrationGit(t, home, "init", "--quiet", "--bare", "-b", "main", origin)
	up := filepath.Join(home, "up")
	gitInit(t, up)
	runIntegrationGit(t, up, "remote", "add", "origin", origin)
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")

	parent := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	runIntegrationGit(t, home, "clone", "--quiet", origin, parent)

	commitIntegrationFile(t, up, "landed-after-the-clone")
	runIntegrationGit(t, up, "push", "--quiet", "origin", "main")
	tip := gitRev(t, up, "HEAD")
	stale := gitRev(t, parent, "main")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", sessionx.HandleCreateSession)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var created session.Session
	do(t, srv, "POST", "/sessions", map[string]any{
		// branch を送らない＝「既定の基点」。親の現在ブランチ(main)が起点になる。
		"worktree": true, "dir": parent, "new_branch": "feat-fresh", "kind": "shell",
	}, http.StatusCreated, &created)
	defer exec.Command("tmux", "kill-session", "-t", session.TmuxName(created.Name)).Run()

	if got := gitRev(t, created.Dir, "HEAD"); got != tip {
		t.Errorf("session started at %s, want origin's tip %s", got, tip)
	}
	if got := gitRev(t, parent, "main"); got != stale {
		t.Errorf("the launch moved the parent's main to %s; it must stay at %s", got, stale)
	}
}
