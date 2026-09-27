package opencode

import "testing"

func TestLastUserModel(t *testing.T) {
	db := newOpencodeTestDB(t)
	// Shapes as opencode 1.18.32 writes them: the user row carries the model it was sent
	// with; the assistant row carries modelID/providerID at the top level instead.
	insMsg(t, db, "u1", "ses_a", 1, `{"role":"user","agent":"build","model":{"providerID":"opencode","modelID":"nemotron"}}`)
	insMsg(t, db, "a1", "ses_a", 2, `{"role":"assistant","agent":"build","modelID":"nemotron","providerID":"opencode"}`)
	insMsg(t, db, "u2", "ses_a", 3, `{"role":"user","agent":"plan","model":{"providerID":"opencode","modelID":"ling","variant":"high"}}`)
	insMsg(t, db, "a2", "ses_a", 4, `{"role":"assistant","agent":"plan","modelID":"ling","providerID":"opencode"}`)
	if got := lastUserModel(db, "ses_a"); got != "opencode/ling" {
		t.Errorf("lastUserModel = %q, want opencode/ling", got)
	}
	if got := mode(db, "ses_a"); got != "plan" {
		t.Errorf("mode = %q, want plan", got)
	}
	insMsg(t, db, "u3", "ses_b", 1, `{"role":"user","agent":"build"}`)
	if got := lastUserModel(db, "ses_b"); got != "" {
		t.Errorf("a user row with no model: lastUserModel = %q, want \"\"", got)
	}
}
