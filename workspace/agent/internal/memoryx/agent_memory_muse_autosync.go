package memoryx

// The memory switch's import phase also carries muse's own memory across (#3858), beside
// claude's (agent_memory_autosync.go). It runs the member's muse import (agent_memory_muse_import.go)
// unattended, so the rules are the ones that import already has: a secret hit is skipped, a
// forgotten memory is not brought back, and a memory AF holds its own version of is reported as
// "diverged" and never replaced.
//
// Two things differ from claude:
//
//   - muse's personal store lands in the user scope, which every project reads. It is imported
//     only by the member's explicit choice (into=user), so the switch never writes it: the run
//     counts what is waiting and lists one "needs_confirmation" row that points at the manual
//     import card.
//   - muse is best effort. A folder that cannot be read or applied is counted (MuseFailed) and the
//     run goes on: it never sets run.Error, because that keeps the whole phase in `importing` and
//     retries, which would hold claude's carry-over and the switch itself hostage to muse's disk.
//
// Switching off writes nothing back into muse's stores (open question, see #3858).

import "log"

const (
	// autoAgentMuse tags the projects and rows of muse's carry-over.
	autoAgentMuse = "muse"
	// autoStatusNeedsConfirmation is the row status of the personal store waiting for a choice.
	autoStatusNeedsConfirmation = "needs_confirmation"
	// autoMuseUserProject is the row's Project for a memory that belongs to the user scope.
	autoMuseUserProject = "user"
)

// autoMuseProjects accumulates one muse entry per AF project: a working copy's project store and
// its personal-project store both land in the same project.
type autoMuseProjects struct {
	order []string
	by    map[string]*AutoSyncProject
}

func (m *autoMuseProjects) get(p *agentMemProject) *AutoSyncProject {
	if m.by == nil {
		m.by = map[string]*AutoSyncProject{}
	}
	pr := m.by[p.ID]
	if pr == nil {
		pr = &AutoSyncProject{Agent: autoAgentMuse, ID: p.ID, Display: autoShowable(p.Display), Counts: map[string]int{}}
		m.by[p.ID] = pr
		m.order = append(m.order, p.ID)
	}
	return pr
}

// autoImportMuse imports every mapped muse folder. It returns true when stop() ended the run.
func autoImportMuse(run *AutoSyncRun, stop func() bool) (stopped bool) {
	list := agentMemMuseList()
	var acc autoMuseProjects
	defer func() {
		for _, id := range acc.order {
			run.Projects = append(run.Projects, *acc.by[id])
		}
	}()
	for _, s := range list.Sources {
		if s.Scope == museScopePersonal {
			continue
		}
		if s.Project == nil {
			run.MuseUnmapped++
			continue
		}
		pr := acc.get(s.Project)
		switch autoImportMuseSource(run, pr, s, stop) {
		case autoMuseStopped:
			return true
		case autoMuseFailed:
			run.MuseFailed++
		}
	}
	if stop() {
		return true
	}
	// The personal store is evaluated only to say what is waiting; nothing in it is written.
	for _, s := range list.Sources {
		if s.Scope != museScopePersonal {
			continue
		}
		agentMemMu.RLock()
		pv, err := agentMemMuseBuild(museScopePersonal, "", museIntoUser)
		agentMemMu.RUnlock()
		if err != nil {
			log.Printf("agent memory: muse carry-over: personal store (%s)", autoErrKind(err))
			run.MuseFailed++
			continue
		}
		if n := pv.Counts[claudeImportNew] + pv.Counts[claudeImportUpdate]; n > 0 {
			run.MusePersonal = n
			run.addRow(AutoSyncRow{Agent: autoAgentMuse, Project: autoMuseUserProject, Name: museScopePersonal,
				Status: autoStatusNeedsConfirmation, Reason: museImportChooseUser})
		}
	}
	return false
}

type autoMuseOutcome int

const (
	autoMuseDone autoMuseOutcome = iota
	autoMuseStopped
	autoMuseFailed
)

