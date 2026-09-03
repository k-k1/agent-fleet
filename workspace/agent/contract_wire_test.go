package main

// contract_wire_test.go — 「契約の型化」の横展開（案 A のゴールデン中継）・agent 側。
//
// control-plane/contract_wire_test.go と**同じ仕組みの写し**。
// 🔴 **写しである理由**: control-plane と workspace/agent は**別の Go モジュール**なので、
// テストヘルパを共有できない（`wire_golden_test.go` / `routes_golden_test.go` が両側に在るのと同じ）。
// **片方を直したらもう片方も直すこと。**仕組みの説明は control-plane 側の冒頭コメントにある。
//
// 3 本立て: ①bind（Go フィールド名 ↔ json キー・同型の取り違えを捕まえる）
//           ②scan（TS 側のキー集合を表に固定する・**ドリフトと、実ファイルで結果が変わる走査の壊れ**）
//           ③match（TS ↔ Go・免除つき・**免除の寿命は 4 方向**）
// 🔴 **走査の壊れ全般を捕まえるのは②ではなく合成標本の対照**（TestTSInterfaceFieldsParser）。
// 実測: 走査を壊す変異を当てても実 TS が 1 行 1 フィールドだと②は緑のまま。詳細は CP 側の冒頭に。
//
// ⚠️ モジュールの外（Console の TS）を読むので、**手元で変異を当てるときは `go test -count=1`**。

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// agentContractFamilies は workspace/agent 側の家系。
func agentContractFamilies() []contractFamily {
	return []contractFamily{
		// チャットの 1 メッセージ。Console のチャット面が読む中心の型。
		{
			name:    "chatx.ChatMessage",
			goType:  reflect.TypeOf(chatx.ChatMessage{}),
			binding: chatMessageBinding,
			tsPath:  "../../console/src/types/chat.ts",
			tsName:  "ChatMessage",
			tsKeys: keySet("role", "content", "ts", "agent", "model", "steps", "session",
				"delivered", "notice_key", "notice_args", "report_kind", "report_reason"),
			tsOnly: map[string]string{},
			goOnly: map[string]string{
				"instr": "【穴】chatx.ChatMessage が出しているが Console の ChatMessage に宣言が無い。",
			},
		},

		// ブランチ一覧の 1 行。
		// 🔴 **この家系だけ TS 1 型 ↔ Go 2 型**——Console の `Branch` は
		// ローカル（gitx.BranchInfo）とリモート（gitx.remoteBranch）の**両方の窓口**が返す形を
		// 1 つの型で受けている。`default` はリモート側にしか無い。
		{
			name:    "gitx.BranchInfo",
			goType:  reflect.TypeOf(gitx.BranchInfo{}),
			binding: branchInfoBinding,
			tsPath:  "../../console/src/features/repos/BranchList.tsx",
			tsName:  "Branch",
			tsKeys:  keySet("name", "unix", "date", "subject", "default", "remote", "current", "worktree_path"),
			tsOnly: map[string]string{
				// **穴ではない。**兄弟の `remoteBranch`（internal/gitx/git_remote.go:42）が
				// `json:"default"` を出しており、BranchList はローカルとリモートの両方を同じ型で描く。
				// ⚠️ **remoteBranch は非公開**なので、この家系からは reflect で触れない。
				// 12 本目以降で internal/gitx に検査を置くなら、そちらで固定すること。
				"default": "穴ではない: 兄弟の remoteBranch(internal/gitx/git_remote.go:42) が json:\"default\" を出す。BranchList はローカルとリモートを同じ TS 型で描いている",
			},
			goOnly: map[string]string{},
		},

		// 掃除の控え（アーカイブ）。
		{
			name:    "cleanupManifest",
			goType:  reflect.TypeOf(cleanupManifest{}),
			binding: cleanupManifestBinding,
			tsPath:  "../../console/src/features/sessions/CleanupModal.tsx",
			tsName:  "CleanupArchive",
			tsKeys:  keySet("id", "at", "reason", "sessions", "branches"),
			tsOnly:  map[string]string{},
			goOnly: map[string]string{
				"worktrees": "【穴】cleanupManifest が出しているが Console の CleanupArchive に宣言が無い（掃除で消したワークツリーの一覧が画面に出ない）。",
			},
		},

		// ブラウザ添付の状態。
		{
			name:    "browserx.BrowserAttachmentResponse",
			goType:  reflect.TypeOf(browserx.BrowserAttachmentResponse{}),
			binding: browserAttachmentBinding,
			tsPath:  "../../console/src/features/browser/attachmentController.ts",
			tsName:  "BrowserAttachmentStatus",
			tsKeys:  keySet("id", "state", "title", "url", "expiresAt", "controlMode", "handoff"),
			tsOnly:  map[string]string{},
			goOnly: map[string]string{
				"openUrl": "【穴】添付を開く URL。Console の BrowserAttachmentStatus に宣言が無い。",
				"viewer":  "【穴】同上。",
			},
		},

		// メモリ取り込みの下見。
		// 🔴 **AST 経路**——`memoryImportPreview` は internal/memoryx の**非公開**型。
		{
			name:    "memoryImportPreview",
			goPath:  "internal/memoryx/memory_import.go",
			goName:  "memoryImportPreview",
			binding: memoryImportPreviewBinding,
			tsPath:  "../../console/src/features/settings/memory/memoryTypes.ts",
			tsName:  "ImportPreview",
			tsKeys: keySet("importId", "format", "head", "headTs", "snapshots", "kinds",
				"projects", "unavailable", "rejected", "secrets", "secretScanFailed"),
			tsOnly: map[string]string{},
			goOnly: map[string]string{
				"ref": "【穴】取り込み元の ref。Console の ImportPreview に宣言が無い。",
				// 🔴 `secretScanFailed` の免除は**この PR で外した**。#339 で入れた
				// 「免除の寿命の逆検査」が実際に「もう要らない」を検出したのが直す動機で、
				// この家系がその仕組みの最初の実例になる（穴を塞いだ瞬間に免除表が縮む）。
			},
		},

		// ブラウザのページ 1 枚。
		// 🔴 **AST 経路**——`browserPageResponse` は internal/browserx の**非公開**型。
		{
			name:    "browserPageResponse",
			goPath:  "internal/browserx/browser_types.go",
			goName:  "browserPageResponse",
			binding: browserPageBinding,
			tsPath:  "../../console/src/features/browser/controller.ts",
			tsName:  "BrowserPageResult",
			tsKeys:  keySet("id", "port", "url", "state"),
			tsOnly:  map[string]string{},
			goOnly: map[string]string{
				"title": "【穴】ページのタイトルを返しているが、Console の BrowserPageResult に宣言が無い。",
			},
		},

		// 転写の 1 断片（mirror が描く最小単位）。
		{
			name:    "transcript.Part",
			goType:  reflect.TypeOf(transcript.Part{}),
			binding: transcriptPartBinding,
			tsPath:  "../../console/src/features/mirror/transcript/types.ts",
			tsName:  "Part",
			tsKeys: keySet("kind", "text", "tool", "info", "cause", "output", "prompt",
				"agentType", "status", "model", "file", "edits", "verb", "questions",
				"answer", "declined", "plan", "qid", "files", "caption", "stderr"),
			tsOnly: map[string]string{
				"stderr": "【穴】Console の Part が宣言しているが transcript.Part は出さない。Console 側の実読みの有無は未調査。",
			},
			goOnly: map[string]string{},
		},

		// 転写の 1 ターン。
		{
			name:    "transcript.Turn",
			goType:  reflect.TypeOf(transcript.Turn{}),
			binding: transcriptTurnBinding,
			tsPath:  "../../console/src/features/mirror/transcript/types.ts",
			tsName:  "Turn",
			tsKeys: keySet("role", "text", "ts", "endTs", "idx", "anchorId", "pending",
				"queued", "source", "peerFrom", "parts", "sidechain", "compact", "bash",
				"cmd", "model", "effort", "ctxWindow", "branch", "cwd", "inTok", "outTok",
				"cacheRead", "cacheCreate"),
			tsOnly: map[string]string{
				// 🔴 4 件とも**穴ではない**可能性が高い——Console 側で組み立てる描画用の値に見える
				// （bash/cmd は「! シェルコマンドのブロック」、pending/queued は送信状態）。
				// **ただし実読みまでは追っていない**ので、そう書いてある以上の主張はしない。
				"bash":    "Console 側で組み立てる描画用の値に見える（! シェルコマンドのブロック）。transcript.Turn は出さない。実読みは未調査",
				"cmd":     "同上。",
				"pending": "送信状態（未送信）を Console 側が持つ値に見える。transcript.Turn は出さない。実読みは未調査",
				"queued":  "同上（送信待ち）。",
			},
			goOnly: map[string]string{},
		},
	}
}

