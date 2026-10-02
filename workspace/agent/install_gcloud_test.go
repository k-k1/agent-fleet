package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGCloudArchive writes a tar.gz laid out like Google's archive: google-cloud-sdk/ with a
// VERSION file and an install.sh that records its arguments and environment and creates
// the two binaries the real one leaves behind (the plugin is what --additional-components
// adds). Returns the archive path and its sha256.
func fakeGCloudArchive(t *testing.T, dir, ver string) (string, string) {
	t.Helper()
	path := filepath.Join(dir, "google-cloud-cli-"+ver+"-fake.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	add := func(name, body string, mode int64) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("google-cloud-sdk/VERSION", ver+"\n", 0o644)
	add("google-cloud-sdk/bin/gcloud", "#!/bin/sh\necho gcloud\n", 0o755)
	add("google-cloud-sdk/install.sh", `#!/bin/sh
here="$(cd "$(dirname "$0")" && pwd)"
echo "$@" > "$here/install.args"
env | sort > "$here/install.env"
printf '#!/bin/sh\necho plugin\n' > "$here/bin/gke-gcloud-auth-plugin"
chmod 755 "$here/bin/gke-gcloud-auth-plugin"
`, 0o755)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	sum := sha256.Sum256(b)
	return path, hex.EncodeToString(sum[:])
}

type gcloudInstallEnvT struct {
	home    string
	fetched []string
	archive string // what the fake fetch serves
}

// newGCloudInstallTest isolates HOME, the pin file and the download: the fetch copies the
// local archive and records the URL it was asked for.
func newGCloudInstallTest(t *testing.T) *gcloudInstallEnvT {
	t.Helper()
	e := &gcloudInstallEnvT{home: t.TempDir()}
	t.Setenv("HOME", e.home)
	oldPins, oldFetch, oldArch := buildPinsPath, gcloudFetch, gcloudGOARCH
	buildPinsPath = filepath.Join(t.TempDir(), "versions.json")
	gcloudGOARCH = "amd64"
	gcloudFetch = func(url, dest string) error {
		e.fetched = append(e.fetched, url)
		b, err := os.ReadFile(e.archive)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, b, 0o644)
	}
	t.Cleanup(func() { buildPinsPath, gcloudFetch, gcloudGOARCH = oldPins, oldFetch, oldArch })
	return e
}

func (e *gcloudInstallEnvT) pin(t *testing.T, ver, sum string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"gcloud": ver, "gcloud_sha256": sum})
	if err := os.WriteFile(buildPinsPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// An archive whose sha256 is not the pinned one is refused before anything is unpacked or
// placed: no SDK tree, no link, no staging left behind.
func TestInstallGCloudRefusesAWrongSha256(t *testing.T) {
	e := newGCloudInstallTest(t)
	e.archive, _ = fakeGCloudArchive(t, t.TempDir(), "587.0.0")
	e.pin(t, "587.0.0", strings.Repeat("0", 64))

	err := installGCloud()
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("installGCloud with a wrong sum = %v, want a sha256 mismatch", err)
	}
	for _, p := range []string{gcloudSDKRoot(), gcloudInstallStaging(), filepath.Join(e.home, ".local", "bin", "gcloud")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s exists after a refused install", p)
		}
	}
}

