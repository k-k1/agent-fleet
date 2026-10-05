package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// af-gcloud-exec's Console login (ADR 0107 decision 3). The verification code the member
// pastes is the one secret this relay carries in a request body: it is bounded here, passed
// on as it is, and never read, logged or audited. The audit row names the profile and the
// attempt's reference, never the attempt id itself (whoever holds it can read the attempt's
// sign-in URL), the code or the URL.

// maxGCPLoginCodeBody is the code route's body limit, the Agent's own (gcpx.maxCodeBody).
const maxGCPLoginCodeBody = 4 << 10

// gcpLoginCode bounds the code route's body before the audited relay.
func (a agentProxyAPI) gcpLoginCode(w http.ResponseWriter, r *http.Request, res *resolved) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxGCPLoginCodeBody+1))
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "could not read the request body"})
		return
	}
	if len(b) > maxGCPLoginCodeBody {
		writeAPIErr(w, &apiError{http.StatusRequestEntityTooLarge, "body_too_large", "the verification code request is limited to 4 KiB"})
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	a.restLoginFlow(w, r, res)
}

// gcpAttemptRef is how the audit names a login attempt: the Agent's gcpx.AttemptRef, so the
// row matches the Agent's log line without the id that would let its reader see the URL.
func gcpAttemptRef(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:4])
}

// gcpLoginAudit classifies the gcp-login changes: the press (from a request or a Settings
// row), the cancel and the code. Reads (the list, the attempt poll) are not audited.
func gcpLoginAudit(r *http.Request, p string, q url.Values) (action, target string, ok bool) {
	rest, isGCP := strings.CutPrefix(p, "/api/gcp-login/")
	if !isGCP || r.Method != http.MethodPost {
		return "", "", false
	}
	name := r.PathValue("name")
	switch {
	case strings.HasPrefix(rest, "profiles/") && strings.HasSuffix(rest, "/code"):
		return "gcp.login.code", "profile: " + name + ", attempt: " + gcpAttemptRef(r.PathValue("attempt")), true
	case strings.HasPrefix(rest, "profiles/") && strings.HasSuffix(rest, "/start"):
		if q.Get("force") == "1" {
			return "gcp.login.start", "profile: " + name + " (log in again)", true
		}
		return "gcp.login.start", "profile: " + name, true
	case strings.HasSuffix(rest, "/start"), strings.HasSuffix(rest, "/cancel"):
		id, verb, _ := strings.Cut(rest, "/")
		// The profile is only the Console's hint in the query: the Agent decides from the id.
		return "gcp.login." + verb, id + " (profile hint: " + q.Get("profile") + ")", true
	}
	return "", "", false
}

// relayLogPath is a relayed path as the CP's log may show it: a gcp-login attempt id is
// replaced by its reference, since whoever holds the id can read the attempt's sign-in URL.
func relayLogPath(p string) string {
	rest, ok := strings.CutPrefix(p, "/api/gcp-login/profiles/")
	if !ok {
		return p
	}
	segs := strings.Split(rest, "/")
	if len(segs) >= 3 && segs[1] == "attempts" {
		segs[2] = "ref:" + gcpAttemptRef(segs[2])
	}
	return "/api/gcp-login/profiles/" + strings.Join(segs, "/")
}

// relayLogErr is err as the CP's log may show it for path: a transport error quotes the
// Agent URL it was sending to, which for a gcp-login attempt holds the id.
func relayLogErr(path string, err error) error {
	var ue *url.Error
	if strings.HasPrefix(path, "/api/gcp-login/") && errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
