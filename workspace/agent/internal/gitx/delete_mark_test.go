package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The overlap rules of BeginDelete (#4833): the same copy, a worktree and its base clone never
// run together; unrelated copies do, and a mark is gone once its delete ends.
func TestBeginDeleteRules(t *testing.T) {
	endA, ok, _ := BeginDelete("/r/app@a", "/r/app")
	if !ok {
		t.Fatal("first delete refused")
	}
	if _, ok, _ := BeginDelete("/r/app@a", "/r/app"); ok {
		t.Fatal("the same copy was marked twice")
	}
	endB, ok, _ := BeginDelete("/r/app@b", "/r/app")
	if !ok {
		t.Fatal("a sibling worktree was refused")
	}
	if _, ok, _ := BeginDelete("/r/app", ""); ok {
		t.Fatal("the base clone was marked while its worktrees were being deleted")
	}
	if !UnderDeletion("/r/app@a") || !UnderDeletion("/r/app@a/sub") || UnderDeletion("/r/app@a2") || UnderDeletion("/r/app") {
		t.Fatal("UnderDeletion does not follow the marks")
	}
	endA()
	endA() // idempotent
	endB()
	endBase, ok, _ := BeginDelete("/r/app", "")
	if !ok {
		t.Fatal("the base clone is still refused after its worktrees' deletes ended")
	}
	if _, ok, _ := BeginDelete("/r/app@c", "/r/app"); ok {
		t.Fatal("a worktree was marked while its base clone was being deleted")
	}
	endBase()
	if UnderDeletion("/r/app") {
		t.Fatal("mark left behind")
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// Removes of different worktrees of one base clone hold the parent lock together (read side),
// and prune waits for every one of them (write side). The hook fires after the read lock is taken,
// so a remove that took the write side instead would never let the second one arrive.
func TestRemoveWorktreeSharesTheParentLockAndPruneWaits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "app")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, parent, "init", "-q", "-b", "main")
	gitT(t, parent, "commit", "-q", "--allow-empty", "-m", "i")
	var wts []string
	for _, n := range []string{"a", "b"} {
		wt := filepath.Join(root, "app@"+n)
		gitT(t, parent, "worktree", "add", "-q", "-b", n, wt, "main")
		wts = append(wts, wt)
	}
	arrived, release, pruneStarted := make(chan string, 2), make(chan struct{}), make(chan struct{}, 8)
	t.Cleanup(SetDeleteStepHook(func(step, dir string) {
		switch step {
		case "git-remove": // fired once the read lock is held
			arrived <- dir
			<-release
		case "prune": // fired once the write lock is held, before git runs
			pruneStarted <- struct{}{}
		}
	}))
	errs := make(chan error, 2)
	for _, wt := range wts {
		go func() { _, err := removeWorktree(parent, wt); errs <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatalf("only %d removes held the parent lock together", i)
		}
	}
	pruneDone := make(chan struct{})
	go func() { pruneWorktrees(parent); close(pruneDone) }()
	select {
	case <-pruneStarted:
		close(release)
		t.Fatal("prune took the write lock while removes were in flight")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case <-pruneStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("prune never ran after the removes ended")
	}
	select {
	case <-pruneDone:
	case <-time.After(10 * time.Second):
		t.Fatal("prune never finished")
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("remove: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a remove never finished")
		}
	}
}

// A path that reaches a marked copy through a symlink, or that is gone already, is the same copy.
func TestMarksFollowSymlinksAndSurviveTheFolderGoing(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "app@one")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	end, ok, _ := BeginDelete(alias, "")
	if !ok {
		t.Fatal("refused")
	}
	if _, ok, _ := BeginDelete(real, ""); ok {
		t.Fatal("the same copy was marked again through its real path")
	}
	if !UnderDeletion(filepath.Join(alias, "sub")) || !UnderDeletion(real) {
		t.Fatal("a path through the alias does not meet the mark")
	}
	if err := os.RemoveAll(real); err != nil {
		t.Fatal(err)
	}
	if !UnderDeletion(real) || !UnderDeletion(filepath.Join(real, "sub")) {
		t.Fatal("the mark is lost once the folder is gone")
	}
	end()
	if UnderDeletion(real) {
		t.Fatal("mark left behind")
	}
}

