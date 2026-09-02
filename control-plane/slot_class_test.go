package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// --- parsing ---------------------------------------------------------------------

// The shape every deployed 30-ingress stack passes today. It must keep parsing
// forever: an operator upgrading the CP does not edit CFN parameters, and a spec that
// stopped parsing would leave the pool with no ladder at all (docs/log/70 §70.4.2).
func TestParseSlotClassesBareLadderStaysOneClass(t *testing.T) {
	cs := parseSlotClasses("m7i.large:8192:2,m7i.xlarge:16384:4,m7i.2xlarge:32768:8")
	if len(cs) != 1 {
		t.Fatalf("a bare ladder is one class, got %d", len(cs))
	}
	if cs[0].id != "default" || cs[0].arch != ec2ArchX86 {
		t.Fatalf("want the default x86_64 class, got %+v", cs[0])
	}
	if len(cs[0].slots) != 3 || cs[0].slots[0].instanceType != "m7i.large" || cs[0].slots[0].vcpu != 2 {
		t.Fatalf("rungs lost: %+v", cs[0].slots)
	}
}

func TestParseSlotClassesMultiple(t *testing.T) {
	cs := parseSlotClasses(
		"standard|標準（Intel）|x86_64|m7i.large:8192:2,m7i.xlarge:16384:4\n" +
			"arm|省コスト（Arm）|arm64|m7g.large:8192:2,m7g.xlarge:16384:4")
	if len(cs) != 2 {
		t.Fatalf("want two classes, got %d (%+v)", len(cs), cs)
	}
	if cs[0].id != "standard" || cs[0].label != "標準（Intel）" || cs[0].arch != ec2ArchX86 {
		t.Errorf("first class wrong: %+v", cs[0])
	}
	if cs[1].id != "arm" || cs[1].arch != ec2ArchArm || cs[1].slots[1].instanceType != "m7g.xlarge" {
		t.Errorf("second class wrong: %+v", cs[1])
	}
	// Declared order is kept: it is the order the Console offers them in, and the
	// operator chose it.
	if cs[0].id != "standard" {
		t.Errorf("declared order not preserved")
	}
}

// A typo'd architecture must drop the class, not default it. Defaulting "aarch64" to
// x86_64 would boot an x86 AMI for an arm instance type — a launch failure with no
// hint of a spelling mistake anywhere near it (docs/log/70 §70.4.2).
func TestParseSlotClassesRejectsUnknownArch(t *testing.T) {
	cs := parseSlotClasses("ok|OK|arm64|m7g.large:8192\nbad|Bad|aarch64|m7g.large:8192")
	if len(cs) != 1 || cs[0].id != "ok" {
		t.Fatalf("only the well-formed class survives, got %+v", cs)
	}
}

func TestParseSlotClassesDropsEmptyLadder(t *testing.T) {
	cs := parseSlotClasses("good|G|x86_64|m7i.large:8192\nempty|E|x86_64|broken")
	if len(cs) != 1 || cs[0].id != "good" {
		t.Fatalf("a class with no usable rung is dropped, got %+v", cs)
	}
}

// --- placement -------------------------------------------------------------------

// The class picks the LADDER, memory still picks the RUNG. That is what keeps "8 GB"
// meaning the same thing before and after a class change.
func TestSlotTypeForPerClass(t *testing.T) {
	p := ec2PoolConfig{
		classes: parseSlotClasses(
			"standard|S|x86_64|m7i.large:8192,m7i.xlarge:16384\n" +
				"econ|E|arm64|m6g.large:8192,m6g.xlarge:16384"),
		defaultClass: "standard",
	}
	for _, c := range []struct {
		class    string
		bytes    int64
		wantType string
		wantArch string
	}{
		{"standard", 8 * gib, "m7i.large", ec2ArchX86},
		{"standard", 9 * gib, "m7i.xlarge", ec2ArchX86},
		{"econ", 8 * gib, "m6g.large", ec2ArchArm},
		{"econ", 9 * gib, "m6g.xlarge", ec2ArchArm},
		// An id the operator has since deleted falls back to the default class rather
		// than failing a Start.
		{"gone", 9 * gib, "m7i.xlarge", ec2ArchX86},
		{"", 0, "m7i.large", ec2ArchX86},
	} {
		gotType, gotArch := p.slotTypeFor(c.class, c.bytes)
		if gotType != c.wantType || gotArch != c.wantArch {
			t.Errorf("slotTypeFor(%q, %d GiB) = %s/%s, want %s/%s",
				c.class, c.bytes/gib, gotType, gotArch, c.wantType, c.wantArch)
		}
	}
}

