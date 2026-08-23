#!/usr/bin/env bash
# Workspace 起動時に最新の Claude CLI を用意してから Agent を起動する。
# claude はイメージに焼き込まず、永続ホーム(~/.local)へ install し、次回以降は
# update で最新化する。ネットワーク不通でも Agent 起動は止めない（端末は使える）。
#
# 制御 env:
#   CLAUDE_INSTALL=0      … claude の用意をスキップ（オフライン/軽量検証）
#   CLAUDE_AUTO_UPDATE=0  … 既存 claude の起動時 update を抑止
set -e
export PATH="$HOME/.local/bin:$PATH"

# --- 資格情報だけ別の永続領域に残す（ADR 0045 決定 3-6・ecs-ec2 ランタイムのみ） --
# AF_WS_KEEP は「home とは別に、確実に生き残る場所」を指す。EC2 プール型では home が
# **単一 AZ の EBS 1 本**になるので、それを失うとログイン情報まで一緒に失う。認証・接続・
# identity（`homeKeep` の 7 つ・合計 100 MiB 未満）だけを EFS 側へ逃がし、home からは
# symlink で見せる。CP が AF_WS_KEEP を注入しないランタイム（docker / native / Fargate）
# では丸ごと no-op で、home の実体はそのまま。
#
# claude/gh などが触る前に済ませる必要があるので、entrypoint の最初に置く。
if [ -n "${AF_WS_KEEP:-}" ] && [ -d "$AF_WS_KEEP" ] && [ -w "$AF_WS_KEEP" ]; then
  AF_KEEP_DIRS="${AF_WS_KEEP_DIRS:-.config .ssh .claude .codex}"
  # keep_dir_exists — その rel が「ディレクトリとして keep 側に実体が要る」方かどうか。
  # ファイル側（.gitconfig 等）は実体が無くてよいので対象外。
  keep_is_dir() { case " $AF_KEEP_DIRS " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }
  for rel in $AF_KEEP_DIRS ${AF_WS_KEEP_FILES:-.git-credentials .gitconfig .claude.json}; do
    src="$HOME/$rel"; dst="$AF_WS_KEEP/$rel"
    if [ -L "$src" ] && [ "$(readlink "$src")" = "$dst" ]; then
      # 既に正しい symlink。**ただし向き先が無いことがある。** golden snapshot から作った
      # home は種が張った symlink を丸ごと持ってくるのに、keep 側（EFS）は新規ユーザーごとに
      # 空だからである。ここで作らずに素通りすると `~/.config` は宙に浮いたままになり、
      # 後段の `mkdir -p "$HOME/.config/opencode"` が **File exists** で落ちて、`set -e` で
      # entrypoint ごと死ぬ —— タスクが延々と再起動するだけで、原因はどこにも出ない。
      # （実機で踏んだ: 本番配備の golden 初号機が起動不能になった。）
      keep_is_dir "$rel" && mkdir -p "$dst" 2>/dev/null || true
      continue
    fi
    if [ -e "$src" ] || [ -L "$src" ]; then
      # home 側に実体がある。初回の移行のほか、**書き込みが symlink を実体で置き換えた**
      # 後（tmp へ書いて rename する実装がこれをやる）にもここへ来るので、新しい方を残す。
      if [ ! -e "$dst" ] || [ "$src" -nt "$dst" ]; then
        rm -rf "$dst" 2>/dev/null || true
        mv "$src" "$dst" 2>/dev/null || { echo "[entrypoint] keep: $rel を退避できませんでした"; continue; }
      else
        rm -rf "$src" 2>/dev/null || continue
      fi
    fi
    keep_is_dir "$rel" && mkdir -p "$dst" 2>/dev/null || true
    # ファイル側は実体が無くても dangling symlink を張っておく: 後から普通に書けば
    # EFS 側にできる（O_CREAT は symlink を辿る）。
    ln -sfn "$dst" "$src" && echo "[entrypoint] keep: ~/$rel -> $dst"
  done
fi

