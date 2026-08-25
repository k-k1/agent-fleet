package resources

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeCgroup writes a cgroup v2 scope的 な最小セットを一時ディレクトリに置き、
// AF_CGROUP_DIR でそこを見させる。
func fakeCgroup(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AF_CGROUP_DIR", dir)
	return dir
}

func TestReadsMemoryFromItsOwnCgroup(t *testing.T) {
	fakeCgroup(t, map[string]string{
		"memory.current": "1073741824\n",
		"memory.max":     "4294967296\n",
	})
	s := Read()
	if s.MemUsed == nil || *s.MemUsed != 1073741824 {
		t.Errorf("mem_used = %v, want 1073741824", s.MemUsed)
	}
	if s.MemMax == nil || *s.MemMax != 4294967296 {
		t.Errorf("mem_max = %v, want 4294967296", s.MemMax)
	}
}

// 上限なしの cgroup は memory.max に "max" と書く。それを数値として載せると
// 画面は 16 EiB のような桁を「上限」として描いてしまうので、キーごと落とす。
func TestUnlimitedMemoryMaxIsOmittedRatherThanHuge(t *testing.T) {
	fakeCgroup(t, map[string]string{
		"memory.current": "1000\n",
		"memory.max":     "max\n",
	})
	if s := Read(); s.MemMax != nil {
		t.Errorf("mem_max = %v, want nil for an unlimited cgroup", *s.MemMax)
	}
}

// 読めない軸は 0 ではなく「無い」。0% と「測れない」が同じ値になると、Console は
// 測れないものを 0 として描く（タイルの「–」が出せなくなる）。
func TestUnreadableAxesAreOmittedNotZero(t *testing.T) {
	fakeCgroup(t, nil) // 空のディレクトリ = どのファイルも無い
	s := Read()
	if s.MemUsed != nil || s.MemMax != nil || s.CPUPct != nil || s.OOMKillTotal != nil {
		t.Fatalf("want every cgroup axis omitted, got %+v", s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mem_used", "mem_max", "cpu_pct", "oom_kill_total"} {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if _, present := m[key]; present {
			t.Errorf("%s present in %s, want the key omitted", key, b)
		}
	}
}

func TestOOMKillCountReadsTheCumulativeCounter(t *testing.T) {
	fakeCgroup(t, map[string]string{
		"memory.events": "low 0\nhigh 4\nmax 2\noom 1\noom_kill 3\n",
	})
	v, ok := OOMKillCount()
	if !ok || v != 3 {
		t.Errorf("OOMKillCount = %d,%v want 3,true", v, ok)
	}
}

// CPU は累積カウンタの差分。初回のサンプルは差分が取れないので必ず「測れない」。
func TestCPUNeedsTwoSamples(t *testing.T) {
	dir := fakeCgroup(t, map[string]string{"cpu.stat": "usage_usec 1000000\n"})
	m := &cpuMeter{}
	base := time.Unix(1700000000, 0)
	if _, ok := m.pct(base); ok {
		t.Fatal("first sample reported a percentage; it has nothing to diff against")
	}
	// 2 秒の壁時計で 1 秒ぶんの CPU を消費 = 50%。
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 2000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pct, ok := m.pct(base.Add(2 * time.Second))
	if !ok {
		t.Fatal("second sample still reported no percentage")
	}
	if pct < 49.9 || pct > 50.1 {
		t.Errorf("cpu_pct = %v, want ~50", pct)
	}
}

// minInterval より短い再訪では前回の答えをそのまま返す。stats は SSE tick と
// 管理画面ポーリングの 2 系統から叩かれるので、毎回差分を取り直すと互いの前回値を
// 踏み合って数字が跳ねる。
func TestCPUShortRevisitReusesTheLastAnswer(t *testing.T) {
	dir := fakeCgroup(t, map[string]string{"cpu.stat": "usage_usec 0\n"})
	m := &cpuMeter{}
	base := time.Unix(1700000000, 0)
	m.pct(base)
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 1000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, ok := m.pct(base.Add(2 * time.Second))
	if !ok {
		t.Fatal("want a percentage from the second sample")
	}
	// カウンタが動いても、100ms しか経っていないなら同じ答えを返す。
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 9000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, ok := m.pct(base.Add(2*time.Second + 100*time.Millisecond))
	if !ok || again != first {
		t.Errorf("short revisit = %v,%v want %v,true (the cached answer)", again, ok, first)
	}
}

// カウンタが巻き戻ったら（コンテナ再作成 = cgroup が作り直された）測れないと言う。
// 古い前回値との差分は意味を持たない。
func TestCPUCounterResetReportsUnmeasurable(t *testing.T) {
	dir := fakeCgroup(t, map[string]string{"cpu.stat": "usage_usec 5000000\n"})
	m := &cpuMeter{}
	base := time.Unix(1700000000, 0)
	m.pct(base)
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pct, ok := m.pct(base.Add(2 * time.Second)); ok {
		t.Errorf("counter reset reported %v%%, want unmeasurable", pct)
	}
}

// ディスクは du の木歩きではなく home が載る FS の statfs。テスト環境でも home は
// 必ずどこかの FS 上にあるので、値が取れて used <= total であることを確かめる。
func TestHomeUsageReportsUsedWithinTotal(t *testing.T) {
	used, total, ok := homeUsage()
	if !ok {
		t.Skip("statfs unavailable for home in this environment")
	}
	if total == 0 || used > total {
		t.Errorf("used=%d total=%d — want 0 < used <= total", used, total)
	}
}
