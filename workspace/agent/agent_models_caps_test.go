package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// capabilityModels is the agents.ModelLister path that replaces kindModels' kind switch
// (#2870), in kindModels' shape so the two can be compared.
func capabilityModels(ctx context.Context, kind string, claudeCustom []string, catalogPref string) (kindModelList, bool) {
	a, _ := sessionx.RegisteredAgent(kind)
	ml, ok := a.(agents.ModelLister)
	if !ok {
		return kindModelList{}, false
	}
	l := ml.ListModels(agents.ModelQuery{Ctx: ctx, CustomModels: claudeCustom, CatalogPref: catalogPref})
	return kindModelList{list: l.Models, enumerated: l.Enumerated, opencodeIDs: l.Listed, route: l.Route}, true
}

// Before the switch goes, every kind (and every spelling that is not one) must get the same
// menu from its capability as from the switch arm. The codex and agy CLIs are stand-ins that
// answer distinct catalogs, so a capability that delegates to the wrong kind's source differs;
// the others find no CLI and answer empty on both paths.
func TestModelListerEqualsTheKindSwitch(t *testing.T) {
	bin := t.TempDir()
	for name, script := range map[string]string{
		"codex": `[ "$1 $2" = "debug models" ] || exit 1
printf '%s' '{"models":[{"slug":"gpt-fake-1","display_name":"Fake One","visibility":"list","priority":1},{"slug":"gpt-fake-2","visibility":"list","priority":2}]}'`,
		"agy": `[ "$1" = models ] || exit 1
printf 'agy-fake-a\tAgy Fake A\nagy-fake-b\n'`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())

	custom := []string{"claude-opus-4-8", "OPUS", "claude-sonnet-9"}
	kinds := append(sessionx.RegisteredKinds(), "", "Claude", " codex", "llama.cpp", "pi")
	rich := 0
	for _, k := range kinds {
		want, wantOK := kindModels(context.Background(), k, custom, "go")
		got, gotOK := capabilityModels(context.Background(), k, custom, "go")
		if gotOK != wantOK {
			t.Errorf("%q: capability has a catalog = %v, the switch = %v", k, gotOK, wantOK)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: capability answers %+v, the switch %+v", k, got, want)
		}
		if len(want.list) > 0 {
			rich++
		}
	}
	// claude, codex and agy at least; fewer means the stand-ins were not reached and the
	// comparison above proved little.
	if rich < 3 {
		t.Fatalf("only %d kinds answered a non-empty menu; the comparison is not exercising the sources", rich)
	}
}