// An arm64 slot needs the arm64 AMI: the launch template pins the x86_64
// ECS-optimized one and Graviton cannot boot it (docs/log/70 §70.8).
func TestAMIForArch(t *testing.T) {
	p := ec2PoolConfig{launchTemplate: "lt-x86", amiArm64: "ami-arm"}
	if got := p.amiFor(ec2ArchArm); got != "ami-arm" {
		t.Errorf("arm64 must override the template's AMI, got %q", got)
	}
	// ⚠️ x86_64 must return "" and not the template's own AMI: strOrNil turns that into
	// a nil ImageId, which is what keeps an x86_64 launch byte-for-byte the call it has
	// always been.
	if got := p.amiFor(ec2ArchX86); got != "" {
		t.Errorf("x86_64 must not override anything, got %q", got)
	}
	// A deployment that never declared classes has no arch on its runtimes at all.
	if got := p.amiFor(""); got != "" {
		t.Errorf("an unset arch must not override anything, got %q", got)
	}
}

// The refusal itself. An arm64 class with no arm64 launch template is a deployment
// that would accept the setting in the Console and then fail every Start of anybody
// who chose it — so the CP does not come up at all.
func TestPoolValidate(t *testing.T) {
	arm := "standard|S|x86_64|m7i.large:8192\narm|A|arm64|m7g.large:8192"

	p := ec2PoolConfig{launchTemplate: "lt-x86", classes: parseSlotClasses(arm)}
	if err := p.validate(); err == nil || !strings.Contains(err.Error(), "AF_ECS_EC2_AMI_ARM64") {
		t.Fatalf("want a refusal naming the missing AMI, got %v", err)
	}

	p = ec2PoolConfig{launchTemplate: "lt-x86", amiArm64: "ami-arm", classes: parseSlotClasses(arm)}
	if err := p.validate(); err != nil {
		t.Fatalf("with the arm64 AMI it must boot: %v", err)
	}
	if p.defaultClass != "standard" {
		t.Errorf("an unset default is the first declared class, got %q", p.defaultClass)
	}

	p = ec2PoolConfig{launchTemplate: "lt-x86", amiArm64: "ami-arm",
		classes: parseSlotClasses(arm), defaultClass: "nope"}
	if err := p.validate(); err == nil {
		t.Errorf("a default that names no declared class must refuse")
	}

	// The shape every existing deployment has.
	p = ec2PoolConfig{launchTemplate: "lt-x86", classes: parseSlotClasses("m7i.large:8192")}
	if err := p.validate(); err != nil {
		t.Fatalf("a bare single ladder must keep booting: %v", err)
	}
	if p.defaultClass != "default" {
		t.Errorf("got default class %q", p.defaultClass)
	}
}

