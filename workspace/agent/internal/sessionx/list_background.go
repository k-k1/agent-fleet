package sessionx

// WaitListBackground blocks until two pieces of work a session list started and left running
// after its response have finished: the Jira project-key refresh (it creates the credential
// store's directory and lock file under HOME) and the overview-facts transcript reads. It does
// not cover the GitHub/Bitbucket PR caches, which also write under HOME through secrets.Load;
// the tests that call it list sessions without a remote, so that path never starts.
//
// Production never calls it — the list must not wait for them. Tests that serve the list from a
// t.TempDir HOME call it before the directory is removed: a refresh still writing there makes
// the RemoveAll cleanup fail with "directory not empty". Call it only when no list
// request is in flight, since a refresh a concurrent request starts can race the wait itself.
func WaitListBackground() {
	jiraProjects.bg.Wait()
	overviewFactsBG.Wait()
}
