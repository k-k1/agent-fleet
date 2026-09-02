// events.go — Console 向け統合 push チャネル（通信量削減 P3）。
//
// GET /api/events は SSE (text/event-stream) で、Console が従来 4〜5 秒毎に
// 別々に叩いていた常時ポーリング 4 本（workspace / sessions / stats /
// notifications）を 1 コネクションに集約する。サーバ側で同じ payload 合成関数
// （workspacePayload / sessionsPayload / containerStats / listPayload）を
// 接続毎の tick で回し、直前に送った JSON と変わった stream だけをフレームで
// 送る — 無変化 tick はブラウザ↔CP 間のバイトがゼロになる（モバイル回線の
// リクエストヘッダ/cookie 往復が主なコストなので、304 ポーリングよりさらに軽い）。
//
// フレームは `data: {"stream":"<name>","data":<REST と同一 shape>}\n\n`。
// shape を既存 REST 応答と揃えることで、Console のストア適用ロジックを
// ポーリング fallback と共用できる（クライアントは旧 CP では 404 を受けて
// 従来のポーリングに自動フォールバックする — 版ずれ耐性）。
//
// gzip / etagJSON 両ミドルウェアは text/event-stream を素通しする（gzip.go /
// etag.go 参照）。認証は他の REST と同じ withResolved ゲート（cookie +
// X-AF-Tenant ヘッダ）。ポーリング同様、この接続は idle クロックに触れない
// （開きっぱなしのタブが idle-reaper の停止判断を妨げてはならない）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

const (
	eventsTick      = 4 * time.Second  // 従来の Console ポーリングと同じ床
	eventsPingEvery = 20 * time.Second // 無送信が続いたらコメント ping で死活を示す
)

type eventsAPI struct {
	memberAuth
	ws    workspaceAPI
	notif notificationAPI
	wi    workItemsAPI
	tick  time.Duration
	ping  time.Duration
}

func newEventsAPI(m *manager, autostart bool) eventsAPI {
	return eventsAPI{memberAuth{m}, newWorkspaceAPI(m, autostart), newNotificationAPI(m),
		newWorkItemsAPI(m), eventsTick, eventsPingEvery}
}

func registerEventsRoutes(mux *http.ServeMux, cfg config) {
	ev := newEventsAPI(cfg.mgr, cfg.autostart)
	mux.HandleFunc("GET /api/events", ev.withResolved(ev.stream))
}

// statsPayload rounds the jittery gauges before diffing: memory.current moves
// by bytes on every read and cpu_pct by fractions, so diffing the raw values
// would push a stats frame every tick and defeat the suppression. The WS-bar
// chip displays rounded percent / 0.1 GiB anyway — an 8 MiB floor and integer
// CPU percent lose nothing visible. The REST endpoint keeps serving raw values.
//
// state はこの tick で workspacePayload が既に引いた State をそのまま渡す
// （docs/log/63 §63.9）。ecs-ec2 の State() は DescribeVolumes + DescribeServices の
// 実 API 呼び出しで、購読者 1 人 × 4 秒ごとに走る——同じ tick の中で 2 度引けば
// その AWS 呼び出しも 2 倍になる。値は同じなのだから 1 回でよい。
func statsPayload(ctx context.Context, m *manager, rt runtime.Runtime, state string) map[string]any {
	return roundStats(workspaceStats(ctx, m, rt, func() string { return state }))
}

func roundStats(m map[string]any) map[string]any {
	if v, ok := m["mem_used"].(uint64); ok {
		m["mem_used"] = v &^ (8<<20 - 1)
	}
	if v, ok := m["cpu_pct"].(float64); ok {
		m["cpu_pct"] = math.Round(v)
	}
	return m
}

// stream serves one subscriber: initial full snapshot, then diff-only pushes.
// Returns when the client disconnects (request context cancellation).
func (a eventsAPI) stream(w http.ResponseWriter, r *http.Request, res *resolved) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "internal", "streaming unsupported"})
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	ctx := r.Context()
	last := map[string]string{}
	lastWrite := time.Now()
	// emit sends one stream's frame when its serialized payload changed since the
	// last send. Go の json.Marshal は map キーをソートするので、内容が同じなら
	// バイト列も同じ — 素朴な文字列比較で diff できる。
	emit := func(stream string, payload any) bool {
		// この tick の payload を組む途中で切断されたなら、それは「変化」ではなく
		// 中断。sessions は Agent への HTTP が ctx キャンセルで失敗すると DB ミラー
		// へフォールバックする（別 shape）ので、ここで弾かないと**購読者がもう居ない
		// のに**「セッションが変わった」フレームを 1 本書き、last[] まで汚す。
		if ctx.Err() != nil {
			return false
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if last[stream] == string(b) {
			return false
		}
		last[stream] = string(b)
		frame, _ := json.Marshal(map[string]any{"stream": stream, "data": json.RawMessage(b)})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
		return true
	}
	tickAll := func() {
		state := res.rt.State(ctx)
		wrote := emit("workspace", a.ws.workspacePayload(ctx, res, state))
		wrote = emit("stats", statsPayload(ctx, a.mgr, res.rt, state)) || wrote
		wrote = emit("sessions", a.ws.sessionsPayload(ctx, res)) || wrote
		// 通知 drain の一時失敗（DB エラー等）はこの tick を落とすだけで
		// ストリーム自体は生かす — 次の tick で回復する。
		if p, aerr := a.notif.listPayload(ctx, res); aerr == nil {
			wrote = emit("notifications", p) || wrote
		}
		// 作業項目（docs/log/80）。この payload は DB のキャッシュを読むだけで、
		// プロバイダへの取得は refreshAsync が別 goroutine で回す —— tick が
		// 外部 API の往復を待つと、この購読者の他の stream まで丸ごと止まる。
		if p, aerr := a.wi.workItemsPayload(ctx, res, state); aerr == nil {
			wrote = emit("workitems", p) || wrote
		}
		if wrote {
			lastWrite = time.Now()
		} else if time.Since(lastWrite) >= a.ping {
			_, _ = fmt.Fprint(w, ": ping\n\n")
			lastWrite = time.Now()
			wrote = true
		}
		if wrote {
			fl.Flush()
		}
	}
	tickAll()
	t := time.NewTicker(a.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tickAll()
		}
	}
}
