package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Per-membership internal git token rotation (issue #1199, docs/build/91-internal-git.md
// §91.5). The token is a deterministic HMAC of (membership id, epoch). Rotating bumps the
// epoch, which the git face reads live on every request, so the old token is refused from
// the next request on, and the new one is pushed to the member's running workspace so it
// keeps working without a restart.

// Outcomes of the push to the running workspace, as the admin API reports them.
const (
	gitTokenPushUpdated    = "updated"     // the running Agent stored the new token
	gitTokenPushNotRunning = "not_running" // nothing to push to: the next start injects it
	gitTokenPushDisabled   = "disabled"    // internal git is off (no PUBLIC_BASE_URL)
	gitTokenPushPending    = "pending"     // a start is in flight: pushed when it finishes
	gitTokenPushFailed     = "failed"      // the Agent did not take it: restart the workspace
)

// gitTokenPushLockWait bounds how long the admin request waits for a start of the same
// workspace to finish before answering "pending" and pushing in the background.
var gitTokenPushLockWait = 15 * time.Second

var gitTokenPushClient = &http.Client{Timeout: 10 * time.Second, Transport: newAgentTransport()}

var errGitTokenNoMembership = errors.New("membership not active")

// agentGitTokenPush is the body of the Agent's PUT /internal-git/token.
type agentGitTokenPush struct {
	Token string `json:"token"`
	Epoch int64  `json:"epoch"`
}

// currentGitToken mints the membership's git token at the epoch the store holds now.
func (m *manager) currentGitToken(ctx context.Context, membershipID string) (string, int64, error) {
	epoch, ok, err := m.store.GitTokenEpoch(ctx, membershipID)
	if err != nil {
		return "", 0, err
	}
	if !ok {
		return "", 0, errGitTokenNoMembership
	}
	return mintGitToken(gitSignKey(m.tokenSignMaster()), membershipID, epoch), epoch, nil
}

// gitEpochOfEnv is the epoch of the internal git token env actually carries, for the memo.
// It is read from the env itself, not looked up beside it: when the injection was skipped
// (the epoch read failed) the answer is -1, which never equals a live epoch, so the next
// start rebuilds the runtime instead of starting without a token for good.
func (m *manager) gitEpochOfEnv(membershipID string, env []string) int64 {
	if m.internalGitHost == "" || membershipID == "" {
		return 0
	}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "AF_INTERNAL_GIT_EPOCH="); ok {
			if e, err := strconv.ParseInt(v, 10, 64); err == nil {
				return e
			}
		}
	}
	return -1
}

// rotateGitToken bumps the membership's git token epoch and hands the new token to its
// running workspace. found=false when there is no such membership. The epoch bump is the
// rotation: a failed push leaves the old token dead and the workspace without the new one
// until its next start, which the outcome says.
func (m *manager) rotateGitToken(ctx context.Context, membershipID string) (epoch int64, push string, found bool, err error) {
	epoch, found, err = m.store.BumpGitTokenEpoch(ctx, membershipID)
	if err != nil || !found {
		return 0, "", found, err
	}
	// The memoized runtime carries the start env, old token included: without this the
	// next start would inject the token that was just killed.
	m.evictMembershipCache(membershipID)
	if m.internalGitHost == "" {
		return epoch, gitTokenPushDisabled, true, nil
	}
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil {
		log.Printf("internal git: rotated token for %s not pushed: %v", membershipID, err)
		return epoch, gitTokenPushFailed, true, nil
	}
	if !ok {
		return epoch, gitTokenPushNotRunning, true, nil
	}
	// The push holds the workspace's start lock, so a start already in flight finishes
	// first and is then pushed to, instead of coming up after the push with the old
	// token in its env.
	lock := m.startLockFor(ws.ID)
	locked := make(chan struct{})
	go func() { lock.Lock(); close(locked) }()
	doPush := func() string {
		defer lock.Unlock()
		return m.pushGitToken(context.WithoutCancel(ctx), ws.ID, membershipID)
	}
	select {
	case <-locked:
		return epoch, doPush(), true, nil
	case <-time.After(gitTokenPushLockWait):
		go func() {
			<-locked
			log.Printf("internal git: rotated token for %s pushed after a start: %s", membershipID, doPush())
		}()
		return epoch, gitTokenPushPending, true, nil
	}
}

// pushGitToken sends the membership's current token to its workspace's Agent. It re-reads
// the workspace and the token: by the time a start lock was free either may have moved.
func (m *manager) pushGitToken(ctx context.Context, wsID, membershipID string) string {
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, membershipID)
	if err != nil || !ok || ws.ID != wsID {
		return gitTokenPushNotRunning
	}
	if ws.State != "running" {
		return gitTokenPushNotRunning
	}
	token, epoch, err := m.currentGitToken(ctx, membershipID)
	if err != nil {
		log.Printf("internal git: rotated token for %s not pushed: %v", membershipID, err)
		return gitTokenPushFailed
	}
	rt := m.runtimeFor(ws, noSecretKeys)
	if rt == nil || rt.Endpoint() == "" {
		return gitTokenPushFailed
	}
	if err := putAgentGitToken(ctx, rt.Endpoint(), rt.Token(), token, epoch); err != nil {
		log.Printf("internal git: rotated token for %s not pushed to ws %s: %v", membershipID, ws.ID, err)
		return gitTokenPushFailed
	}
	return gitTokenPushUpdated
}

// putAgentGitToken is a package var so tests can stand in for the Agent. The epoch rides
// along so the Agent keeps the later token when two replicas' pushes cross; an answer of
// "superseded" (it already holds a later one) is success for this caller.
var putAgentGitToken = func(ctx context.Context, endpoint, agentToken, gitToken string, epoch int64) error {
	body, _ := json.Marshal(agentGitTokenPush{Token: gitToken, Epoch: epoch})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint+"/internal-git/token", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if agentToken != "" {
		req.Header.Set("Authorization", "Bearer "+agentToken)
	}
	resp, err := gitTokenPushClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		// 404 is an Agent older than the push: it takes the token at its next start.
		return fmt.Errorf("agent answered %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
