package mcpx

// Equivalence proof for agents.EffortPolicy: the catalog-free part of checkCreateEffort against a
// verbatim copy of the kind switch it replaced, over every registered kind x model.

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// oldEffortPrecheck is checkCreateEffort up to (not including) the empty-model refusal and the
// catalog read, as it was before the capability.
func oldEffortPrecheck(kind, model string) (decided bool, msg string) {
	if kind == "" {
		kind = "claude"
	}
	switch kind {
	case "agy", "cursor":
		return true, kind + " は effort を別に指定できません。effort はモデル id に含まれているので、list_models から目的の effort の id を選んでください"
	case "lcpp":
		return true, "lcpp は effort を指定できません（推論量はエンジン側の起動設定で決まります）"
	case "shell", "ssm":
		return true, kind + " は effort を指定できません"
	case "opencode", "kiro":
		return true, ""
	}
	if kind == "copilot" && (model == "" || strings.EqualFold(model, "auto")) {
		return true, "copilot の auto は effort を指定できません。effort を使うなら list_models から model も指定してください"
	}
	return false, ""
}

func TestEffortPrecheckMatchesOldSwitch(t *testing.T) {
	kinds := append(agentreg.Kinds(), "", "no-such-kind", "Codex", " copilot")
	models := []string{"", "auto", "AUTO", "Auto", "gpt-x", "claude-sonnet-4"}
	checked := 0
	for _, k := range kinds {
		for _, m := range models {
			wantDecided, wantMsg := oldEffortPrecheck(k, m)
			kk := k
			if kk == "" {
				kk = "claude"
			}
			decided, err := effortPrecheck(kk, m)
			gotMsg := ""
			if err != nil {
				gotMsg = err.Error()
			}
			if decided != wantDecided || gotMsg != wantMsg {
				t.Errorf("effortPrecheck(kind=%q model=%q) = %v/%q, old switch %v/%q", k, m, decided, gotMsg, wantDecided, wantMsg)
			}
			checked++
		}
	}
	if want := len(kinds) * len(models); checked != want {
		t.Fatalf("checked %d combinations, want %d", checked, want)
	}
}

// A kind added later must not silently fall through to the catalog read with a wrong rule: every
// kind that declares a non-catalog rule is one the old switch named.
func TestEffortPolicyDeclaredOnlyWhereOldSwitchNamed(t *testing.T) {
	for _, k := range agentreg.Kinds() {
		a, _ := agentreg.Lookup(k)
		p, ok := a.(agents.EffortPolicy)
		if !ok {
			continue
		}
		tr := p.EffortTraits()
		if tr.Rule == agents.EffortByCatalog && !tr.AutoRejectsEffort {
			continue
		}
		named := false
		for _, m := range []string{"", "auto", "gpt-x"} {
			if d, _ := oldEffortPrecheck(k, m); d {
				named = true
			}
		}
		if !named {
			t.Errorf("kind %q declares a non-catalog effort rule or AutoRejectsEffort the old switch did not have", k)
		}
	}
}