var transcriptPartBinding = map[string]string{
	"Kind": "kind", "Text": "text", "Tool": "tool", "Info": "info", "Cause": "cause",
	"Output": "output", "Prompt": "prompt", "AgentType": "agentType", "Status": "status",
	"Model": "model", "Questions": "questions", "Answer": "answer", "Declined": "declined",
	"Plan": "plan", "File": "file", "Edits": "edits", "Verb": "verb", "Files": "files",
	"Caption": "caption", "QID": "qid",
}

var transcriptTurnBinding = map[string]string{
	"Role": "role", "Parts": "parts", "Text": "text", "Source": "source",
	"PeerFrom": "peerFrom", "Model": "model", "Effort": "effort", "CtxWindow": "ctxWindow",
	"Sidechain": "sidechain", "Branch": "branch", "Cwd": "cwd", "InTok": "inTok",
	"OutTok": "outTok", "CacheRead": "cacheRead", "CacheCreate": "cacheCreate",
	"TS": "ts", "Idx": "idx", "AnchorID": "anchorId", "EndTS": "endTs", "Compact": "compact",
}

var memoryImportPreviewBinding = map[string]string{
	"ImportID": "importId", "Format": "format", "Ref": "ref", "Head": "head",
	"HeadTs": "headTs", "Snapshots": "snapshots", "Kinds": "kinds", "Projects": "projects",
	"Unavailable": "unavailable", "Rejected": "rejected", "Secrets": "secrets",
	"SecretScanFailed": "secretScanFailed",
}

var browserPageBinding = map[string]string{
	"ID": "id", "Port": "port", "URL": "url", "Title": "title", "State": "state",
}

var chatMessageBinding = map[string]string{
	"Role": "role", "Content": "content", "TS": "ts", "Agent": "agent", "Model": "model",
	"Steps": "steps", "Session": "session", "Instr": "instr", "Delivered": "delivered",
	"NoticeKey": "notice_key", "NoticeArgs": "notice_args", "ReportKind": "report_kind",
	"ReportReason": "report_reason",
}

var branchInfoBinding = map[string]string{
	"Name": "name", "Remote": "remote", "Unix": "unix", "Date": "date",
	"Subject": "subject", "Current": "current", "WorktreePath": "worktree_path",
}

var cleanupManifestBinding = map[string]string{
	"ID": "id", "At": "at", "Reason": "reason", "Sessions": "sessions",
	"Branches": "branches", "Worktrees": "worktrees",
}

var browserAttachmentBinding = map[string]string{
	"ID": "id", "State": "state", "Title": "title", "URL": "url", "OpenURL": "openUrl",
	"ExpiresAt": "expiresAt", "Viewer": "viewer", "ControlMode": "controlMode", "Handoff": "handoff",
}

func TestContractFamilies(t *testing.T) {
	fams := agentContractFamilies()
	// 🔴 走査の母数を見張る（#320 型）。家系が黙って消えたらここで気付く。
	if len(fams) != 8 {
		t.Fatalf("家系が %d 件しかない＝表から落ちている（足したなら本数も直すこと）", len(fams))
	}
	for _, f := range fams {
		t.Run(f.name, func(t *testing.T) { checkContractFamily(t, f) })
	}
}

