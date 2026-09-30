// Package ingresstest reads the AWS ingress's idle timeout for tests that pin a silence budget
// under it. Tests import it; the product binary does not.
package ingresstest

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// HopMargin is what the CP hop and the network between the Agent and the ingress may take on
// top of the Agent's own silence.
const HopMargin = 10 * time.Second

// IdleTimeout reads the ALB's idle timeout from the template itself, so raising a budget or
// lowering the ingress value both turn the caller red. The path is taken from this source
// file, not the working directory, so every package's tests find the same template.
func IdleTimeout(t *testing.T) time.Duration {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate ingresstest source")
	}
	path := filepath.Join(filepath.Dir(self), "../../../../deploy/aws/ecs/cfn/30-ingress.yaml")
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
