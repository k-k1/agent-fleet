package memreview

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testInput() Input {
	return Input{
		First: 10, Last: 30,
		// 20 is a row the excerpt leaves out (a compaction summary, say): inside the range, not shown.
		Numbers: map[int]bool{10: true, 12: true, 30: true},
		Known:   map[string]int{"project/old-note": 3, "project/not-shown": 2, "project/listed-only": 5, "user/style": 1},
		Listed:  map[string]bool{"project/old-note": true, "project/listed-only": true, "user/style": true},
		Full:    map[string]bool{"project/old-note": true, "user/style": true},
	}
}

func op(over map[string]any) string {
	m := map[string]any{"op": "create", "scope": "project", "name": "new-note", "type": "feedback",
		"description": "one line", "body": "the text", "reason": "why", "evidence": []int{12}}
	for k, v := range over {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func reply(ops ...string) string { return `{"ops":[` + strings.Join(ops, ",") + `]}` }

func kindOf(err error) string {
	var re *ReplyError
	if errors.As(err, &re) {
		return re.Kind
	}
	return ""
}

func TestParseReplyAcceptsTheDocumentedShapes(t *testing.T) {
	ops, err := ParseReply(reply(
		op(nil),
		op(map[string]any{"op": "update", "name": "old-note"}),
		op(map[string]any{"op": "forget", "scope": "user", "name": "style", "type": nil, "description": nil, "body": nil}),
	), testInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 || ops[0].BaseRevision != 0 || ops[1].BaseRevision != 3 || ops[2].BaseRevision != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops, err := ParseReply(`{"ops":[]}`, testInput()); err != nil || len(ops) != 0 {
		t.Fatalf("empty reply: %v %v", ops, err)
	}
	// A memory that was only listed can still be forgotten (not updated).
	if ops, err := ParseReply(reply(op(map[string]any{"op": "forget", "name": "listed-only", "type": nil, "description": nil, "body": nil})), testInput()); err != nil || len(ops) != 1 || ops[0].BaseRevision != 5 {
		t.Fatalf("forget of a listed memory: %v %v", ops, err)
	}
	fenced := "```json\n" + reply(op(nil)) + "\n```"
	if ops, err := ParseReply(fenced, testInput()); err != nil || len(ops) != 1 {
		t.Fatalf("fenced reply: %v %v", ops, err)
	}
}

func TestParseReplyDropsAMalformedReplyWhole(t *testing.T) {
	big := strings.Repeat("x", OpTextBytes+1)
	cases := map[string]struct {
		raw  string
		kind string
	}{
		"prose":                          {"Sure! here you go", ReplyMalformed},
		"trailing text":                  {reply() + " thanks", ReplyMalformed},
		"trailing brace":                 {reply() + " }", ReplyMalformed},
		"trailing bracket":               {reply() + " ] unscanned trailing text", ReplyMalformed},
		"trailing after a good op":       {reply(op(nil)) + " }", ReplyMalformed},
		"two objects":                    {reply() + reply(), ReplyMalformed},
		"no ops":                         {`{}`, ReplyMalformed},
		"null ops":                       {`{"ops":null}`, ReplyMalformed},
		"unknown top field":              {`{"ops":[],"note":"x"}`, ReplyMalformed},
		"unknown op field":               {reply(op(map[string]any{"extra": 1})), ReplyMalformed},
		"wrong type":                     {`{"ops":[{"op":1}]}`, ReplyMalformed},
		"bad op":                         {reply(op(map[string]any{"op": "delete"})), ReplyMalformed},
		"bad scope":                      {reply(op(map[string]any{"scope": "session"})), ReplyMalformed},
		"bad name":                       {reply(op(map[string]any{"name": "Not A Slug"})), ReplyMalformed},
		"name with a dot dot":            {reply(op(map[string]any{"name": "a..b"})), ReplyMalformed},
		"no reason":                      {reply(op(map[string]any{"reason": ""})), ReplyMalformed},
		"two line reason":                {reply(op(map[string]any{"reason": "a\nb"})), ReplyMalformed},
		"no evidence":                    {reply(op(map[string]any{"evidence": []int{}})), ReplyMalformed},
		"evidence before":                {reply(op(map[string]any{"evidence": []int{9}})), ReplyMalformed},
		"evidence after":                 {reply(op(map[string]any{"evidence": []int{31}})), ReplyMalformed},
		"evidence in a gap":              {reply(op(map[string]any{"evidence": []int{20}})), ReplyMalformed},
		"one evidence in a gap":          {reply(op(map[string]any{"evidence": []int{12, 20}})), ReplyMalformed},
		"create over existing":           {reply(op(map[string]any{"name": "old-note"})), ReplyMalformed},
		"create over hidden":             {reply(op(map[string]any{"name": "not-shown"})), ReplyMalformed},
		"update unseen":                  {reply(op(map[string]any{"op": "update", "name": "not-shown"})), ReplyMalformed},
		"update of a memory only listed": {reply(op(map[string]any{"op": "update", "name": "listed-only"})), ReplyMalformed},
		"update unknown":                 {reply(op(map[string]any{"op": "update", "name": "nothing"})), ReplyMalformed},
		"forget unseen":                  {reply(op(map[string]any{"op": "forget", "name": "not-shown", "type": nil, "description": nil, "body": nil})), ReplyMalformed},
		"forget with a body":             {reply(op(map[string]any{"op": "forget", "name": "old-note"})), ReplyMalformed},
		"no description":                 {reply(op(map[string]any{"description": ""})), ReplyMalformed},
		"two line description":           {reply(op(map[string]any{"description": "a\nb"})), ReplyMalformed},
		"no body":                        {reply(op(map[string]any{"body": ""})), ReplyMalformed},
		"bad type":                       {reply(op(map[string]any{"type": "bogus"})), ReplyMalformed},
		"no type":                        {reply(op(map[string]any{"type": ""})), ReplyMalformed},
		"six ops":                        {reply(op(nil), op(nil), op(nil), op(nil), op(nil), op(nil)), ReplyOversize},
		"oversize body":                  {reply(op(map[string]any{"body": big})), ReplyOversize},
		"oversize reply":                 {reply(op(map[string]any{"reason": strings.Repeat("r", ReplyBytes)})), ReplyOversize},
	}
	for name, tc := range cases {
		ops, err := ParseReply(tc.raw, testInput())
		if err == nil || ops != nil {
			t.Errorf("%s: accepted (%v)", name, ops)
			continue
		}
		if kindOf(err) != tc.kind {
			t.Errorf("%s: kind %q, want %q (%v)", name, kindOf(err), tc.kind, err)
		}
	}
	// One bad operation drops the good ones with it.
	if ops, err := ParseReply(reply(op(nil), op(map[string]any{"scope": "session"})), testInput()); err == nil || ops != nil {
		t.Errorf("a partly good reply was salvaged: %v", ops)
	}
}

// Every field is scanned, not only the body, and the error never echoes the value.
func TestParseReplyScansEveryField(t *testing.T) {
	secret := fakeKey()
	for _, field := range []string{"description", "body", "reason"} {
		_, err := ParseReply(reply(op(map[string]any{field: "see " + secret})), testInput())
		if kindOf(err) != ReplySecret {
			t.Errorf("%s: err = %v, want a secret refusal", field, err)
			continue
		}
		if strings.Contains(err.Error(), "AKIA") || strings.Contains(err.Error(), secret[4:]) {
			t.Errorf("%s: the error echoes the value: %v", field, err)
		}
	}
	_, err := ParseReply(reply(op(map[string]any{"body": "a\x00b"})), testInput())
	if kindOf(err) != ReplySecret {
		t.Errorf("a NUL byte: %v", err)
	}
}

func TestParseReplyDoesNotEchoTheReplyInErrors(t *testing.T) {
	for _, raw := range []string{
		`{"ops":[],"` + fakeKey() + `":1}`,
		`{"ops":[{"op":"` + fakeKey() + `"}]}`,
		`not json ` + fakeKey(),
	} {
		_, err := ParseReply(raw, testInput())
		if err == nil || strings.Contains(err.Error(), "AKIA") {
			t.Errorf("err = %v for %.20q", err, raw)
		}
	}
}

func TestParseReplyCollapsesOperationsOnOneTarget(t *testing.T) {
	ops, err := ParseReply(reply(
		op(map[string]any{"name": "first-take", "body": "one"}),
		op(map[string]any{"name": "other", "body": "o"}),
		op(map[string]any{"name": "first-take", "body": "two"}),
	), testInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0].Name != "other" || ops[1].Name != "first-take" || ops[1].Body != "two" {
		t.Fatalf("ops = %+v", ops)
	}
	// The same name in another scope is another target.
	ops, err = ParseReply(reply(op(nil), op(map[string]any{"scope": "user"})), testInput())
	if err != nil || len(ops) != 2 {
		t.Fatalf("ops = %+v, err %v", ops, err)
	}
}