# --- home が別アーキの上に載ったときの自己修復（docs/70 §70.5） ---------------
# home（`~`）は永続する。ecs-ec2 では EBS 1 本が人について回り、docs/70 で「どの箱に
# 載るか」が per-member の設定になるので、**x86 で埋めた home が arm64 のスロットに付く**
# ことが起こり得る。そのときファイルシステムは正常にマウントされ、壊れるのはバイナリ
# だけである——症状は「昨日まで動いていた claude が Exec format error」で、原因（箱が
# 変わった）はどこにも出ない。
#
# そこで `~` にアーキの刻印を置き、変わっていたら**製品が入れたものだけ**を捨てて、
# 下の boot-install に入れ直させる。刻印が無い home（この変更より前からある home）は
# 「いまのアーキで作られた」とみなして刻むだけ——それが唯一安全な既定である。
#
# ⚠️ 捨てる対象は下の boot-install ブロックが入れるものと 1:1 で対応する。
#    CLI を足したらここにも足すこと（docs/70 §70.5 の表）。
# ⚠️ `~/repos` には絶対に触らない（利用者の未コミットの作業がある）。`~/.local/bin` に
#    利用者が自分で入れたツールも消さない——消せば「勝手に消えた」になる。壊れている
#    事実だけ伝えて、入れ直すかは本人に委ねる。
af_arch_now="$(dpkg --print-architecture 2>/dev/null || uname -m)"
case "$af_arch_now" in x86_64) af_arch_now=amd64 ;; aarch64) af_arch_now=arm64 ;; esac
AF_ARCH_STAMP="$HOME/.local/share/agent-fleet/arch"
af_arch_was="$(cat "$AF_ARCH_STAMP" 2>/dev/null || true)"
if [ -n "$af_arch_now" ] && [ -n "$af_arch_was" ] && [ "$af_arch_was" != "$af_arch_now" ]; then
  echo "[entrypoint] arch: この home は $af_arch_was で作られ、いま $af_arch_now の上に居ます"
  echo "[entrypoint] arch: アーキ依存の導入物を入れ直します（初回は数分かかることがあります）"
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
  # cursor の `agent` エイリアスは symlink のときだけ落とす（同名の自前スクリプトを
  # 巻き込まないため）。
  if [ -L "$HOME/.local/bin/agent" ]; then rm -f "$HOME/.local/bin/agent"; fi
  # JDK は名前にアーキが入っている（temurin-<major>-jdk-<arch>）ので、他アーキの分だけ
  # 落とせばよい。入れ直しは Console の toolchains / install-jdk の仕事。
  for d in "$HOME"/.local/share/agent-fleet/jvm/temurin-*-jdk-*; do
    [ -d "$d" ] || continue
    case "$d" in *-jdk-"$af_arch_now") continue ;; esac
    rm -rf "$d" && echo "[entrypoint] arch: 削除 ${d#"$HOME"/}"
  done
  echo "[entrypoint] arch: ⚠️ ~/repos 配下の node_modules / target / .venv と、自分で ~/.local へ入れた"
  echo "[entrypoint] arch:    ツールは $af_arch_was 用のままです。使う前に入れ直してください（~/repos は触っていません）"
fi
if [ -n "$af_arch_now" ] && [ "$af_arch_was" != "$af_arch_now" ]; then
  mkdir -p "$(dirname "$AF_ARCH_STAMP")" 2>/dev/null || true
  printf '%s\n' "$af_arch_now" > "$AF_ARCH_STAMP" 2>/dev/null || true
fi

# claude records installMethod="native" and self-checks its launcher at
# ~/.local/bin/claude on every start, warning "claude command … missing or broken"
# when it is gone/dangling. After the node→dev rename that launcher dangled (it
# pointed at the old /home/node/.local/share/claude/…). Removing it isn't enough —
# claude still expects a native install — so REPAIR it via the baked claude
# (`claude install`), which reinstalls a valid ~/.local install (and keeps it
# auto-updatable). Gated on installMethod=native so fresh homes just use the baked
# /usr/local claude. Best-effort (needs network); claude still runs if it fails.
CCD_EARLY="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
if [ -x /usr/local/bin/claude ] && [ ! -e "$HOME/.local/bin/claude" ] \
   && grep -q '"installMethod"[[:space:]]*:[[:space:]]*"native"' "$CCD_EARLY/.claude.json" 2>/dev/null; then
  rm -f "$HOME/.local/bin/claude" # clear a dangling symlink first
  echo "[entrypoint] repairing native claude install (claude install) ..."
  /usr/local/bin/claude install >/dev/null 2>&1 \
    && echo "[entrypoint] claude install ok" \
    || echo "[entrypoint] WARN: claude install failed (using baked /usr/local)"
fi

# gh 透過認証（§8.3）: 焼き込みの /usr/local/bin/gh は git と同一トークンを注入する
# ラッパー。home volume に実体の ~/.local/bin/gh が残っていると PATH 先頭で焼き込み
# ラッパーを隠し、透過認証が効かなくなる。シンボリックリンク以外（=実バイナリ）なら
# 除去して PATH をラッパーへ通す（標準イメージは ~/.local/bin に gh を置かない）。
if [ -e "$HOME/.local/bin/gh" ] && [ ! -L "$HOME/.local/bin/gh" ]; then
  echo "[entrypoint] removing shadowing $HOME/.local/bin/gh (use baked gh auth wrapper)"
  rm -f "$HOME/.local/bin/gh"
fi

# Relocate Claude state out of the browsable home BEFORE claude runs (docs/17
# P3-5 段2): when CLAUDE_CONFIG_DIR points outside home, migrate a pre-existing
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

