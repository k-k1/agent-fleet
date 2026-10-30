package memreview

// Releasing the rejected hashes of a forgotten memory (ADR 0117 decision 5, owner answer 6): a
// rejection is remembered only as long as the memory it concerned. The forget happens in the memory
// store (memoryx), which tells this package twice: before it commits, and after. The first call
// writes down that a release is owed (an intent), the second does it and strikes the intent. A
// release that fails, or a stop between the commit and the second call, leaves the intent, and
// ReconcileReleases (every wake-up of the loop and at start) finishes it. Whether the forget behind
// an intent committed is read from the memory's history, newer than the change the forget began
// from (never from whether the memory exists, which an explicit save may have changed); a forget
// that did not commit owes nothing and its intent is dropped, and an answer the history cannot give
// leaves the intent in place.
//
// An intent carries the time it was written, and only rejections made up to then are released: one
// made after the forget (a review proposing the forgotten memory again, rejected by the member) is
// about the memory as it is now and stays.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

type releaseIntent struct {
	Scope     string `json:"scope"`
	Name      string `json:"name"`
	ProjectID string `json:"projectId,omitempty"`
	// Base is the memory's newest change when the forget began. It tells this forget from any other
	// of the same name: one intent per forget, so a second attempt cannot overwrite what the first
	// still owes, and memoryx.ReviewForgetCommitted can say whether this forget committed.
	Base string    `json:"base,omitempty"`
	At   time.Time `json:"at"`
}

var releaseStore = store[releaseIntent]{"releasing"}

func releaseKey(scope, projectID, name, base string) string {
	h := sha256.Sum256([]byte(scope + "\x00" + projectID + "\x00" + name + "\x00" + base))
	return hex.EncodeToString(h[:])[:24]
}

// ForgetHooks are the hooks package main installs on memoryx.
func ForgetHooks() *memoryx.ForgetHooks {
	return &memoryx.ForgetHooks{Intent: noteForgetIntent, Done: noteForgotten}
}

func hasRejectionsLocked(scope, projectID, name string) bool {
	for _, h := range rejectedStore.keys() {
		if r, ok := rejectedStore.Read(h); ok && r.Scope == scope && r.Name == name && r.ProjectID == projectID {
			return true
		}
	}
	return false
}

// errIntent is why a forget is refused: the owed release could not be made durable. Nothing in it
// names the memory.
var errIntent = errors.New("could not record a release of rejected proposals")

// noteForgetIntent runs before a forget commits. With nothing remembered about the memory nothing is
// owed and nothing is written, so a forget does not depend on this package's directory. When
// something is owed and the note cannot be made durable the forget is refused (memoryx): a commit
// that nothing could recover the release from is worse than a forget that has to be tried again.
func noteForgetIntent(scope, projectID, name, base string) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	if !hasRejectionsLocked(scope, projectID, name) {
		return nil
	}
	in := releaseIntent{Scope: scope, Name: name, ProjectID: projectID, Base: base, At: time.Now()}
	if err := releaseStore.Write(releaseKey(scope, projectID, name, base), in); err != nil {
		log.Printf("memory review: could not record a release of rejected proposals")
		return errIntent
	}
	return nil
}

// noteForgotten runs after a forget committed: it releases what this forget's intent covers
// (everything remembered so far when there is none) and strikes the intent only when that worked.
func noteForgotten(scope, projectID, name, base string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	key := releaseKey(scope, projectID, name, base)
	in, owed := releaseStore.Read(key)
	if !owed {
		in = releaseIntent{Scope: scope, Name: name, ProjectID: projectID} // zero At: every rejection
	}
	finishLocked(key, in, owed)
}

func finishLocked(key string, in releaseIntent, owed bool) {
	if _, err := releaseLocked(in.Scope, in.Name, in.ProjectID, in.At); err != nil {
		log.Printf("memory review: releasing rejected proposals failed; it is retried")
		return
	}
	if owed {
		if err := releaseStore.Remove(key); err != nil {
			log.Printf("memory review: could not clear a finished release")
		}
	}
}

// ReconcileReleases finishes the releases that are still owed. Whether each forget committed is
// asked of the memory's history (memoryx.ReviewForgetCommitted), not of whether the memory exists:
// an explicit save may have re-created it since. A forget that never committed leaves nothing owed.
func ReconcileReleases() {
	type owedIntent struct {
		key string
		in  releaseIntent
	}
	var owed []owedIntent
	stateMu.Lock()
	for _, k := range releaseStore.keys() {
		if in, ok := releaseStore.Read(k); ok {
			owed = append(owed, owedIntent{k, in})
		}
	}
	stateMu.Unlock()
	for _, o := range owed {
		committed, err := memoryx.ReviewForgetCommitted(o.in.Scope, o.in.ProjectID, o.in.Name, o.in.Base)
		if err != nil {
			continue // cannot tell; the intent stays and is visible in the status
		}
		settleIntent(o.key, o.in, committed)
	}
}

// settleIntent acts on the snapshot in only if the stored intent is still that one: the lock was
// let go while the history was read, and a newer intent under the same key (a retried forget) is not
// this snapshot's to remove.
func settleIntent(key string, in releaseIntent, committed bool) {
	stateMu.Lock()
	defer stateMu.Unlock()
	cur, ok := releaseStore.Read(key)
	if !ok || !cur.At.Equal(in.At) {
		return
	}
	if !committed {
		if err := releaseStore.Remove(key); err != nil {
			log.Printf("memory review: could not drop an obsolete release")
		}
		return
	}
	finishLocked(key, in, true)
}

// ReleasePending is how many releases are owed and not done: a failure the member can see.
func ReleasePending() int {
	stateMu.Lock()
	defer stateMu.Unlock()
	return len(releaseStore.keys())
}
