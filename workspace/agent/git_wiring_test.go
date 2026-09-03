package main

// git_wiring.go の配線が**生きているか**を通しで見る 1 本。
//
// 🔥 `gitx.Configure` が捕まえるのは**未配線**（nil / 零値）だけで、**間違った配線**は
// 捕まえられない。実際に踏める形が 3 つある:
//
//   - `RepoLocked` を `return false` 固定    → **削除ロックが丸ごと消える**
//   - `WorktreeHasSessions` を `false` 固定  → 動いているセッションのワークツリーを消す
//   - `ErrCodeBranchInUse` を別の綴りにする  → Console が生のコードを出す（i18n が引けない）
//
// どれも配線 1 行の書き換えである。移送でカバレッジが落ちたわけではない（移送前は
// そもそも「配線」という概念が無かった）が、**壊せる面が増えた**のは確かなので、
// ここで 1 本止める。
//
// 検査の形は 2 つ:
//
//   - 関数は**関数ポインタの同一性**（別の関数や閉包にすり替わっていれば落ちる）
//   - 値は本物の定数と同じであること
//
// そして **Deps のフィールド集合と検査の集合を突き合わせる**ので、フィールドが増えたのに
// 検査を足さなければここが落ちる。

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

func TestGitWiringIsLive(t *testing.T) {
	w := gitx.Wired()

	checks := map[string]func(t *testing.T){
		"AbsPath":        func(t *testing.T) { sameGitFunc(t, w.AbsPath, sessionx.AbsPath) },
		"RepoLocked":     func(t *testing.T) { sameGitFunc(t, w.RepoLocked, sessionx.RepoLocked) },
		"LockedRepoDirs": func(t *testing.T) { sameGitFunc(t, w.LockedRepoDirs, sessionx.LockedRepoDirs) },

		"LiveSessionsInDir":   func(t *testing.T) { sameGitFunc(t, w.LiveSessionsInDir, sessionx.LiveSessionsInDir) },
		"LockedSessionsInDir": func(t *testing.T) { sameGitFunc(t, w.LockedSessionsInDir, sessionx.LockedSessionsInDir) },
		"WorktreeHasSessions": func(t *testing.T) { sameGitFunc(t, w.WorktreeHasSessions, sessionx.WorktreeHasSessions) },
		"ManagedAlive":        func(t *testing.T) { sameGitFunc(t, w.ManagedAlive, sessionx.ManagedAlive) },

		"FinalizeSessionUsage": func(t *testing.T) { sameGitFunc(t, w.FinalizeSessionUsage, finalizeSessionUsage) },

		"RepoJobActive": func(t *testing.T) { sameGitFunc(t, w.RepoJobActive, repoJobActive) },
		// StartRepoJob だけは本物そのものではない —— sink を詰め替える adapter を
		// 通している（git_wiring.go の startGitRepoJob）。**その adapter であること**を
		// 見る（素の startRepoJob には型が合わないので取り違えは起きないが、
		// 「別の閉包に差し替えた」は捕まえたい）。
		"StartRepoJob": func(t *testing.T) { sameGitFunc(t, w.StartRepoJob, startGitRepoJob) },

		"IsSvnRepo":    func(t *testing.T) { sameGitFunc(t, w.IsSvnRepo, isSvnRepo) },
		"SvnRepoEntry": func(t *testing.T) { sameGitFunc(t, w.SvnRepoEntry, svnRepoEntry) },

		"EnsureCredHelper": func(t *testing.T) { sameGitFunc(t, w.EnsureCredHelper, ensureCredHelper) },
		"InternalGitHost":  func(t *testing.T) { sameGitFunc(t, w.InternalGitHost, internalGitHost) },

		"FirstNonEmpty": func(t *testing.T) {
			sameGitFunc(t, w.FirstNonEmpty, firstNonEmpty)
			// 🔥 **本物の優先順位をここで直接止める。**
			// gitx のテストはこの関数の**写し**を使う（パッケージを跨いで本物を
			// 呼べない）ので、写しと本物がずれると **gitx 側は緑のまま本番だけ壊れる**。
			// 実測: `connections.go:808` に「先頭の優先順位を無視する」変異を当てると
			// develop は 2 失敗（TestApplyGitIdentity + TestParseBitbucketPullRequests）
			// だが、移送後は 1 失敗に減った —— 減ったのは、git の identity が
			// 「上書き＞プロバイダ＞アカウント」の順で効くことを**ついでに**押さえていた
			// TestApplyGitIdentity である。その 1 本ぶんをここで取り戻す。
			for _, c := range []struct {
				in   []string
				want string
			}{
				{[]string{"a", "b"}, "a"},     // 先頭が勝つ（identity の上書きが効く根拠）
				{[]string{"", "b", "c"}, "b"}, // 空は飛ばす
				{[]string{"", ""}, ""},        // 全部空なら空
			} {
				if got := firstNonEmpty(c.in...); got != c.want {
					t.Fatalf("firstNonEmpty(%q) = %q, want %q（gitx の写しとずれている）", c.in, got, c.want)
				}
			}
		},
		"GitConfigGlobal": func(t *testing.T) { sameGitFunc(t, w.GitConfigGlobal, gitConfigGlobal) },
		"GitHosts": func(t *testing.T) {
			if !reflect.DeepEqual(w.GitHosts, gitHosts) {
				t.Fatalf("対応ホストの表 = %v, want %v", w.GitHosts, gitHosts)
			}
		},

		"ScratchAutoRelocate": func(t *testing.T) { sameGitFunc(t, w.ScratchAutoRelocate, scratchAutoRelocate) },

		// エラーコードは errcodes.go の**本物と同一の綴り**であること。
		// ここが違うと Console の i18n が引けず、生のコードが画面に出る。
		"ErrCodeSessionsRunning":       func(t *testing.T) { sameGitCode(t, w.ErrCodeSessionsRunning, errCodeSessionsRunning) },
		"ErrCodeSessionsRunningDelete": func(t *testing.T) { sameGitCode(t, w.ErrCodeSessionsRunningDelete, errCodeSessionsRunningDelete) },
		"ErrCodeBranchInUse":           func(t *testing.T) { sameGitCode(t, w.ErrCodeBranchInUse, errCodeBranchInUse) },
		"ErrCodeWorktreeDirty":         func(t *testing.T) { sameGitCode(t, w.ErrCodeWorktreeDirty, errCodeWorktreeDirty) },
		"ErrCodeWorktreeRemoveFailed":  func(t *testing.T) { sameGitCode(t, w.ErrCodeWorktreeRemoveFailed, errCodeWorktreeRemoveFailed) },
		"ErrCodeHasWorktrees":          func(t *testing.T) { sameGitCode(t, w.ErrCodeHasWorktrees, errCodeHasWorktrees) },
		"ErrCodeLocked":                func(t *testing.T) { sameGitCode(t, w.ErrCodeLocked, errCodeLocked) },
		"ErrCodeLockedSessions":        func(t *testing.T) { sameGitCode(t, w.ErrCodeLockedSessions, errCodeLockedSessions) },
	}

	// 検査の集合と Deps のフィールド集合を突き合わせる。**フィールドが増えたら必ずここが
	// 落ちる**ので、「配線は足したが検査は足さなかった」が起きない。
	typ := reflect.TypeOf(w)
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		if _, ok := checks[name]; !ok {
			t.Errorf("gitx.Deps.%s の配線を検査していない（フィールドを足したら検査も足すこと）", name)
		}
	}
	for name := range checks {
		if !seen[name] {
			t.Errorf("gitx.Deps に %s は無い（検査だけが古い）", name)
		}
	}
	for name, run := range checks {
		t.Run(name, run)
	}
}