# --- 作業ディスクへの退避（ADR 0044 決定 3・docs/63 §63.5） -------------------
# AF_WS_SCRATCH が指すのはタスクローカルの速いディスクで、**コンテナ停止で消える**。
# CP は ECS ランタイムのときだけこれを注入する（docker/native はホストのローカル
# ディスクを bind mount しているので、逃がす動機が無い）。
#
# 逃がすのは「ファイル数が多く、再生成が安い」ものだけ。EFS のペナルティは 1 ファイル
# 約 14.5ms 固定で帯域差は 1MiB 約 1ms しか無いため、平均ファイルサイズが小さいものだけが
# 致命的に遅い（実測 docs/63 §63.4）。~/.npm は 20GiB あってもファイル数は 6,756 なので
# **EFS に残す**——残すことで、朝いちばんの npm ci がネットワーク無しで走る。
#
# 既にホーム側に実体がある場合は、EFS 上の削除が遅い（数万ファイルで数分）ので
# 退避してから背景で消す。中身はいずれも製品が「消してよい」と宣言しているもの
# （home 掃除の対象）なので、失われても再生成できる。
if [ -n "${AF_WS_SCRATCH:-}" ]; then
  # 退避先の実サイズを見てから決める。Fargate 既定の 20 GiB にはイメージ層と /tmp も
  # 同居しており、実測の go-build 9GiB + uv 1GiB を足すと余裕が無い。作業ディスクを
  # 明示的に広げたデプロイでだけ有効にする——つまり **ディスクの設定そのものが
  # このスイッチ**で、既定のままのデプロイは従来どおり全部 EFS で動く。
  scratch_kb=$(df -Pk "$AF_WS_SCRATCH" 2>/dev/null | awk 'NR==2{print $2}')
  scratch_min_kb=$(( ${AF_WS_SCRATCH_MIN_GB:-30} * 1024 * 1024 ))
  if [ -z "$scratch_kb" ] || [ "$scratch_kb" -lt "$scratch_min_kb" ]; then
    echo "[entrypoint] scratch: 作業ディスクが小さいため退避しません（$(( ${scratch_kb:-0} / 1048576 )) GiB < ${AF_WS_SCRATCH_MIN_GB:-30} GiB）"
  elif mkdir -p "$AF_WS_SCRATCH/home" 2>/dev/null && [ -w "$AF_WS_SCRATCH/home" ]; then
    # 既定は go のビルドキャッシュ・モジュールキャッシュと uv。いずれも実測で
    # ファイル数が飛び抜けて多い（uv は 1GiB に 10 万ファイル）。
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
    # EBS をマウントした場合など、所有者が dev でないと書けない。ここで止める理由は
    # 無いので、EFS のまま（＝従来どおり）動かして事実だけ残す。
    echo "[entrypoint] scratch: $AF_WS_SCRATCH に書けないため退避をスキップします"
  fi
fi

