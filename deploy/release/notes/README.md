# Release notes

One file per released version, written by hand before publishing. These are the
bodies of the public GitHub Releases on the dist repo, so they are **user-facing**
text: describe what changed for someone running Agent Fleet, not what changed in the
tree. Internal-only work (CI, refactors, docs for this repo) does not belong here.

```
0.3.0.md      English — canonical
0.3.0.ja.md   Japanese — same content
index.tsv     published release ledger: version → publish date → build commit
```

Same convention as the READMEs: English is canonical, Japanese sits alongside as
`.ja.md`. A missing `<version>.md` fails the publish; a missing `.ja.md` only omits
the Japanese section.

## Structure

Open with a one- or two-sentence summary of the release — `gen-changelog.sh` lifts
that first paragraph into the dist repo's CHANGELOG, so it should stand alone. Then
use `###` sections, keeping only the ones that apply:

`### New` · `### Improved` · `### Fixed` · `### Upgrade notes` · `### Other changes`

Lead each bullet with the user-visible effect, in bold when it is a headline item.
For a fix, say what went wrong rather than which function was patched — that is what
tells a reader whether they were affected.

## Keep it short

These notes are read to answer "is there anything in here for me?", not to explain the
work. Earlier releases grew into essays nobody finishes, so:

- **One or two sentences per bullet.** A bullet that needs a third is carrying
  background, and background belongs in `docs/`.
- **No causes, no measurements, no internals.** Not the design that was wrong, not the
  before/after milliseconds, not what a component is called. Name the symptom and the
  outcome and stop.
- **Never list a fix to something new in the same release.** The reader only ever sees
  the shipped state, so a bullet about it describes a version nobody ran. If the repair
  left a limitation, say that in the feature's own bullet instead.
- **Collect the small stuff.** Several minor fixes in one area are one bullet, not six.
  A bullet nobody would act on can simply go.
- **Only write `### Upgrade notes` when there is something to do.** When there is not,
  one line ("Nothing to do.") is the whole section — do not restate the release.

Bold is for headline items, not for every bullet: if most of a section is bold, none of
it stands out.

Do **not** put the download links, asset names or the rootfs tag in these files:
`notes-body.sh` appends that footer, because the rootfs content hash is only known at
build time.

## Rendering

```sh
# preview the exact release body
VERSION=0.3.0 ROOTFS=0acd1112b7b0 deploy/release/notes-body.sh

# re-render an already published release (notes are metadata, so this is allowed —
# unlike the assets, which are immutable)
VERSION=0.2.3 ROOTFS=0acd1112b7b0 deploy/release/notes-body.sh \
  | gh release edit v0.2.3 -R k-k1/agent-fleet-dist --notes-file -
```

`publish-dist.sh` renders the body itself and writes it to
`deploy/release/dist/RELEASE_NOTES-<version>.md`, so what was published can be
inspected afterwards.

## Publishing a version

Covered by the runbook in [docs/35 §35.8.2](../../../docs/35-packaging.md); the parts
that concern this directory:

1. Write `<version>.md` and `<version>.ja.md`.
2. Append the row to `index.tsv` (version, publish date, build commit).
3. Run `deploy/release/gen-changelog.sh` and commit the regenerated
   `dist-repo/CHANGELOG*.md`.
4. Tag the build commit `v<version>` in this repo and push the tag.
5. Publish with `--seed` so the CHANGELOG reaches the dist repo.

Steps 2-4 exist because the version→commit mapping used to be recoverable only from
the `publish-dist` workflow runs' `head_sha`. Releases 0.1.0-0.2.3 were reconstructed
that way and tagged retroactively; keep the ledger and the tags current so that never
has to be repeated.
