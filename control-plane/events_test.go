// events_test.go — /api/events 統合 push チャネル（通信量削減 P3）の契約テスト。
// 初回 tick は全 stream のスナップショットを流し、無変化 tick は何も流さない
// （diff 抑制）こと、変化した stream だけが再送されること、静穏時は ping で
// 死活を示すことを固定する。payload の shape 自体は既存 REST と共用の合成関数
// （workspacePayload / sessionsPayload / listPayload）側のテストが正。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// eventsTestEnv builds a sqlite-backed eventsAPI plus a stub Agent serving
// /sessions and /notifications, and the resolved record pointing at it.
func eventsTestEnv(t *testing.T, sessionsBody *atomic.Value) (eventsAPI, *resolved) {
	t.Helper()
	var sessionsDelay atomic.Value
	return eventsTestEnvDelayed(t, sessionsBody, &sessionsDelay)
}

// eventsTestEnvDelayed is eventsTestEnv with a knob that makes the stub Agent's
// /sessions slow, so a poll can still be in flight when the request context is
// cancelled — the shape the hosted CI runner hit by being slow.
func eventsTestEnvDelayed(t *testing.T, sessionsBody *atomic.Value, sessionsDelay *atomic.Value) (eventsAPI, *resolved) {
	t.Helper()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(context.Background())
	id, _ := st.UpsertIdentity(context.Background(), "events@example.com", "events", "")
	m, _ := st.EnsureMembership(context.Background(), id.ID, tenant.ID, "member")
	ws := Workspace{ID: newID(), TenantID: tenant.ID, MembershipID: m.ID,
		ContainerName: "af-ws-events", Network: "af-net-events", DataDir: t.TempDir(),
		AgentPort: "7700", AgentToken: "tok", State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			if d, ok := sessionsDelay.Load().(time.Duration); ok && d > 0 {
				time.Sleep(d)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(sessionsBody.Load().(string)))
		case "/notifications":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"notifications":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	mgr := &manager{store: st}
	a := eventsAPI{memberAuth{mgr}, newWorkspaceAPI(mgr, false), notificationAPI{memberAuth{mgr}, st},
		5 * time.Millisecond, time.Hour /* ping 実質 OFF（ping テストだけ上書き） */}
	res := &resolved{rt: stubRuntime{endpoint: srv.URL, token: "tok"}, ws: ws,
		mv: MembershipView{MembershipID: m.ID}}
	return a, res
}

// runStream drives a.stream until the deadline and returns the decoded frames
// (per-stream payload sequences) plus the raw body (for ping assertions).
func runStream(t *testing.T, a eventsAPI, res *resolved, d time.Duration) (map[string][]json.RawMessage, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	r := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	a.stream(w, r, res)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	frames := map[string][]json.RawMessage{}
	for _, part := range strings.Split(w.Body.String(), "\n\n") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "data: ") {
			continue
		}
		var f struct {
			Stream string          `json:"stream"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(part, "data: ")), &f); err != nil {
			t.Fatalf("bad frame %q: %v", part, err)
		}
		frames[f.Stream] = append(frames[f.Stream], f.Data)
	}
	return frames, w.Body.String()
}

// TestEventsStreamSnapshotThenSilence: 初回 tick で 4 stream 全部のスナップ
// ショットが 1 回ずつ流れ、その後の無変化 tick では再送されない（diff 抑制 —
// これが P3 の削減効果そのもの）。
func TestEventsStreamSnapshotThenSilence(t *testing.T) {
	var body atomic.Value
	body.Store(`{"sessions":[{"name":"s1","kind":"claude","alive":true}]}`)
	a, res := eventsTestEnv(t, &body)
	frames, _ := runStream(t, a, res, 120*time.Millisecond)

	for _, stream := range []string{"workspace", "stats", "sessions", "notifications"} {
		if len(frames[stream]) != 1 {
			t.Errorf("%s frames = %d, want 1 (snapshot once, then suppressed)", stream, len(frames[stream]))
		}
	}
	var wsp map[string]any
	_ = json.Unmarshal(frames["workspace"][0], &wsp)
	if wsp["state"] != "running" || wsp["name"] != "stub" {
		t.Errorf("workspace payload = %v", wsp)
	}
	var sess struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal(frames["sessions"][0], &sess)
	if len(sess.Sessions) != 1 || sess.Sessions[0]["name"] != "s1" {
		t.Errorf("sessions payload = %v", sess)
	}
	var notif map[string]any
	_ = json.Unmarshal(frames["notifications"][0], &notif)
	if notif["sourceState"] != "ready" {
		t.Errorf("notifications payload = %v", notif)
	}
}

// TestEventsStreamPushesChange: Agent 側の sessions が変わったら、その stream
// だけがもう 1 フレーム流れる。
func TestEventsStreamPushesChange(t *testing.T) {
	var body atomic.Value
	body.Store(`{"sessions":[{"name":"s1","kind":"claude","alive":true}]}`)
	a, res := eventsTestEnv(t, &body)

	go func() {
		time.Sleep(50 * time.Millisecond)
		body.Store(`{"sessions":[{"name":"s1","kind":"claude","alive":false}]}`)
	}()
	frames, _ := runStream(t, a, res, 150*time.Millisecond)

	if got := len(frames["sessions"]); got != 2 {
		t.Fatalf("sessions frames = %d, want 2 (snapshot + change)", got)
	}
	if got := len(frames["workspace"]); got != 1 {
		t.Errorf("workspace frames = %d, want 1 (unchanged stream stays silent)", got)
	}
	var sess struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal(frames["sessions"][1], &sess)
	if sess.Sessions[0]["alive"] != false {
		t.Errorf("second sessions frame = %v, want alive=false", sess)
	}
}

// TestEventsStreamNoFrameWhenCancelledMidPoll: 購読者が切れた瞬間に走っていた
// tick は、フレームを 1 本も足さない。
//
// これは hosted CI で上の 2 本が落ちていた原因そのもの。sessions の payload は
// Agent への HTTP が失敗すると DB ミラー（別 shape）へフォールバックするので、
// リクエスト ctx のキャンセルが poll の最中に当たると「変化した」ように見えて
// 余分なフレームが出る。手元では poll が 1ms 未満で終わるので窓に当たらず、
// 遅いランナーでだけ当たっていた。ここでは Agent をわざと遅くして再現する。
func TestEventsStreamNoFrameWhenCancelledMidPoll(t *testing.T) {
	var body, delay atomic.Value
	body.Store(`{"sessions":[{"name":"s1","kind":"claude","alive":true}]}`)
	a, res := eventsTestEnvDelayed(t, &body, &delay)

	// 初回 tick（スナップショット）は素通しし、その後の poll を締切より長くする。
	go func() {
		time.Sleep(20 * time.Millisecond)
		delay.Store(500 * time.Millisecond)
	}()
	frames, _ := runStream(t, a, res, 120*time.Millisecond)

	if got := len(frames["sessions"]); got != 1 {
		t.Fatalf("sessions frames = %d, want 1 (a cancelled poll is not a change): %s",
			got, frames["sessions"])
	}
}

// TestRoundStats: 生の cgroup 値はバイト単位で毎読み揺れるので、diff 抑制が
// 効くよう mem_used は 8MiB floor・cpu_pct は整数へ丸める（表示粒度は WS バー
// チップの丸め percent / 0.1GiB なので情報は落ちない）。それ以外のキーは不変。
func TestRoundStats(t *testing.T) {
	got := roundStats(map[string]any{
		"running": true, "mem_used": uint64(8<<20 + 12345), "mem_max": uint64(1 << 30),
		"cpu_pct": 12.7, "oom_kill_total": uint64(2),
	})
	if got["mem_used"] != uint64(8<<20) {
		t.Errorf("mem_used = %v, want %v", got["mem_used"], uint64(8<<20))
	}
	if got["cpu_pct"] != 13.0 {
		t.Errorf("cpu_pct = %v, want 13", got["cpu_pct"])
	}
	if got["mem_max"] != uint64(1<<30) || got["oom_kill_total"] != uint64(2) || got["running"] != true {
		t.Errorf("untouched keys changed: %v", got)
	}
	// 揺れだけ違う 2 サンプルが同一 JSON になる（= diff 抑制が効く）こと。
	a, _ := json.Marshal(roundStats(map[string]any{"mem_used": uint64(100<<20 + 1), "cpu_pct": 3.2}))
	b, _ := json.Marshal(roundStats(map[string]any{"mem_used": uint64(100<<20 + 999999), "cpu_pct": 2.8}))
	if string(a) != string(b) {
		t.Errorf("jittered samples differ after rounding: %s vs %s", a, b)
	}
}

// TestEventsStreamPing: 無送信が ping 間隔を超えたらコメント ping を流す
// （クライアント watchdog / 中間プロキシの keep-alive）。
func TestEventsStreamPing(t *testing.T) {
	var body atomic.Value
	body.Store(`{"sessions":[]}`)
	a, res := eventsTestEnv(t, &body)
	a.ping = time.Millisecond
	_, raw := runStream(t, a, res, 100*time.Millisecond)
	if !strings.Contains(raw, ": ping") {
		t.Errorf("no ping in quiet stream; body=%q", raw)
	}
}
