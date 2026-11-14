//go:build e2e

// The member inbox against a member's real workspace: what reaches the agent and what does
// not (docs/log/130 A-9, T-1..T-4, V-2..V-4 on one Agent version), launching from a request
// (R-6..R-8) and the inbox leaving a stopped workspace stopped (W-3).
//
// "What the agent sees" is observed the way an agent observes it: the session's af MCP
// server (workspace-agent mcp-stdio) is run inside the workspace from a shell session, with
// that session's environment, and its list_memos / add_memo / update_memo answers are read.
// A flush is delivered into a shell session running `cat`, so the text the session received
// can be read back from a file.
package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// memoView is the subset of the memo wire the scenario reads.
type memoView struct {
	ID     string `json:"id"`
	Body   string `json:"body"`
	Origin *struct {
		ItemID string `json:"itemId"`
		Sender string `json:"sender"`
	} `json:"origin"`
}

// sessionToolFlags is a session-side af MCP server with every optional capability on, so a
// tools/list shows the most an agent can be offered.
var sessionToolFlags = []string{"--self-report", "--chromium-attach", "--peer-messaging", "--image-gen",
	"--fleet-spawn", "--session-search", "--agent-memory"}

// toolText is the text of a tools/call answer, failing on an in-band tool error.
func toolText(t *testing.T, r mcpResponse, what string) string {
	t.Helper()
	if r.Error != nil || r.Result.IsError || len(r.Result.Content) == 0 {
		t.Fatalf("%s: tool answered error=%v isError=%v %v", what, r.Error, r.Result.IsError, r.Result.Content)
	}
	return r.Result.Content[0].Text
}

func (m *member) listMemos(session string) []memoView {
	m.t.Helper()
	// list_memos answers {"memos": [...], "categories": [...]} through the Agent's relay.
	var out struct {
		Memos []memoView `json:"memos"`
	}
	text := toolText(m.t, m.mcp(session, sessionToolFlags, mcpTool("list_memos", nil))[2], "list_memos")
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		m.t.Fatalf("list_memos: %v: %s", err, text)
	}
	return out.Memos
}

// memoBridge calls the CP's memo bridge from inside the workspace with the workspace's own
// memo token, the way the af MCP server does, but with any body: what an agent that skips
// the tool could send. It fails unless the answer is 2xx.
func (m *member) memoBridge(session, method, path string, body any) memoView {
	m.t.Helper()
	m.cmds++
	in := fmt.Sprintf("e2e-%s-bridge-%d.json", m.name, m.cmds)
	b, _ := json.Marshal(body)
	m.writeHome(in, string(b))
	data := ""
	if body != nil {
		data = "-H 'Content-Type: application/json' --data-binary @$HOME/" + in
	}
	out := m.shell(session, `curl -sS -m 20 -o ~/`+in+`.out -w '%{http_code}' -X `+method+` `+data+
		` -H "Authorization: Bearer $AF_MEMO_TOKEN" "${AF_CP_INTERNAL_URL:-$AF_CP_BASE_URL}`+path+`"; echo; cat ~/`+in+`.out 2>/dev/null || true`)
	code, rest, _ := strings.Cut(out, "\n")
	if !strings.HasPrefix(code, "2") {
		m.t.Fatalf("%s: memo bridge %s %s: %s %s", m.name, method, path, code, rest)
	}
	var v memoView
	_ = json.Unmarshal([]byte(rest), &v)
	return v
}

// sessionNames lists the member's sessions by name, with their inbox provenance.
func (m *member) sessionNames() map[string]json.RawMessage {
	m.t.Helper()
	var out struct {
		Sessions []struct {
			Name        string          `json:"name"`
			InboxOrigin json.RawMessage `json:"inboxOrigin"`
		} `json:"sessions"`
	}
	decode(m.t, m, http.MethodGet, "/api/sessions", nil, 200, &out)
	got := map[string]json.RawMessage{}
	for _, s := range out.Sessions {
		got[s.Name] = s.InboxOrigin
	}
	return got
}

