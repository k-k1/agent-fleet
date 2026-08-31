# 0060. Settings export/import is one bundle of "sections that contain no secrets", and importing only adds

English | [日本語](0060-settings-export-import.ja.md)

- Status: **adopted and implemented** (2026-08-26). The design and the background are in [docs/79](../log/79-settings-export-import.md).
- See also: [0022-agent-memory-management.md](0022-agent-memory-management.md) (the precedent for taking things between environments. Memory has its own format and is not in this bundle) /
  [0042-user-instructions.md](0042-user-instructions.md) ("instructions to the agent", one of the three layers carried) /
  [0036-working-sets.md](0036-working-sets.md) (the representative example of accumulated data riding on ui-prefs)

## Context

It began with "I want to export/import the SSM settings". Registering SSM entries is heavy manual
input, and it gets re-entered every time someone moves environment. But what they want to move is not
SSM — it is **their environment**, of which SSM is one part. Adding a button per feature means one more
every time a request of the same shape arrives, and the file formats scatter.

## Decision

### 1. One JSON (`kind: "agent-fleet-settings"` / `version` / `sections`)

A collection of sections. What travels for now is **personal settings (ui-prefs) / AWS SSM /
instructions to the agent**. Environment variables, MCP definitions and internal repository
registrations ride later by adding a section.

**Versions are not forward compatible.** An unknown `version` is refused rather than read. Pretending
to read part of it removes the user's means of confirming what did and did not go in.

### 2. Secrets are always excluded

Connections (Git / agent / AWS / chat tokens and API keys) are not included. On the other side you sign
in again.

**Why:** this file gets treated casually, as "a settings file" (emailed, pasted into chat, committed to
a repository). If even one secret is in it, that casualness becomes the leak path itself. **Excluding
is not the default — excluding is all it does**: if a single checkbox could include them, the accident
would not be "I forgot to untick it" but "I forgot that I had included them".

As a side benefit the bundle becomes **something you can hand to a person** (distributing a team's whole
set of SSM registrations becomes possible).

### 3. A host references a profile by "display name", not by id

The CP's ids differ per environment, so carrying an id always requires re-pointing on import. The
display name is also the raw material of the profile name in `~/.aws`, i.e. a natural key, so
**the format itself removes the id problem**.

### 4. Importing only adds; it never rewrites what exists

Profiles are matched against existing ones by display name and hosts by alias plus instance id, and a
match is skipped with the reason shown. When the name matches but the contents differ, the Console
cannot judge which is correct, and overwriting is rarely the right answer. Damaging an existing
environment during a settings transfer is not worth it.

### 5. Personal settings are **layered**. Do not crush accumulated data with an empty value

ui-prefs PUTs the whole state and the last writer wins. On 2026-08-18 that property combined with a
silent failure in hydrate to cause **an incident where every device's learned reply suggestions were
erased**. An import is an operation that writes a blob from elsewhere in one go, so implementing it
naively would open the same hole again.

- Keep only values under known keys whose shape matches the default (`sanitizeImportedPrefs`).
- Layer them onto the current values. Accumulating keys are **not overwritten by an empty value**, and
  objects are **added to** (`mergeImportedPrefs`).

### 6. Zero server-side changes (contained in the front end)

Every REST endpoint needed (`api/env/ui-prefs`, `api/ssm/*`, `api/user-notes`) already exists. Adding a
new one requires entries in both the CP's proxy allowlist and the Agent, and we keep repeating the
accident of adding only one and getting a 404.

**The price:** there is no atomicity (a host can fail after a profile was created). Since importing
"only adds and skips what exists", **importing the same file again brings in the remainder** — we
judged idempotency to be enough.

## Options discarded

- **Put export/import only on the SSM tab (the minimum).** Fast, but the next request means rebuilding
  the format. Building the container first is cheaper.
- **Allow secrets to be included via an explicit checkbox.** Convenient for "a complete move of my own
  environment", but it changes the file's nature from "something you can hand over" to "something you
  must not", and **the two are indistinguishable by sight**. We do not create a state where files with
  and without secrets coexist under the same name.
- **Add a bulk import API to the CP to make it atomic.** Not worth adding one more instance of the
  known accident path (double registration in the allowlist) when idempotent re-import already suffices.
- **Have an import overwrite what exists / let the user pick per difference.** The former is
  destructive; the latter becomes a diff UI over three layers and dozens of items. Excessive for a
  transfer tool.
- **PUT ui-prefs as they are (the most straightforward implementation).** A re-run of the incident
  (decision 5).
