package imagegen

// A press in a studio (ADR 0100 decision 9): the pane's trial and enqueue buttons and the
// agent's run_image_trial all come through POST /imagegen/studios/{id}/press, which records the
// version and then calls the same queue /imagegen/jobs does. The order is the contract:
//
//	① the press line — the version id, the whole draft, who pressed and when — BEFORE anything
//	  runs, because it is the only record "put this setting back" can restore from (a sidecar
//	  holds the resolved request, not the draft);
//	② the reference gate's copies;
//	③ the enqueue, with the studio and the version on the JobSpec so every picture carries them;
//	④ the press_result line — the group and jobs, or the error.
//
// A crash between ① and ④ leaves a version with no result; StartStudios settles it from the
// sidecars (recoverStudioPresses).

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// The press modes (ImageStudioPress.Mode).
const (
	pressTrial      = "trial"
	pressEnqueue    = "enqueue"
	pressAgentTrial = "agent_trial"
)

// The press_result states (DraftLogEntry.State).
const (
	pressOK        = "ok"
	pressFailed    = "failed"
	pressRecovered = "recovered"
	pressLost      = "lost"
)

// studioJobRequest is the draft as POST /imagegen/jobs' own body — the keys are the same, which
// is why there is no translation table here — adjusted for the button that was pressed.
func studioJobRequest(d ImageStudioDraft, mode string) jobRequest {
	b := jobRequest{
		Provider: d.Provider, Op: d.Op, Prompt: d.Prompt, NegativePrompt: d.NegativePrompt,
		Size: d.Size, AspectRatio: d.AspectRatio, Count: d.Count, Inputs: d.Inputs, Mask: d.Mask,
		Model: d.Model, Loras: d.Loras, Seed: d.Seed, Strength: d.Strength, Params: d.Params,
		Label: d.Label, OutDir: d.OutDir, Jobs: d.Jobs, SeedPolicy: d.SeedPolicy, FullSteps: d.FullSteps,
	}
	switch mode {
	case pressTrial:
		b.Trial, b.Jobs = true, 1
	case pressAgentTrial:
		// One picture at the family's trial steps (decision 3): the agent does not get the
		// member's "trial at full steps", nor a batch.
		b.Trial, b.Jobs, b.Count, b.FullSteps = true, 1, 0, false
	}
	return b
}

// HandleStudioPress answers POST /imagegen/studios/{id}/press.
func HandleStudioPress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body ImageStudioPress
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Mode != pressTrial && body.Mode != pressEnqueue && body.Mode != pressAgentTrial {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_mode", `mode is "trial", "enqueue" or "agent_trial"`)
		return
	}
	agent := body.Mode == pressAgentTrial
	// The studio's lock is held through the enqueue: the version number and the log's order are
	// decided under it, and Enqueue does not wait for a picture.
	unlock := lockStudio(id)
	defer unlock()
	rec, err := loadStudio(id)
	if err != nil {
		writeStudioErr(w, err)
		return
	}
	if agent {
		if body.Session == "" || body.Session != rec.Session {
			httpx.WriteErr(w, http.StatusConflict, "studio_not_bound",
				"this session is not the one bound to the studio; the user can bind it again from the studio pane")
			return
		}
		if !rec.AgentTrial {
			httpx.WriteErr(w, http.StatusForbidden, "agent_trial_off",
				"the user has not allowed the agent to run trials in this studio (studio settings)")
			return
		}
		// A trial runs on the row the pane shows, never on a default the member did not pick.
		if strings.TrimSpace(rec.Draft.Model) == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "no_model",
				"no model is chosen in the studio yet; ask the user to pick one (you can propose one with suggest_model)")
			return
		}
	}
	if studioNeedsMask(rec.Draft) {
		httpx.WriteErr(w, http.StatusBadRequest, "needs_mask",
			"op is inpaint and there is no mask yet; the user places the mask in the studio pane")
		return
	}
	spec, code, msg := studioJobRequest(rec.Draft, body.Mode).spec()
	if code != "" {
		httpx.WriteErr(w, http.StatusBadRequest, code, msg)
		return
	}
	st := logStateLocked(id)
	version := fmt.Sprintf("v%d", st.versions+1)
	author, session := studioAuthorHuman, ""
	if agent {
		author, session = studioAuthorAgent, body.Session
	}
	draft := rec.Draft
	at := studioNow().UTC().Format(time.RFC3339Nano)
	if err := appendStudioLog(id, &DraftLogEntry{Kind: DraftLogPress, At: at, Author: author, Session: session,
		Version: version, Mode: body.Mode, Draft: &draft}); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "press_unrecorded",
			"the press could not be recorded, so nothing was queued: "+err.Error())
		return
	}
	st.versions++

	fail := func(err error) {
		result := &DraftLogEntry{Kind: DraftLogPressResult, At: studioNow().UTC().Format(time.RFC3339Nano),
			Version: version, State: pressFailed, Error: err.Error()}
		if lerr := appendStudioLog(id, result); lerr != nil {
			log.Printf("image studio %s: %s failed (%v) and the failure was not logged: %v", id, version, err, lerr)
		}
		writeStudioPressErr(w, err, agent)
	}
	spec, err = stageJobSpec(spec)
	if err != nil {
		fail(err)
		return
	}
	spec.Studio, spec.Version = id, version
	out, err := jobs.Enqueue(r.Context(), spec)
	if err != nil {
		removeInputSet(spec.InputSet)
		fail(err)
		return
	}
	jobIDs := make([]string, 0, len(out.Jobs))
	for _, j := range out.Jobs {
		jobIDs = append(jobIDs, j.ID)
	}
	result := DraftLogEntry{Kind: DraftLogPressResult, Version: version, Group: out.Group, Jobs: jobIDs, State: pressOK}
	recorded := false
	// Once more at once when the first append fails: the jobs are already running, and the
	// start-up recovery is the only other chance this version gets its result.
	for range 2 {
		e := result
		e.At = studioNow().UTC().Format(time.RFC3339Nano)
		if err := appendStudioLog(id, &e); err == nil {
			recorded = true
			break
		} else {
			log.Printf("image studio %s: the result of %s was not logged: %v", id, version, err)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, ImageStudioPressResult{Version: version, Group: out.Group, Jobs: out.Jobs, Recorded: recorded})
}