// ===== 共有機構ここから（control-plane と workspace/agent で byte 一致・下の検査が見張る）=====
// 🔴 **両モジュールで共有する道具は、必ずこの区間の中に置くこと。**
// 検査が守っているのは「この区間」であって「ファイル」ではない——**区間の外に足した共有ヘルパは
// 片側だけに在っても緑のまま通る**（#346 のレビュワーが実測）。見ない場所を作った以上、
// どこが見られているかを人に伝える文言が要る（免除表に理由を書くのと同じ構造）。
// contractFamily は 1 家系分の契約。
type contractFamily struct {
	name string // 家系名（エラーメッセージ用）

	// Go 側のワイヤ型。**経路は 2 つあり、選び方は機械的**（下の goStructFieldsFromSource の
	// コメント参照）: 同じパッケージから届くなら goType（reflect）、
	// 別パッケージの非公開型なら goPath + goName（go/ast）。**両方を埋めてはいけない。**
	goType  reflect.Type
	goPath  string            // reflect で届かないときだけ。宣言ファイルへの相対パス
	goName  string            // 同上。struct 名
	binding map[string]string // Go フィールド名 → json キー（①の原本）

	tsPath string          // Console の手書き型の在り処
	tsName string          // TS の interface 名
	tsKeys map[string]bool // TS 側のキー集合（②の原本）

	// 免除。🔴 **増やすときは必ず理由を書くこと。ここは「まだ直していない」を隠す場所ではない。**
	tsOnly map[string]string // TS に在って Go が出さない
	goOnly map[string]string // Go が出すが TS に無い
}

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// checkContractFamily は 1 家系に ①bind ②scan ③match を当てる。
//
// 🔴 **原本の取り方に意味がある**（#339 で片肺だった反省）:
//   - ③ が読む Go 側は **reflect（実際の struct）**——手書きの表を材料にすると、
//     構造体を直しても③に届かず、免除の寿命の逆検査が鳴らなくなる。
//   - ③ が読む TS 側は **実際に走査した結果**——表を材料にすると、TS を直しても③に届かない。
//   - 表（binding / tsKeys）は **①②が守るもので、③が読むものではない。**
func checkContractFamily(t *testing.T, f contractFamily) {
	t.Helper()

	// --- ① Go フィールド名 ↔ json キーの結び付き ---
	goFields := contractGoFields(t, f)
	for name, want := range f.binding {
		got, ok := goFields[name]
		if !ok {
			t.Errorf("%s に フィールド %s が無い（消えたか改名された）", f.name, name)
			continue
		}
		if got != want {
			t.Errorf("%s.%s の json タグが %q（表は %q）"+
				"——同じ型のフィールド同士でタグを入れ替えると、ワイヤのキー集合は変わらないまま値だけが入れ替わる",
				f.name, name, got, want)
		}
	}
	for name, key := range goFields {
		if _, ok := f.binding[name]; !ok {
			t.Errorf("%s.%s (json:%q) が表に無い——足したなら表にも足すこと（Console 側の型にも要るはず）", f.name, name, key)
		}
	}

	// --- ② TS 側のキー集合を表に固定する（走査が壊れたことを捕まえるのはここだけ）---
	scanned := consoleInterfaceFields(t, f.tsPath, f.tsName)
	for k := range f.tsKeys {
		if !scanned[k] {
			t.Errorf("%s: %s の %q が表に在るのに TS 側で見つからない。原因は 2 つのどちらか——"+
				"(a) キーを意図して消した → tsKeys の表と免除表も直すこと（同じ実行の下のほうに"+
				"「免除はもう要らない」が出ているはず）／(b) 走査が壊れた → 合成標本の対照"+
				"（TestTSInterfaceFieldsParser）も一緒に赤くなっているはず",
				f.name, f.tsName, k)
		}
	}
	for k := range scanned {
		if !f.tsKeys[k] {
			t.Errorf("%s: %s に %q が増えている——表にも足すこと（③の判定はここを通らない）", f.name, f.tsName, k)
		}
	}

	// --- ③ TS ↔ Go のキー集合（免除つき）---
	goKeys := map[string]bool{}
	for _, k := range goFields {
		goKeys[k] = true
	}
	var tsOnly, goOnly []string
	for k := range scanned {
		if !goKeys[k] {
			tsOnly = append(tsOnly, k)
		}
	}
	for k := range goKeys {
		if !scanned[k] {
			goOnly = append(goOnly, k)
		}
	}
	sort.Strings(tsOnly)
	sort.Strings(goOnly)
	for _, k := range tsOnly {
		if _, ok := f.tsOnly[k]; !ok {
			t.Errorf("%s: %s が %q を宣言しているが %s は出さない"+
				"——Console は常に undefined を読む（optional なので型検査は鳴らない）", f.name, f.tsName, k, f.name)
		}
	}
	for _, k := range goOnly {
		if _, ok := f.goOnly[k]; !ok {
			t.Errorf("%s: %s が %q を出すが %s に宣言が無い——Console からは型の上で見えない",
				f.name, f.name, k, f.tsName)
		}
	}

	// --- 免除の寿命（4 方向。「揃った」だけでなく「消えた」も見る）---
	for k, why := range f.tsOnly {
		if goKeys[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s が出すようになった", f.name, k, why, f.name)
		}
		if !scanned[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s から消えた（両側に無いキーの免除は理由ごと嘘になる）",
				f.name, k, why, f.tsName)
		}
	}
	for k, why := range f.goOnly {
		if !goKeys[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s が出さなくなった（両側に無いキーの免除は理由ごと嘘になる）",
				f.name, k, why, f.name)
		}
		if scanned[k] {
			t.Errorf("%s: 免除 %q (%s) はもう要らない——%s が宣言するようになった", f.name, k, why, f.tsName)
		}
	}
}

