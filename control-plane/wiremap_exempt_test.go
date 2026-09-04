// wiremap_exempt_test.go — struct 化しない map サイトの免除表と、**免除の寿命の逆検査**。
//
// 🔴 README §4: 免除表を足すなら、**免除が要らなくなったら外させる逆検査を同じコミットで**
// 入れる。入れないと「免除だから見ない」が積み上がり、検査が名前だけになる。
// #339 では**免除が要らなくなる 2 つの道のうち片方が緑のまま**だったのが【要対応】になった。
// ここは最初から**両方向**で書く。
//
// 免除の理由は 1 件ずつ書く。理由の無い免除は、次の人には穴と区別が付かない。
package main

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/wiretest"
)

// wiremapExemption — 「この map サイトは struct 化しない」という宣言。
type wiremapExemption struct {
	// Func はゴールデンの 2 列目（<レシーバ>.<メソッド> か関数名）。
	Func string
	// Why は免除の理由。**書けないなら免除ではなく、まだ調べていないだけ。**
	Why string
	// NeedsFlag は「その理由がゴールデン上でどう見えるはずか」。
	// dyn = キー集合が静的に確定しない / partial = 開けきれていない。
	// 🔴 これが**逆検査の軸**になる: 印が消えたら理由も消えている。
	NeedsFlag string
}

// wiremapExemptions — CP 側で struct 化しないと決めたサイト。
//
// ⚠️ ここに無い map サイトは「まだ変換していない」だけであって、免除ではない。
// 免除は「**構造的に変換できない**」ものに限る。
func wiremapExemptions() []wiremapExemption {
	return []wiremapExemption{
		{
			Func: "workspaceAPI.stats",
			// metrics.go の workspaceStats は containerStats() の map に
			// **Agent から来た map を for k, v := range で丸ごと合流**させる。
			// キー集合は Agent が何を返したかで決まるので静的に確定せず、
			// struct 化すると **Agent が足したフィールドが無言で消える**。
			// wire_golden_test.go 冒頭のコメントも「撮れない経路」として名指ししている。
			Why:       "Agent 応答の map を range で合流させるためキー集合が静的に確定しない",
			NeedsFlag: "dyn",
		},
		{
			Func: "adminAPI.memberStats",
			// 同じ workspaceStats を通るので同じ理由。
			Why:       "workspaceAPI.stats と同じ workspaceStats を経由する",
			NeedsFlag: "dyn",
		},
		{
			Func: "sessionShareAPI.messages",
			// 所有者 Workspace の Agent から受けた JSON を allowlist で絞って
			// そのまま中継する。キーは相手の応答で決まる。
			Why:       "所有者 Agent の応答をそのまま中継するためキー集合が静的に確定しない",
			NeedsFlag: "dyn",
		},
	}
}

// exemptionStillNeeded — **免除がまだ必要か**の判定。
//
// 🔴 **この関数が唯一の実装であることが重要。** 本物の逆検査
// （TestWiremapExemptionsAreStillNeeded）と、その逆検査自身を検査する対照
// （TestWiremapExemptionReverseCheckActuallyFires）の**両方がここを呼ぶ**。
//
// 以前はこの判定が 2 箇所に写しで在り、**出荷される側を潰しても対照は緑のまま**だった
// （#345 のレビューで実測: 本物の `if !still {` を `if false {` にしても免除まわり 4 本すべて PASS）。
// **検証する側とされる側が二重化すると、写しだけが検査される。**
//
// 戻り値: (まだ必要か, 必要でないと判断した理由)
func exemptionStillNeeded(ex wiremapExemption, byFunc map[string][]wireMapSite) (bool, string) {
	got, ok := byFunc[ex.Func]
	if !ok {
		return false, "対象サイトがもう存在しない（変換済み・削除済みなら免除表からも消すこと）"
	}
	for _, s := range got {
		switch ex.NeedsFlag {
		case "dyn":
			if s.DynKey {
				return true, ""
			}
		case "partial":
			if s.Partial {
				return true, ""
			}
		}
	}
	return false, ex.NeedsFlag + " 印が付いていない（キー集合が確定するようになったか、走査が痩せた）"
}

