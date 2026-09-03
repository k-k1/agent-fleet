package main

// managed runtime（共有 daemon）の起動失敗を Console へ返すときの分類。
//
// もとは失敗が種類を問わず `502 runtime_failed` 1 本で、Console はそれを
// 「エージェントを起動できませんでした。しばらく待ってから再試行してください。」と訳す。
// docs/log/27 の共有 daemon が「未認証なら起こさない」ゲート（codex.ErrNotLoggedIn /
// opencode.ErrNotConnected）を持つようになってから、**待っても直らない恒久的な原因が
// 「しばらく待って再試行」として出る**ようになった —— 利用者は起動が通るまで何度も
// 押し続けることになり、原因（ログインしていない）は画面のどこにも出ない。
//
// そこでゲート由来の 2 つだけを切り出して、**恒久 = 4xx + 専用コード**、
// **一時 = 502 runtime_failed**（従来どおり）に分ける。4xx にするのは文言のためだけでは
// なく、Console の isTransientErr（core/api/client.ts）が 5xx を「再試行してよい失敗」と
// 判定するため —— 恒久要因を 5xx で返すと、文言を直しても再試行の対象のままになる。
//
// 分類に使うのは各 package が export しているセンチネルだけ。メッセージの文字列一致は
// 使わない（上流の文言が変わった瞬間に黙って一時扱いへ戻るため）。

import (
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// writeRuntimeErr は managed runtime の起動失敗を HTTP へ落とす。ゲート由来（未ログイン／
// 未接続）は 409 + errCodeAgentNotConnected、それ以外は従来どおり 502 + runtime_failed。
// message は両方とも err.Error() ——「なぜ」は汎用コードでは表せないので、Console 側は
// errDetail() で併記する。
func writeRuntimeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, codex.ErrNotLoggedIn) || errors.Is(err, opencode.ErrNotConnected) {
		httpx.WriteErr(w, http.StatusConflict, errCodeAgentNotConnected, err.Error())
		return
	}
	httpx.WriteErr(w, http.StatusBadGateway, "runtime_failed", err.Error())
}
