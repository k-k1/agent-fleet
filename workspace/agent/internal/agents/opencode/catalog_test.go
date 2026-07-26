package opencode

// カタログ整形（catalog.go）。実測の並び（`opencode models` は Zen 59 件のあとに Go
// 16 件を返し、うち 10 件は同名）を縮めた固定入力で、並び順・絞り込み・退避条件を固定する。

import (
	"strings"
	"testing"
)

// live は実測カタログの縮小版: Zen 側に free と同名 twin と Go に無いモデル、Go 側に
// twin と Go 専用、加えてユーザーが直結した別プロバイダ。
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

func TestCatalogAllKeepsEverythingInOrder(t *testing.T) {
	got := ids(t, CatalogAll)
	if strings.Join(got, ",") != strings.Join(live, ",") {
		t.Errorf("all = %v", got)
	}
}

// go-first: Go を先頭へ寄せるが、捨てない。Go 内・非 Go 内の相対順は保つ（安定ソート）
// ので、カタログの並びがそのまま読める。
func TestCatalogGoFirstHoistsGoAndKeepsRest(t *testing.T) {
	got := ids(t, CatalogGoFirst)
	want := []string{
		"opencode-go/deepseek-v4-pro", "opencode-go/glm-5.2", "opencode-go/kimi-k3",
		"opencode/deepseek-v4-flash-free", "opencode/claude-opus-5", "opencode/deepseek-v4-pro",
		"opencode/glm-5.2", "anthropic/claude-opus-5",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("go-first = %v", got)
	}
}

// hide-zen が落とすのは従量の opencode/… だけ。ユーザーが自分で繋いだ直結プロバイダ
// （anthropic/… 等）まで消すと、自分の鍵が使えなくなる。
func TestCatalogHideZenDropsOnlyMeteredIDs(t *testing.T) {
	got := ids(t, CatalogHideZen)
	want := []string{
		"opencode-go/deepseek-v4-pro", "opencode-go/glm-5.2", "opencode-go/kimi-k3",
		"anthropic/claude-opus-5",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("hide-zen = %v", got)
	}
}

// Go 契約が無いアカウント（opencode-go/… が 1 件も無い）で 隠す を選んでも、ピッカーを
// 空にはしない — 起動不能になるくらいなら設定を無視する。
func TestCatalogHideZenFallsBackWhenItWouldEmptyThePicker(t *testing.T) {
	zenOnly := []string{"opencode/deepseek-v4-pro", "opencode/glm-5.2"}
	got := Catalog(zenOnly, CatalogHideZen)
	if len(got) != 2 {
		t.Fatalf("got %+v, want the full list back", got)
	}
}

func TestCatalogPrefNormalizesUnknownToGoFirst(t *testing.T) {
	for _, v := range []string{"", "nonsense", "GO-FIRST"} {
		if got := CatalogPref(v); got != CatalogGoFirst {
			t.Errorf("CatalogPref(%q) = %q", v, got)
		}
	}
	for _, v := range []string{CatalogHideZen, CatalogAll} {
		if got := CatalogPref(v); got != v {
			t.Errorf("CatalogPref(%q) = %q", v, got)
		}
	}
}

// 空カタログ（CLI 不在 / オフライン）は空のまま返す — 呼び出し側は「既定のみ」を出す。
func TestCatalogEmptyStaysEmpty(t *testing.T) {
	if got := Catalog(nil, CatalogHideZen); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
