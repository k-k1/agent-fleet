package gitx

// gitx 単体のテストは package main を持たないので、外向きの依存を自前で配線する。
//
// 🔥 **「作り物を並べる」だけにしなかった理由。** 移送前、これらのテストは main の
// **本物**を呼んで走っていた。作り物に差し替えると、アサーションも通る枝も同じまま
// **捕まえられるバグの集合だけが縮む**（README §4 の 1 番目の落とし穴）。
// そこで、どの依存が実際に踏まれるかを**測ってから**決めた:
//
//	計測（`p(name)` で数えるだけの配線に差し替えて全 gitx テストを 1 回）→
//	  ScratchAutoRelocate=9 / InternalGitHost=8 / FirstNonEmpty=2、他は 0 回
//
// 踏まれる 3 本は **main の実装をそのまま写す**（下記）。どれも env と純粋な計算だけで
// 出来ており、main の状態には触らないので、写しは本物と同じ振る舞いをする。
// 踏まれない 15 本は **panic する**。作り物の戻り値を置くと、将来ここへ到達する
// テストが増えたときに**嘘の値で静かに緑になる**ので、鳴る側に倒してある。

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func init() { Configure(testDeps()) }

// unreached は「gitx のテストからは踏まれない」と実測した依存の配線。
// 踏んだら止まる ——「静かに動くより落ちる方を選ぶ」を、作り物にも適用する。
func unreached(name string) {
	panic("gitx test deps: " + name + " は移送時の実測では 1 度も踏まれていない。" +
		"ここへ来たということは新しい検査が main の実装を必要としている: " +
		"作り物の戻り値を置く前に、main 側（git_wiring.go）と同じ振る舞いを写すか、" +
		"テストを package main へ置くかを決めること")
}

// testDeps は gitx 単体テスト用の配線一式。**網羅性の検査（下）が同じものを使う**ので、
// 1 箇所に置く。
func testDeps() Deps {
	return Deps{
		// --- 実測で踏まれる 3 本（main の実装の写し）---

		// scratch.go の写し。env が無ければ何もしない ——「テストでは no-op だから」と
		// 空関数にすると、**AF_WS_SCRATCH が立っている環境でだけ本物と挙動が変わる**。
		// 写しておけば、どの環境でも移送前と同じことが起きる。
		ScratchAutoRelocate: func(dir string) {
			if dir == "" || os.Getenv("AF_WS_SCRATCH") == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "af-scratch", "--auto", dir).CombinedOutput()
			msg := strings.TrimSpace(string(out))
			if err != nil {
				log.Printf("scratch: auto relocate %s failed: %v: %s", dir, err, msg)
				return
			}
			if msg != "" {
				log.Printf("scratch: %s", msg)
			}
		},
		// cred_helper.go の写し（1 行・env だけ）。
		InternalGitHost: func() string { return strings.TrimSpace(os.Getenv("AF_INTERNAL_GIT_HOST")) },
		// connections.go の写し（純粋）。
		FirstNonEmpty: func(vals ...string) string {
			for _, v := range vals {
				if v != "" {
					return v
				}
			}
			return ""
		},

		// --- 実測で踏まれない 15 本 ---
		AbsPath:              func(s string) string { unreached("AbsPath"); return s },
		RepoLocked:           func(string) bool { unreached("RepoLocked"); return false },
		LockedRepoDirs:       func() map[string]bool { unreached("LockedRepoDirs"); return nil },
		LiveSessionsInDir:    func(string) []string { unreached("LiveSessionsInDir"); return nil },
		LockedSessionsInDir:  func([]session.Meta, string) []string { unreached("LockedSessionsInDir"); return nil },
		WorktreeHasSessions:  func(string) bool { unreached("WorktreeHasSessions"); return false },
		ManagedAlive:         func(session.Meta) bool { unreached("ManagedAlive"); return false },
		FinalizeSessionUsage: func(session.Meta) { unreached("FinalizeSessionUsage") },
		RepoJobActive:        func(string) bool { unreached("RepoJobActive"); return false },
		StartRepoJob: func(string, string, string, string, func(context.Context, RepoJobSink) error) any {
			unreached("StartRepoJob")
			return nil
		},
		IsSvnRepo:        func(string) bool { unreached("IsSvnRepo"); return false },
		SvnRepoEntry:     func(string, string) Repo { unreached("SvnRepoEntry"); return Repo{} },
		EnsureCredHelper: func() error { unreached("EnsureCredHelper"); return nil },
		GitConfigGlobal:  func(string, string) error { unreached("GitConfigGlobal"); return nil },

		// 値で受け取るもの。**本物の値を書き写さない** —— 書き写した瞬間に、
		// それ自体が二つ目の出所になる（本物は main の git_wiring.go が配線する）。
		// gitx のテストはどちらも読まないことを確認済み（gitHosts の唯一の読み手は
		// HandleGitProviderIdentityPut で、gitx のテストは呼んでいない）。
		GitHosts: map[string]string{"gitx-test.invalid": "gitx-test"},

		ErrCodeSessionsRunning:       "gitx-test-sessions_running",
		ErrCodeSessionsRunningDelete: "gitx-test-sessions_running_delete",
		ErrCodeBranchInUse:           "gitx-test-branch_in_use",
		ErrCodeWorktreeDirty:         "gitx-test-worktree_dirty",
		ErrCodeWorktreeRemoveFailed:  "gitx-test-worktree_remove_failed",
		ErrCodeHasWorktrees:          "gitx-test-has_worktrees",
		ErrCodeLocked:                "gitx-test-locked",
		ErrCodeLockedSessions:        "gitx-test-locked_sessions",
	}
}

// 🔥 Deps の**どのフィールドを 1 つ落としても** Configure が落ちること。
//
// 網羅の検査そのものを reflect で回すので、**フィールドが増えたら自動で対象になる**。
// 手で並べた一覧（実装側もテスト側も）は、増えたときに漏れて、しかも漏れても何も
// 起きない —— 落ちるのは「削除のロック判定が黙って常に false になる」ような、
// 誰も気付かない側である。
func TestConfigureRejectsEveryUnwiredField(t *testing.T) {
	good := testDeps()
	v := reflect.ValueOf(good)
	typ := v.Type()
	if typ.NumField() < 20 {
		t.Fatalf("Deps のフィールドが %d 個しか無い（構造体を取り違えている）", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		t.Run(f.Name, func(t *testing.T) {
			// 正しい配線へ必ず戻す（deps はパッケージ全体で共有する）。
			defer Configure(good)
			broken := reflect.New(typ).Elem()
			broken.Set(v)
			broken.Field(i).Set(reflect.Zero(f.Type))
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s を未配線にしても Configure が通った（配線漏れが静かに素通りする）", f.Name)
				}
				if !strings.Contains(fmt.Sprint(r), f.Name) {
					t.Fatalf("panic に %s の名前が出ていない: %v", f.Name, r)
				}
			}()
			Configure(broken.Interface().(Deps))
		})
	}
}

// 零値ではないが**中身の無い**配線も拒むこと（`unwired` のマップの枝）。
// 零値だけを見ていると、`map[string]string{}` が「配線済み」として通り、
// 対応プロバイダの表が空のまま動く（＝どのホストも「未対応」になる）。
func TestConfigureRejectsHollowGitHosts(t *testing.T) {
	defer Configure(testDeps())
	d := testDeps()
	d.GitHosts = map[string]string{}
	defer func() {
		if recover() == nil {
			t.Fatal("空の GitHosts でも Configure が通った")
		}
	}()
	Configure(d)
}
