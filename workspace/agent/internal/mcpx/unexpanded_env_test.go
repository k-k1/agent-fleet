package mcpx

import (
	"os"
	"testing"
)

func TestDropUnexpandedEnvClearsOnlySelfReferences(t *testing.T) {
	t.Setenv("AGENT_ADDR", "${env:AGENT_ADDR}")
	t.Setenv("AF_SESSION_NAME", "s-real")
	// Someone else's reference, or a value that merely looks like one, is not ours to clear.
	t.Setenv("AF_MEMO_TOKEN", "${env:AGENT_TOKEN}")
	t.Setenv("AF_CP_BASE_URL", "${AF_CP_BASE_URL}")

	dropUnexpandedEnv()

	if v, ok := os.LookupEnv("AGENT_ADDR"); ok {
		t.Errorf("unexpanded AGENT_ADDR survived as %q", v)
	}
	for name, want := range map[string]string{
		"AF_SESSION_NAME": "s-real",
		"AF_MEMO_TOKEN":   "${env:AGENT_TOKEN}",
		"AF_CP_BASE_URL":  "${AF_CP_BASE_URL}",
	} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want %q (left alone)", name, got, want)
		}
	}
}
