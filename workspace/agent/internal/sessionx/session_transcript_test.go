package sessionx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// TestToBrowseRel checks SendUserFile path normalization: absolute paths under the browse
// root become root-relative (forward-slashed), cwd-relative paths are anchored on the
// turn's cwd first, and anything outside the root (or a relative path with no cwd) is left
// untouched so it still lists in the panel even if it can't be opened.
func TestToBrowseRel(t *testing.T) {
	root := "/home/dev"
	cases := []struct {
		name, p, cwd, want string
	}{
		{"abs under root", "/home/dev/repos/x/report.md", "", "repos/x/report.md"},
		{"rel joined on cwd", "report.md", "/home/dev/repos/x", "repos/x/report.md"},
		{"rel dotted on cwd", "./out/a.png", "/home/dev/repos/x", "repos/x/out/a.png"},
		{"abs outside root", "/tmp/claude/scratch/a.png", "", "/tmp/claude/scratch/a.png"},
		{"rel no cwd", "report.md", "", "report.md"},
		{"cwd outside root", "a.png", "/tmp/work", "/tmp/work/a.png"},
	}
	for _, c := range cases {
		if got := toBrowseRel(c.p, c.cwd, root); got != c.want {
			t.Errorf("%s: toBrowseRel(%q,%q,%q) = %q, want %q", c.name, c.p, c.cwd, root, got, c.want)
		}
	}
}

func TestGenericMutableTail(t *testing.T) {
	all := []transcript.Turn{
		{Role: "user", Idx: 0, Text: "調べて"},
		{Role: "assistant", Idx: 1, Text: "最終回答"},
	}

	got := genericMutableTail(all, len(all))
	if len(got) != 1 || got[0].Idx != 1 || got[0].Text != "最終回答" {
		t.Fatalf("mutable tail = %+v, want the completed assistant turn", got)
	}
	if got := genericMutableTail(all, 1); got != nil {
		t.Fatalf("behind cursor tail = %+v, want nil", got)
	}
	if got := genericMutableTail(all[:1], 1); got != nil {
		t.Fatalf("user tail = %+v, want nil", got)
	}
}

const testPlan = "# D-1 改稿計画\n\n代償設計を見直す"

func planTurns() []transcript.Turn {
	return []transcript.Turn{
		{Role: "user", Idx: 10, Text: "計画を出して"},
		{Role: "assistant", Idx: 11, Text: "こうします", Parts: []transcript.Part{{Kind: "text", Text: "こうします"}}},
		{Role: "assistant", Idx: 12, Parts: []transcript.Part{{Kind: "plan", Tool: "ExitPlanMode", Plan: testPlan, QID: "toolu_1"}}},
	}
}

// 承認待ちのプランは、転写の tool_use（ASK 時点で書かれる）と hook が捕まえた保留
// ペイロードの二重になる。片方＝インラインを落とし、その行までカーソルを戻して
// 「決まったあとに出し直せる」ことまでを見る。
func TestHidePendingInteractionPlan(t *testing.T) {
	pending := map[string]any{"pendingPlan": testPlan}
	turns, hold := hidePendingInteraction(planTurns(), pending, map[string]claude.InteractionAnswer{})

	if hold != 12 {
		t.Fatalf("hold = %d, want 12 (the plan's line — it must stay unconsumed)", hold)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %+v, want the plan-only turn dropped (no empty bubble)", turns)
	}
	if _, ok := pending["pendingPlan"]; !ok {
		t.Error("pendingPlan was withdrawn — the actionable card is the one that must survive")
	}
}