# --- boot-install（lean 配布 variant、docs/35 §35.4.1 / §35.7.1-6） -----------
# BAKE_AGENT_CLIS=0 で焼いたイメージ/rootfs はエージェント CLI
# （claude/opencode/codex/copilot/cursor/agy/rtk）を含まない。ここで versions.json の
# ピン版（= e2e-smoke で動作検証した版）を ~/.local へ導入する。各デプロイ先が
# 公式配布元（npm / GitHub Releases / Google）から直接取得する形なので、当方に
# よる再配布に当たらない（各社が各配布元の規約を自ら受諾する）。焼き込み
# （/usr/local/bin）か home（~/.local/bin）に既に居る CLI は触らない。ネット不通は
# WARN で続行（Agent 起動は止めない — 端末は使える。次回起動時に再試行）。
VJ=/usr/local/share/agent-fleet/versions.json
vj_pin() { node -e 'try{process.stdout.write(String(require(process.argv[1])[process.argv[2]]||""))}catch{}' "$VJ" "$1" 2>/dev/null; }
cli_present() { [ -x "/usr/local/bin/$1" ] || [ -e "$HOME/.local/bin/$1" ]; }
# agy_effective_version — 「いま在る agy は何版か」。
#
# ⚠️ マーカー（.agy.version）は **AF が最後に入れた版**であって、**いま在る版ではない**。
# agy は自分を書き換えることがあり（実測・docs/70 §70.14.9: 1.1.17 で起動した 34 秒後に
# 1.1.19 になり、ログに `auto_updater.go:305 Spawned background update process` が
# 残っていた）、マーカーは AF が書くファイルなのでその更新では動かない。
#
# その自己更新の直接の原因は `AGY_CLI_DISABLE_AUTO_UPDATE=1` という**値の誤り**で、
# 受け付けるのは `true` だけだった（Dockerfile で修正済み）。ここを実体比較のままに
# しておくのは、封殺が外れる経路が他にもあるから: 利用者の明示的な `agy update`、
# 自己更新 opt-in（下の shadow ブロック）、そして**古いイメージで焼かれた home**は
# 封殺が効いていなかった時代の版を抱えたまま永続する。
#
# だから marker だけで repin を判定すると **marker == pin なのに実体が違う**状態が
# 固着する。しかも実害は静かで、その版で出力形式が変わっていれば「セッションは動く
# のに黙って別のモデル」になる（§70.14.8 で実際にそうなった）。
#
# 実体を問えるなら実体を問う。問えないのは RDRAND 非提示の x86 ホストだけで、そこは
# 起動即 SIGABRT する（decisions/0008）ので marker に落ちる。arm64 は §70.13 の実測で
# 安全と確定している（BoringCrypto が乱数を命令でなく getrandom(2) から取るため、
# `rng` を持たない Graviton2 でも RC=0 だった）。
agy_effective_version() {
  local bin="$HOME/.local/bin/agy" v=""
  if [ -x "$bin" ] && { [ "$(uname -m)" = "aarch64" ] || grep -qw rdrand /proc/cpuinfo 2>/dev/null; }; then
    v="$(timeout 30 "$bin" --version 2>/dev/null | head -1 | tr -dc '0-9.')"
  fi
  [ -n "$v" ] || v="$(cat "$HOME/.local/bin/.agy.version" 2>/dev/null)"
  printf '%s' "$v"
}
# lean 判定: claude が焼かれておらず versions.json にピンがある = lean variant。
# lean では下の CLAUDE_INSTALL ブロックの起動時 update も抑止してピン版を維持する
# （最新への追従は self-update opt-in の仕事）。
LEAN_CLIS=0
if [ ! -x /usr/local/bin/claude ] && [ -n "$(vj_pin claude)" ]; then LEAN_CLIS=1; fi
if [ "$LEAN_CLIS" = 1 ]; then
  # lean 配布 variant であることを明示（agent.log で「なぜ DL したか / しなかったか」を
  # 追えるように）。初回起動は npm/GitHub から数分かけて DL するが、~/.local は home
  # ボリュームに永続するので 2 回目以降（オフライン再起動含む）は下の各ブロックが
  # cli_present=true で無音スキップし即起動する。これは設計どおりの正常動作で、
  # 「rootfs に CLI が焼かれている」わけではない（docs/35 §35.7.2-8）。
  echo "[entrypoint] lean variant: ensuring pinned agent CLIs under ~/.local (versions.json)"
  # ピン再固定（repin）: self-update opt-in が OFF のこの起動では、過去の ON が
  # ~/.local を最新へ進めていても versions.json のピン版へ戻す。焼き込み variant の
  # 「OFF に戻して Stop→Start で焼き込み版へ復帰」と同じ意味論を lean にも与える。
  # 従来は cli_present の在/不在ガードだけだったため、一度 ON で進んだ版が OFF に
  # 戻しても永久に残った（kiro の起動ガードで直したのと同型の穴 — docs/43 §4-2）。
  # 無人起動（AF_AGENT_SELF_UPDATE_SKIP=1）は「今回は触らない」意味論なので温存。
  REPIN=0
  if [ "${AF_AGENT_SELF_UPDATE_SKIP:-0}" != "1" ] \
     && { [ "${AF_AGENT_SELF_UPDATE_ALLOWED:-0}" != "1" ] || [ "${AF_AGENT_SELF_UPDATE:-0}" != "1" ]; }; then
    REPIN=1
  fi
  # npm 配布の 4 CLI はまとめて 1 回の npm install（prefix=$HOME/.local → ~/.local/bin）。
  # repin 判定用の導入済み版は npm ls 1 回で取る（CLI 自体の --version は数秒かかる）。
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
      # 進んだ shadow をピンへ戻す。npm 管理でない導入（版が取れない）は触らない。
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
  # rtk: GitHub Releases のピン版（checksum 検証つき — Dockerfile 焼き込みと同じ経路）。
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
      # until the next start (observed on the WSL2 gate — docs/35 §35.9-9).
      curl -fsSL --retry 3 --retry-delay 2 --retry-connrefused "${base}/${asset}" -o "${asset}"
      curl -fsSL --retry 3 --retry-delay 2 --retry-connrefused "${base}/checksums.txt" -o checksums.txt
      grep " ${asset}\$" checksums.txt | sha256sum -c - >/dev/null
      tar xzf "${asset}"
      install -D -m 0755 rtk "$HOME/.local/bin/rtk"
      # ⚠️ 実行して確かめてから残す。arm64 の配布は gnu ビルドだけで GLIBC_2.39 を要求し、
      # このイメージ（Debian 12・glibc 2.36）では **DL も sha256 も通ったうえで起動だけが
      # できない**（実測 2026-08-22・docs/70 §70.9.2）。確かめずに置くと、PATH の先頭に
      # 動かない rtk が居座り、失敗するのは使った瞬間になる。
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
  # agy: 公式installer manifestが示す不変GCS objectのピン版。
  # （versions.json の agy + agy_build + agy_sha256 で取得・検証 — Dockerfile焼き込みと同じ経路）。
  # self-update の版比較マーカーも書いておく（ピン導入直後の無駄な再取得を防ぐ）。
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
      echo "${asha}  agy.tgz" | sha256sum -c - >/dev/null
      tar -xzf agy.tgz antigravity
      install -D -m 0755 antigravity "$HOME/.local/bin/agy"
      printf '%s\n' "$aver" > "$HOME/.local/bin/.agy.version"
    ) && echo "[entrypoint] boot-install agy $(vj_pin agy)" \
      || echo "[entrypoint] WARN: agy boot-install failed (retrying next start)"
  elif cli_present agy; then
    echo "[entrypoint] boot-install: agy already present (skip)"
  fi
  # cursor（kind="cursor"、docs/40）: 版付き tarball の Node.js バンドルを
  # ~/.local/share/cursor-agent/versions/<版>/ へ展開し ~/.local/bin/cursor-agent を張る
  # （上流 install.sh と同レイアウト・Dockerfile 焼き込みと同経路）。sha256 は
  # versions.json の cursor_sha256（arch 依存の焼き込み値）で検証。
  CUR_NEED=0
  if [ -n "$(vj_pin cursor)" ] && [ -n "$(vj_pin cursor_sha256)" ]; then
    if ! cli_present cursor-agent; then
      CUR_NEED=1
    elif [ "$REPIN" = 1 ] && [ -L "$HOME/.local/bin/cursor-agent" ]; then
      # 現在版は symlink 先の versions/<版>/ から取る（cursor-agent --version は
      # Node 起動で数秒かかるためパスで判定）。
      cur_ver="$(readlink "$HOME/.local/bin/cursor-agent" 2>/dev/null | sed -n 's#.*/versions/\([^/]*\)/.*#\1#p')"
      if [ -n "$cur_ver" ] && [ "$cur_ver" != "$(vj_pin cursor)" ]; then CUR_NEED=1; fi
    fi
  fi
  if [ "$CUR_NEED" = 1 ]; then
    (
      set -e
      cver="$(vj_pin cursor)"; csha="$(vj_pin cursor_sha256)"
      dir="$HOME/.local/share/cursor-agent/versions/${cver}"
      # repin 時: self-update（上流 install.sh）は新版を別ディレクトリに足すだけで
      # ピン版の展開先は残っているのが普通 — 残っていれば ~100MB の再取得を省いて
      # symlink の張り替えだけで戻す。
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
        echo "${csha}  $tmp/cursor.tgz" | sha256sum -c - >/dev/null
        rm -rf "$dir"; mkdir -p "$dir"
        tar --strip-components=1 -xzf "$tmp/cursor.tgz" -C "$dir"
      fi
      mkdir -p "$HOME/.local/bin"
      ln -sf "$dir/cursor-agent" "$HOME/.local/bin/cursor-agent"
      # self-update の install.sh が張る agent エイリアスが残っていれば同じ版へ揃える
      if [ -L "$HOME/.local/bin/agent" ]; then ln -sf "$dir/cursor-agent" "$HOME/.local/bin/agent"; fi
    ) && echo "[entrypoint] boot-install cursor $(vj_pin cursor)" \
      || echo "[entrypoint] WARN: cursor boot-install failed (retrying next start)"
  elif cli_present cursor-agent; then
    echo "[entrypoint] boot-install: cursor already present (skip)"
  fi
