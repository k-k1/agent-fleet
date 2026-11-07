package tenantsrv

import (
	"bytes"
	"errors"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
)

// internalErr answers a fixed message plus a reference; the cause stays in the server log.
func TestInternalErrHidesCause(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	e := internalErr(errors.New("kms: key arn:aws:kms:region:000000000000:key/x denied"))
	rec := httptest.NewRecorder()
	writeAPIErr(rec, e)
	if e.Code != "internal" || e.Status != 500 || strings.Contains(rec.Body.String(), "kms") {
		t.Fatalf("response: %+v %s", e, rec.Body.String())
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(e.Message, "internal error (ref: "), ")")
	if len(ref) != 8 || !strings.Contains(buf.String(), "ref="+ref) || !strings.Contains(buf.String(), "denied") {
		t.Fatalf("message %q log %s", e.Message, buf.String())
	}
}
