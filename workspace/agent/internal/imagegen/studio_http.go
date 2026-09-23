package imagegen

// The studio routes (ADR 0100). The shapes are studio.go's; the store is studio_store.go's.

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Who wrote a draft change (ImageStudioPatch.Author, DraftLogEntry.Author).
const (
	studioAuthorHuman  = "human"
	studioAuthorAgent  = "agent"
	studioAuthorRewind = "rewind"
)

// studioRecentLog is how many edit-log entries ride on the studio's own GET; the rest is paged
// through draft-log, so the answer the pane polls stays small.
const studioRecentLog = 20

func studioWire(rec *studioRec) ImageStudioWire {
	w := ImageStudioWire{ImageStudio: rec.ImageStudio, NeedsMask: studioNeedsMask(rec.Draft)}
	if w.Locks == nil {
		w.Locks = []string{}
	}
	entries := readStudioLog(rec.ID)
	w.RecentLog = entries[max(len(entries)-studioRecentLog, 0):]
	return w
}

func writeStudio(w http.ResponseWriter, rec *studioRec) {
	w.Header().Set("ETag", strconv.Quote(rec.UpdatedAt))
	httpx.WriteJSON(w, http.StatusOK, studioWire(rec))
}

func writeStudioErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrStudioNotFound):
		httpx.WriteErr(w, http.StatusNotFound, "no_studio", err.Error())
	case errors.Is(err, ErrStudioBound):
		httpx.WriteErr(w, http.StatusConflict, "studio_bound", err.Error())
	default:
		httpx.WriteErr(w, http.StatusInternalServerError, "studio_failed", err.Error())
	}
}

// ifMatchStale reports whether the request names a version other than the studio's. No header
// is not stale: set_image_draft sends none, and its writes are fields rather than the form.
func ifMatchStale(r *http.Request, rec *studioRec) bool {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" || v == "*" {
		return false
	}
	v = strings.TrimPrefix(v, "W/")
	if u, err := strconv.Unquote(v); err == nil {
		v = u
	}
	return v != rec.UpdatedAt
}

func writeStale(w http.ResponseWriter) {
	httpx.WriteErr(w, http.StatusPreconditionFailed, "studio_changed",
		"the studio changed since it was read; read it again and reapply the edit")
}

// HandleStudios answers GET (list) and POST (create) /imagegen/studios.
func HandleStudios(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		httpx.WriteJSON(w, http.StatusOK, ImageStudioList{Studios: listStudios()})
		return
	}
	var body ImageStudioCreate
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	id, err := newStudioID()
	if err != nil {
		writeStudioErr(w, err)
		return
	}
	unlock := lockStudio(id)
	defer unlock()
	// The pane moves its localStorage draft in (decision 2). Held to the same per-field checks as
	// an edit; a value the pane kept that no longer passes is left behind rather than refusing
	// the whole studio.
	draft, changes, _ := applyDraftPatch(ImageStudioDraft{}, draftToMap(body.Draft), studioAuthorHuman, nil)
	rec := &studioRec{ImageStudio: ImageStudio{ID: id, Title: truncateRunes(strings.TrimSpace(body.Title), studioShortTextMax),
		Draft: draft, AgentTrial: true}}
	touchStudio(rec)
	rec.CreatedAt = rec.UpdatedAt
	if err := saveStudio(rec); err != nil {
		writeStudioErr(w, err)
		return
	}
	// The first line is the draft the studio started from, so it can be rewound to.
	if len(changes) > 0 {
		_ = appendStudioLog(id, &DraftLogEntry{Kind: DraftLogEdit, At: rec.UpdatedAt, Author: studioAuthorHuman,
			Changes: changes, Draft: &rec.Draft})
	}
	writeStudio(w, rec)
}

