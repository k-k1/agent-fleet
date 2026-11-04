package sessionx

import (
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// The list answers before its background refreshes finish; WaitListBackground is what lets a
// test hold off its TempDir cleanup until they have (#3261). Each refresh is parked on a
// channel here, so the assertion does not depend on timing: without the wait the refresh is
// still running when the list returns, which is exactly what raced RemoveAll.
func TestWaitListBackgroundCoversJiraRefreshAndOverviewFacts(t *testing.T) {
	release := make(chan struct{})
	var done sync.WaitGroup
	done.Add(2)

	oldJira, oldRead := jiraProjectsOf, overviewFactsRead
	oldCache := jiraProjects
	t.Cleanup(func() {
		jiraProjectsOf, overviewFactsRead, jiraProjects = oldJira, oldRead, oldCache
		overviewFactsMu.Lock()
		delete(overviewFactsCache, "bg-facts")
		overviewFactsMu.Unlock()
	})
	jiraProjects = &jiraProjectCache{
		read: func() ([]string, bool, error) {
			<-release
			done.Done()
			return nil, false, nil
		},
		async: func(f func()) { go f() },
		now:   time.Now,
	}
	jiraProjectsOf = jiraProjects.known
	overviewFactsRead = func(session.Meta) ([]transcript.Turn, bool) {
		<-release
		done.Done()
		return nil, false
	}

	jiraProjectsOf(time.Now())
	overviewFactsFor(session.Meta{Name: "bg-facts", Kind: session.KindCodex}, true)

	waited := make(chan struct{})
	go func() { WaitListBackground(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("WaitListBackground returned while a refresh was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitListBackground did not return after the refreshes finished")
	}
	// Both refreshes ran to completion before the wait returned.
	done.Wait()
}
