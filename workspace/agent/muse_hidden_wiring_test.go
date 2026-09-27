package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
)

// muse picks a session's default model without reading ui-prefs itself; the hook main installs
// is what makes it see the member's hidden-models setting (#1023).
func TestMuseModelHiddenReadsTheHiddenModelsSetting(t *testing.T) {
	writeUIPrefs(t, `{"hiddenModels":{"muse":["muse-spark-1.3"]}}`)
	if !muse.ModelHidden("muse-spark-1.3") {
		t.Fatal("muse.ModelHidden ignores the saved hidden-models setting")
	}
	if muse.ModelHidden("muse-spark-1.2") {
		t.Fatal("muse.ModelHidden hides a model the setting does not name")
	}
}