// contractGoFields は家系の経路に従って「Go フィールド名 → json キー」を取る。
func contractGoFields(t *testing.T, f contractFamily) map[string]string {
	t.Helper()
	if (f.goType == nil) == (f.goPath == "") {
		t.Fatalf("%s: goType と goPath はどちらか一方だけを埋めること（両方 or どちらも空）", f.name)
	}
	if f.goPath != "" {
		return goStructFieldsFromSource(t, f.goPath, f.goName)
	}
	out, err := reflectJSONFields(f.goType, 0)
	if err != nil {
		t.Fatalf("%s: %v", f.name, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s から json タグを 1 つも読めなかった＝この検査が無言化している", f.goType)
	}
	return out
}

// reflectJSONFields は struct の「Go フィールド名 → json キー」を返す。
//
// 🔴 **埋め込み（無名フィールド）は encoding/json と同じく昇格する。**
// 素朴に `Tag.Get("json")` が空なら飛ばす書き方だと、**タグの無い埋め込みごと飛ばして
// 昇格したキーを丸ごと落とす**——実例 `usageHourPoint`（control-plane/usage_hourly.go:55）は
// `store.UsageHourCounters` を埋め込んでおり、飛ばすと **8 キーのうち 7 つが消えて**
// 「TS のみ」が 7 件に化ける。**浅く読んで偽の赤を出すのが、この面のいちばん怖い壊れ方。**
//
// 🔴 **追えない形に出会ったら浅い結果を返さずに落ちる**（AST 経路と同じ規律）:
// 2 段以上の埋め込みと、キーがぶつかる昇格は error にしてある。
// **json の昇格規則はもっと複雑**（深さ優先・同深さの衝突は両方消えるなど）なので、
// **近似で通さず、想定外はすべて落とす。**
func reflectJSONFields(rt reflect.Type, depth int) (map[string]string, error) {
	if rt.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%s は struct ではない", rt)
	}
	out := map[string]string{}
	for i := 0; i < rt.NumField(); i++ {
		fl := rt.Field(i)
		tag := fl.Tag.Get("json")
		if fl.Anonymous && tag == "" {
			// タグの無い埋め込み＝昇格。1 段だけ許す。
			et := fl.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() != reflect.Struct {
				// 🔴 **公開された非 struct の埋め込みは、json が「型名」をキーにして出す**
				// （`MyDur int64` を埋め込むと `{"MyDur":0}`）。飛ばすと**取りこぼし**になる。
				// 非公開なら json も出さないが、**出す／出さないを型名の公開性で判断するのは
				// この走査の役目ではない**ので、まとめて落とす。
				if fl.IsExported() {
					return nil, fmt.Errorf("%s に公開された非 struct の埋め込みが在る（%s %s）"+
						"——json は型名をキーにして出すが、この走査は追わない。"+
						"この家系は埋め込みを解いた型を指すこと", rt, fl.Name, et)
				}
				continue
			}
			if depth > 0 {
				return nil, fmt.Errorf("%s に 2 段以上の埋め込みが在る（%s）"+
					"——json の昇格規則は深さ優先で衝突の扱いも複雑なので、近似で通さない。"+
					"この家系は埋め込みを解いた型を指すか、経路を作り直すこと", rt, et)
			}
			sub, err := reflectJSONFields(et, depth+1)
			if err != nil {
				return nil, err
			}
			for k, v := range sub {
				if err := putJSONField(out, rt, k, v); err != nil {
					return nil, err
				}
			}
			continue
		}
		if tag == "-" || (tag == "" && !fl.IsExported()) {
			continue // json:"-" と非公開はワイヤに出ない
		}
		// 🔴 **タグ無しの公開フィールドは、json が「Go のフィールド名」をキーにして出す。**
		// 「タグが空なら飛ばす」と書くと取りこぼす（差分試験で実測）。
		name := splitJSONName(tag)
		if name == "" {
			name = fl.Name
		}
		if err := putJSONField(out, rt, fl.Name, name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- 別パッケージの非公開型を読む経路（go/ast）---
//
// 🔴 **どちらの経路を使うかは機械的に決まる。迷ったら分岐条件を読むこと。**
//
//	同じパッケージから届く（package main の型・別パッケージの公開型） → reflect（goType）
//	別パッケージの**非公開**型                                        → go/ast（goPath + goName）
//
// reflect で届かない型のためだけの経路である。**届くなら必ず reflect を使う**——
// reflect は「実際の型」を見るが、AST は**ソースの見た目**しか見ないので、
// 埋め込み・型別名・生成コードのぶんだけ弱い。
//
// 🔴 **AST が追えない構文に出会ったら、浅い結果を返さずに落ちること。**
// 「今日の入力には埋め込みが 0 件だから AST と reflect は等価」という実測は
// **今日の入力に対してだけ**成立する。**次に誰かが埋め込みを足した日に黙って浅く読む**ので、
// 埋め込みを見つけたら Fatal にしてある。パスが移送で変わったときも同じ（Skip で黙らせない）。

// goStructFieldsFromSource は parseGoStructFields の薄い包み。読めなければ **Fatal**。
// （Skip で黙らせない。移送でパスが変わったら家系表の goPath を直すこと。）
func goStructFieldsFromSource(t *testing.T, path, name string) map[string]string {
	t.Helper()
	out, err := parseGoStructFields(path, name)
	if err != nil {
		t.Fatalf("%v——移送でパスが変わったなら家系表の goPath を直すこと（Skip で黙らせない）", err)
	}
	return out
}

// parseGoStructFields は <path> の `type <name> struct` を読み、
// 「Go フィールド名 → json キー」を返す。**追えない構文に出会ったら浅い結果ではなく error。**
//
// 📌 **error を返す形にしてあるのは、落ちること自体を対照で確かめられるようにするため**
// （TestGoStructFieldsFromSourceGuards）。Fatal のままだと「落ちるはず」を検査できない。
func parseGoStructFields(path, name string) (map[string]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("%s を読めない: %v", path, err)
	}
	var st *ast.StructType
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != name {
			return true
		}
		if s, ok := ts.Type.(*ast.StructType); ok {
			st = s
		}
		return false
	})
	if st == nil {
		return nil, fmt.Errorf("%s に type %s struct が見つからない＝この検査が無言化している", path, name)
	}
	out := map[string]string{}
	for _, fl := range st.Fields.List {
		// 🔴 埋め込み（無名フィールド）。AST では中身を追えないので、
		// **浅い結果を返さずに落ちる**。reflect なら見える差なので、
		// 埋め込みが要るようになったらこの家系は reflect 経路へ移すこと。
		if len(fl.Names) == 0 {
			return nil, fmt.Errorf("%s の %s に埋め込みフィールドが在る（%s）"+
				"——AST では埋め込み先の json タグを追えない。浅く読むと「TS のみ」の見落としと"+
				"「Go のみ」の偽の赤が同時に出るので、この家系は reflect 経路へ移すこと",
				path, name, exprString(fl.Type))
		}
		if fl.Tag == nil {
			continue
		}
		tv, err := strconv.Unquote(fl.Tag.Value)
		if err != nil {
			return nil, fmt.Errorf("%s の %s: タグを読めない (%s): %v", path, name, fl.Tag.Value, err)
		}
		jt := reflect.StructTag(tv).Get("json")
		if jt == "" || jt == "-" {
			continue
		}
		key := splitJSONName(jt)
		if key == "" {
			continue
		}
		for _, id := range fl.Names {
			if id.IsExported() {
				out[id.Name] = key
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s の %s から json タグを 1 つも読めなかった＝この検査が無言化している", path, name)
	}
	return out, nil
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	}
	return "?"
}

func splitJSONName(tag string) string {
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i]
		}
	}
	return tag
}

