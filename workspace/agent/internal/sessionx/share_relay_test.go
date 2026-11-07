package sessionx

import "testing"

func TestShareRelayRefusesFieldsOutsideTheAllowList(t *testing.T) {
	cases := []struct {
		name, path, body, code string
	}{
		{"plain turn", "/sessions/s/turn", `{"op":"start","prompt":"hi"}`, ""},
		{"steer with id", "/sessions/s/turn", `{"op":"steer","prompt":"hi","clientMessageID":"c1"}`, ""},
		{"empty attachments", "/sessions/s/turn", `{"op":"start","prompt":"hi","attachments":[]}`, "share_attachments_refused"},
		{"attachment path", "/sessions/s/turn", `{"op":"start","prompt":"hi","attachments":["/home/dev/.ssh/id"]}`, "share_attachments_refused"},
		{"unknown field", "/sessions/s/turn", `{"op":"start","prompt":"hi","discard_queue":true}`, "share_field_refused"},
		{"queue op", "/sessions/s/turn", `{"op":"remove","id":"x"}`, "share_field_refused"},
		{"interrupt op", "/sessions/s/turn", `{"op":"interrupt"}`, "share_op_refused"},
		{"respond", "/sessions/s/respond", `{"id":"i","decision":"allow","scope":"once"}`, ""},
		{"respond extra", "/sessions/s/respond", `{"id":"i","decision":"allow","x":1}`, "share_field_refused"},
		{"plan expect", "/sessions/s/plan-respond", `{"decision":"approve","expect_plan":"p"}`, "share_field_refused"},
		{"plan ok", "/sessions/s/plan-respond", `{"decision":"reject","feedback":"no"}`, ""},
		{"trailing garbage", "/sessions/s/turn", `{"op":"start","prompt":"hi","attachments":["/x"]} x`, "share_body_refused"},
		{"second value", "/sessions/s/turn", `{"op":"start","prompt":"hi"}{"op":"start"}`, "share_body_refused"},
		{"empty body", "/sessions/s/turn", ``, "share_body_refused"},
		{"not an object", "/sessions/s/turn", `[]`, "share_body_refused"},
		{"answer ok", "/sessions/s/answer-question", `{"choices":[1]}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, _ := refuseShareRelay(c.path, []byte(c.body)); code != c.code {
				t.Fatalf("code = %q, want %q", code, c.code)
			}
		})
	}
}
