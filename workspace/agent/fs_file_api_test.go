package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func putJSONBody(path, content, revision string) string {
	body, _ := json.Marshal(map[string]string{
		"path": path, "content": content, "baseDiskRevision": revision,
	})
	return string(body)
}

func doAgentFSPut(t *testing.T, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/fs/file", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	handleFSFilePut(rec, req)
	return rec
}

func responseErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, rec.Body.String())
	}
	return envelope.Error.Code
}

func TestFSFileGetRevisionAndEditability(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	const content = "# title\nbody\n"
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}

	for _, queryPath := range []string{"note.md", filepath.Join(root, "note.md")} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/fs/file?path="+url.QueryEscape(queryPath), nil)
		handleFSFile(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %q: status=%d body=%s", queryPath, rec.Code, rec.Body.String())
		}
		var got struct {
			Path              string  `json:"path"`
			Size              int64   `json:"size"`
			Content           string  `json:"content"`
			Revision          string  `json:"revision"`
			Editable          bool    `json:"editable"`
			EditabilityReason *string `json:"editabilityReason"`
			Binary            bool    `json:"binary"`
			Truncated         bool    `json:"truncated"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Path != "note.md" || got.Content != content || got.Size != int64(len(content)) ||
			got.Revision != fileRevision([]byte(content)) || !got.Editable ||
			got.EditabilityReason != nil || got.Binary || got.Truncated {
			t.Fatalf("unexpected GET response: %+v", got)
		}
	}
}

func TestFSFileEmptyFileIsEditableAndSavable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	handleFSFile(get, httptest.NewRequest(http.MethodGet, "/fs/file?path=empty.txt", nil))
	var payload map[string]any
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &payload) != nil {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	if payload["editable"] != true || payload["content"] != "" || payload["revision"] != fileRevision(nil) {
		t.Fatalf("GET payload=%#v", payload)
	}
	put := doAgentFSPut(t, putJSONBody("empty.txt", "", fileRevision(nil)), "application/json")
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
	}
}

func TestFSFileGetEditabilityReasons(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	files := []struct {
		name, reason string
		content      []byte
		binary       bool
		truncated    bool
	}{
		{"nul.bin", "binary", []byte{'a', 0, 'b'}, true, false},
		{"invalid.txt", "invalid_utf8", []byte{0xff, 0xfe}, true, false},
		{"crlf.txt", "unsupported_newline", []byte("a\r\nb\r"), false, false},
		{"large.txt", "too_large", bytes.Repeat([]byte("x"), maxEditorFileBytes+1), false, true},
	}
	for _, tc := range files {
		if err := os.WriteFile(filepath.Join(root, tc.name), tc.content, 0o600); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		handleFSFile(rec, httptest.NewRequest(http.MethodGet, "/fs/file?path="+tc.name, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["editable"] != false || got["editabilityReason"] != tc.reason ||
			got["binary"] != tc.binary || got["truncated"] != tc.truncated {
			t.Errorf("%s: unexpected response: %#v", tc.name, got)
		}
		if _, ok := got["revision"]; ok {
			t.Errorf("%s: non-editable response leaked a revision", tc.name)
		}
	}
}

func TestFSFileReadOnlyRootNeverReturnsRevision(t *testing.T) {
	root := t.TempDir()
	docs := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	t.Setenv("AGENT_DOCS_DIR", docs)
	path := filepath.Join(docs, "guide.md")
	if err := os.WriteFile(path, []byte("guide\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleFSFile(rec, httptest.NewRequest(http.MethodGet, "/fs/file?path="+path, nil))
	var got map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got["editabilityReason"] != "read_only_root" || got["editable"] != false {
		t.Fatalf("read-only response: %#v", got)
	}
	if _, ok := got["revision"]; ok {
		t.Fatal("read-only root returned a revision")
	}
}

func TestFSFileSymlinksDeniedForGetDownloadAndPut(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "real-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real-dir", "nested.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real-dir", filepath.Join(root, "link-dir")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"link.txt", "link-dir/nested.txt"} {
		for _, endpoint := range []string{"/fs/file?path=", "/fs/download?path="} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, endpoint+path, nil)
			if strings.Contains(endpoint, "download") {
				handleFSDownload(rec, req)
			} else {
				handleFSFile(rec, req)
			}
			if rec.Code != http.StatusBadRequest || responseErrorCode(t, rec) != errCodeFSSymlinkNotAllowed {
				t.Errorf("%s %s: status=%d body=%s", endpoint, path, rec.Code, rec.Body.String())
			}
		}
		rec := doAgentFSPut(t, putJSONBody(path, "new\n", fileRevision([]byte("old\n"))), "application/json")
		if rec.Code != http.StatusBadRequest || responseErrorCode(t, rec) != errCodeFSSymlinkNotAllowed {
			t.Errorf("PUT %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestCodexGeneratedImagesAreReadOnlyAndNotEnumerable(t *testing.T) {
	t.Setenv("AF_BROWSE_ROOT", t.TempDir())
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	dir := filepath.Join(codexGeneratedImagesRoot(), "job")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(dir, "result.png")
	if err := os.WriteFile(image, []byte{'p', 'n', 'g'}, 0o600); err != nil {
		t.Fatal(err)
	}

	get := httptest.NewRecorder()
	handleFSFile(get, httptest.NewRequest(http.MethodGet, "/fs/file?path="+url.QueryEscape(image), nil))
	if get.Code != http.StatusOK {
		t.Fatalf("generated image GET: status=%d body=%s", get.Code, get.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(get.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["path"] != image || payload["editabilityReason"] != "read_only_root" {
		t.Fatalf("generated image response = %#v", payload)
	}

	for _, endpoint := range []string{"/fs/tree?path=", "/fs/search?path="} {
		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodGet, endpoint+url.QueryEscape(codexGeneratedImagesRoot()), nil)
		if strings.Contains(endpoint, "search") {
			httpReq.URL.RawQuery += "&q=result"
			handleFSSearch(rec, httpReq)
		} else {
			handleFSTree(rec, httpReq)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s generated-images root: status=%d body=%s", endpoint, rec.Code, rec.Body.String())
		}
	}

	for _, name := range []string{"notes.txt", "vector.svg"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		handleFSFile(rec, httptest.NewRequest(http.MethodGet, "/fs/file?path="+url.QueryEscape(path), nil))
		if rec.Code != http.StatusForbidden || responseErrorCode(t, rec) != errCodeFSDenied {
			t.Errorf("GET %s: status=%d body=%s", name, rec.Code, rec.Body.String())
		}
	}
	if err := os.Symlink("result.png", filepath.Join(dir, "linked.png")); err != nil {
		t.Fatal(err)
	}
	symlink := httptest.NewRecorder()
	handleFSFile(symlink, httptest.NewRequest(http.MethodGet, "/fs/file?path="+url.QueryEscape(filepath.Join(dir, "linked.png")), nil))
	if symlink.Code != http.StatusBadRequest || responseErrorCode(t, symlink) != errCodeFSSymlinkNotAllowed {
		t.Fatalf("GET generated-image symlink: status=%d body=%s", symlink.Code, symlink.Body.String())
	}
}

func TestFSFilePathAndDenylistValidation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	if err := os.Mkdir(filepath.Join(root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ssh", "config"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := fileRevision([]byte("old\n"))
	denied := doAgentFSPut(t, putJSONBody(".ssh/config", "new\n", base), "application/json")
	if denied.Code != http.StatusForbidden || responseErrorCode(t, denied) != errCodeFSDenied {
		t.Fatalf("denylist: status=%d body=%s", denied.Code, denied.Body.String())
	}

	badPaths := []string{
		"", "/tmp/a", "../a", "a/../b", "./a", "a//b", "a/", `a\b`,
		`C:\a`, "C:a", strings.Repeat("a", maxFSNameBytes+1),
		strings.Repeat("a/", 17) + strings.Repeat("b", maxFSPathBytes),
	}
	for _, path := range badPaths {
		rec := doAgentFSPut(t, putJSONBody(path, "new\n", base), "application/json")
		if rec.Code != http.StatusBadRequest || responseErrorCode(t, rec) != errCodeFSBadPath {
			t.Errorf("path %q: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}

	for _, path := range []string{".ssh/config", "/etc/passwd"} {
		rec := httptest.NewRecorder()
		handleFSFile(rec, httptest.NewRequest(http.MethodGet, "/fs/file?path="+path, nil))
		wantStatus, wantCode := http.StatusForbidden, errCodeFSDenied
		if strings.HasPrefix(path, "/") {
			wantStatus, wantCode = http.StatusBadRequest, errCodeFSBadPath
		}
		if rec.Code != wantStatus || responseErrorCode(t, rec) != wantCode {
			t.Errorf("GET %q: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestFSFilePutCASAndPermissionPreservation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	conflict := doAgentFSPut(t, putJSONBody("a.txt", "new\n", fileRevision([]byte("stale\n"))), "application/json")
	if conflict.Code != http.StatusConflict || responseErrorCode(t, conflict) != errCodeFSRevisionConflict {
		t.Fatalf("conflict: status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if got, _ := os.ReadFile(path); string(got) != "old\n" {
		t.Fatalf("conflict changed file: %q", got)
	}

	success := doAgentFSPut(t, putJSONBody("a.txt", "new\n", fileRevision([]byte("old\n"))), "application/json; charset=utf-8")
	if success.Code != http.StatusOK {
		t.Fatalf("success: status=%d body=%s", success.Code, success.Body.String())
	}
	var got fsFilePUTResponse
	if err := json.Unmarshal(success.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != "a.txt" || got.Size != len("new\n") || got.Revision != fileRevision([]byte("new\n")) {
		t.Fatalf("response: %+v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions=%#o want 0640", info.Mode().Perm())
	}
}

func TestFSFilePutRequiresExistingRegularFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"missing.txt", "dir", "pipe"} {
		rec := doAgentFSPut(t, putJSONBody(path, "new\n", fileRevision(nil)), "application/json")
		if rec.Code != http.StatusNotFound || responseErrorCode(t, rec) != errCodeFSNotFile {
			t.Errorf("%s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestFSFilePutCurrentFileValidationPrecedesCAS(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	cases := []struct {
		name string
		data []byte
		code string
		http int
	}{
		{"large", bytes.Repeat([]byte("x"), maxEditorFileBytes+1), errCodeFSTooLarge, 413},
		{"nul", []byte{'x', 0}, errCodeFSBinaryNotSupported, 415},
		{"utf8", []byte{0xff}, errCodeFSBinaryNotSupported, 415},
		{"cr", []byte("x\r\n"), errCodeFSUnsupportedNewline, 415},
	}
	for _, tc := range cases {
		path := tc.name + ".txt"
		if err := os.WriteFile(filepath.Join(root, path), tc.data, 0o600); err != nil {
			t.Fatal(err)
		}
		rec := doAgentFSPut(t, putJSONBody(path, "new\n", fileRevision([]byte("definitely stale"))), "application/json")
		if rec.Code != tc.http || responseErrorCode(t, rec) != tc.code {
			t.Errorf("%s: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestFSFilePutStrictJSON(t *testing.T) {
	validRev := fileRevision(nil)
	valid := func(contentLiteral string) string {
		return `{"path":"a.txt","content":` + contentLiteral + `,"baseDiskRevision":"` + validRev + `"}`
	}
	cases := []struct {
		name, body, contentType string
		status                  int
		code                    string
		okContent               string
	}{
		{"lone high", valid(`"\ud800"`), "application/json", 400, errCodeFSBadRequest, ""},
		{"lone low", valid(`"\udc00"`), "application/json", 400, errCodeFSBadRequest, ""},
		{"bad pair", valid(`"\ud800\u0041"`), "application/json", 400, errCodeFSBadRequest, ""},
		{"pair", valid(`"\ud83d\ude00"`), "application/json", 0, "", "😀"},
		{"literal replacement", valid(`"�"`), "application/json", 0, "", "�"},
		{"escaped replacement", valid(`"\ufffd"`), "application/json", 0, "", "�"},
		{"escaped backslash", valid(`"\\ud800"`), "application/json", 0, "", `\ud800`},
		{"unknown field", strings.TrimSuffix(valid(`""`), "}") + `,"extra":1}`, "application/json", 400, errCodeFSBadRequest, ""},
		{"second value", valid(`""`) + `{}`, "application/json", 400, errCodeFSBadRequest, ""},
		{"trailing garbage", valid(`""`) + `x`, "application/json", 400, errCodeFSBadRequest, ""},
		{"missing field", `{"path":"a.txt","content":""}`, "application/json", 400, errCodeFSBadRequest, ""},
		{"wrong type", `{"path":1,"content":"","baseDiskRevision":"` + validRev + `"}`, "application/json", 400, errCodeFSBadRequest, ""},
		{"bad revision", `{"path":"a.txt","content":"","baseDiskRevision":"SHA256:00"}`, "application/json", 400, errCodeFSBadRequest, ""},
		{"nul", valid(`"\u0000"`), "application/json", 415, errCodeFSBinaryNotSupported, ""},
		{"cr", valid(`"\r\n"`), "application/json", 415, errCodeFSUnsupportedNewline, ""},
		{"media", valid(`""`), "text/plain", 415, errCodeFSUnsupportedMedia, ""},
		{"media extra param", valid(`""`), "application/json; charset=utf-8; version=1", 415, errCodeFSUnsupportedMedia, ""},
		{"empty body", "", "application/json", 400, errCodeFSBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/fs/file", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			got, aerr := decodeFSFilePUT(req)
			if tc.status == 0 {
				if aerr != nil || string(got.content) != tc.okContent {
					t.Fatalf("got request=%+v error=%+v", got, aerr)
				}
				return
			}
			if aerr == nil || aerr.status != tc.status || aerr.code != tc.code {
				t.Fatalf("error=%+v want status=%d code=%s", aerr, tc.status, tc.code)
			}
		})
	}

	invalidUTF8 := append([]byte(`{"path":"a.txt","content":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","baseDiskRevision":"`+validRev+`"}`)...)
	req := httptest.NewRequest(http.MethodPut, "/fs/file", bytes.NewReader(invalidUTF8))
	req.Header.Set("Content-Type", "application/json")
	if _, aerr := decodeFSFilePUT(req); aerr == nil || aerr.code != errCodeFSBadRequest {
		t.Fatalf("invalid UTF-8 error=%+v", aerr)
	}

	req = httptest.NewRequest(http.MethodPut, "/fs/file", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = maxFSFilePUTBodyBytes + 1
	if _, aerr := decodeFSFilePUT(req); aerr == nil || aerr.status != 413 || aerr.code != errCodeFSTooLarge {
		t.Fatalf("wire limit error=%+v", aerr)
	}

	req = httptest.NewRequest(http.MethodPut, "/fs/file", bytes.NewReader(make([]byte, maxFSFilePUTBodyBytes+1)))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1 // exercise the streaming/chunked limit, not the declaration shortcut
	if _, aerr := decodeFSFilePUT(req); aerr == nil || aerr.status != 413 || aerr.code != errCodeFSTooLarge {
		t.Fatalf("streaming wire limit error=%+v", aerr)
	}
}

func TestFSFilePutDecodedContentLimit(t *testing.T) {
	atLimit := strings.Repeat("x", maxEditorFileBytes)
	req := httptest.NewRequest(http.MethodPut, "/fs/file", strings.NewReader(putJSONBody("a.txt", atLimit, fileRevision(nil))))
	req.Header.Set("Content-Type", "application/json")
	if got, aerr := decodeFSFilePUT(req); aerr != nil || len(got.content) != maxEditorFileBytes {
		t.Fatalf("content at limit: len=%d error=%+v", len(got.content), aerr)
	}

	content := strings.Repeat("x", maxEditorFileBytes+1)
	req = httptest.NewRequest(http.MethodPut, "/fs/file", strings.NewReader(putJSONBody("a.txt", content, fileRevision(nil))))
	req.Header.Set("Content-Type", "application/json")
	if _, aerr := decodeFSFilePUT(req); aerr == nil || aerr.status != 413 || aerr.code != errCodeFSTooLarge {
		t.Fatalf("decoded limit error=%+v", aerr)
	}
}

func serviceWithOps(ops fsAtomicWriteOps) fsFileService {
	return fsFileService{locks: &keyedFileMutex{}, writeOps: ops}
}

func directPut(path, content string, base []byte) fsFilePUTRequest {
	return fsFilePUTRequest{path: path, content: []byte(content), baseDiskRevision: fileRevision(base)}
}

func TestFSFileAtomicFailureClassification(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*fsAtomicWriteOps)
		wantCode    string
		wantContent string
	}{
		{
			name: "temp fsync before rename",
			mutate: func(ops *fsAtomicWriteOps) {
				ops.fsync = func(int) error { return errors.New("injected temp fsync") }
			},
			wantCode: errCodeFSWriteFailed, wantContent: "old\n",
		},
		{
			name: "rename before namespace change",
			mutate: func(ops *fsAtomicWriteOps) {
				ops.renameat = func(int, string, int, string) error { return errors.New("injected rename") }
			},
			wantCode: errCodeFSWriteFailed, wantContent: "old\n",
		},
		{
			name: "parent fsync after rename",
			mutate: func(ops *fsAtomicWriteOps) {
				calls := 0
				ops.fsync = func(fd int) error {
					calls++
					if calls == 2 {
						return errors.New("injected parent fsync")
					}
					return unix.Fsync(fd)
				}
			},
			wantCode: errCodeFSWriteStateUnknown, wantContent: "new\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("AF_BROWSE_ROOT", root)
			target := filepath.Join(root, "a.txt")
			if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ops := defaultFSAtomicWriteOps
			tc.mutate(&ops)
			_, aerr := putFSFile(t.Context(), directPut("a.txt", "new\n", []byte("old\n")), serviceWithOps(ops))
			if aerr == nil || aerr.code != tc.wantCode {
				t.Fatalf("error=%+v want %s", aerr, tc.wantCode)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != tc.wantContent {
				t.Fatalf("content=%q err=%v want=%q", got, err, tc.wantContent)
			}
			matches, err := filepath.Glob(filepath.Join(root, ".af-edit-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("temporary files remain: %v err=%v", matches, err)
			}
		})
	}
}

