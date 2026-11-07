package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// A 500 answers a fixed message and a short reference; the original error text appears
// only in the server log, under the same reference.
func TestInternalErrHidesCauseAndLogsIt(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	const cause = "pq: relation \"tenant_secrets\" does not exist at db.internal.example:5432"
	rec := httptest.NewRecorder()
	writeAPIErr(rec, internalErr(errors.New(cause)))

	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "internal" {
		t.Errorf("code %q", body.Error.Code)
	}
	if strings.Contains(rec.Body.String(), "tenant_secrets") || strings.Contains(rec.Body.String(), "db.internal") {
		t.Fatalf("cause reached the response: %s", rec.Body.String())
	}
	m := regexp.MustCompile(`^internal error \(ref: ([0-9a-f]{8})\)$`).FindStringSubmatch(body.Error.Message)
	if m == nil {
		t.Fatalf("message %q", body.Error.Message)
	}
	logged := buf.String()
	if !strings.Contains(logged, "ref="+m[1]) || !strings.Contains(logged, "tenant_secrets") {
		t.Fatalf("log lacks the cause under ref %s:\n%s", m[1], logged)
	}
}
