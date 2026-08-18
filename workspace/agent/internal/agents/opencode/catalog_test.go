package opencode

// カタログ整形（catalog.go）。実測の並び（Zen 61 件のあとに Go 18 件が続き、うち何件かは
// 同名）を縮めた固定入力で、枠ごとの絞り込み・並び順・退避条件を固定する。

import (
	"strings"
	"testing"
)

// live は実測カタログの縮小版: Zen 側に無料モデルと Go と同名の twin と Go に無いモデル、
// Go 側に twin と Go 専用、加えてユーザーが直結した別プロバイダ。
var live = []string{
	"opencode/deepseek-v4-flash-free",
	"opencode/claude-opus-5",
	"opencode/deepseek-v4-pro",
	"opencode/glm-5.2",
	"anthropic/claude-opus-5",
	"opencode-go/deepseek-v4-pro",
	"opencode-go/glm-5.2",
	"opencode-go/kimi-k3",
}

func ids(t *testing.T, pref string) []string {
	t.Helper()
	var out []string
	for _, c := range Catalog(live, pref) {
		if c.ID != c.Label {
			t.Errorf("label must stay the raw id (Console localizes): %+v", c)
		}
		out = append(out, c.ID)
	}
	return out
}

// withFreeIDs pins the zero-cost set the way a daemon read would leave it.
func withFreeIDs(t *testing.T, free ...string) {
	t.Helper()
	m := map[string]bool{}
	for _, id := range free {
		m[id] = true
	}
	modelsMu.Lock()
	prev := freeIDs
	freeIDs = m
	modelsMu.Unlock()
	t.Cleanup(func() {
		modelsMu.Lock()
		freeIDs = prev
		modelsMu.Unlock()
	})
}

// Zen: opencode.ai 側は絞らない（Go を併用していれば両方出る）。Go を先頭へ寄せるが
// 捨てず、群の中の相対順は保つ（安定ソート）ので、カタログの並びがそのまま読める。
func TestCatalogZenKeepsBothRoutesGoFirst(t *testing.T) {
	got := ids(t, UsageZen)
	want := []string{
		"opencode-go/deepseek-v4-pro", "opencode-go/glm-5.2", "opencode-go/kimi-k3",
		"opencode/deepseek-v4-flash-free", "opencode/claude-opus-5", "opencode/deepseek-v4-pro",
		"opencode/glm-5.2", "anthropic/claude-opus-5",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("zen = %v", got)
	}
}

// Go のみ: 落とすのは従量の opencode/… だけ。ユーザーが自分で繋いだ直結プロバイダ
// （anthropic/… 等）まで消すと、自分の鍵が使えなくなる。
func TestCatalogGoDropsOnlyMeteredIDs(t *testing.T) {
	got := ids(t, UsageGo)
	want := []string{
		"opencode-go/deepseek-v4-pro", "opencode-go/glm-5.2", "opencode-go/kimi-k3",
		"anthropic/claude-opus-5",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("go = %v", got)
	}
}

// 無料枠: opencode.ai 側は無料モデルだけ。ここでも直結プロバイダは別課金なので残す。
func TestCatalogFreeKeepsZeroCostAndDirectProviders(t *testing.T) {
	withFreeIDs(t, "opencode/deepseek-v4-flash-free")
	got := ids(t, UsageFree)
	want := []string{"opencode/deepseek-v4-flash-free", "anthropic/claude-opus-5"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("free = %v", got)
	}
}

// 価格を知らない（CLI 由来で freeIDs が空）ときは素通し。無料枠では
// OPENCODE_API_KEY を注入しないので、その CLI が返す opencode.ai の一覧は
// もともと無料枠のものだけになる（実測）。ここで全部落とすと空になってしまう。
func TestCatalogFreeWithoutCostDataPassesThrough(t *testing.T) {
	withFreeIDs(t) // 空 = 未知
	if got := ids(t, UsageFree); len(got) != len(live) {
		t.Errorf("free(価格不明) = %v, want すべて素通し", got)
	}
}

// Go 契約が無いアカウント（opencode-go/… が 1 件も無い）で Go のみ を選んでも、
// ピッカーを空にはしない — 起動不能になるくらいなら設定を無視する。
func TestCatalogFallsBackWhenItWouldEmptyThePicker(t *testing.T) {
	zenOnly := []string{"opencode/deepseek-v4-pro", "opencode/glm-5.2"}
	if got := Catalog(zenOnly, UsageGo); len(got) != 2 {
		t.Fatalf("got %+v, want the full list back", got)
	}
	withFreeIDs(t, "opencode/nothing-here")
	if got := Catalog(zenOnly, UsageFree); len(got) != 2 {
		t.Fatalf("free: got %+v, want the full list back", got)
	}
}

// 旧値からの移行: 「Zen を隠す」は Go だけ見たいという意思、「Go 優先」「すべて表示」は
// 両方見たいという意思。未設定/不明は明示的に選ぶまで無効（Off）に倒す。
func TestCatalogPrefMigratesLegacyValues(t *testing.T) {
	for v, want := range map[string]string{
		"hide-zen": UsageGo,
		"go-first": UsageZen,
		"all":      UsageZen,
		"":         UsageOff,
		"nonsense": UsageOff,
		"FREE":     UsageOff,
		UsageFree:  UsageFree,
		UsageGo:    UsageGo,
		UsageZen:   UsageZen,
		UsageOff:   UsageOff,
	} {
		if got := CatalogPref(v); got != want {
			t.Errorf("CatalogPref(%q) = %q, want %q", v, got, want)
		}
	}
}

// off は「一切使わない」の明示的な宣言なので、Catalog の空ピッカー救済（本来
// 起動不能を避けるための Zen フォールバック）の対象外 — 空のままが正しい。他社
// プロバイダの id（opencode.ai 経由でない）も含め、何も出さない。
func TestCatalogOffStaysEmpty(t *testing.T) {
	ids := []string{"opencode/deepseek-v4-pro", "opencode-go/glm-5.2", "anthropic/claude-opus-5"}
	if got := Catalog(ids, UsageOff); len(got) != 0 {
		t.Errorf("off = %+v, want 空", got)
	}
}

// 空カタログ（CLI 不在 / オフライン）は空のまま返す — 呼び出し側は「既定のみ」を出す。
func TestCatalogEmptyStaysEmpty(t *testing.T) {
	if got := Catalog(nil, UsageGo); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
