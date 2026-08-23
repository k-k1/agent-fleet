package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// JDK provisioning (docs/09-deploy portability). JDKs come from two sources,
// searched in this priority order:
//
//  1. /usr/lib/jvm — provided by the deployment: baked into the image, or (the
//     local Docker default) bind-mounted read-only from the host's shared JDK dir
//     (WS_JVM_DIR). On ECS nothing is mounted here, so it is empty.
//  2. ~/.local/share/agent-fleet/jvm — the per-user home volume. `install-jdk`
//     downloads Temurin here; it persists across restarts (local volume / ECS EFS)
//     and is the ONLY JDK source on ECS. Named temurin-<major>-jdk-<goarch> to
//     mirror the /usr/lib/jvm layout so one discovery path finds both.
//
// This keeps the "JDK at /usr/lib/jvm" story true where a deployment provides it,
// while giving every runtime (incl. ECS) a common, writable place to add JDKs on
// demand — consistent with the Console's JAVA_HOME (toolchain) selection.

// jvmHomeRoot is the per-user JDK dir on the home volume.
func jvmHomeRoot() string {
	return filepath.Join(homeDir(), ".local", "share", "agent-fleet", "jvm")
}

// jvmSearchDirs lists JDK roots in priority order (deployment-provided first, then
// the home volume that install-jdk populates).
func jvmSearchDirs() []string {
	return []string{"/usr/lib/jvm", jvmHomeRoot()}
}

// installableJava are the Temurin majors install-jdk can fetch on demand (Adoptium
// GA). Offered in the Console picker in addition to whatever is already on disk, so
// a member can select a version that isn't present yet — the entrypoint downloads
// it into the home volume on the next launch (the only path on ECS).
var installableJava = []string{"8", "11", "17", "21", "25"}

var javaMajorRe = regexp.MustCompile(`^temurin-(\d+)-jdk`)

// jdkArchSuffixes are the architecture tokens a JDK directory can be named with.
// Both sources use the same two: the Debian packages under /usr/lib/jvm are
// temurin-<major>-jdk-<dpkg arch> and installJDK writes
// temurin-<major>-jdk-<runtime.GOARCH> — amd64 / arm64 in both vocabularies.
var jdkArchSuffixes = []string{"amd64", "arm64"}

// foreignArchJDK reports whether a JDK directory name carries an architecture
// suffix that is not this container's.
//
// ⚠️ This exists because the home volume OUTLIVES the architecture it was filled
// on. On the ecs-ec2 runtime a member's `~` is an EBS volume that follows them,
// and docs/70 makes the box they land on a per-member setting — so a home filled
// on x86 can be attached to an arm64 slot, and then
// ~/.local/share/agent-fleet/jvm holds temurin-21-jdk-amd64. Picking it would set
// JAVA_HOME to a tree whose bin/java cannot exec, which surfaces as a gradle
// build failing on a machine where `java -version` also fails — a long way from
// the cause.
func foreignArchJDK(name string) bool {
	for _, a := range jdkArchSuffixes {
		if a != runtime.GOARCH && strings.HasSuffix(name, "-"+a) {
			return true
		}
	}
	return false
}

// installedJavaMajors returns the sorted-ascending unique Temurin majors actually
// present across the search dirs AND runnable here — a wrong-architecture tree is
// not "installed" for our purposes, because reporting it as present is what hides
// the Console's Install button for a JDK that cannot run.
func installedJavaMajors() []string {
	seen := map[string]bool{}
	for _, dir := range jvmSearchDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if foreignArchJDK(e.Name()) {
				continue
			}
			if m := javaMajorRe.FindStringSubmatch(e.Name()); m != nil {
				seen[m[1]] = true
			}
		}
	}
	return sortMajors(seen)
}

// javaOptions merges installed majors with the installable set (deduped, sorted) —
// the list the Console offers for selection.
func javaOptions() []string {
	seen := map[string]bool{}
	for _, v := range installedJavaMajors() {
		seen[v] = true
	}
	for _, v := range installableJava {
		seen[v] = true
	}
	return sortMajors(seen)
}

func sortMajors(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i])
		b, _ := strconv.Atoi(out[j])
		return a < b
	})
	return out
}

