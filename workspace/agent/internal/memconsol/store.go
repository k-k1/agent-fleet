package memconsol

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// Store is the memory store as the consolidator sees it. Production is memoryx; the seam exists so
// the adapter's rules (ripeness, budget, validation, the queue, approval) are tested against a
// fake store, while memoryx's own tests prove the store half (authorship, revert, no revival).
// Everything but Publish only reads, and Publish is called from Approve alone.
type Store interface {
	Scopes() ([]memoryx.ConsolScope, error)
	Memories(scope, projectID string) ([]memoryx.ConsolMemory, error)
	Target(scope, projectID, name string) (memoryx.ReviewTargetState, error)
	Publish(proposalID string, it memoryx.ConsolItem, now time.Time) (memoryx.ConsolPublished, error)
	// Published is the commit that already published the item with this id, "" if none did.
	Published(publishID string) (string, error)
	Validate(scope, name, description, memType, body string) error
}

type memoryxStore struct{}

func (memoryxStore) Scopes() ([]memoryx.ConsolScope, error) { return memoryx.ConsolScopes() }
func (memoryxStore) Memories(scope, projectID string) ([]memoryx.ConsolMemory, error) {
	return memoryx.ConsolMemories(scope, projectID)
}
func (memoryxStore) Target(scope, projectID, name string) (memoryx.ReviewTargetState, error) {
	return memoryx.ReviewTarget(scope, projectID, name)
}
func (memoryxStore) Publish(id string, it memoryx.ConsolItem, now time.Time) (memoryx.ConsolPublished, error) {
	return memoryx.PublishConsolidation(id, it, now)
}
func (memoryxStore) Published(id string) (string, error) { return memoryx.ConsolCommitOf(id) }
func (memoryxStore) Validate(scope, name, description, memType, body string) error {
	return memoryx.ValidateReviewMemory(scope, name, description, memType, nil, body)
}

// store is replaced by tests only.
var store Store = memoryxStore{}
