package sessionx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// oldLiveCatalogChain is the if/else chain checkModel held before liveCatalogs, a deliberate
// literal copy; it answers whether the model was accepted.
func oldLiveCatalogChain(c *createRun) bool {
	if NormalizeKind(c.req.Kind) == session.KindCodex && strings.TrimSpace(c.req.Model) != "" {
		model, err := resolveLiveModel(c.req.Model, FilterVisibleModels(session.KindCodex, codexModelsFn()))
		if err != nil {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", err.Error())
			return false
		}
		c.req.Model = model
	} else if NormalizeKind(c.req.Kind) == session.KindCopilot && strings.TrimSpace(c.req.Model) != "" {
		model, err := resolveLiveModel(c.req.Model, FilterVisibleModels(session.KindCopilot, copilotModelsFn()))
		if err != nil {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", err.Error())
			return false
		}
		c.req.Model = model
	} else if NormalizeKind(c.req.Kind) == session.KindOpencode && strings.TrimSpace(c.req.Model) != "" {
		ids := VisibleModelIDs(session.KindOpencode, opencodeModelsFn())
		choices := make([]agents.ModelChoice, 0, len(ids))
		for _, id := range ids {
			choices = append(choices, agents.ModelChoice{ID: id, Label: id})
		}
		model, err := resolveLiveModel(c.req.Model, choices)
		if err != nil {
			if requested := strings.TrimSpace(c.req.Model); opencodeRetiredFn(requested) {
				httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", retiredModelError(requested, choices))
				return false
			}
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", err.Error())
			return false
		}
		c.req.Model = model
	} else if NormalizeKind(c.req.Kind) == session.KindLcpp && strings.TrimSpace(c.req.Model) != "" {
		var choices []agents.ModelChoice
		if LcppLiveModels != nil {
			choices = FilterVisibleModels(session.KindLcpp, LcppLiveModels(c.r.Context()))
		}
		model, err := resolveLiveModel(c.req.Model, choices)
		if err != nil {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", err.Error())
			return false
		}
		c.req.Model = model
	}
	return true
}

// The table answers what the chain did for every registered kind, alias spellings and an
// unregistered kind, for a short name, an exact id, a miss, a retired id and no model at all.
func TestLiveCatalogsMatchOldChain(t *testing.T) {
	p := newCreateProbe(t)
	plantCatalogs(p)
	LcppLiveModels = func(context.Context) []agents.ModelChoice { return []agents.ModelChoice{{ID: "qwen3-8b"}} }
	kinds := append([]string{"", "unknown-kind", "Codex", " copilot", "OPENCODE"}, agentreg.Kinds()...)
	models := []string{"", "  ", "sol", "gpt-5.5", "claude-sonnet", "pickle", "opencode/grok-code", "opencode/old-free", "qwen3", "nope"}
	live := 0
	for _, kind := range kinds {
		for _, model := range models {
			run := func(f func(*createRun) bool) (string, int, string, bool) {
				rec := httptest.NewRecorder()
				c := &createRun{w: rec, r: httptest.NewRequest(http.MethodPost, "/sessions", nil),
					req: &CreateReq{Kind: kind, Model: model}}
				ok := f(c)
				return c.req.Model, rec.Code, rec.Body.String(), ok
			}
			wm, wc, wb, wok := run(oldLiveCatalogChain)
			gm, gc, gb, gok := run((*createRun).resolveLiveCatalogModel)
			if gm != wm || gc != wc || gb != wb || gok != wok {
				t.Errorf("kind %q model %q: got (%q,%d,%q,%v), old chain (%q,%d,%q,%v)", kind, model, gm, gc, gb, gok, wm, wc, wb, wok)
			}
			if wm != model && wok {
				live++
			}
		}
	}
	if live == 0 {
		t.Fatal("no probe was expanded by a live catalog; the comparison proves nothing")
	}
}
