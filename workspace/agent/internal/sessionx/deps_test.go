package sessionx

// sessionx 単体のテストは package main を持たないので、外向きの依存を自前で配線する。
//
// 🔥 **「作り物を並べる」だけにしなかった理由。** 移送前、これらのテストは main の**本物**を
// 呼んで走っていた。作り物に差し替えると、アサーションも通る枝も同じまま
// **捕まえられるバグの集合だけが縮む**（README §4 の 1 番目の落とし穴）。
// そこで、どの依存が実際に踏まれるかを**測ってから**決めた:
//
//	計測（`p(name)` で数えるだけの配線に差し替えて全 sessionx テストを 1 回）→
//	  SplitFrontmatter=22 / BrowseRoot=14 / FirstNonEmpty=13 / ToolchainShellPrefix=5 /
//	  IsSvnRepo=2 / RepoJobsRunning=1、他の 7 本は 0 回
//
// 踏まれる 6 本は **main の実装をそのまま写す**（下記）。踏まれない 7 本は **panic する**。
// 作り物の戻り値を置くと、将来ここへ到達するテストが増えたときに**嘘の値で静かに緑になる**
// ので、鳴る側に倒してある（internal/gitx/deps_test.go と同じ形）。
//
// 🔥 **`ToolchainShellPrefix` に空文字を返す作り物を置いてはいけない。** main の
// `env_toolchains.go` は選択ファイルが無くても **`defaultTimezone = "Asia/Tokyo"` を既定に入れる**
// ので、本物は空ではなく `export TZ='Asia/Tokyo'; ` を返す。ここを空にすると、
// tmux へ渡すプログラム文字列が本番と変わったまま**テストは緑**になる（実際に一度そうした）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func init() { Configure(testDeps()) }

// probe は「どの依存が何回踏まれたか」を数えるための計測用。SESSIONX_PROBE=1 で
// stderr へ出す。**測り直すときにこれを使う**（配線を作り物へ戻さずに済む）。
var (
	probeMu sync.Mutex
	probe   = map[string]int{}
)

func p(name string) {
	probeMu.Lock()
	probe[name]++
	probeMu.Unlock()
	if os.Getenv("SESSIONX_PROBE") != "" {
		fmt.Fprintln(os.Stderr, "PROBE "+name)
	}
}

// unreached は「sessionx のテストからは踏まれない」と実測した依存の配線。
// 踏んだら止まる ——「静かに動くより落ちる方を選ぶ」を、作り物にも適用する。
func unreached(name string) {
	panic("sessionx test deps: " + name + " は移送時の実測では 1 度も踏まれていない。" +
		"ここへ来たということは新しい検査が main の実装を必要としている: " +
		"作り物の戻り値を置く前に、main 側（session_wiring.go）と同じ振る舞いを写すか、" +
		"テストを package main へ置くかを決めること")
}

