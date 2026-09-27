package imagegen

// Prompts are written for a model (ADR 0100 revision 9): the agent drafts and records nothing
// while none is chosen, files a record only under the studio's model or its family, and is told
// when the member switched the model under it.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAgentWritesNeedAModel(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"provider":"comfy","prompt":"a cat"}`)
	bindForTest(t, s.ID, "s1")
	if code, _ := putStudio(t, s.ID, `{"author":"agent","session":"s1","draft":{"prompt":"a dog"}}`); code != http.StatusConflict {
		t.Errorf("agent write with no model = %d, want 409", code)
	}
	// The member is not held to it: choosing the model is theirs.
	if code, _ := putStudio(t, s.ID, `{"author":"human","draft":{"model":"sdxl-base"}}`); code != http.StatusOK {
		t.Fatalf("member write = %d", code)
	}
	if code, res := putStudio(t, s.ID, `{"author":"agent","session":"s1","draft":{"prompt":"a dog"}}`); code != http.StatusOK || res.Studio.Draft.Prompt != "a dog" {
		t.Errorf("agent write with a model = %d %+v", code, res.Studio.Draft)
	}
}

func TestAgentRecordsGoToTheStudiosModel(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"provider":"comfy"}`)
	bindForTest(t, s.ID, "s1")
	add := func(body string) (int, string) {
		rec := studioDo(t, HandleKnowledge, http.MethodPost, "/imagegen/knowledge", body)
		var e struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		return rec.Code, e.Error.Code
	}
	// Measured on the dev deployment: with no model the agent filed its note under the
	// provider's name, as if it were a family.
	if code, why := add(`{"scope":"family","key":"comfy","note":"n","session":"s1"}`); code != http.StatusConflict || why != "no_model" {
		t.Errorf("no model = %d %s", code, why)
	}
	putStudio(t, s.ID, `{"author":"human","draft":{"model":"anima-base"}}`)
	if code, why := add(`{"scope":"model","key":"sdxl-base","note":"n","session":"s1"}`); code != http.StatusConflict || why != "wrong_key" {
		t.Errorf("another model's key = %d %s", code, why)
	}
	// The provider knows no family for the model here, so a family record cannot be checked.
	if code, why := add(`{"scope":"family","key":"sdxl","note":"n","session":"s1"}`); code != http.StatusConflict || why != "wrong_key" {
		t.Errorf("unknown family = %d %s", code, why)
	}
	if code, _ := add(`{"scope":"model","key":"anima-base","note":"n","session":"s1"}`); code != http.StatusOK {
		t.Errorf("the studio's model = %d", code)
	}
	// The member's own record from the pane's notes names its document directly.
	if code, _ := add(`{"scope":"family","key":"sdxl","note":"n"}`); code != http.StatusOK {
		t.Errorf("the member's record = %d", code)
	}
}

func TestAgentIsToldTheModelWasSwitched(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"provider":"comfy","model":"sdxl-base","prompt":"1girl, harbour"}`)
	bindForTest(t, s.ID, "s1")
	view := func() string {
		rec := studioDo(t, HandleStudio, http.MethodGet, "/imagegen/studios/"+s.ID+"?view=agent&session=s1", "")
		var v struct {
			Note string `json:"note"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return v.Note
	}
	_ = view() // the first call is the baseline
	putStudio(t, s.ID, `{"author":"human","draft":{"model":"qwen-image-2.1"}}`)
	if n := view(); !strings.Contains(n, `"sdxl-base" to "qwen-image-2.1"`) {
		t.Errorf("note after a switch = %q", n)
	}
	if n := view(); strings.Contains(n, "switched") {
		t.Errorf("the switch is told once, got %q again", n)
	}
}
