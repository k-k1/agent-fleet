package main

// Parity between the CP's signing and the checked-in list of member-origin signed routes. The
// list lives with the Agent (workspace/agent/internal/memberorigin/signed_routes.txt) because
// the two Go modules cannot import each other; the Agent module checks the same file against
// its memberorigin.Require registrations. A route signed here without a line there, or a line
// without a relay row, fails this test.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const signedRoutesList = "../workspace/agent/internal/memberorigin/signed_routes.txt"

func TestSignedRoutesMatchRelayRows(t *testing.T) {
	raw, err := os.ReadFile(signedRoutesList)
	if err != nil {
		t.Fatal(err)
	}
	listedRelay := map[string]bool{}
	var direct []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			t.Fatalf("malformed line %q", line)
		}
		switch f[2] {
		case "relay":
			listedRelay[f[0]+" /api"+f[1]] = true
		case "direct":
			direct = append(direct, f[1])
		default:
			t.Fatalf("unknown CP-SIDE %q in %q", f[2], line)
		}
	}

	signed, _ := signedRelayRoutes()
	if len(signed) == 0 {
		t.Fatal("no relay row has sign: the derivation is broken")
	}
	var problems []string
	for r := range signed {
		if !listedRelay[r] {
			problems = append(problems, "relay row "+r+" has sign but the list does not carry it as relay")
		}
	}
	for r := range listedRelay {
		if !signed[r] {
			problems = append(problems, "the list carries "+r+" as relay but no relay row signs it")
		}
	}

	// Direct signing: every memberOriginSign call site other than the relay's own (the one in
	// member_origin.go that signs a request by its relay row) is a CP handler signing for
	// itself, and each must be listed. The path literal of each listed route must also appear
	// in a CP source file next to such a call.
	files, _ := filepath.Glob("*.go")
	calls := 0
	var src strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "member_origin.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(regexp.MustCompile(`\.memberOriginSign\(`).FindAll(b, -1)); n > 0 {
			calls += n
			src.Write(b)
		}
	}
	if calls != len(direct) {
		problems = append(problems, "the CP has "+strconv.Itoa(calls)+" direct memberOriginSign call sites but the list carries "+strconv.Itoa(len(direct))+" direct routes")
	}
	for _, p := range direct {
		lit := p
		if i := strings.LastIndex(p, "}"); i >= 0 {
			lit = p[i+1:]
		}
		if !strings.Contains(src.String(), lit) {
			problems = append(problems, "direct route "+p+" is not named in any file that signs directly")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