func TestFSFileAtomicWriteOrdering(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var order []string
	ops := defaultFSAtomicWriteOps
	ops.fchmod = func(fd int, mode uint32) error {
		order = append(order, "fchmod")
		return unix.Fchmod(fd, mode)
	}
	syncCount := 0
	ops.fsync = func(fd int) error {
		syncCount++
		if syncCount == 1 {
			order = append(order, "temp_fsync")
		} else {
			order = append(order, "parent_fsync")
		}
		return unix.Fsync(fd)
	}
	ops.renameat = func(oldFD int, oldPath string, newFD int, newPath string) error {
		order = append(order, "rename")
		return unix.Renameat(oldFD, oldPath, newFD, newPath)
	}
	if _, aerr := putFSFile(t.Context(), directPut("a.txt", "new\n", []byte("old\n")), serviceWithOps(ops)); aerr != nil {
		t.Fatalf("PUT: %+v", aerr)
	}
	if got, want := strings.Join(order, ","), "fchmod,temp_fsync,rename,parent_fsync"; got != want {
		t.Fatalf("order=%q want=%q", got, want)
	}
}

func TestFSFileConcurrentPUTsSerializeThroughParentSync(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentSyncEntered := make(chan struct{})
	releaseParentSync := make(chan struct{})
	var fsyncCalls atomic.Int32
	ops := defaultFSAtomicWriteOps
	ops.fsync = func(fd int) error {
		call := fsyncCalls.Add(1)
		if call == 2 {
			close(parentSyncEntered)
			<-releaseParentSync
		}
		return unix.Fsync(fd)
	}
	service := serviceWithOps(ops)
	type result struct {
		resp fsFilePUTResponse
		err  *fsAPIError
	}
	first := make(chan result, 1)
	second := make(chan result, 1)
	go func() {
		resp, err := putFSFile(t.Context(), directPut("a.txt", "first\n", []byte("old\n")), service)
		first <- result{resp, err}
	}()
	select {
	case <-parentSyncEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first PUT did not reach parent fsync")
	}
	go func() {
		resp, err := putFSFile(t.Context(), directPut("a.txt", "second\n", []byte("old\n")), service)
		second <- result{resp, err}
	}()
	select {
	case got := <-second:
		t.Fatalf("second PUT completed before first parent fsync result: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseParentSync)
	if got := <-first; got.err != nil {
		t.Fatalf("first PUT: %+v", got.err)
	}
	if got := <-second; got.err == nil || got.err.code != errCodeFSRevisionConflict {
		t.Fatalf("second PUT: %+v", got)
	}
	if got, _ := os.ReadFile(target); string(got) != "first\n" {
		t.Fatalf("final content=%q", got)
	}
}

func TestFSFileGetWaitsForInFlightPut(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	renameEntered := make(chan struct{})
	releaseRename := make(chan struct{})
	ops := defaultFSAtomicWriteOps
	ops.renameat = func(oldFD int, oldPath string, newFD int, newPath string) error {
		close(renameEntered)
		<-releaseRename
		return unix.Renameat(oldFD, oldPath, newFD, newPath)
	}
	service := serviceWithOps(ops)

	// A PUT whose client already timed out and disconnected keeps running on
	// the Agent. A recovery GET issued after that timeout must not observe the
	// pre-rename base while the rename is still pending.
	putDone := make(chan *fsAPIError, 1)
	go func() {
		_, aerr := putFSFile(t.Context(), directPut("a.txt", "new\n", []byte("old\n")), service)
		putDone <- aerr
	}()
	select {
	case <-renameEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("PUT did not reach rename")
	}

	type getResult struct {
		resp map[string]any
		err  *fsAPIError
	}
	getDone := make(chan getResult, 1)
	go func() {
		resp, aerr := readFSFile("a.txt", service)
		getDone <- getResult{resp, aerr}
	}()
	select {
	case got := <-getDone:
		t.Fatalf("recovery GET overtook the in-flight PUT: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRename)
	if aerr := <-putDone; aerr != nil {
		t.Fatalf("PUT: %+v", aerr)
	}
	got := <-getDone
	if got.err != nil {
		t.Fatalf("GET: %+v", got.err)
	}
	if got.resp["content"] != "new\n" || got.resp["revision"] != fileRevision([]byte("new\n")) {
		t.Fatalf("GET observed the pre-rename base: %#v", got.resp)
	}
}

func TestFSFilePutCancelledBeforeLockDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var writeTouched atomic.Bool
	ops := defaultFSAtomicWriteOps
	ops.fchmod = func(fd int, mode uint32) error {
		writeTouched.Store(true)
		return unix.Fchmod(fd, mode)
	}
	service := serviceWithOps(ops)

	// The client timed out while this PUT's goroutine was still parked before
	// the path mutex, so the recovery GET won the lock first and observed the
	// old base as its discard target.
	got, aerr := readFSFile("a.txt", service)
	if aerr != nil || got["content"] != "old\n" {
		t.Fatalf("GET before cancelled PUT: %#v error=%+v", got, aerr)
	}

	// The parked PUT resumes with its request context already cancelled. It
	// must abort at the post-lock check: a CAS against the old base would
	// otherwise succeed and invalidate the snapshot the GET just returned.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, aerr = putFSFile(ctx, directPut("a.txt", "new\n", []byte("old\n")), service)
	if aerr == nil || aerr.status != 499 || aerr.code != errCodeFSWriteCancelled {
		t.Fatalf("cancelled PUT error=%+v", aerr)
	}
	if writeTouched.Load() {
		t.Fatal("cancelled PUT reached the write path")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "old\n" {
		t.Fatalf("cancelled PUT changed disk: %q err=%v", got, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".af-edit-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files remain: %v err=%v", matches, err)
	}
	// The aborted PUT released the lock without writing; a follow-up GET reads
	// the unchanged base instead of waiting behind a write.
	got, aerr = readFSFile("a.txt", service)
	if aerr != nil || got["content"] != "old\n" || got["revision"] != fileRevision([]byte("old\n")) {
		t.Fatalf("GET after cancelled PUT: %#v error=%+v", got, aerr)
	}
}

func TestFSFilePutHandlerHonorsRequestCancellation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequest(http.MethodPut, "/fs/file",
		strings.NewReader(putJSONBody("a.txt", "new\n", fileRevision([]byte("old\n"))))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleFSFilePut(rec, req)
	if rec.Code != 499 || responseErrorCode(t, rec) != errCodeFSWriteCancelled {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "old\n" {
		t.Fatalf("cancelled handler PUT changed disk: %q err=%v", got, err)
	}
}

func TestFSFileExternalWriterRaceIsOutsideCASGuarantee(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultFSAtomicWriteOps
	ops.renameat = func(oldFD int, oldPath string, newFD int, newPath string) error {
		// A non-cooperating writer changes the namespace after CAS comparison.
		// v1 intentionally cannot detect this interval; the subsequent rename wins.
		if err := os.WriteFile(target, []byte("external\n"), 0o600); err != nil {
			return err
		}
		return unix.Renameat(oldFD, oldPath, newFD, newPath)
	}
	if _, aerr := putFSFile(t.Context(), directPut("a.txt", "saved\n", []byte("old\n")), serviceWithOps(ops)); aerr != nil {
		t.Fatalf("PUT: %+v", aerr)
	}
	if got, _ := os.ReadFile(target); string(got) != "saved\n" {
		t.Fatalf("final content=%q; test documents that API rename wins this unsupported race", got)
	}
}

func TestFSFileAtomicVisibility(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	oldContent := bytes.Repeat([]byte("o"), 1<<20)
	newContent := bytes.Repeat([]byte("n"), 1<<20)
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, oldContent, 0o600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad atomic.Value
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := os.ReadFile(target)
				if err != nil || (!bytes.Equal(got, oldContent) && !bytes.Equal(got, newContent)) {
					bad.Store(fmt.Sprintf("len=%d err=%v", len(got), err))
					return
				}
			}
		}()
	}
	req := fsFilePUTRequest{path: "a.txt", content: newContent, baseDiskRevision: fileRevision(oldContent)}
	_, aerr := putFSFile(t.Context(), req, serviceWithOps(defaultFSAtomicWriteOps))
	close(stop)
	wg.Wait()
	if aerr != nil {
		t.Fatalf("PUT: %+v", aerr)
	}
	if value := bad.Load(); value != nil {
		t.Fatalf("reader observed non-atomic content: %v", value)
	}
}

func TestReadStableFileSnapshotRetriesAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	snapshot, aerr := readStableFileSnapshot(file, snapshotHooks{
		afterBeforeStat: func(attempt int) {
			if attempt == 0 {
				if err := os.WriteFile(path, []byte("updated"), 0o600); err != nil {
					t.Error(err)
				}
			}
		},
	})
	if aerr != nil || string(snapshot.bytes) != "updated" || snapshot.size != int64(len("updated")) {
		t.Fatalf("snapshot=%+v error=%+v", snapshot, aerr)
	}

	var calls int
	_, aerr = readStableFileSnapshot(file, snapshotHooks{
		afterBeforeStat: func(int) {
			calls++
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.WriteString(f, "x")
			_ = f.Close()
		},
	})
	if aerr == nil || aerr.code != errCodeFSReadFailed || calls != 2 {
		t.Fatalf("unstable read: calls=%d error=%+v", calls, aerr)
	}
}

// The meta=1 metadata GET (docs/log/44 §3.2) is contractually "the ordinary GET
// minus content": every field, the editability order, and the revision-only-
// when-editable rule must match the full response byte for byte.
func TestFSFileGetMetaOmitsContentOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	files := map[string][]byte{
		"note.md":   []byte("# title\nbody\n"),
		"crlf.txt":  []byte("a\r\nb\r"),
		"nul.bin":   {'a', 0, 'b'},
		"large.txt": bytes.Repeat([]byte("x"), maxEditorFileBytes+1),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
		responses := make([]map[string]any, 2)
		for i, query := range []string{"/fs/file?path=" + name, "/fs/file?path=" + name + "&meta=1"} {
			rec := httptest.NewRecorder()
			handleFSFile(rec, httptest.NewRequest(http.MethodGet, query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status=%d body=%s", query, rec.Code, rec.Body.String())
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &responses[i]); err != nil {
				t.Fatal(err)
			}
		}
		full, meta := responses[0], responses[1]
		if _, ok := meta["content"]; ok {
			t.Errorf("%s: meta=1 leaked content", name)
		}
		delete(full, "content")
		if !reflect.DeepEqual(full, meta) {
			t.Errorf("%s: meta response diverged from the full GET:\nfull=%#v\nmeta=%#v", name, full, meta)
		}
		if name == "note.md" {
			if meta["revision"] != fileRevision(content) || meta["editable"] != true {
				t.Errorf("note.md meta: %#v", meta)
			}
		} else if _, ok := meta["revision"]; ok {
			t.Errorf("%s: non-editable meta response leaked a revision", name)
		}
	}
}

func TestFSFileGetMetaReadOnlyRoot(t *testing.T) {
	root := t.TempDir()
	docs := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	t.Setenv("AGENT_DOCS_DIR", docs)
	path := filepath.Join(docs, "guide.md")
	if err := os.WriteFile(path, []byte("guide\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleFSFile(rec, httptest.NewRequest(http.MethodGet, "/fs/file?path="+url.QueryEscape(path)+"&meta=1", nil))
	var got map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got["editable"] != false || got["editabilityReason"] != "read_only_root" {
		t.Fatalf("read-only meta response: %#v", got)
	}
	for _, field := range []string{"content", "revision"} {
		if _, ok := got[field]; ok {
			t.Fatalf("read-only meta response leaked %s: %#v", field, got)
		}
	}
}

// Safety-boundary errors must not be rounded to a metadata answer: meta=1 goes
// through the same resolution as the full GET and returns the same code.
func TestFSFileGetMetaSharesErrorContract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AF_BROWSE_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ssh", "config"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path   string
		status int
		code   string
	}{
		{"link.txt", http.StatusBadRequest, errCodeFSSymlinkNotAllowed},
		{".ssh/config", http.StatusForbidden, errCodeFSDenied},
		{"/etc/passwd", http.StatusBadRequest, errCodeFSBadPath},
		{"missing.txt", http.StatusNotFound, errCodeFSNotFile},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		handleFSFile(rec, httptest.NewRequest(http.MethodGet, "/fs/file?path="+url.QueryEscape(tc.path)+"&meta=1", nil))
		if rec.Code != tc.status || responseErrorCode(t, rec) != tc.code {
			t.Errorf("meta GET %q: status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
}
