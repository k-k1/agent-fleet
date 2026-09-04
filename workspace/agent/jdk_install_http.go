package main

// jdk_install_http.go — the HTTP face of the on-demand Temurin installer, i.e. the
// Console's one-button "install this JDK" in Settings → Toolchains.
//
// Java is the one toolchain the picker could offer without being able to deliver: the
// list is "installed ∪ installable" (jdk.go javaOptions), and choosing an installable
// major only wrote the selection — the download happened at the NEXT container start.
// So the member picked Temurin 21, nothing changed in their running workspace, and the
// only way forward was a Stop → Start or `workspace-agent install-jdk 21` in a terminal.
// On ECS that is also the ONLY source of a JDK at all (/usr/lib/jvm is empty there).
//
// This exposes the same installJDK() as a background job with a tiny state machine the
// settings tab polls, mirroring the Kiro installer (kiro_install_http.go): POST starts
// it (idempotent while running), GET reports {state, error} plus the majors on disk.
// A finished install needs no restart — resolvedToolchains() globs the JDK dirs at each
// session/shell launch, so the next launch already has the new JAVA_HOME.

import (
	"net/http"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

type jdkInstall struct {
	mu    sync.Mutex
	state string // "" (idle) | "installing" | "done" | "error"
	major string // the major the last/current job is for
	err   string
}

var jdkInstaller jdkInstall

func (j *jdkInstall) snapshot() (state, major, errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	state = j.state
	if state == "" {
		state = "idle"
	}
	return state, j.major, j.err
}

// jdkInstallStatus is the body both verbs answer with, so the caller always learns the
// job state AND what is actually on disk in one round trip.
func jdkInstallStatus() map[string]any {
	state, major, errMsg := jdkInstaller.snapshot()
	return map[string]any{
		"state":          state,
		"major":          major,
		"error":          errMsg,
		"java_installed": installedJavaMajors(),
		"java_available": javaOptions(),
	}
}

// handleJDKInstall drives the on-demand JDK install. POST /env/jdk-install {"major":"21"}
// starts it; GET reports the state.
func handleJDKInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		httpx.WriteJSON(w, http.StatusOK, jdkInstallStatus())
		return
	}
	var req struct {
		Major string `json:"major"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	// The major reaches a URL and a directory name, so accept only the digits the
	// picker offers — never a free-form string.
	if !majorOnlyRe.MatchString(req.Major) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_major", "invalid JDK major version")
		return
	}
	installable := false
	for _, v := range javaOptions() {
		if v == req.Major {
			installable = true
			break
		}
	}
	if !installable {
		httpx.WriteErr(w, http.StatusBadRequest, "unsupported_major", "Temurin "+req.Major+" is not offered by this workspace")
		return
	}
	jdkInstaller.mu.Lock()
	if jdkInstaller.state == "installing" {
		// One download at a time. A second request while one runs reports the running
		// job rather than starting a competing one (they share the home JVM dir).
		jdkInstaller.mu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, jdkInstallStatus())
		return
	}
	jdkInstaller.state = "installing"
	jdkInstaller.major = req.Major
	jdkInstaller.err = ""
	jdkInstaller.mu.Unlock()

	major := req.Major
	go func() {
		_, err := installJDK(major)
		jdkInstaller.mu.Lock()
		if err != nil {
			jdkInstaller.state, jdkInstaller.err = "error", err.Error()
		} else {
			jdkInstaller.state, jdkInstaller.err = "done", ""
		}
		jdkInstaller.mu.Unlock()
	}()
	httpx.WriteJSON(w, http.StatusOK, jdkInstallStatus())
}
