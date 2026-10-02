package main

// install_gcloud.go — `workspace-agent install-gcloud` (ADR 0107 decision 4).
//
// The Google Cloud SDK is not baked into the image: it unpacks to ~486 MB and a fraction of
// members use it, while every cold image pull would pay for it. It is installed on demand,
// one pinned version, into ~/.local/share/agent-fleet/google-cloud-sdk with the core and
// gke-gcloud-auth-plugin components, and linked into ~/.local/bin.
//
// The pin is the Dockerfile's GCLOUD_VERSION, recorded in versions.json as `gcloud` with the
// archive's sha256 for the build arch as `gcloud_sha256`. The archive is refused unless it
// matches that sum. The plugin is not in the archive: install.sh fetches it from Google's
// component repository, pinned to the same SDK version (CLOUDSDK_COMPONENT_MANAGER_
// FIXED_SDK_VERSION) and checked against the checksums in that version's snapshot.
//
// install.sh runs with a throwaway config root and with every inherited CLOUDSDK_*,
// GOOGLE_*, GCLOUD_* and GCE_METADATA_* variable removed: run as the member it would
// otherwise create ~/.config/gcloud, write a DEBUG log there and probe the metadata server,
// and the member's own gcloud directory is never ours to touch.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// gcloudArchiveBase is Google's versioned archive location. A var only so tests can point it
// elsewhere; never rewritten at runtime.
var gcloudArchiveBase = "https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/"

// gcloudFetch downloads url to dest. Tests replace it with a copy from a local archive, so
// no test ever downloads.
var gcloudFetch = func(url, dest string) error {
	return runCmd("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "--retry", "3",
		"--retry-delay", "2", "--retry-connrefused", "-o", dest, url)
}

// gcloudGOARCH mirrors runtime.GOARCH; tests override it to check the arm mapping.
var gcloudGOARCH = runtime.GOARCH

// gcloudLinkedBins are the SDK's bin/ entries linked into ~/.local/bin, and the ones an
// install must produce before it is placed.
var gcloudLinkedBins = []string{"gcloud", "gke-gcloud-auth-plugin"}

// gcloudSDKRoot is where the pinned SDK lives.
func gcloudSDKRoot() string { return filepath.Join(agentFleetShareDir(), "google-cloud-sdk") }

func gcloudInstallStaging() string {
	return filepath.Join(agentFleetShareDir(), "google-cloud-sdk-install")
}

// gcloudArchiveName maps the Go arch to Google's archive naming (amd64 → x86_64, arm64 → arm).
func gcloudArchiveName(ver string) (string, error) {
	switch gcloudGOARCH {
	case "amd64":
		return "google-cloud-cli-" + ver + "-linux-x86_64.tar.gz", nil
	case "arm64":
		return "google-cloud-cli-" + ver + "-linux-arm.tar.gz", nil
	default:
		return "", fmt.Errorf("unsupported arch %q", gcloudGOARCH)
	}
}

// gcloudSDKVersion reads the version an SDK tree declares, "" when the tree is missing or
// incomplete (an install killed before every linked binary was in place counts as absent).
func gcloudSDKVersion(root string) string {
	for _, b := range gcloudLinkedBins {
		if !fileExecutable(filepath.Join(root, "bin", b)) {
			return ""
		}
	}
	v, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(v))
}

// gcloudInstallMarker records, inside the SDK root, which pin, architecture and archive an
// install came from. VERSION alone cannot tell an amd64 tree from an arm64 one, and a home
// outlives a move between the two; the bundled Python and the plugin are native, so a tree
// of the right version for the other CPU links fine and then fails with "Exec format error".
const gcloudInstallMarker = ".af-install"

func gcloudInstallIdentity(ver, sum string) string {
	return "version=" + ver + " arch=" + gcloudGOARCH + " sha256=" + sum + "\n"
}

// gcloudInstalled reports whether root holds a complete install of exactly this pin for this
// architecture. A tree without the marker (installed some other way) does not count.
func gcloudInstalled(root, ver, sum string) bool {
	if gcloudSDKVersion(root) != ver {
		return false
	}
	b, err := os.ReadFile(filepath.Join(root, gcloudInstallMarker))
	return err == nil && string(b) == gcloudInstallIdentity(ver, sum)
}

func runInstallGCloud(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent install-gcloud   (installs the versions.json pin; takes no arguments)")
		os.Exit(2)
	}
	if err := installGCloud(); err != nil {
		fmt.Fprintf(os.Stderr, "[install-gcloud] %v\n", err)
		os.Exit(1)
	}
}

// installGCloud installs the pinned SDK, or only refreshes the links when that pin is
// already there for this architecture. Anything else is replaced: the pin is the version
// verified. Everything, the "already installed" path included, runs under the install lock,
// because the link refresh writes through one shared temporary name.
func installGCloud() error {
	pins := readBuildPins()
	ver, sum := pins["gcloud"], pins["gcloud_sha256"]
	if ver == "" || sum == "" {
		return fmt.Errorf("no gcloud / gcloud_sha256 pin in versions.json — this image pre-dates the Google Cloud SDK pin; rebuild the workspace image")
	}
	unlock, err := gcloudInstallLock()
	if err != nil {
		return err
	}
	defer unlock()
	root := gcloudSDKRoot()
	if gcloudInstalled(root, ver, sum) {
		fmt.Fprintf(os.Stderr, "[install-gcloud] Google Cloud SDK %s already installed at %s\n", ver, root)
		return linkGCloudBins(root)
	}
	if err := doInstallGCloud(ver, sum, root); err != nil {
		return err
	}
	return linkGCloudBins(root)
}

