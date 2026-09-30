package runtime

import "testing"

// The live tests only run against real AWS, so the identity match they rely on is
// pinned here where CI runs it.
func TestIsAssumedRoleOf(t *testing.T) {
	const role = "arn:aws:iam::111111111111:role/af-ec2c-cp"
	cases := []struct {
		sts, role string
		ok        bool
	}{
		{"arn:aws:sts::111111111111:assumed-role/af-ec2c-cp/botocore-session-1", role, true},
		{"arn:aws:sts::111111111111:assumed-role/af-ec2c-cp/s", "arn:aws:iam::111111111111:role/harness/af-ec2c-cp", true},
		{"arn:aws:sts::222222222222:assumed-role/af-ec2c-cp/session", role, false}, // same name, other account
		{"arn:aws:sts::111111111111:assumed-role/af-ec2c-cp-old/s", role, false},   // name prefix
		{"arn:aws:sts::111111111111:assumed-role/Deployer/me", role, false},
		{"arn:aws-cn:sts::111111111111:assumed-role/af-ec2c-cp/s", role, false},
		{"arn:aws:sts::111111111111:assumed-role/af-ec2c-cp/", role, false},
		{"", role, false},
		{"arn:aws:sts::111111111111:assumed-role/af-ec2c-cp/s", "", false},
		{"arn:aws:sts::111111111111:assumed-role/af-ec2c-cp/s", "arn:aws:iam:::role/af-ec2c-cp", false},
	}
	for _, c := range cases {
		if err := isAssumedRoleOf(c.sts, c.role); (err == nil) != c.ok {
			t.Errorf("isAssumedRoleOf(%q, %q) = %v, want ok=%v", c.sts, c.role, err, c.ok)
		}
	}
}