// javaHomeFor resolves a major to a concrete JAVA_HOME across the search dirs
// (priority order), or "" when that major is not installed anywhere FOR THIS
// ARCHITECTURE.
//
// ⚠️ Do not go back to "glob, sort, take the first". Both sources name their
// directories temurin-<major>-jdk-<arch>, and "amd64" sorts before "arm64", so on
// an arm64 workspace holding a home that was filled on x86 the sorted-first entry
// is always the unusable one (docs/70 §70.5.1).
func javaHomeFor(major string) string {
	for _, dir := range jvmSearchDirs() {
		m, _ := filepath.Glob(filepath.Join(dir, "temurin-"+major+"-jdk*"))
		sort.Strings(m)
		if hit := pickArchJDK(m); hit != "" {
			return hit
		}
	}
	return ""
}

// pickArchJDK chooses this architecture's JDK out of one directory's matches:
// an exact -<GOARCH> suffix wins; a name with no architecture suffix at all is
// accepted as a fallback (a hand-placed or differently-packaged tree); a name
// carrying SOMEONE ELSE'S architecture is never returned.
func pickArchJDK(matches []string) string {
	var neutral string
	for _, p := range matches {
		name := filepath.Base(p)
		if strings.HasSuffix(name, "-"+runtime.GOARCH) {
			return p
		}
		if foreignArchJDK(name) {
			continue
		}
		if neutral == "" {
			neutral = p
		}
	}
	return neutral
}

// adoptiumArch maps the container's GOARCH to the Adoptium API arch token.
func adoptiumArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("unsupported arch %q", runtime.GOARCH)
	}
}

var majorOnlyRe = regexp.MustCompile(`^\d+$`)

// runInstallJDK is the `workspace-agent install-jdk <major>` subcommand — a thin CLI
// face over installJDK (which the Console's one-button install shares, jdk_install_http.go).
func runInstallJDK(args []string) {
	if len(args) < 1 || !majorOnlyRe.MatchString(args[0]) {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent install-jdk <major>   (e.g. 21)")
		os.Exit(2)
	}
	if _, err := installJDK(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "[install-jdk]", err)
		os.Exit(1)
	}
}

// installJDK downloads the latest GA Temurin JDK for the given major into the
// per-user home dir as temurin-<major>-jdk-<goarch>, replacing any existing dir for
// that major, and returns where it landed. Keyed by major only, so re-running just
// updates that major to the latest patch — multiple patches of the same major never
// accumulate. Progress goes to stderr, which is the agent log for the HTTP caller.
func installJDK(major string) (string, error) {
	if !majorOnlyRe.MatchString(major) {
		return "", fmt.Errorf("invalid major %q", major)
	}
	aarch, err := adoptiumArch()
	if err != nil {
		return "", err
	}
	root := jvmHomeRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	// Download to a temp tarball, extract into a staging dir, then atomically swap
	// it into place — a failed/half download never corrupts an existing JDK.
	tgz, err := os.CreateTemp(root, ".jdk-*.tgz")
	if err != nil {
		return "", err
	}
	tgzPath := tgz.Name()
	tgz.Close()
	defer os.Remove(tgzPath)
	staging, err := os.MkdirTemp(root, ".install-"+major+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)

	url := fmt.Sprintf("https://api.adoptium.net/v3/binary/latest/%s/ga/linux/%s/jdk/hotspot/normal/eclipse", major, aarch)
	fmt.Fprintf(os.Stderr, "[install-jdk] downloading Temurin %s (%s) ...\n", major, aarch)
	// curl follows the API redirect to the release tarball（--proto-redir =https で
	// http への降格リダイレクトは拒否）. tar --strip-components=1
	// drops the jdk-<ver>/ top-level dir so bin/, lib/ land directly under staging.
	if err := runCmd("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "-o", tgzPath, url); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	if err := runCmd("tar", "-xzf", tgzPath, "--strip-components=1", "-C", staging); err != nil {
		return "", fmt.Errorf("extract failed: %w", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "bin", "java")); err != nil {
		return "", fmt.Errorf("extracted tree has no bin/java")
	}

	dest := filepath.Join(root, "temurin-"+major+"-jdk-"+runtime.GOARCH)
	if err := os.RemoveAll(dest); err != nil {
		return "", err
	}
	if err := os.Rename(staging, dest); err != nil {
		return "", err
	}
	out, _ := exec.Command(filepath.Join(dest, "bin", "java"), "-version").CombinedOutput()
	fmt.Fprintf(os.Stderr, "[install-jdk] installed at %s\n%s", dest, string(out))
	return dest, nil
}

func runCmd(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stderr = os.Stderr
	return c.Run()
}