// wiremapSitesByFunc は走査結果を関数名で引ける形にする。
func wiremapSitesByFunc(t *testing.T) map[string][]wireMapSite {
	t.Helper()
	byFunc := map[string][]wireMapSite{}
	for _, s := range scanWireMapSites(t, ".") {
		byFunc[s.Func] = append(byFunc[s.Func], s)
	}
	return byFunc
}

// TestWiremapExemptionsAreStillNeeded — **逆検査その 1（Go 側の方向）**。
//
// 免除の理由は「キー集合が静的に確定しない」こと。その根拠はゴールデンの `dyn` / `partial` 印。
// 🔴 **印が消えたら、免除の理由も消えている**——それは
//
//	(a) 実装が変わってキーが確定するようになった（＝変換できるので免除を外すべき）か
//	(b) 走査が痩せて印を落とした（＝道具が壊れている）
//
// のどちらかで、**どちらも黙って通してはいけない。**
func TestWiremapExemptionsAreStillNeeded(t *testing.T) {
	reportStaleExemptions(t, wiremapExemptions(), wiremapSitesByFunc(t))
}

// reportStaleExemptions — **出荷される逆検査の本体**（判定 ＋ 報告）。
//
// 🔴 テスト関数ではなくここに本体を置くのは、**対照がこれを最後まで駆動できるようにする**ため。
// 判定だけを共有して報告をテスト関数に書くと、**報告側（t.Errorf）を消しても対照は緑のまま**
// になり、#345 で指摘された穴が形を変えて残る。
// 引数の t を interface にしてあるので、対照は wiretest.Recorder を渡して「実際に報告されたか」を見る。
func reportStaleExemptions(t wiretest.TB, exs []wiremapExemption, byFunc map[string][]wireMapSite) {
	t.Helper()
	for _, ex := range exs {
		if ok, why := exemptionStillNeeded(ex, byFunc); !ok {
			t.Errorf("免除 %q はもう要らない（理由: %s）。\n"+
				"  免除の根拠は %q だった。\n"+
				"  実装が変わってキー集合が確定するようになったなら**免除を外して変換する**。\n"+
				"  走査が痩せて印を落としたなら**道具を直す**。どちらにせよこのまま緑にはしない。",
				ex.Func, why, ex.Why)
		}
	}
}

// wiremapDeferred — **免除のもう一方の種類**: 構造的には変換できるのに、
// この PR では変換しないと決めたサイト。
//
// 🔴 構造的免除（上）とは寿命の切れ方が違うので、逆検査も別に要る。
// 上は「理由が消えたか」を見るが、こちらは「**まだ変換されていないか**」を見る。
// これを機械で持たないと、**変換候補が計画から静かに落ちる**（誰も気付かない）。
var wiremapDeferred = []wiremapExemption{
	{
		Func: "registerAuthRoutes",
		// routes.go:141 の DeploymentVersion。Console に手書き型が在る J=1.0 の
		// 変換候補だが、registerAuthRoutes 内のインライン map で共有度が高く、
		// 司令塔判断で owners.tsv の CONTRACT-MAP から外れている。
		Why: "control-plane/routes.go は CONTRACT-MAP の所有外（司令塔判断・共有度が高い）",
	},
}

// TestWiremapDeferredAreStillMaps — **逆検査その 2（保留の方向）**。
//
// 保留したサイトが**もう map ではなくなっていたら**、誰かが変換したか消したかで、
// 保留の理由は無くなっている。**表から外させる。**
// 🔴 これが無いと「所有外だから保留」がそのまま化石になり、
// **変換候補が計画から消えたことに誰も気付かない。**
func TestWiremapDeferredAreStillMaps(t *testing.T) {
	sites := scanWireMapSites(t, ".")
	byFunc := map[string]bool{}
	for _, s := range sites {
		byFunc[s.Func] = true
	}
	for _, d := range wiremapDeferred {
		if !byFunc[d.Func] {
			t.Errorf("保留 %q はもう map サイトではない（%s）。\n"+
				"  変換済み・削除済みなら**保留表から外すこと**。"+
				"残すと変換候補が計画から静かに落ちる。", d.Func, d.Why)
		}
		if strings.TrimSpace(d.Why) == "" {
			t.Errorf("保留 %q に理由が無い", d.Func)
		}
	}
}

