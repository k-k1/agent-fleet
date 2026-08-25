// Package resources は **この Workspace 自身の**リソース実測値（メモリ・CPU・
// ディスク）を、コンテナの中から読む。
//
// なぜ Agent 側なのか。CP の `containerStats`（control-plane/metrics.go）は
// `docker inspect` でコンテナ ID を引き、ホストの
// `/sys/fs/cgroup/system.slice/docker-<id>.scope` を読む——**CP と Workspace が
// 同じホストに載っている構成でしか成立しない**読み方である。ECS（Fargate も
// `ecs-ec2` も）では CP タスクに docker バイナリも対象の cgroup も無いので、
// メモリ / CPU / ディスクが 3 つとも取れず、メンバー詳細のタイルが「–」のまま
// だった（docs/63 §63.9）。
//
// コンテナの中からなら話は逆で、`/sys/fs/cgroup` は cgroup 名前空間で自分自身に
// 張り替えられているため、**ランタイムが何であれ**同じ 2 ファイルを読むだけで
// 済む。前例もある: status.OOMKillCount（本パッケージへ移設）は以前からこの
// 読み方で自分の oom_kill を数えていた。
//
// 読めなかった軸は **省く**（ゼロを返さない）。「0%」と「測れない」を同じ値で
// 表すと、画面は測れないものを 0 として描いてしまう。
package resources

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// cgroupDir is the container's own cgroup v2 root. Overridable for tests.
func cgroupDir() string {
	if v := os.Getenv("AF_CGROUP_DIR"); v != "" {
		return v
	}
	return "/sys/fs/cgroup"
}

// Stats は 1 回の観測。読めなかった軸は nil で、JSON からも消える。
// フィールド名は CP の docker 経路（control-plane/metrics.go）が出す JSON と
// **同じキー**にしてある: CP はどちらの経路で得た値も同じ map に載せ、Console は
// 出どころを区別しない。
type Stats struct {
	MemUsed *uint64 `json:"mem_used,omitempty"`
	MemMax  *uint64 `json:"mem_max,omitempty"`
	// CPUPct は「1 コア = 100%」（docker stats と同じ規約）。2 サンプル要るので
	// プロセス起動後の最初の 1 回は必ず nil になる。
	CPUPct *float64 `json:"cpu_pct,omitempty"`
	// OOMKillTotal は累積値。「直近で増えたか」の判定は CP 側（oomTracker）が持つ。
	OOMKillTotal *uint64 `json:"oom_kill_total,omitempty"`
	// DiskUsed / DiskTotal は home が載っているファイルシステムの statfs。du の
	// ような木の走査ではないので毎回叩いてよい。
	DiskUsed  *uint64 `json:"disk_used,omitempty"`
	DiskTotal *uint64 `json:"disk_total,omitempty"`
}

// Read は今の観測値を返す。どの軸も独立に失敗してよい（片方だけ読める環境が
// 現に存在する: cgroup v1 のホスト、home が読めない権限、など）。
func Read() Stats {
	var s Stats
	if v, ok := readCgroupUint("memory.current"); ok {
		s.MemUsed = &v
	}
	if v, ok := readCgroupUint("memory.max"); ok {
		s.MemMax = &v
	}
	if v, ok := cpu.pct(time.Now()); ok {
		s.CPUPct = &v
	}
	if v, ok := OOMKillCount(); ok {
		s.OOMKillTotal = &v
	}
	if used, total, ok := homeUsage(); ok {
		s.DiskUsed, s.DiskTotal = &used, &total
	}
	return s
}

// readCgroupUint は cgroup v2 の単一値ファイルを読む。"max"（無制限）は !ok —
// 上限が無いことを巨大な数値として画面に出さないため。
func readCgroupUint(name string) (uint64, bool) {
	b, err := os.ReadFile(cgroupDir() + "/" + name)
	if err != nil {
		return 0, false
	}
	t := strings.TrimSpace(string(b))
	if t == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(t, 10, 64)
	return v, err == nil
}

