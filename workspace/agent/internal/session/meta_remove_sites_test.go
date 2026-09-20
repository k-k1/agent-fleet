package session

// ADR 0096 decision 6: forgetting a session's meta erases its fleet-graph lineage row too,
// UNLESS the forgetting is the stopped-session TTL auto-prune — that is the one case the
// ledger exists to protect (docs/log/94: losing a parent to the 7-day prune used to sever a
// child's branch line, and the ledger was built to undo exactly that).
//
// A conditional written as "if this is the prune path, skip erasing" is the failure this
// project has already had three times over (revive, archived, and the classification here
// itself, on the first pass): a call site added later has no reason to know the rule and
// picks whichever function name happens to compile. So the rule lives in TWO function
// NAMES instead (RemoveMeta / RemoveMetaAndLineage) and this test freezes how many
// production call sites use each — a call site added under the wrong name changes a count,
// which is what turns "a reviewer has to notice" into "the suite goes red".

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// removeMetaCaller matches a call, e.g. `RemoveMeta(name)` from within this package or
// `session.RemoveMeta(name)` from everywhere else. Requiring the "(" immediately after the
// name is what keeps it from also matching `RemoveMetaAndLineage(` ("RemoveMeta" is a
// prefix of that name, but "AndLineage" sits between it and the "(").
var removeMetaCaller = regexp.MustCompile(`(?:^|[^.\w])(?:session\.)?RemoveMeta\(`)
var removeMetaAndLineageCaller = regexp.MustCompile(`(?:^|[^.\w])(?:session\.)?RemoveMetaAndLineage\(`)

// wantRemoveMeta / wantRemoveMetaAndLineage are the frozen counts. Update them ONLY after
// classifying the new call site against decision 6 above (never by copying whatever number
// makes the test pass) — say which bucket it belongs in in the same commit.
const (
	wantRemoveMeta           = 1                          // the stopped-session TTL auto-prune, internal/sessionx/session_handlers.go
	wantRemoveMetaAndLineage = 4                          // /stop, DELETE /sessions (both branches), working-copy delete collateral
	metaGoRelPath            = "internal/session/meta.go" // defines both; excluded from the scan
)

func TestRemoveMetaCallSitesAreClassified(t *testing.T) {
	root := metaTestModuleRoot(t)
	var plainSites, erasingSites []string

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if filepath.ToSlash(rel) == metaGoRelPath {
			return nil // the definitions themselves, not call sites
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, ln := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(ln)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if removeMetaAndLineageCaller.MatchString(ln) {
				erasingSites = append(erasingSites, rel+": "+trimmed)
				continue // do not also count it as a RemoveMeta call
			}
			if removeMetaCaller.MatchString(ln) {
				plainSites = append(plainSites, rel+": "+trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(plainSites) != wantRemoveMeta {
		t.Errorf("RemoveMeta (no lineage erasure) has %d call site(s), want %d:\n%s\n"+
			"If this is a NEW call site: is it the TTL auto-prune (RemoveMeta is correct) or a "+
			"person asking for the session to be gone (use RemoveMetaAndLineage instead, ADR 0096 "+
			"decision 6)? Update wantRemoveMeta only after deciding which.",
			len(plainSites), wantRemoveMeta, strings.Join(plainSites, "\n"))
	}
	if len(erasingSites) != wantRemoveMetaAndLineage {
		t.Errorf("RemoveMetaAndLineage has %d call site(s), want %d:\n%s\n"+
			"Update wantRemoveMetaAndLineage after confirming the new site really is a person's "+
			"delete, not the TTL prune.",
			len(erasingSites), wantRemoveMetaAndLineage, strings.Join(erasingSites, "\n"))
	}
}

// metaTestModuleRoot walks up to workspace/agent (the directory holding go.mod) — the same
// technique internal/statemig/statemig_drift_test.go uses, duplicated locally since that
// helper is unexported in another package.
func metaTestModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found above the test's working directory")
	return ""
}
