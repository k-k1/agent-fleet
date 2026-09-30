#!/usr/bin/env bash
# Prepare the agent CLIs (claude in particular), then exec the Agent.
# Where the CLIs come from depends on how the image was built (workspace/Dockerfile):
#   BAKE_AGENT_CLIS=0 (the default, lean image): no agent CLI is baked. The boot-install
#     block below installs claude/opencode/codex/copilot/cursor/agy/rtk at their
#     versions.json pins into the persistent ~/.local and keeps them on those pins.
#   BAKE_AGENT_CLIS=1: they are baked at /usr/local, pinned to the image version.
# If claude is still missing after that, the latest is installed into ~/.local via
# claude.ai/install.sh; a ~/.local claude in a non-lean image is updated on every start.
# No network never stops the Agent from starting (the terminal still works).
#
# Control env:
#   CLAUDE_INSTALL=0      … skip the claude install/update step (offline / lightweight checks;
#                           the lean boot-install still runs)
#   CLAUDE_AUTO_UPDATE=0  … skip the start-time `claude update` of a non-lean ~/.local claude
set -e
export PATH="$HOME/.local/bin:$PATH"

# --- Keep credentials on a separate durable volume (ADR 0045 decisions 3-6; ecs-ec2 only) --
# AF_WS_KEEP points at a place that survives independently of home. In the EC2 pool model
# home is a single single-AZ EBS volume, and losing it would take the logins with it. Only
# auth, connections and identity (the 7 `homeKeep` entries, under 100 MiB in total) move to
# EFS and are exposed in home through symlinks. Runtimes where the CP does not inject
# AF_WS_KEEP (docker / native / Fargate) make this a no-op and home stays as it is.
#
# It must run before claude/gh touch these files, hence first in the entrypoint.
if [ -n "${AF_WS_KEEP:-}" ] && [ -d "$AF_WS_KEEP" ] && [ -w "$AF_WS_KEEP" ]; then
  AF_KEEP_DIRS="${AF_WS_KEEP_DIRS:-.config .ssh .claude .codex}"
  # keep_is_dir — whether rel must exist on the keep side as a directory. File entries
  # (.gitconfig etc.) may legitimately be absent, so they are excluded.
  keep_is_dir() { case " $AF_KEEP_DIRS " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }
  for rel in $AF_KEEP_DIRS ${AF_WS_KEEP_FILES:-.git-credentials .gitconfig .claude.json}; do
    src="$HOME/$rel"; dst="$AF_WS_KEEP/$rel"
    if [ -L "$src" ] && [ "$(readlink "$src")" = "$dst" ]; then
      # Already the correct symlink, but its target may be missing: a home built from a
      # golden snapshot carries the seed's symlinks, while the keep side (EFS) is empty for
      # every new user. Skipping here leaves `~/.config` dangling, the later
      # `mkdir -p "$HOME/.config/opencode"` fails with File exists, and `set -e` kills the
      # entrypoint — the task restart-loops with no cause logged anywhere.
      keep_is_dir "$rel" && mkdir -p "$dst" 2>/dev/null || true
      continue
    fi
    if [ -e "$src" ] || [ -L "$src" ]; then
      # home holds a real file. Besides the first migration, this also happens after a write
      # replaced the symlink with a real file (write-to-tmp-then-rename does that), so keep
      # whichever is newer.
      if [ ! -e "$dst" ] || [ "$src" -nt "$dst" ]; then
        rm -rf "$dst" 2>/dev/null || true
        mv "$src" "$dst" 2>/dev/null || { echo "[entrypoint] keep: $rel を退避できませんでした"; continue; }
      else
        rm -rf "$src" 2>/dev/null || continue
      fi
    fi
    keep_is_dir "$rel" && mkdir -p "$dst" 2>/dev/null || true
    # File entries get a dangling symlink too: a later ordinary write creates the file on
    # the EFS side (O_CREAT follows the symlink).
    ln -sfn "$dst" "$src" && echo "[entrypoint] keep: ~/$rel -> $dst"
  done
fi

# --- Self-repair when home lands on another architecture (docs/log/70 §70.5) ------
# home (`~`) persists. On ecs-ec2 one EBS volume follows the member, and docs/log/70 makes
# the instance a per-member setting, so a home populated on x86 can be attached to an
# arm64 slot. The filesystem then mounts fine and only the binaries break — the symptom is
# "claude worked yesterday, now Exec format error", and the cause (the instance changed)
# is logged nowhere.
#
# So stamp the architecture into `~`, and when it changed, discard only what the product
# installed so the boot-install below reinstalls it. A home without a stamp is assumed to
# have been built on the current architecture and is just stamped — the only safe default.
#
# The discard list must match what the boot-install block installs, 1:1. When adding a
# CLI there, add it here too (the table in docs/log/70 §70.5).
# Never touch `~/repos` (the user's uncommitted work lives there). Do not delete tools the
# user put in `~/.local/bin` either — that would read as "they vanished". Report that they
# are broken and leave reinstalling to the user.
af_arch_now="$(dpkg --print-architecture 2>/dev/null || uname -m)"
case "$af_arch_now" in x86_64) af_arch_now=amd64 ;; aarch64) af_arch_now=arm64 ;; esac
AF_ARCH_STAMP="$HOME/.local/share/agent-fleet/arch"
af_arch_was="$(cat "$AF_ARCH_STAMP" 2>/dev/null || true)"
if [ -n "$af_arch_now" ] && [ -n "$af_arch_was" ] && [ "$af_arch_was" != "$af_arch_now" ]; then
  echo "[entrypoint] arch: この home は $af_arch_was で作られ、いま $af_arch_now の上に居ます"
  echo "[entrypoint] arch: アーキ依存の導入物を入れ直します（初回は数分かかることがあります）"
  # Before deleting, record the user's own npm globals with their versions.
  # `~/.local/lib/node_modules` holds both the product CLIs and the user's `npm i -g`, and
  # the loop below deletes the whole directory (correct, since native addons are broken).
  # boot-install restores only the product's 4 CLIs, so without this record the user's
  # packages silently disappear. af-arch-repair restores them at the end of the start.
  mkdir -p "$HOME/.local/share/agent-fleet" 2>/dev/null || true
  node -e '
    const fs = require("fs"), path = require("path");
    const root = process.argv[1];
    // Skip what boot-install restores itself (it would be installed twice).
    const skip = new Set(["@anthropic-ai/claude-code", "opencode-ai", "@openai/codex", "@github/copilot"]);
    const out = [];
    const add = (rel) => {
      try {
        const p = JSON.parse(fs.readFileSync(path.join(root, rel, "package.json"), "utf8"));
        if (p.name && p.version && !skip.has(p.name)) out.push(p.name + "@" + p.version);
      } catch {}
    };
    let ents = [];
    try { ents = fs.readdirSync(root); } catch { process.exit(0); }
    for (const e of ents) {
      if (e.startsWith("@")) {              // scoped packages: descend one level
        try { for (const s of fs.readdirSync(path.join(root, e))) add(e + "/" + s); } catch {}
      } else if (e !== ".bin") add(e);
    }
    if (out.length) process.stdout.write(out.join("\n") + "\n");
  ' "$HOME/.local/lib/node_modules" > "$HOME/.local/share/agent-fleet/arch-repair-npm" 2>/dev/null || true
  [ -s "$HOME/.local/share/agent-fleet/arch-repair-npm" ] || rm -f "$HOME/.local/share/agent-fleet/arch-repair-npm"
  for rel in \
    .local/bin/claude .local/bin/codex .local/bin/opencode .local/bin/copilot \
    .local/bin/rtk .local/bin/agy .local/bin/.agy.version \
    .local/bin/cursor-agent \
    .local/bin/kiro-cli .local/bin/kiro-cli-chat .local/bin/kiro-cli-term .local/bin/.kiro.version \
    .local/lib/node_modules \
    .local/share/claude .local/share/cursor-agent .local/share/kiro-cli \
    .local/share/agent-fleet/chromium \
    .cache/ms-playwright \
    .nvm; do
    { [ -e "$HOME/$rel" ] || [ -L "$HOME/$rel" ]; } || continue
    rm -rf "${HOME:?}/$rel" && echo "[entrypoint] arch: 削除 ~/$rel"
  done
  # Remove cursor's `agent` alias only when it is a symlink, so a user's own script of the
  # same name is left alone.
  if [ -L "$HOME/.local/bin/agent" ]; then rm -f "$HOME/.local/bin/agent"; fi
  # JDK directories carry the architecture in their name (temurin-<major>-jdk-<arch>), so
  # only the other architecture's ones need to go. The reinstall happens automatically in
  # this same start: the java block below looks for the selected version, `find_jh` ignores
  # other-arch directories and reports it missing, and `workspace-agent install-jdk` runs.
  # No user action is needed.
  # Only the version selected in toolchains.json comes back. Other installed but unselected
  # versions stay gone (reinstall them from the Console's toolchains if needed).
  for d in "$HOME"/.local/share/agent-fleet/jvm/temurin-*-jdk-*; do
    [ -d "$d" ] || continue
    case "$d" in *-jdk-"$af_arch_now") continue ;; esac
    rm -rf "$d" && echo "[entrypoint] arch: 削除 ${d#"$HOME"/}"
  done
  # The repair itself runs at the end of the start, once node / java / go are selected;
  # running it here would reinstall with the pre-selection python / node. Just leave the
  # marker for af-arch-repair.
  AF_ARCH_REPAIR_FROM="$af_arch_was"