func TestInstallGCloudInstallsPinnedAndIsIdempotent(t *testing.T) {
	e := newGCloudInstallTest(t)
	var sum string
	e.archive, sum = fakeGCloudArchive(t, t.TempDir(), "587.0.0")
	e.pin(t, "587.0.0", sum)
	t.Setenv("CLOUDSDK_AUTH_ACCESS_TOKEN_FILE", "/inherited/token")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/inherited/adc.json")
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(e.home, ".config", "gcloud"))

	if err := installGCloud(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(e.fetched) != 1 || e.fetched[0] != gcloudArchiveBase+"google-cloud-cli-587.0.0-linux-x86_64.tar.gz" {
		t.Fatalf("fetched %v", e.fetched)
	}
	root := gcloudSDKRoot()
	if root != filepath.Join(e.home, ".local", "share", "agent-fleet", "google-cloud-sdk") || gcloudSDKVersion(root) != "587.0.0" {
		t.Fatalf("installed tree at %s: version %q", root, gcloudSDKVersion(root))
	}
	args, _ := os.ReadFile(filepath.Join(root, "install.args"))
	if !strings.Contains(string(args), "--additional-components gke-gcloud-auth-plugin") || !strings.Contains(string(args), "--path-update=false") {
		t.Errorf("install.sh args = %q", args)
	}
	env, _ := os.ReadFile(filepath.Join(root, "install.env"))
	for _, want := range []string{"CLOUDSDK_COMPONENT_MANAGER_FIXED_SDK_VERSION=587.0.0", "CLOUDSDK_CORE_DISABLE_FILE_LOGGING=true", "CLOUDSDK_CORE_CHECK_GCE_METADATA=false"} {
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("install.sh env lacks %s", want)
		}
	}
	for _, leak := range []string{"CLOUDSDK_AUTH_ACCESS_TOKEN_FILE=", "GOOGLE_APPLICATION_CREDENTIALS=", "CLOUDSDK_CONFIG=" + filepath.Join(e.home, ".config")} {
		if strings.Contains(string(env), leak) {
			t.Errorf("install.sh env carries %s", leak)
		}
	}
	for _, b := range gcloudLinkedBins {
		link := filepath.Join(e.home, ".local", "bin", b)
		if dst, err := os.Readlink(link); err != nil || dst != filepath.Join(root, "bin", b) {
			t.Errorf("%s -> %q (%v)", link, dst, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.home, ".config", "gcloud")); err == nil {
		t.Error("the install created the member's ~/.config/gcloud")
	}
	if _, err := os.Stat(gcloudInstallStaging()); err == nil {
		t.Error("staging left behind")
	}

	// Second run: nothing downloaded, a removed link is put back.
	_ = os.Remove(filepath.Join(e.home, ".local", "bin", "gcloud"))
	if err := installGCloud(); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(e.fetched) != 1 {
		t.Errorf("an installed pin was downloaded again: %v", e.fetched)
	}
	if _, err := os.Readlink(filepath.Join(e.home, ".local", "bin", "gcloud")); err != nil {
		t.Errorf("link not restored: %v", err)
	}

	// A pin bump replaces the tree.
	e.archive, sum = fakeGCloudArchive(t, t.TempDir(), "588.0.0")
	e.pin(t, "588.0.0", sum)
	if err := installGCloud(); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if len(e.fetched) != 2 || gcloudSDKVersion(root) != "588.0.0" {
		t.Fatalf("after a pin bump: fetched %v, version %q", e.fetched, gcloudSDKVersion(root))
	}
	if _, err := os.Stat(root + ".old"); err == nil {
		t.Error("the replaced tree was left behind")
	}
}

// The pin decides the archive, and the archive must say it is that version: a sum that
// matches a mislabelled archive still does not install it.
func TestInstallGCloudRefusesAnArchiveOfAnotherVersion(t *testing.T) {
	e := newGCloudInstallTest(t)
	var sum string
	e.archive, sum = fakeGCloudArchive(t, t.TempDir(), "586.0.0")
	e.pin(t, "587.0.0", sum)
	if err := installGCloud(); err == nil || !strings.Contains(err.Error(), "not the pinned") {
		t.Fatalf("installGCloud = %v", err)
	}
	if gcloudSDKVersion(gcloudSDKRoot()) != "" {
		t.Error("a mislabelled archive was installed")
	}
}

func TestInstallGCloudNeedsBothPins(t *testing.T) {
	newGCloudInstallTest(t)
	b, _ := json.Marshal(map[string]string{"gcloud": "587.0.0"})
	_ = os.WriteFile(buildPinsPath, b, 0o644)
	if err := installGCloud(); err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatalf("installGCloud without a sum = %v", err)
	}
}