func TestSizingProfileHidesSingleClassPicker(t *testing.T) {
	one := (&ecsEC2Factory{pool: ec2PoolConfig{
		classes: parseSlotClasses("m7i.large:8192:2"), defaultClass: "default", homeGiB: 50,
	}}).SizingProfile()
	if len(one.SlotClasses) != 0 {
		t.Errorf("a single unnamed ladder offers no picker, got %+v", one.SlotClasses)
	}
	if len(one.Slots) != 1 {
		t.Errorf("the ladder itself is still reported: %+v", one.Slots)
	}

	two := (&ecsEC2Factory{pool: ec2PoolConfig{
		classes:      parseSlotClasses("standard|S|x86_64|m7i.large:8192:2\narm|A|arm64|m7g.large:8192:2"),
		defaultClass: "arm", homeGiB: 50,
	}}).SizingProfile()
	if len(two.SlotClasses) != 2 || two.DefaultSlotClass != "arm" {
		t.Fatalf("both classes and the default must be reported: %+v", two)
	}
	// Slots is the DEFAULT class's ladder, not the first declared one — it is what a
	// Console that predates classes would show, and it must not describe a class
	// nobody lands on.
	if len(two.Slots) != 1 || two.Slots[0].InstanceType != "m7g.large" {
		t.Errorf("Slots must be the default class's ladder, got %+v", two.Slots)
	}
}

// --- resolution ------------------------------------------------------------------

func TestResolveSlotClass(t *testing.T) {
	ctx := context.Background()
	classes := "standard|S|x86_64|m7i.large:8192\narm|A|arm64|m7g.large:8192\necon|E|arm64|m6g.large:8192"

	newMgr := func(t *testing.T) (*manager, string, string) {
		t.Helper()
		m := p3Manager(t, p3Store(t))
		m.rtFactory = &ecsEC2Factory{pool: ec2PoolConfig{
			classes: parseSlotClasses(classes), defaultClass: "standard", homeGiB: 50,
		}}
		ten, err := m.store.CreateTenant(ctx, "acme", "Acme")
		if err != nil {
			t.Fatal(err)
		}
		ident, err := m.store.UpsertIdentity(ctx, "a@example.com", "a", "")
		if err != nil {
			t.Fatal(err)
		}
		mem, err := m.store.EnsureMembership(ctx, ident.ID, ten.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		return m, ten.ID, mem.ID
	}
	setLimits := func(t *testing.T, m *manager, tenantID string, lim tenantLimits) {
		t.Helper()
		lj, err := json.Marshal(lim)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.store.SetTenantLimits(ctx, tenantID, string(lj)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("nothing set anywhere is the deployment default", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		got, note := m.resolveSlotClass(ctx, Workspace{TenantID: tid, MembershipID: mid})
		if got != "standard" || note != "" {
			t.Fatalf("got %q note=%q", got, note)
		}
	})

	t.Run("the tenant default applies to a member with no value", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		setLimits(t, m, tid, tenantLimits{SlotClass: "arm"})
		if got, _ := m.resolveSlotClass(ctx, Workspace{TenantID: tid, MembershipID: mid}); got != "arm" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("the per-user value wins", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		setLimits(t, m, tid, tenantLimits{SlotClass: "arm"})
		if err := m.store.PutUserLimit(ctx, mid, UserQuota{SlotClass: "econ"}); err != nil {
			t.Fatal(err)
		}
		if got, _ := m.resolveSlotClass(ctx, Workspace{TenantID: tid, MembershipID: mid}); got != "econ" {
			t.Fatalf("got %q", got)
		}
	})

	// The substitution is REPORTED, not just done: a member silently running on
	// another class is invisible until the invoice.
	t.Run("a class the tenant may not use falls back and says so", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		setLimits(t, m, tid, tenantLimits{SlotClass: "arm", AllowedSlotClasses: []string{"standard", "arm"}})
		if err := m.store.PutUserLimit(ctx, mid, UserQuota{SlotClass: "econ"}); err != nil {
			t.Fatal(err)
		}
		got, note := m.resolveSlotClass(ctx, Workspace{TenantID: tid, MembershipID: mid})
		if got != "arm" {
			t.Fatalf("got %q, want the tenant default", got)
		}
		if !strings.Contains(note, "econ") {
			t.Fatalf("the note must name the class that was refused, got %q", note)
		}
	})

	// An operator can drop a class at any redeploy. Somebody's stored id then names
	// nothing, and that must not fail their Start.
	t.Run("an id the deployment no longer declares falls back", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		if err := m.store.PutUserLimit(ctx, mid, UserQuota{SlotClass: "retired"}); err != nil {
			t.Fatal(err)
		}
		got, note := m.resolveSlotClass(ctx, Workspace{TenantID: tid, MembershipID: mid})
		if got != "standard" || note == "" {
			t.Fatalf("got %q note=%q", got, note)
		}
	})

	// A super_admin can restrict a tenant to a set that excludes the deployment
	// default. "no usable class" must not become a failed Start.
	t.Run("an allowed set without the deployment default picks an allowed one", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		setLimits(t, m, tid, tenantLimits{AllowedSlotClasses: []string{"econ"}})
		if got, _ := m.resolveSlotClass(ctx, Workspace{TenantID: tid, MembershipID: mid}); got != "econ" {
			t.Fatalf("got %q", got)
		}
	})

	// Every runtime but ecs-ec2, and every ecs-ec2 deployment with one unnamed ladder:
	// the whole feature is inert and nothing about an existing deployment changes.
	t.Run("no classes declared resolves to empty", func(t *testing.T) {
		m, tid, mid := newMgr(t)
		m.rtFactory = &ecsEC2Factory{pool: ec2PoolConfig{
			classes: parseSlotClasses("m7i.large:8192"), defaultClass: "default",
		}}
		if err := m.store.PutUserLimit(ctx, mid, UserQuota{SlotClass: "arm"}); err != nil {
			t.Fatal(err)
		}
		if got, _ := m.resolveSlotClass(ctx, Workspace{TenantID: tid, MembershipID: mid}); got != "" {
			t.Fatalf("got %q, want \"\"", got)
		}
	})
}

