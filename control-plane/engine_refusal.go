package main

// engine_refusal.go — a refusal that names who holds the thing and what to press next
// (ADR 0085 decision 5).
//
// Every 409 in the catalogue area used to end in prose: "already recorded; choose a new
// destination or use verified reuse". An operator reading that has nothing to look for and
// nothing to press. The Console cannot turn a sentence into a button, so the refusal carries the
// two facts as fields, and the message stays for logs and for a reader with no Console.
//
// A separate type rather than two more fields on apiError: apiError is written positionally in
// hundreds of places, and a field added there would have to be added to every one of them.

import (
	"errors"
	"net/http"
	"strings"
)

// apiHolder is what stands in the way: a catalogue row, an ingest job, an object in the bucket,
// or a task that may still write one.
type apiHolder struct {
	Kind string `json:"kind"` // row | job | object | task
	ID   string `json:"id,omitempty"`
	Key  string `json:"key,omitempty"`
}

// apiNext is the one act that gets past the holder, as the Console's button.
type apiNext struct {
	Act    string `json:"act"` // register | complete | replace | forget_row | dismiss_job | wait
	Target string `json:"target,omitempty"`
}

// apiRefusal is an apiError plus the holder and the next act. `Plain` hands the bare error to
// code that only knows apiError; the fields are lost there, which is why the handlers this ADR
// touches write refusals with writeAPIRefusal and not writeAPIErr.
type apiRefusal struct {
	*apiError
	Holder *apiHolder
	Next   *apiNext
}

func refuse(status int, code, message string, holder *apiHolder, next *apiNext) *apiRefusal {
	return &apiRefusal{apiError: &apiError{status, code, message}, Holder: holder, Next: next}
}

// Plain is the refusal as a bare apiError, for callers that predate the fields.
func (r *apiRefusal) Plain() *apiError {
	if r == nil {
		return nil
	}
	return r.apiError
}

// body is the `error` object as the wire carries it. `code` and `message` stay exactly where
// every client reads them; `holder` and `next` are added beside them and omitted when nil, so a
// refusal without them is byte-for-byte what writeAPIErr writes.
func (r *apiRefusal) body() map[string]any {
	body := map[string]any{"code": r.code, "message": r.message}
	if r.Holder != nil {
		body["holder"] = r.Holder
	}
	if r.Next != nil {
		body["next"] = r.Next
	}
	return body
}

// writeAPIRefusal is writeAPIErr with the two fields.
func writeAPIRefusal(w http.ResponseWriter, r *apiRefusal) {
	writeAPIRefusalWith(w, r, "", nil)
}

// writeAPIRefusalWith adds ONE named value beside the error object.
//
// It exists for the stale plan (ADR 0085 decision 4): a refusal that says "what you were looking
// at has changed" and does not carry the current answer makes the caller ask again for it, and
// between the two calls it can change again. The fresh plan therefore rides on the refusal that
// rejected the old one.
func writeAPIRefusalWith(w http.ResponseWriter, r *apiRefusal, field string, v any) {
	if r == nil || r.apiError == nil {
		writeAPIErr(w, internalErr(errNilRefusal))
		return
	}
	out := map[string]any{"error": r.body()}
	if strings.TrimSpace(field) != "" {
		out[field] = v
	}
	writeJSON(w, r.status, out)
}

var errNilRefusal = errors.New("nil refusal")
