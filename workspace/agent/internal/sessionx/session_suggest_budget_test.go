package sessionx

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ingressHopMargin is what the CP hop and the network between the Agent and the ingress may
// take on top of the handler's own deadline.
const ingressHopMargin = 10 * time.Second

// ingressIdleTimeout reads the ALB's idle timeout from the template itself, so raising the
// budget or lowering the ingress value both turn this test red.
func ingressIdleTimeout(t *testing.T) time.Duration {
	t.Helper()
	const path = "../../../../deploy/aws/ecs/cfn/30-ingress.yaml"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Every non-comment line naming the key has to be the one strict form: a comment that
	// still says 60 must not stand in for a live value rewritten in another shape.
	attr := regexp.MustCompile(`^\s*-\s*\{\s*Key:\s*idle_timeout\.timeout_seconds,\s*Value:\s*"(\d+)"\s*\}\s*$`)
	var values []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || !strings.Contains(line, "idle_timeout.timeout_seconds") {
			continue
		}
		m := attr.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s: unrecognised idle_timeout.timeout_seconds line %q", path, line)
		}
		values = append(values, m[1])
	}
	if len(values) != 1 {
		t.Fatalf("%s: want exactly one idle_timeout.timeout_seconds attribute, found %d", path, len(values))
	}
	sec, _ := strconv.Atoi(values[0])
	return time.Duration(sec) * time.Second
}

// A suggestion the Console waits for sends no byte until the model answers, so a deadline at or
// past the ingress idle timeout lets the Agent succeed while the browser gets a gateway error.
func TestSyncSuggestBudgetStaysUnderTheIngressIdleTimeout(t *testing.T) {
	idle := ingressIdleTimeout(t)
	for name, d := range map[string]time.Duration{
		"TitleSuggestTimeout": TitleSuggestTimeout, // session title, branch name, chat title
		"ReplySuggestTimeout": ReplySuggestTimeout, // session and chat reply suggestions
	} {
		if d+ingressHopMargin > idle {
			t.Errorf("%s = %s; with the %s hop margin it reaches the ingress idle timeout %s", name, d, ingressHopMargin, idle)
		}
	}
}
