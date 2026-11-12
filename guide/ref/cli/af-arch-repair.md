---
audience: "anyone looking up what af-arch-repair restores after a CPU architecture change, and what it leaves to you"
source_of_truth: "workspace/af-arch-repair.sh, installed as /usr/local/bin/af-arch-repair"
---

# af-arch-repair

English | [日本語](af-arch-repair.ja.md)

Reinstalls, at the same versions, what you installed into your home yourself, after the workspace moved to a machine
of another CPU architecture (`amd64` ↔ `arm64`). The workspace runs it on its own during that start; you run it by
hand only to retry or to see what is still broken. A reinstall that fails is retried at the next few starts. Leftovers
it finds but must not fix, and reinstalls it stopped retrying, it names in a Console notification. It finds what it looks for, not everything: see [Limits](#limits).

The tools Agent Fleet itself installs (the agent CLIs, the selected JDK and Node.js, Chromium) are reinstalled by the
workspace's start, not by af-arch-repair.

## Synopsis

```sh
af-arch-repair <was> <now>
```

`<was>` and `<now>` are the architecture before and now, `amd64` or `arm64`. `<was>` only appears in the messages.
Without `<now>` it prints `usage: af-arch-repair <was> <now>` and exits with status 2.

```sh
af-arch-repair amd64 arm64
```

```sh
af-arch-repair --due
```

`--due` changes nothing and prints nothing. It exits with status 0 when a failed reinstall is waiting for an automatic
retry, or for the start that gives up on it and raises the notification (see [Retries](#retries)), and 1 otherwise. The
workspace's start uses it.

## What it reinstalls

| What | Which ones | How |
|---|---|---|
| `pip install --user` packages | Those whose file list names a native extension built for the other architecture. Pure-Python ones are left alone | `pip install --user --force-reinstall --no-deps <name>==<version>`, one package at a time. `--no-deps` keeps their dependencies as they are |
| `uv tool` tools | Those whose environment holds a native extension (`*-linux-gnu.so`) built for the other architecture | `uv tool install --force --reinstall <tool>==<version>`. When the tool's version cannot be read, without `==<version>`, so it gets the latest |
| `npm install -g` packages in `~/.local` | The ones listed when the workspace removed `~/.local/lib/node_modules` at the start of the change | `npm install -g --prefix ~/.local <name>@<version>`, one package at a time |

The workspace writes the npm list at a start that changes architecture, adding what it is about to remove to any
entries still pending from an earlier change. The list then stays until each of its packages is reinstalled (see
[Retries](#retries)). Run by hand, af-arch-repair reinstalls npm packages only while that list exists.

## What it only reports

It does not touch these, and names them instead:

| What | Why it is left alone | How you fix it |
|---|---|---|
| Build output under `~/repos`: `node_modules`, `target`, `.venv` holding binaries for the other architecture | Uncommitted work lives there | `npm ci`, `cargo clean && cargo build`, or recreate the `.venv` |
| Programs you put into `~/.local/bin` yourself, built for the other architecture | Where they came from is unknown | Download or build them again |

af-arch-repair checks the binaries themselves, not whether a directory exists: a pure-JavaScript `node_modules` is not
reported, and a fixed one is no longer reported the next time it runs.

## Limits

What it does not find, it neither fixes nor reports. In particular:

- A pip package is found only through native extension files whose names carry the architecture, and only when its
  name and version can be read. Others are not reinstalled.
- Under `~/repos` it looks only at `node_modules`, `target` and `.venv` directly inside each working copy, not in
  subprojects deeper down. It judges each by the first `.node` file (`node_modules`) or `.so` file (`target`, `.venv`)
  it finds, so a directory with a mix can go unreported, and programs built there are not checked.
- In `~/.local/bin` it looks only at programs placed there directly.

When something still fails with `Exec format error` or a missing module after the move, rebuild or reinstall it as
you would on a new machine.

## Output

Its own lines start with `[arch-repair]`; the output of `pip`, `uv` and `npm` appears between them as those tools print it:

```
[arch-repair] pip: 2 件を同じ版で入れ直します: greenlet==3.0.3 PyYAML==6.0.1
...
[arch-repair] pip: 完了
[arch-repair] ⚠️ ~/repos のビルド生成物が amd64 用のままです（~/repos は触っていません）:
[arch-repair] ⚠️  shop/node_modules
[arch-repair] ⚠️  直す: node_modules → npm ci ／ target → cargo clean && cargo build ／ .venv → 作り直し
[arch-repair] 完了
```

When a reinstall fails it prints a `WARN:` line with the command to run by hand, and the last line names what is
still to be reinstalled and whether a later start will try again:

```
[arch-repair] 入れ直せなかったものが残っています: pip:greenlet==3.0.3（次の起動で自動でもう一度試します。自動で試すのはあと 3 回です）
```

During the workspace's start this output goes to the start log, which you cannot see. What you see is the notification
**Some things could not be restored automatically**, listing the leftover `~/repos` directories, `~/.local/bin`
programs, and the reinstalls no start will retry any more (such as `pip:greenlet==3.0.3` or `npm:cowsay@1.6.0`). It is
raised from the file af-arch-repair writes, `~/.local/share/agent-fleet/arch-residue`, which holds the
current leftovers and is removed when there are none. A notification already shown for the same leftovers is not
shown again.

A failed reinstall reaches the notification only once its automatic retries are used up: a failure the next start
fixes is never shown. While retries remain, a tool you installed yourself can stay broken for a few starts; run
`af-arch-repair <was> <now>` in a terminal to retry at once and read its `WARN:` lines.

## Retries

A failed reinstall leaves its list in `~/.local/share/agent-fleet` (`arch-repair-npm`, `arch-repair-pip`,
`arch-repair-uv`), and the next start runs af-arch-repair again while a list remains, also when the architecture did
not change at that start.

| What | Behaviour |
|---|---|
| What stays in a list | Exactly the packages whose install has not exited successfully yet. Each package is installed on its own and leaves the list only when its own install succeeds, so one that keeps failing does not hold back the others. A run that is stopped half-way keeps every package it had not finished |
| What a retry reinstalls | Only the kinds that failed (pip, uv, npm). Every package in a list is installed again, whatever is on disk: a failed install can leave files that look installed. pip and uv are also checked again, so a package that broke since is added. The version is the one installed now when the package is present, so a package you have upgraded since keeps your version and only gets its native parts rebuilt. The listed version is used only when the package is gone |
| How often | At most 3 retry starts after the one that changed architecture. A package that cannot be installed would otherwise slow down every start. A retry counts when it starts, so a start that is stopped during the reinstall counts too. A start that changed architecture and was stopped before af-arch-repair finished is redone in full at the next start, and those reruns count against the same 3 |
| After the 3rd retry | The next start installs nothing: it names the leftovers in the notification, then records that it gave up, and no start runs af-arch-repair for them again. The lists stay: run the command in the `WARN:` line, or `af-arch-repair <was> <now>` |
| Run by hand | Retries everything that is broken, whatever the count. When it succeeds, the lists and the notification's leftovers are removed |
| Python major version changed at that start | A pending pip failure is dropped, not retried: the same version may not exist for the new Python |

## Environment

| Variable | Effect |
|---|---|
| `AF_REPAIR_PY=0` | Skips the pip reinstall. The workspace sets it when the Python major version changed in the same start, because the same package version may not exist for the new Python |
| `AF_ARCH_REPAIR_AUTO` | Set by the workspace's start: `change` resets the retry count; `retry` counts one retry and reinstalls only what failed before; `rerun` redoes a change start that was stopped and counts one retry. Past the cap, `retry` and `rerun` install nothing and only report. Do not set it yourself |

## Exit status

| Status | Meaning |
|---|---|
| 0 | It ran, including when a reinstall failed (the output says so). With `--due`: a list is pending and the give-up has not been recorded |
| 1 | With `--due` only: nothing is pending, or the workspace already gave up on it |
| 2 | `<now>` is missing |

## Related

- [See your machine and its usage](../../user/workspace/machine.md): the workspace's architecture
- [Choose your toolchains](../../user/workspace/toolchains.md)
