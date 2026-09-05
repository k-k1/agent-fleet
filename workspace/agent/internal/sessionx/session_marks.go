package sessionx

// Transcript marks (docs/log/69 / ADR 0050): a per-session annotation store that underlines
// "here" in a conversation and shows the same mark at the same place to whoever the session
// is shared with.
//
// The anchor is the equivalent of W3C Web Annotation's TextQuoteSelector (quoted string plus
// occurrence number), counted within the rendered text of a single part. The ordinal of a
// transcript line (Idx) is not used because compaction moves it (see the comment on
// transcript.Idx). Details in docs/log/69 §69.3.
//
// Kind is validated here so that a mark's Quote cannot smuggle out the coordinates the shared
// DTO drops (cwd / file / diffs, docs/log/69 §69.4). Only prose fields that pass through the
// shared DTO untouched may be marked, and enforcing that invariant at write time keeps the
// leak closed even if the Console later allows marking in more places.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// MarkQuoteMaxRunes is the stored quote length: enough to anchor with, short enough that
	// the list stays readable (the same value as the Console's planComments.MAX_QUOTE).
	MarkQuoteMaxRunes = 300
	markTurnMaxBytes  = 256
	// markAuthorMaxBytes caps the login id (an email address).
	markAuthorMaxBytes = 320
	// markMaxPerSession / markMaxPerAuthor stop a runaway loop, and stop one person from
	// papering over the whole conversation.
	markMaxPerSession = 200
	markMaxPerAuthor  = 100
	markMaxParts      = 4096
)

// markProseKinds lists the part kinds that render prose passing through the shared DTO
// untouched. A kind absent from here cannot be marked (docs/log/69 §69.4). "" is the turn
// body (Turn.Text).
var markProseKinds = map[string]bool{
	"":       true,
	"text":   true,
	"plan":   true,
	"answer": true,
	"output": true,
	"prompt": true,
}

var markColors = map[string]bool{"yellow": true, "green": true, "blue": true, "pink": true}

