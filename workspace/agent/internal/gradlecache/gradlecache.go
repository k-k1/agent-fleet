package gradlecache

// Package gradlecache decides which parts of ~/.gradle no project here still needs (the Gradle rows of
// the tool-caches list in the Machine tab).
//
// Gradle's share of the tool-cache rows (Phase 3 of #3079, tracked in #3202).
//
// Unlike go or npm, ~/.gradle is not emptied wholesale: its version-keyed parts can be told
// apart, so the two rows below remove only what no project here still names.
//
//	gradle-caches  ~/.gradle/caches/<version>        the per-version caches (not modules-2,
//	                                                 jars-*, build-cache-*, journal-*)
//	gradle-wrapper ~/.gradle/wrapper/dists/gradle-<version>-(bin|all)
//
// A version is kept when a gradle-wrapper.properties under ~/repos names it (distributionUrl)
// or a SDKMAN Gradle of that version is installed. Anything that cannot be read with
// certainty fails closed: an unreadable directory, a properties file without a parseable
// version, or a scan that hit its entry cap turns the row into "skipped" and removes nothing.
//
// Measured on one Workspace (2026-11): caches/8.11.1 and caches/8.14.3 (391 MB + 333 MB, last
// touched 136 and 26 days before) and the two matching dists (146 MB each) were all still on
// disk, so Gradle's own 30-day cleanup is not something to rely on, and the wrapper never
// removes a dist. Both rows are manual-only and not "regenerable" in the Phase 4 sense: the
// next build of a project that needs a removed version downloads it again, which a Workspace
// without access to services.gradle.org cannot do.
//
// Deliberately not a row: ~/.gradle/jdks. Whether a toolchain JDK is still wanted is decided
// by `jvm { … }` / `java.toolchain` blocks inside build scripts in any language version,
// which cannot be read reliably, and the same JDK may be re-provisioned only with network
// access. The directory did not exist on the measured Workspace; it stays in the ~/.gradle
// figure of the home breakdown.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// pinScanEntries caps the repositories walk; hitting it makes the plan "skipped".
const pinScanEntries = 500_000

// Plan is what a partial row would remove right now.
type Plan struct {
	Remove []string
	// Skipped says why nothing may be removed ("" = the plan is decided).
	Skipped string
}

var (
	versionDirRe = regexp.MustCompile(`^[0-9]+\.[0-9]+[0-9A-Za-z._-]*$`)
	distDirRe    = regexp.MustCompile(`^gradle-([0-9]+\.[0-9]+[0-9A-Za-z._-]*)-(bin|all)$`)
	distURLRe    = regexp.MustCompile(`^gradle-([0-9]+\.[0-9]+[0-9A-Za-z._-]*)-(?:bin|all)\.zip$`)

	// Directories a wrapper never lives in, and that hold most of a repository's entries.
	pinScanSkip = map[string]bool{
		".git": true, "node_modules": true, "build": true, "target": true,
		".gradle": true, ".next": true, "dist": true,
	}
)

// pinnedVersions returns every Gradle version a project here would ask for, or the
// reason it cannot be known.
func pinnedVersions(root, home string) (map[string]bool, string) {
	pins := map[string]bool{}
	if _, err := os.Stat(root); err != nil {
		return nil, "cannot read the repositories folder: " + err.Error()
	}
	budget := pinScanEntries
	var fail string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if fail != "" {
			return filepath.SkipAll
		}
		if err != nil {
			fail = "cannot read " + p + ": " + err.Error()
			return filepath.SkipAll
		}
		if budget--; budget <= 0 {
			fail = "the repositories scan hit its entry cap"
			return filepath.SkipAll
		}
		if d.IsDir() {
			// Only names are pruned, never depth: a wrapper deep in a monorepo must still pin its
			// version, and the entry cap already turns a huge tree into "skipped".
			if p != root && pinScanSkip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "gradle-wrapper.properties" || !d.Type().IsRegular() {
			return nil
		}
		v, ok := wrapperVersion(p)
		if !ok {
			fail = "cannot read the Gradle version pinned by " + p
			return filepath.SkipAll
		}
		pins[v] = true
		return nil
	})
	if fail != "" {
		return nil, fail
	}
	// A Gradle installed through SDKMAN runs without a wrapper and uses caches/<its version>.
	sdk := filepath.Join(home, ".sdkman", "candidates", "gradle")
	ents, err := os.ReadDir(sdk)
	if err != nil && !os.IsNotExist(err) {
		return nil, "cannot read " + sdk + ": " + err.Error()
	}
	for _, e := range ents {
		pins[e.Name()] = true
	}
	return pins, ""
}

// wrapperVersion reads the version from a wrapper properties file's distributionUrl.
func wrapperVersion(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "distributionUrl" {
			continue
		}
		v = strings.ReplaceAll(strings.TrimSpace(v), `\:`, ":")
		v = v[strings.LastIndexAny(v, `/\`)+1:]
		if m := distURLRe.FindStringSubmatch(v); m != nil {
			return m[1], true
		}
		return "", false
	}
	return "", false
}

// plan lists the children of dir whose name matches re and carries a version no
// project pins. Links are never candidates: they are not ours to follow.
func plan(dir, repos, home string, re *regexp.Regexp, versionOf func(name string) string) Plan {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return Plan{}
	}
	if err != nil {
		return Plan{Skipped: "cannot read " + dir + ": " + err.Error()}
	}
	pins, why := pinnedVersions(repos, home)
	if why != "" {
		return Plan{Skipped: why}
	}
	var p Plan
	for _, e := range ents {
		if !re.MatchString(e.Name()) || e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
			continue
		}
		if !pins[versionOf(e.Name())] {
			p.Remove = append(p.Remove, filepath.Join(dir, e.Name()))
		}
	}
	return p
}

func PlanCaches(dir, repos, home string) Plan {
	return plan(dir, repos, home, versionDirRe, func(n string) string { return n })
}

func PlanDists(dir, repos, home string) Plan {
	return plan(dir, repos, home, distDirRe, func(n string) string { return distDirRe.FindStringSubmatch(n)[1] })
}

// ArgvRuns matches the gradle launcher, the wrapper script (`sh ./gradlew`) and the
// JVMs Gradle runs in (client, daemon). An unrelated java process does not match; an idle
// daemon does, because it keeps its version's caches open.
func ArgvRuns(argv []string) bool {
	if anyBase(argv[:min(len(argv), 1)], "gradle", "gradlew") {
		return true
	}
	if len(argv) > 1 && isShell(argv[0]) && anyBase(argv[1:2], "gradlew", "gradle") {
		return true
	}
	if len(argv) == 0 || filepath.Base(argv[0]) != "java" {
		return false
	}
	for _, a := range argv[1:] {
		switch a {
		case "org.gradle.launcher.daemon.bootstrap.GradleDaemon",
			"org.gradle.wrapper.GradleWrapperMain",
			"org.gradle.launcher.GradleMain":
			return true
		}
		if strings.HasPrefix(a, "-Dorg.gradle.appname=") {
			return true
		}
	}
	return false
}

func anyBase(argv []string, names ...string) bool {
	if len(argv) == 0 {
		return false
	}
	b := filepath.Base(argv[0])
	for _, n := range names {
		if b == n {
			return true
		}
	}
	return false
}

func isShell(prog string) bool {
	switch filepath.Base(prog) {
	case "sh", "bash", "dash", "zsh":
		return true
	}
	return false
}