// A regular file at ~/.local/bin/gcloud is the member's own and is left as it is.
func TestInstallGCloudLeavesAMembersOwnBinary(t *testing.T) {
	e := newGCloudInstallTest(t)
	var sum string
	e.archive, sum = fakeGCloudArchive(t, t.TempDir(), "587.0.0")
	e.pin(t, "587.0.0", sum)
	own := filepath.Join(e.home, ".local", "bin", "gcloud")
	_ = os.MkdirAll(filepath.Dir(own), 0o755)
	_ = os.WriteFile(own, []byte("#!/bin/sh\necho mine\n"), 0o755)
	if err := installGCloud(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if b, _ := os.ReadFile(own); string(b) != "#!/bin/sh\necho mine\n" {
		t.Errorf("the member's gcloud was replaced: %q", b)
	}
}

func TestGCloudArchiveNameMapsArch(t *testing.T) {
	old := gcloudGOARCH
	t.Cleanup(func() { gcloudGOARCH = old })
	for arch, want := range map[string]string{
		"amd64": "google-cloud-cli-587.0.0-linux-x86_64.tar.gz",
		"arm64": "google-cloud-cli-587.0.0-linux-arm.tar.gz",
	} {
		gcloudGOARCH = arch
		if got, err := gcloudArchiveName("587.0.0"); err != nil || got != want {
			t.Errorf("%s: %q, %v", arch, got, err)
		}
	}
	gcloudGOARCH = "386"
	if _, err := gcloudArchiveName("587.0.0"); err == nil {
		t.Error("386 accepted")
	}
}

// The Toolchain row reads the SDK's VERSION file through the link and never runs gcloud,
// which would create the member's ~/.config/gcloud.
func TestGCloudToolRowReadsVersionFileWithoutRunning(t *testing.T) {
	home := t.TempDir()
	sdk := filepath.Join(home, "sdk")
	_ = os.MkdirAll(filepath.Join(sdk, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(sdk, "VERSION"), []byte("587.0.0\n"), 0o644)
	marker := filepath.Join(home, "ran")
	_ = os.WriteFile(filepath.Join(sdk, "bin", "gcloud"), []byte("#!/bin/sh\ntouch "+marker+"\necho 'Google Cloud SDK 1.0.0'\n"), 0o755)
	link := filepath.Join(home, "gcloud")
	_ = os.Symlink(filepath.Join(sdk, "bin", "gcloud"), link)

	var spec toolSpec
	for _, s := range toolSpecs {
		if s.Name == "gcloud" {
			spec = s
		}
	}
	if spec.Pin != "gcloud" || spec.Cmd != "gcloud" {
		t.Fatalf("toolSpecs gcloud row = %+v", spec)
	}
	b := probeTool(context.Background(), spec, link, home)
	if b == nil || b.Version != "587.0.0" || b.Path != link {
		t.Fatalf("probe = %+v", b)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the probe ran gcloud")
	}
}

// Two runs at once over an installed SDK whose links are missing must both succeed: the
// link refresh shares one temporary name, so it has to happen under the install lock.
func TestInstallGCloudConcurrentRunsRelinkSafely(t *testing.T) {
	e := newGCloudInstallTest(t)
	var sum string
	e.archive, sum = fakeGCloudArchive(t, t.TempDir(), "587.0.0")
	e.pin(t, "587.0.0", sum)
	if err := installGCloud(); err != nil {
		t.Fatalf("install: %v", err)
	}
	for round := 0; round < 20; round++ {
		for _, b := range gcloudLinkedBins {
			_ = os.Remove(filepath.Join(e.home, ".local", "bin", b))
		}
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			go func() { errs <- installGCloud() }()
		}
		for i := 0; i < 8; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: concurrent install-gcloud: %v", round, err)
			}
		}
	}
	if len(e.fetched) != 1 {
		t.Errorf("relinking downloaded again: %v", e.fetched)
	}
}

// A home carried from one architecture to another holds an SDK of the right version for the
// wrong CPU (bundled Python and the plugin are native). It is reinstalled, and so is a tree
// that records no install identity at all.
func TestInstallGCloudReinstallsForAnotherArchOrAnUnmarkedTree(t *testing.T) {
	e := newGCloudInstallTest(t)
	var sum string
	e.archive, sum = fakeGCloudArchive(t, t.TempDir(), "587.0.0")
	e.pin(t, "587.0.0", sum)
	if err := installGCloud(); err != nil {
		t.Fatalf("install: %v", err)
	}
	gcloudGOARCH = "arm64"
	if err := installGCloud(); err != nil {
		t.Fatalf("install on arm64: %v", err)
	}
	if len(e.fetched) != 2 || !strings.HasSuffix(e.fetched[1], "-linux-arm.tar.gz") {
		t.Fatalf("an amd64 SDK was kept on arm64: fetched %v", e.fetched)
	}
	if err := installGCloud(); err != nil || len(e.fetched) != 2 {
		t.Fatalf("control: same arch again: %v, fetched %v", err, e.fetched)
	}
	_ = os.Remove(filepath.Join(gcloudSDKRoot(), gcloudInstallMarker))
	if err := installGCloud(); err != nil || len(e.fetched) != 3 {
		t.Fatalf("an unmarked tree was kept: %v, fetched %v", err, e.fetched)
	}
}