// 決着済みなのに保留ペイロードが残っている（hook が消し損ねた）ときは逆 — 履歴では
// なくゴーストのカードを引っ込める。履歴を消すとカーソルが永久に戻ってしまう。
func TestHidePendingInteractionStalePayload(t *testing.T) {
	pending := map[string]any{"pendingPlan": testPlan}
	answers := map[string]claude.InteractionAnswer{"toolu_1": {Text: "approved"}}
	turns, hold := hidePendingInteraction(planTurns(), pending, answers)

	if hold != -1 {
		t.Errorf("hold = %d, want -1 (nothing hidden)", hold)
	}
	if len(turns) != 3 {
		t.Errorf("turns = %d, want 3 (history untouched)", len(turns))
	}
	if _, ok := pending["pendingPlan"]; ok {
		t.Error("stale pendingPlan survived — it would show a 承認待ち card for a decided plan")
	}
}

// 却下されたプランは同じ Markdown のまま出し直されることがある。保留に当たるのは
// 常に最後の提示で、決着済みの古いカードは履歴に残さなければならない。
func TestHidePendingInteractionRepresentedPlan(t *testing.T) {
	turns := append(planTurns(), transcript.Turn{
		Role: "assistant", Idx: 20,
		Parts: []transcript.Part{{Kind: "plan", Tool: "ExitPlanMode", Plan: testPlan, QID: "toolu_2"}},
	})
	answers := map[string]claude.InteractionAnswer{"toolu_1": {Text: "[Request interrupted by user for tool use]"}}
	got, hold := hidePendingInteraction(turns, map[string]any{"pendingPlan": testPlan}, answers)

	if hold != 20 {
		t.Fatalf("hold = %d, want 20 (the newest presentation)", hold)
	}
	if len(got) != 3 || got[2].Idx != 12 || got[2].Parts[0].QID != "toolu_1" {
		t.Fatalf("turns = %+v, want the decided first presentation kept", got)
	}
}

// AUQ も同じ二重になる。保留ペイロードは tool_input.questions そのものなので、
// パース後の形どうしで突き合わせる（hook 側に tool_use id が無いため）。
func TestHidePendingInteractionQuestion(t *testing.T) {
	raw := json.RawMessage(`[{"header":"方式","question":"どれにしますか？","options":[{"label":"案A"},{"label":"案B"}]}]`)
	var qs []transcript.Question
	if err := json.Unmarshal(raw, &qs); err != nil {
		t.Fatal(err)
	}
	turns := []transcript.Turn{
		{Role: "assistant", Idx: 5, Text: "前置き", Parts: []transcript.Part{
			{Kind: "text", Text: "前置き"},
			{Kind: "question", Tool: "AskUserQuestion", Questions: qs, QID: "toolu_q"},
		}},
	}
	pending := map[string]any{"pendingQuestions": raw, "pendingText": "前置き"}
	got, hold := hidePendingInteraction(turns, pending, map[string]claude.InteractionAnswer{})

	if hold != 5 {
		t.Fatalf("hold = %d, want 5", hold)
	}
	if len(got) != 1 || len(got[0].Parts) != 1 || got[0].Parts[0].Kind != "text" {
		t.Fatalf("parts = %+v, want the question stripped and the prose kept", got)
	}
	if _, ok := pending["pendingQuestions"]; !ok {
		t.Error("pendingQuestions was withdrawn — the answerable card must survive")
	}
	// 質問の直前の地の文は、質問の tool_use が転写に出ている時点で転写にも出ている
	// （claude は地の文のメッセージを先に書く）。カードにも重ねると同じ段落が2回出る。
	if _, ok := pending["pendingText"]; ok {
		t.Error("pendingText survived — the same prose is already inline")
	}
}

// 別の質問が保留のときに、たまたま近くにある別の question part を巻き添えで消さない。
func TestHidePendingInteractionNoMatch(t *testing.T) {
	turns := planTurns()
	pending := map[string]any{"pendingPlan": "# 別の計画"}
	got, hold := hidePendingInteraction(turns, pending, map[string]claude.InteractionAnswer{})

	if hold != -1 || len(got) != 3 {
		t.Fatalf("hold = %d, turns = %d, want -1 / 3 (unrelated payload changes nothing)", hold, len(got))
	}
}

