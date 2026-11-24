package secretpat

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/secretpat/secretpattest"
)

// A long line packed with stateless-looking starts and no dot must be scanned in linear time:
// the memory scan takes a 4 MiB blob, and one such line must not stall it. Quadratic scanning
// took about 11 s for 78 KB; linear takes milliseconds for 1 MiB.
func TestGitHubStatelessSpansIsLinear(t *testing.T) {
	for _, unit := range []string{"ghs_1_eyJ", "ghs_eyJabcdef", "ghs_1_eyJ-", "ghs_12345_eyJhbGciOiJ."} {
		in := strings.Repeat(unit, (1<<20)/len(unit))
		start := time.Now()
		GitHubStatelessSpans(in)
		GitHubTokens.MatchString(in)
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%q × %d: %v", unit, len(in)/len(unit), d)
		}
	}
}

// statelessAt answers what the anchored pattern plus the header checks answer, on every start.
func TestStatelessAtAgreesWithThePattern(t *testing.T) {
	ref := func(s string) [][2]int {
		var out [][2]int
		for _, p := range regexp.MustCompile(`gh[pousr]_`).FindAllStringIndex(s, -1) {
			m := githubStateless.FindStringIndex(s[p[0]:])
			if m == nil {
				continue
			}
			tok := s[p[0] : p[0]+m[1]]
			h := tok[len("ghs_"):]
			if h[0] >= '0' && h[0] <= '9' {
				h = h[strings.IndexByte(h, '_')+1:]
			}
			h, _, _ = strings.Cut(h, ".")
			if len(h) <= statelessMaxHeader && jwtHeaderIsObject(tok) {
				out = append(out, [2]int{p[0], p[0] + m[1]})
			}
		}
		return out
	}
	var ins []string
	for _, tok := range secretpattest.All() {
		ins = append(ins, tok, "x "+tok+" y", tok+tok, tok+"-"+tok, tok[:len(tok)/2], tok+".md", "cache_"+tok)
	}
	for tok := range shortHeaderTokens() {
		ins = append(ins, tok, "x "+tok+" y", tok+tok, "cache_"+tok)
	}
	ins = append(ins, secretpattest.Benign...)
	for _, in := range ins {
		got, want := GitHubStatelessSpans(in), ref(in)
		if len(got) != len(want) {
			t.Errorf("%.40q…: %v, pattern says %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%.40q…: %v, pattern says %v", in, got, want)
			}
		}
	}
}
