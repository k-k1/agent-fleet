package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// fakeSSMParams answers GetParameter for the two public AMI parameters.
type fakeSSMParams struct {
	mu    sync.Mutex
	vals  map[string]string
	err   error
	calls int
}

func (f *fakeSSMParams) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.vals[aws.ToString(in.Name)]
	if !ok {
		return nil, errors.New("ParameterNotFound")
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(v)}}, nil
}

func (f *fakeSSMParams) set(x86, arm string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vals = map[string]string{ssmSlotAMIX86: x86, ssmSlotAMIArm: arm}
}

// amiHarness is a pool with one x86 class and one arm64 class, an SSM fake and a clock the
// test moves.
func amiHarness(t *testing.T) (*ec2Harness, *fakeSSMParams, *time.Time) {
	t.Helper()
	h := newEC2Harness(t)
	h.rt.pool.classes = parseSlotClasses("standard|S|x86_64|m7i.large:8192\narm|A|arm64|m7g.large:8192")
	h.rt.pool.defaultClass = "standard"
	h.rt.pool.amiArm64 = "ami-cfn-arm"
	sp := &fakeSSMParams{}
	sp.set("ami-x86-1", "ami-arm-1")
	clock := time.Now()
	h.rt.pool.amis = newSlotAMIs(sp)
	h.rt.pool.amis.now = func() time.Time { return clock }
	h.ec2.ltLatest = 3
	return h, sp, &clock
}

func (h *ec2Harness) addStamped(id, itype, ami, ver string) {
	h.ec2.addSlot(id, "ap-northeast-1a", itype, false, false)
	h.ec2.instances[id].ImageId = aws.String(ami)
	h.ec2.setInstanceTag(id, ec2TagLaunchTemplateID, "lt-1")
	h.ec2.setInstanceTag(id, ec2TagLaunchTemplateVersion, ver)
}

// reasonsOf judges slot id the way the admin screen does.
func reasonsOf(t *testing.T, h *ec2Harness, id string) []string {
	t.Helper()
	lt, err := h.rt.pool.readLaunchTemplate(context.Background(), h.ec2)
	if err != nil {
		t.Fatal(err)
	}
	return reasons(h.rt.pool, *h.ec2.instances[id], lt)
}

func reasons(p ec2PoolConfig, inst ec2types.Instance, lt ec2LaunchTemplate) []string {
	_, r, _ := p.slotStale(inst, lt)
	return r
}

// sameData makes every version's data identical, as when only the ImageId moved.
func sameData(h *ec2Harness) {
	h.ec2.ltData = map[int64]*ec2types.ResponseLaunchTemplateData{}
	for v := int64(1); v <= h.ec2.ltLatest; v++ {
		h.ec2.ltData[v] = &ec2types.ResponseLaunchTemplateData{ImageId: aws.String(fmt.Sprintf("ami-x86-v%d", v)), UserData: aws.String("same")}
	}
}

// #3318 acceptance: only the x86 AMI changed. The launch template version moved (CloudFormation
// re-resolved SlotAmiId) but its data only in ImageId.
func TestSlotOutdated_OnlyX86AMIChanged(t *testing.T) {
	h, sp, clock := amiHarness(t)
	sameData(h)
	h.addStamped("i-x86", "m7i.large", "ami-x86-1", "1")
	h.addStamped("i-arm", "m7g.large", "ami-arm-1", "1")
	if r := reasonsOf(t, h, "i-x86"); len(r) != 0 {
		t.Fatalf("x86 slot on the current AMI = %v, want not outdated", r)
	}
	sp.set("ami-x86-2", "ami-arm-1")
	*clock = clock.Add(2 * time.Hour)
	if r := reasonsOf(t, h, "i-x86"); len(r) != 1 || r[0] != slotStaleAMI {
		t.Fatalf("x86 slot behind = %v, want [%s]", r, slotStaleAMI)
	}
	if r := reasonsOf(t, h, "i-arm"); len(r) != 0 {
		t.Fatalf("arm64 slot after an x86-only change = %v, want not outdated (version number alone must not decide)", r)
	}
}

func TestSlotOutdated_OnlyArmAMIChanged(t *testing.T) {
	h, sp, clock := amiHarness(t)
	sameData(h)
	h.addStamped("i-x86", "m7i.large", "ami-x86-1", "3")
	h.addStamped("i-arm", "m7g.large", "ami-arm-1", "3")
	reasonsOf(t, h, "i-arm") // first read
	sp.set("ami-x86-1", "ami-arm-2")
	*clock = clock.Add(2 * time.Hour)
	if r := reasonsOf(t, h, "i-arm"); len(r) != 1 || r[0] != slotStaleAMI {
		t.Fatalf("arm64 slot behind = %v, want [%s] although the template version is current", r, slotStaleAMI)
	}
	if r := reasonsOf(t, h, "i-x86"); len(r) != 0 {
		t.Fatalf("x86 slot after an arm64-only change = %v", r)
	}
}