fi
if [ -n "$af_arch_now" ] && [ "$af_arch_was" != "$af_arch_now" ]; then
  mkdir -p "$(dirname "$AF_ARCH_STAMP")" 2>/dev/null || true
  printf '%s\n' "$af_arch_now" > "$AF_ARCH_STAMP" 2>/dev/null || true
fi

# --- Notice when the base image's python major changes (docs/decisions/0068 decision 4) ---
# Same mechanism as the arch stamp above, with one essential difference: it deletes nothing.
#
# `pip install --user` output lives in `~/.local/lib/python<major>/site-packages`. When python
# moves 3.11 → 3.13 it is not deleted but becomes invisible (the new python looks in another
# directory). Extensions carry an ABI tag (`…cpython-311-….so`), so moving them over does not
# work; they need reinstalling. And the launchers in `~/.local/bin` use `#!/usr/bin/python3`,
# so they still start and fail at once with ModuleNotFoundError under the new python — "it
# worked yesterday", with no cause shown anywhere. Hence the notice.
#
# Reinstalling is not done here: it needs the network at start, takes minutes, and silently
# resolves different versions — the same line the arch self-repair draws by not deleting
# tools the user installed into `~/.local`.
# This is not an architecture event: every amd64 member also hits it once, on the first
# start of a new image, so it cannot live in the block above.
af_py_now="$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])' 2>/dev/null || true)"
AF_PY_STAMP="$HOME/.local/share/agent-fleet/python-major"
af_py_was="$(cat "$AF_PY_STAMP" 2>/dev/null || true)"
af_py_changed=0
if [ -n "$af_py_now" ] && [ -n "$af_py_was" ] && [ "$af_py_was" != "$af_py_now" ]; then
  af_py_changed=1
  echo "[entrypoint] python: ベースの python が $af_py_was から $af_py_now に上がりました"
  af_py_old_sp="$HOME/.local/lib/python$af_py_was/site-packages"
  af_py_pkgs=""
  if [ -d "$af_py_old_sp" ]; then
    # dist-info directories are named `<name>-<version>.dist-info`; take the name only.
    # dist-info names normalise `-` to `_` (cfn-lint → cfn_lint), so map back to the PEP 503
    # canonical form (`-`). pip accepts either, but the user would otherwise be shown a
    # name that a PyPI search does not find.
    for d in "$af_py_old_sp"/*.dist-info; do
      [ -d "$d" ] || continue
      b="$(basename "$d" .dist-info)"
      af_py_pkgs="$af_py_pkgs $(printf '%s' "${b%-*}" | tr '_.' '--')"
    done
  fi
  if [ -n "$af_py_pkgs" ]; then
    # shellcheck disable=SC2086 # intentional word splitting (collapses the whitespace onto one line)
    echo "[entrypoint] python: ⚠️ 次は python$af_py_was 用のまま残っており、$af_py_now からは見えません:"
    echo "[entrypoint] python:   $(echo $af_py_pkgs)"
    echo "[entrypoint] python:   入れ直す: pip install --user --force-reinstall $(echo $af_py_pkgs)"
    echo "[entrypoint] python:   （古い方は消していません。要らなければ rm -rf $af_py_old_sp）"
  fi
  if [ -d "$HOME/.local/share/uv/tools" ]; then
    af_uv_tools=""
    for d in "$HOME"/.local/share/uv/tools/*; do
      [ -d "$d" ] || continue
      af_uv_tools="$af_uv_tools $(basename "$d")"
    done
    if [ -n "$af_uv_tools" ]; then
      echo "[entrypoint] python: ⚠️ uv tool も同じです（venv は起動できるのに import だけ落ちます）:"
      echo "[entrypoint] python:   $(echo $af_uv_tools)"
      echo "[entrypoint] python:   入れ直す: uv tool upgrade --reinstall --all"
    fi
  fi
  echo "[entrypoint] python: ⚠️ ~/repos 配下の .venv も python$af_py_was のままです（~/repos は触っていません）"
fi
if [ -n "$af_py_now" ] && [ "$af_py_was" != "$af_py_now" ]; then
  mkdir -p "$(dirname "$AF_PY_STAMP")" 2>/dev/null || true
  printf '%s\n' "$af_py_now" > "$AF_PY_STAMP" 2>/dev/null || true
fi

# claude records installMethod="native" and self-checks its launcher at
# ~/.local/bin/claude on every start, warning "claude command … missing or broken"
# when it is gone or dangling (e.g. a home from before the node→dev user rename, whose
# launcher points at /home/node/.local/share/claude/…). Removing it isn't enough —
# claude still expects a native install — so REPAIR it via the baked claude
# (`claude install`), which reinstalls a valid ~/.local install (and keeps it
# auto-updatable). This needs /usr/local/bin/claude, so it only runs in a
# BAKE_AGENT_CLIS=1 image, and only for homes with installMethod=native; fresh homes
# there just use the baked claude. In the lean default image there is no /usr/local
# claude and the boot-install below provides ~/.local/bin/claude instead.
# Best-effort (needs network); claude still runs if it fails.
CCD_EARLY="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
if [ -x /usr/local/bin/claude ] && [ ! -e "$HOME/.local/bin/claude" ] \
   && grep -q '"installMethod"[[:space:]]*:[[:space:]]*"native"' "$CCD_EARLY/.claude.json" 2>/dev/null; then
  rm -f "$HOME/.local/bin/claude" # clear a dangling symlink first
  echo "[entrypoint] repairing native claude install (claude install) ..."
  /usr/local/bin/claude install >/dev/null 2>&1 \
    && echo "[entrypoint] claude install ok" \
    || echo "[entrypoint] WARN: claude install failed (using baked /usr/local)"
fi

# gh transparent auth (§8.3): the baked /usr/local/bin/gh is a wrapper that injects the
# same token as git. A real ~/.local/bin/gh left on the home volume comes first on PATH,
# hides the wrapper and breaks transparent auth. If it is not a symlink (i.e. a real
# binary), remove it so PATH reaches the wrapper (the standard image never puts gh in
# ~/.local/bin).
if [ -e "$HOME/.local/bin/gh" ] && [ ! -L "$HOME/.local/bin/gh" ]; then
  echo "[entrypoint] removing shadowing $HOME/.local/bin/gh (use baked gh auth wrapper)"
  rm -f "$HOME/.local/bin/gh"
fi

# Relocate Claude state out of the browsable home BEFORE claude runs (docs/17
# P3-5 stage 2): when CLAUDE_CONFIG_DIR points outside home, migrate a pre-existing
# ~/.claude into it once (must precede claude install/update, which would
# otherwise populate the new dir first and skip the migration). Auth also works
# via the per-session env token, so a glitch here is non-fatal. The Console file
# browser denylists .claude/.claude.json regardless, so this is hardening.
CCD="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
mkdir -p "$CCD"
if [ "$CCD" != "$HOME/.claude" ] && [ -d "$HOME/.claude" ] && [ -z "$(ls -A "$CCD" 2>/dev/null)" ]; then
  echo "[entrypoint] migrating ~/.claude -> $CCD"
  if cp -a "$HOME/.claude/." "$CCD/" 2>/dev/null; then
    rm -rf "$HOME/.claude"
  fi
fi

# --- Move to the scratch disk (ADR 0044 decision 3; docs/log/63 §63.5) -----------
# AF_WS_SCRATCH is a fast task-local disk that vanishes when the container stops. The CP
# injects it only on the ECS runtimes (docker/native bind-mount a host-local disk, so
# there is nothing to gain by moving).
#
# Only move what has many files and is cheap to regenerate. EFS costs a fixed ~14.5 ms per
# file while the bandwidth difference is only ~1 ms per MiB, so only data with a small
# average file size is fatally slow (measured, docs/log/63 §63.4). ~/.npm can be 20 GiB but
# has only 6,756 files, so it stays on EFS — which lets the first npm ci of the morning run
# without network.
#
# If a real directory already exists in home, deleting it on EFS is slow (minutes for tens
# of thousands of files), so move it aside and delete it in the background. Everything here
# is declared safe to delete by the product (home cleanup targets), so it can be
# regenerated if lost.
if [ -n "${AF_WS_SCRATCH:-}" ]; then
  # Decide from the scratch disk's actual size. Fargate's default 20 GiB also holds the image
  # layers and /tmp, and the measured go-build 9 GiB + uv 1 GiB leave no headroom. Only
  # deployments that explicitly enlarged the scratch disk enable this — the disk setting is
  # itself the switch, and a default deployment keeps everything on EFS.
  scratch_kb=$(df -Pk "$AF_WS_SCRATCH" 2>/dev/null | awk 'NR==2{print $2}')
  scratch_min_kb=$(( ${AF_WS_SCRATCH_MIN_GB:-30} * 1024 * 1024 ))
  if [ -z "$scratch_kb" ] || [ "$scratch_kb" -lt "$scratch_min_kb" ]; then
    echo "[entrypoint] scratch: 作業ディスクが小さいため退避しません（$(( ${scratch_kb:-0} / 1048576 )) GiB < ${AF_WS_SCRATCH_MIN_GB:-30} GiB）"
  elif mkdir -p "$AF_WS_SCRATCH/home" 2>/dev/null && [ -w "$AF_WS_SCRATCH/home" ]; then
    # Defaults: the go build cache, the go module cache and uv — measured to have by far
    # the most files (uv: 100k files in 1 GiB).
    for rel in ${AF_WS_SCRATCH_DIRS:-.cache/go-build .cache/uv go/pkg/mod}; do
      src="$HOME/$rel"; dst="$AF_WS_SCRATCH/home/$rel"
      mkdir -p "$dst" 2>/dev/null || continue
      if [ -L "$src" ]; then
        [ "$(readlink "$src")" = "$dst" ] && continue
        rm -f "$src"
      elif [ -e "$src" ]; then
        old="$src.af-old-$$"
        mv "$src" "$old" 2>/dev/null || continue
        echo "[entrypoint] scratch: $rel の旧実体を背景で削除します"
        ( rm -rf "$old" >/dev/null 2>&1 & )
      fi
      mkdir -p "$(dirname "$src")" 2>/dev/null
      ln -s "$dst" "$src" && echo "[entrypoint] scratch: ~/$rel -> $dst"
    done
  else
    # e.g. a mounted EBS volume not owned by dev is not writable. No reason to stop here:
    # keep running on EFS and just log the fact.
    echo "[entrypoint] scratch: $AF_WS_SCRATCH に書けないため退避をスキップします"
  fi
fi

# --- boot-install (lean variant; docs/log/35 §35.4.1 / §35.7.1-6) ----------------
# An image/rootfs built with BAKE_AGENT_CLIS=0 (the default) contains no agent CLIs
# (claude/opencode/codex/copilot/cursor/agy/rtk). Install their versions.json pins (the
# versions e2e-smoke verified) into ~/.local here. Each deployment fetches directly from the
# official distribution (npm / GitHub Releases / Google), so this is not redistribution by
# us (each party accepts the distributor's terms itself). A CLI already present, baked
# (/usr/local/bin, BAKE_AGENT_CLIS=1) or in home (~/.local/bin), is not reinstalled, except
# that the repin below moves a drifted ~/.local copy back to the pin. No network → WARN and
# continue (the Agent still starts, the terminal works, the next start retries).
#
# Inside this section's `( set -e … ) && ok || WARN`, `set -e` has no effect.
# POSIX (and bash and dash — measured on bash 5.2.37 / trixie's dash) ignores -e in every
# command of an AND-OR list except the last, and the whole subshell is the left operand, so
# even an explicit `set -e` inside it is ignored. Measured (ADR 0095 stage 1, gate A):
#
#   ( set -e; echo "0000  f" | sha256sum -c - >/dev/null; echo INSTALLED ) && echo OK
#   → prints "WARNING: 1 computed checksum did NOT match", then INSTALLED and OK
#
# i.e. the sha256 check would be decorative: an artifact that failed it lands in ~/.local
# and is logged as a success. So the check and "do not go past it" are made explicit with
# `|| exit 1` instead of relying on errexit (`exit` works regardless of errexit). Rewriting
# it as `if ( set -e; … ); then` does not fix it — an if condition is also a context where
# -e is ignored.
VJ=/usr/local/share/agent-fleet/versions.json
vj_pin() { node -e 'try{process.stdout.write(String(require(process.argv[1])[process.argv[2]]||""))}catch{}' "$VJ" "$1" 2>/dev/null; }
cli_present() { [ -x "/usr/local/bin/$1" ] || [ -e "$HOME/.local/bin/$1" ]; }
# agy_effective_version — the version of the agy that is actually installed now.
#
# The marker (.agy.version) is the version AF last installed, not the one present now. agy
# can rewrite itself (measured, docs/log/70 §70.14.9: started as 1.1.17, 34 s later it was
# 1.1.19 with `auto_updater.go:305 Spawned background update process` in its log), and the
# marker is AF's own file, so that update does not move it.
#
# The Dockerfile's AGY_CLI_DISABLE_AUTO_UPDATE lock does not make the marker trustworthy:
# an explicit `agy update` by the user, the self-update opt-in (the shadow block below) and
# homes populated by older images all move the binary without moving the marker.
#
# Deciding the repin from the marker alone therefore gets stuck at "marker == pin, binary
# differs". The harm is quiet: if that version changed its output format, the session runs
# but silently on a different model (docs/log/70 §70.14.8).
#
# So always ask the binary. On x86 hosts whose kernel withdrew RDRAND a plain start SIGABRTs
# (decisions/0008), so apply the same OPENSSL_ia32cap mask the Agent applies to every spawn
# (only on hosts without RDRAND, as 0008 decides). arm64 is confirmed
# safe unmasked by the §70.13 measurement (BoringCrypto takes randomness from getrandom(2),
# not the instruction, so RC=0 even on Graviton2, which lacks `rng`).
# The mask is applied per call — exporting it would reach every process, not just agy.
agy_effective_version() {
  local bin="$HOME/.local/bin/agy" v=""
  if [ ! -x "$bin" ]; then :
  elif [ "$(uname -m)" = "aarch64" ] || grep -qw rdrand /proc/cpuinfo 2>/dev/null; then
    v="$(timeout 30 "$bin" --version 2>/dev/null | head -1 | tr -dc '0-9.')"
  else
    v="$(OPENSSL_ia32cap='~0x4000000000000000' timeout 30 "$bin" --version 2>/dev/null | head -1 | tr -dc '0-9.')"
  fi
  [ -n "$v" ] || v="$(cat "$HOME/.local/bin/.agy.version" 2>/dev/null)"
  printf '%s' "$v"
}
# Lean detection: claude is not baked and versions.json has a pin for it → lean variant.
# In lean, the start-time update in the CLAUDE_INSTALL block below is suppressed too, to keep
# the pin (tracking latest is the self-update opt-in's job).
LEAN_CLIS=0
if [ ! -x /usr/local/bin/claude ] && [ -n "$(vj_pin claude)" ]; then LEAN_CLIS=1; fi
if [ "$LEAN_CLIS" = 1 ]; then
  # Say explicitly that this is the lean variant, so agent.log shows why CLIs were or were
  # not downloaded. The first start downloads from npm/GitHub for several minutes; ~/.local
  # persists on the home volume, so later starts (offline restarts included) skip silently
  # via cli_present=true and start immediately. That is by design, not a sign that the CLIs
  # are baked into the rootfs (docs/log/35 §35.7.2-8).
  echo "[entrypoint] lean variant: ensuring pinned agent CLIs under ~/.local (versions.json)"
  # Repin: on a start where the self-update opt-in is OFF, put ~/.local back on the
  # versions.json pin even if an earlier ON moved it ahead. This gives lean the same
  # semantics as the baked variant's "turn it OFF and Stop→Start to return to the baked
  # version". A presence check alone would keep a version advanced under ON forever after
  # OFF (the kiro start guard closes the same hole — docs/log/43 §4-2).
  # An unattended start (AF_AGENT_SELF_UPDATE_SKIP=1) means "leave it alone this time", so
  # it keeps whatever is installed.
  REPIN=0
  if [ "${AF_AGENT_SELF_UPDATE_SKIP:-0}" != "1" ] \
     && { [ "${AF_AGENT_SELF_UPDATE_ALLOWED:-0}" != "1" ] || [ "${AF_AGENT_SELF_UPDATE:-0}" != "1" ]; }; then
    REPIN=1
  fi
  # The 4 npm-distributed CLIs go in a single npm install (prefix=$HOME/.local → ~/.local/bin).
  # Their installed versions for the repin check come from one npm ls (each CLI's own
  # --version takes seconds).
  NPM_LS=""
  if [ "$REPIN" = 1 ]; then
    NPM_LS="$(npm ls -g --prefix "$HOME/.local" --depth=0 --json 2>/dev/null || true)"
  fi
  npm_cur() {
    NPM_LS="$NPM_LS" node -e 'try{const d=JSON.parse(process.env.NPM_LS).dependencies||{};process.stdout.write(((d[process.argv[1]]||{}).version)||"")}catch{}' "$1" 2>/dev/null
  }
  NPM_BOOT=""
  for pair in "claude=@anthropic-ai/claude-code" "opencode=opencode-ai" \
              "codex=@openai/codex" "copilot=@github/copilot"; do
    cli="${pair%%=*}"; pkg="${pair#*=}"; ver="$(vj_pin "$cli")"
    [ -n "$ver" ] || continue
    if ! cli_present "$cli"; then
      NPM_BOOT="$NPM_BOOT ${pkg}@${ver}"
    elif [ "$REPIN" = 1 ] && [ -e "$HOME/.local/bin/$cli" ]; then
      # Move a drifted copy back to the pin. Installs npm does not manage (no version) are left alone.
      cur="$(npm_cur "$pkg")"
      if [ -n "$cur" ] && [ "$cur" != "$ver" ]; then NPM_BOOT="$NPM_BOOT ${pkg}@${ver}"; fi
    fi
  done
  if [ -n "$NPM_BOOT" ]; then
    echo "[entrypoint] boot-install (pinned):$NPM_BOOT ..."
    # shellcheck disable=SC2086
    npm install -g --prefix "$HOME/.local" $NPM_BOOT >/dev/null 2>&1 \
      && echo "[entrypoint] boot-install ok" \
      || echo "[entrypoint] WARN: npm boot-install failed (retrying next start)"
  else
    echo "[entrypoint] boot-install: npm CLIs already present in ~/.local (skip)"
  fi
  # rtk: the pinned GitHub Releases build, checksum-verified (same path as the Dockerfile bake).
  RTK_NEED=0
  if [ -n "$(vj_pin rtk)" ]; then
    if ! cli_present rtk; then
      RTK_NEED=1
    elif [ "$REPIN" = 1 ] && [ -x "$HOME/.local/bin/rtk" ]; then
      rtk_cur="$("$HOME/.local/bin/rtk" --version 2>/dev/null | head -1 | awk '{print $2}')"
      if [ -n "$rtk_cur" ] && [ "$rtk_cur" != "$(vj_pin rtk)" ]; then RTK_NEED=1; fi
    fi
  fi
  if [ "$RTK_NEED" = 1 ]; then
    (
      set -e
      rver="$(vj_pin rtk)"
      arch="$(dpkg --print-architecture 2>/dev/null || uname -m)"
      case "$arch" in
        amd64 | x86_64) asset="rtk-x86_64-unknown-linux-musl.tar.gz" ;;
        arm64 | aarch64) asset="rtk-aarch64-unknown-linux-gnu.tar.gz" ;;
        *) echo "unsupported arch: $arch" >&2; exit 1 ;;
      esac
      base="https://github.com/rtk-ai/rtk/releases/download/v${rver}"
      tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
      cd "$tmp"
      # --retry: a transient blip on first boot otherwise leaves rtk uninstalled
      # until the next start (observed on the WSL2 gate — docs/log/35 §35.9-9).
      curl -fsSL --retry 3 --retry-delay 2 --retry-connrefused "${base}/${asset}" -o "${asset}"
      curl -fsSL --retry 3 --retry-delay 2 --retry-connrefused "${base}/checksums.txt" -o checksums.txt
      grep " ${asset}\$" checksums.txt | sha256sum -c - >/dev/null || exit 1
      tar xzf "${asset}"
      install -D -m 0755 rtk "$HOME/.local/bin/rtk"
      # Keep it only after running it: a matching checksum says nothing about whether the
      # binary's runtime libraries match this image (the arm64 release is a gnu build that
      # needs a recent glibc — docs/log/70 §70.9.2). A binary that fails --version is not
      # left on PATH, where it would fail only when used.
      if ! err="$("$HOME/.local/bin/rtk" --version 2>&1)"; then
        rm -f "$HOME/.local/bin/rtk"
        echo "[entrypoint] rtk はこの環境では動かないため導入しません: $err"
        exit 0
      fi
    ) && echo "[entrypoint] boot-install rtk $(vj_pin rtk)" \
      || echo "[entrypoint] WARN: rtk boot-install failed (retrying next start)"
  elif cli_present rtk; then
    echo "[entrypoint] boot-install: rtk already present (skip)"
  fi
  # agy: the pinned immutable GCS object named by the official installer manifest, fetched
  # and verified via versions.json's agy + agy_build + agy_sha256 (same path as the Dockerfile
  # bake). Also write the self-update version marker, so the pin is not re-fetched right
  # after installing it.
  AGY_NEED=0
  if [ -n "$(vj_pin agy)" ] && [ -n "$(vj_pin agy_build)" ] && [ -n "$(vj_pin agy_sha256)" ]; then
    if ! cli_present agy; then
      AGY_NEED=1
    elif [ "$REPIN" = 1 ] && [ -x "$HOME/.local/bin/agy" ] \
         && [ "$(agy_effective_version)" != "$(vj_pin agy)" ]; then
      AGY_NEED=1
    fi
  fi
  if [ "$AGY_NEED" = 1 ]; then
    (
      set -e
      aver="$(vj_pin agy)"; abuild="$(vj_pin agy_build)"; asha="$(vj_pin agy_sha256)"
      arch="$(dpkg --print-architecture 2>/dev/null || uname -m)"
      case "$arch" in
        amd64 | x86_64) asset="linux-x64/cli_linux_x64.tar.gz" ;;
        arm64 | aarch64) asset="linux-arm/cli_linux_arm64.tar.gz" ;;
        *) echo "unsupported arch: $arch" >&2; exit 1 ;;
      esac
      tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
      cd "$tmp"
      curl -fsSL --retry 3 --retry-delay 2 --retry-connrefused "https://storage.googleapis.com/antigravity-public/antigravity-cli/${aver}-${abuild}/${asset}" -o agy.tgz
      echo "${asha}  agy.tgz" | sha256sum -c - >/dev/null || exit 1
      tar -xzf agy.tgz antigravity
      install -D -m 0755 antigravity "$HOME/.local/bin/agy"
      printf '%s\n' "$aver" > "$HOME/.local/bin/.agy.version"
    ) && echo "[entrypoint] boot-install agy $(vj_pin agy)" \
      || echo "[entrypoint] WARN: agy boot-install failed (retrying next start)"
  elif cli_present agy; then
    echo "[entrypoint] boot-install: agy already present (skip)"
  fi
  # cursor (kind="cursor", docs/log/40): unpack the versioned tarball's Node.js bundle into
  # ~/.local/share/cursor-agent/versions/<version>/ and link ~/.local/bin/cursor-agent (the
  # upstream install.sh layout; same path as the Dockerfile bake). sha256 is checked against
  # versions.json's cursor_sha256 (a per-architecture value written at image build).
  CUR_NEED=0
  if [ -n "$(vj_pin cursor)" ] && [ -n "$(vj_pin cursor_sha256)" ]; then
    if ! cli_present cursor-agent; then
      CUR_NEED=1
    elif [ "$REPIN" = 1 ] && [ -L "$HOME/.local/bin/cursor-agent" ]; then
      # Read the current version from the symlink target's versions/<version>/ (cursor-agent
      # --version takes seconds to start Node, so decide from the path).
      cur_ver="$(readlink "$HOME/.local/bin/cursor-agent" 2>/dev/null | sed -n 's#.*/versions/\([^/]*\)/.*#\1#p')"
      if [ -n "$cur_ver" ] && [ "$cur_ver" != "$(vj_pin cursor)" ]; then CUR_NEED=1; fi
    fi
  fi
  if [ "$CUR_NEED" = 1 ]; then
    (
      set -e
      cver="$(vj_pin cursor)"; csha="$(vj_pin cursor_sha256)"
      dir="$HOME/.local/share/cursor-agent/versions/${cver}"
      # On repin: the self-update (upstream install.sh) only adds the new version in another
      # directory, so the pinned version's tree usually still exists — if so, skip the
      # ~100 MB re-download and just repoint the symlink.
      if [ ! -x "$dir/cursor-agent" ]; then
        arch="$(dpkg --print-architecture 2>/dev/null || uname -m)"
        case "$arch" in
          amd64 | x86_64) casset="x64" ;;
          arm64 | aarch64) casset="arm64" ;;
          *) echo "unsupported arch: $arch" >&2; exit 1 ;;
        esac
        tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
        curl -fsSL --retry 3 --retry-delay 2 --retry-connrefused \
          "https://downloads.cursor.com/lab/${cver}/linux/${casset}/agent-cli-package.tar.gz" -o "$tmp/cursor.tgz"
        echo "${csha}  $tmp/cursor.tgz" | sha256sum -c - >/dev/null || exit 1
        rm -rf "$dir"; mkdir -p "$dir"
        tar --strip-components=1 -xzf "$tmp/cursor.tgz" -C "$dir"
      fi
      mkdir -p "$HOME/.local/bin"
      ln -sf "$dir/cursor-agent" "$HOME/.local/bin/cursor-agent"
      # If the self-update install.sh left an `agent` alias, point it at the same version.
      if [ -L "$HOME/.local/bin/agent" ]; then ln -sf "$dir/cursor-agent" "$HOME/.local/bin/agent"; fi
    ) && echo "[entrypoint] boot-install cursor $(vj_pin cursor)" \
      || echo "[entrypoint] WARN: cursor boot-install failed (retrying next start)"
  elif cli_present cursor-agent; then
    echo "[entrypoint] boot-install: cursor already present (skip)"
  fi
