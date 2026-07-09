package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// addMember creates an identity + membership in the default tenant and returns a
// git token for it, so lock tests can exercise multiple owners/roles.
func (e *lfsEnv) addMember(t *testing.T, email, key, role string) string {
	t.Helper()
	ctx := context.Background()
	id, err := e.st.UpsertIdentity(ctx, email, key, "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mem, err := e.st.EnsureMembership(ctx, id.ID, e.tenantID(), role)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	return mintGitToken(gitSignKey(e.g.mgr.master32), mem.ID)
}

func (e *lfsEnv) lockCall(h http.HandlerFunc, method, url, token string, body any, pv map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, url, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, url, nil)
	}
	r.SetBasicAuth("x-access-token", token)
	pv["slug"] = "default"
	pv["repo"] = "shared.git"
	for k, v := range pv {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

const locksURL = "/git/default/shared.git/info/lfs/locks"

func TestLFSLockLifecycle(t *testing.T) {
	e := newLFSEnv(t) // e.token is member "u-x"
	path := "art/hero.psd"

	// Create.
	w := e.lockCall(e.g.lfsLockCreate, "POST", locksURL, e.token, map[string]any{"path": path, "ref": map[string]any{"name": "refs/heads/main"}}, map[string]string{})
	if w.Code != 201 {
		t.Fatalf("create: want 201 got %d (%s)", w.Code, w.Body.String())
	}
	var created struct {
		Lock struct {
			ID    string `json:"id"`
			Path  string `json:"path"`
			Owner struct {
				Name string `json:"name"`
			} `json:"owner"`
		} `json:"lock"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.Lock.ID == "" || created.Lock.Path != path {
		t.Fatalf("bad create body: %s", w.Body.String())
	}
	if created.Lock.Owner.Name != "u@x" {
		t.Fatalf("owner name = %q, want u@x", created.Lock.Owner.Name)
	}
	lockID := created.Lock.ID

	// Re-lock same path → 409 with the existing lock.
	w = e.lockCall(e.g.lfsLockCreate, "POST", locksURL, e.token, map[string]any{"path": path}, map[string]string{})
	if w.Code != 409 {
		t.Fatalf("relock: want 409 got %d", w.Code)
	}
	var conflict map[string]any
	json.Unmarshal(w.Body.Bytes(), &conflict)
	if conflict["message"] != "already created lock" || conflict["lock"] == nil {
		t.Fatalf("409 body missing lock/message: %s", w.Body.String())
	}

	// List shows it.
	w = e.lockCall(e.g.lfsLocksList, "GET", locksURL, e.token, nil, map[string]string{})
	if w.Code != 200 {
		t.Fatalf("list: want 200 got %d", w.Code)
	}
	var listed struct {
		Locks []struct {
			ID string `json:"id"`
		} `json:"locks"`
		NextCursor string `json:"next_cursor"`
	}
	json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Locks) != 1 || listed.Locks[0].ID != lockID {
		t.Fatalf("list: want [%s], got %s", lockID, w.Body.String())
	}

	// Verify as owner → ours.
	w = e.lockCall(e.g.lfsLocksVerify, "POST", locksURL+"/verify", e.token, map[string]any{}, map[string]string{})
	var vOwner struct {
		Ours   []map[string]any `json:"ours"`
		Theirs []map[string]any `json:"theirs"`
	}
	json.Unmarshal(w.Body.Bytes(), &vOwner)
	if len(vOwner.Ours) != 1 || len(vOwner.Theirs) != 0 {
		t.Fatalf("verify owner: ours=%d theirs=%d", len(vOwner.Ours), len(vOwner.Theirs))
	}

	// Verify as a different member → theirs.
	bob := e.addMember(t, "bob@x", "bob-x", "member")
	w = e.lockCall(e.g.lfsLocksVerify, "POST", locksURL+"/verify", bob, map[string]any{}, map[string]string{})
	var vBob struct {
		Ours   []map[string]any `json:"ours"`
		Theirs []map[string]any `json:"theirs"`
	}
	json.Unmarshal(w.Body.Bytes(), &vBob)
	if len(vBob.Ours) != 0 || len(vBob.Theirs) != 1 {
		t.Fatalf("verify bob: ours=%d theirs=%d", len(vBob.Ours), len(vBob.Theirs))
	}

	// Bob cannot unlock someone else's lock without force.
	unlockURL := locksURL + "/" + lockID + "/unlock"
	w = e.lockCall(e.g.lfsUnlock, "POST", unlockURL, bob, map[string]any{}, map[string]string{"id": lockID})
	if w.Code != 403 {
		t.Fatalf("bob unlock: want 403 got %d", w.Code)
	}
	// force by a non-admin is still refused.
	w = e.lockCall(e.g.lfsUnlock, "POST", unlockURL, bob, map[string]any{"force": true}, map[string]string{"id": lockID})
	if w.Code != 403 {
		t.Fatalf("bob force unlock (non-admin): want 403 got %d", w.Code)
	}

	// A tenant_admin may force-unlock an abandoned lock.
	admin := e.addMember(t, "admin@x", "admin-x", "tenant_admin")
	w = e.lockCall(e.g.lfsUnlock, "POST", unlockURL, admin, map[string]any{"force": true}, map[string]string{"id": lockID})
	if w.Code != 200 {
		t.Fatalf("admin force unlock: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	// Gone now.
	w = e.lockCall(e.g.lfsLocksList, "GET", locksURL, e.token, nil, map[string]string{})
	json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Locks) != 0 {
		t.Fatalf("after unlock: want 0 locks, got %d", len(listed.Locks))
	}
}

func TestLFSLockOwnerUnlockAndReadOnly(t *testing.T) {
	e := newLFSEnv(t)

	// Owner unlocks their own lock.
	w := e.lockCall(e.g.lfsLockCreate, "POST", locksURL, e.token, map[string]any{"path": "a.bin"}, map[string]string{})
	var created struct {
		Lock struct {
			ID string `json:"id"`
		} `json:"lock"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	w = e.lockCall(e.g.lfsUnlock, "POST", locksURL+"/"+created.Lock.ID+"/unlock", e.token, map[string]any{}, map[string]string{"id": created.Lock.ID})
	if w.Code != 200 {
		t.Fatalf("owner unlock: want 200 got %d", w.Code)
	}

	// A read-only role (viewer) can neither create nor unlock.
	viewer := e.addMember(t, "v@x", "v-x", "viewer")
	w = e.lockCall(e.g.lfsLockCreate, "POST", locksURL, viewer, map[string]any{"path": "b.bin"}, map[string]string{})
	if w.Code != 403 {
		t.Fatalf("viewer create: want 403 got %d", w.Code)
	}
	// But a viewer CAN list/verify (read).
	w = e.lockCall(e.g.lfsLocksList, "GET", locksURL, viewer, nil, map[string]string{})
	if w.Code != 200 {
		t.Fatalf("viewer list: want 200 got %d", w.Code)
	}

	// Unlock of a missing lock → 404.
	w = e.lockCall(e.g.lfsUnlock, "POST", locksURL+"/nope/unlock", e.token, map[string]any{}, map[string]string{"id": "nope"})
	if w.Code != 404 {
		t.Fatalf("unlock missing: want 404 got %d", w.Code)
	}
}
