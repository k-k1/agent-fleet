---
audience: "anyone looking up what af-arch-repair restores after a CPU architecture change, and what it leaves to you"
source_of_truth: "workspace/af-arch-repair.sh, installed as /usr/local/bin/af-arch-repair"
---

# af-arch-repair

English | [日本語](af-arch-repair.ja.md)

Reinstalls, at the same versions, what you installed into your home yourself, after the workspace moved to a machine
of another CPU architecture (`amd64` ↔ `arm64`). The workspace runs it on its own during that start; you run it by
hand only to retry or to see what is still broken. What it cannot fix, it names in a Console notification.

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

## What it reinstalls

| What | Which ones | How |
|---|---|---|
| `pip install --user` packages | Those whose files include a native extension built for the other architecture. Pure-Python ones are left alone | `pip install --user --force-reinstall --no-deps <name>==<version>`. `--no-deps` keeps their dependencies as they are, so nothing moves to a newer version |
| `uv tool` tools | Those whose environment holds a native extension built for the other architecture | `uv tool install --force --reinstall <tool>==<version>` |
| `npm install -g` packages in `~/.local` | The ones listed when the workspace removed `~/.local/lib/node_modules` at the start of the change | `npm install -g --prefix ~/.local <name>@<version>...` |

The npm list exists only during a start that changed architecture. Run by hand, af-arch-repair reinstalls npm packages
only when a previous attempt failed and left the list behind.

## What it only reports

It does not touch these, and names them instead:

| What | Why it is left alone | How you fix it |
|---|---|---|
| Build output under `~/repos`: `node_modules`, `target`, `.venv` holding binaries for the other architecture | Uncommitted work lives there | `npm ci`, `cargo clean && cargo build`, or recreate the `.venv` |
| Programs you put into `~/.local/bin` yourself, built for the other architecture | Where they came from is unknown | Download or build them again |

af-arch-repair checks the binaries themselves, not whether a directory exists: a pure-JavaScript `node_modules` is not
reported, and a fixed one stops being reported.

## Output

Every line starts with `[arch-repair]`:

```
[arch-repair] pip: 2 件を同じ版で入れ直します: greenlet==3.0.3 PyYAML==6.0.1
...
[arch-repair] pip: 完了
[arch-repair] ⚠️ ~/repos のビルド生成物が amd64 用のままです（~/repos は触っていません）:
[arch-repair] ⚠️  shop/node_modules
[arch-repair] ⚠️  直す: node_modules → npm ci ／ target → cargo clean && cargo build ／ .venv → 作り直し
[arch-repair] 完了
```

When a reinstall fails it prints a `WARN:` line with the command to run by hand.

During the workspace's start this output goes to the start log, which you cannot see. What you see is the notification
**Some things could not be restored automatically**, listing the leftover `~/repos` directories and `~/.local/bin`
programs. It is raised from the file af-arch-repair writes, `~/.local/share/agent-fleet/arch-residue`, which holds the
current leftovers and is removed when there are none. A notification already shown for the same leftovers is not
shown again.

## Retries

| What failed | Retried |
|---|---|
| npm packages | At the next start: the list stays until the reinstall succeeds |
| pip packages, uv tools | Only when af-arch-repair runs again: by hand, or at a start that also retries npm. Run the command in the `WARN:` line, or `af-arch-repair <was> <now>` |

## Environment

| Variable | Effect |
|---|---|
| `AF_REPAIR_PY=0` | Skips the pip reinstall. The workspace sets it when the Python major version changed in the same start, because the same package version may not exist for the new Python |

## Exit status

| Status | Meaning |
|---|---|
| 0 | It ran, including when a reinstall failed (the output says so) |
| 2 | `<now>` is missing |

## Related

- [See your machine and its usage](../../user/workspace/machine.md): the workspace's architecture
- [Choose your toolchains](../../user/workspace/toolchains.md)
