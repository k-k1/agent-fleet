package main

// "Push, then send" (#2072) through the CP's offer handler, with the owner's Agent replaced by the
// lifecycle stub: the push is pinned to the snapshot the owner confirmed, the Agent call carries a
// member-origin mark, the offer is stored only on the verdict the Agent builds after its push and
// only while that verdict still describes the confirmed commit, and a refused push leaves no offer.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const (
	confirmedHead   = "abcdef1234567890"
	confirmedRemote = "https://example.com/team/private.git"
)

func confirmedPush() map[string]any {
	return map[string]any{"pushFirst": true, "pushExpect": map[string]any{
		"branch": "feature/x", "headSha": confirmedHead, "pushRemote": confirmedRemote}}
}

// blockedForPush makes the stub Agent report a gate a push can clear, with the push target set
// as the real Agent does.
func (f *handoffLife) blockedForPush(reason string) {
	f.setAgent(map[string]any{"blocked": reason, "pushRemote": confirmedRemote})
}

// pushClears makes the stub Agent behave like a push that succeeded: the next handoff-context
// reports the gate open on the same HEAD. Every request it saw is kept.
func (f *handoffLife) pushClears(seen *[]*http.Request, bodies *[][]byte) {
	f.push = func(r *http.Request, body []byte) (int, map[string]any) {
		*seen, *bodies = append(*seen, r), append(*bodies, body)
		delete(f.agentCtx, "blocked")
		delete(f.agentCtx, "pushRemote")
		return http.StatusOK, map[string]any{"pushed": true, "remote": confirmedRemote, "branch": "feature/x"}
	}
}

// pushThenSpace is pushClears for a push after which the Agent starts reporting the space (a verdict
// that changed under the owner's feet).
func (f *handoffLife) pushThenSpace(seen *[]*http.Request, bodies *[][]byte) {
	f.push = func(r *http.Request, body []byte) (int, map[string]any) {
		*seen, *bodies = append(*seen, r), append(*bodies, body)
		delete(f.agentCtx, "blocked")
		delete(f.agentCtx, "pushRemote")
		f.agentCtx["space"], f.agentCtx["warning"] = true, "space_files_stay"
		return http.StatusOK, map[string]any{"pushed": true, "remote": confirmedRemote, "branch": "feature/x"}
	}
}

func (f *handoffLife) offerRows() int {
	f.t.Helper()
	rows, err := f.st.ListSessionHandoffOffersByOwner(context.Background(), f.owner.mv.MembershipID)
	if err != nil {
		f.t.Fatal(err)
	}
	return len(rows)
}

func (f *handoffLife) pushHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits["/sessions/session-1/handoff-push"]
}

func TestHandoffPushThenSendStoresTheConfirmedHead(t *testing.T) {
	for _, reason := range []string{"no_upstream", "unpushed_commits"} {
		t.Run(reason, func(t *testing.T) {
			f := newHandoffLife(t)
			f.blockedForPush(reason)
			var seen []*http.Request
			var bodies [][]byte
			f.pushClears(&seen, &bodies)
			o := f.mustOffer(confirmedPush())
			if o.HeadSha != confirmedHead || f.row(o.ID).HeadSha != confirmedHead || o.RepoRemote != confirmedRemote {
				t.Fatalf("offer = %+v, want %s from %s", o, confirmedHead, confirmedRemote)
			}
			if len(seen) != 1 {
				t.Fatalf("push hits = %d, want 1", len(seen))
			}
			// The Agent is told exactly what was confirmed, under the CP's mark for this call.
			var got handoffPushExpect
			if json.Unmarshal(bodies[0], &got) != nil || got != (handoffPushExpect{"feature/x", confirmedHead, confirmedRemote}) {
				t.Fatalf("push body = %s", bodies[0])
			}
			pub := memberOriginKey(f.api.mgr.tokenSignMaster(), 1).Public().(ed25519.PublicKey)
			c := decodeMark(t, pub, seen[0].Header.Get(memberOriginHeader))
			sum := sha256.Sum256(bodies[0])
			if c.Method != http.MethodPost || c.Path != "/sessions/session-1/handoff-push" || c.BodySHA256 != hex.EncodeToString(sum[:]) ||
				c.Membership != f.owner.mv.MembershipID || c.Workspace != "ws-owner" || c.Subject != f.owner.ident.ID {
				t.Fatalf("mark claims = %+v", c)
			}
		})
	}
}

// TestHandoffPushFailureStoresNoOffer: a rejected push (non-fast-forward, protected branch,
// pre-push hook) reaches the owner as git reported it, and nothing is sent.
func TestHandoffPushFailureStoresNoOffer(t *testing.T) {
	f := newHandoffLife(t)
	f.blockedForPush("unpushed_commits")
	const gitSaid = " ! [rejected]        feature/x -> feature/x (non-fast-forward)"
	f.push = func(*http.Request, []byte) (int, map[string]any) {
		return http.StatusConflict, map[string]any{"error": map[string]string{"code": "push_rejected", "message": gitSaid}}
	}
	rec := f.offer(confirmedPush())
	f.requireConflict(rec, "handoff_push_failed", "")
	if e := errorOf(t, rec); e["reason"] != "push_rejected" || !strings.Contains(e["message"].(string), "non-fast-forward") {
		t.Fatalf("push failure answered %v, want reason push_rejected with git's message", e)
	}
	if n := f.offerRows(); n != 0 {
		t.Fatalf("%d offers stored after a failed push", n)
	}
}