// The stored value survives a round trip, which is the whole point of the migration.
func TestUserLimitRoundTripsSlotClass(t *testing.T) {
	ctx := context.Background()
	m := p3Manager(t, p3Store(t))
	ten, err := m.store.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	ident, err := m.store.UpsertIdentity(ctx, "a@example.com", "a", "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := m.store.EnsureMembership(ctx, ident.ID, ten.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.store.PutUserLimit(ctx, mem.ID, UserQuota{MaxSessions: 3, SlotClass: "arm"}); err != nil {
		t.Fatal(err)
	}
	ul, ok, err := m.store.GetUserLimit(ctx, mem.ID)
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if ul.SlotClass != "arm" || ul.MaxSessions != 3 {
		t.Fatalf("got %+v", ul)
	}
	// And clearing it goes back to "the tenant default", not to the previous value.
	if err := m.store.PutUserLimit(ctx, mem.ID, UserQuota{MaxSessions: 3}); err != nil {
		t.Fatal(err)
	}
	if ul, _, _ := m.store.GetUserLimit(ctx, mem.ID); ul.SlotClass != "" {
		t.Fatalf("clearing left %q", ul.SlotClass)
	}
}

// The whole arm64 launch, end to end through the fake EC2: the request carries the
// arm64 AMI as an override, and the task definition declares ARM64 so ECS refuses to
// place it anywhere else.
func TestArmClassLaunchesWithTheArmAMI(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.classes = parseSlotClasses(
		"standard|S|x86_64|m7i.large:8192\narm|A|arm64|m7g.large:8192")
	h.rt.pool.defaultClass = "standard"
	h.rt.pool.amiArm64 = "ami-arm64"
	h.rt.instanceType, h.rt.arch = "m7g.large", ec2ArchArm

	if _, err := h.rt.runSlot(ctx, "ap-northeast-1a"); err != nil {
		t.Fatalf("runSlot: %v", err)
	}
	if len(h.ec2.ranAMI) != 1 || h.ec2.ranAMI[0] != "ami-arm64" {
		t.Fatalf("arm64 slot launched with ImageId %v, want [ami-arm64]", h.ec2.ranAMI)
	}

	// ...and the x86_64 path overrides nothing, so it is the call it has always been.
	h.ec2.ranAMI = nil
	h.rt.instanceType, h.rt.arch = "m7i.large", ec2ArchX86
	if _, err := h.rt.runSlot(ctx, "ap-northeast-1a"); err != nil {
		t.Fatalf("runSlot: %v", err)
	}
	if len(h.ec2.ranAMI) != 1 || h.ec2.ranAMI[0] != "" {
		t.Fatalf("x86_64 slot sent ImageId %v, want no override", h.ec2.ranAMI)
	}
}

