package main

// jdk_latest_http.go — "is there a newer patch of this JDK?" for Settings → Toolchains.
//
// The toolchains GET stays offline: it only reports what is on disk (java_versions).
// Asking Adoptium is a separate call the Console makes for the selected major, so a
// workspace without egress still opens the tab instantly and simply shows no update.
// The comparison is made here, with the same compareJDKVersions javaHomeFor uses, so
// the Console never has to parse a JDK version.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const (
	jdkLatestTTL     = 6 * time.Hour
	jdkLatestFailTTL = 10 * time.Minute // a failed lookup is not retried on every tab open
)

type jdkLatestEntry struct {
	version string
	err     string
	at      time.Time
}

// jdkLatestCall is one in-flight lookup that concurrent callers for the same major
// share. Without it two tab opens both fetch, and whichever finishes last wins the
// cache: a timeout landing after a success hid the update for the failure TTL.
type jdkLatestCall struct {
	done chan struct{}
	e    jdkLatestEntry
}

var (
	jdkLatestMu       sync.Mutex // guards the cache and the in-flight table
	jdkLatestCache    = map[string]jdkLatestEntry{}
	jdkLatestInflight = map[string]*jdkLatestCall{}
	// jdkLatestFetch is a var so tests can answer without the network.
	jdkLatestFetch = fetchJDKLatest
	jdkLatestNow   = time.Now
)

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

// fetchJDKLatest asks Adoptium for the latest GA Temurin of a major on this
// architecture — the same build `install-jdk` downloads.
func fetchJDKLatest(major string) (string, error) {
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

// jdkLatest returns the cached latest version of a major, fetching when the entry
// is missing or stale. Concurrent callers for one major share a single fetch.
func jdkLatest(major string) (string, error) {
	now := jdkLatestNow()
	jdkLatestMu.Lock()
	if e, ok := jdkLatestCache[major]; ok && now.Sub(e.at) < e.ttl() {
		jdkLatestMu.Unlock()
		return e.result()
	}
	if c := jdkLatestInflight[major]; c != nil {
		jdkLatestMu.Unlock()
		<-c.done
		return c.e.result()
	}
	c := &jdkLatestCall{done: make(chan struct{})}
	jdkLatestInflight[major] = c
	jdkLatestMu.Unlock()

	v, err := jdkLatestFetch(major)
	c.e = jdkLatestEntry{version: v, at: now}
	if err != nil {
		c.e.err = err.Error()
	}
	jdkLatestMu.Lock()
	jdkLatestCache[major] = c.e
	delete(jdkLatestInflight, major)
	jdkLatestMu.Unlock()
	close(c.done)
	return c.e.result()
}

func (e jdkLatestEntry) ttl() time.Duration {
	if e.err != "" {
		return jdkLatestFailTTL
	}
	return jdkLatestTTL
}

func (e jdkLatestEntry) result() (string, error) {
	if e.err != "" {
		return "", errors.New(e.err)
	}
	return e.version, nil
}

// jdkLatestReply is the GET /env/jdk-latest answer.
type jdkLatestReply struct {
	Major           string `json:"major"`
	Current         string `json:"current"` // "" = not installed: nothing to update
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
}

// handleJDKLatest is GET /env/jdk-latest?major=21: the version JAVA_HOME resolves to
// now, the latest GA patch, and whether installing would update it. A major that is
// not installed has nothing to update (the install button covers it).
func handleJDKLatest(w http.ResponseWriter, r *http.Request) {
	major := r.URL.Query().Get("major")
	// The major reaches a URL, so only the digits the picker offers.
	if !majorOnlyRe.MatchString(major) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_major", "invalid JDK major version")
		return
	}
	current := ""
	if jh := javaHomeFor(major); jh != "" {
		current = jdkVersion(jh)
	}
	if current == "" {
		httpx.WriteJSON(w, http.StatusOK, jdkLatestReply{Major: major})
		return
	}
	latest, err := jdkLatest(major)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "jdk_latest_unavailable", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, jdkLatestReply{
		Major:           major,
		Current:         current,
		Latest:          latest,
		UpdateAvailable: compareJDKVersions(latest, current) > 0,
	})
}

// installedJavaVersions maps each installed major to the version its JAVA_HOME
// resolves to ("" when the tree records none), for the Console to show.
func installedJavaVersions() map[string]string {
	out := map[string]string{}
	for _, m := range installedJavaMajors() {
		if jh := javaHomeFor(m); jh != "" {
			out[m] = jdkVersion(jh)
		}
	}
	return out
}
