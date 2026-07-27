package codex

import (
	"strings"
	"testing"
)

// stubCheapModel は seed のモデル pin を固定する（実 codex を起動させない）。
func stubCheapModel(t *testing.T, id string) {
	t.Helper()
	prev := cheapModelFn
	cheapModelFn = func() string { return id }
	t.Cleanup(func() { cheapModelFn = prev })
}

func TestMemoriesDefaultOffAndRoundTrip(t *testing.T) {
	stubCheapModel(t, "gpt-x-mini")
	original := []byte("model = \"gpt-5.6-sol\"\n\n[projects.\"/repo\"]\ntrust_level = \"trusted\"\n")
	if memoriesEnabled(original) {
		t.Fatal("absent features.memories must read as OFF — that is codex's own default")
	}
	on := setMemories(original, true)
	if !memoriesEnabled(on) {
		t.Fatalf("enabling did not round-trip:\n%s", on)
	}
	got := string(on)
	if !strings.Contains(got, "[features]\nmemories = true") {
		t.Fatalf("feature flag not written as codex expects:\n%s", got)
	}
	if !strings.Contains(got, "[projects.\"/repo\"]\ntrust_level = \"trusted\"") ||
		!strings.Contains(got, "model = \"gpt-5.6-sol\"") {
		t.Fatalf("unrelated user config was not preserved byte-for-byte:\n%s", got)
	}
	// 有効化と同時に保守的なチューニングが入る（決着 #4 のコスト懸念に対する手当）。
	for _, want := range []string{"[memories]", "min_rollout_idle_hours = 12",
		"max_rollouts_per_startup = 4", "extract_model = \"gpt-x-mini\"",
		"consolidation_model = \"gpt-x-mini\""} {
		if !strings.Contains(got, want) {
			t.Fatalf("tuning seed missing %q:\n%s", want, got)
		}
	}

	off := setMemories(on, false)
	if memoriesEnabled(off) {
		t.Fatalf("disabling did not round-trip:\n%s", off)
	}
	if strings.Count(string(off), "memories = true") != 0 ||
		strings.Count(string(off), "memories = false") != 1 {
		t.Fatalf("the feature key was duplicated instead of flipped:\n%s", off)
	}
}

// 無効化は [memories] を消さない。トグルを往復しただけで利用者の調整値が飛ぶと、
// 「元に戻したつもり」が設定の消失になる。
func TestMemoriesDisableKeepsTuning(t *testing.T) {
	stubCheapModel(t, "")
	on := setMemories(nil, true)
	off := setMemories(on, false)
	if !strings.Contains(string(off), "[memories]") ||
		!strings.Contains(string(off), "min_rollout_idle_hours = 12") {
		t.Fatalf("disabling wiped the tuning table:\n%s", off)
	}
}

// 既に [memories] がある設定は seed で一切触らない（利用者が調整した値が正）。
func TestMemoriesSeedLeavesExistingTuningAlone(t *testing.T) {
	stubCheapModel(t, "gpt-x-mini")
	original := []byte("[memories]\nmin_rollout_idle_hours = 1\nextract_model = \"mine\"\n")
	on := string(setMemories(original, true))
	if strings.Contains(on, "min_rollout_idle_hours = 12") ||
		strings.Contains(on, "gpt-x-mini") ||
		strings.Count(on, "[memories]") != 1 {
		t.Fatalf("seed overwrote or duplicated a user-tuned [memories] table:\n%s", on)
	}
	if !strings.Contains(on, "extract_model = \"mine\"") {
		t.Fatalf("user's own value was lost:\n%s", on)
	}
}

// カタログから安価なモデルを引けないときは pin を書かない（存在しないスラッグを
// 焼き込むより、codex の既定に委ねる方が壊れない）。
func TestMemoriesSeedOmitsModelPinWhenUnresolvable(t *testing.T) {
	stubCheapModel(t, "")
	on := string(setMemories(nil, true))
	if strings.Contains(on, "extract_model") || strings.Contains(on, "consolidation_model") {
		t.Fatalf("model pin was written without a resolvable catalog entry:\n%s", on)
	}
	if !strings.Contains(on, "min_rollout_idle_hours = 12") {
		t.Fatalf("the catalog-independent tuning should still be seeded:\n%s", on)
	}
}

// 既存の [features] セクションがあるときは、その中へ足す（新しい [features] を
// 増やすと後勝ちで元の値が死ぬ）。
func TestMemoriesJoinsExistingFeaturesSection(t *testing.T) {
	stubCheapModel(t, "")
	original := []byte("[features]\nhooks = true\n\n[projects.\"/repo\"]\ntrust_level = \"trusted\"\n")
	on := string(setMemories(original, true))
	if strings.Count(on, "[features]") != 1 {
		t.Fatalf("a second [features] table was appended:\n%s", on)
	}
	if !strings.Contains(on, "hooks = true") || !strings.Contains(on, "trust_level = \"trusted\"") {
		t.Fatalf("existing keys were lost:\n%s", on)
	}
	if !memoriesEnabled([]byte(on)) {
		t.Fatalf("memories key landed outside [features]:\n%s", on)
	}
}

// 2 キー同時更新でも互いを壊さない（PUT が 1 回の read-modify-write で両方を畳むため）。
func TestMemoriesAndNudgeCoexist(t *testing.T) {
	stubCheapModel(t, "")
	b := setMemories(setRateLimitModelNudge(nil, false), true)
	if rateLimitModelNudgeEnabled(b) {
		t.Fatalf("the nudge setting was clobbered by the memories write:\n%s", b)
	}
	if !memoriesEnabled(b) {
		t.Fatalf("the memories setting did not survive:\n%s", b)
	}
}
