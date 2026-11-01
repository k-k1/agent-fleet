package opencode

import "testing"

// A finished attempt answers connected on every look; only the first may say stored, which is
// what the Control Plane audits the sign-in by.
func TestOAuthFirstCompleteIsOncePerAttempt(t *testing.T) {
	// The set is package-global: isolate it so the test repeats (-count=2) and leaks nothing.
	oauthCompletedMu.Lock()
	saved := oauthCompleted
	oauthCompleted = map[string]bool{}
	oauthCompletedMu.Unlock()
	t.Cleanup(func() {
		oauthCompletedMu.Lock()
		oauthCompleted = saved
		oauthCompletedMu.Unlock()
	})

	if !oauthFirstComplete("att-1") {
		t.Fatal("first completion of att-1 not reported")
	}
	if oauthFirstComplete("att-1") {
		t.Fatal("att-1 reported twice")
	}
	if !oauthFirstComplete("att-2") {
		t.Fatal("a different attempt was swallowed")
	}
}