type sessionMark struct {
	ID string `json:"id"`
	// Turn is the stable identity of the source turn: the Agent's own anchorId, or for kinds
	// that have none, the "h:<hash>" the Console derives from the body. Idx (the line ordinal)
	// is not used because compaction moves it.
	Turn string `json:"turn"`
	// Part is the part number within the source turn, not relative to the block (Group):
	// groupTurns() concatenates the parts of consecutive turns, so a block-relative number
	// shifts on both sides of the tail window's boundary.
	Part int `json:"part"`
	// Kind is that part's kind ("" = turn body), checked against markProseKinds.
	Kind  string `json:"kind"`
	Quote string `json:"quote"`
	Nth   int    `json:"nth"`
	Color string `json:"color"`
	// Author empty means the session's owner. For a mark added by someone the session is shared
	// with, the CP overwrites this with the authenticated login id before passing it on (the
	// Agent does not know the caller's identity).
	Author    string `json:"author,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

func SessionMarksPath(name string) string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "session-marks", name+".json")
}

func readSessionMarks(name string) ([]*sessionMark, error) {
	b, err := os.ReadFile(SessionMarksPath(name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*sessionMark
	if err := json.Unmarshal(b, &list); err != nil {
		// Broken JSON must not stop the conversation over an auxiliary feature: start empty.
		return nil, nil
	}
	out := list[:0]
	for _, m := range list {
		if m != nil && m.ID != "" && m.Quote != "" {
			out = append(out, m)
		}
	}
	return out, nil
}

// writeSessionMarks stores list as it is. When empty it removes the file rather than leaving
// an empty array behind.
func writeSessionMarks(name string, list []*sessionMark) error {
	path := SessionMarksPath(name)
	if len(list) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// RemoveSessionMarks cleans up after a session is gone, i.e. once the slot name can be
// reused. Left behind, the previous session's marks show up in the next session in that slot.
func RemoveSessionMarks(name string) {
	_ = os.Remove(SessionMarksPath(name))
}

var errMarkExists = errors.New("mark already exists")

// addSessionMark is create-only. A resend of the same id returns what is already stored, so
// as long as the caller allocates the id, a retry does not add the mark twice - idempotent
// without an Operation-ID ledger.
func addSessionMark(name string, m *sessionMark) (*sessionMark, error) {
	list, err := readSessionMarks(name)
	if err != nil {
		return nil, err
	}
	byAuthor := 0
	for _, x := range list {
		if x.ID == m.ID {
			return x, errMarkExists
		}
		if x.Author == m.Author {
			byAuthor++
		}
	}
	if len(list) >= markMaxPerSession {
		return nil, errors.New("too many marks in this session")
	}
	if byAuthor >= markMaxPerAuthor {
		return nil, errors.New("too many marks by this author")
	}
	m.CreatedAt = time.Now().UnixMilli()
	list = append(list, m)
	if err := writeSessionMarks(name, list); err != nil {
		return nil, err
	}
	return m, nil
}

// deleteSessionMark removes the mark with that id. A non-empty author restricts the deletion
// to that author's own marks: someone the session is shared with may only delete their own
// (the CP passes their login id). Deletion by the owner passes no author and may remove
// anyone's.
func deleteSessionMark(name, id, author string) error {
	list, err := readSessionMarks(name)
	if err != nil {
		return err
	}
	out := list[:0]
	found := false
	for _, m := range list {
		if m.ID == id {
			if author != "" && m.Author != author {
				return os.ErrPermission
			}
			found = true
			continue
		}
		out = append(out, m)
	}
	if !found {
		return os.ErrNotExist
	}
	return writeSessionMarks(name, out)
}

// validMarkID only checks the shape, because the caller allocates the id (that is what makes
// create-only idempotent).
func validMarkID(id string) bool {
	if !strings.HasPrefix(id, "mk_") || len(id) < 6 || len(id) > 40 {
		return false
	}
	for _, r := range id[3:] {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// HandleSessionMarks serves GET (list), POST (add) and DELETE (remove).
//
// There are two kinds of caller: the owner's Console (through the CP's /api/sessions/...
// proxy) and someone the session is shared with (through the CP's
// /api/shared-sessions/{id}/marks). The Agent does not know identities, so the CP puts the
// sharee's login id in the body / query. Since the CP always overwrites whatever the client
// claimed, storing the value as passed is all that is needed here.
func HandleSessionMarks(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := readSessionMarks(name)
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "marks_read", err.Error())
			return
		}
		if list == nil {
			list = []*sessionMark{}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"marks": list})
	case http.MethodPost:
		var body struct {
			ID     string `json:"id"`
			Turn   string `json:"turn"`
			Part   int    `json:"part"`
			Kind   string `json:"kind"`
			Quote  string `json:"quote"`
			Nth    int    `json:"nth"`
			Color  string `json:"color"`
			Author string `json:"author"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		m := &sessionMark{
			ID:     strings.TrimSpace(body.ID),
			Turn:   strings.TrimSpace(body.Turn),
			Part:   body.Part,
			Kind:   body.Kind,
			Quote:  truncateRunes(body.Quote, MarkQuoteMaxRunes),
			Nth:    body.Nth,
			Color:  body.Color,
			Author: truncateRunes(strings.TrimSpace(body.Author), markAuthorMaxBytes),
		}
		if !validMarkID(m.ID) {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_bad_id", "id must be mk_<hex>")
			return
		}
		if m.Turn == "" || len(m.Turn) > markTurnMaxBytes {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_bad_turn", "turn anchor is missing or too long")
			return
		}
		if m.Part < 0 || m.Part > markMaxParts || m.Nth < 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_bad_anchor", "part/nth out of range")
			return
		}
		if m.Quote == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_quote_empty", "quote is empty")
			return
		}
		if !markProseKinds[m.Kind] {
			// Parts carrying coordinates (tool-line paths, diffs) must not be markable. Relax this
			// and a path the shared DTO is supposed to drop reaches the sharee as a Quote
			// (docs/log/69 §69.4).
			httpx.WriteErr(w, http.StatusBadRequest, "mark_kind_not_markable", "this part kind cannot be marked")
			return
		}
		if !markColors[m.Color] {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_bad_color", "unknown color")
			return
		}
		saved, err := addSessionMark(name, m)
		if errors.Is(err, errMarkExists) {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"mark": saved}) // a resend is a no-op
			return
		}
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_write", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"mark": saved})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "mark_id_missing", "id is required")
			return
		}
		err := deleteSessionMark(name, id, strings.TrimSpace(r.URL.Query().Get("author")))
		if errors.Is(err, os.ErrPermission) {
			httpx.WriteErr(w, http.StatusForbidden, "mark_not_yours", "this mark belongs to someone else")
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			httpx.WriteErr(w, http.StatusInternalServerError, "mark_delete", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpx.WriteErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
