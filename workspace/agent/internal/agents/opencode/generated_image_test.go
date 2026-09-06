package opencode

import "testing"

// opencode keeps a tool's output on the same part, so the picture card needs no pairing pass
// the way claude's does — it can be emitted the moment the call is parsed.
func TestOpencodeGeneratedImageBecomesAUserFilePart(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_img"
	insMsg(t, db, "m1", ses, 1000, `{"role":"assistant","modelID":"m"}`)
	insPart(t, db, "p1", "m1", ses, 1,
		`{"type":"tool","tool":"af_40ed9852_generate_image","state":{"status":"completed","input":{"prompt":"a red circle"},`+
			`"output":"{\"files\":[{\"path\":\"/home/u/.cache/agent-fleet/generated/sid/image-1.png\"}],\"provider\":\"codex\"}"}}`)
	// Another server's tool of the same name is not af's picture.
	insPart(t, db, "p2", "m1", ses, 2,
		`{"type":"tool","tool":"pictures_generate_image","state":{"status":"completed","input":{},`+
			`"output":"{\"files\":[{\"path\":\"/tmp/other.png\"}]}"}}`)

	turns := readSession(db, ses)
	if len(turns) != 1 {
		t.Fatalf("turns = %+v, want one", turns)
	}
	parts := turns[0].Parts
	if len(parts) != 3 {
		t.Fatalf("parts = %+v, want the af trace, its card, and the other tool's trace alone", parts)
	}
	if parts[0].Kind != "tool" || parts[1].Kind != "userfile" || parts[2].Kind != "tool" {
		t.Fatalf("parts = %+v", parts)
	}
	if len(parts[1].Files) != 1 || parts[1].Files[0] != "/home/u/.cache/agent-fleet/generated/sid/image-1.png" {
		t.Fatalf("userfile = %+v", parts[1])
	}
}

// A refusal is prose, and there is no file to show.
func TestOpencodeGeneratedImageFailureShowsNoCard(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_img_err"
	insMsg(t, db, "m1", ses, 1000, `{"role":"assistant","modelID":"m"}`)
	insPart(t, db, "p1", "m1", ses, 1,
		`{"type":"tool","tool":"af_40ed9852_generate_image","state":{"status":"completed","input":{"prompt":"x"},`+
			`"output":"画像を生成できませんでした: codex is not logged in"}}`)

	for _, p := range readSession(db, ses)[0].Parts {
		if p.Kind == "userfile" {
			t.Fatalf("a failed generation produced a picture card: %+v", p)
		}
	}
}