// TestSweepSettledPending は、モーダルがもう無い保留ペイロードが掃除されること、
// そして**まだ出ているモーダル**は掃除されないことを固定する。
//
// 実バグ（2026-08-31 利用者報告「AUQ をキャンセルしても何度も聞かれる」）: キャンセルは
// ツールの却下なので AskUserQuestion の PostToolUse が鳴らず、pending-question が残る。
// 決着した行が窓から出た次のポーリングで、それが**生きた回答フォーム付きカード**として
// 出し直され、答えてもキャンセルしても無反応になる。
func TestSweepSettledPending(t *testing.T) {
	const sid = "sid-sweep"
	ask := []byte(`{"type":"assistant","timestamp":"2026-08-31T12:00:00.000Z","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"header":"方式","question":"どれ？","options":[{"label":"A"}]}]}}]}}`)
	// キャンセルの実文言（実転写から採取）。回答ではなく「ツールが却下された」形で来る。
	decided := []byte(`{"type":"user","timestamp":"2026-08-31T12:05:00.000Z","message":{"content":[{"type":"tool_result","tool_use_id":"q1","is_error":true,"content":"The user doesn't want to proceed with this tool use. The tool use was rejected"}]}}`)
	raw := json.RawMessage(`[{"header":"方式","question":"どれ？","options":[{"label":"A"}]}]`)

	t.Run("決着より前に捕まえた保留は掃除される", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		status.AppendPendingText(sid, "前置き")
		// ペイロードは質問が出た時点＝決着より前に書かれたもの、という関係を作る。
		backdate(t, filepath.Join(paths.AgentConfigDir(), "pending-question", sid+".json"), "2026-08-31T12:00:00.100Z")

		// 掃除は surfacePendingPayloads の中で走る（出す経路と同じ場所）ので、
		// ここも「出す側」を呼んで、消えることと出ないことを一度に固定する。
		resp := map[string]any{}
		surfacePendingPayloads(resp, sid, "question", [][]byte{ask, decided})

		if _, ok := resp["pendingQuestions"]; ok {
			t.Error("a settled question was surfaced — the Console shows it as a live, unanswerable card")
		}
		if _, ok := status.ReadPendingQuestion(sid); ok {
			t.Error("pending question survived a settled modal — the next poll offers it again")
		}
		if _, ok := status.ReadPendingText(sid); ok {
			t.Error("pending text survived — it is only ever shown with the question card")
		}
	})

	t.Run("決着より後に捕まえた保留は残す", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		// フックが先に書き、tool_use の行はまだ flush されていない状態（実測 106〜122ms）。
		// 転写に見えている決着は**ひとつ前の**モーダルのもので、生きた質問を消してはならない。
		backdate(t, filepath.Join(paths.AgentConfigDir(), "pending-question", sid+".json"), "2026-08-31T12:05:00.100Z")

		resp := map[string]any{}
		surfacePendingPayloads(resp, sid, "question", [][]byte{ask, decided})

		if _, ok := resp["pendingQuestions"]; !ok {
			t.Error("a live question was not surfaced")
		}
		if _, ok := status.ReadPendingQuestion(sid); !ok {
			t.Error("a live question was swept — its payload was captured after the last decision")
		}
	})

	// AUQ は自分自身の permission_prompt を Pre/PostToolUse の間に鳴らす（実測: 質問の
	// 6 秒後に state=permission）。その許可ペイロードは「質問が保留である」ことだけを
	// 理由に伏せられていたので、質問を掃除したら一緒に捨てないと、キャンセルした直後に
	// **決着済みのツールの承認ダイアログ**が出る（利用者報告・掃除を入れた直後の回帰）。
	t.Run("質問が伏せていた許可も道連れにする", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		status.WritePendingPermission(sid, "Claude needs your permission")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "pending-question", sid+".json"), "2026-08-31T12:00:00.100Z")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "pending-perm", sid+".txt"), "2026-08-31T12:00:06.000Z")

		resp := map[string]any{}
		surfacePendingPayloads(resp, sid, "permission", [][]byte{ask, decided})

		if _, ok := resp["pendingPermission"]; ok {
			t.Error("a permission prompt for an already-decided tool was surfaced — the 承認ダイアログ pops right after キャンセル")
		}
		if _, ok := status.ReadPendingPermission(sid); ok {
			t.Error("stale permission payload survived; the next poll pops the dialog again")
		}
	})

	// 逆向き: 決着より**後**に来た許可は本物（Edit/Bash の承認待ち）。掃除してはならない。
	t.Run("決着より後の許可は本物なので残す", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingPermission(sid, "Edit · /tmp/a.go")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "pending-perm", sid+".txt"), "2026-08-31T12:09:00.000Z")

		resp := map[string]any{}
		surfacePendingPayloads(resp, sid, "permission", [][]byte{ask, decided})

		if _, ok := resp["pendingPermission"]; !ok {
			t.Error("a live permission prompt was swept — it was captured after the last decision")
		}
	})

	// ペイロードを消したら state も消す。片方だけだと「決めるカードが無いのに送信を断る」
	// が残る（利用者報告 2026-09-04「AUQ をキャンセルしたあと、メッセージが送信できなく
	// なる」）。カード（表示）と state（判定）は同じ決着で同時に畳む。
	t.Run("伏せていた許可状態も掃除される", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		status.WritePendingPermission(sid, "Claude needs your permission")
		// AUQ 自身の permission_prompt が state を permission に上書きした形（実測: 質問の
		// 6 秒後）。キャンセルは PostToolUse を鳴らさないので、これを書き直す者はいない。
		status.Persist(sid, "permission")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "pending-question", sid+".json"), "2026-08-31T12:00:00.100Z")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "pending-perm", sid+".txt"), "2026-08-31T12:00:06.000Z")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "session-status", sid+".json"), "2026-08-31T12:00:06.000Z")

		surfacePendingPayloads(map[string]any{}, sid, "permission", [][]byte{ask, decided})

		if st, ok := status.Read(sid); ok {
			t.Errorf("state %q survived the sweep — 送信は permission_pending で断られるのに、決めるカードはもう無い", st.State)
		}
		if got := status.LiveState(sid); got != "idle" {
			t.Errorf("LiveState = %q, want idle（走っていれば次の poll の reverse-heal が working へ戻す）", got)
		}
	})

	// 逆向き: 生きた許可（決着より後に捕まえた本物の Edit/Bash 承認）が残っているなら、
	// state にも断ってもらわないと自由文が許可メニューに飲まれて Enter が「許可」を押す。
	t.Run("生きた許可が残っているなら state も残す", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingPermission(sid, "Edit · /tmp/a.go")
		status.Persist(sid, "permission")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "pending-perm", sid+".txt"), "2026-08-31T12:09:00.000Z")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "session-status", sid+".json"), "2026-08-31T12:00:06.000Z")

		surfacePendingPayloads(map[string]any{}, sid, "permission", [][]byte{ask, decided})

		if st, ok := status.Read(sid); !ok || st.State != "permission" {
			t.Errorf("state = %q/%v, want permission（生きた承認ダイアログの前で送信を通してはならない）", st.State, ok)
		}
	})

	// 決着の**後**に書かれた state は新しいモーダルのもの（フックが先、tool_use の flush が
	// 後、の 106〜122ms を含む）。掃除してはならない。
	t.Run("決着より後の state は残す", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.Persist(sid, "permission")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "session-status", sid+".json"), "2026-08-31T12:05:00.100Z")

		surfacePendingPayloads(map[string]any{}, sid, "permission", [][]byte{ask, decided})

		if st, ok := status.Read(sid); !ok || st.State != "permission" {
			t.Errorf("state = %q/%v, want permission（決着より後に立ったモーダル）", st.State, ok)
		}
	})

	// working / idle は掃除の管轄外 — ターンの話であってモーダルの話ではない。ここを
	// 消すと、答えた直後（PostToolUse→working）のターンが 入力待ち を名乗る。
	t.Run("モーダルでない state は触らない", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.Persist(sid, "working")
		backdate(t, filepath.Join(paths.AgentConfigDir(), "session-status", sid+".json"), "2026-08-31T12:00:06.000Z")

		surfacePendingPayloads(map[string]any{}, sid, "working", [][]byte{ask, decided})

		if st, ok := status.Read(sid); !ok || st.State != "working" {
			t.Errorf("state = %q/%v, want working", st.State, ok)
		}
	})

	t.Run("決着がひとつも無ければ触らない", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		status.WritePendingQuestion(sid, raw)
		backdate(t, filepath.Join(paths.AgentConfigDir(), "pending-question", sid+".json"), "2026-08-31T12:00:00.100Z")

		surfacePendingPayloads(map[string]any{}, sid, "question", [][]byte{ask})

		if _, ok := status.ReadPendingQuestion(sid); !ok {
			t.Error("pending question swept with no tool_result in the transcript")
		}
	})
}

