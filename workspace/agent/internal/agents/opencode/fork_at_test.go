package opencode

// 発言時点からの分岐（docs/55）の opencode 側ユニットテスト。
//
// 守りたい契約は 2 つ。① ResolveForkAt が「この会話のメッセージ」以外を通さないこと
// （通ると、ユーザーが指したのと無関係な地点で分岐した会話が、それらしい見た目で生える）。
// ② serveForkSession が分岐点を messageID として送ること — ここが空のまま通ると会話まるごと
// 分岐に黙って化ける。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// forkAtStore builds a store with one root conversation (2 messages) and one child
// (subagent) conversation, and returns the slot meta that resolves to the root.
func forkAtStore(t *testing.T) session.Meta {
	t.Helper()
	db := newOpencodeLiveStore(t)
	defer db.Close()
	dir := "/home/dev/repos/x"
	const root, child = "ses_root", "ses_child"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, root, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,?,?,2)`, child, root, dir); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ id, ses, data string }{
		{"msg_1", root, `{"role":"user","time":{"created":1000}}`},
		{"msg_2", root, `{"role":"assistant","time":{"created":1100,"completed":1200}}`},
		{"msg_9", child, `{"role":"user","time":{"created":1300}}`},
	} {
		if _, err := db.Exec(`INSERT INTO message(id,session_id,time_created,time_updated,data) VALUES(?,?,1,1,?)`,
			m.id, m.ses, m.data); err != nil {
			t.Fatal(err)
		}
	}
	return session.Meta{Dir: dir, Name: "n", Kind: session.KindOpencode}
}

func TestResolveForkAtPassesAnchorThrough(t *testing.T) {
	m := forkAtStore(t)
	got, err := agentImpl{}.ResolveForkAt(m, "msg_1")
	if err != nil {
		t.Fatalf("ResolveForkAt(msg_1) error: %v", err)
	}
	// opencode の messageID は排他（指定メッセージの手前で打ち切る）なので、Console が
	// 指したアンカーをそのまま渡すのが正しい — ここで 1 つずらすと分岐点が 1 往復ずれる。
	if got != "msg_1" {
		t.Fatalf("ResolveForkAt(msg_1) = %q; want msg_1", got)
	}
}

func TestResolveForkAtRejectsUnusableAnchors(t *testing.T) {
	m := forkAtStore(t)
	for _, tc := range []struct{ name, anchor string }{
		{"empty", ""},
		{"unknown", "msg_nope"},
		// 子（サブエージェント）会話のメッセージ: 親の id 並びに属さないので、これで
		// 親を分岐すると無関係な地点で切れる。
		{"sidechain", "msg_9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (agentImpl{}).ResolveForkAt(m, tc.anchor)
			if err == nil {
				t.Fatalf("ResolveForkAt(%q) = %q, nil; want an error", tc.anchor, got)
			}
		})
	}
}

func TestServeForkSessionBody(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ses_new"}`)
	}))
	defer srv.Close()

	// 分岐点あり: messageID を載せる。
	id, err := serveForkSession(srv.URL, "ses_src", "/dir", "msg_7")
	if err != nil {
		t.Fatalf("serveForkSession error: %v", err)
	}
	if id != "ses_new" {
		t.Fatalf("id = %q; want ses_new", id)
	}
	if gotPath != "/session/ses_src/fork" {
		t.Fatalf("path = %q", gotPath)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body %q: %v", gotBody, err)
	}
	if body["messageID"] != "msg_7" {
		t.Fatalf("body = %v; want messageID=msg_7", body)
	}

	// 分岐点なし: 会話まるごと分岐（従来どおり空オブジェクト）。messageID を空文字で
	// 送ると opencode 側の ^msg パターンに弾かれるので、キーごと落ちていること。
	if _, err := serveForkSession(srv.URL, "ses_src", "/dir", ""); err != nil {
		t.Fatalf("serveForkSession (whole) error: %v", err)
	}
	if strings.Contains(gotBody, "messageID") {
		t.Fatalf("whole-conversation fork sent %q; want no messageID", gotBody)
	}
}

func TestServeForkSessionRejectedAnchorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"bad messageID"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := serveForkSession(srv.URL, "ses_src", "/dir", "msg_7")
	if err == nil {
		t.Fatal("serveForkSession(400) = nil error; want one")
	}
	// 「応答を解釈できません」だと daemon の不調に見える。分岐点が拒否されたと分かる文言に。
	if !strings.Contains(err.Error(), "分岐点") {
		t.Fatalf("error = %v; want it to name the anchor", err)
	}
}

func TestBuildLaunchRefusesForkAtOnCLIRoute(t *testing.T) {
	// CLI ルートの `--session <src> --fork` には分岐点を渡す引数が無い。ここで黙って
	// 落とすと「地点を指したのに会話まるごと分岐」になるので、起動を拒否する。
	m := session.Meta{Dir: t.TempDir(), Name: "n", Kind: session.KindOpencode, ForkFrom: "ses_src", ForkAt: "msg_1"}
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err == nil {
		t.Fatal("BuildLaunch with ForkAt on the CLI route = nil error; want a refusal")
	}
}