func doInstallGCloud(ver, sum, root string) error {
	name, err := gcloudArchiveName(ver)
	if err != nil {
		return err
	}
	staging := gcloudInstallStaging()
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(staging, "tree"), 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	archive := filepath.Join(staging, name)
	fmt.Fprintf(os.Stderr, "[install-gcloud] downloading %s ...\n", name)
	if err := gcloudFetch(gcloudArchiveBase+name, archive); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if err := verifySha256(archive, sum); err != nil {
		return fmt.Errorf("%s: %w — refusing to install", name, err)
	}
	if err := runCmd("tar", "-xzf", archive, "-C", filepath.Join(staging, "tree")); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	_ = os.Remove(archive)
	sdk := filepath.Join(staging, "tree", "google-cloud-sdk")
	if got, _ := os.ReadFile(filepath.Join(sdk, "VERSION")); strings.TrimSpace(string(got)) != ver {
		return fmt.Errorf("the archive declares version %q, not the pinned %s", strings.TrimSpace(string(got)), ver)
	}

	// --no-compile-python: install.sh otherwise byte-compiles every module of the SDK, bq and
	// gsutil included — 320 MiB and ~18,000 files of __pycache__ in a tree that is 511 MiB
	// without them, and about a minute of the install. Python writes the cache for the
	// modules a run actually imports instead, so only the first run of a command pays (ADR
	// 0107 note of 2026-10-02).
	fmt.Fprintf(os.Stderr, "[install-gcloud] installing core + gke-gcloud-auth-plugin ...\n")
	cmd := exec.Command(filepath.Join(sdk, "install.sh"),
		"--quiet", "--usage-reporting=false", "--path-update=false", "--command-completion=false",
		"--no-compile-python", "--additional-components", "gke-gcloud-auth-plugin")
	cmd.Dir = sdk
	cmd.Env = gcloudInstallEnv(os.Environ(), filepath.Join(staging, "config"), ver)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install.sh: %w", err)
	}
	if v := gcloudSDKVersion(sdk); v != ver {
		return fmt.Errorf("install.sh left an incomplete SDK (bin/gcloud and bin/gke-gcloud-auth-plugin are required)")
	}
	if err := os.WriteFile(filepath.Join(sdk, gcloudInstallMarker), []byte(gcloudInstallIdentity(ver, sum)), 0o644); err != nil {
		return err
	}

	// Swap: the old tree is moved aside before the new one takes its place, so a kill in
	// between leaves no tree rather than a mixed one, and the next run installs again.
	old := root + ".old"
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if _, err := os.Lstat(root); err == nil {
		if err := os.Rename(root, old); err != nil {
			return err
		}
	}
	if err := os.Rename(sdk, root); err != nil {
		return err
	}
	_ = os.RemoveAll(old)
	fmt.Fprintf(os.Stderr, "[install-gcloud] installed Google Cloud SDK %s at %s\n", ver, root)
	return nil
}

// gcloudInstallEnv is install.sh's environment: the caller's, minus everything that would
// steer gcloud at a config, an identity or a metadata server, plus a private config root
// and the component pin.
func gcloudInstallEnv(environ []string, configDir, ver string) []string {
	out := make([]string, 0, len(environ)+6)
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLOUDSDK_") || strings.HasPrefix(k, "GOOGLE_") ||
			strings.HasPrefix(k, "GCLOUD_") || strings.HasPrefix(k, "GCE_METADATA_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"CLOUDSDK_CONFIG="+configDir,
		"CLOUDSDK_CORE_DISABLE_FILE_LOGGING=true",
		"CLOUDSDK_CORE_CHECK_GCE_METADATA=false",
		"CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true",
		"CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK=true",
		"CLOUDSDK_COMPONENT_MANAGER_FIXED_SDK_VERSION="+ver,
	)
}

// linkGCloudBins points ~/.local/bin/{gcloud,gke-gcloud-auth-plugin} at the SDK. A regular
// file already there is the member's own and is left alone, with a warning.
func linkGCloudBins(root string) error {
	binDir := filepath.Join(homeDir(), ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	for _, b := range gcloudLinkedBins {
		link := filepath.Join(binDir, b)
		target := filepath.Join(root, "bin", b)
		if fi, err := os.Lstat(link); err == nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				fmt.Fprintf(os.Stderr, "[install-gcloud] WARN: %s is not a link; leaving it (the SDK's is %s)\n", link, target)
				continue
			}
			if cur, _ := os.Readlink(link); cur == target {
				continue
			}
		}
		tmp := link + ".af-new"
		_ = os.Remove(tmp)
		if err := os.Symlink(target, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, link); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return nil
}

// gcloudInstallLock serialises installs across processes: two at once would race on the
// staging directory and the swap.
func gcloudInstallLock() (func(), error) {
	if err := os.MkdirAll(agentFleetShareDir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(agentFleetShareDir(), ".google-cloud-sdk-install.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "[install-gcloud] another install is in progress; waiting ...")
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