fi

# Kiro CLI (kind="kiro", docs/log/43 Track B / §4-2) is ~855 MB, an order of magnitude larger,
# so unlike the CLIs above it is not boot-installed for every user: `workspace-agent
# install-kiro` installs it into ~/.local, pinned by manifest sha256, the first time a kiro
# session starts (on demand, only for users of kiro). Following the pin for a home install
# (reinstalling when versions.json moves) is done on every start by the kiro start guard's
# `workspace-agent install-kiro --if-needed`, so no 855 MB download hangs off container
# start here. This block only re-applies the self-update lock on every start: kiro has no
# build ENV knob like copilot's COPILOT_AUTO_UPDATE and is stopped by a setting,
# app.disableAutoupdates (~/.kiro/settings/cli.json, plain text), so it is re-pinned on every
# start whether baked (/usr/local, BAKE_AGENT_CLIS=1 only) or installed in home. Silent skip
# when kiro is not installed.
if command -v kiro-cli >/dev/null 2>&1; then
  kiro-cli settings app.disableAutoupdates true >/dev/null 2>&1 || true
  kiro-cli settings chat.disableTrustAllConfirmation true >/dev/null 2>&1 || true
  echo "[entrypoint] kiro: pinned app.disableAutoupdates (version managed by rebuild / on-demand install)"
