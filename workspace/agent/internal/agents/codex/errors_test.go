package codex

import "testing"

// decodeCodexError covers the two CodexErrorInfo wire shapes verified against the real
// v0.146.0 app-server schema (codex app-server generate-json-schema): a bare string enum
// and a single-key object for the variants that carry an httpStatusCode.

func TestDecodeCodexErrorBareEnum(t *testing.T) {
	e, ok := decodeCodexError([]byte(`{"message":"You've hit your usage limit.","additionalDetails":null,"codexErrorInfo":"usageLimitExceeded"}`))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if e.message != "You've hit your usage limit." || e.label != "usageLimitExceeded" || e.status != 0 {
		t.Fatalf("decoded = %+v", e)
	}
	if got, want := e.summary(), "[error] usageLimitExceeded: You've hit your usage limit."; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	part := e.part()
	if part.Kind != "error" || part.Info != "usageLimitExceeded" || part.Text != "You've hit your usage limit." {
		t.Fatalf("part = %+v", part)
	}
}

func TestDecodeCodexErrorObjectVariantWithStatus(t *testing.T) {
	e, ok := decodeCodexError([]byte(`{"message":"connect failed","codexErrorInfo":{"httpConnectionFailed":{"httpStatusCode":429}}}`))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if e.label != "httpConnectionFailed" || e.status != 429 {
		t.Fatalf("decoded = %+v", e)
	}
	if got, want := e.summary(), "[error] httpConnectionFailed (HTTP 429): connect failed"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestDecodeCodexErrorAdditionalDetails(t *testing.T) {
	raw := `{"message":"rate limited","additionalDetails":"retry in 5m","codexErrorInfo":"serverOverloaded"}`
	e, ok := decodeCodexError([]byte(raw))
	if !ok {
		t.Fatal("ok = false")
	}
	if e.message != "rate limited (retry in 5m)" {
		t.Fatalf("message = %q", e.message)
	}
	if !e.retryable() {
		t.Fatal("serverOverloaded must be retryable")
	}
}

func TestCodexErrorRetryableClassification(t *testing.T) {
	if !(codexError{status: 500}).retryable() {
		t.Fatal("HTTP 500 must be retryable")
	}
	for _, e := range []codexError{{label: "usageLimitExceeded"}, {status: 401}, {label: "contextWindowExceeded"}} {
		if e.retryable() {
			t.Fatalf("permanent error classified retryable: %+v", e)
		}
	}
}

func TestDecodeCodexErrorNoMessageNotOK(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, ``, `{"message":""}`, `not json`} {
		if _, ok := decodeCodexError([]byte(raw)); ok {
			t.Fatalf("raw=%q: ok = true, want false", raw)
		}
	}
}

func TestDecodeCodexErrorInfoAbsent(t *testing.T) {
	e, ok := decodeCodexError([]byte(`{"message":"plain failure"}`))
	if !ok {
		t.Fatal("ok = false")
	}
	if e.label != "" {
		t.Fatalf("label = %q, want empty", e.label)
	}
	if got, want := e.summary(), "[error] plain failure"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got, want := e.part().Info, "error"; got != want {
		t.Fatalf("part().Info = %q, want fallback %q", got, want)
	}
}

// codexErrorFromRPC covers the OTHER failure surface: turn/start rejected synchronously
// as a JSON-RPC error rather than a completed Turn.

func TestCodexErrorFromRPCPlainMessage(t *testing.T) {
	err := &rpcError{Code: -32000, Message: "usage limit exceeded"}
	e, ok := codexErrorFromRPC(err)
	if !ok || e.message != "usage limit exceeded" || e.label != "" {
		t.Fatalf("decoded = %+v ok=%v", e, ok)
	}
}

func TestCodexErrorFromRPCStructuredData(t *testing.T) {
	err := &rpcError{Code: -32000, Message: "outer", Data: []byte(`{"message":"inner detail","codexErrorInfo":"usageLimitExceeded"}`)}
	e, ok := codexErrorFromRPC(err)
	if !ok || e.message != "inner detail" || e.label != "usageLimitExceeded" {
		t.Fatalf("decoded = %+v ok=%v, want the structured Data to win over the bare message", e, ok)
	}
}

func TestCodexErrorFromRPCNonRPCError(t *testing.T) {
	if _, ok := codexErrorFromRPC(errTimeout); ok {
		t.Fatal("ok = true for a plain error, want false")
	}
}

var errTimeout = &notAnRPCError{}

type notAnRPCError struct{}

func (*notAnRPCError) Error() string { return "turn/start: timeout" }