// writeStudioPressErr is writeEnqueueErr with the trial queue's refusal said in the studio's
// terms: the three trial places are shared by the whole workspace, and an agent told only "too
// many trials" retries at once — which is exactly what the refusal is there to stop.
func writeStudioPressErr(w http.ResponseWriter, err error, agent bool) {
	if errors.Is(err, errTrialPending) {
		msg := "other trial pictures are already waiting in this workspace (at most " +
			fmt.Sprint(imagegenTrialMax) + "); press again once one of them has come back"
		if agent {
			msg = "他の試走がすでに " + fmt.Sprint(imagegenTrialMax) + " 枚待っています（ワークスペース共通の枠）。" +
				"結果が get_image_studio に出るのを待ってから試してください。続けて呼び直さないでください"
		}
		httpx.WriteErr(w, http.StatusTooManyRequests, "trial_pending", msg)
		return
	}
	writeEnqueueErr(w, err)
}

// recoverStudioPresses gives every press the previous process recorded without a result one:
// "recovered" with the jobs whose pictures carry that version, or "lost" when none do. The
// sidecars are read rather than the history index, which may itself have lost its tail.
func recoverStudioPresses() {
	type key struct{ studio, version string }
	var pending []key
	for _, id := range listStudioIDs() {
		results := map[string]bool{}
		entries := readStudioLog(id)
		for _, e := range entries {
			if e.Kind == DraftLogPressResult {
				results[e.Version] = true
			}
		}
		for _, e := range entries {
			if e.Kind == DraftLogPress && !results[e.Version] {
				pending = append(pending, key{id, e.Version})
			}
		}
	}
	if len(pending) == 0 {
		return
	}
	found := map[key][]HistoryItem{}
	jobsOf := map[key][]string{}
	groupOf := map[key]string{}
	for _, it := range scanSidecars() {
		k := key{it.Studio, it.Version}
		found[k] = append(found[k], it)
		if p, ok := readSidecar(it.Path); ok && !slices.Contains(jobsOf[k], p.Job) {
			jobsOf[k] = append(jobsOf[k], p.Job)
			groupOf[k] = p.Group
		}
	}
	for _, k := range pending {
		e := &DraftLogEntry{Kind: DraftLogPressResult, At: studioNow().UTC().Format(time.RFC3339Nano), Version: k.version}
		if len(found[k]) > 0 {
			e.State, e.Group, e.Jobs = pressRecovered, groupOf[k], jobsOf[k]
		} else {
			e.State, e.Error = pressLost, "the Agent stopped before this press's result was recorded, and no picture carries it"
		}
		unlock := lockStudio(k.studio)
		if err := appendStudioLog(k.studio, e); err != nil {
			log.Printf("image studio %s: could not settle %s: %v", k.studio, k.version, err)
		}
		unlock()
	}
}
