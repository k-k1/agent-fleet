package runtime

import "testing"

// MachineProfile must be the SAME resolution placement uses. Going through the factory's
// New (rather than filling an ecsEC2Runtime by hand) is the whole point of the test: a
// literal would keep passing while New wired a different rung into the fields that decide
// which box is launched.
func TestMachineProfileNamesTheRungPlacementWillUse(t *testing.T) {
	h := newEC2Harness(t)
	f := h.factory()
	f.pool.classes = parseSlotClasses("standard|Standard|x86_64|m7i.large:8192:2,m7i.xlarge:16384:4\n" +
		"arm|低コスト (Arm)|arm64|m8g.large:8192:2,m8g.xlarge:16384:4")
	f.pool.defaultClass = "standard"
	f.pool.homeGiB = 60
	f.pool.hostReserveMiB = 1536

	rt, ok := f.New(Workspace{MembershipID: "M-1", MemBytes: 9 * 1024 * mib, SlotClass: "arm"}, "dek", nil).(*ecsEC2Runtime)
	if !ok {
		t.Fatal("ecs-ec2 factory did not return an ecsEC2Runtime")
	}
	m := rt.MachineProfile()
	// 9 GiB does not fit the 8192 rung, so it lands on the next one up — in the ARM
	// class's ladder, not the default one's.
	if m.InstanceType != "m8g.xlarge" || m.Arch != EC2ArchArm {
		t.Errorf("box = %s/%s, want m8g.xlarge/arm64", m.InstanceType, m.Arch)
	}
	if m.VCPU != 4 || m.SlotMemMiB != 16384 {
		t.Errorf("rung = %d vCPU / %d MiB, want 4 / 16384", m.VCPU, m.SlotMemMiB)
	}
	// What the member may actually spend is the rung less the host reserve, and it is a
	// different number from the box's — printing only the box would promise memory the
	// cgroup refuses.
	if m.MemCapMiB != 16384-1536 {
		t.Errorf("mem_cap_mib = %d, want the rung less the reserve", m.MemCapMiB)
	}
	if m.HomeGiB != 60 {
		t.Errorf("home_gib = %d, want 60", m.HomeGiB)
	}
	if m.ClassID != "arm" || m.ClassLabel != "低コスト (Arm)" {
		t.Errorf("class = %s/%s, want the operator's own label", m.ClassID, m.ClassLabel)
	}
	if !m.Dedicated {
		t.Error("an EC2 slot is one member's box; Dedicated is what lets the Console show it as theirs")
	}
}

// A deployment with a single unnamed ladder has no class to name. The parser's synthetic
// "default" id is not a word the operator chose, so it must not reach the screen — the same
// rule SizingProfile follows for the picker.
func TestMachineProfileNamesNoClassOnASingleLadder(t *testing.T) {
	h := newEC2Harness(t)
	f := h.factory()
	f.pool.classes = parseSlotClasses("m7i.large:8192:2")
	f.pool.defaultClass = "default"

	m := f.New(Workspace{MembershipID: "M-1"}, "dek", nil).(*ecsEC2Runtime).MachineProfile()
	if m.ClassID != "" || m.ClassLabel != "" {
		t.Errorf("class = %q/%q, want both empty on a single unnamed ladder", m.ClassID, m.ClassLabel)
	}
	// 0 (unset memory) lands on the SMALLEST rung here, not on a deployment default.
	if m.InstanceType != "m7i.large" {
		t.Errorf("instance_type = %q, want the smallest rung", m.InstanceType)
	}
}