// sameGitFunc は「その関数そのものが配線されている」ことを見る。閉包や別の関数に
// すり替わっていれば、コードポインタが違うので落ちる。
func sameGitFunc(t *testing.T, got, want any) {
	t.Helper()
	g, w := reflect.ValueOf(got).Pointer(), reflect.ValueOf(want).Pointer()
	if g != w {
		t.Fatalf("配線先が違う: got %s, want %s", gitFuncName(g), gitFuncName(w))
	}
}

func gitFuncName(pc uintptr) string {
	if f := runtime.FuncForPC(pc); f != nil {
		return f.Name()
	}
	return "?"
}

func sameGitCode(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("エラーコード = %q, want %q", got, want)
	}
}

// startGitRepoJob の詰め替えが**本物の sink まで届いている**こと。
//
// ここは移送で新しく出来た唯一の実行時の継ぎ目である。`*repoJobSink` の
// `tailString()` が未公開なので、gitx へは adapter 越しにしか渡せない
// （git_wiring.go の gitRepoJobSink）。詰め替えを間違えると:
//
//   - Write が本物の sink へ行かない → **進捗が Console に出ない**
//   - Tail() が空を返す              → clone が失敗したとき **理由が消える**
//     （「clone に失敗しました」だけが出て、git の出力が落ちる）
//
// どちらもコンパイルは通り、既存のどのテストも赤くならない。
func TestStartGitRepoJobCarriesTheRealSink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetRepoJobs(t)

	done := make(chan string, 1)
	got := startGitRepoJob("git", "probe", t.TempDir(), "https://example.invalid/x.git",
		func(ctx context.Context, sink gitx.RepoJobSink) error {
			if sink == nil {
				done <- ""
				return nil
			}
			if _, err := sink.Write([]byte("Receiving objects: 42%")); err != nil {
				done <- "write: " + err.Error()
				return nil
			}
			done <- sink.Tail()
			return nil
		})

	// 戻り値は main の RepoJob のまま（gitx は `any` で受けて JSON にするだけ）。
	// ここが別の型に化けると、202 の本文の形が黙って変わる。
	job, ok := got.(RepoJob)
	if !ok {
		t.Fatalf("戻り値の型 = %T, want main.RepoJob（202 の本文の形が変わる）", got)
	}
	if job.ID == "" || job.Kind != "git" || job.Name != "probe" {
		t.Fatalf("ジョブの初期スナップショットが壊れている: %+v", job)
	}

	select {
	case tail := <-done:
		if tail != "Receiving objects: 42%" {
			t.Fatalf("adapter 越しに書いた内容が本物の sink から読めない: %q "+
				"(空なら Tail() が tailString() に繋がっていない・"+
				"違う内容なら Write の宛先が違う)", tail)
		}
	case <-time.After(5 * time.Second):
		// 🔥 素の `<-done` にしない。配線事故のとき **CI が「赤」ではなく
		// ジョブのタイムアウト**になり、何を待っていたのか残らない。
		t.Fatal("5 秒待っても run が呼ばれない（startRepoJob へ渡す詰め替えが壊れている）")
	}
}

