package main

// jdk_latest_http.go — "which patch would Install fetch, and is it newer than the one
// in use?" for the Java row of Settings → Toolchains.
//
// The toolchains GET stays offline: it only reports what is on disk (java_versions).
// Asking the vendor (Adoptium, corretto.aws) is a separate call the Console makes for
// the selected JDK, so a
// workspace without egress still opens the tab instantly and simply shows no update.
// The comparison is made here, with the same vendor compare javaHomeFor uses, so the
// Console never has to parse a JDK version.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/jdkvendor"
)

var jdkLatestLookup = newVersionLookup(fetchJDKLatest)

// adoptiumVersion is the "version" object of an Adoptium v3 asset.
type adoptiumVersion struct {
	Major    int `json:"major"`
	Minor    int `json:"minor"`
	Security int `json:"security"`
	Patch    int `json:"patch"`
	Build    int `json:"build"`
}

// semantic renders it in the release file's SEMANTIC_VERSION form ("21.0.13+9",
// "8.0.504+1"). Adoptium's own semver adds ".0.LTS", and openjdk_version is
// "1.8.0_504-b01" on 8, so neither can be shown next to the installed version as is.
func (v adoptiumVersion) semantic() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Security)
	if v.Patch > 0 {
		s += fmt.Sprintf(".%d", v.Patch)
	}
	return fmt.Sprintf("%s+%d", s, v.Build)
}

// fetchJDKLatest asks the id's vendor for its latest GA release on this architecture —
// the same build `install-jdk` downloads. The id is the lookup's cache key, so Temurin
// 21 and Corretto 21 are cached apart.
func fetchJDKLatest(id string) (string, error) {
	v, major, ok := jdkvendor.Parse(id)
	if !ok {
		return "", fmt.Errorf("invalid JDK %q", id)
	}
	return jdkOpsOf(v).latest(major)
}

// fetchTemurinLatest asks Adoptium for the latest GA Temurin of a major.
func fetchTemurinLatest(major string) (string, error) {
	aarch, err := adoptiumArch()
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("https://api.adoptium.net/v3/assets/latest/%s/hotspot?architecture=%s&image_type=jdk&os=linux&vendor=eclipse", major, aarch)
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("adoptium: HTTP %d", resp.StatusCode)
	}
	var assets []struct {
		Version adoptiumVersion `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
		return "", fmt.Errorf("adoptium: %w", err)
	}
	if len(assets) == 0 || assets[0].Version.Major == 0 {
		return "", fmt.Errorf("adoptium: no GA release for %s", major)
	}
	return assets[0].Version.semantic(), nil
}

// handleJDKLatest is GET /env/jdk-latest?major=<id> (21, corretto-21): the version
// JAVA_HOME resolves to now, the latest GA patch, and whether installing would update
// it. For an id that is not installed, latest is what Install would download. The
// parameter keeps its name "major" because Temurin ids are majors.
func handleJDKLatest(w http.ResponseWriter, r *http.Request) {
	major := r.URL.Query().Get("major")
	// The id reaches a URL and a cache key, so only the ids the picker offers.
	vendor, _, ok := jdkvendor.Parse(major)
	if !ok || !isOffered(major, javaOptions()) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_major", "invalid JDK major version")
		return
	}
	current := ""
	if jh := javaHomeFor(major); jh != "" {
		current = jdkVersion(jh)
	}
	latest, err := jdkLatestLookup.latest(major)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "jdk_latest_unavailable", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toolchainLatestReply{
		Major:           major,
		Current:         current,
		Latest:          latest,
		UpdateAvailable: current != "" && jdkOpsOf(vendor).compare(latest, current) > 0,
	})
}

// installedJavaVersions maps each installed JDK id to the version its JAVA_HOME
// resolves to ("" when the tree records none), for the Console to show.
func installedJavaVersions() map[string]string {
	out := map[string]string{}
	for _, m := range installedJavaIDs() {
		if jh := javaHomeFor(m); jh != "" {
			out[m] = jdkVersion(jh)
		}
	}
	return out
}