fi

# Kiro CLI（kind="kiro"、docs/43 Track B / §4-2）は ~855MB と桁違いに巨大なため、
# 上の CLI 群と違い全ユーザー一律の boot-install は「しない」— kiro を使うユーザーの
# 初回起動時に `workspace-agent install-kiro` が ~/.local へ manifest sha256 ピン付きで
# 導入する（オンデマンド・利用ユーザー限定）。導入済み home 版のピン追従（versions.json
# が上がったら再導入）は kiro 起動ガードの `workspace-agent install-kiro --if-needed` が
# 毎起動見るので、ここでは 855MB の DL を起動時にぶら下げない。ここでやるのは自己更新封殺の毎起動再固定
# だけ: kiro は copilot の COPILOT_AUTO_UPDATE のような build ENV ノブを持たず、
# app.disableAutoupdates（~/.kiro/settings/cli.json・平文）で止める設定型なので、
# 焼き込み（/usr/local・BAKE=1）でも home 導入済みでも毎起動固定する。未導入なら無音スキップ。
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
        # lean の boot-install 品はピン維持なので起動時 update をしない（最新への
        # 追従は self-update opt-in が担う）。
        if [ "$LEAN_CLIS" = 1 ]; then
          :
        elif [ "${CLAUDE_AUTO_UPDATE:-1}" = "1" ]; then
          echo "[entrypoint] updating Claude CLI (user install) ..."
          claude update || echo "[entrypoint] WARN: claude update failed (continuing)"
        fi
        ;;
      *)
        # Baked into the image (/usr/local) → version-pinned, no self-update.
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

