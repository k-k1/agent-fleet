package codex

import (
	"strings"
	"testing"
)

// stubCheapModel pins the model the seed writes, so no real codex is launched.
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
	// Enabling also seeds conservative tuning, the hedge against resolution #4's cost concern.
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

// Disabling must not delete [memories]. If a round trip of the toggle dropped the user's
// tuned values, "I just put it back" would silently mean losing the settings.
func TestMemoriesDisableKeepsTuning(t *testing.T) {
	stubCheapModel(t, "")
	on := setMemories(nil, true)
	off := setMemories(on, false)
	if !strings.Contains(string(off), "[memories]") ||
		!strings.Contains(string(off), "min_rollout_idle_hours = 12") {
		t.Fatalf("disabling wiped the tuning table:\n%s", off)
	}
}

// A config that already has [memories] is left entirely alone by the seed: the value the
// user tuned wins.
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

// With no cheap model resolvable from the catalog, no pin is written: leaving codex to its
// own default breaks less than baking in a slug that does not exist.
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

// An existing [features] section is added to in place; appending a second [features] table
// would let the later one win and kill the original values.
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

// Updating both keys must not clobber either, since one PUT folds them into a single
// read-modify-write.
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
