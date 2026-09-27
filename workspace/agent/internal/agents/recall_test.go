package agents

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestRecalledSettingsApply(t *testing.T) {
	base := session.Meta{Model: "sonnet", Effort: "low", Mode: "plan"}
	cases := []struct {
		name    string
		r       RecalledSettings
		want    session.Meta
		changed bool
	}{
		{"nothing recorded keeps the meta", RecalledSettings{}, base, false},
		{"same values are not a change", RecalledSettings{Model: "sonnet", Effort: "low", Mode: "plan"}, base, false},
		{"switched model", RecalledSettings{Model: "opus"}, session.Meta{Model: "opus", Effort: "low", Mode: "plan"}, true},
		{"switched effort and mode", RecalledSettings{Effort: "high", Mode: "normal"}, session.Meta{Model: "sonnet", Effort: "high", Mode: "normal"}, true},
		{"back to the CLI default", RecalledSettings{ClearModel: true, ClearEffort: true}, session.Meta{Mode: "plan"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := base
			if got := c.r.Apply(&m); got != c.changed {
				t.Errorf("changed = %v, want %v", got, c.changed)
			}
			if m.Model != c.want.Model || m.Effort != c.want.Effort || m.Mode != c.want.Mode {
				t.Errorf("meta = %+v, want %+v", m, c.want)
			}
		})
	}
}

func TestRecalledSettingsApplyNormalOverUnsetMode(t *testing.T) {
	m := session.Meta{Model: "sonnet"}
	if (RecalledSettings{Mode: "normal"}).Apply(&m) || m.Mode != "" {
		t.Errorf("normal over an unset mode changed the meta: %+v", m)
	}
}
