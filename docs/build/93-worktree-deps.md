---
audience: "anyone dealing with worktree dependencies and build caches"
source_of_truth: "measurement inside a container (this records what was measured, and how)"
updated: "2026-08"
---

# 93. Worktree dependencies and build caches, measured per ecosystem

English | [日本語](93-worktree-deps.ja.md)

A session usually runs in its own worktree, so ten worktrees of one repository can
exist at once. What that costs is **not memory but disk, and whether a thing is
shared** — and the answer differs per ecosystem. The operating guide every agent reads
carries only the headline; the evidence and the per-language detail are here.

Two persistence facts drive everything:

- **The working copies are deleted on a recreate** — and with them every worktree's
  dependency tree.
- **The package caches in the home survive.**

So **reinstalling is cheap and duplicating is expensive.** What to cut is *the Nth copy
of the same thing*, never the cache.

## 93.1 At a glance

| Ecosystem | Shared by default | Grows per worktree | What to do |
|---|---|---|---|
| Node (npm) | the tarball cache only | `node_modules`, 300 MB+ | symlink to the parent clone **only when the lockfiles match**; otherwise install from the warm cache |
| Go | modules and the build cache | effectively nothing | nothing. **The pressure is memory** — cap the test parallelism |
| Python | the `uv` cache (plain `pip` shares nothing) | a virtual environment, tens to hundreds of MB | create one per worktree with `uv`. It hardlinks, so the second one is nearly free |
| JVM | the Gradle and Maven caches | the build output | nothing — just be careful how you stop the daemon |
| Rust | the registry cache | the target directory, gigabytes | keep it per worktree. **A shared target directory is not an option** (below) |

Check the disk with `df -h ~`. The cache-cleaning commands are **shared across every
worktree**, so do not run them while another session is building.

## 93.2 Node — the only one that loses unless you share explicitly

`node_modules` is duplicated whole per worktree (measured at 349 MB × worktrees in this
repository). **The parent clone's tree can be shared by symlink**, with one condition
and three ways to hurt yourself.

The condition is that **the lockfile is identical to the parent's**:

```bash
cd <repo-wt>/<pkg>
cmp -s package-lock.json ~/repos/<repo>/<pkg>/package-lock.json \
  && ln -s ~/repos/<repo>/<pkg>/node_modules node_modules
```

Measured:

- The test runner works through the link in both projects, and so does the production
  build. **Bundlers follow symlinks by default**, so resolution needs no help.
- ⚠️ **Running a clean install through the link empties the parent's tree.** The link is
  replaced by a real directory and **every other session sharing it is destroyed**.
  Always remove the link before any install.
- ⚠️ **`rm -rf node_modules/` — with the trailing slash — deletes through the link the
  same way.** Without the slash, only the link goes.
- Installing a single package silently replaces the link with a real tree. Nothing
  breaks, but you are no longer sharing and are 300 MB heavier.

When the lockfiles differ, do not share — install from the warm cache instead. A
content-addressable package manager would not have this problem at all.

## 93.3 Go — do nothing; the pressure is memory

The module and build caches are global, so an extra worktree costs essentially nothing.
What runs out instead is memory: `go test ./...` compiles and runs **package-by-package
in parallel**. On a busy host, cap it.

The toolchain downloads itself if the module pins a newer version, and it lands in the
persistent home — so a recreate costs that download once.

## 93.4 Python — the default `pip` is the dangerous one

The system Python is externally managed, so a bare `pip install` **does not error** —
it falls back to a user install. That location **persists and is shared by every
project**, so it breaks quietly the moment two worktrees need different versions.

The right answer is a virtual environment per worktree, with the tool that is already
baked in:

```bash
uv venv && uv pip install -r requirements.txt
```

It hardlinks from the cache, so the second worktree costs almost no disk. **Never copy
or symlink a virtual environment between worktrees** — it has absolute paths baked in.

## 93.5 JVM — already shared; just mind how you stop it

The Gradle and Maven caches are shared across worktrees already. The heap and daemon
defaults belong to the operating guide.

One worktree-specific hazard: **stopping the Gradle daemon stops it for the whole
container.** Running that while another session is building takes theirs down too.
Doing it when you finish is fine; doing it reflexively "because it is heavy" is not.

## 93.6 Rust and the languages that are not in the image

Install it yourself; the installation directory persists in the home and its registry
cache is shared automatically.

The target directory reaches gigabytes, but **do not point several worktrees at a
shared one**. Cargo takes a build lock on it, so parallel sessions **serialise, each
waiting for the other's build**. Keep it per worktree and clean up when you are done —
that is faster overall.

The same rule generalises: **share the caches in the home; never share the output
directory in the worktree.** And with no root available, choose installers that work in
user space.
