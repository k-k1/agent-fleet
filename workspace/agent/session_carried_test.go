package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// 配達文面は飾りではなく機能。docs/75 §75.10 C の実測で、「質問し直さず」の一文が
// 無いと claude は質問し直すことが分かっている（会話木から質問が消えているため）。
// 文面を書き換えるときは、この 2 つの不変条件を壊していないか見ること。
func TestCarriedQuestionPromptCarriesQuestionAndForbidsReasking(t *testing.T) {
	qs := []transcript.Question{{Header: "書き込む語", Question: "out.txt に書き込む語を選んでください。"}}
	got := buildCarriedQuestionPrompt(qs, []CarriedAnswer{{Labels: []string{"みかん"}}})
	if !strings.Contains(got, "質問し直さず") {
		t.Errorf("配達文に「質問し直さず」が無い: %q", got)
	}
	if !strings.Contains(got, "out.txt に書き込む語を選んでください。") {
		t.Errorf("質問文そのものを運んでいない（回答だけ送っても何への回答か分からない）: %q", got)
	}
	if !strings.Contains(got, "みかん") {
		t.Errorf("選ばれたラベルが無い: %q", got)
	}
}

// TUI へは send-keys -l で 1 バイトずつ載るので、改行はそのまま Enter として作用する
// （docs/dev/92）。配達文は必ず 1 行。
func TestCarriedPromptsAreSingleLine(t *testing.T) {
	qs := []transcript.Question{{Question: "改行\nを含む\r\n質問"}, {Question: "2 問目"}}
	answers := []CarriedAnswer{{Labels: []string{"A\nB"}, Notes: "補足\nの続き"}, {Labels: []string{"C"}}}
	for _, s := range []string{
		buildCarriedQuestionPrompt(qs, answers),
		buildCarriedPlanPrompt(true, "気を\nつけて"),
		buildCarriedPlanPrompt(false, "作り\n直して"),
		buildCarriedPermissionPrompt("Bash · rm\n-rf"),
	} {
		if strings.ContainsAny(s, "\n\r\t") {
			t.Errorf("配達文に制御文字が残っている: %q", s)
		}
		if s == "" {
			t.Error("配達文が空")
		}
	}
}

func TestCarriedQuestionPromptMultiSelectAndNotes(t *testing.T) {
	qs := []transcript.Question{{Question: "好きな果物は？"}}
	got := buildCarriedQuestionPrompt(qs, []CarriedAnswer{{Labels: []string{"りんご", "みかん"}, Notes: "皮ごと"}})
	if !strings.Contains(got, "りんご・みかん") {
		t.Errorf("複数選択が 1 つに畳まれていない: %q", got)
	}
	if !strings.Contains(got, "（補足: 皮ごと）") {
		t.Errorf("自由入力が落ちている: %q", got)
	}
	// 選択肢を 1 つも選ばずに自由入力だけ、も成立する（preview 付き AUQ の notes 形）。
	only := buildCarriedQuestionPrompt(qs, []CarriedAnswer{{Notes: "どちらでもない"}})
	if !strings.Contains(only, "どちらでもない") {
		t.Errorf("自由入力のみの回答が落ちた: %q", only)
	}
	if buildCarriedQuestionPrompt(qs, []CarriedAnswer{{}}) != "" {
		t.Error("空の回答からは配達文を作らない")
	}
}

// ★承認は「承認」ではなく実行の指示（§75.10 E）。文面が承認と却下で取り違えられないこと。
func TestCarriedPlanPromptApproveRejectAreDistinct(t *testing.T) {
	ok := buildCarriedPlanPrompt(true, "")
	ng := buildCarriedPlanPrompt(false, "")
	if !strings.Contains(ok, "承認します") || strings.Contains(ok, "承認しません") {
		t.Errorf("承認の文面が承認になっていない: %q", ok)
	}
	if !strings.Contains(ng, "承認しません") {
		t.Errorf("却下の文面が却下になっていない: %q", ng)
	}
	if withFb := buildCarriedPlanPrompt(false, "手順 2 を分けて"); !strings.Contains(withFb, "手順 2 を分けて") {
		t.Errorf("却下時の指示が落ちている: %q", withFb)
	}
}

