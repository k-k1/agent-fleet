package bridge

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// customIDsOf walks the action-row components approvalRow builds and returns each button's
// custom_id, so a test can round-trip them through ParseCustomID.
func customIDsOf(rows []any) []string {
	var ids []string
	for _, r := range rows {
		row, _ := r.(map[string]any)
		comps, _ := row["components"].([]any)
		for _, c := range comps {
			b, _ := c.(map[string]any)
			if cid, ok := b["custom_id"].(string); ok {
				ids = append(ids, cid)
			}
		}
	}
	return ids
}

// TestApprovalCustomIDRoundTrip: the approve/reject buttons encode a custom_id that
// ParseCustomID decodes back to (kind "op", the same id, the right choice) — the P3 encode
// and decode halves agree.
func TestApprovalCustomIDRoundTrip(t *testing.T) {
	ids := customIDsOf(approvalRow("abc123", false))
	if len(ids) != 2 {
		t.Fatalf("want approve+reject buttons, got %v", ids)
	}
	want := map[string]bool{"approve": false, "reject": false}
	for _, cid := range ids {
		pi, ok := ParseCustomID(cid)
		if !ok || pi.Kind != "op" || pi.Approval != "abc123" {
			t.Fatalf("ParseCustomID(%q) = %+v ok=%v", cid, pi, ok)
		}
		if _, isChoice := want[pi.Choice]; !isChoice {
			t.Fatalf("unexpected choice %q in %q", pi.Choice, cid)
		}
		want[pi.Choice] = true
	}
	if !want["approve"] || !want["reject"] {
		t.Fatalf("both choices must be present: %v", want)
	}
}

// TestParseCustomIDOpRejectsMalformed: a truncated / bogus-choice op id is not accepted.
func TestParseCustomIDOpRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"af|op|approve", "af|op|maybe|id", "af|op||id", "af|op|approve|"} {
		if _, ok := ParseCustomID(bad); ok {
			t.Fatalf("malformed op id accepted: %q", bad)
		}
	}
}

// TestPostOperatorApprovalNoTarget: with no operator thread provisioned, the send half
// reports a no-target error so the gate can fail closed.
func TestPostOperatorApprovalNoTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := PostOperatorApproval("delete s7?", "id1"); err == nil {
		t.Fatal("expected a no-target error when no operator thread exists")
	}
}

// TestPostOperatorApprovalPosts: with an operator thread + Discord creds, the button
// message is posted into the thread, scrubbed of secrets.
func TestPostOperatorApprovalPosts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	SaveOperatorState("C1", "T-op", "conv-1")

	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.Discord = &secrets.DiscordCreds{Token: "tok", ChannelID: "C1", Lang: "ja"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	hits, bodies, _ := captureDiscordBodies(t)

	secret := "トークン xoxb-123456789012-abcdefghijklmnopqrstuvwx を delete して"
	if err := PostOperatorApproval("🔒 承認\n"+secret, "id1"); err != nil {
		t.Fatal(err)
	}
	var posted bool
	for _, h := range *hits {
		if h == "POST /channels/T-op/messages" {
			posted = true
		}
	}
	if !posted {
		t.Fatalf("approval must post into the operator thread; hits=%v", *hits)
	}
	for _, b := range *bodies {
		if strings.Contains(b, "xoxb-123456789012-abcdefghijklmnopqrstuvwx") {
			t.Fatalf("secret leaked into approval prompt: %q", b)
		}
	}
}