func TestSlotOutdated_TemplateChangedBeyondImageID(t *testing.T) {
	h, _, _ := amiHarness(t)
	sameData(h)
	h.ec2.ltData[3] = &ec2types.ResponseLaunchTemplateData{ImageId: aws.String("ami-x86-v3"), UserData: aws.String("new user data")}
	h.addStamped("i-x86", "m7i.large", "ami-x86-1", "2")
	h.addStamped("i-arm", "m7g.large", "ami-arm-1", "2")
	for _, id := range []string{"i-x86", "i-arm"} {
		if r := reasonsOf(t, h, id); len(r) != 1 || r[0] != slotStaleTemplate {
			t.Fatalf("%s after a user-data change = %v, want [%s]", id, r, slotStaleTemplate)
		}
	}
}

func TestSlotOutdated_BothReasonsAreReported(t *testing.T) {
	h, sp, _ := amiHarness(t)
	h.addStamped("i-arm", "m7g.large", "ami-arm-0", "1") // default fake data: every version differs
	_ = sp
	if r := reasonsOf(t, h, "i-arm"); len(r) != 2 {
		t.Fatalf("reasons = %v, want both", r)
	}
}

// SSM unreadable: the launch falls back, and no slot is called outdated on an AMI nobody knows.
func TestSlotAMI_SSMUnreadable(t *testing.T) {
	h, sp, _ := amiHarness(t)
	sameData(h)
	sp.err = errors.New("AccessDeniedException: ssm:GetParameter")
	h.addStamped("i-arm", "m7g.large", "ami-arm-old", "3")
	h.addStamped("i-x86", "m7i.large", "ami-x86-old", "3")
	for _, id := range []string{"i-arm", "i-x86"} {
		if r := reasonsOf(t, h, id); len(r) != 0 {
			t.Fatalf("%s judged %v with SSM unreadable and no earlier read", id, r)
		}
	}
	h.rt.instanceType, h.rt.arch = "m7g.large", EC2ArchArm
	if _, err := h.rt.runSlot(context.Background(), "ap-northeast-1a", h.rt.pool.maxSlots, ""); err != nil {
		t.Fatal(err)
	}
	h.rt.instanceType, h.rt.arch = "m7i.large", EC2ArchX86
	if _, err := h.rt.runSlot(context.Background(), "ap-northeast-1a", h.rt.pool.maxSlots, ""); err != nil {
		t.Fatal(err)
	}
	if got := h.ec2.ranAMI; len(got) != 2 || got[0] != "ami-cfn-arm" || got[1] != "" {
		t.Fatalf("fallback launch ImageIds = %q, want [ami-cfn-arm \"\"]", got)
	}
}

// A read that worked once is kept when SSM later fails, and the failure is not retried per call.
func TestSlotAMI_KeepsLastValueAndDoesNotHammerSSM(t *testing.T) {
	h, sp, clock := amiHarness(t)
	ctx := context.Background()
	if got := h.rt.pool.launchAMI(ctx, EC2ArchArm); got != "ami-arm-1" {
		t.Fatalf("first launch AMI = %q", got)
	}
	sp.mu.Lock()
	sp.err = errors.New("ThrottlingException")
	sp.mu.Unlock()
	*clock = clock.Add(2 * time.Hour)
	for i := 0; i < 5; i++ {
		if got := h.rt.pool.launchAMI(ctx, EC2ArchArm); got != "ami-arm-1" {
			t.Fatalf("launch AMI after a failed refresh = %q, want the last read value", got)
		}
	}
	if sp.calls > 3 { // 1 initial + 1 failed refresh (+ at most one more x86 never asked)
		t.Fatalf("SSM called %d times; a failing read must back off", sp.calls)
	}
	// A garbage answer is not an AMI.
	sp.err = nil
	sp.set("", "not-an-ami")
	*clock = clock.Add(2 * time.Hour)
	if got := h.rt.pool.launchAMI(ctx, EC2ArchArm); got != "ami-arm-1" {
		t.Fatalf("launch AMI after a garbage read = %q", got)
	}
}