// 層をまたいで固定する: 掃除（表示側）を通ったあと、送信のガード（判定側）が実際に
// 開いていること。片方だけのテストでは、この 2 つが食い違ったこと自体を見逃す — 実バグ
// （2026-09-04）は「カードは消えたのに送信は permission_pending で断られる」だった。
func TestCancelledInteractionFreesComposer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "auq_cancel"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, name)

	ask := []byte(`{"type":"assistant","timestamp":"2026-09-04T12:00:00.000Z","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"header":"方式","question":"どれ？","options":[{"label":"A"}]}]}}]}}`)
	cancelled := []byte(`{"type":"user","timestamp":"2026-09-04T12:05:00.000Z","message":{"content":[{"type":"tool_result","tool_use_id":"q1","is_error":true,"content":"The user doesn't want to proceed with this tool use. The tool use was rejected"}]}}`)

	// キャンセル直前の姿: 質問ペイロード＋それが伏せていた許可ペイロード＋
	// AUQ 自身の permission_prompt が書いた state。
	status.WritePendingQuestion(sid, json.RawMessage(`[{"header":"方式","question":"どれ？","options":[{"label":"A"}]}]`))
	status.WritePendingPermission(sid, "Claude needs your permission")
	status.Persist(sid, "permission")
	for path, ts := range map[string]string{
		filepath.Join(paths.AgentConfigDir(), "pending-question", sid+".json"): "2026-09-04T12:00:00.100Z",
		filepath.Join(paths.AgentConfigDir(), "pending-perm", sid+".txt"):      "2026-09-04T12:00:06.000Z",
		filepath.Join(paths.AgentConfigDir(), "session-status", sid+".json"):   "2026-09-04T12:00:06.000Z",
	} {
		backdate(t, path, ts)
	}
	if got := promptBlocker(name); got != "question" {
		t.Fatalf("キャンセル前は質問カードへ誘導するはず: promptBlocker = %q", got)
	}

	resp := map[string]any{}
	surfacePendingPayloads(resp, sid, "permission", [][]byte{ask, cancelled})

	if len(resp) != 0 {
		t.Fatalf("決着済みのモーダルが出た: %v", resp)
	}
	if got := promptBlocker(name); got != "" {
		t.Fatalf("promptBlocker = %q — 決めるカードが 1 枚も無いのに送信を断っている", got)
	}
}

// backdate は保留ペイロードの mtime を「いつ捕まえたか」に合わせる。掃除の判定材料は
// 中身ではなく捕捉時刻なので、テストもそこだけを作る。
func backdate(t *testing.T, path, ts string) {
	t.Helper()
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}