// tsProbeFixture は走査の陽性対照に使う合成標本。**実際に踏んだ／踏みかけた形だけ**を並べてある。
//
// 📌 別ファイル（testdata/*.ts）ではなく定数に畳んである理由: この 1 枚は検査と一体で、
// 分けると移送で孤児になるうえ、所有権の単位も分かれる（`console/src/types/*` とは別物）。
const tsProbeFixture = `
// ① 1 行 1 フィールド（Session が実際にこの形。ここだけ通っても意味がない）
export interface OnePerLine {
  a1: string;
  a2?: number;
  a3: boolean;
}

// ② 一部の行だけ複数キー。🔴 これが最も危ない —— 行単位の走査は b11 を落とすが、
// 総数は 10 を超えるので「フィールドが少なすぎる」Fatal に落ちず、黙って穴が開く。
export interface Mixed {
  b01: string;
  b02: string;
  b03: string;
  b04: string;
  b05: string;
  b06: string;
  b07: string;
  b08: string;
  b09: string;
  b10: string; b11?: number;
}

// ③ 入れ子の 1 行オブジェクト。🔴 行を「;」で割る直し方をすると、
// nested の中の name / display をこの型の直下のキーとして数えてしまう（測定器で実際に踏んだ）。
export interface Nested {
  n1: string;
  n2?: { name: string; display?: string }[];
  n3: boolean;
}

// ④ コメント・文字列リテラルに 「:」「;」「{」「}」が入る形（深さと文頭の判定を狂わせにくる）
export interface Tricky {
  // これはコメント: セミコロン; と波括弧 { } を含む
  t1: "a;b" | "c:{d}" | string;
  /* ブロックコメント: t9: string; ← これは拾ってはいけない */
  t2?: string;
  t3: string;
}

// ⑤-a type 別名（interface と同じだけ普通に使われる。UptimePoint が実際にこの形）
export type AliasShape = {
  al1: string;
  al2?: number;
  al3: boolean;
};

// ⑤ 名前が前方一致する別の型（Session と SessionContextUsage の関係）
export interface Pre {
  p1: string;
  p2: string;
  p3: string;
}

export interface PreExtra {
  x1: string;
  x2: string;
  x3: string;
}
`

// TestTSInterfaceFieldsParser は**走査そのものの陽性対照**。
//
// 🔴 この検査が要る理由: `Session` は 1 行 1 フィールドなので、**走査が壊れていても
// Session だけは通ってしまう。**案 A を他の家系へ写したとき、`a: string; b?: number;` の形が
// 1 つでもあると、**取りこぼしたキーが「TS に無い」に化けて偽の赤／穴の見落としになる。**
// 合成標本（上の tsProbeFixture）は、実際に踏んだ形だけを並べてある。
//
// 🔥 **レビュワーが #343 で実測**: 走査を壊す変異（`;` を文の区切りから外す／深さ判定を外す）を
// 当てても、①`TestSessionWireFieldBinding` と ②`TestSessionWireMatchesConsoleType` は
// **どちらも PASS のまま**だった——**実入力の `Session` が 1 行 1 フィールドなので、
// 走査が壊れても本番の突き合わせは何も言わない。**この対照だけが鳴る。
// **横展開する家系にも必ず合成標本を付けること**（実入力は易しすぎて対照にならない）。
func TestTSInterfaceFieldsParser(t *testing.T) {
	src := tsProbeFixture
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"OnePerLine", []string{"a1", "a2", "a3"}},
		// 同じ行に 2 キー。行単位の走査は b11 を落とす（総数 11 なので Fatal には落ちない）。
		{"Mixed", []string{"b01", "b02", "b03", "b04", "b05", "b06", "b07", "b08", "b09", "b10", "b11"}},
		// 入れ子の name / display を拾ってはいけない。
		{"Nested", []string{"n1", "n2", "n3"}},
		// コメント／文字列の中の `t9` を拾ってはいけない。
		{"Tricky", []string{"t1", "t2", "t3"}},
		// 前方一致する別の型を掴んではいけない（Pre が PreExtra を拾わない）。
		// type 別名も interface と同じに読めること（読めないと家系ごと Fatal する）。
		{"AliasShape", []string{"al1", "al2", "al3"}},
		{"Pre", []string{"p1", "p2", "p3"}},
		{"PreExtra", []string{"x1", "x2", "x3"}},
	} {
		got, err := tsInterfaceFields(src, tc.name)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		want := map[string]bool{}
		for _, k := range tc.want {
			want[k] = true
		}
		for k := range want {
			if !got[k] {
				t.Errorf("%s: %q を落としている（走査が壊れている）", tc.name, k)
			}
		}
		for k := range got {
			if !want[k] {
				t.Errorf("%s: %q を余計に拾っている（入れ子・コメント・文字列を巻き込んでいる）", tc.name, k)
			}
		}
	}

	// TS のテンプレートリテラル型（バッククォート）でも深さを見失わないこと。
	// 上の標本は raw string なので、この 1 例だけ通常の文字列で組む。
	tmpl := "export interface Tmpl {\n  m1: `a;b{c}`;\n  m2: string;\n  m3: string;\n}\n"
	if got, err := tsInterfaceFields(tmpl, "Tmpl"); err != nil {
		t.Errorf("Tmpl: %v", err)
	} else if len(got) != 3 || !got["m1"] || !got["m2"] || !got["m3"] {
		t.Errorf("Tmpl: テンプレートリテラルで深さを見失っている: %v", got)
	}

	// 無いものを探したら Fatal 相当のエラーになること（Skip や空返しで黙らない）。
	if _, err := tsInterfaceFields(src, "NoSuchInterface"); err == nil {
		t.Error("存在しない interface でエラーにならない＝この検査が無言化しうる")
	}
}

