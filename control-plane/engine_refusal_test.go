package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteAPIRefusalCarriesHolderAndNext(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAPIRefusal(rec, refuse(http.StatusConflict, "engine_bad_body", "held",
		&apiHolder{Kind: "job", ID: "j1", Key: "image/vae/x.safetensors"},
		&apiNext{Act: "dismiss_job", Target: "j1"}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d", rec.Code)
	}
	var got struct {
		Error struct {
			Code, Message string
			Holder        *apiHolder
			Next          *apiNext
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != "engine_bad_body" || got.Error.Message != "held" {
		t.Fatalf("code/message: %+v", got.Error)
	}
	if got.Error.Holder == nil || got.Error.Holder.Kind != "job" || got.Error.Holder.ID != "j1" {
		t.Fatalf("holder: %+v", got.Error.Holder)
	}
	if got.Error.Next == nil || got.Error.Next.Act != "dismiss_job" || got.Error.Next.Target != "j1" {
		t.Fatalf("next: %+v", got.Error.Next)
	}
}

// A refusal with neither field is what writeAPIErr writes — the shape every client already reads.
func TestWriteAPIRefusalWithoutFieldsMatchesWriteAPIErr(t *testing.T) {
	a, b := httptest.NewRecorder(), httptest.NewRecorder()
	e := &apiError{http.StatusBadRequest, "x", "y"}
	writeAPIErr(a, e)
	writeAPIRefusal(b, &apiRefusal{apiError: e})
	if a.Body.String() != b.Body.String() || a.Code != b.Code {
		t.Fatalf("differ:\n%s\n%s", a.Body.String(), b.Body.String())
	}
	if (*apiRefusal)(nil).Plain() != nil {
		t.Fatal("nil Plain")
	}
}