# Agent CLI self-update (opt-in + operator-gated). The CLIs (claude/opencode/codex/
# copilot), agy, and rtk are baked at /usr/local, pinned to the image version. Both
# gates come from the CP as env at container start: AF_AGENT_SELF_UPDATE_ALLOWED=1 (the
# tenant policy) AND AF_AGENT_SELF_UPDATE=1 (the member's per-workspace opt-in, stored
# in the CP DB so it can be toggled while the container is stopped).
#
# Model (all self-updatable tools identical): the baked /usr/local copy is the PINNED,
# IMMUTABLE baseline — self-update never writes it. When ON, latest is installed under
# ~/.local as a PATH-first shadow ("$HOME/.local/bin:$PATH", top of file); when OFF the
# else branch removes that shadow so PATH falls back to the /usr/local pin — no container
# recreate needed. This unifies the npm trio with the agy/rtk/cursor shadows below and
# keeps the known-good baked baseline untouched even if an @latest release is broken.
#
# lean variant (no /usr/local bake): the boot-install品 under ~/.local IS the pin, so
# there is no separate immutable baseline — ON updates it in place. Reverting on OFF is
# the boot-install REPIN's job (it reinstalls the versions.json pin over a drifted
# ~/.local earlier in this script); the shadow cleanup below stays gated on the
# /usr/local pin existing because in lean there is nothing to fall back to.
#
# 無人起動の抑止（AF_AGENT_SELF_UPDATE_SKIP=1）: スケジュール実行の wake など「人が
# 見ていない起動」では、opt-in が ON でも今回の boot に限り更新を走らせない。狙いは 2 つ。
#   ① 起動時間: 更新は exec workspace-agent より前の同期処理なので、そのまま /healthz の
#      待ち時間になる（4CLI cold で実測 35s、agy 15s、cursor 6s ＝ 全部走ると約60s）。
#   ② 事故: 未検証の @latest を無人で引くと、その版の破壊的変更でエージェントが動かない
#      まま無人実行に入る（TUI 文字列契約の破損は本リポジトリで再発済み）。更新は人が
#      いる起動＝手動 Start に寄せる。
# ここは「今回はスキップ」であって OFF ではない。下の else（opt-in OFF）は ~/.local の
# shadow を撤去して焼き込み版へ戻す意味論を持つので、無人起動でそれを踏むと 1.3GB の
# uninstall→次回 reinstall のチャーンになる。専用の分岐に分けているのはそのため。
if [ "${AF_AGENT_SELF_UPDATE_SKIP:-0}" = "1" ]; then
  echo "[entrypoint] agent self-update: skipped for this boot (unattended start) — keeping installed versions"
