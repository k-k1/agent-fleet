package agents

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastIdleTick shrinks the observation interval for the duration of a test.
func fastIdleTick(t *testing.T) {
	t.Helper()
	prev := idleTick
	idleTick = 2 * time.Millisecond
	t.Cleanup(func() { idleTick = prev })
}

func TestIdleGrace(t *testing.T) {
	const key = "AF_TEST_IDLE_GRACE"
	for _, tc := range []struct {
		name string
		env  string
		set  bool
		want time.Duration
	}{
		{name: "unset falls back to the default", want: 90 * time.Second},
		{name: "seconds are honoured", env: "5", set: true, want: 5 * time.Second},
		{name: "zero disables auto-stop", env: "0", set: true, want: 0},
		{name: "garbage falls back", env: "しばらく", set: true, want: 90 * time.Second},
		{name: "negative falls back", env: "-1", set: true, want: 90 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(key, tc.env)
			}
			if got := IdleGrace(key, 90*time.Second); got != tc.want {
				t.Fatalf("IdleGrace = %v, want %v", got, tc.want)
			}
		})
	}
}

// grace<=0 は「自動停止しない」の意味なので、監視自体を張らずに即戻ること。
// ここが回ってしまうと、無効化したはずのワークスペースで daemon が畳まれる。
func TestWatchIdleDisabledReturnsImmediately(t *testing.T) {
	var stopped atomic.Bool
	done := make(chan struct{})
	go func() {
		WatchIdle("test", func() int { return 0 }, func() bool { stopped.Store(true); return true }, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WatchIdle did not return for grace<=0")
	}
	if stopped.Load() {
		t.Fatal("auto-stop fired even though it was disabled")
	}
}

func TestWatchIdleStopsAfterGrace(t *testing.T) {
	fastIdleTick(t)
	stopped := make(chan struct{})
	go WatchIdle("test", func() int { return 0 }, func() bool { close(stopped); return true }, 10*time.Millisecond)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("需要ゼロが続いても停止しなかった")
	}
}

// 需要が戻ったら猶予は数え直し: 「一度ゼロを見た」だけで畳んではいけない。
func TestWatchIdleResetsWhileNeeded(t *testing.T) {
	fastIdleTick(t)
	var needs atomic.Int64
	needs.Store(1)
	var stopped atomic.Bool
	go WatchIdle("test", func() int { return int(needs.Load()) },
		func() bool { stopped.Store(true); return true }, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	if stopped.Load() {
		t.Fatal("需要が在るのに停止した")
	}
	needs.Store(0)
	deadline := time.Now().Add(2 * time.Second)
	for !stopped.Load() {
		if time.Now().After(deadline) {
			t.Fatal("需要が消えたのに停止しなかった")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// stopIfIdle の false は「ロック内の再確認で需要が復活していた」— 監視を降りずに
// 数え直すこと。ここで降りると、その後どれだけ暇になっても二度と畳まれない。
func TestWatchIdleKeepsWatchingWhenStopRefuses(t *testing.T) {
	fastIdleTick(t)
	var mu sync.Mutex
	calls := 0
	done := make(chan struct{})
	go WatchIdle("test", func() int { return 0 }, func() bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return false
		}
		close(done)
		return true
	}, 5*time.Millisecond)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		mu.Lock()
		got := calls
		mu.Unlock()
		t.Fatalf("停止拒否のあと監視が続かなかった（stopIfIdle 呼び出し %d 回）", got)
	}
}