// 昇格は pending-* を持ち越しへ移すだけ。優先順位は question > plan > permission
// （EffectiveModal と同じ）。
func TestPromoteCarriedPrefersQuestionOverPlan(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-1"
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"どっち？","options":[{"label":"A"}]}]`))
	status.WritePendingPlan(sid, "# 計画\n- やる")
	status.WritePendingPermission(sid, "Bash · ls")
	status.AppendPendingText(sid, "その前置き")

	if !status.PromoteCarried(sid, "halt") {
		t.Fatal("PromoteCarried = false, want true")
	}
	c, ok := status.ReadCarried(sid)
	if !ok {
		t.Fatal("持ち越しが読めない")
	}
	if c.Kind != "question" {
		t.Errorf("Kind = %q, want question（question は plan/permission に優先する）", c.Kind)
	}
	if !strings.Contains(string(c.Questions), "どっち？") {
		t.Errorf("質問ペイロードが運ばれていない: %s", c.Questions)
	}
	if c.Text != "その前置き" {
		t.Errorf("Text = %q, want その前置き（カードの文脈）", c.Text)
	}
	if c.Reason != "halt" {
		t.Errorf("Reason = %q, want halt", c.Reason)
	}
}

func TestPromoteCarriedPlanOnlyAndNoopWhenNothingPending(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-2"
	if status.PromoteCarried(sid, "stopped") {
		t.Error("保留が何も無いのに昇格した")
	}
	status.WritePendingPlan(sid, "# 計画")
	if !status.PromoteCarried(sid, "stopped") {
		t.Fatal("plan の昇格が false")
	}
	c, _ := status.ReadCarried(sid)
	if c.Kind != "plan" || c.Plan != "# 計画" {
		t.Errorf("plan が運ばれていない: %+v", c)
	}
	// ★上書きしない: halt で昇格した後、再開の boot フックがもう一度呼ぶ経路がある。
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"後から"}]`))
	if status.PromoteCarried(sid, "stopped") {
		t.Error("既存の持ち越しを上書きした（halt→resume の順で消える）")
	}
	if c2, _ := status.ReadCarried(sid); c2.Kind != "plan" {
		t.Errorf("Kind = %q, want plan のまま", c2.Kind)
	}
}