// readCgroupKV は "key value" 形式の平坦なファイル（memory.events 等）から 1 行引く。
func readCgroupKV(name, key string) (uint64, bool) {
	b, err := os.ReadFile(cgroupDir() + "/" + name)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == key {
			v, err := strconv.ParseUint(f[1], 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// OOMKillCount reads the cumulative oom_kill counter from the container's own
// cgroup v2 memory.events. From inside the container /sys/fs/cgroup is
// cgroup-namespaced to this container, so this is our own count. Reports !ok when
// unreadable (a non cgroup-v2 host, a different layout, etc.) so callers degrade
// instead of guessing OOM.（record_exit.go → internal/status → 本パッケージへ二度目の
// 移設。cgroup を読む実装を 1 つに束ねるため。status.OOMKillCount は互換のため
// 残してあり、ここへ委譲するだけ。）
func OOMKillCount() (uint64, bool) { return readCgroupKV("memory.events", "oom_kill") }

// --- CPU ---

// cpuMeter は cpu.stat の累積 usage_usec から使用率を出す。累積カウンタなので
// 差分を取るしかなく、**前回の値を憶えておく主体が要る**。
//
// 呼び出し側で憶えるのではなく、ここに 1 つだけ置いているのが肝である。stats は
// CP の SSE tick（4 秒）と管理画面のポーリング（4 秒）の両方から叩かれるので、
// 呼び出し毎に差分を取ると 2 系統が互いの前回値を踏み合い、どちらも短い間隔の
// 差分になって数字が跳ねる。1 つの計器を共有し、minInterval より短い再訪には
// 前回の答えをそのまま返す。
type cpuMeter struct {
	mu   sync.Mutex
	prev uint64
	at   time.Time
	last float64
	have bool // last が有効（= 2 サンプル以上取れた）
}

// minInterval より短い間隔では新しい差分を取らない。usage_usec の分解能に対して
// 差分の窓が短すぎると量子化誤差が支配的になり、静止中の Workspace が数十 % に
// 見えることがある。
const minInterval = time.Second

var cpu = &cpuMeter{}

func (m *cpuMeter) pct(now time.Time) (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.at.IsZero() && now.Sub(m.at) < minInterval {
		return m.last, m.have
	}
	usage, ok := readUsageUsec()
	if !ok {
		return 0, false
	}
	prev, prevAt := m.prev, m.at
	m.prev, m.at = usage, now
	wall := now.Sub(prevAt).Microseconds()
	// 初回・時計の巻き戻し・カウンタのリセット（コンテナ再作成）は測れない。
	// 直前の値を返し続けるのではなく have を倒す——古い数字を今の値として
	// 出すくらいなら「測れない」と言う方がよい。
	if prevAt.IsZero() || wall <= 0 || usage < prev {
		m.last, m.have = 0, false
		return 0, false
	}
	m.last, m.have = float64(usage-prev)/float64(wall)*100, true
	return m.last, true
}

func readUsageUsec() (uint64, bool) {
	return readCgroupKV("cpu.stat", "usage_usec")
}

// --- Disk ---

// homeUsage は home が載っているファイルシステムの使用量 / 容量を statfs で返す。
//
// **`du` ではない**のは意図的である。CP の docker 経路は
// `du -sb <dataDir>/home` でホーム木を歩くが、あれは「ホスト上の 1 ディレクトリ」
// の大きさを知る唯一の手段だったからで、木が大きいほど高くつく（だから CP 側は
// 60 秒キャッシュしている）。コンテナの中では home は自分のボリュームなので、
// statfs 1 発で使用量も容量も分かる。`ecs-ec2` では home = 永続 EBS なので、
// この 2 つは**まさに知りたい数字そのもの**になる（docs/64）。
func homeUsage() (used, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(paths.HomeDir(), &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	if bs == 0 || st.Blocks == 0 {
		return 0, 0, false
	}
	total = st.Blocks * bs
	// 使用量は Blocks-Bfree（root 予約ぶんを含む実使用）。Bavail を使うと
	// 一般ユーザーから見た空きになり、used+free が total に合わなくなる。
	used = (st.Blocks - st.Bfree) * bs
	return used, total, true
}