fi

if [ "${CLAUDE_INSTALL:-1}" = "1" ]; then
  if command -v claude >/dev/null 2>&1; then
    case "$(command -v claude)" in
      "$HOME"/*)
        # User-home install (~/.local) takes PATH precedence → keep it current.
        # A lean boot-installed copy stays on its pin, so no start-time update (tracking
        # latest is the self-update opt-in's job).
        if [ "$LEAN_CLIS" = 1 ]; then
          :
        elif [ "${CLAUDE_AUTO_UPDATE:-1}" = "1" ]; then
          echo "[entrypoint] updating Claude CLI (user install) ..."
          claude update || echo "[entrypoint] WARN: claude update failed (continuing)"
        fi
        ;;
      *)
        # Outside home = baked at /usr/local (BAKE_AGENT_CLIS=1 images only)
        # → version-pinned, no self-update.
        : ;;
    esac
  else
    echo "[entrypoint] installing latest Claude CLI ..."
    curl -fsSL https://claude.ai/install.sh | bash || echo "[entrypoint] WARN: claude install failed (continuing)"
  fi
  if command -v claude >/dev/null 2>&1; then
    echo "[entrypoint] claude $(claude --version 2>/dev/null || echo '?')"
  else
    echo "[entrypoint] WARN: claude not on PATH (sessions will fail until installed)"
  fi
fi

# Agent CLI self-update (opt-in + operator-gated). Where the pinned baseline lives depends
# on how the image was built: with BAKE_AGENT_CLIS=1 the CLIs (claude/opencode/codex/
# copilot), agy and cursor sit at /usr/local, pinned to the image version, and so does rtk
# unless the build set BAKE_RTK=0. In the lean default (BAKE_AGENT_CLIS=0) nothing is at
# /usr/local and the boot-install above put the versions.json pins into ~/.local (see
# "lean variant" below). Both
# gates come from the CP as env at container start: AF_AGENT_SELF_UPDATE_ALLOWED=1 (the
# tenant policy) AND AF_AGENT_SELF_UPDATE=1 (the member's per-workspace opt-in, stored
# in the CP DB so it can be toggled while the container is stopped).
#
# Model with a BAKE_AGENT_CLIS=1 image (all self-updatable tools identical): the baked
# /usr/local copy is the PINNED, IMMUTABLE baseline — self-update never writes it. When
# ON, latest is installed under ~/.local as a PATH-first shadow ("$HOME/.local/bin:$PATH",
# top of file); when OFF the else branch removes that shadow so PATH falls back to the
# /usr/local pin — no container recreate needed. This unifies the npm trio with the
# agy/rtk/cursor shadows below and keeps the known-good baked baseline untouched even if
# an @latest release is broken.
#
# lean variant (the default; no /usr/local bake): the boot-installed copy under ~/.local
# IS the pin, so there is no separate immutable baseline — ON updates it in place.
# Reverting on OFF is the boot-install REPIN's job (it reinstalls the versions.json pin
# over a drifted ~/.local earlier in this script); the shadow cleanup below stays gated on
# the /usr/local pin existing because in lean there is nothing to fall back to.
#
# Unattended starts (AF_AGENT_SELF_UPDATE_SKIP=1): on a start nobody is watching, such as a
# scheduled-run wake, no update runs on this boot even with the opt-in ON. Two reasons:
#   1. Start time: the update runs synchronously before exec workspace-agent, so it adds
#      directly to the /healthz wait (measured cold: 4 CLIs 35 s, agy 15 s, cursor 6 s —
#      about 60 s when all of them run).
#   2. Breakage: pulling an unverified @latest unattended means a breaking change in that
#      release leaves the agent broken going into an unattended run (TUI string contract
#      breakage has recurred in this repo). Updates belong on starts with a person
#      present, i.e. a manual Start.
# This is "skip this time", not OFF. The else branch below (opt-in OFF) removes the
# ~/.local shadow to fall back to the baked version, so hitting it on unattended starts
# would churn a 1.3 GB uninstall→reinstall next time. Hence the separate branch.
if [ "${AF_AGENT_SELF_UPDATE_SKIP:-0}" = "1" ]; then
  echo "[entrypoint] agent self-update: skipped for this boot (unattended start) — keeping installed versions"
elif [ "${AF_AGENT_SELF_UPDATE_ALLOWED:-0}" = "1" ] && [ "${AF_AGENT_SELF_UPDATE:-0}" = "1" ]; then
  echo "[entrypoint] agent self-update: checking versions (member opt-in, operator-allowed) ..."
  # Always target ~/.local (a baked /usr/local pin is immutable; only the PATH-first
  # shadow is updated and compared).
  NPM_PREFIX_DIR="$HOME/.local"
  NPM_PREFIX_ARG="--prefix $HOME/.local"
  # Skip when unchanged: if every package's installed version (in the effective PATH
  # prefix) equals the registry latest, skip the reinstall entirely (tarballs are fetched
  # only on a new release). When it cannot be decided, update.
  NPM_NEED=$(NPM_PREFIX_DIR="$NPM_PREFIX_DIR" node -e '
    const { execSync } = require("child_process");
    const pfx = process.env.NPM_PREFIX_DIR ? " --prefix " + process.env.NPM_PREFIX_DIR : "";
    const run = (c) => execSync(c, { stdio: ["ignore", "pipe", "ignore"] }).toString().trim();
    try {
      const ls = JSON.parse(run("npm ls -g --depth=0 --json" + pfx));
      let need = 0;
      for (const p of ["@anthropic-ai/claude-code", "opencode-ai", "@openai/codex", "@github/copilot"]) {
        const cur = ((ls.dependencies || {})[p] || {}).version || "";
        const latest = run("npm view " + p + " version");
        if (!cur || !latest || cur !== latest) { need = 1; break; }
      }
      process.stdout.write(String(need));
    } catch (e) { process.stdout.write("1"); }
  ' 2>/dev/null || echo 1)
  if [ "$NPM_NEED" = "0" ]; then
    echo "[entrypoint] agent CLIs already latest; skip"
  elif npm install -g $NPM_PREFIX_ARG @anthropic-ai/claude-code@latest opencode-ai@latest @openai/codex@latest @github/copilot@latest >/dev/null 2>&1; then
    echo "[entrypoint] agent CLIs updated${NPM_PREFIX_DIR:+ (~/.local)}: claude $(claude --version 2>/dev/null | head -1) | opencode $(opencode --version 2>/dev/null | head -1) | codex $(codex --version 2>/dev/null | head -1) | copilot $(copilot --version 2>/dev/null | head -1)"
  else
    echo "[entrypoint] WARN: agent CLI update failed (using baked versions)"
  fi
  # agy (Antigravity) follows the same opt-in to latest. It ships via Google's install.sh
  # rather than npm, and a baked copy (BAKE_AGENT_CLIS=1) is in root-owned /usr/local/bin,
  # so it goes into ~/.local/bin and wins on PATH (shadow). Skip when unchanged: take
  # latest from the same distribution manifest install.sh uses (small JSON) and skip the
  # ~187 MB re-download when it matches agy_effective_version() (the binary where it can be
  # asked, otherwise the marker). Comparing the marker alone is wrong both ways: agy's own
  # self-update does not move the marker, so on the pin side (repin) "marker == pin" sticks
  # while the binary moves ahead (docs/log/70 §70.14.9), and here (opt-in ON) a stale marker
  # would re-download ~187 MB on every start even when the binary is already latest.
  # install.sh exits 0 without updating when a binary already exists, so install into an
  # empty temp dir and then swap it in (the previous shadow survives a failure).
  AGY_MARK="$HOME/.local/bin/.agy.version"
  agy_arch="$(dpkg --print-architecture 2>/dev/null || uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  agy_latest="$(curl -fsSL --max-time 15 \
    "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_${agy_arch}.json" 2>/dev/null \
    | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
  if [ -n "$agy_latest" ] && [ -x "$HOME/.local/bin/agy" ] \
     && [ "$(agy_effective_version)" = "$agy_latest" ]; then
    echo "[entrypoint] agy already latest ($agy_latest); skip"
  else
    agy_tmp="$(mktemp -d)"
    if curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir "$agy_tmp" >/dev/null 2>&1 \
       && [ -x "$agy_tmp/agy" ]; then
      install -D -m 0755 "$agy_tmp/agy" "$HOME/.local/bin/agy"
      if [ -n "$agy_latest" ]; then printf '%s\n' "$agy_latest" > "$AGY_MARK"; else rm -f "$AGY_MARK"; fi
      echo "[entrypoint] agy updated: ${agy_latest:-latest}"
    else
      echo "[entrypoint] WARN: agy update failed (using $([ -x "$HOME/.local/bin/agy" ] && echo previous || echo baked) version)"
    fi
    rm -rf "$agy_tmp"
  fi
  # rtk follows the same opt-in to latest. A baked /usr/local/bin/rtk (BAKE_AGENT_CLIS=1 and
  # BAKE_RTK=1) is root-owned and cannot be overwritten, so the latest release goes into
  # ~/.local/bin and wins on PATH (same shape as claude's user install). Checksum-verified;
  # failure is soft (continue on the current version). Turning the opt-in OFF makes the
  # branch below remove this shadow and fall back to the baked version, when there is one.
  # Skip when unchanged: take the latest tag from GitHub's /releases/latest redirect and
  # skip the download when it matches the PATH-first `rtk --version` (shadow or baked).
  rtk_latest="$(curl -fsSI -o /dev/null -w '%{redirect_url}' --max-time 15 \
    https://github.com/rtk-ai/rtk/releases/latest 2>/dev/null | sed -n 's#.*/tag/v##p')"
  rtk_cur="$(rtk --version 2>/dev/null | head -1 | awk '{print $2}')"
  if [ -n "$rtk_latest" ] && [ "$rtk_cur" = "$rtk_latest" ]; then
    echo "[entrypoint] rtk already latest ($rtk_cur); skip"
  else (
    set -e
    arch="$(dpkg --print-architecture 2>/dev/null || uname -m)"
    case "$arch" in
      amd64 | x86_64) asset="rtk-x86_64-unknown-linux-musl.tar.gz" ;;
      arm64 | aarch64) asset="rtk-aarch64-unknown-linux-gnu.tar.gz" ;;
      *) echo "unsupported arch: $arch" >&2; exit 1 ;;
    esac
    base="https://github.com/rtk-ai/rtk/releases/latest/download"
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    cd "$tmp"
    curl -fsSL "${base}/${asset}" -o "${asset}"
    curl -fsSL "${base}/checksums.txt" -o checksums.txt
    grep " ${asset}\$" checksums.txt | sha256sum -c - >/dev/null || exit 1
    tar xzf "${asset}"
    install -D -m 0755 rtk "$HOME/.local/bin/rtk"
  ) && echo "[entrypoint] rtk updated: $("$HOME/.local/bin/rtk" --version 2>/dev/null | head -1)" \
    || echo "[entrypoint] WARN: rtk update failed (using baked version)"
  fi
  # cursor follows the same opt-in to latest. It ships via upstream's version-pinned
  # install.sh rather than npm; a baked copy (BAKE_AGENT_CLIS=1) lives in root-owned
  # /usr/local, so install into install.sh's default ~/.local and win on PATH (shadow,
  # same shape as agy/rtk). install.sh links ~/.local/bin/{agent,cursor-agent} and unpacks
  # into ~/.local/share/cursor-agent. Turning the opt-in OFF makes the branch below remove
  # this shadow and fall back to the baked version, when there is one. Skip when unchanged:
  # take the latest version from install.sh (a small script with the version embedded)
  # and skip the ~100 MB re-download when it matches the PATH-first `cursor-agent
  # --version` (shadow or baked).
  cursor_latest="$(curl -fsSL --max-time 15 https://cursor.com/install 2>/dev/null \
    | grep -oE 'lab/[0-9][0-9.]*-[a-f0-9]+' | head -1 | sed 's#lab/##')"
  cursor_cur="$(cursor-agent --disable-auto-update --version 2>/dev/null | head -1)"
  if [ -n "$cursor_latest" ] && [ "$cursor_cur" = "$cursor_latest" ]; then
    echo "[entrypoint] cursor already latest ($cursor_cur); skip"
  elif curl -fsSL https://cursor.com/install 2>/dev/null | bash >/dev/null 2>&1 \
       && [ -x "$HOME/.local/bin/cursor-agent" ]; then
    echo "[entrypoint] cursor updated: $(cursor-agent --disable-auto-update --version 2>/dev/null | head -1)"
  else
    echo "[entrypoint] WARN: cursor update failed (using $([ -e "$HOME/.local/bin/cursor-agent" ] && echo previous || echo baked) version)"
  fi