// consoleInterfaceFields は TS の `interface <name> { ... }` の**深さ 1 の**フィールド名を返す。
func consoleInterfaceFields(t *testing.T, path, name string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Console の型を読めない (%s): %v"+
			"——移送でパスが変わったなら consoleSessionTS を直すこと（Skip で黙らせない）", path, err)
	}
	out, err := tsInterfaceFields(string(b), name)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	// 🔴 **件数の下限は「0 件」しか見ない。これは抜けではなく、意図してこうしてある。**
	//
	// 以前は「表に固定したキー数」を下限にして Fatal していたが、**診断が誤った方向を指した**——
	// TS からキーが 1 つ消えると必ず Fatal し、文言は「走査が壊れている」。**実際の原因は
	// 「キーが意図して消された」で走査は無傷**であり、しかも Fatal が後続を止めるので
	// **「免除を外せ」という正しい指示が出なかった**（死んだ TS 宣言を消す作業がこの経路を通る）。
	//
	// 件数ガードが要らない理由: **呼び出し側の②（キー集合を表と突き合わせる）が、同じ面を
	// 「どのキーが」まで含めて見ている。**走査が痩せれば、読めなかったキーが②で名指しで赤くなり、
	// ③でも「Go のみ」として出る。**件数は情報を足していない**どころか、キーが 6〜7 個の
	// 小さい家系（SsmHost / SsmProfileEntry / GitOAuthApp）を誤って Fatal させていた。
	// **下限が無いのを見て足しに来ないこと。**
	//
	// ⚠️ 走査の壊れ全般を捕まえるのは②でも③でもなく **TestTSInterfaceFieldsParser（合成標本）**。
	// 実入力はどの家系も 1 行 1 フィールドで、壊れた枝を通らない（実測）。
	if len(out) == 0 {
		t.Fatalf("interface %s のフィールドを 1 つも読めなかった＝走査が無言化している", name)
	}
	return out
}

// tsInterfaceFields は TS の interface 本体を 1 文字ずつ辿り、**深さ 1 の**フィールド名を返す。
//
// 🔴 **行単位で「1 行 1 キー」を取ってはいけない。**`a: string; b?: number;` のように
// 同じ行にキーが並ぶ形を取りこぼす。取りこぼしても総数は 10 を超えるので上の Fatal に落ちず、
// **「TS のみ」の検出漏れ（＝穴の見落とし）と「Go のみ」の誤検出（＝偽の赤）を同時に起こす。**
//
// 🔴 **だからといって行を `;` で割るのも誤り。**入れ子の 1 行オブジェクトを巻き込む——
// 実例 `sessions?: { name: string; display?: string }[];` を `;` で割ると
// `name` / `display` を**この型の直下のキーとして数えてしまう**（測定器で実際に踏んだ）。
// **深さを見るしかない。**
func tsInterfaceFields(src, name string) (map[string]bool, error) {
	start := -1
	// `interface X { … }` と `type X = { … }` の両方を見る。
	// 🔴 **`type` 別名を見ないと、その家系だけ「見つからない」で Fatal する**——
	// 実例 `UptimePoint`（console/src/features/usage/uptime.ts:11）は `export type … = { … }`。
	// TS ではどちらの書き方も同じだけ普通なので、片方しか見ない走査は家系を選べない。
	for _, pre := range []string{
		"export interface " + name, "interface " + name,
		"export type " + name, "type " + name,
	} {
		for i := 0; i+len(pre) <= len(src); i++ {
			if !strings.HasPrefix(src[i:], pre) {
				continue
			}
			if i > 0 && isTSIdentRune(rune(src[i-1])) {
				continue // 別の名前の末尾に一致しただけ（SessionFoo など）
			}
			// 宣言名の直後は識別子の続きであってはならない（Session と SessionContextUsage）
			if j := i + len(pre); j < len(src) && isTSIdentRune(rune(src[j])) {
				continue
			}
			if k := strings.IndexByte(src[i:], '{'); k >= 0 {
				start = i + k
			}
			break
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("interface / type %s が見つからない＝この検査が無言化している", name)
	}

	out := map[string]bool{}
	depth := 0
	stmt := true // 「文の頭」＝ここから始まる識別子だけがフィールド名になりうる
	for i := start; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			stmt = true
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			if k := strings.Index(src[i+2:], "*/"); k >= 0 {
				i += 2 + k + 1
			} else {
				i = len(src)
			}
			continue
		case c == '"' || c == '\'' || c == '`':
			q := c
			for i++; i < len(src); i++ {
				if src[i] == '\\' {
					i++
					continue
				}
				if src[i] == q {
					break
				}
			}
			stmt = false
			continue
		case c == '{':
			depth++
			stmt = true
			continue
		case c == '}':
			depth--
			stmt = true
			if depth == 0 {
				return out, nil
			}
			continue
		case c == ';' || c == ',' || c == '\n':
			stmt = true
			continue
		case c == ' ' || c == '\t' || c == '\r':
			continue
		}
		if depth != 1 || !stmt {
			stmt = false
			continue
		}
		// 深さ 1 の文頭。識別子を読み、`?` を挟んで `:` が続けばフィールド名。
		j := i
		for j < len(src) && isTSIdentRune(rune(src[j])) {
			j++
		}
		if j > i {
			k := j
			for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
				k++
			}
			if k < len(src) && src[k] == '?' {
				k++
				for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
					k++
				}
			}
			if k < len(src) && src[k] == ':' {
				out[src[i:j]] = true
			}
		}
		if j > i {
			i = j - 1
		}
		stmt = false
	}
	return nil, fmt.Errorf("interface %s の本体が閉じていない＝走査が壊れている", name)
}