// status.Remove（halt が呼ぶ）は持ち越しを消してはいけない — 消す順序を間違えると
// 昇格した直後に自分で捨てることになる。
func TestStatusRemoveKeepsCarried(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-3"
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"残る？"}]`))
	status.PromoteCarried(sid, "halt")
	status.Remove(sid)
	if _, ok := status.ReadPendingQuestion(sid); ok {
		t.Error("pending-question が残っている")
	}
	if _, ok := status.ReadCarried(sid); !ok {
		t.Error("status.Remove が持ち越しまで消した")
	}
	status.RemoveCarried(sid)
	if _, ok := status.ReadCarried(sid); ok {
		t.Error("RemoveCarried の後も残っている")
	}
}

// 保留（生きたモーダル）があるあいだは持ち越しを出さない — 出すと Console に
// 「キーで答えるカード」と「文章で答えるカード」が同時に並ぶ。
func TestSurfaceCarriedYieldsToLivePending(t *testing.T) {
	withTempHome(t)
	sid := "sid-carried-4"
	status.WritePendingQuestion(sid, json.RawMessage(`[{"question":"いま出ている"}]`))
	status.PromoteCarried(sid, "halt")

	resp := map[string]any{}
	surfacePendingPayloads(resp, sid, "question")
	surfaceCarried(resp, sid)
	if _, ok := resp["carried"]; ok {
		t.Error("保留中なのに持ち越しまで出した")
	}

	// 保留が消えた後（＝再開後）は持ち越しだけが出る。
	status.RemovePendingQuestion(sid)
	resp2 := map[string]any{}
	surfacePendingPayloads(resp2, sid, "idle")
	surfaceCarried(resp2, sid)
	c, ok := resp2["carried"].(map[string]any)
	if !ok {
		t.Fatal("持ち越しが出ていない")
	}
	if c["kind"] != "question" {
		t.Errorf("kind = %v, want question", c["kind"])
	}
}

// --- P5: claude 以外の持ち越し（docs/75）------------------------------------------

// fakeModalAgent は ModalReporter を実装しただけのダミー kind。promoteCarriedOther が
// 「kind に訊く」ところだけを固定する（実 kind の探知手段＝会話 DB / events.jsonl /
// ペイン / ACP handle はそれぞれの package のテストが持つ）。
type fakeModalAgent struct {
	agents.Agent
	modal agents.PendingModal
	ok    bool
}

func (f fakeModalAgent) PendingModal(session.Meta) (agents.PendingModal, bool) {
	return f.modal, f.ok
}

// withFakeAgent swaps one kind's registry entry for the duration of a test.
func withFakeAgent(t *testing.T, kind string, a agents.Agent) {
	t.Helper()
	prev, had := agentRegistry[kind]
	agentRegistry[kind] = a
	t.Cleanup(func() {
		if had {
			agentRegistry[kind] = prev
			return
		}
		delete(agentRegistry, kind)
	})
}

// 質問を持つ非 claude kind は、選択肢ごと持ち越される（再開後に文章で配達できる）。
func TestPromoteCarriedOtherCarriesQuestionForm(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-q", Dir: t.TempDir(), Kind: session.KindCodex}
	qs := []transcript.Question{{Question: "どちらで進めますか？",
		Options: []transcript.Option{{Label: "A 案"}, {Label: "B 案"}}}}
	withFakeAgent(t, m.Kind, fakeModalAgent{modal: agents.PendingModal{Kind: "question", Questions: qs}, ok: true})

	if !promoteCarriedOther(m) {
		t.Fatal("promoteCarriedOther = false, want true")
	}
	c, ok := status.ReadCarried(session.UUID(m.Dir, m.Name))
	if !ok {
		t.Fatal("持ち越しが読めない")
	}
	if c.Kind != "question" {
		t.Fatalf("Kind = %q, want question", c.Kind)
	}
	// 回答フォームが往復すること: Console はこれを描いて選ばせ、選ばれたラベルが
	// 配達文になる。ここが壊れると「答えられない持ち越しカード」が出る。
	got := carriedQuestions(c)
	if len(got) != 1 || len(got[0].Options) != 2 || got[0].Options[1].Label != "B 案" {
		t.Fatalf("質問フォームが往復していない: %+v", got)
	}
	if p := buildCarriedQuestionPrompt(got, []CarriedAnswer{{Labels: []string{"B 案"}}}); !strings.Contains(p, "B 案") ||
		!strings.Contains(p, "質問し直さず") {
		t.Errorf("配達文が組み立たない: %q", p)
	}
}

// ★許可は **question として運ばない**。可否の宛先（ACP の JSON-RPC id / TUI の
// メニュー）はプロセスと一緒に消えているので、選ばせても届かない。運ぶのは事実だけ。
func TestPromoteCarriedOtherCarriesPermissionAsFactOnly(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-p", Dir: t.TempDir(), Kind: session.KindKiro}
	withFakeAgent(t, m.Kind, fakeModalAgent{
		modal: agents.PendingModal{Kind: "permission", Detail: "shell requires approval"}, ok: true})

	if !promoteCarriedOther(m) {
		t.Fatal("promoteCarriedOther = false, want true")
	}
	c, _ := status.ReadCarried(session.UUID(m.Dir, m.Name))
	if c.Kind != "permission" {
		t.Fatalf("Kind = %q, want permission（許可を question で運ぶと届かない答えを選ばせる）", c.Kind)
	}
	if c.Permission != "shell requires approval" {
		t.Errorf("何を訊かれていたかが落ちている: %q", c.Permission)
	}
	if len(c.Questions) != 0 {
		t.Errorf("許可に回答フォームを付けた: %s", c.Questions)
	}
}

// 保留が無ければ何も書かない。ここが漏れると、畳むたびに空のカードが増える。
func TestPromoteCarriedOtherNoopWithoutModal(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-n", Dir: t.TempDir(), Kind: session.KindAgy}
	withFakeAgent(t, m.Kind, fakeModalAgent{ok: false})
	if promoteCarriedOther(m) {
		t.Error("保留が無いのに昇格した")
	}
	if _, ok := status.ReadCarried(session.UUID(m.Dir, m.Name)); ok {
		t.Error("空の持ち越しを書いた")
	}
}

// ModalReporter を実装しない kind（shell / ssm）は対象外 — 保留という概念が無い。
func TestPromoteCarriedOtherSkipsKindsWithoutReporter(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-s", Dir: t.TempDir(), Kind: session.KindShell}
	if promoteCarriedOther(m) {
		t.Error("shell を昇格した")
	}
}

// 非 claude の /messages（generic 経路）にも持ち越しが載ること。載らないと、一覧の
// バッジは「質問あり」と言うのに開いても答える口が無い＝書かれるだけで見えない。
func TestPutCarriedRejectsUnanswerableShapes(t *testing.T) {
	withTempHome(t)
	if status.PutCarried("sid-x", status.Carried{Kind: "question"}, "halt") {
		t.Error("回答フォームの無い question を書いた（押せないカードになる）")
	}
	if status.PutCarried("sid-x", status.Carried{Kind: "teleport"}, "halt") {
		t.Error("知らない Kind を書いた")
	}
	if !status.PutCarried("sid-x", status.Carried{Kind: "permission", Permission: "Bash · ls"}, "halt") {
		t.Fatal("permission が書けない")
	}
	// ★上書きしない（halt で昇格した後、shutdown / 一覧経路がもう一度呼ぶ）。
	if status.PutCarried("sid-x", status.Carried{Kind: "permission", Permission: "別のもの"}, "stopped") {
		t.Error("既存の持ち越しを上書きした")
	}
	if c, _ := status.ReadCarried("sid-x"); c.Permission != "Bash · ls" || c.Reason != "halt" {
		t.Errorf("最初の持ち越しが残っていない: %+v", c)
	}
}

// --- 畳む経路は halt だけではない（archive / recreate）--------------------------

// orderingModalAgent は PendingModal が**いつ**呼ばれたかを記録する ModalReporter。
// 呼ばれた時点で tmux ログに kill-session が載っていたら、保留はもう存在しない —
// 順序そのものを検査するための仕掛け（ADR 0055 決定 12。過去に dropManagedRuntime の
// **後**で昇格していて一度も発火しなかった、という同型のバグを出している）。
type orderingModalAgent struct {
	agents.Agent
	modal     agents.PendingModal
	logPath   string
	calls     *int
	afterKill *bool
}

func (a orderingModalAgent) PendingModal(session.Meta) (agents.PendingModal, bool) {
	*a.calls++
	if b, err := os.ReadFile(a.logPath); err == nil && strings.Contains(string(b), "kill-session") {
		*a.afterKill = true
	}
	return a.modal, true
}

// fakeTmuxOnlyFor は fakeTmux と同じ tmux スタブだが、has-session が **指定した 1 つ**
// だけを「生きている」と答える。fakeTmux は has-session に常に成功を返すので、recreate が
// 新しいスラグを引く allocSessionName（空いているスラグを探して回る）が終わらない。
func fakeTmuxOnlyFor(t *testing.T, tn string) string {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) [ "$3" = "$TMUX_TEST_ALIVE" ] || exit 1 ;;
  list-panes) printf '1 %%7\n' ;;
  capture-pane) printf '\n' ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("TMUX_TEST_ALIVE", session.ExactTarget(tn))
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	isolateAgentConfigDirs(t)
	return logPath
}

// アーカイブ / 作り直しも「モーダルを出したまま畳む」経路である。halt だけが正しくても、
// 日常操作のこの 2 つが同じ順序を守らなければ同じデータロスになる（ADR 0055 決定 2:
// 畳んでよいのは失われないときだけ）。ハンドラを実際に叩いて carried-interaction が
// 書かれること、かつ**ペインを殺す前**に書かれることを見る。
func TestArchiveAndRecreatePromoteCarriedBeforeKill(t *testing.T) {
	for _, tc := range []struct {
		route  string
		handle func(http.ResponseWriter, *http.Request)
	}{
		{"archive", handleArchiveSession},
		{"recreate", handleRecreateSession},
	} {
		t.Run(tc.route, func(t *testing.T) {
			const name = "slot_carried"
			// このスロットだけが生きている＝畳む前に殺されるペインがある状態。
			logPath := fakeTmuxOnlyFor(t, session.TmuxName(name))
			m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex}
			session.WriteMeta(m)

			calls, afterKill := 0, false
			withFakeAgent(t, m.Kind, orderingModalAgent{
				Agent:   agentOf(m.Kind),
				modal:   agents.PendingModal{Kind: "permission", Detail: "shell requires approval"},
				logPath: logPath, calls: &calls, afterKill: &afterKill,
			})

			req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/"+tc.route, nil)
			req.SetPathValue("name", name)
			rec := httptest.NewRecorder()
			tc.handle(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, body = %s", tc.route, rec.Code, rec.Body.String())
			}

			if calls == 0 {
				t.Fatalf("%s が持ち越しの昇格を呼んでいない（保留中の質問が無言で消える）", tc.route)
			}
			if afterKill {
				t.Errorf("%s が kill-session の**後**で昇格した（ペインと一緒に保留は消えている）", tc.route)
			}
			c, ok := status.ReadCarried(session.UUID(m.Dir, name))
			if !ok {
				t.Fatalf("%s: carried-interaction が書かれていない", tc.route)
			}
			if c.Kind != "permission" || c.Permission != "shell requires approval" {
				t.Errorf("%s: 持ち越しの中身が違う: %+v", tc.route, c)
			}
		})
	}
}

// 非 claude の /messages（generic 経路）にも持ち越しが載ること。
//
// ここが抜けていると、tier1 が畳んだ非 claude セッションの持ち越しは**書かれるだけで
// 誰にも見えない**: 一覧のバッジは「停止中・質問あり」と言うのに、開いても答える
// カードが出ず、POST /carried-answer への入口がどこにも無い。
func TestGenericMessagesSurfacesCarried(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot-g", Dir: t.TempDir(), Kind: session.KindCodex}
	sid := session.UUID(m.Dir, m.Name)
	if !status.PutCarried(sid, status.Carried{Kind: "permission", Permission: "shell requires approval"}, "halt") {
		t.Fatal("持ち越しを書けない")
	}

	rec := httptest.NewRecorder()
	handleGenericMessages(rec, httptest.NewRequest("GET", "/sessions/slot-g/messages", nil), m, false, "stopped")
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("応答が JSON でない: %v (%s)", err, rec.Body.String())
	}
	c, ok := resp["carried"].(map[string]any)
	if !ok {
		t.Fatalf("carried が載っていない: %v", resp)
	}
	if c["kind"] != "permission" || c["permission"] != "shell requires approval" {
		t.Errorf("carried の中身が違う: %v", c)
	}
}