else
  # Opt-in disabled (tenant disallows it or member OFF): rtk / agy shadows an earlier
  # opt-in left in ~/.local/bin hide the baked version on PATH, so remove them, matching
  # the CLIs' "turn it OFF and Stop→Start to return to the baked version" semantics.
  # In lean (nothing baked) ~/.local is the boot-installed copy itself, so it is not
  # deleted — a shadow is cleaned up only when a baked version exists to fall back to.
  # Returning lean to the pin is the boot-install REPIN's job above.
  # The 4 npm CLIs: remove the ~/.local shadow only when the baked pin (/usr/local) exists,
  # so PATH falls back to it at once (never in lean, where ~/.local is the pin itself).
  if [ -x /usr/local/bin/claude ]; then
    npm uninstall -g --prefix "$HOME/.local" \
      @anthropic-ai/claude-code opencode-ai @openai/codex @github/copilot >/dev/null 2>&1 || true
  fi
  if [ -x /usr/local/bin/rtk ]; then rm -f "$HOME/.local/bin/rtk"; fi
  if [ -x /usr/local/bin/agy ]; then rm -f "$HOME/.local/bin/agy" "$HOME/.local/bin/.agy.version"; fi
  # cursor: install.sh creates both the agent and cursor-agent symlinks and the share tree,
  # so remove all of them (only when a baked /usr/local/bin/cursor-agent exists to fall
  # back to).
  if [ -x /usr/local/bin/cursor-agent ]; then
    rm -f "$HOME/.local/bin/cursor-agent" "$HOME/.local/bin/agent"
    rm -rf "$HOME/.local/share/cursor-agent"
  fi