func isTSIdentRune(r rune) bool {
	return r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// putJSONField は 1 件足す。🔴 **重複判定は「Go フィールド名」ではなく「json 名」で行う。**
// `encoding/json` の衝突規則は json 名で決まる——**Go 名が違っても json 名が同じなら、
// json は（同深さなら）どちらも出さない。**Go 名で見ていると
// **「json は出さないのにこちらは出す」**という最悪の向きの食い違いになる
// （レビュワーが json.Marshal との差分試験で実測。#350 参考 1）。
func putJSONField(out map[string]string, rt reflect.Type, field, jsonName string) error {
	for f, j := range out {
		if j == jsonName {
			return fmt.Errorf("%s: json 名 %q が %s と %s で重なる"+
				"——encoding/json は同深さの衝突でどちらも出さない。近似で通さない", rt, jsonName, f, field)
		}
	}
	if _, dup := out[field]; dup {
		return fmt.Errorf("%s: フィールド名 %s が重複している", rt, field)
	}
	out[field] = jsonName
	return nil
}

// TestReflectJSONFieldsMatchesEncodingJSON は **仕様の実装そのものと差分試験する**。
//
// 🔥 `reflectJSONFields` の目標は「`encoding/json` の昇格規則に合わせる」ことなので、
// **目標そのものが手元で動く**——机上で規則を読むより、合成型を両方に通して出力を比べるほうが
// 速くて強い。**レビュワーがこの方法で 3 件の食い違いを見つけた**（#350 参考 1）。
//
// 期待は 2 通りだけ書ける: **json と同じキー集合になる**か、**error（＝安全側に倒す）**か。
// 🔴 **「json は出さないのにこちらは出す」は許さない**——それは検査が実在しないキーを
// 契約に載せることで、免除表に偽の穴が生える。
func TestReflectJSONFieldsMatchesEncodingJSON(t *testing.T) {
	type inner struct {
		A string `json:"a"`
		B int    `json:"b"`
	}
	type innerDup struct {
		P string `json:"p"`
	}
	type deep2 struct{ inner }
	type MyDur int64

	for _, tc := range []struct {
		name    string
		v       any
		wantErr bool // true = 追えない形なので error に倒す（json より狭くてよい）
	}{
		{"① 素の 1 段埋め込み", struct {
			Hour string `json:"hour"`
			inner
		}{}, false},
		{"② タグ付き埋め込み（入れ子になる）", struct {
			Hour  string `json:"hour"`
			Inner inner  `json:"inner"`
		}{}, false},
		// ③ は同一 struct 内で json 名が重なる形。
		// 📌 **この形はソースに書くと `go vet`（structtag）が弾く**ので、実行時に組んで渡す。
		// ＝**同一 struct 内の重複は vet が既に守っている。**この走査の検査が要るのは
		// **④⑤ の「昇格したキーと外側／別の埋め込みが重なる」形**で、
		// **そちらは 2 つの struct にまたがるので vet は見ない。**
		{"③ 別の Go 名・同じ json 名（実行時に組む）", reflect.New(reflect.StructOf([]reflect.StructField{
			{Name: "X", Type: reflect.TypeOf(""), Tag: `json:"a"`},
			{Name: "Y", Type: reflect.TypeOf(""), Tag: `json:"a"`},
		})).Elem().Interface(), true},
		{"④ 同じ json 名が外側と昇格でぶつかる", struct {
			A string `json:"a"`
			inner
		}{}, true},
		{"⑤ 同深さの 2 つの埋め込みが同じ json 名", struct {
			innerDup
			Other struct{} `json:"-"`
		}{}, false}, // 衝突が無いので一致する側
		{"⑥ 2 段の埋め込み", struct {
			Z string `json:"z"`
			deep2
		}{}, true},
		// ⚠️ ポインタ埋め込みは **nil だと json がフィールドを出さない**ので、
		// 非 nil の値で比べる（対照の作りの問題であって、規則の違いではない）。
		{"⑦ ポインタ埋め込み（非 nil）", struct {
			Hour string `json:"hour"`
			*inner
		}{Hour: "h", inner: &inner{}}, false},
		{"⑨ 公開された非 struct の埋め込み", struct {
			MyDur
			C string `json:"c"`
		}{}, true},
		{"⑩ タグ無しの公開フィールド（json は Go 名で出す）", struct {
			Plain string
			C     string `json:"c"`
		}{}, false},
	} {
		got, err := reflectJSONFields(reflect.TypeOf(tc.v), 0)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: error になるはずが %v を返した（json との食い違いを安全側に倒せていない）", tc.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 追えるはずの形で error: %v", tc.name, err)
			continue
		}
		// json.Marshal が実際に出すキー集合と比べる。
		b, mErr := json.Marshal(tc.v)
		if mErr != nil {
			t.Errorf("%s: marshal: %v", tc.name, mErr)
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Errorf("%s: unmarshal: %v", tc.name, err)
			continue
		}
		mine := map[string]bool{}
		for _, jn := range got {
			mine[jn] = true
		}
		for k := range raw {
			if !mine[k] {
				t.Errorf("%s: json は %q を出すのに走査が落としている（取りこぼし）", tc.name, k)
			}
		}
		for k := range mine {
			if _, ok := raw[k]; !ok {
				t.Errorf("%s: 走査が %q を出すのに json は出さない"+
					"——契約に実在しないキーが載り、免除表に偽の穴が生える", tc.name, k)
			}
		}
	}
}