// autoImportMuseSource brings one muse folder in with the same re-evaluate-and-chunk loop as the
// claude import. Failures are logged by kind only (a path in an error carries a name).
func autoImportMuseSource(run *AutoSyncRun, pr *AutoSyncProject, s agentMemMuseSource, stop func() bool) autoMuseOutcome {
	for pass := 0; pass <= autoImportPasses; pass++ {
		if stop() {
			return autoMuseStopped
		}
		agentMemMu.RLock()
		pv, err := agentMemMuseBuild(s.Scope, s.Dir, "")
		agentMemMu.RUnlock()
		if err != nil {
			log.Printf("agent memory: muse carry-over: %s (%s)", s.Scope, autoErrKind(err))
			return autoMuseFailed
		}
		if pv.Project == nil {
			return autoMuseDone
		}
		if pv.Truncate {
			// Name uniqueness cannot be proven for notes that were not listed, so none is applied
			// (the member's import refuses the same folder).
			pr.Counts["skipped"]++
			run.addRow(AutoSyncRow{Agent: autoAgentMuse, Project: pr.ID, Name: autoMuseRowName(s), Status: "skipped", Reason: "too_many_files"})
			return autoMuseDone
		}
		var todo []agentMemImportReqItem
		for _, it := range pv.Items {
			if it.Status == claudeImportNew || it.Status == claudeImportUpdate {
				todo = append(todo, agentMemImportReqItem{Name: it.Name, SourceHash: it.SourceHash})
			}
		}
		if len(todo) == 0 || pass == autoImportPasses {
			autoImportLeftovers(run, pr, pv.Items)
			return autoMuseDone
		}
		for at := 0; at < len(todo); at += autoImportChunk {
			if autoChunkHook != nil {
				autoChunkHook()
			}
			if stop() {
				return autoMuseStopped
			}
			end := min(at+autoImportChunk, len(todo))
			res, err := agentMemMuseApply(agentMemMuseReq{Scope: s.Scope, Project: pv.Project.ID, Dir: s.Dir, Items: todo[at:end]}, autoNow())
			if err != nil {
				log.Printf("agent memory: muse carry-over: %s apply (%s)", s.Scope, autoErrKind(err))
				return autoMuseFailed
			}
			for _, r := range res.Results {
				if r.Result == "imported" || r.Result == "updated" {
					pr.Counts[r.Result]++
				}
			}
		}
	}
	return autoMuseDone
}

// autoMuseRowName is a name a run row may carry: the folder's own name when it passes the scan,
// else the scope.
func autoMuseRowName(s agentMemMuseSource) string {
	if n := autoShowable(s.Dir); n != "" {
		return n
	}
	return s.Scope
}

// autoMusePreview adds what the muse import would do to the switch-on preview. A folder that
// cannot be read is left out: the confirmation must not fail because of muse.
func autoMusePreview(out *AutoSyncPreview) {
	var acc autoMuseProjects
	for _, s := range agentMemMuseList().Sources {
		if s.Scope == museScopePersonal {
			agentMemMu.RLock()
			pv, err := agentMemMuseBuild(museScopePersonal, "", museIntoUser)
			agentMemMu.RUnlock()
			if err == nil {
				out.MusePersonal = pv.Counts[claudeImportNew] + pv.Counts[claudeImportUpdate]
			}
			continue
		}
		if s.Project == nil {
			out.MuseUnmapped++
			continue
		}
		agentMemMu.RLock()
		pv, err := agentMemMuseBuild(s.Scope, s.Dir, "")
		agentMemMu.RUnlock()
		if err != nil || pv.Project == nil {
			continue
		}
		pr := acc.get(pv.Project)
		if pv.Truncate {
			// The run applies nothing from a folder cut at the file limit, so the preview must not
			// promise it.
			pr.Counts["skipped"]++
			continue
		}
		for k, n := range pv.Counts {
			pr.Counts[k] += n
			out.Totals[k] += n
		}
	}
	for _, id := range acc.order {
		out.Projects = append(out.Projects, *acc.by[id])
	}
}