elif [ "${AF_AGENT_SELF_UPDATE_ALLOWED:-0}" = "1" ] && [ "${AF_AGENT_SELF_UPDATE:-0}" = "1" ]; then
  echo "[entrypoint] agent self-update: checking versions (member opt-in, operator-allowed) ..."
  # 常に ~/.local を対象（/usr/local ピンは不変。PATH 先勝ちの shadow だけ更新・版比較）。
  NPM_PREFIX_DIR="$HOME/.local"
  NPM_PREFIX_ARG="--prefix $HOME/.local"
  # 版比較スキップ: レジストリの latest と（PATH 実効 prefix の）導入版が全一致なら再
  # インストールを丸ごと省く（毎起動の tarball 取得を新リリース時だけに）。判定不能時は更新。
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
  # agy (Antigravity) も同じ opt-in で最新へ。npm でなく Google の install.sh 供給で、
  # 焼き込みは root 所有の /usr/local/bin のため ~/.local/bin へ入れて PATH 先勝ちで
  # 差し替える（shadow 方式）。版比較スキップ: install.sh と同じ配布 manifest（軽量
  # JSON）から latest を取り、前回導入時に記録したマーカーと一致なら ~187MB の再取得を
  # 省く。比較は agy_effective_version()（実体を問えるホストでは実体・そうでなければ
  # マーカー）。⚠️ かつてここは marker 決め打ちで、「agy 自身の自己更新で進んでいたら
  # 比較がズレて再導入されるだけで無害」と書いてあった。無害ではなかった——自己更新は
  # marker を動かさないので、ピン側（repin）では marker == pin のまま実体だけが先へ
  # 行って固着する（docs/70 §70.14.9）。ここ（opt-in ON 側）では逆に、実体が既に
  # latest でも marker が古いせいで毎回 ~187MB を取り直していた。
  # install.sh は既存バイナリがあると更新せず即 exit 0 する仕様なので、空の temp dir
  # へ導入してから差し替える（失敗時は旧 shadow 温存）。
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
  # rtk も同じ opt-in で最新へ。焼き込みの /usr/local/bin/rtk は root 所有で上書き
  # できないため、latest release を ~/.local/bin へ入れて PATH 先勝ちで差し替える
  # （claude の user-install と同じ構図）。checksum 検証つき・失敗はソフト（焼き込み
  # 版のまま続行）。OFF に戻すと下の分岐がこの shadow を除去し、焼き込み版へ戻る。
  # 版比較スキップ: GitHub の /releases/latest リダイレクトから latest タグを取り、
  # PATH 先勝ちの `rtk --version`（shadow か焼き込み）と一致なら取得を省く。
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
    grep " ${asset}\$" checksums.txt | sha256sum -c - >/dev/null
    tar xzf "${asset}"
    install -D -m 0755 rtk "$HOME/.local/bin/rtk"
  ) && echo "[entrypoint] rtk updated: $("$HOME/.local/bin/rtk" --version 2>/dev/null | head -1)" \
    || echo "[entrypoint] WARN: rtk update failed (using baked version)"
  fi
  # cursor も同じ opt-in で最新へ。npm でなく上流の版ピン install.sh 供給で、焼き込みは
  # root 所有の /usr/local に置くため、install.sh の既定インストール先 ~/.local へ入れて
  # PATH 先勝ちで差し替える（shadow 方式・agy/rtk と同構図）。install.sh は
  # ~/.local/bin/{agent,cursor-agent} を張り ~/.local/share/cursor-agent へ展開する。
  # OFF に戻すと下の分岐がこの shadow を除去し焼き込み版へ戻る。版比較スキップ:
  # install.sh（軽量スクリプト・版ピン埋め込み）から latest 版を取り、PATH 先勝ちの
  # `cursor-agent --version`（shadow か焼き込み）と一致なら ~100MB の再取得を省く。
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
  # Opt-in が無効（テナント不許可 or メンバー OFF）: 過去の opt-in が残した
  # ~/.local/bin の rtk / agy shadow は焼き込み版を PATH で隠すので除去し、CLI 群と
  # 同じ「OFF に戻して Stop→Start で焼き込み版へ復帰」の意味論に揃える。
  # lean（焼き込みが無い）では ~/.local が boot-install 品そのものなので消さない —
  # 「復帰先の焼き込み版がある時だけ shadow を掃除」に限定する。lean のピン復帰は
  # 上の boot-install の REPIN（ピン版の再導入）が担う。
  # npm 系4CLI: 焼き込みピン(/usr/local)がある時だけ ~/.local の shadow を撤去し、PATH を
  # 焼き込みピンへ即復帰させる（lean=~/.local がピン本体の時は消さない）。
  if [ -x /usr/local/bin/claude ]; then
    npm uninstall -g --prefix "$HOME/.local" \
      @anthropic-ai/claude-code opencode-ai @openai/codex @github/copilot >/dev/null 2>&1 || true
  fi
  if [ -x /usr/local/bin/rtk ]; then rm -f "$HOME/.local/bin/rtk"; fi
  if [ -x /usr/local/bin/agy ]; then rm -f "$HOME/.local/bin/agy" "$HOME/.local/bin/.agy.version"; fi
  # cursor: install.sh は agent/cursor-agent の両シンボリックリンクと share ツリーを
  # 作るので両方畳む（焼き込み /usr/local/bin/cursor-agent がある時のみ = 復帰先あり）。
  if [ -x /usr/local/bin/cursor-agent ]; then
    rm -f "$HOME/.local/bin/cursor-agent" "$HOME/.local/bin/agent"
    rm -rf "$HOME/.local/share/cursor-agent"
  fi
fi

# 既定 settings.json を seed（ファイルが無い時のみ。以後は Console の Claude 設定が真実）。
#   skipDangerousModePermissionPrompt … bypass 警告での誤 exit を防ぐ
#   remoteControlAtStartup            … 起動時の Remote Control は既定 OFF（新規WSのみ。以後は Console 設定が真実）
#   agentPushNotifEnabled             … プッシュ通知を有効化
#   hooks(PreToolUse/Bash → rtk hook claude) … rtk がコンテナにあれば seed（トークン節約）
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
  # 既存WS: remoteControlAtStartup キーが未設定なら一度だけ false を補い、既定 OFF へ揃える。
  # キーが既にある（ユーザーが Console で true/false を明示設定した）場合は尊重して上書きしない。
  # キー補完後は次回以降キーが存在するため再度は触らない＝「一度だけ」。
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