func TestInboxWorkspace(t *testing.T) {
	f := startMemberFleet(t, cpReachableEnv(t)...)
	a, b, c := f.addMember("alice"), f.addMember("bob"), f.addMember("carol")
	b.startWorkspace()
	sh := b.createSession(map[string]any{"kind": "shell", "title": "inbox-e2e"})

	// --- A-9: no inbox surface for an agent ---
	answers := b.mcp(sh, sessionToolFlags, map[string]any{"method": "tools/list"})
	operator := b.mcp(sh, []string{"--write"}, map[string]any{"method": "tools/list"})
	for which, r := range map[string]mcpResponse{"session": answers[2], "operator": operator[2]} {
		var names []string
		for _, tool := range r.Result.Tools {
			names = append(names, tool.Name)
		}
		if !slices.Contains(names, "list_memos") { // the list is real: the memo tools are in it
			t.Fatalf("A-9: the %s server lists no list_memos: %v", which, names)
		}
		for _, n := range names {
			if strings.Contains(n, "inbox") || strings.Contains(n, "roster") || strings.Contains(n, "member") {
				t.Fatalf("A-9: the %s server offers %s", which, n)
			}
		}
	}
	// With the workspace's own credentials, from inside it: the memo bridge answers (so the
	// address and token are good), the inbox does not exist there.
	before := f.inboxRowCounts()
	codes := strings.Fields(b.shell(sh, `base=${AF_CP_INTERNAL_URL:-$AF_CP_BASE_URL}; `+
		`for p in /internal/memos /api/inbox /internal/inbox /internal/inbox/items /api/inbox/stream; do `+
		`for tok in "$AF_MEMO_TOKEN" "$AGENT_TOKEN"; do `+
		`curl -s -o /dev/null -m 10 -w '%{http_code} ' -H "Authorization: Bearer $tok" "$base$p"; done; done; `+
		`curl -s -o /dev/null -m 10 -w '%{http_code}' -X POST -H "Authorization: Bearer $AF_MEMO_TOKEN" `+
		`-H 'Content-Type: application/json' -d '{"kind":"dm","body":"A-9","recipients":["`+c.key+`"]}' "$base/api/inbox/items"`))
	if len(codes) != 11 || codes[0] != "200" {
		t.Fatalf("A-9: memo bridge from the workspace answered %v, want 200 first (11 answers)", codes)
	}
	for i, code := range codes[2:] {
		if code != "401" && code != "404" {
			t.Fatalf("A-9: an inbox route answered %s to a workspace credential (answer %d of %v)", code, i+2, codes)
		}
	}
	if got := f.inboxRowCounts(); got != before {
		t.Fatalf("A-9: inbox rows %s -> %s after the workspace's requests", before, got)
	}

	// --- T-1: nothing of the inbox reaches the agent before a take-in ---
	nonce := randHex(t, 6)
	recipe := "T-recipe " + nonce
	rq := a.sendWith(map[string]any{"kind": "request", "body": "T-1 request body " + nonce, "recipients": []string{b.key, c.key},
		"attachments": []map[string]any{{"type": "recipe", "recipe": map[string]any{"prompt": recipe, "kind": "shell"}}}})
	a.sendWith(map[string]any{"kind": "dm", "body": "T-1 dm body " + nonce, "recipients": []string{b.key},
		"attachments": []map[string]any{{"type": "recipe", "recipe": map[string]any{"prompt": "T-dm-recipe " + nonce}}}})
	for _, m := range b.listMemos(sh) {
		if strings.Contains(m.Body, nonce) {
			t.Fatalf("T-1: list_memos shows inbox text before a take-in: %+v", m)
		}
	}
	// The agent's own memos, through add_memo.
	added := b.mcp(sh, sessionToolFlags,
		mcpTool("add_memo", map[string]any{"kind": "text", "body": "own one"}),
		mcpTool("add_memo", map[string]any{"kind": "text", "body": "own two"}),
		mcpTool("add_memo", map[string]any{"kind": "text", "body": "own three"}),
		mcpTool("add_memo", map[string]any{"kind": "text", "body": "V-3 forged", "origin": map[string]any{"itemId": rq.ID, "sender": a.key}}))
	var own []memoView
	for id := 2; id <= 4; id++ {
		var m memoView
		if err := json.Unmarshal([]byte(toolText(t, added[id], "add_memo")), &m); err != nil || m.ID == "" {
			t.Fatalf("add_memo answered %q: %v", toolText(t, added[id], "add_memo"), err)
		}
		own = append(own, m)
	}
	// V-3: the tool refuses an origin argument, and the CP ignores one sent past the tool,
	// straight to the memo bridge with the workspace's token.
	if r := added[5]; !r.Result.IsError {
		t.Fatalf("V-3: add_memo accepted an origin argument: %v", r.Result.Content)
	}
	forged := b.memoBridge(sh, "POST", "/internal/memos", map[string]any{"kind": "text", "body": "V-3 forged " + nonce,
		"origin": map[string]any{"itemId": rq.ID, "sender": a.key}})
	if forged.ID == "" || forged.Origin != nil || f.count(`SELECT count(*) FROM memo WHERE id=? AND origin_item_id<>''`, forged.ID) != 0 {
		t.Fatalf("V-3: a memo created with an origin through the bridge = %+v, want no origin", forged)
	}
	b.memoBridge(sh, "DELETE", "/internal/memos/"+forged.ID, nil)

	// A shell session running cat is the flush target: what a session receives lands in a file.
	catcher := b.createSession(map[string]any{"kind": "shell", "title": "flush-target"})
	waitFor(t, 20*time.Second, "flush target listening", func() (bool, string) {
		code, body := b.do(http.MethodPost, "/api/sessions/"+catcher+"/input", map[string]any{"prompt": "cat > ~/e2e-flush.txt"})
		return code == 200, fmt.Sprintf("%d %s", code, truncate(body))
	})
	flush := func(text string, memos ...memoView) (int, map[string]any) {
		var ids []string
		for _, m := range memos {
			ids = append(ids, m.ID)
		}
		code, body := b.do(http.MethodPost, "/api/memos/flush", map[string]any{"sessionName": catcher, "ids": ids, "text": text})
		out := map[string]any{}
		_ = json.Unmarshal(body, &out)
		return code, out
	}
	received := func(want string) string {
		t.Helper()
		var content string
		waitFor(t, 30*time.Second, "flush target received "+want, func() (bool, string) {
			code, body := b.do(http.MethodGet, "/api/fs/file?path=e2e-flush.txt", nil)
			var fc struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal(body, &fc)
			content = fc.Content
			return code == 200 && strings.Contains(content, want), fmt.Sprintf("%d %s", code, truncate([]byte(content)))
		})
		return content
	}
	if code, out := flush("", own[0], own[1]); code != 200 {
		t.Fatalf("T-1: flush of own memos: %d %v", code, out)
	}
	if got := received("own two"); strings.Contains(got, nonce) {
		t.Fatalf("T-1: the flushed message carries inbox text:\n%s", got)
	}

	// --- T-2: the assignee takes the recipe in; the agent sees it, with its origin ---
	b.mustAct(rq.ID, "claim", 200)
	var took struct {
		Memo    memoView `json:"memo"`
		Created bool     `json:"created"`
	}
	decode(t, b, http.MethodPost, "/api/inbox/items/"+rq.ID+"/take-in", map[string]any{}, http.StatusCreated, &took)
	var seen []memoView
	for _, m := range b.listMemos(sh) {
		if strings.Contains(m.Body, nonce) {
			seen = append(seen, m)
		}
	}
	if len(seen) != 1 || seen[0].ID != took.Memo.ID || seen[0].Origin == nil ||
		seen[0].Origin.ItemID != rq.ID || seen[0].Origin.Sender != a.key || !strings.Contains(seen[0].Body, recipe) {
		t.Fatalf("T-2: list_memos shows %+v, want the one taken-in recipe with origin %s from %s", seen, rq.ID, a.key)
	}

	// --- T-3: carol, a recipient but not the assignee, cannot; bob's second press is the same memo ---
	if code, out := c.act(rq.ID, "take-in"); code != http.StatusConflict {
		t.Fatalf("T-3: carol's take-in answered %d %v, want 409", code, out)
	}
	if n := f.count(`SELECT count(*) FROM memo WHERE membership_id=?`, c.membershipID()); n != 0 {
		t.Fatalf("T-3: carol has %d memos after a refused take-in", n)
	}
	var again struct {
		Memo    memoView `json:"memo"`
		Created bool     `json:"created"`
	}
	decode(t, b, http.MethodPost, "/api/inbox/items/"+rq.ID+"/take-in", map[string]any{}, http.StatusOK, &again)
	if again.Created || again.Memo.ID != took.Memo.ID ||
		f.count(`SELECT count(*) FROM memo WHERE origin_item_id=?`, rq.ID) != 1 {
		t.Fatalf("T-3: second take-in = %+v, want the same memo %s and no new row", again, took.Memo.ID)
	}

	// --- V-2 and V-3 (this Agent version): update_memo changes the body, never the origin,
	// and an origin sent past the tool is ignored ---
	edited := "edited " + recipe
	toolText(t, b.mcp(sh, sessionToolFlags, mcpTool("update_memo", map[string]any{"id": took.Memo.ID, "body": edited}))[2], "update_memo")
	b.memoBridge(sh, "PATCH", "/internal/memos/"+took.Memo.ID, map[string]any{"body": edited, "origin": map[string]any{"itemId": "forged", "sender": "mallory"}})
	b.memoBridge(sh, "PATCH", "/internal/memos/"+took.Memo.ID, map[string]any{"body": edited, "origin": nil})
	found := false
	for _, m := range b.listMemos(sh) {
		if m.ID == took.Memo.ID {
			found = true
			if m.Body != edited || m.Origin == nil || m.Origin.ItemID != rq.ID || m.Origin.Sender != a.key {
				t.Fatalf("V-2/V-3: after the edits the memo is %+v, want body %q and the CP's origin", m, edited)
			}
		}
	}
	if !found {
		t.Fatalf("V-2: the taken-in memo is gone from list_memos")
	}

	// --- T-4: a taken-in memo is flushed on its own, under the line the CP writes ---
	taken := took.Memo
	for _, text := range []string{"", "T-4 edited mixed"} {
		code, out := flush(text, own[2], taken)
		if c, _ := apiErr(out); code != http.StatusConflict || c != "memo_origin_alone" {
			t.Fatalf("T-4: mixed flush (text %q) answered %d %v, want 409 memo_origin_alone", text, code, out)
		}
	}
	originLine := "受信箱から取り込んだ内容（送信者: " + a.key + "、項目: " + rq.ID + "）"
	if code, out := flush("", taken); code != 200 {
		t.Fatalf("T-4: flush of the taken-in memo: %d %v", code, out)
	}
	got := received(edited)
	if i, j := strings.Index(got, originLine), strings.Index(got, edited); i < 0 || j < i {
		t.Fatalf("T-4: the flushed message does not open with the CP's origin line:\n%s", got)
	}
	if code, out := flush("T-4 edited "+nonce, taken); code != 200 {
		t.Fatalf("T-4: flush of the taken-in memo with edited text: %d %v", code, out)
	}
	got = received("T-4 edited " + nonce)
	if n := strings.Count(got, originLine); n != 2 || strings.LastIndex(got, originLine) > strings.Index(got, "T-4 edited "+nonce) {
		t.Fatalf("T-4: the edited flush lost the origin line (%d of 2):\n%s", n, got)
	}
	if strings.Contains(got, "own three") {
		t.Fatalf("T-4: an own memo was sent with the taken-in one:\n%s", got)
	}

	// --- R-7: carol, not the assignee, launches; nothing is created for her ---
	launchBody := map[string]any{"kind": "shell", "title": "from the request"}
	if code, body := c.do(http.MethodPost, "/api/inbox/items/"+rq.ID+"/launch", launchBody); code != http.StatusConflict {
		t.Fatalf("R-7: carol's launch answered %d %s, want 409", code, truncate(body))
	}
	if n := f.count(`SELECT count(*) FROM workspace WHERE membership_id=?`, c.membershipID()); n != 0 {
		t.Fatalf("R-7: carol's refused launch left %d workspace record(s)", n)
	}

	// --- R-8 and V-4: bob presses launch twice at once; one session, recorded on the card ---
	sessionsBefore := b.sessionNames()
	codes8, outs := race(
		func() (int, map[string]any) { return launch(b, rq.ID, launchBody) },
		func() (int, map[string]any) { return launch(b, rq.ID, launchBody) })
	var created string
	for i, code := range codes8 {
		switch code {
		case http.StatusCreated, http.StatusOK:
			if created != "" {
				t.Fatalf("R-8: both launches created a session: %v %v", codes8, outs)
			}
			created, _ = outs[i]["name"].(string)
		case http.StatusConflict:
			if c, _ := apiErr(outs[i]); c != "inbox_launch_in_progress" {
				t.Fatalf("R-8: the second launch answered %v, want inbox_launch_in_progress", outs[i])
			}
		default:
			t.Fatalf("R-8: launch answered %d %v", code, outs[i])
		}
	}
	after := b.sessionNames()
	if created == "" || len(after) != len(sessionsBefore)+1 {
		t.Fatalf("R-8: launches %v made %d new session(s), want exactly one", codes8, len(after)-len(sessionsBefore))
	}
	var origin struct {
		Item   string `json:"item"`
		Sender string `json:"sender"`
	}
	if err := json.Unmarshal(after[created], &origin); err != nil || origin.Item != rq.ID || origin.Sender != a.key {
		t.Fatalf("V-4: session %s carries provenance %s, want item %s from %s", created, after[created], rq.ID, a.key)
	}
	if got := b.inboxGet(rq.ID); got.Session == nil || got.Session.Name != created || got.Session.LaunchedAt == "" {
		t.Fatalf("R-8/V-4: bob's card records %+v, want session %s", got.Session, created)
	}
	if got := a.inboxGet(rq.ID); got.Session == nil || got.Session.By.UserKey != b.key || got.Session.Name != "" {
		t.Fatalf("V-4: alice's card shows %+v, want a session by bob without its name", got.Session)
	}

	// --- R-6: the request is withdrawn, or handed back, before bob presses launch ---
	for _, lose := range []func(id string){
		func(id string) { a.mustAct(id, "withdraw", 200) },
		func(id string) { b.mustAct(id, "release", 200) },
	} {
		it := a.send("request", "R-6", b)
		b.mustAct(it.ID, "claim", 200)
		lose(it.ID)
		if code, out := launch(b, it.ID, launchBody); code != http.StatusConflict {
			t.Fatalf("R-6: launch after losing the claim answered %d %v, want 409", code, out)
		}
	}
	if n := len(b.sessionNames()); n != len(after) {
		t.Fatalf("R-6: refused launches changed bob's sessions %d -> %d", len(after), n)
	}

	// --- W-3: with the workspace stopped, the inbox leaves it stopped ---
	b.stopWorkspace()
	w1, w2 := a.send("request", "W-3 one", b), a.send("request", "W-3 two", b)
	b.inbox()
	b.postJSON("/api/inbox/items/"+w1.ID+"/read", map[string]any{}, 200)
	for _, op := range []string{"claim", "release", "claim", "done"} {
		b.mustAct(w1.ID, op, 200)
	}
	b.mustAct(w2.ID, "decline", 200)
	b.requireStopped("after using the inbox (W-3)")
}

// launch is POST /api/inbox/items/{id}/launch with a session-create body.
func launch(m *member, id string, body map[string]any) (int, map[string]any) {
	code, b := m.do(http.MethodPost, "/api/inbox/items/"+id+"/launch", body)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return code, out
}
