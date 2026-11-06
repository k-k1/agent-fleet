package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/jdkvendor"
)

// fakeCorretto creates <root>/<name> the way a Corretto tarball lays it out: a
// version.txt and no SEMANTIC_VERSION ("" = no version.txt).
func fakeCorretto(t *testing.T, root, name, version string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if version != "" {
		if err := os.WriteFile(filepath.Join(dir, "version.txt"), []byte(version+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInstalledJavaIDsAcrossVendors(t *testing.T) {
	sys, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	fakeJDK(t, sys, "temurin-21-jdk"+arch, "21.0.11+10")
	fakeCorretto(t, home, "corretto-21-jdk"+arch, "21.0.13.0.11")
	fakeCorretto(t, home, "corretto-8-jdk"+arch, "8.512.09.1")
	fakeCorretto(t, home, "corretto-17-jdk-"+otherArch(), "17.0.17.10.1")
	fakeJDK(t, home, "zulu-21-jdk"+arch, "21.0.11+10")

	want := []string{"corretto-8", "21", "corretto-21"}
	if got := installedJavaIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("installedJavaIDs = %v, want %v", got, want)
	}
	opts := javaOptions()
	i, j := slices.Index(opts, "21"), slices.Index(opts, "corretto-21")
	if i < 0 || j != i+1 || slices.Index(opts, "corretto-25") < 0 {
		t.Fatalf("javaOptions = %v, want Temurin 21 right before Corretto 21 and Corretto 25 offered", opts)
	}
	if got := installedJavaVersions(); got["corretto-21"] != "21.0.13.0.11" || got["21"] != "21.0.11+10" || got["corretto-8"] != "8.512.09.1" {
		t.Fatalf("installedJavaVersions = %v", got)
	}
}

// Same major, two vendors: each id finds its own tree, never the other's.
func TestJavaHomeForKeepsVendorsApart(t *testing.T) {
	sys, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	tem := fakeJDK(t, home, "temurin-21-jdk"+arch, "21.0.13+9")
	cor := fakeCorretto(t, home, "corretto-21-jdk"+arch, "21.0.13.0.11")
	if got := javaHomeFor("21"); got != tem {
		t.Fatalf("javaHomeFor(21) = %q, want Temurin %q", got, tem)
	}
	if got := javaHomeFor("corretto-21"); got != cor {
		t.Fatalf("javaHomeFor(corretto-21) = %q, want %q", got, cor)
	}
	if got := javaHomeFor("corretto-17"); got != "" {
		t.Fatalf("javaHomeFor(corretto-17) = %q, want \"\"", got)
	}
	// An older Corretto in /usr/lib/jvm, which outranks home on a tie, loses to the home
	// copy only by Corretto's own scheme: the two differ in the fifth field alone.
	fakeCorretto(t, sys, "corretto-21-jdk"+arch, "21.0.13.0.10")
	if got := javaHomeFor("corretto-21"); got != cor {
		t.Fatalf("javaHomeFor(corretto-21) = %q, want the newer home copy %q", got, cor)
	}
}

func TestFetchCorrettoLatestReadsRedirect(t *testing.T) {
	arch, err := adoptiumArch()
	if err != nil {
		t.Skip(err)
	}
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if strings.Contains(r.URL.Path, "-99-") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", "/downloads/resources/21.0.13.0.11/amazon-corretto-21.0.13.0.11-linux-"+arch+".tar.gz")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	prev := jdkvendor.CorrettoBaseURL
	jdkvendor.CorrettoBaseURL = srv.URL
	t.Cleanup(func() { jdkvendor.CorrettoBaseURL = prev })

	v, err := fetchJDKLatest("corretto-21")
	if err != nil || v != "21.0.13.0.11" {
		t.Fatalf("fetchJDKLatest = %q, %v", v, err)
	}
	if gotMethod != http.MethodHead || gotPath != "/downloads/latest/amazon-corretto-21-"+arch+"-linux-jdk.tar.gz" {
		t.Fatalf("asked %s %s", gotMethod, gotPath)
	}
	if _, err := fetchJDKLatest("corretto-99"); err == nil {
		t.Fatal("a 404 must be an error, not a version")
	}
}

// The case compareJDKVersions would get wrong: only Corretto's fifth field moved.
func TestJDKLatestCorrettoUpdate(t *testing.T) {
	_, home := jdkRoots(t)
	fakeCorretto(t, home, "corretto-21-jdk-"+runtime.GOARCH, "21.0.13.0.11")
	stubJDKLatest(t, "21.0.13.0.12", nil)
	code, out := jdkLatestReq(t, "corretto-21")
	if code != http.StatusOK || out["current"] != "21.0.13.0.11" || out["update_available"] != true || out["major"] != "corretto-21" {
		t.Fatalf("code %d body %v", code, out)
	}
}

func TestJDKUninstallPerVendor(t *testing.T) {
	_, home := jdkRoots(t)
	arch := "-" + runtime.GOARCH
	tem := fakeJDK(t, home, "temurin-21-jdk"+arch, "21.0.13+9")
	cor := fakeCorretto(t, home, "corretto-21-jdk"+arch, "21.0.13.0.11")
	selectToolchains(t, "corretto-21", "")

	got := listToolchainInstalls()
	byMajor := map[string]toolchainInstall{}
	for _, e := range got.Java {
		byMajor[e.Major] = e
	}
	if e := byMajor["corretto-21"]; !e.InUse || e.Removable || e.Reason != "selected" || e.Version != "21.0.13.0.11" {
		t.Fatalf("corretto-21: %+v", e)
	}
	if e := byMajor["21"]; e.InUse || !e.Removable {
		t.Fatalf("temurin 21: %+v", e)
	}

	if code, body := uninstallReq(t, handleJDKUninstall, "jdk", "corretto-21"); code != http.StatusConflict || !strings.Contains(body, "Corretto 21") {
		t.Fatalf("selected corretto: %d %s", code, body)
	}
	if code, body := uninstallReq(t, handleJDKUninstall, "jdk", "21"); code != http.StatusOK {
		t.Fatalf("temurin 21 while corretto-21 is selected: %d %s", code, body)
	}
	if exists(tem) || !exists(cor) {
		t.Fatalf("after removing Temurin 21: temurin %v corretto %v", exists(tem), exists(cor))
	}
}

func TestToolchainsPutAcceptsJDKIDs(t *testing.T) {
	jdkRoots(t)
	put := func(java string) int {
		w := httptest.NewRecorder()
		handleToolchainsPut(w, httptest.NewRequest(http.MethodPut, "/env/toolchains", strings.NewReader(`{"java":"`+java+`"}`)))
		return w.Code
	}
	for _, ok := range []string{"corretto-21", "21", ""} {
		if code := put(ok); code != http.StatusOK {
			t.Errorf("java %q: want 200 got %d", ok, code)
		}
	}
	if readToolchains().Java != "" {
		t.Fatalf("last write lost: %q", readToolchains().Java)
	}
	for _, bad := range []string{"corretto-x", "corretto-21;id", "zulu-21"} {
		if code := put(bad); code != http.StatusBadRequest {
			t.Errorf("java %q: want 400 got %d", bad, code)
		}
	}
}
