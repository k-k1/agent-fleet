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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/jdkvendor"
)

// JDK provisioning (docs/09-deploy portability). JDKs come from two sources,
// listed here in priority order (javaHomeFor leaves that order only for a newer patch):
//
//  1. /usr/lib/jvm — provided by the deployment: baked into the image, or (the
//     local Docker default) bind-mounted read-only from the host's shared JDK dir
//     (WS_JVM_DIR). On ECS nothing is mounted here, so it is empty.
//  2. ~/.local/share/agent-fleet/jvm — the per-user home volume. `install-jdk`
//     downloads Temurin or Corretto here; it persists across restarts (local volume /
//     ECS EFS) and is the ONLY JDK source on ECS. Named <vendor>-<major>-jdk-<goarch>
//     to mirror the /usr/lib/jvm layout so one discovery path finds both
//     (internal/jdkvendor).
//
// This keeps the "JDK at /usr/lib/jvm" story true where a deployment provides it,
// while giving every runtime (incl. ECS) a common, writable place to add JDKs on
// demand — consistent with the Console's JAVA_HOME (toolchain) selection.

// jvmSystemRoot is the deployment-provided JDK dir; a var so tests can point it at a
// temp tree.
var jvmSystemRoot = "/usr/lib/jvm"

// jvmHomeRoot is the per-user JDK dir on the home volume.
func jvmHomeRoot() string {
	return filepath.Join(homeDir(), ".local", "share", "agent-fleet", "jvm")
}

// jvmSearchDirs lists JDK roots in priority order (deployment-provided first, then
// the home volume that install-jdk populates).
func jvmSearchDirs() []string {
	return []string{jvmSystemRoot, jvmHomeRoot()}
}

// installableJava are the JDK ids install-jdk can fetch on demand: every vendor's
// jdkvendor.InstallableMajors. Offered in the Console picker in addition to whatever is
// already on disk, so a member can select a version that isn't present yet — the
// entrypoint downloads it into the home volume on the next launch (the only path on
// ECS).
var installableJava = func() []string {
	var out []string
	for _, v := range jdkvendor.All {
		for _, m := range jdkvendor.InstallableMajors {
			out = append(out, jdkvendor.ID(v, m))
		}
	}
	return out
}()

// jdkArchSuffixes are the architecture tokens a JDK directory can be named with.
// Both sources use the same two: the Debian packages under /usr/lib/jvm are
// temurin-<major>-jdk-<dpkg arch> and installJDK writes
// <vendor>-<major>-jdk-<runtime.GOARCH> — amd64 / arm64 in both vocabularies.
var jdkArchSuffixes = []string{"amd64", "arm64"}

// foreignArchJDK reports whether a JDK directory name carries an architecture
// suffix that is not this container's.
//
// This exists because the home volume OUTLIVES the architecture it was filled
// on. On the ecs-ec2 runtime a member's `~` is an EBS volume that follows them,
// and docs/log/70 makes the box they land on a per-member setting — so a home filled
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

// installedJavaIDs returns the sorted unique JDK ids (internal/jdkvendor) actually
// present across the search dirs AND runnable here — a wrong-architecture tree is
// not "installed" for our purposes, because reporting it as present is what hides
// the Console's Install button for a JDK that cannot run.
func installedJavaIDs() []string {
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
			if id := jdkvendor.IDOfTree(e.Name()); id != "" {
				seen[id] = true
			}
		}
	}
	return sortJDKIDs(seen)
}

// javaOptions merges installed ids with the installable set (deduped, sorted) —
// the list the Console offers for selection.
func javaOptions() []string {
	seen := map[string]bool{}
	for _, v := range installedJavaIDs() {
		seen[v] = true
	}
	for _, v := range installableJava {
		seen[v] = true
	}
	return sortJDKIDs(seen)
}

// sortJDKIDs orders ids by major, then by vendor in jdkvendor.All order, so the picker
// lists "21" (Temurin) right before "corretto-21".
func sortJDKIDs(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		vi, mi, _ := jdkvendor.Parse(out[i])
		vj, mj, _ := jdkvendor.Parse(out[j])
		a, _ := strconv.Atoi(mi)
		b, _ := strconv.Atoi(mj)
		if a != b {
			return a < b
		}
		return jdkvendor.Rank(vi) < jdkvendor.Rank(vj)
	})
	return out
}