// A session's stored Dir is matched against the delete's path both as stored and cleaned: `..`
// after a symlink can clean to outside the copy while the OS resolves it inside, and a Dir such as
// `/r/./app@a/sub` reaches the copy only once cleaned.
func TestSessionUnderDeletionMatchesTheStoredDir(t *testing.T) {
	end, ok, _ := BeginDelete("/r/app@a", "/r/app")
	if !ok {
		t.Fatal("delete refused")
	}
	defer end()
	for _, dir := range []string{"/r/app@a", "/r/app@a/sub", "/r/app@a/jump/../../kept", "/r/app@a/sub/../x", "/r/app@a//sub", "/r/./app@a/sub"} {
		if !SessionUnderDeletion("other", dir) {
			t.Errorf("SessionUnderDeletion(%q) = false, want true", dir)
		}
	}
	for _, dir := range []string{"", "/r/app", "/r/app@a2", "/r/kept"} {
		if SessionUnderDeletion("other", dir) {
			t.Errorf("SessionUnderDeletion(%q) = true, want false", dir)
		}
	}
}

// A launch's reservation is seen by a delete of its folder or of any folder above it, by the
// spelling it was named with and by its real path, and not by a sibling's; a launch into a copy
// being deleted is refused, and a released reservation is gone.
func TestLaunchReservationsMeetDeletes(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "notes")
	if err := os.MkdirAll(filepath.Join(space, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(space, link); err != nil {
		t.Fatal(err)
	}
	// Meta.Dir at home, the cwd inside the space through a symlink.
	release, ok := ReserveLaunch(root, filepath.Join(link, "sub"))
	if !ok {
		t.Fatal("a launch with no delete in flight was refused")
	}
	if !LaunchingUnder(space) || !LaunchingUnder(link) || !LaunchingUnder(filepath.Join(space, "sub")) {
		t.Fatal("the delete of the folder the launch runs in does not see the launch")
	}
	if LaunchingUnder(filepath.Join(root, "spaces", "notes2")) {
		t.Fatal("a sibling folder's delete sees the launch")
	}
	release()
	release() // idempotent
	if LaunchingUnder(space) {
		t.Fatal("a released reservation still holds the folder")
	}

	end, ok, _ := BeginDelete(link, "")
	if !ok {
		t.Fatal("BeginDelete refused")
	}
	if _, ok := ReserveLaunch(filepath.Join(space, "sub")); ok {
		t.Fatal("a launch into a folder being deleted was reserved")
	}
	if r, ok := ReserveLaunch(filepath.Join(root, "elsewhere")); !ok {
		t.Fatal("a launch elsewhere was refused")
	} else {
		r()
	}
	end()
	if r, ok := ReserveLaunch(filepath.Join(space, "sub")); !ok {
		t.Fatal("a launch was refused after the delete ended")
	} else {
		r()
	}
}

// A `..` after a symlink leaves the link's target, the way the kernel enters the cwd, not the
// folder the link sits in (which a lexical clean would give). Both a launch reserved through such
// a path and a launch made while the copy is marked are matched where the kernel puts them.
func TestLaunchReservationsFollowALinkBeforeDotDot(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "repos", "app")
	for _, d := range []string{filepath.Join(app, "a", "b"), filepath.Join(app, "kept"), filepath.Join(root, "alias")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(app, "a", "b"), filepath.Join(root, "alias", "jump")); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "alias", "jump") + "/../../kept" // the kernel's <app>/kept
	release, ok := ReserveLaunch(cwd)
	if !ok {
		t.Fatal("reservation refused")
	}
	if !LaunchingUnder(app) {
		t.Error("a delete of the copy does not see a launch whose cwd reaches it through link/..")
	}
	release()
	end, ok, _ := BeginDelete(app, "")
	if !ok {
		t.Fatal("BeginDelete refused")
	}
	defer end()
	if r, ok := ReserveLaunch(cwd); ok {
		r()
		t.Error("a launch into a marked copy through link/.. was reserved")
	}
}