// Both architectures launch with the SSM AMI as ImageId override.
func TestSlotAMI_BothArchitecturesLaunchWithSSMAMI(t *testing.T) {
	h, _, _ := amiHarness(t)
	ctx := context.Background()
	h.rt.instanceType, h.rt.arch = "m7i.large", EC2ArchX86
	if _, err := h.rt.runSlot(ctx, "ap-northeast-1a", h.rt.pool.maxSlots, ""); err != nil {
		t.Fatal(err)
	}
	h.rt.instanceType, h.rt.arch = "m7g.large", EC2ArchArm
	if _, err := h.rt.runSlot(ctx, "ap-northeast-1a", h.rt.pool.maxSlots, ""); err != nil {
		t.Fatal(err)
	}
	if got := h.ec2.ranAMI; len(got) != 2 || got[0] != "ami-x86-1" || got[1] != "ami-arm-1" {
		t.Fatalf("launch ImageIds = %q, want [ami-x86-1 ami-arm-1]", got)
	}
}

// Template versions are immutable: they are read once, and an unreadable list is "unknown",
// never "changed".
func TestSlotTemplate_VersionsCachedAndUnknownWhenUnreadable(t *testing.T) {
	h, _, _ := amiHarness(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := h.rt.pool.readLaunchTemplate(ctx, h.ec2); err != nil {
			t.Fatal(err)
		}
	}
	if h.ec2.ltVersionCalls != 1 {
		t.Fatalf("DescribeLaunchTemplateVersions called %d times for an unchanged $Latest, want 1", h.ec2.ltVersionCalls)
	}

	h2, _, _ := amiHarness(t)
	h2.ec2.ltVersionsErr = errors.New("UnauthorizedOperation: ec2:DescribeLaunchTemplateVersions")
	h2.addStamped("i-arm", "m7g.large", "ami-arm-1", "1")
	if r := reasonsOf(t, h2, "i-arm"); len(r) != 0 {
		t.Fatalf("template versions unreadable: slot judged %v, want not outdated", r)
	}
	h2.addStamped("i-arm2", "m7g.large", "ami-arm-0", "1")
	if r := reasonsOf(t, h2, "i-arm2"); len(r) != 1 || r[0] != slotStaleAMI {
		t.Fatalf("AMI half must still work without template versions: %v", r)
	}
}

// The reservation path follows the new rule and says why.
func TestReserveSlotReplacement_AMIBehind(t *testing.T) {
	h, _, _ := amiHarness(t)
	sameData(h)
	h.addStamped("i-arm", "m7g.large", "ami-arm-0", "3")
	h.addStamped("i-ok", "m7g.large", "ami-arm-1", "3")
	f := h.factory()
	res, err := f.ReserveSlotReplacement(context.Background(), "i-arm", true, true)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(res.Reasons) != 1 || res.Reasons[0] != slotStaleAMI {
		t.Fatalf("reasons = %v", res.Reasons)
	}
	if _, err := f.ReserveSlotReplacement(context.Background(), "i-ok", true, true); !errors.Is(err, ErrSlotNotOutdated) {
		t.Fatalf("bulk reserve of a current slot = %v, want ErrSlotNotOutdated", err)
	}
	if !strings.Contains(ErrSlotNotOutdated.Error(), "AMI") {
		t.Fatal("the refusal should name the AMI rule")
	}
}

// The sweeper retires a free slot behind the AMI (and only that one) through the same
// af-slot-retire fence as for a template change.
func TestSweeper_RetiresFreeSlotBehindAMI(t *testing.T) {
	h := freeSlotHarness(t, time.Minute)
	sp := &fakeSSMParams{}
	sp.set("ami-x86-1", "ami-arm-1")
	h.rt.pool.amis = newSlotAMIs(sp)
	h.rt.pool.ltSigs = newLTSigCache()
	h.rt.pool.slotSleepAfter, h.rt.pool.slotTerminateAfter = 0, 0
	h.ec2.ltLatest = 3
	sameData(h)
	h.ec2.addSlot("i-cur", "ap-northeast-1a", "m7i.large", false, false)
	for id, ami := range map[string]string{"i-free": "ami-x86-0", "i-cur": "ami-x86-1"} {
		h.ec2.instances[id].ImageId = aws.String(ami)
		h.ec2.setInstanceTag(id, ec2TagLaunchTemplateID, "lt-1")
		h.ec2.setInstanceTag(id, ec2TagLaunchTemplateVersion, "3")
	}
	h.factory().sweepFreeSlots(context.Background(), nil)
	got := terminatedInstances(h)
	if len(got) != 1 || got[0] != "i-free" {
		t.Fatalf("terminated = %v, want only [i-free] (calls %v)", got, h.ec2.calls)
	}
}
