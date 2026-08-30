# 0059. Repository imports (clone / svn checkout) become named jobs, and completion is not decided by "did a response come back"

English | [日本語](0059-repo-import-jobs.ja.md)

- Status: **adopted and implemented** (2026-08-26). The design and the background are in [docs/78](../log/78-repo-import-jobs.md).
- See also: [0024-svn-checkout.md](0024-svn-checkout.md) (SVN import proper; this ADR replaces its completion check) /
  [0055-idle-stop-and-carried-interactions.md](0055-idle-stop-and-carried-interactions.md) (adding "importing" to what decides whether it may be stopped) /
  [0030-turn-abort-auto-resume.md](0030-turn-abort-auto-resume.md) (taking a long operation out of a request's lifetime — a precedent of the same shape)

## Context

On the `<prod-deployment>` environment, a large SVN repository checkout **was reported as "checked
out" while the working copy was half-finished**. Pressing Update there gave `E155037`, and pressing
Clean up lock gave `E200033 (database is locked)`. Past the 30-minute mark, the working copy
**disappeared, folder and all**.

The cause is one reinterpretation. An import takes minutes to hours, while the ALB's idle timeout is 60
seconds ([30-ingress.yaml](../../deploy/aws/ecs/cfn/30-ingress.yaml)), so a large import's response is
always cut off there. The Console anticipated that and judged "an error, but a success if a folder
appeared in `~/repos`" (the old `console/src/features/repos/clone.ts`). But `svn checkout` **creates
`.svn` within a second of starting**, so that judgement always falls to success. Everything after that
is the consequence:

- The list (`GET /repos`) shows any folder with a `.svn` as a working copy. **Including one still
  running.**
- The list runs `svn info` / `svn status` per row. That touches the same `wc.db` as the running
  checkout, so they fight over the sqlite lock every 60 seconds. The `L` (locked) that `svn status`
  returns is displayed as Uncommitted.
- When the user presses Update or Clean up lock, it collides head-on with the running checkout (the two
  errors above).
- At 30 minutes (`svnNetTimeout`) it is killed and the failure path's `os.RemoveAll(dir)` deletes the
  working copy. **Nobody is waiting for the response at that point**, so nobody is told it is gone.

The same shape existed for git clone (`.git` also appears at the start of a clone).

## Decision

**An import becomes a named job on the Agent side.** `POST /repos` and `POST /repos/svn` do only the
validation synchronously and return `202` plus a job. The network work runs as a
`context.Background()` job, and progress and outcome are observed with `GET /repo-jobs`. The only
grounds for completion is the job's `state`, not whether an HTTP response arrived.

Five things come with it:

1. **A folder that is still running does not appear in `GET /repos`.** A half-finished working copy is
   not "something you can launch in, update, or run `svn status` against". The Console draws the job's
   row instead.
2. **Do not delete a working copy that can be resumed after a failure.** svn can carry on with
   `cleanup` + `update`. Only debris that never became a working copy may be deleted. The cap is six
   hours (a value that only cuts off something obviously broken), and it can be cancelled when you
   want to stop it.
3. **Make an interruption detectable.** The Agent dies along with the import on a task swap or an
   idle-stop. A marker is put on disk, and if it survives at startup the job comes back to the list as
   `interrupted`. Otherwise a half-finished working copy lines up wearing the face of a normal
   repository — the same state as before the incident.
4. **Do not stop the workspace while importing.** The number of running jobs goes on `GET /sessions`
   and is added to the reaper's busy check. GET polling does not count as activity by convention
   (docs/19), so without this a one-hour import is killed by automatic stopping. `repojob` also appears
   in the holders for "why will it not stop" (docs/75 decision 11).
5. **`svnLocked()` must include `E155037`.** What svn actually says after an interruption is
   "run **'cleanup'**", not `svn cleanup`. We were looking only at the E155004-family wording, so
   automatic repair had never once run.

## Options rejected

- **Raise the ALB's idle timeout.** 60 seconds is a cap on all of the CP's handlers, and raising it for
  a long operation is explicitly forbidden by 30-ingress.yaml's comments (it may only be raised for a
  new long-poll). And a design of "hold the connection until the import finishes" means the outcome
  disappears if you close the tab.
- **Infer completion by looking inside the folder** (file counts, whether `svn status` is empty). There
  is no external way to distinguish running from finished. `svn info` returns the target revision from
  the very beginning, so it is no evidence of completion either.
- **Count the Console's polling as activity and suppress idle-stop.** An open tab keeping a workspace
  warm is the failure docs/19 explicitly avoided (the billing never stops). Busy must be grounded in
  work that is actually running.
- **Always delete the folder on failure (the old behaviour).** What was lost here was tens of minutes of
  downloading, and svn could have resumed. Deleting is the user's choice, not the default cleanup.