fi

# Seed the default settings.json (only when the file is absent; after that the Console's
# Claude settings are the source of truth).
#   skipDangerousModePermissionPrompt … prevents an accidental exit on the bypass warning
#   remoteControlAtStartup            … Remote Control at start is OFF by default (new workspaces only; the Console setting rules after that)
#   agentPushNotifEnabled             … enables push notifications
#   hooks(PreToolUse/Bash → rtk hook claude) … seeded when rtk is in the container (saves tokens)
SETTINGS="$CCD/settings.json"
mkdir -p "$CCD"
if [ ! -f "$SETTINGS" ]; then
  RTK=0; command -v rtk >/dev/null 2>&1 && RTK=1
  node -e '
    const fs = require("fs"), p = process.argv[1], rtk = process.argv[2] === "1";
    const s = {
      skipDangerousModePermissionPrompt: true,
      remoteControlAtStartup: false,
      agentPushNotifEnabled: true,
    };
    if (rtk) s.hooks = { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: "rtk hook claude" }] }] };
    fs.writeFileSync(p, JSON.stringify(s, null, 2) + "\n");
  ' "$SETTINGS" "$RTK" \
    && echo "[entrypoint] seeded default $SETTINGS (rtk=$RTK)" \
    || echo "[entrypoint] WARN: failed to seed $SETTINGS"
