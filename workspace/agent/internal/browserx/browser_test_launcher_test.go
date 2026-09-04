package browserx

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Picks how to launch Chromium per test environment. Whether the sandbox works is decided by
// the HOST configuration, not by where the binary sits, so guessing from the path (such as
// "--no-sandbox when the path contains /.cache/ms-playwright/") is wrong: on GitHub's ubuntu
// runner it picked the ordinary Chrome on PATH, launched it sandboxed, and the browser died at
// once, leaving CDP at EOF and the ci workspace-agent permanently red (Ubuntu 24.04 blocks
// unprivileged user namespaces via AppArmor).
//
// So instead of guessing, launch once and see whether CDP answers. Sandboxed first, the same as
// production, and --no-sandbox only when that fails. When neither works Chromium cannot run
// here at all, so the test skips.
var (
	browserTestFactoryOnce sync.Once
	browserTestFactoryVal  browserCDPFactory
	browserTestSandboxVal  bool
)

// browserTestCDPFactory returns a launcher that actually works here, or skips.
func browserTestCDPFactory(t *testing.T) browserCDPFactory {
	t.Helper()
	f, _ := browserTestLauncher(t)
	return f
}

// browserTestUsesSandbox reports whether the working launcher is the sandboxed
// one, for the tests that drive the smoke helper by flag rather than by factory.
func browserTestUsesSandbox(t *testing.T) bool {
	t.Helper()
	_, sandbox := browserTestLauncher(t)
	return sandbox
}

func browserTestLauncher(t *testing.T) (browserCDPFactory, bool) {
	t.Helper()
	if _, err := findChromiumBinary(); err != nil {
		t.Skip("Chromium is not installed in this test environment")
	}
	browserTestFactoryOnce.Do(func() {
		for _, c := range []struct {
			f       browserCDPFactory
			sandbox bool
		}{{launchPipeCDP, true}, {LaunchPipeCDPWithoutSandboxForTest, false}} {
			if browserCDPFactoryWorks(c.f) {
				browserTestFactoryVal, browserTestSandboxVal = c.f, c.sandbox
				return
			}
		}
	})
	if browserTestFactoryVal == nil {
		t.Skip("Chromium cannot start here (neither sandboxed nor with --no-sandbox)")
	}
	return browserTestFactoryVal, browserTestSandboxVal
}

// browserCDPFactoryWorks launches Chromium once and asks it for its version.
// Start() succeeding proves nothing — a Chromium that cannot sandbox exits right
// afterwards, and the failure only shows up as EOF on the first real call.
func browserCDPFactoryWorks(f browserCDPFactory) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cdp, err := f(ctx)
	if err != nil {
		return false
	}
	defer func() { _ = cdp.Close() }()
	var out struct {
		Product string `json:"product"`
	}
	if err := cdp.Call(ctx, "Browser.getVersion", nil, "", &out); err != nil {
		return false
	}
	return out.Product != ""
}