// The push is opt-in, only for the reasons it can clear, and only for the snapshot the owner
// confirmed; anything that would refuse the offer anyway is refused before the push, so such a
// click publishes nothing.
func TestHandoffPushOnlyWhenItCanLeadToAnOffer(t *testing.T) {
	f := newHandoffLife(t)
	var seen []*http.Request
	var bodies [][]byte
	f.pushClears(&seen, &bodies)

	f.blockedForPush("no_upstream")
	f.requireConflict(f.offer(nil), "handoff_blocked", "") // not asked for
	if rec := f.offer(map[string]any{"pushFirst": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("pushFirst without the confirmed snapshot = %d %s, want 400", rec.Code, rec.Body)
	}
	moved := confirmedPush()
	moved["pushExpect"].(map[string]any)["headSha"] = "0123456789abcdef"
	f.requireConflict(f.offer(moved), "handoff_changed", "")
	elsewhere := confirmedPush()
	elsewhere["pushExpect"].(map[string]any)["pushRemote"] = "https://example.com/other.git"
	f.requireConflict(f.offer(elsewhere), "handoff_changed", "")
	if f.pushHits() != 0 {
		t.Fatalf("push hits = %d for a snapshot that no longer matches, want 0", f.pushHits())
	}

	f.setAgent(map[string]any{"blocked": "detached_head", "pushRemote": nil})
	f.requireConflict(f.offer(withAck(confirmedPush())), "handoff_blocked", "")

	f.blockedForPush("no_upstream")
	f.setAgent(map[string]any{"warning": "uncommitted_changes"})
	f.requireConflict(f.offer(confirmedPush()), "handoff_warning", "")
	if f.pushHits() != 0 {
		t.Fatalf("push hits = %d before any offer could be sent, want 0", f.pushHits())
	}

	f.setAgent(map[string]any{"blocked": nil, "warning": nil, "pushRemote": nil})
	f.mustOffer(nil)
	f.blockedForPush("unpushed_commits")
	f.requireConflict(f.offer(confirmedPush()), "handoff_already_pending", "")
	if f.pushHits() != 0 {
		t.Fatalf("push hits = %d with an offer already pending, want 0", f.pushHits())
	}
}

func withAck(m map[string]any) map[string]any {
	m["ackWarning"] = true
	return m
}

// After the push the verdict is the Agent's again, and it must still describe the confirmed
// commit: a gate that still blocks, or a state that moved to another HEAD or remote (a hook
// checked out another, already-pushed branch), stops the offer.
func TestHandoffPushAfterwardsMustStillMatch(t *testing.T) {
	cases := map[string]struct {
		after map[string]any
		code  string
	}{
		"still blocked":  {map[string]any{"blocked": "unpushed_commits"}, "handoff_blocked"},
		"another head":   {map[string]any{"headSha": "0123456789abcdef"}, "handoff_changed"},
		"another branch": {map[string]any{"branch": "already-pushed"}, "handoff_changed"},
		"another remote": {map[string]any{"remote": "https://example.com/other.git"}, "handoff_changed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newHandoffLife(t)
			f.blockedForPush("unpushed_commits")
			f.push = func(*http.Request, []byte) (int, map[string]any) {
				delete(f.agentCtx, "blocked")
				for k, v := range c.after {
					f.agentCtx[k] = v
				}
				return http.StatusOK, map[string]any{"pushed": true}
			}
			f.requireConflict(f.offer(confirmedPush()), c.code, "")
			if n := f.offerRows(); n != 0 || f.pushHits() != 1 {
				t.Fatalf("rows=%d pushHits=%d, want 0 and 1", n, f.pushHits())
			}
		})
	}
}

// The gate opened by itself after the modal was shown (another client pushed) and the branch then
// moved: "push, then send" was confirmed for one commit, so no offer names another.
func TestHandoffPushFirstOnAnOpenGateStillMatchesTheSnapshot(t *testing.T) {
	f := newHandoffLife(t)
	f.setAgent(map[string]any{"headSha": "0123456789abcdef"})
	f.requireConflict(f.offer(confirmedPush()), "handoff_changed", "")
	if n := f.offerRows(); n != 0 || f.pushHits() != 0 {
		t.Fatalf("rows=%d pushHits=%d, want 0 and 0", n, f.pushHits())
	}
	f.setAgent(map[string]any{"headSha": confirmedHead})
	if o := f.mustOffer(confirmedPush()); o.HeadSha != confirmedHead || f.pushHits() != 0 {
		t.Fatalf("matching open gate: offer %+v, pushHits=%d", o, f.pushHits())
	}
}

// An Agent without the route answers 404 too; that is a refused push, not "no such session".
func TestHandoffPushOnAnAgentWithoutTheRoute(t *testing.T) {
	f := newHandoffLife(t)
	f.blockedForPush("unpushed_commits")
	rec := f.offer(confirmedPush()) // f.push is nil: the stub answers 404 with no Agent code
	f.requireConflict(rec, "handoff_push_failed", "")
	if n := f.offerRows(); n != 0 {
		t.Fatalf("%d offers stored", n)
	}
}