// javaHomeFor resolves a JDK id to a concrete JAVA_HOME across the search dirs, or ""
// when it is not installed anywhere FOR THIS ARCHITECTURE (or is not an id).
//
// Priority order decides, except that a candidate whose recorded version is newer
// than the current pick replaces it. Without that, `install-jdk 21` lands the latest
// patch in the home volume and JAVA_HOME stays on an older /usr/lib/jvm tree forever.
// Only a provable upgrade moves the pick: an unreadable version on either side keeps
// the priority order, so a hand-placed tree never displaces a known one by accident.
//
// Do not go back to "glob, sort, take the first". Both sources name their
// directories <vendor>-<major>-jdk-<arch>, and "amd64" sorts before "arm64", so on
// an arm64 workspace holding a home that was filled on x86 the sorted-first entry
// is always the unusable one (docs/log/70 §70.5.1).
//
// The entrypoint resolves its own JAVA_HOME through `workspace-agent java-home`, so
// this is the one place the rule lives.
func javaHomeFor(id string) string {
	vendor, major, ok := jdkvendor.Parse(id)
	if !ok {
		return ""
	}
	best, bestVer := "", ""
	for _, dir := range jvmSearchDirs() {
		m, _ := filepath.Glob(jdkvendor.TreeGlob(dir, vendor, major))
		sort.Strings(m)
		hit := pickArchJDK(m)
		if hit == "" {
			continue
		}
		v := jdkOpsOf(vendor).version(hit)
		if best == "" || (bestVer != "" && v != "" && jdkOpsOf(vendor).compare(v, bestVer) > 0) {
			best, bestVer = hit, v
		}
	}
	return best
}

// jdkOps is the vendor-specific behaviour the resolver and installer need. Temurin's
// half (release-file SEMANTIC_VERSION, Adoptium's assets API) stays in package main
// because the resolver was built on it; Corretto's lives in internal/jdkvendor.
type jdkOps struct {
	url     func(major, arch string) string
	latest  func(major string) (string, error)
	version func(home string) string
	compare func(a, b string) int
}

func jdkOpsOf(v *jdkvendor.Vendor) jdkOps {
	if v == jdkvendor.Corretto {
		return jdkOps{
			url: jdkvendor.CorrettoURL,
			latest: func(major string) (string, error) {
				arch, err := adoptiumArch()
				if err != nil {
					return "", err
				}
				return jdkvendor.CorrettoLatest(major, arch)
			},
			version: jdkvendor.CorrettoTreeVersion,
			compare: jdkvendor.CompareCorretto,
		}
	}
	return jdkOps{url: jdkvendor.TemurinURL, latest: fetchTemurinLatest, version: semanticJDKVersion, compare: compareJDKVersions}
}

// jdkVersion returns the version a JDK tree records, in its vendor's own scheme, or ""
// when it records none. The vendor comes from the tree's directory name; a name that
// belongs to no vendor is read as a Temurin release file.
func jdkVersion(home string) string {
	if v, _, ok := jdkvendor.Parse(jdkvendor.IDOfTree(filepath.Base(home))); ok {
		return jdkOpsOf(v).version(home)
	}
	return semanticJDKVersion(home)
}

