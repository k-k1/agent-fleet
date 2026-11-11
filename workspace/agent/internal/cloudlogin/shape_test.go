package cloudlogin

import (
	"strings"
	"testing"
)

func TestRedactedShapeHidesCodeAndURLTail(t *testing.T) {
	out := "Open:\n\nhttps://d-0123456789.awsapps.com/start/#/device?user_code=ABCD-EFGH\n\nCode: ABCD-EFGH\n\x1b[0m"
	got := redactedShape(out)
	for _, leak := range []string{"ABCD", "EFGH", "user_code", "/start/"} {
		if strings.Contains(got, leak) {
			t.Fatalf("shape leaks %q: %s", leak, got)
		}
	}
	for _, keep := range []string{"d-0123456789.awsapps.com", "<CODE>", `\x1b`} {
		if !strings.Contains(got, keep) {
			t.Fatalf("shape lost %q: %s", keep, got)
		}
	}
}

func TestRedactedShapeCapsLength(t *testing.T) {
	if got := redactedShape(strings.Repeat("#", 10000)); len(got) > 2100 {
		t.Fatalf("shape not capped: %d bytes", len(got))
	}
}