func TestTaskDefDeclaresArchitecture(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.arch = ec2ArchArm
	in := h.rt.buildTaskDef(ctx, ec2Placement{instanceID: "i-1", az: "ap-northeast-1a"}, ec2Prep{})
	if in.RuntimePlatform == nil || in.RuntimePlatform.CpuArchitecture != ecstypes.CPUArchitectureArm64 {
		t.Fatalf("arm64 slot must declare ARM64, got %+v", in.RuntimePlatform)
	}
	h.rt.arch = ec2ArchX86
	in = h.rt.buildTaskDef(ctx, ec2Placement{instanceID: "i-1", az: "ap-northeast-1a"}, ec2Prep{})
	if in.RuntimePlatform == nil || in.RuntimePlatform.CpuArchitecture != ecstypes.CPUArchitectureX8664 {
		t.Fatalf("x86_64 slot must declare X86_64, got %+v", in.RuntimePlatform)
	}
}

// The exact string cfn/30-ingress.yaml ships as Ec2SlotTypes' default.
//
// ⚠️ Copied here on purpose. A CFN parameter is a STRING that nothing validates until
// a CP boots on it, and this one is now long enough to get a `|` or a `;` wrong. The
// failure mode is not a syntax error — parseSlotClasses drops what it cannot read — so
// a typo would ship as "the picker is missing" or "the saver class vanished", on a
// deployment nobody is watching. Keep this in step with the template.
func TestShippedDefaultSlotClassesParse(t *testing.T) {
	const shipped = "standard|Standard (Intel)|x86_64|m7i.large:8192:2,m7i.xlarge:16384:4,m7i.2xlarge:32768:8" +
		";saver|Lower cost (Intel, previous gen)|x86_64|m6i.large:8192:2,m6i.xlarge:16384:4,m6i.2xlarge:32768:8"

	cs := parseSlotClasses(shipped)
	if len(cs) != 2 {
		t.Fatalf("the shipped default must declare two classes, got %d: %+v", len(cs), cs)
	}
	if cs[0].id != "standard" || cs[1].id != "saver" {
		t.Fatalf("ids/order changed: %+v", cs)
	}
	for _, c := range cs {
		// ⚠️ Both classes are x86_64, and that is the point: the shipped default must
		// need no arm64 image and no arm64 AMI, so it works on every deployment as it
		// stands. An arm64 class here would make the CP refuse to boot until the
		// operator also set Ec2SlotAmiArm64 (validate()).
		if c.arch != ec2ArchX86 {
			t.Errorf("the shipped default must not require an arm64 AMI: %s is %s", c.id, c.arch)
		}
		if len(c.slots) != 3 || c.slots[0].vcpu != 2 || c.slots[2].vcpu != 8 {
			t.Errorf("class %s lost rungs: %+v", c.id, c.slots)
		}
	}
	// And it boots with nothing but the launch template every ecs-ec2 deployment
	// already has.
	p := ec2PoolConfig{launchTemplate: "lt-1", classes: cs}
	if err := p.validate(); err != nil {
		t.Fatalf("the shipped default must boot without any new parameter: %v", err)
	}
	if p.defaultClass != "standard" {
		t.Errorf("the shipped default must land nobody on the cheaper class, got %q", p.defaultClass)
	}
}