// HandleStudio answers GET, PUT and DELETE /imagegen/studios/{id}.
func HandleStudio(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("view") == "agent" {
			handleStudioAgentView(w, r, id)
			return
		}
		rec, err := loadStudio(id)
		if err != nil {
			writeStudioErr(w, err)
			return
		}
		writeStudio(w, rec)
	case http.MethodPut:
		handleStudioPut(w, r, id)
	case http.MethodDelete:
		handleStudioDelete(w, id)
	default:
		httpx.WriteErr(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
	}
}

func handleStudioPut(w http.ResponseWriter, r *http.Request, id string) {
	var body ImageStudioPatch
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Author != studioAuthorHuman && body.Author != studioAuthorAgent {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_author", `author is "human" or "agent"`)
		return
	}
	unlock := lockStudio(id)
	defer unlock()
	rec, err := loadStudio(id)
	if err != nil {
		writeStudioErr(w, err)
		return
	}
	if ifMatchStale(r, rec) {
		writeStale(w)
		return
	}
	// The call-time half of decision 2: the session writing is the one the studio names back.
	if body.Author == studioAuthorAgent && (body.Session == "" || body.Session != rec.Session) {
		httpx.WriteErr(w, http.StatusConflict, "studio_not_bound",
			"this session is not the one bound to the studio; the user can bind it again from the studio pane")
		return
	}
	draft, changes, dropped := applyDraftPatch(rec.Draft, body.Draft, body.Author, rec.Locks)
	touched := len(changes) > 0
	rec.Draft = draft
	human := body.Author == studioAuthorHuman
	humanOnly := func(field string) {
		dropped = append(dropped, DroppedField{Field: field, Reason: dropHumanOnly, Detail: "only the user sets this"})
	}
	if body.Title != nil {
		if !human {
			humanOnly("title")
		} else if t := strings.TrimSpace(*body.Title); utf8.RuneCountInString(t) > studioShortTextMax {
			dropped = append(dropped, DroppedField{Field: "title", Reason: dropInvalid, Detail: fmt.Sprintf("at most %d characters", studioShortTextMax)})
		} else {
			rec.Title, touched = t, true
		}
	}
	if body.Locks != nil {
		if human {
			locks, bad := validLocks(*body.Locks)
			rec.Locks, touched = locks, true
			dropped = append(dropped, bad...)
		} else {
			humanOnly("locks")
		}
	}
	if body.AgentTrial != nil {
		if human {
			rec.AgentTrial, touched = *body.AgentTrial, true
		} else {
			humanOnly("agent_trial")
		}
	}
	if len(body.MaskStrokes) > 0 {
		if human {
			if isJSONNull(body.MaskStrokes) {
				rec.MaskStrokes = nil
			} else {
				rec.MaskStrokes = body.MaskStrokes
			}
			touched = true
		} else {
			humanOnly("mask_strokes")
		}
	}
	if touched {
		touchStudio(rec)
		if err := saveStudio(rec); err != nil {
			writeStudioErr(w, err)
			return
		}
	}
	if len(changes) > 0 {
		e := &DraftLogEntry{Kind: DraftLogEdit, At: rec.UpdatedAt, Author: body.Author, Changes: changes, Draft: &rec.Draft}
		if body.Author == studioAuthorAgent {
			e.Session = body.Session
		}
		// The draft is saved either way; a missing log line costs one step of the history, which
		// is less than refusing an edit the member can see took effect.
		if err := appendStudioLog(id, e); err != nil {
			log.Printf("image studio %s: the edit was saved but not logged: %v", id, err)
		}
	}
	w.Header().Set("ETag", strconv.Quote(rec.UpdatedAt))
	httpx.WriteJSON(w, http.StatusOK, ImageStudioPatchResult{Studio: studioWire(rec), Dropped: dropped})
}