// A path that resolves is not also matched by its lexical clean, which names another folder when
// a link precedes `..`; and a delete of a folder the spelling passes through meets the launch even
// when the kernel ends up elsewhere, since the launch enters the cwd as spelled.
func TestLaunchReservationsMatchSpellingAndKernelNotTheClean(t *testing.T) {
	root := t.TempDir()
	repos, other := filepath.Join(root, "repos"), filepath.Join(root, "other")
	for _, d := range []string{filepath.Join(repos, "app"), filepath.Join(other, "nested"), filepath.Join(other, "app"), filepath.Join(other, "a", "b"), filepath.Join(other, "kept")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(other, "nested"), filepath.Join(repos, "jump")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "a", "b"), filepath.Join(repos, "app", "jump")); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(repos, "app")

	// <repos>/jump/../app is <other>/app to the kernel; its clean, <repos>/app, is not where it runs.
	elsewhere := filepath.Join(repos, "jump") + "/../app"
	end, ok, _ := BeginDelete(app, "")
	if !ok {
		t.Fatal("BeginDelete refused")
	}
	r, ok := ReserveLaunch(elsewhere)
	if !ok {
		end()
		t.Fatal("a launch that runs elsewhere was refused by the clean of its path")
	}
	end()
	if LaunchingUnder(app) {
		t.Error("a delete of the clean of the path sees a launch that runs elsewhere")
	}
	r()

	// <repos>/app/jump/../../kept runs in <other>/kept but passes through <repos>/app.
	through := filepath.Join(app, "jump") + "/../../kept"
	r, ok = ReserveLaunch(through)
	if !ok {
		t.Fatal("reservation refused")
	}
	if !LaunchingUnder(app) {
		t.Error("a delete of a folder the spelling passes through does not see the launch")
	}
	r()
	end, ok, _ = BeginDelete(app, "")
	if !ok {
		t.Fatal("BeginDelete refused")
	}
	defer end()
	if r, ok := ReserveLaunch(through); ok {
		r()
		t.Error("a launch whose spelling passes through a marked copy was reserved")
	}
}

// A link into a copy followed by `..` back out of it still needs the copy until the kernel has
// entered the cwd: the folders the walk passes through are held too.
func TestLaunchReservationsHoldTheFoldersPassedThrough(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "repos", "app")
	for _, d := range []string{filepath.Join(app, "sub"), filepath.Join(root, "repos", "kept")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(app, "sub"), filepath.Join(root, "jump")); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "jump") + "/../../kept" // through <app>/sub into <repos>/kept
	r, ok := ReserveLaunch(cwd)
	if !ok {
		t.Fatal("reservation refused")
	}
	if !LaunchingUnder(app) {
		t.Error("a delete of a copy the walk passes through does not see the launch")
	}
	r()
	end, ok, _ := BeginDelete(app, "")
	if !ok {
		t.Fatal("BeginDelete refused")
	}
	defer end()
	if r, ok := ReserveLaunch(cwd); ok {
		r()
		t.Error("a launch whose walk passes through a marked copy was reserved")
	}
}

// A delete that read the running sessions before a launch in its folder published and released
// reads them again; a release elsewhere does not make it, and a reservation still held refuses.
func TestMarkAgainstLaunches(t *testing.T) {
	root := t.TempDir()
	app, other := filepath.Join(root, "app"), filepath.Join(root, "other")
	var names []string

	epoch := LaunchEpoch()
	r, _ := ReserveLaunch(other)
	r()
	end, out, _ := MarkAgainstLaunches(app, "", epoch, names)
	if out != MarkTaken {
		t.Fatalf("a release elsewhere: outcome %d, want MarkTaken", out)
	}
	end()

	epoch = LaunchEpoch()
	r, _ = ReserveLaunch(filepath.Join(app, "sub"))
	if _, out, _ := MarkAgainstLaunches(app, "", epoch, names); out != MarkLaunching {
		t.Fatalf("a held reservation: outcome %d, want MarkLaunching", out)
	}
	r()
	if _, out, _ := MarkAgainstLaunches(app, "", epoch, names); out != MarkReread {
		t.Fatalf("a release in the folder since the read: outcome %d, want MarkReread", out)
	}
	if end, out, _ := MarkAgainstLaunches(app, "", LaunchEpoch(), names); out != MarkTaken {
		t.Fatalf("after reading again: outcome %d, want MarkTaken", out)
	} else {
		end()
	}

	epoch = LaunchEpoch()
	for i := 0; i <= recentReleases; i++ {
		r, _ := ReserveLaunch(other)
		r()
	}
	if _, out, _ := MarkAgainstLaunches(app, "", epoch, names); out != MarkReread {
		t.Fatalf("more releases than are kept: outcome %d, want MarkReread", out)
	}
}
