package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxFSFilePUTBodyBytes = 16 << 20
	maxFSFileContentBytes = 2 << 20
	maxFSPathBytes        = 4096
	maxFSNameBytes        = 255
)

var fsRevisionPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type fsPutAuditTargetContextKey struct{}

type cpFSFilePUTWire struct {
	Path             *string `json:"path"`
	Content          *string `json:"content"`
	BaseDiskRevision *string `json:"baseDiskRevision"`
}

// fsFilePut applies the public-edge envelope checks after authentication and
// workspace resolution, then replays the exact bytes to the Agent. Only the
// lexically canonical path is attached to the request context for auditing;
// content is never retained in audit metadata.
func (a agentProxyAPI) fsFilePut(w http.ResponseWriter, r *http.Request, res *resolved) {
	switch res.rt.State(r.Context()) {
	case "running":
	case "starting":
		writeAPIErr(w, &apiError{
			http.StatusConflict, "workspace_starting",
			"workspace is starting — wait for it to come up",
		})
		return
	default:
		writeAPIErr(w, &apiError{
			http.StatusConflict, "workspace_stopped",
			"workspace is stopped — start it first",
		})
		return
	}
	body, target, aerr := decodeCPFSFilePUT(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r = r.WithContext(context.WithValue(r.Context(), fsPutAuditTargetContextKey{}, target))
	a.rest(w, r, res)
}

func decodeCPFSFilePUT(r *http.Request) ([]byte, string, *apiError) {
	if !cpJSONContentType(r.Header.Get("Content-Type")) {
		return nil, "", &apiError{
			http.StatusUnsupportedMediaType, errCodeFSUnsupportedMedia,
			"Content-Type must be application/json",
		}
	}
	if r.ContentLength > maxFSFilePUTBodyBytes {
		return nil, "", &apiError{http.StatusRequestEntityTooLarge, errCodeFSTooLarge, "JSON body exceeds the size limit"}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFSFilePUTBodyBytes+1))
	if err != nil {
		return nil, "", &apiError{http.StatusBadRequest, errCodeFSBadRequest, "cannot read JSON body"}
	}
	if len(body) > maxFSFilePUTBodyBytes {
		return nil, "", &apiError{http.StatusRequestEntityTooLarge, errCodeFSTooLarge, "JSON body exceeds the size limit"}
	}
	if !utf8.Valid(body) {
		return nil, "", &apiError{http.StatusBadRequest, errCodeFSBadRequest, "JSON body is not valid UTF-8"}
	}
	if err := validateCPJSONSurrogates(body); err != nil {
		return nil, "", &apiError{http.StatusBadRequest, errCodeFSBadRequest, err.Error()}
	}

	var wire cpFSFilePUTWire
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return nil, "", &apiError{http.StatusBadRequest, errCodeFSBadRequest, "invalid JSON body"}
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, "", &apiError{http.StatusBadRequest, errCodeFSBadRequest, "JSON body must contain exactly one value"}
	}
	if wire.Path == nil || wire.Content == nil || wire.BaseDiskRevision == nil {
		return nil, "", &apiError{http.StatusBadRequest, errCodeFSBadRequest, "path, content, and baseDiskRevision are required"}
	}
	if !fsRevisionPattern.MatchString(*wire.BaseDiskRevision) {
		return nil, "", &apiError{http.StatusBadRequest, errCodeFSBadRequest, "baseDiskRevision is invalid"}
	}
	content := []byte(*wire.Content)
	switch {
	case len(content) > maxFSFileContentBytes:
		return nil, "", &apiError{http.StatusRequestEntityTooLarge, errCodeFSTooLarge, "decoded content exceeds 2 MiB"}
	case bytes.IndexByte(content, 0) >= 0:
		return nil, "", &apiError{http.StatusUnsupportedMediaType, errCodeFSBinaryNotSupported, "NUL bytes are not supported"}
	case bytes.IndexByte(content, '\r') >= 0:
		return nil, "", &apiError{http.StatusUnsupportedMediaType, errCodeFSUnsupportedNewline, "only LF newlines are supported"}
	}

	target := "<invalid-path>"
	if canonicalCPFSRelativePath(*wire.Path) {
		target = *wire.Path
	}
	return body, target, nil
}

func canonicalCPFSRelativePath(input string) bool {
	if input == "" || len(input) > maxFSPathBytes || strings.HasPrefix(input, "/") ||
		strings.Contains(input, "\\") || strings.IndexByte(input, 0) >= 0 {
		return false
	}
	if len(input) >= 2 && ((input[0] >= 'a' && input[0] <= 'z') || (input[0] >= 'A' && input[0] <= 'Z')) && input[1] == ':' {
		return false
	}
	for _, part := range strings.Split(input, "/") {
		if part == "" || part == "." || part == ".." || len(part) > maxFSNameBytes {
			return false
		}
	}
	return true
}

func cpJSONContentType(value string) bool {
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return false
	}
	if len(params) == 0 {
		return true
	}
	charset, ok := params["charset"]
	return len(params) == 1 && ok && strings.EqualFold(charset, "utf-8")
}

func validateCPJSONSurrogates(body []byte) error {
	inString := false
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			i++
			if i >= len(body) {
				return fmt.Errorf("invalid JSON string escape")
			}
			if body[i] != 'u' {
				continue
			}
			code, ok := cpJSONHex4(body, i+1)
			if !ok {
				return fmt.Errorf("invalid JSON Unicode escape")
			}
			i += 4
			switch {
			case code >= 0xd800 && code <= 0xdbff:
				if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
					return fmt.Errorf("lone high surrogate in JSON string")
				}
				low, ok := cpJSONHex4(body, i+3)
				if !ok || low < 0xdc00 || low > 0xdfff {
					return fmt.Errorf("lone high surrogate in JSON string")
				}
				i += 6
			case code >= 0xdc00 && code <= 0xdfff:
				return fmt.Errorf("lone low surrogate in JSON string")
			}
		}
	}
	return nil
}

func cpJSONHex4(body []byte, start int) (uint16, bool) {
	if start+4 > len(body) {
		return 0, false
	}
	var value uint16
	for _, c := range body[start : start+4] {
		value <<= 4
		switch {
		case c >= '0' && c <= '9':
			value += uint16(c - '0')
		case c >= 'a' && c <= 'f':
			value += uint16(c-'a') + 10
		case c >= 'A' && c <= 'F':
			value += uint16(c-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
