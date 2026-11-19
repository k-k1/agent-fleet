// Package ingresstest reads the AWS ingress's idle timeout for tests that pin a silence budget
// under it. Tests import it; the product binary does not.
package ingresstest

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// HopMargin is what the CP hop and the network between the Agent and the ingress may take on
// top of the Agent's own silence.
const HopMargin = 10 * time.Second

// IdleTimeout reads the ALB's idle timeout from the template itself, so raising a budget or
// lowering the ingress value both turn the caller red. The template is found by walking up from
// the test's working directory (the package directory under `go test`), so every package's tests
// find the same one. Not runtime.Caller: under -trimpath (the workspace default) it returns a
// module-relative path that names no file on disk.
func IdleTimeout(t *testing.T) time.Duration {
	t.Helper()
	const rel = "deploy/aws/ecs/cfn/30-ingress.yaml"
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := ""
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, rel)); err == nil {
			path = filepath.Join(d, rel)
			break
		}
		if filepath.Dir(d) == d {
			t.Fatalf("%s not found above %s", rel, dir)
		}
	}
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