// TestWiremapExemptionsHaveReasons — 理由の無い免除を作らせない。
// 🔴 理由が書けない免除は「調べていない」であって免除ではない。
func TestWiremapExemptionsHaveReasons(t *testing.T) {
	seen := map[string]bool{}
	for _, ex := range wiremapExemptions() {
		if strings.TrimSpace(ex.Why) == "" {
			t.Errorf("免除 %q に理由が無い", ex.Func)
		}
		if ex.NeedsFlag != "dyn" && ex.NeedsFlag != "partial" {
			t.Errorf("免除 %q の NeedsFlag が dyn/partial のどちらでもない: %q", ex.Func, ex.NeedsFlag)
		}
		if seen[ex.Func] {
			t.Errorf("免除 %q が重複している", ex.Func)
		}
		seen[ex.Func] = true
	}
	if len(wiremapExemptions()) == 0 {
		t.Fatal("免除が 0 件（表が空なら逆検査は何も見ていない）")
	}
}

// TestWiremapExemptionReverseCheckActuallyFires — **逆検査を検査する。**
//
// 🔴 README §4:「緑」は検査が対象を拾っていることを確かめてから採用する。
// 逆検査は**免除が要らなくなったときだけ**赤くなる仕掛けなので、
// 平常時は必ず緑＝**壊れていても緑**と区別が付かない。
// そこで「理由が裏付けられない免除」を**合成して**当て、実際に赤くなることを見る。
func TestWiremapExemptionReverseCheckActuallyFires(t *testing.T) {
	byFunc := wiremapSitesByFunc(t)
	// 🔴 **出荷される逆検査を最後まで駆動する。**判定を写して書き直すと、
	// 本物を潰しても対照が緑のままになる（#345 のレビューで実測された穴）。
	// wiretest.Recorder を渡すので、判定と報告の**どちらを潰しても**この対照が赤くなる。
	check := func(ex wiremapExemption) bool { // true = 実際に報告された＝赤くなるべき
		rec := &wiretest.Recorder{}
		reportStaleExemptions(rec, []wiremapExemption{ex}, byFunc)
		return len(rec.Errs()) > 0
	}

	t.Run("対象が消えた免除は赤くなる", func(t *testing.T) {
		if !check(wiremapExemption{Func: "存在しない.ハンドラ", Why: "合成", NeedsFlag: "dyn"}) {
			t.Error("対象が居ない免除を素通しした＝「変換済みなのに免除が残っている」を捕まえられない")
		}
	})

	t.Run("印が裏付けない免除は赤くなる", func(t *testing.T) {
		// dyn 印を持たない実在サイトを 1 つ選び、dyn を理由にした免除を合成する。
		var plain string
		for fn, ss := range byFunc {
			clean := true
			for _, s := range ss {
				if s.DynKey || s.Partial {
					clean = false
				}
			}
			if clean {
				plain = fn
				break
			}
		}
		if plain == "" {
			t.Skip("dyn/partial を持たないサイトが 1 つも無い（対照を作れない）")
		}
		if !check(wiremapExemption{Func: plain, Why: "合成", NeedsFlag: "dyn"}) {
			t.Errorf("%s は dyn 印を持たないのに dyn を理由にした免除を素通しした", plain)
		}
	})

	t.Run("本物の免除は通る", func(t *testing.T) {
		// 陰性だけでなく陽性も見る。全部赤くする検査は何も守らない。
		for _, ex := range wiremapExemptions() {
			if check(ex) {
				t.Errorf("実在する免除 %q を赤にした（検査が厳しすぎる）", ex.Func)
			}
		}
	})
}
