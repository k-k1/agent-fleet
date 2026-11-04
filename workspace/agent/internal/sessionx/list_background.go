package sessionx

// WaitListBackground blocks until the work a session list started and left running after its
// response has finished: the Jira project-key refresh (it creates the credential store's
// directory and lock file under HOME) and the overview-facts transcript reads.
//
// Production never calls it — the list must not wait for them. Tests that serve the list from a
// t.TempDir HOME call it before the directory is removed: a refresh still writing there makes
// the RemoveAll cleanup fail with "directory not empty" (#3261). Call it only when no list
// request is in flight, since a refresh a concurrent request starts can race the wait itself.
func WaitListBackground() {
	jiraProjects.bg.Wait()
	overviewFactsBG.Wait()
}