// TestReflectJSONFieldsEmbedding は **reflect 経路の埋め込みの扱いの陽性対照**。
//
// 🔴 素朴に「json タグが空なら飛ばす」と書くと、**タグの無い埋め込みごと飛ばして
// 昇格したキーを丸ごと落とす**。実入力（usageHourPoint）は 1 段の埋め込みなので、
// **昇格が壊れても「TS のみ」が 7 件出るだけで、原因が埋め込みだとは読めない。**
// だから合成型で、昇格すること・追えない形では落ちることを固定する。
func TestReflectJSONFieldsEmbedding(t *testing.T) {
	type inner struct {
		A string `json:"a"`
		B int    `json:"b,omitempty"`
	}
	type deeper struct{ inner }
	type flat struct {
		X string `json:"x"`
		Y string // タグ無しの公開フィールド＝json は "Y" で出す
		z string // 非公開＝出ない
	}

	// ① 1 段の埋め込みは昇格する。
	type promoted struct {
		Hour string `json:"hour"`
		inner
	}
	got, err := reflectJSONFields(reflect.TypeOf(promoted{}), 0)
	if err != nil {
		t.Fatalf("1 段の埋め込みを読めない: %v", err)
	}
	if len(got) != 3 || got["Hour"] != "hour" || got["A"] != "a" || got["B"] != "b" {
		t.Fatalf("昇格の結果が違う: %v（外側 1 ＋ 昇格 2 のはず）", got)
	}

	// ② タグ無しの**公開**フィールドは json が Go 名をキーにして出すので、こちらも出す。
	// 非公開はワイヤに出ないので落とす。
	// 📌 **この対照は当初「タグ無しは落とす」と書いていて誤っていた**——
	// json.Marshal との差分試験（TestReflectJSONFieldsMatchesEncodingJSON）が正で、
	// **手で書いた期待値のほうが間違っていた。**規則の正は常に標準ライブラリ側にある。
	got, err = reflectJSONFields(reflect.TypeOf(flat{}), 0)
	if err != nil || len(got) != 2 || got["X"] != "x" || got["Y"] != "Y" {
		t.Fatalf("タグ無しフィールドの扱いが違う: %v (%v)", got, err)
	}

	// ③ 🔴 2 段以上の埋め込みは**浅い結果ではなく error**。
	type twoLevel struct {
		Z string `json:"z"`
		deeper
	}
	if _, err := reflectJSONFields(reflect.TypeOf(twoLevel{}), 0); err == nil {
		t.Error("2 段の埋め込みで error にならない" +
			"——json の昇格規則は深さ優先で衝突の扱いも複雑なので、近似で通してはいけない")
	}

	// ④ 昇格が外側とぶつかったら error（json は同深さの衝突で両方消える）。
	type clash struct {
		A string `json:"outerA"`
		inner
	}
	if _, err := reflectJSONFields(reflect.TypeOf(clash{}), 0); err == nil {
		t.Error("昇格したフィールド名が外側とぶつかっているのに error にならない")
	}

	// ⑤ struct でないものを渡したら error（無言で空を返さない）。
	if _, err := reflectJSONFields(reflect.TypeOf(""), 0); err == nil {
		t.Error("struct でない型で error にならない＝この経路が無言化しうる")
	}
}

// TestGoStructFieldsFromSourceGuards は **AST 経路そのものの陽性対照**。
//
// 🔴 この経路は「今日の入力に埋め込みが 0 件」という実測に乗っている。
// **次に誰かが埋め込みを足した日に黙って浅く読む**のが最も怖い壊れ方なので、
// 合成標本で「落ちること」を固定する。（TS 走査に合成標本を付けたのと同じ理由——
// 実入力が易しいままだと、壊れても本番の突き合わせは何も言わない。）
func TestGoStructFieldsFromSourceGuards(t *testing.T) {
	dir := t.TempDir()
	write := func(base, body string) string {
		p := filepath.Join(dir, base)
		if err := os.WriteFile(p, []byte("package x\n\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 素の形は正しく読めること（この対照が空を測っていないことの確認）。
	ok := write("ok.go", "type T struct {\n\tA string `json:\"a\"`\n\tB int    `json:\"b,omitempty\"`\n\tC string `json:\"-\"`\n\tD string\n}\n")
	got, err := parseGoStructFields(ok, "T")
	if err != nil {
		t.Fatalf("素の struct を読めない: %v", err)
	}
	if len(got) != 2 || got["A"] != "a" || got["B"] != "b" {
		t.Fatalf("素の struct の読み取りが違う: %v（json:\"-\" とタグ無しは落とす）", got)
	}

	// 🔴 埋め込み（無名フィールド）→ 浅い結果ではなく error。
	emb := write("emb.go", "type Base struct {\n\tX string `json:\"x\"`\n}\n\ntype T struct {\n\tBase\n\tA string `json:\"a\"`\n}\n")
	if _, err := parseGoStructFields(emb, "T"); err == nil {
		t.Error("埋め込みフィールドが在るのに error にならない" +
			"——AST は埋め込み先の json タグを追えないので、浅く読むと穴の見落としと偽の赤が同時に出る")
	}

	// 型が無い → error（無言化しない）。
	if _, err := parseGoStructFields(ok, "NoSuchType"); err == nil {
		t.Error("存在しない型で error にならない＝この経路が無言化しうる")
	}

	// パスが無い → error（移送でパスが変わった場合）。
	if _, err := parseGoStructFields(filepath.Join(dir, "nope.go"), "T"); err == nil {
		t.Error("存在しないパスで error にならない＝移送のパス変更を黙って通す")
	}

	// json タグが 1 つも無い → error（「0 件」を結果として採らない）。
	none := write("none.go", "type T struct {\n\tA string\n\tB int\n}\n")
	if _, err := parseGoStructFields(none, "T"); err == nil {
		t.Error("json タグ 0 件で error にならない＝この経路が無言化しうる")
	}
}

// ===== 共有機構ここまで =====