else
  # Existing workspace: if remoteControlAtStartup is unset, add false once to match the OFF
  # default. If the key exists (the user set true/false in the Console), respect it. Once
  # added, the key exists on later starts, so this happens only once.
  node -e '
    const fs = require("fs"), p = process.argv[1];
    let s; try { s = JSON.parse(fs.readFileSync(p, "utf8")); } catch { process.exit(0); }
    if (s && typeof s === "object" && !Array.isArray(s) && !("remoteControlAtStartup" in s)) {
      s.remoteControlAtStartup = false;
      fs.writeFileSync(p, JSON.stringify(s, null, 2) + "\n");
      console.log("[entrypoint] defaulted remoteControlAtStartup=false in existing " + p);
    }
  ' "$SETTINGS" 2>/dev/null || true
fi

# opencode plugins: copy the bundled *.js plugins into the user's opencode plugin
# dir (home, persists): agent-fleet-status.js reports session working/idle state back
# to the agent — the opencode analog of claude's settings.json hooks — and
# agent-fleet-caller.js stamps the calling session on af's MCP tools (#989). Refreshed
# each start so they track the image version. opencode auto-loads
# ~/.config/opencode/plugin/*.js. (rtk.ts is the agent's to seed or remove.)
OC_PLUG_SRC="/usr/local/share/agent-fleet/opencode-plugin"
OC_PLUG_DST="$HOME/.config/opencode/plugin"
if [ -d "$OC_PLUG_SRC" ]; then
  mkdir -p "$OC_PLUG_DST"
  cp -f "$OC_PLUG_SRC"/*.js "$OC_PLUG_DST"/ 2>/dev/null \
    && echo "[entrypoint] seeded opencode status plugin" \
    || echo "[entrypoint] WARN: failed to seed opencode plugin"
fi

# opencode permission config: run fully unattended like claude/codex (the container IS
# the sandbox). The `--auto` launch flag auto-approves most permissions, but NOT
# `external_directory` (access outside the project dir, e.g. ~/repos siblings) — that
# stays "ask" and stalls the TUI on a prompt the Console user can't answer. Set every
# permission to "allow" in ~/.config/opencode/opencode.jsonc, preserving any other keys.
# Best-effort: skips if the file isn't plain JSON (e.g. the user added comments).
OC_CFG="$HOME/.config/opencode/opencode.jsonc"
mkdir -p "$HOME/.config/opencode"
python3 - "$OC_CFG" <<'PY' && echo "[entrypoint] set opencode permission=allow" || echo "[entrypoint] WARN: skipped opencode permission config"
import json, os, sys
p = sys.argv[1]
cfg = {}
if os.path.exists(p):
    try:
        with open(p) as f:
            cfg = json.load(f)
    except Exception:
        sys.exit(1)  # not plain JSON (comments?) — don't clobber
if not isinstance(cfg, dict):
    sys.exit(1)
cfg.setdefault("$schema", "https://opencode.ai/config.json")
perm = cfg.get("permission")
if not isinstance(perm, dict):
    perm = {}
for k in ("edit", "bash", "webfetch", "doom_loop", "external_directory"):
    perm[k] = "allow"
cfg["permission"] = perm
tmp = p + ".af-tmp"
with open(tmp, "w") as f:
    json.dump(cfg, f, indent=2)
os.replace(tmp, p)
PY
# The opencode rtk plugin (rtk.ts) and codex's AGENTS.md rtk block are applied by
# the agent (reconcileAgentRTK in agent_rtk.go) from the durable ~/.config/agent-
# fleet/rtk.json toggle — NOT seeded here — so the Console on/off choice survives
# restarts. The agent runs immediately after this entrypoint (exec workspace-agent).

# Lock cursor's auto-update (docs/log/40 Track B): per the bundle analysis, the background
# self-update is skipped when `disableAutoUpdate || channel==="static"`. AF passes
# --disable-auto-update on every launch path, but stopping the background update when the
# user runs `cursor-agent` directly (which writes a ~/.local copy that moves it off the pin,
# and in a BAKE_AGENT_CLIS=1 image shadows the baked copy on PATH) needs a persistent
# setting: pin channel to "static" in ~/.cursor/cli-config.json. It is re-pinned on every
# start, because a setting pinned once stays flipped if it is later changed. Only the
# channel key is touched and the rest is kept; a non-JSON file is left alone.
# The same config applies to a version the self-update opt-in put into ~/.local; that is
# harmless because the opt-in updates by running install.sh explicitly (only cursor's own
# background update is stopped).
if command -v cursor-agent >/dev/null 2>&1; then
  CUR_CFG="$HOME/.cursor/cli-config.json"
  mkdir -p "$HOME/.cursor"
  python3 - "$CUR_CFG" <<'PY' && echo "[entrypoint] set cursor channel=static (auto-update off)" || echo "[entrypoint] WARN: skipped cursor channel config"
import json, os, sys
p = sys.argv[1]
cfg = {}
if os.path.exists(p):
    try:
        with open(p) as f:
            cfg = json.load(f)
    except Exception:
        sys.exit(1)  # not plain JSON — don't clobber
if not isinstance(cfg, dict):
    sys.exit(1)
if cfg.get("channel") == "static":
    sys.exit(0)  # already fixed
cfg["channel"] = "static"
tmp = p + ".af-tmp"
with open(tmp, "w") as f:
    json.dump(cfg, f, indent=2)
os.replace(tmp, p)
PY
fi

# Placing the Workspace guide and the user instructions is the agent's job
# (reconcileAgentInstructions; docs/log/60 / ADR 0042). This only creates the directories.
#   claude   … /etc/claude-code/CLAUDE.md (managed policy baked into the image; not touched here)
#              + $CLAUDE_CONFIG_DIR/CLAUDE.md (user instructions, written by the agent)
#   codex    … ~/.codex/AGENTS.md (fleet policy + user instructions + rtk, composed by the agent)
#   opencode … ~/.config/opencode/AGENTS.md (fleet policy) + an AF-only file referenced by
#              opencode.json's instructions (user instructions)
#
# Never `cp -f` these files from here: overwriting them erases whatever the user added to
# AGENTS.md outside the markers on every container restart (docs/log/60, harm 1). The agent
# is the single writer: it composes fleet policy +
# user instructions + the rtk block inside markers and preserves everything outside them.
# The agent is exec'd right after this and starts every session itself, so no session ever
# reads a file before it is composed.
mkdir -p "$HOME/.codex" "$HOME/.config/opencode"

# Gradle defaults for a shared, memory-constrained host (seed only when missing, so
# user/project tuning persists). Real harm seen: builds ballooned RAM and the daemon
# stayed resident (Gradle's idle-timeout defaults to 3h). Cap the heap, reap idle
# daemons after 2min, and disable parallelism. Projects can override these in their
# own gradle.properties (project + CLI flags take precedence over $HOME/.gradle).
GRADLE_PROPS="$HOME/.gradle/gradle.properties"
if [ ! -f "$GRADLE_PROPS" ]; then
  mkdir -p "$HOME/.gradle"
  cat > "$GRADLE_PROPS" <<'EOF'
# agent-fleet defaults for a shared, memory-constrained workspace.
# Override per project in the project's own gradle.properties when a build needs more.
org.gradle.jvmargs=-Xmx768m -XX:MaxMetaspaceSize=384m
org.gradle.daemon.idletimeout=120000
org.gradle.parallel=false
org.gradle.workers.max=2
org.gradle.caching=true
EOF
  echo "[entrypoint] seeded $GRADLE_PROPS"
fi

# Toolchains: node (nvm, installed into the home volume) and java (pre-baked
# Temurin). The selection lives per-workspace in toolchains.json, chosen in the
# Console. We apply it HERE so the agent — and every tmux session it spawns —
# inherits JAVA_HOME and the selected node on PATH.
TOOLS="$HOME/.config/agent-fleet/toolchains.json"
NODE_VER=""; JAVA_VER=""; GO_VER=""; TZ_VAL=""
if [ -f "$TOOLS" ]; then
  NODE_VER=$(node -e 'try{process.stdout.write(String((require(process.argv[1]).node)||""))}catch{}' "$TOOLS" 2>/dev/null)
  JAVA_VER=$(node -e 'try{process.stdout.write(String((require(process.argv[1]).java)||""))}catch{}' "$TOOLS" 2>/dev/null)
  GO_VER=$(node -e 'try{process.stdout.write(String((require(process.argv[1]).go)||""))}catch{}' "$TOOLS" 2>/dev/null)
  TZ_VAL=$(node -e 'try{process.stdout.write(String((require(process.argv[1]).timezone)||""))}catch{}' "$TOOLS" 2>/dev/null)
fi

# Timezone (per-user, default JST). Export TZ so the agent — and every session
# label / shell / claude it spawns — uses the user's local time. glibc and Go both
# honor TZ; tzdata is baked into the image. We can't symlink /etc/localtime as a
# non-root user, but TZ alone is sufficient.
[ -n "$TZ_VAL" ] || TZ_VAL="Asia/Tokyo"
if [ -f "/usr/share/zoneinfo/$TZ_VAL" ]; then
  export TZ="$TZ_VAL"
  echo "[entrypoint] TZ=$TZ"
else
  echo "[entrypoint] WARN: unknown timezone '$TZ_VAL' (falling back to UTC)"
fi

# java: point JAVA_HOME at the selected Temurin. JDKs come from the deployment-
# provided /usr/lib/jvm (baked image or local bind-mount) or the per-user home
# volume that `install-jdk` populates — the latter being the only source on ECS,
# where nothing is mounted at /usr/lib/jvm. Search both; if the selection is absent
# everywhere, download it into the home volume now (persists on the volume / EFS, so
# only the first launch pays the download). Soft-fail: no network → keep going.
if [ -n "$JAVA_VER" ]; then
  # Never go back to "glob and take the first". Both locations name JDKs
  # temurin-<major>-jdk-<arch>, and "amd64" sorts before "arm64", so once a home populated
  # on x86 is attached to an arm64 slot, the first match is always the one that cannot run
  # (docs/log/70 §70.5.1; workspace-agent's javaHomeFor follows the same rule). Prefer this
  # architecture's suffix and never take another architecture's.
  find_jh() {
    for d in /usr/lib/jvm "$HOME/.local/share/agent-fleet/jvm"; do
      [ -d "$d" ] || continue
      jh=""
      for c in "$d"/temurin-"$JAVA_VER"-jdk*; do
        [ -d "$c" ] || continue
        case "$c" in
          *-jdk-"$af_arch_now") printf '%s\n' "$c"; return 0 ;;
          *-jdk-amd64 | *-jdk-arm64) continue ;;              # other arch: never take
          *) [ -n "$jh" ] || jh="$c" ;;                       # no suffix: fallback
        esac
      done
      [ -n "$jh" ] && { printf '%s\n' "$jh"; return 0; }
    done
    return 1
  }
  JH=$(find_jh || true)
  if [ -z "$JH" ]; then
    echo "[entrypoint] temurin-$JAVA_VER not present; installing into home volume ..."
    workspace-agent install-jdk "$JAVA_VER" || echo "[entrypoint] WARN: install-jdk $JAVA_VER failed"
    JH=$(find_jh || true)
  fi
  if [ -n "$JH" ]; then
    export JAVA_HOME="$JH"
    export PATH="$JH/bin:$PATH"
    echo "[entrypoint] JAVA_HOME=$JH"
  else
    echo "[entrypoint] WARN: temurin-$JAVA_VER-jdk unavailable"
  fi
fi

# go: point GOROOT at the selected toolchain (docs/log/35 §35.7.2-5). The lean rootfs
# bakes no /usr/local/go — `workspace-agent install-go` puts the pinned version
# under the home volume (go.dev/dl keeps all past releases + sha256, verified).
# "system"/empty keeps the baked go, if any. Soft-fail like the JDK path.
if [ -n "$GO_VER" ] && [ "$GO_VER" != "system" ]; then
  GOROOT_SEL="$HOME/.local/share/agent-fleet/go/$GO_VER"
  if [ ! -x "$GOROOT_SEL/bin/go" ]; then
    BAKED_GO_PIN=$(node -e 'try{process.stdout.write(String(require("/usr/local/share/agent-fleet/versions.json").go||""))}catch{}' 2>/dev/null)
    if [ "$BAKED_GO_PIN" = "$GO_VER" ] && [ -x /usr/local/go/bin/go ]; then
      GOROOT_SEL=/usr/local/go
    else
      echo "[entrypoint] go $GO_VER not present; installing into home volume ..."
      workspace-agent install-go "$GO_VER" || echo "[entrypoint] WARN: install-go $GO_VER failed"
    fi
  fi
  if [ -x "$GOROOT_SEL/bin/go" ]; then
    export GOROOT="$GOROOT_SEL"
    export PATH="$GOROOT_SEL/bin:$PATH"
    echo "[entrypoint] GOROOT=$GOROOT_SEL"
  else
    echo "[entrypoint] WARN: go $GO_VER unavailable"
  fi
fi

# node: install/activate the selected version (home volume → persists).
# "system" / empty keeps the image's base node.
if [ -n "$NODE_VER" ] && [ "$NODE_VER" != "system" ]; then
  export NVM_DIR="$HOME/.nvm"
  if [ ! -s "$NVM_DIR/nvm.sh" ]; then
    echo "[entrypoint] installing nvm ..."
    curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash >/dev/null 2>&1 \
      || echo "[entrypoint] WARN: nvm install failed (continuing)"
  fi
  # Versions are installed by `workspace-agent install-node`, not nvm, for two reasons:
  # 1. the selection works even where nvm itself cannot be installed (no route to GitHub
  # etc.); 2. it is the same path as the Console's install button — the same fix as for
  # the JDK's "selected but never installed" hole (docs/decisions/0068). It installs into
  # nvm's layout, ~/.nvm/versions/node/v<full>, so it coexists with nvm, and returns at once
  # when already installed. It prints the installed bin directory on stdout, which goes
  # first on PATH.
  # Never go back to `ls | tail -1` here: lexical order picked v22.9.0 over newer releases;
  # install-node resolves numerically (the same rule as nodeBinFor in env_toolchains.go).
  af_node_bin="$(workspace-agent install-node "$NODE_VER" 2>/dev/null || true)"
  if [ -n "$af_node_bin" ] && [ -x "$af_node_bin/node" ]; then
    export PATH="$af_node_bin:$PATH"
  fi
  if [ -s "$NVM_DIR/nvm.sh" ]; then
    # shellcheck disable=SC1091
    . "$NVM_DIR/nvm.sh"
    nvm alias default "$NODE_VER" >/dev/null 2>&1
    nvm use "$NODE_VER" >/dev/null 2>&1
  fi
  # Always check that the selected version is what runs. If both the install and `nvm use`
  # fail, `node -v` answers with the image's base node; without this check the log looks
  # successful while a node other than the selected one runs.
  af_node_now="$(node -v 2>/dev/null)"
  case "${af_node_now#v}" in
    "${NODE_VER#v}" | "${NODE_VER#v}".*) echo "[entrypoint] node $af_node_now" ;;
    *) echo "[entrypoint] WARN: node $NODE_VER を選択しましたが、いま走っているのは ${af_node_now:-不明} です（導入に失敗？）" ;;
  esac
fi

# --- Automatic repair after an architecture change (the user's own installs) ------
# By now node / java / go are selected, so the reinstall uses the selected toolchains. The
# JDK and node (selected versions) were already restored by their own blocks; what remains
# is what the user installed themselves, which is af-arch-repair's job.
# When the python major changed in the same start, pip reinstalls are not attempted (the
# same version may not exist for the new python, and it would silently resolve another
# one); that degrades to a notice.
if [ -n "${AF_ARCH_REPAIR_FROM:-}" ] || [ -s "$HOME/.local/share/agent-fleet/arch-repair-npm" ]; then
  AF_REPAIR_PY=$([ "${af_py_changed:-0}" = 1 ] && echo 0 || echo 1) \
    af-arch-repair "${AF_ARCH_REPAIR_FROM:-?}" "$af_arch_now" || true
fi
# Surface what could not be repaired (build outputs under ~/repos, self-built binaries)
# where the user will see it. The [entrypoint] / [arch-repair] output above goes only to
# the container's stdout, i.e. the operator's docker logs; there is no path to the user,
# so without a notification "Exec format error with no known cause" keeps happening
# (docs/decisions/0068 decision 4).
# Nothing to report → nothing happens. It is keyed on the residue's content, so an
# unchanged residue does not pile up and fixing it makes the notice go away
# (arch_residue.go).
workspace-agent notify-arch-residue "${AF_ARCH_REPAIR_FROM:-}" || true

exec "$@"
