// Package jdkvendor names the JDK distributions the Agent can install and the JDK id
// that selects one of them everywhere a JDK is chosen: toolchains.json's "java", the
// install-jdk / java-home operands, and the Console's /env/jdk* endpoints.
//
// An id is a bare major ("21") for Temurin or "<prefix>-<major>" ("corretto-21") for
// any other vendor. Temurin stays bare because every selection stored before a second
// vendor existed is a bare major, and the entrypoint passes that value straight to
// java-home / install-jdk; a prefixed Temurin id would need a migration of every home
// volume for nothing. On disk every vendor uses <dir>-<major>-jdk-<goarch>, the layout
// /usr/lib/jvm already has, so one discovery path finds them all.
//
// This package holds what is vendor-specific and has no Agent state: ids, names,
// download URLs, and Corretto's version scheme. Temurin's version handling stays with
// the Agent's resolver, which predates this package.
package jdkvendor

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Vendor is one JDK distribution.
type Vendor struct {
	Prefix string // id prefix before "-<major>"; "" for Temurin, whose ids are bare
	Dir    string // tree name prefix: <Dir>-<major>-jdk-<goarch>
	Label  string // display name, also used in error messages
}

var (
	Temurin  = &Vendor{Dir: "temurin", Label: "Temurin"}
	Corretto = &Vendor{Prefix: "corretto", Dir: "corretto", Label: "Corretto"}
)

// All lists every vendor; its order is also the display order of one major's vendors.
var All = []*Vendor{Temurin, Corretto}

// InstallableMajors are the majors every vendor publishes a Linux tarball for on both
// architectures (Adoptium GA, corretto.aws "latest").
var InstallableMajors = []string{"8", "11", "17", "21", "25"}

var majorRe = regexp.MustCompile(`^\d+$`)

// Parse splits an id into its vendor and major; ok is false for anything that is not
// exactly a bare major or "<known prefix>-<major>". The id reaches URLs, directory
// globs and a shell export, so nothing looser may pass.
func Parse(id string) (v *Vendor, major string, ok bool) {
	if majorRe.MatchString(id) {
		return Temurin, id, true
	}
	for _, v := range All {
		if v.Prefix == "" {
			continue
		}
		if m, found := strings.CutPrefix(id, v.Prefix+"-"); found && majorRe.MatchString(m) {
			return v, m, true
		}
	}
	return nil, "", false
}

// ID is the id of a vendor's major.
func ID(v *Vendor, major string) string {
	if v.Prefix == "" {
		return major
	}
	return v.Prefix + "-" + major
}

// Label is the human name of an id ("Temurin 21", "Corretto 21"); an id that does not
// parse is returned as is.
func Label(id string) string {
	if v, major, ok := Parse(id); ok {
		return v.Label + " " + major
	}
	return id
}

// Rank is a vendor's position in All, for sorting.
func Rank(v *Vendor) int {
	for i, x := range All {
		if x == v {
			return i
		}
	}
	return len(All)
}

var treeRe = func() *regexp.Regexp {
	dirs := make([]string, 0, len(All))
	for _, v := range All {
		dirs = append(dirs, regexp.QuoteMeta(v.Dir))
	}
	return regexp.MustCompile(`^(` + strings.Join(dirs, "|") + `)-(\d+)-jdk`)
}()

// IDOfTree is the id a JDK directory name belongs to, or "" for a name no vendor uses.
func IDOfTree(name string) string {
	m := treeRe.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	for _, v := range All {
		if v.Dir == m[1] {
			return ID(v, m[2])
		}
	}
	return ""
}

// TreeGlob is the glob for one vendor major's trees in a JDK root. "-jdk" right after
// the major keeps "temurin-2" from matching temurin-21.
func TreeGlob(root string, v *Vendor, major string) string {
	return filepath.Join(root, v.Dir+"-"+major+"-jdk*")
}

// TemurinURL is Adoptium's latest-GA binary link; it redirects to the release tarball.
func TemurinURL(major, arch string) string {
	return fmt.Sprintf("https://api.adoptium.net/v3/binary/latest/%s/ga/linux/%s/jdk/hotspot/normal/eclipse", major, arch)
}

// CorrettoBaseURL is a var so tests can point the latest lookup at a local server.
var CorrettoBaseURL = "https://corretto.aws"

// CorrettoURL is Amazon's permanent "latest" link; it redirects to the versioned
// tarball (/downloads/resources/<version>/...). The arch tokens are Adoptium's
// (x64 / aarch64).
func CorrettoURL(major, arch string) string {
	return fmt.Sprintf("%s/downloads/latest/amazon-corretto-%s-%s-linux-jdk.tar.gz", CorrettoBaseURL, major, arch)
}

var correttoResourceRe = regexp.MustCompile(`/downloads/resources/(\d+(?:\.\d+)+)/`)

// CorrettoLatest reads the version out of the "latest" link's redirect without
// following it. corretto.aws has no JSON API like Adoptium's, and the redirect target
// is exactly what install-jdk then downloads, so the two cannot disagree.
func CorrettoLatest(major, arch string) (string, error) {
	cl := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := cl.Head(CorrettoURL(major, arch))
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("corretto: HTTP %d", resp.StatusCode)
	}
	m := correttoResourceRe.FindStringSubmatch(resp.Header.Get("Location"))
	if m == nil {
		return "", fmt.Errorf("corretto: no version in redirect for %s", major)
	}
	return m[1], nil
}

// CorrettoTreeVersion is the Corretto version a tree records in version.txt
// ("21.0.13.0.11", "8.512.09.1"), or "". Corretto 8 ships no release file at all, and
// on 11+ the release file has no SEMANTIC_VERSION; version.txt is present on every
// major and uses the same scheme as the redirect CorrettoLatest reads.
func CorrettoTreeVersion(home string) string {
	b, err := os.ReadFile(filepath.Join(home, "version.txt"))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if dotted(v) == nil {
		return ""
	}
	return v
}

// CompareCorretto orders two Corretto versions over every dotted field. A
// four-field-plus-build comparison would not do: 21.0.13.0.11 and 21.0.13.0.12 differ
// only in the fifth field.
func CompareCorretto(a, b string) int {
	pa, pb := dotted(a), dotted(b)
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

func dotted(s string) []int {
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}