// handleStudioDelete removes the draft and its history (decision 10). The pictures stay: they
// are in the generated folder and the gallery, not here.
func handleStudioDelete(w http.ResponseWriter, id string) {
	unlock := lockStudio(id)
	defer unlock()
	rec, err := loadStudio(id)
	if err != nil {
		writeStudioErr(w, err)
		return
	}
	if err := os.Remove(studioPath(id)); err != nil {
		writeStudioErr(w, err)
		return
	}
	_ = os.Remove(studioLogPath(id))
	forgetStudioLogState(id)
	if rec.Session != "" && SessionStudioCAS != nil {
		SessionStudioCAS(rec.Session, id, "")
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HandleStudioBind answers POST /imagegen/studios/{id}/bind: bind an existing session, or
// unbind with an empty one. Both sides are written, the studio first (it is the truth).
func HandleStudioBind(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body ImageStudioBind
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Session)
	if name == "" {
		unlock := lockStudio(id)
		rec, err := loadStudio(id)
		if err != nil {
			unlock()
			writeStudioErr(w, err)
			return
		}
		prev := rec.Session
		if prev != "" {
			rec.Session = ""
			touchStudio(rec)
			if err := saveStudio(rec); err != nil {
				unlock()
				writeStudioErr(w, err)
				return
			}
		}
		unlock()
		if prev != "" && SessionStudioCAS != nil {
			SessionStudioCAS(prev, id, "")
		}
		writeStudio(w, rec)
		return
	}
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_session", "invalid session name: "+name)
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_session", "no such session: "+name)
		return
	}
	if why := StudioSessionUnsupported(m.Kind, m.DriverKind()); why != "" {
		httpx.WriteErr(w, http.StatusConflict, "studio_kind_unsupported", why)
		return
	}
	if m.Studio != "" && m.Studio != id {
		httpx.WriteErr(w, http.StatusConflict, "session_bound", "the session is bound to another image studio")
		return
	}
	replaced, err := bindStudioSession(id, name, "")
	if err != nil {
		writeStudioErr(w, err)
		return
	}
	if SessionStudioCAS != nil {
		if replaced != "" {
			SessionStudioCAS(replaced, id, "")
		}
		SessionStudioCAS(name, "", id)
	}
	rec, err := loadStudio(id)
	if err != nil {
		writeStudioErr(w, err)
		return
	}
	writeStudio(w, rec)
}

// HandleStudioRewind answers POST /imagegen/studios/{id}/rewind (decision 9). A rewind is the
// member's act, so it restores the WHOLE draft — the locked fields included (locks bind the
// agent, not the member) — and leaves the locks as they are. Nothing is deleted from the log:
// the rewind is a line of its own, and the next get_image_studio tells the agent about it.
func HandleStudioRewind(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body ImageStudioRewind
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	unlock := lockStudio(id)
	defer unlock()
	rec, err := loadStudio(id)
	if err != nil {
		writeStudioErr(w, err)
		return
	}
	if ifMatchStale(r, rec) {
		writeStale(w)
		return
	}
	var target *ImageStudioDraft
	for _, e := range readStudioLog(id) {
		if e.Seq == body.To && e.Draft != nil {
			target = e.Draft
		}
	}
	if target == nil {
		httpx.WriteErr(w, http.StatusNotFound, "no_entry", "no history entry with a draft at #"+strconv.Itoa(body.To))
		return
	}
	changes := draftChanges(rec.Draft, *target)
	rec.Draft = *target
	touchStudio(rec)
	if err := saveStudio(rec); err != nil {
		writeStudioErr(w, err)
		return
	}
	if err := appendStudioLog(id, &DraftLogEntry{Kind: DraftLogRewind, At: rec.UpdatedAt, Author: studioAuthorRewind,
		Changes: changes, Draft: &rec.Draft, RewindTo: body.To}); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "unlogged", "the draft was restored but the rewind is not in the history: "+err.Error())
		return
	}
	writeStudio(w, rec)
}

// HandleStudioDraftLog answers GET /imagegen/studios/{id}/draft-log?before=&limit=.
func HandleStudioDraftLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := loadStudio(id); err != nil {
		writeStudioErr(w, err)
		return
	}
	before, _ := strconv.Atoi(r.URL.Query().Get("before"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	httpx.WriteJSON(w, http.StatusOK, draftLogPage(readStudioLog(id), before, limit))
}
