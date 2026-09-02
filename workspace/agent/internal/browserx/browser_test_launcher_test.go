package browserx

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Chromium の起動方法をテスト環境ごとに決める。以前は「パスに
// /.cache/ms-playwright/ を含むなら --no-sandbox」というパス由来の推測だったが、
// サンドボックスが使えるかどうかは**バイナリの置き場所ではなくホストの設定**で
// 決まる。実例が GitHub の ubuntu ランナーで、PATH に居る通常の Chrome を掴んで
// サンドボックス付きで起動 → 即死 → CDP が EOF になり、ci の workspace-agent が
// 恒常的に赤かった（Ubuntu 24.04 は非特権 user namespace を AppArmor で塞ぐ）。
//
// そこで推測をやめ、**実際に 1 回起動して CDP が応答するか**で選ぶ。順番は
// 本番と同じサンドボックス付きが先で、それが駄目なときだけ --no-sandbox。
// どちらも駄目なら Chromium が動かない環境なので Skip する。
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
