---
audience: "the maintainer who cuts a release, and anyone who reads the vulnerability tracking issue"
source_of_truth: ".github/workflows/image-vuln-scan.yml, deploy/local/vuln-scan-report.py, deploy/local/vuln-scan-issue.sh, .github/workflows/publish-dist.yml"
---

# 94. Image vulnerability scan and the on-demand patch release

English | [日本語](94-patch-release.ja.md)

The shipped images build from floating base tags (`debian:trixie-slim`, `node:22-trixie-slim`,
`golang:1.26-trixie`) with no `apt-get upgrade`, and a Go binary only gets a newer standard
library when it is rebuilt. A release therefore carries what was current on its build day, and
nothing moves until the next release. Hosts are covered elsewhere (Fargate, SSM-resolved AMIs).
This chapter covers the images: how a missing fix is **detected**, and the cheap path from the
detection to a **patch release**. Scheduled patch releases are not adopted; the maintainer
decides, from the issue. Decision record: [#3320](https://github.com/k-k1/agent-fleet/issues/3320).

## 94.1 Detect: `image-vuln-scan.yml`

Weekly (Monday), after every successful `publish-dist` run, and on dispatch
(`gh workflow run image-vuln-scan.yml [-f version=0.40.0]`).

| Target | How it is reached | Scanner |
|---|---|---|
| `control-plane` image | `ghcr.io/k-k1/agent-fleet/control-plane:<version>` (the tag `deploy/compose/release.sh` pushes; no `v`) | Trivy |
| `workspace` image | `ghcr.io/k-k1/agent-fleet/workspace:<version>` | Trivy |
| native rootfs | the `agent-fleet-rootfs-*-linux-amd64.tar.zst` asset of dist release `v<version>`, extracted | Trivy |
| CP binary | `bin/af-cp` of the `agent-fleet-native-<version>-linux-amd64.tar.gz` asset | `govulncheck -mode=binary` |
| Agent binary | `usr/local/bin/workspace-agent` inside that rootfs | `govulncheck -mode=binary` |

`<version>` is the highest `vX.Y.Z` non-draft, non-prerelease release of the dist repo. The
amd64 halves are scanned; both architectures build from the same Dockerfile and Go version.

**What counts:** a Trivy finding is HIGH or CRITICAL *and* has a fixed version. The Go
vulnerability database has no severity, so every `govulncheck` finding with a fixed version
counts (binary mode only reports symbols present in the binary).

**Only the latest release owns the issue.** A dispatch with an older `version` still scans it (run summary and artifact), but leaves the issue alone, and the latest is asked again right before the issue is touched, so a release published mid-scan cannot close or overwrite the newer one's issue. The downloaded package and rootfs are treated as data: nothing from them is executed, and the rootfs is unpacked by `vuln-scan-extract.py`, which refuses paths and links that leave the directory.

**One issue, idempotent.** Label `image-vuln-scan`, opened by the workflow. Findings and no
issue: open it. Findings and an issue: rewrite the body only when the text differs (the body has
no timestamp or run link on purpose) and comment. Clean and an issue: comment and close. Clean and
no issue: nothing.

**A scan that did not run is red, never clean.** The Trivy download is checked against a pinned
sha256, `govulncheck` is installed at a pinned version, and `vuln-scan-report.py` exits 2 for a
missing, empty or non-scanner file. A red run leaves the issue as it was.

Each row has a **Remedy**:

- `rebuild`: an OS package, or the Go standard library in `af-cp` / `workspace-agent`. A rebuild
  with a refreshed base image and Go toolchain cures it, so it is a patch-release case.
- `bump`: a Go module, an npm package, or a tool we bake in (its own pin, e.g. a Go tool built with
  an old toolchain). A rebuild does not move it. Fix it with a normal PR to `main`, which the next
  release carries; if it cannot wait, the patch release has to carry that bump too (see below).

Tests: `deploy/local/vuln-scan-report-test.sh` (fixtures and a fake `gh`; run in `ci.yml`). The
schedule itself has not been observed on GitHub at the time of writing; the scanner commands were
run by hand against the published 0.40.0.

## 94.2 Patch release on demand

Use it when the issue has `rebuild` rows, and a normal release is not about to happen. It
produces `0.x.y+1` from the **last release tag**, so unreleased work on `main` stays out.

Constraints:

- Patch the **latest** published release only. If a newer version exists, release from `main`
  instead: GitHub marks the most recently *created* release as "latest", which is what the
  installer and `af update` follow, so publishing an older line last would point them backwards.
- Never re-push a tag or image. Published tags and digests are immutable; the fix is a new version.
- The images are built without a layer cache on a clean runner (`CP_CACHE_REF` / `WS_CACHE_REF`
  are empty for releases), so the floating base tags and the golang image resolve to whatever they
  are *now*. That is the whole refresh: no Dockerfile change is needed for it. If a fix exists
  only in a newer Go minor than `golang:1.26-trixie`, a rebuild does not carry it; bump the Dockerfile
  and the `go` directives by a normal PR instead.

Steps (in your own shell; `0.40.0` → `0.40.1` as the example):

```bash
# 1) a release branch off the last tag. publish-dist.yml and the Dockerfiles are
#    the tag's own, which is the point.
git fetch origin --tags
git switch -c release/0.40.1 v0.40.0

# 2) the Debian package pins. The publish preflight fails on a pin that trixie-security
#    no longer serves, and an old tag is exactly where that happens.
deploy/local/apt-pin-check.sh        # exit 0 = nothing to do
#    else: bring the new ARG values over from main (the apt-pin-bump PR), or run
#    deploy/local/apt-pin-bump.sh, and commit ("build(workspace): ...").

# 3) the release notes, as for any release (deploy/release/notes/README.md): 0.40.1.md and
#    0.40.1.ja.md, a SUMMARY.md / SUMMARY.ja.md section, a `pending` row at the end of
#    index.tsv, then deploy/release/gen-changelog.sh. The notes say what the user gets:
#    "Updated base images and the Go toolchain to pick up security fixes. Nothing to do."
git commit -m "docs(release): draft 0.40.1 notes, ledger row and SUMMARY"
git push origin release/0.40.1

# 4) publish through the existing flow, from that branch.
gh workflow run publish-dist.yml --ref release/0.40.1 -f version=0.40.1

# 5) record the build commit, tag it, push the tag (the same two steps as any release, in [deploy/release/notes](../../deploy/release/notes/README.md)).
#    Replace `pending` in index.tsv with the commit the run built.
git tag -a v0.40.1 <build commit> -m "agent-fleet 0.40.1" && git push origin v0.40.1

# 6) bring the notes, SUMMARY, ledger row and CHANGELOG to main with a PR, so the
#    ledger there stays complete (release-gate checks every ledger row has a SUMMARY section).
```

Dispatching `publish-dist.yml` from a branch works because the workflow file exists on the default
branch and on the ref; everything else (scripts, Dockerfiles) is read from the ref. After the run
succeeds, `image-vuln-scan` runs by itself and closes the issue if nothing fixable is left. If it
does not close, the remaining rows are `bump` rows or a fix the base image does not have yet.

For a deployment, the patch version is an ordinary new `ImageTag`; there is nothing special to
roll out. The ordinary publish steps are in [deploy/release/notes](../../deploy/release/notes/README.md#publishing-a-version).