// TestGitRepoJobSinkHasOneConstructionSite は、`startGitRepoJob` の
// `if sink == nil` ガードが**到達不能である前提**を機械で固定する（RECLAIM-C の債務）。
//
// 前提は 2 つ: ①`*repoJobSink` を作るのは `startRepoJob` の 1 箇所だけ
// ②`gitRepoJobSink{…}` を組むのも 1 箇所だけ。どちらかが増えると、
// **中身が nil の非 nil インターフェース**（`gitRepoJobSink{nil}`）を渡す経路が生まれ、
// gitx 側の `if sink != nil` が真になって nil の Writer へ書きに行く。
//
// 🔥 これまでこの罠は**コメントにしか書かれておらず、ガード自身はどのテストも通らない**
// （到達不能なので変異を当てても赤くならない）。守っているのは「作る箇所の数」なので、
// そこをソースで見る。
// 🔥 **走査した本数も確かめる**（#320 の「1 件も見つからなければ何も検査しない」対策）。
func TestGitRepoJobSinkHasOneConstructionSite(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned, sinkNew, wrapNew := 0, 0, 0
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), n, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		scanned++
		ast.Inspect(f, func(node ast.Node) bool {
			lit, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			switch id := lit.Type.(type) {
			case *ast.Ident:
				switch id.Name {
				case "repoJobSink":
					sinkNew++
				case "gitRepoJobSink":
					wrapNew++
				}
			}
			return true
		})
	}
	if scanned < 50 {
		t.Fatalf("非テストの .go を %d 本しか読めていない＝この検査が無言化している", scanned)
	}
	if sinkNew != 1 {
		t.Errorf("repoJobSink を組む箇所が %d 箇所（want 1）。"+
			"増やすなら nil を渡さないことを各所で保証すること", sinkNew)
	}
	if wrapNew != 1 {
		t.Errorf("gitRepoJobSink を組む箇所が %d 箇所（want 1）。"+
			"gitRepoJobSink{nil} は『中身が nil の非 nil インターフェース』になり、"+
			"gitx 側の nil 判定を素通りする", wrapNew)
	}
}
