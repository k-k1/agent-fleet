package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func jdkInstallReq(t *testing.T, method, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var r *http.Request
	if method == http.MethodGet {
		r = httptest.NewRequest(method, "/env/jdk-install", nil)
	} else {
		r = httptest.NewRequest(method, "/env/jdk-install", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	handleJDKInstall(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// The major reaches an Adoptium URL and a directory name, so anything that is not one
// of the offered numeric majors must be refused before a download starts.
func TestJDKInstallRejectsBadMajor(t *testing.T) {
	for _, body := range []string{`{"major":"21; rm -rf /"}`, `{"major":"../../etc"}`, `{"major":""}`, `{"major":"latest"}`} {
		w, _ := jdkInstallReq(t, http.MethodPost, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: want 400 got %d (%s)", body, w.Code, w.Body.String())
		}
	}
	// Numeric but not offered by this workspace (javaOptions is installed ∪ installable).
	if w, _ := jdkInstallReq(t, http.MethodPost, `{"major":"999"}`); w.Code != http.StatusBadRequest {
		t.Errorf("unoffered major: want 400 got %d", w.Code)
	}
}

// GET reports the job state AND what is on disk, so one round trip tells the Console
// whether the button is still needed.
func TestJDKInstallStatusShape(t *testing.T) {
	w, out := jdkInstallReq(t, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if out["state"] != "idle" {
		t.Errorf("fresh state: want idle got %v", out["state"])
	}
	for _, k := range []string{"java_installed", "java_available"} {
		if _, ok := out[k]; !ok {
			t.Errorf("status must carry %s", k)
		}
	}
}

// A second request while one download runs reports the running job instead of starting
// a competing one into the same home JVM dir.
func TestJDKInstallSingleFlight(t *testing.T) {
	jdkInstaller.mu.Lock()
	jdkInstaller.state, jdkInstaller.major, jdkInstaller.err = "installing", "21", ""
	jdkInstaller.mu.Unlock()
	t.Cleanup(func() {
		jdkInstaller.mu.Lock()
		jdkInstaller.state, jdkInstaller.major, jdkInstaller.err = "", "", ""
		jdkInstaller.mu.Unlock()
	})

	w, out := jdkInstallReq(t, http.MethodPost, `{"major":"17"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if out["state"] != "installing" || out["major"] != "21" {
		t.Fatalf("want the RUNNING job reported (installing/21), got %v/%v", out["state"], out["major"])
	}
}