// semanticJDKVersion returns the SEMANTIC_VERSION a Temurin tree records in its release
// file ("21.0.11+10", "8.0.492+9"), or "" when the file or the key is missing.
// SEMANTIC_VERSION rather than JAVA_VERSION because the latter is "1.8.0_492" on 8 and
// carries no build number on any major.
func semanticJDKVersion(home string) string {
	b, err := os.ReadFile(filepath.Join(home, "release"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "SEMANTIC_VERSION="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// compareJDKVersions orders two JDK semantic versions numerically: the dotted part
// first, then the build after "+". Text after the build number is ignored, so
// Adoptium's "21.0.13+9.0.LTS" compares equal to a release file's "21.0.13+9".
// A missing or non-numeric field counts as 0.
func compareJDKVersions(a, b string) int {
	pa, pb := jdkVersionFields(a), jdkVersionFields(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// jdkVersionFields splits "21.0.11+10" into [21 0 11 0 10]: the dotted part padded to
// four fields (17.0.4.1 exists) so the build number always lands in the same slot.
func jdkVersionFields(v string) []int {
	ver, build, _ := strings.Cut(v, "+")
	out := make([]int, 4, 5)
	for i, f := range strings.Split(ver, ".") {
		if i >= len(out) {
			break
		}
		out[i], _ = strconv.Atoi(f)
	}
	digits := build
	if i := strings.IndexFunc(build, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		digits = build[:i]
	}
	n, _ := strconv.Atoi(digits)
	return append(out, n)
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

// adoptiumArch maps the container's GOARCH to the Adoptium API arch token, which is
// also the token corretto.aws file names use.
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

// runInstallJDK is the `workspace-agent install-jdk <id>` subcommand — a thin CLI
// face over installJDK (which the Console's one-button install shares, jdk_install_http.go).
func runInstallJDK(args []string) {
	if _, _, ok := jdkvendor.Parse(firstArg(args)); !ok {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent install-jdk <major>|corretto-<major>   (e.g. 21, corretto-21)")
		os.Exit(2)
	}
	if _, err := installJDK(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "[install-jdk]", err)
		os.Exit(1)
	}
}

// installJDK downloads the latest GA JDK of an id's vendor and major into the per-user
// home dir as <vendor>-<major>-jdk-<goarch>, replacing any existing dir for that id,
// and returns where it landed. Keyed by vendor and major only, so re-running just
// updates it to the latest patch — multiple patches of the same major never
// accumulate, while Temurin 21 and Corretto 21 live side by side. Progress goes to
// stderr, which is the agent log for the HTTP caller.
func installJDK(id string) (string, error) {
	vendor, major, ok := jdkvendor.Parse(id)
	if !ok {
		return "", fmt.Errorf("invalid JDK %q", id)
	}
	aarch, err := adoptiumArch()
	if err != nil {
		return "", err
	}
	root := jvmHomeRoot()
	// Held for the whole install, from any process (the CLI included), so an uninstall
	// cannot stage away a tree this install is about to place or has just placed.
	unlock, _, err := installLock(root, true)
	if err != nil {
		return "", err
	}
	defer unlock()
	// Download to a temp tarball, extract into a staging dir, then atomically swap
	// it into place — a failed/half download never corrupts an existing JDK.
	tgz, err := os.CreateTemp(root, ".jdk-*.tgz")
	if err != nil {
		return "", err
	}
	tgzPath := tgz.Name()
	tgz.Close()
	defer os.Remove(tgzPath)
	staging, err := os.MkdirTemp(root, ".install-"+id+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)

	url := jdkOpsOf(vendor).url(major, aarch)
	fmt.Fprintf(os.Stderr, "[install-jdk] downloading %s (%s) ...\n", jdkvendor.Label(id), aarch)
	// curl follows the API redirect to the release tarball (--proto-redir =https refuses a
	// downgrade redirect to http). tar --strip-components=1
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

	dest := filepath.Join(root, vendor.Dir+"-"+major+"-jdk-"+runtime.GOARCH)
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

// runJavaHome is `workspace-agent java-home <id>`: print the JAVA_HOME the id
// resolves to (javaHomeFor), exit 1 when it is not installed for this architecture.
// The entrypoint uses it so the container-start JAVA_HOME and the per-session one
// cannot disagree.
func runJavaHome(args []string) {
	if _, _, ok := jdkvendor.Parse(firstArg(args)); !ok {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent java-home <major>|corretto-<major>   (e.g. 21, corretto-21)")
		os.Exit(2)
	}
	jh := javaHomeFor(args[0])
	if jh == "" {
		os.Exit(1)
	}
	fmt.Println(jh)
}

func runCmd(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stderr = os.Stderr
	return c.Run()
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