// ⚠️ Two migrations with the same numeric prefix are a SILENT data loss:
// schema_migrations is keyed by that integer, so the first of the pair records the
// version and the second is skipped forever as "already applied" — the column it was
// meant to add never exists, and the failure surfaces as a query error in production.
//
// It is the normal outcome of two branches adding a migration in parallel, which is
// how this repository works. Measured once (2026-08-22: develop's 0030_memo_category
// met a branch's 0030_user_limit_slot_class in migrations-pg). This test is the cheap
// half of the fix — the other half refuses to start (sqlStore.migrate).
func TestMigrationVersionsAreUniquePerDialect(t *testing.T) {
	// 2026-09-02: 両系列は internal/store へ移った（ADR 0067 / CP-STORE）。
	// //go:embed が自分のディレクトリより上を見られないので、SQL は embed する
	// パッケージと一緒に動く。ここが読むのはディスク上の実体なので追随させる。
	for _, dir := range []string{"internal/store/migrations", "internal/store/migrations-pg"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		seen := map[int]string{}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".sql") {
				continue
			}
			v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
			if err != nil {
				t.Errorf("%s/%s: version prefix is not a number", dir, name)
				continue
			}
			if prev, dup := seen[v]; dup {
				t.Errorf("%s: %s and %s share version %d — one would be silently skipped; renumber the newer one",
					dir, prev, name, v)
			}
			seen[v] = name
		}
		if len(seen) == 0 {
			t.Errorf("%s: no migrations found (did the directory move?)", dir)
		}
	}
}

// ★ The bug P5 found on a live deployment (docs/log/70 §70.14.5).
//
// The attachment is AFFINITY, not a decision: it records the slot this workspace used
// last time, for the size and class it had THEN. When a member moves to a class on
// another architecture, buildTaskDef declares RuntimePlatform for the NEW class while
// the placement constraint still pins the OLD instance — and ECS then refuses to place
// the task at all, forever, with desiredCount 1 and nothing running:
//
//	"no container instance met all of its requirements … the closest matching
//	 (container-instance …) is missing an attribute required by your task"
//
// So placeHome must check the match before honouring the affinity.
func TestPlaceHomeReleasesASlotOfTheWrongType(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.rt.pool.classes = parseSlotClasses(
		"saver|S|x86_64|m6i.large:8192\narm|A|arm64|m8g.large:8192")
	h.rt.pool.defaultClass = "saver"
	h.rt.pool.amiArm64 = "ami-arm"

	// The member's home sits on an m6i slot from when they were on the saver class.
	h.ec2.addSlot("i-old", "ap-northeast-1a", "m6i.large", true, false)
	h.ec2.addHomeVolume("vol-1", h.rt.base.membershipID, h.rt.base.name, "ap-northeast-1a")
	h.ec2.attach("vol-1", "i-old", time.Now())

	// They have since moved to the arm class.
	h.rt.instanceType, h.rt.arch = "m8g.large", ec2ArchArm
	// A free arm slot is available in the same AZ (an EBS volume never leaves its AZ).
	h.ec2.addSlot("i-arm", "ap-northeast-1a", "m8g.large", true, false)

	p, err := h.rt.placeHome(ctx)
	if err != nil {
		t.Fatalf("placeHome: %v", err)
	}
	if p.instanceID == "i-old" {
		t.Fatalf("reused the m6i slot for an arm64 workspace — ECS would refuse to place the task forever")
	}
	if p.instanceID != "i-arm" {
		t.Fatalf("placed on %q, want the free m8g slot", p.instanceID)
	}
}

// The other side of the same rule: a slot that still matches is reused, because that
// affinity is what makes a restart cheap (no attach, no mount to redo).
func TestPlaceHomeKeepsAMatchingSlot(t *testing.T) {
	ctx := context.Background()
	h := newEC2Harness(t)
	h.ec2.addSlot("i-mine", "ap-northeast-1a", "m7i.large", true, false)
	h.ec2.addHomeVolume("vol-1", h.rt.base.membershipID, h.rt.base.name, "ap-northeast-1a")
	h.ec2.attach("vol-1", "i-mine", time.Now())
	h.rt.instanceType, h.rt.arch = "m7i.large", ec2ArchX86

	p, err := h.rt.placeHome(ctx)
	if err != nil {
		t.Fatalf("placeHome: %v", err)
	}
	if p.instanceID != "i-mine" {
		t.Fatalf("placed on %q, want the slot the home is already on", p.instanceID)
	}
}