# opencode status plugin: copy the bundled plugin into the user's opencode plugin
# dir (home, persists) so opencode reports session working/idle state back to the
# agent — the opencode analog of claude's settings.json hooks. Refreshed each start
# so it tracks the image version. opencode auto-loads ~/.config/opencode/plugin/*.js.
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

# cursor auto-update 封殺（docs/40 Track B）: バンドル解析で背景自己更新は
# `disableAutoUpdate || channel==="static"` でスキップされる。AF は起動フラグ
# --disable-auto-update を全経路で渡すが、ユーザーが素で `cursor-agent` を叩いた
# 場合の背景更新（~/.local へ home shadow を作り PATH で焼き込みを隠す）まで防ぐには
# 恒久設定が要る。~/.cursor/cli-config.json の channel を "static" に固定する（起動毎
# 再固定 — 5a19080 教訓）。channel 鍵のみ触り他は保存。JSON でなければ触らない。
# self-update opt-in で ~/.local に shadow を入れた版にも同じ config が効くが、opt-in の
# 更新は install.sh 明示実行なので無害（cursor 自身の背景更新だけを止める）。
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

# Workspace 利用ガイドと**ユーザー指示**の配置は agent 側が持つ（docs/60 / ADR 0042 の
# reconcileAgentInstructions）。ここは置き場のディレクトリだけ用意する。
#   claude   … /etc/claude-code/CLAUDE.md（イメージ焼込の managed policy。ここでは触らない）
#              ＋ $CLAUDE_CONFIG_DIR/CLAUDE.md（ユーザー指示・agent が書く）
#   codex    … ~/.codex/AGENTS.md（フリート方針 + ユーザー指示 + rtk を agent が合成）
#   opencode … ~/.config/opencode/AGENTS.md（フリート方針）＋ opencode.json の
#              instructions が指す AF 専用ファイル（ユーザー指示）
#
# ⚠️ ここは以前 `cp -f` でこの 2 ファイルを丸ごと上書きしていた。つまり利用者が
# AGENTS.md へ書き足した文章はコンテナ再起動のたびに黙って消えており、それが
# 「ユーザー層を自力で作れない」原因そのものだった（docs/60 実害①）。いまは agent が
# フリート方針 + ユーザー指示 + rtk ブロックを**1 人の書き手**としてマーカー付きで
# 合成し、マーカー外は温存する。agent はこの直後に exec され、セッションを起こすのは
# その agent 自身なので、合成前のファイルを読むセッションは存在しない。
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
  # ⚠️ 「glob して先頭」に戻さないこと。どちらの置き場も temurin-<major>-jdk-<arch> と
  # いう名前で、"amd64" は "arm64" より先に並ぶ。x86 で埋めた home を arm64 のスロットに
  # 付けた瞬間、先頭は**必ず動かない方**になる（docs/70 §70.5.1・workspace-agent 側の
  # javaHomeFor も同じ規則）。自分のアーキの接尾辞を優先し、他アーキは採らない。
  find_jh() {
    for d in /usr/lib/jvm "$HOME/.local/share/agent-fleet/jvm"; do
      [ -d "$d" ] || continue
      jh=""
      for c in "$d"/temurin-"$JAVA_VER"-jdk*; do
        [ -d "$c" ] || continue
        case "$c" in
          *-jdk-"$af_arch_now") printf '%s\n' "$c"; return 0 ;;
          *-jdk-amd64 | *-jdk-arm64) continue ;;              # 他アーキ: 採らない
          *) [ -n "$jh" ] || jh="$c" ;;                       # 接尾辞なし: 予備
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

# go: point GOROOT at the selected toolchain (docs/35 §35.7.2-5). The lean rootfs
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

# node: install/activate the selected version via nvm (home volume → persists).
# "system" / empty keeps the image's base node.
if [ -n "$NODE_VER" ] && [ "$NODE_VER" != "system" ]; then
  export NVM_DIR="$HOME/.nvm"
  if [ ! -s "$NVM_DIR/nvm.sh" ]; then
    echo "[entrypoint] installing nvm ..."
    curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash >/dev/null 2>&1 \
      || echo "[entrypoint] WARN: nvm install failed (continuing)"
  fi
  if [ -s "$NVM_DIR/nvm.sh" ]; then
    # shellcheck disable=SC1091
    . "$NVM_DIR/nvm.sh"
    nvm install "$NODE_VER" >/dev/null 2>&1 && nvm alias default "$NODE_VER" >/dev/null 2>&1
    nvm use "$NODE_VER" >/dev/null 2>&1
    echo "[entrypoint] node $(node -v 2>/dev/null)"
  fi
fi

exec "$@"
