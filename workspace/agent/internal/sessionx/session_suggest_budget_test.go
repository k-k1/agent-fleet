package sessionx

import (
	"os"
	"regexp"
	"strconv"
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
	m := regexp.MustCompile(`Key:\s*idle_timeout\.timeout_seconds,\s*Value:\s*"(\d+)"`).FindAllSubmatch(b, -1)
	if len(m) != 1 {
		t.Fatalf("%s: want exactly one idle_timeout.timeout_seconds attribute, found %d", path, len(m))
	}
	sec, _ := strconv.Atoi(string(m[0][1]))
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
