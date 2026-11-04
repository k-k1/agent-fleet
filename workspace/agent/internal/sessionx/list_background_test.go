package sessionx

import (
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// The list answers before its background refreshes finish; WaitListBackground is what lets a
// test hold off its TempDir cleanup until they have. Each refresh is parked on its own
// channel, so the assertions do not depend on timing and drop either Wait call to see one
// fail: the wait must cover both refreshes, whichever finishes first.
func TestWaitListBackgroundCoversJiraRefreshAndOverviewFacts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first, rest string // the refresh released first, and the one still parked
	}{
		{"jira first", "jira", "overview"},
		{"overview first", "overview", "jira"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := map[string]chan struct{}{"jira": make(chan struct{}), "overview": make(chan struct{})}
			var mu sync.Mutex
			open := map[string]bool{"jira": true, "overview": true}
			free := func(name string) { // idempotent: the cleanup frees whatever the test left parked
				mu.Lock()
				defer mu.Unlock()
				if open[name] {
					open[name] = false
					close(release[name])
				}
			}

			oldJira, oldRead, oldCache := jiraProjectsOf, overviewFactsRead, jiraProjects
			t.Cleanup(func() {
				free("jira") // a failed assertion must not leave a refresh parked past the globals' restore
				free("overview")
				WaitListBackground()
				jiraProjectsOf, overviewFactsRead, jiraProjects = oldJira, oldRead, oldCache
				overviewFactsMu.Lock()
				delete(overviewFactsCache, "bg-facts")
				overviewFactsMu.Unlock()
			})
			jiraProjects = &jiraProjectCache{
				read: func() ([]string, bool, error) {
					<-release["jira"]
					return nil, false, nil
				},
				async: func(f func()) { go f() },
				now:   time.Now,
			}
			jiraProjectsOf = jiraProjects.known
			overviewFactsRead = func(session.Meta) ([]transcript.Turn, bool) {
				<-release["overview"]
				return nil, false
			}

			jiraProjectsOf(time.Now())
			overviewFactsFor(session.Meta{Name: "bg-facts", Kind: session.KindCodex}, true)

			waited := make(chan struct{})
			go func() { WaitListBackground(); close(waited) }()
			closed := func(d time.Duration) bool {
				select {
				case <-waited:
					return true
				case <-time.After(d):
					return false
				}
			}
			if closed(100 * time.Millisecond) {
				t.Fatal("WaitListBackground returned with both refreshes parked")
			}
			free(tc.first)
			if closed(100 * time.Millisecond) {
				t.Fatalf("WaitListBackground returned while the %s refresh was still parked", tc.rest)
			}
			free(tc.rest)
			if !closed(5 * time.Second) {
				t.Fatal("WaitListBackground did not return after both refreshes finished")
			}
		})
	}
}
