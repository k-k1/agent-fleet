package usagesvc

// usagesvc's unit tests have no package main, so the outward dependencies are wired here with
// the real values' shape. main's usage_wiring_test.go proves the production wiring against the
// real functions.

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func init() { Configure(testDeps()) }

func testDeps() Deps {
	return Deps{
		ModelsDevPath: func() string { return filepath.Join(usagex.Dir(), "models.dev.json") },
		ModelFamilyIndexed: func() map[string]bool {
			m := map[string]bool{}
			for _, p := range []string{
				"anthropic", "openai", "google", "google-vertex", "alibaba", "zhipuai", "moonshotai",
				"deepseek", "xai", "mistral", "minimax", "cohere", "nvidia", "meta", "amazon-bedrock",
				"opencode", "opencode-go", "github-copilot", "openrouter",
			} {
				m[p] = true
			}
			return m
		},
	}
}

func TestConfigureRejectsEveryUnwiredField(t *testing.T) {
	full := testDeps()
	typ := reflect.TypeOf(full)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		d := full
		reflect.ValueOf(&d).Elem().Field(i).Set(reflect.Zero(typ.Field(i).Type))
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Configure accepted a Deps with %s left unwired", name)
				}
			}()
			Configure(d)
		}()
	}
	Configure(full) // leave the package wired for the tests that follow
}
