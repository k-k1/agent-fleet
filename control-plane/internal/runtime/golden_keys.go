// golden_keys.go — the reserved names a golden bake uses.
//
// The baker itself stays with the CP: it drives tenants, memberships and workspace
// starts, none of which this package can see. What moved is only the naming, because
// the ecs-ec2 side re-derives the same names from AWS tags with no database to consult
// (runtime_ecs_ec2_golden.go), and two spellings of "af-golden-seed" would orphan a
// membership and its home volume the first time they disagreed.
package runtime

// The two reserved members of the golden tenant. Ordinary rows, named so nobody
// mistakes them for people.
const (
	GoldenSeedKey  = "af-golden-seed"
	GoldenProbeKey = "af-golden-probe"
)

// ArchKey names the reserved workspace for one architecture.
//
// x86_64 keeps the ORIGINAL, unsuffixed keys. Every deployment that has ever baked
// a golden has an af-golden-seed membership, and renaming it would orphan that row
// (and its home volume) with nothing left pointing at it — a workspace nobody can see
// and nothing will ever clean up.
func ArchKey(base, arch string) string {
	if arch == "" || arch == EC2ArchX86 {
		return base
	}
	return base + "-" + arch
}