// testDeps は sessionx 単体テスト用の配線一式。**網羅性の検査（下）が同じものを使う**ので、
// 1 箇所に置く。
func testDeps() Deps {
	return Deps{
		// --- 実測で踏まれる 6 本（main の実装の写し）---

		// connections.go の写し。
		FirstNonEmpty: func(vals ...string) string {
			p("FirstNonEmpty")
			for _, v := range vals {
				if v != "" {
					return v
				}
			}
			return ""
		},

		// repo_prompts.go の写し。**「近似で書き直さない」**——境界（終端の無いブロック・
		// CRLF・値のクォート剥がし）まで含めて同じでないと、被覆だけが縮む。
		SplitFrontmatter: func(s string) (map[string]string, string) {
			p("SplitFrontmatter")
			meta := map[string]string{}
			if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
				return meta, s
			}
			rest := s[strings.IndexByte(s, '\n')+1:]
			end := strings.Index(rest, "\n---")
			if end < 0 {
				return meta, s // unterminated block — treat the whole file as body
			}
			fm := rest[:end]
			body := rest[end+len("\n---"):]
			if i := strings.IndexByte(body, '\n'); i >= 0 { // drop the rest of the closing --- line
				body = body[i+1:]
			} else {
				body = ""
			}
			for _, ln := range strings.Split(fm, "\n") {
				ln = strings.TrimRight(ln, "\r")
				if i := strings.IndexByte(ln, ':'); i > 0 {
					k := strings.ToLower(strings.TrimSpace(ln[:i]))
					v := strings.Trim(strings.TrimSpace(ln[i+1:]), `"'`)
					meta[k] = v
				}
			}
			return meta, strings.TrimLeft(body, "\n")
		},

		// fs.go の写し。env が無ければ homeDir()（テストは temp HOME を張る）。
		BrowseRoot: func() string {
			p("BrowseRoot")
			if r := os.Getenv("AF_BROWSE_ROOT"); r != "" {
				return r
			}
			return homeDir()
		},

		// svn.go の写し。
		IsSvnRepo: func(dir string) bool {
			p("IsSvnRepo")
			fi, err := os.Stat(filepath.Join(dir, ".svn"))
			return err == nil && fi.IsDir()
		},

		// repo_jobs.go の写し —— **に見えるが、台帳そのものが main にしか無い**。
		// sessionx のテストバイナリでは取り込みジョブを 1 本も起動しないので、本物も
		// 0 を返す。**「テストでは 0 だから」ではなく「この プロセスには台帳が存在しない」**
		// という理由で 0 である。台帳を使う検査が来たら main へ置くこと。
		RepoJobsRunning: func() int { p("RepoJobsRunning"); return 0 },

		// env_toolchains.go の写し（選択ファイルが無い経路のみ）。
		// 🔥 **空文字を返してはいけない。** 選択が無くても `defaultTimezone` が入るので、
		// 本物は `export TZ='Asia/Tokyo'; ` を返す。選択ファイルが在る環境で走らせたら、
		// 写しでは再現できないので落とす。
		ToolchainShellPrefix: func() string {
			p("ToolchainShellPrefix")
			path := filepath.Join(homeDir(), ".config", "agent-fleet", "toolchains.json")
			if b, err := os.ReadFile(path); err == nil {
				var t struct {
					Java, Node, Go, Timezone string
				}
				_ = json.Unmarshal(b, &t)
				if t.Java != "" || (t.Node != "" && t.Node != "system") || (t.Go != "" && t.Go != "system") {
					panic("sessionx test deps: ToolchainShellPrefix —— " + path +
						" に選択が在る環境では、この写しでは本物（javaHomeFor / nvm の glob / goRootFor）を再現できない。" +
						"この検査は package main へ置くこと")
				}
			}
			// 選択なし: java / node / go は空、TZ だけが既定で入る。
			const defaultTimezone = "Asia/Tokyo"
			if _, err := os.Stat("/usr/share/zoneinfo/" + defaultTimezone); err != nil {
				return ""
			}
			return "export TZ=" + session.ShellQuote(defaultTimezone) + "; "
		},

		// --- 実測で踏まれない 7 本（踏んだら落ちる）---
		EnvOr:                 func(k, d string) string { unreached("EnvOr"); return d },
		MaxUploadBytes:        func() int64 { unreached("MaxUploadBytes"); return 0 },
		FinalizeSessionUsage:  func(session.Meta) { unreached("FinalizeSessionUsage") },
		MaybeFoldSessionUsage: func() { unreached("MaybeFoldSessionUsage") },
		RemoveTerminalHistory: func(string) { unreached("RemoveTerminalHistory") },
		MCPConvID:             func() string { unreached("MCPConvID"); return "" },
		RunOperatorTurn: func(conv, text string) (string, error) {
			unreached("RunOperatorTurn")
			return "", nil
		},

		// --- エラーコード ---
		//
		// 🔥 **本物の綴りをここに写す。** 適当な文字列を置くと、コードを本文に出す検査が
		// 「何かが入っている」だけで緑になる。綴りが本物と一致していることは main 側の
		// session_wiring_test.go が errcodes.go と突き合わせて守っている。
		ErrCodeChatConversationNotFnd: "chat_conversation_not_found",
		ErrCodeForkAtUnsupported:      "fork_at_unsupported",
		ErrCodeForkBadAnchor:          "fork_bad_anchor",
		ErrCodeForkMissingDir:         "fork_missing_dir",
		ErrCodeForkUnsupportedKind:    "fork_unsupported_kind",
		ErrCodeLocked:                 "locked",
		ErrCodePasteTooLarge:          "paste_too_large",
		ErrCodePasteUnsupportedAgent:  "paste_unsupported_agent",
		ErrCodePasteUnsupportedKind:   "paste_unsupported_kind",
		ErrCodeTitleFeatureDisabled:   "title_feature_disabled",
		ErrCodeTitleNoContent:         "title_no_content",
	}
}
