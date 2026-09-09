package mcpreg

import "testing"

// The opt-in reaches the session-side server only through this run-arg (ADR 0073 decision 3).
// A preference nobody puts on the argv is a preference that does nothing: every CLI's MCP
// config is written from these args at materialize time.
func TestFleetSpawnRunArg(t *testing.T) {
	old := FleetSpawnEnabled
	t.Cleanup(func() { FleetSpawnEnabled = old })

	FleetSpawnEnabled = nil
	if args, _ := BuiltinRunArgs(BuiltinAF); contains(args, "--fleet-spawn") {
		t.Fatalf("args with no hook = %v", args)
	}

	FleetSpawnEnabled = func() bool { return false }
	if args, _ := BuiltinRunArgs(BuiltinAF); contains(args, "--fleet-spawn") {
		t.Fatalf("args with the preference off = %v", args)
	}

	FleetSpawnEnabled = func() bool { return true }
	args, _ := BuiltinRunArgs(BuiltinAF)
	if !contains(args, "--fleet-spawn") {
		t.Fatalf("args with the preference on = %v", args)
	}
	// It rides on the session-side server, so --self-report has to be there with it: the flag
	// is inert without it (parseStdioFlags takes the conjunction).
	if !contains(args, "--self-report") {
		t.Fatalf("--fleet-spawn without --self-report = %v", args)
	}

	// Only af's own server takes it.
	if args, _ := BuiltinRunArgs(BuiltinAWS); contains(args, "--fleet-spawn") {
		t.Fatalf("a non-af builtin took the flag: %v", args)
	}
}
