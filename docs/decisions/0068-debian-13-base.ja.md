# 0068. コンテナのベースを Debian 13 (trixie) へ上げる——rtk のためではなく、chromium の期限のために

[English](0068-debian-13-base.md) | 日本語

- 状態: **採用・①〜⑥ すべて実施済み（⑤⑥ は実機で実測）**（2026-09-04）。実測は下の
  「実行順」の各行に記録した。ベースを動かす動機と 5 案（A〜E）の比較は
  [docs/70 §70.9.3](../log/70-slot-instance-classes.md)、その 🔴 訂正がこの ADR の直接の引き金。
- 関連: [0018-container-browser-pane.ja.md](0018-container-browser-pane.ja.md)（chromium を
  Playwright 配布ではなく **Debian パッケージで revision まで固定して**採ると決めた記録。
  この ADR の期限はそこから来る）/ [0026-kiro-agent-kind.ja.md](0026-kiro-agent-kind.ja.md)
  （arm64 で musl 変種を選んだ理由）/
  [0045-ec2-persistent-workspace.ja.md](0045-ec2-persistent-workspace.ja.md)（永続 `~` と
  アーキ差し替え。python の移行はここの自己修復に**乗らない**）/
  [0053-cp-arch-and-availability.ja.md](0053-cp-arch-and-availability.ja.md)（CP の 2 アーキ index）

## 背景

Workspace / Control Plane のイメージは最初から `node:22-bookworm-slim`（Debian 12・glibc 2.36）
で、ベースを動かす話が出たのは一度だけ——arm64 で rtk が `GLIBC_2.39 not found` で起動しなかった
ときである（docs/70 §70.9.2）。そのとき比較した 5 案のうち A（trixie 化）は「効くが代償が rtk と
無関係に大きい」として退けられ、代償として 2 つが挙がっていた: **chromium が 151 → 150 へ下がる**
ことと、**python が 3.11 → 3.13 になる**こと。

**2026-09-04 に取り直したら、1 つ目は最初から存在しなかった。** 比較の左右が揃っておらず、
bookworm 側は **security** 索引の版を、trixie 側は **main** 索引の版を読んでいた。同じ索引で
測ると差は無い（security は両スイート・両アーキとも `152.0.7977.75`、main はどちらも
`150.0.7871.100`）。

同時に、時間の側が具体的になった。bookworm は **2026-07-12 に通常のセキュリティ支援を終えて
LTS 隊へ引き渡され**、LTS は 2028-06-30 まで。ブラウザペインは製品機能なので、この判断は
「いずれやる」から「いつまでにやるか」に変わった。

## 決定

**決定 1. 上げる。ただし rtk のためではない。** 動機は chromium の期限（決定 2）であって、
arm64 の rtk が動き出すのは**副次効果**である。⚠️ この区別は保存する価値がある——もし rtk が
動機なら正しい答えは今も B（musl を自前ビルド）で、それは外科的で他に何も壊さない。
**1 つのツールを 1 つのアーキで動かすためにフリート全体を移行させるのは、依然として誤り。**
逆に言えば、この移行が延びても rtk のために前倒しする理由にはならない。

**決定 2. 期限は LTS の終わり（2028-06-30）ではなく、chromium が `bookworm-security` から
落ちる日である。** [0018](0018-container-browser-pane.ja.md) が Debian パッケージの chromium を
（revision 固定 ＋ setuid sandbox の検証込みで）採ると決めている以上、ブラウザペインの寿命は
そのパッケージの寿命そのものである。⚠️ **その日は宣言ではなくビルド失敗として来る**——
2026-09-04 時点で `debian-security-support` の非支援リストに chromium は載っていないが、

- bullseye では **LTS に入る前に**打ち切られた前例がある（Debian bug #1061268・2024-01・
  `120.0.6099.224-1~deb11u1` で停止）。
- bookworm でも既に一度失敗している——2026-08-21 の DLA 4749-1 で **bookworm のビルドだけが
  失敗して trixie だけ出た**（LTS 側の回答は「bookworm の新しい rustc が原因かもしれない」）。

**「LTS は 2028 年まで」を猶予と読まないこと。** 引き金が引かれてから移行を始めると、
セキュリティ更新の止まった chromium を抱えたまま、全メンバーの python を壊す作業をすることになる。

**決定 3. chromium の版差は代償ではなかった。測り方の誤りだった。** 蒸し返し防止のため
理由まで残す: **security 索引と main 索引を突き合わせていた。** 一般化すると——
**Debian の版を比べるときは左右で同じ索引を読むこと。** security スイートは「現行ビルドだけ」を
持ち main とは別の版を指すので、片方を security・片方を main で読むと**存在しない版差が出る**。
残る作業は pin と setuid sandbox 検証の取り直し（Debian revision は `~deb12u1` → `~deb13u1` で
変わる）だけで、**版が退行するという代償は無い。**

**決定 4. 残る唯一の実質的な代償は python 3.11 → 3.13 で、これは「検知して知らせる、
入れ直さない」で扱う。** 永続 `~` に対する ABI 破壊であり、実測すると:

- `~/.local/lib/python3.11/site-packages` の中身は**消えるのではなく見えなくなる**（3.13 は
  `python3.13/site-packages` を見る）。拡張は `cpython-311-…so` の ABI タグ付きなので、
  ディレクトリを寄せても駄目で入れ直しが要る。
- `~/.local/bin` の `#!/usr/bin/python3` ランチャは**残って起動し**、3.13 で即
  `ModuleNotFoundError` になる。症状は「昨日まで動いていた」で、原因はどこにも出ない。
- 利用者自身の `uv tool install`（`~/.local/share/uv/tools/`）も同じ。venv の `bin/python` は
  `python3.11` ではなく **`python3` へのリンク**なので、**リンクが切れるのではなく黙って
  3.13 で起動して import に失敗する**——検知が `test -x` では通ってしまう型である。

扱いは [0045](0045-ec2-persistent-workspace.ja.md) のアーキ刻印と同じ仕掛け（`~` に python の
major を刻み、変わっていたら知らせる）だが、**製品が入れたものを捨てて入れ直す既存の自己修復
とは違い、ここでは何も消さない**。理由は 3 つ: 起動時にネットワークを要求する / 数分かかる /
黙って別バージョンを解決する。entrypoint は**取り残された dist の一覧と、やり直す 1 行**を
出すところまでをやる。

⚠️ **これはアーキのイベントではない。** amd64 のメンバーも全員が新イメージの初回起動で 1 回
踏むので、アーキ変更に紐づく既存の導線には**原理的に乗らない**。リリースノートで別途知らせる。

⚠️ **「入れ直さない」はこの python の話に限る。アーキ変更の側は自動で入れ直す**（`af-arch-repair`）。
最初この 2 つを同じ方針でまとめたが、それは雑だった——難しさが違う。

| | アーキ変更 | python の major 変更 |
|---|---|---|
| 直し方 | **同じ版の別 wheel / 別バイナリ**に置き換わるだけ | その版が新しい python 用に**存在するとは限らない** |
| 解決結果 | 元と一致する | **黙って別バージョンになり得る** |
| 壊れる範囲 | 拡張を持つ dist だけ（実測 35 中 **8**） | **全部**（置き場ごと見えなくなる） |
| 検出 | `.so` のファイル名にアーキが入るので**厳密**（`cpython-311-x86_64-linux-gnu.so`） | 版が違えば全滅なので検出は自明 |

つまりアーキ側は「元の状態を寸分違わず再現する」操作であり、決定 4 が挙げた 3 つの理由
（ネットワーク・時間・別バージョン解決）のうち最後が消える。残る 2 つは JDK / node が
既に起動時にネットワークで入れ直している以上、前例がある。**⚠️ 両方が同時に動いた起動では
pip の入れ直しをしない**（`AF_REPAIR_PY=0`）——同一版が新しい python に無い場合があるため。

**決定 5. Amazon Linux（案 E）の却下は動かない。** 再測でも AL2023 に chromium パッケージは
無く、glibc は 2.34 で Debian 12 より**古い**。**ベースを動かすなら候補は Debian 13 だけ。**

**決定 6. kiro の arm64 を musl から gnu へ戻すのは、この移行に混ぜない。** trixie の glibc 2.41
なら gnu 変種も動くはずだが、musl 変種は現に動いていて検証済みである。同じ変更に入れると、
何かが壊れたときに二分できなくなる。**必要になったら別の変更として。**

**決定 7. 検証は「焼いて起動するまで」で、e2e が緑でも足りない。**
[e2e.yml](../../.github/workflows/e2e.yml) は `BAKE_OPTIONAL_TOOLS=1` の枝しかビルドしないので、
**lean 枝（native rootfs 用）は e2e が一度も通らない**。そこには trixie で確実に落ちる箇所が
ある（下の「実行順」④）。arm64 も QEMU が答えるのは「ロードするか」までである。

## 実行順

前提: **開発ホストでイメージをビルドしない**（フリートごと OOM させる）。実イメージは hosted CI が正。

| | やること | 何が分かるか |
|---|---|---|
| **①** | ベースタグを差し替え、`dev-image.yml` を `platforms=linux/amd64` / `bake_agent_clis=true` で dispatch | apt の解決・python・焼き込み全部。**一番安い信号を最初に** |
| **②** | 同じものを `linux/amd64,linux/arm64` で | **rtk aarch64 が起動するか**（Dockerfile が焼いた後 `rtk --version` を実行して駄目なら消す形なので、イメージが自分で答える） |
| **③** | `gh workflow run e2e.yml --ref <branch>` | L1 smoke（ピン一致・chromium の revision・setuid sandbox・日本語スクショ・2 ページ CDP）→ L2 → L3 |
| **④** | `BAKE_OPTIONAL_TOOLS=0` を 1 本 | **e2e が絶対に通らない枝。** t64 改名（下記）はここに出る |
| **⑤** | 両アーキの golden を焼き直す | golden はアーキ毎に要る（docs/70 §70.6）。⚠️ §70.14.7 の「image が同じだけで選ばれていた」を踏まないこと |
| **⑥** | rtk を arm で使うなら Graviton 実機 | ②の QEMU は「ロードするか」までしか答えない（下の帰結） |

**⑤⑥ の実測（2026-09-04・実 AWS の開発配備・`WsRuntime=ecs-ec2`）**:

- **⑤ golden・両アーキ。** `develop` のイメージで配備を立て直した（`standup.sh --image-tag …`）
  ところ、**CP の自動焼きが誰に言われるでもなく両アーキ分を作った**——`BakeArches()` は
  宣言されたクラスの相異なるアーキであり、`Ec2SlotTypes` は既に `arm` クラスを宣言していた。
  アーキごとに 1 スロット（1a に `m7i.large` / 1c に `m8g.large`）を起こし、通常の Start 経路で
  seed を起動し、その home を snapshot し、**その snapshot から probe のワークスペースが
  実際に起動できて初めて**公開した——x86_64 `snap-070ac5bdc2a768b4a`、arm64
  `snap-0aaee9345bf91879c`。⚠️ どちらも同じ `af-image-fp`（`sha256:4f71ed…`）を持つので、
  §70.14.7 の「image が同じだけで選ばれていた」形はもう起きない。印は**参照文字列ではなく
  プラットフォーム毎のマニフェスト digest の集合＝内容**である。
- **⑥ 実機 Graviton の rtk——動く。** 検査は `deploy/aws/ecs/harness/probe-rtk.sh`。
  `m8g.large` スロット（aarch64・kernel `6.1.182-227.379.amzn2023.aarch64`・Debian 13・
  glibc 2.41）の上で arm64 workspace イメージの中を走らせ、**製品自身の boot-install が
  置いた rtk** を使うために arm64 golden の home を載せた。6 項目すべて ok。うち QEMU が
  答えられなかった 2 つが本題である: **`rtk grep` が子プロセスを起こし、その出力が返った**。
  そして削減が本物だった——**345,786 バイト → 2,754 バイト・`total_saved` +107,758 トークン**、
  **amd64 と数値まで一致**。`rtk hook claude` が実機上で `grep …` を `rtk grep …` に書き換えた。
- **⑥ を製品自身の経路で: arm スロット上の実 claude セッション。** Console からメンバーを
  `arm` クラスへ移してワークスペースを起動したところ `m8g.large` スロットに載り（x86_64 側は
  停止したまま）、コンテナの `/var/lib/af/claude/settings.json` には
  `PreToolUse` / `matcher: "Bash"` の `rtk hook claude` が入っていた。**その生きたコンテナの中**で
  同じ probe を流して 6 項目とも ok。そのうえでセッションへの指示だけで `rtk gain` が動いた——
  **4 → 5 コマンド、`total_saved` 107,758 → 230,356。claude 発の 1 コマンドが入力 122,798
  トークンを 200 に圧縮した（99.8%）**。全文は
  `~/.local/share/rtk/tee/1788534491_ls-hidden.log` へ退避され、これは claude が利用者に
  提示したファイル名そのものである。フックが発火し、rtk が spawn し、削減が本物であることが、
  Graviton の上で端から端まで揃った。
- ⚠️ **フックの matcher は `Bash` であり、ここは測り間違えやすい。** 最初の試行では
  セッションに grep を頼んだところ、claude は正しく答えたのに**カウンタが 1 も動かなかった**——
  Bash ではなく自前の Grep ツールを使ったからで、rtk は何も壊れていない。**ここでの「0 件」は、
  rtk についてと同じくらい「claude がどのツールを選んだか」を語っている**。だから答えが
  それらしいことではなく、Bash 経路を明示的に踏ませてカウンタを読むこと。2 回目（素の
  `grep …`）はコマンド数こそ増えたが削減は 0 だった——出力が 52 行で圧縮する余地が無かった
  ——ので、3 回目は測れるだけの大きさの出力を使った。
- ⚠️ **この経路では rtk はイメージに入っていない。** `dev-deploy.sh` は `BAKE_AGENT_CLIS=0`
  で焼くので `/usr/local/bin/rtk` は無く、entrypoint がピン版
  `rtk-aarch64-unknown-linux-gnu` を `~/.local/bin` へ boot-install する。**素のイメージを
  走らせる検査は rtk を見つけられず、問いに何も答えない**——実測で、この検査の 1 回目が
  まさにそれだった。⑥に答えるのは **boot-install を通った home** の側である。arm64 の seed が
  `[entrypoint] boot-install rtk 0.47.0` を出していること自体が証拠でもある。entrypoint は
  導入後に `--version` を走らせ、**動かなければ理由を出して削除する**からである。
- **この検査は判子ではない。** `--version` にだけ答えてあとは何もできないスタブの `rtk`
  ——まさに「ロードするが spawn できない」形、QEMU が隠していたかもしれない形——に対して
  流すと、1 は通り **2・3・5・6 が落ちる**。中の 2 つは意図的に対で置いてある:
  「在る needle を見つけた」は「無い needle を見つけない」が隣に無いと意味を持たず、
  「rtk の出力の方が小さい」は 1 行のエラーメッセージでも満たされるので、計上された
  差分と並べて初めて読める。

**①〜④ の実測（2026-09-04・hosted CI・すべて成功）**:

- **① amd64**（run 33843064097）: python `3.13.5-1` / git-delta `0.18.2-4+b1` /
  `Chromium 152.0.7977.75 built on Debian GNU/Linux 13 (trixie)`。chromium は pin 一致と
  setuid sandbox の `0:0:4755` を Dockerfile 内の `test` が通した。
- **② arm64**（run 33843699642・QEMU）: 🔴 **rtk aarch64-gnu が起動した。**
  `+ /usr/local/bin/rtk --version` → `err=rtk 0.47.0`——つまり Dockerfile の
  「動かなければ消して理由を残す」分岐に**入らなかった**（`rtk-unavailable` は作られていない）。
  glibc 2.41 ≥ 2.39 が効いている。chromium・python・git-delta も amd64 と同じ。
- **③ e2e**（run 33843817457）: **L1 smoke が NG 0 件で `== smoke OK ==`**。
  `chromium 152.0.7977.75-1~deb13u1` / `tmux 3.5a` / `rtk 0.47.0` / CLI 9 種の版一致、
  `versions.json` の全キー一致、日本語スクリーンショット、setuid helper が唯一の
  setuid 実行ファイルであること、2 ページ同時 CDP まで通過。L2 / L3（ui-e2e）も success。
  L4（live-smoke）は quota を使うので skip。
- **④ lean 枝**（run 33843815539・`BAKE_OPTIONAL_TOOLS=0`）: 成功。t64 改名の 6 個
  （`libasound2t64` / `libglib2.0-0t64` / `libatk1.0-0t64` / `libatspi2.0-0t64` /
  `libatk-bridge2.0-0t64` / `libcups2t64`）が実際に解決・展開されたことをログで確認。
  ⚠️ **この枝は従来 CI から焼けなかった**ので、`release.sh` に `BAKE_OPTIONAL_TOOLS` を通し
  `dev-image.yml` に入力を足して塞いだ（既定は 1 ＝既存の呼び出し側は不変）。

差し替える箇所——**4 箇所ではなく 8 ファイル**:

- `workspace/Dockerfile`（`:8` builder / `:19` runtime）・`control-plane/Dockerfile`（`:33` `:43` `:62`）・
  `deploy/release/native/Dockerfile.afcp`（`:7`）・`deploy/release/native/Dockerfile.console`（`:6`）
- `workspace/jvm.Dockerfile`——`:7` のベースに加えて **`:12` の adoptium apt 行にスイート名が
  リテラルで入っている**（adoptium は trixie スイートを持つ・amd64/arm64 あり）
- `deploy/aws/ecs/harness/probe-agy-arm64.sh`（4 箇所）——**わざと本番のベースを写している**
  プローブなので、追随しなければ検査の意味が消える
- **`NOTICE`（2 箇所）はライセンス文書**——GPL の corresponding-source 条項が「イメージに記録された
  bookworm スイートに対して `apt-get source`」と書いている。ベースと一緒に動かさないと**嘘になる**
- `CHROMIUM_VERSION` → `~deb13u1`。⚠️ **決定 3 の誤りを生んだのは、どの索引を読むかを説明した
  `workspace/Dockerfile` のコメントそのもの**なので、スイート名だけでなくその文も直す

🔴 **t64 改名で lean 枝は確実に落ちる。** `BAKE_OPTIONAL_TOOLS=0` の枝が明示している chromium
実行時ライブラリ 21 個のうち **6 個が trixie に存在しない**——`libasound2` / `libatk-bridge2.0-0` /
`libatk1.0-0` / `libatspi2.0-0` / `libcups2` / `libglib2.0-0`（すべて `…t64`）。apt が大声で
落ちるので静かな事故にはならないが、**e2e はこの枝をビルドしない**ので④が要る。

## 帰結

- **git-delta が入る。** trixie main に `git-delta` がある。「bookworm に無いので意図的に外した」
  （`workspace/Dockerfile` のコメント）が解消し、代償ではなく利得の側に移る。
- **arm64 のメンバーが rtk を使える。** rtk aarch64-gnu の要求は
  `pidfd_getpid` / `pidfd_spawnp` の `GLIBC_2.39` 2 シンボルだけで、trixie は 2.41。
  ②で QEMU 上の実 arm64 イメージが `rtk --version` を成功させ、**⑥で実機 Graviton
  m8g.large が本当に使えることを示した**（上の実測）。⑥ を測るまでここに置いていた
  但し書きは、**QEMU が何の証拠で何の証拠でないか**の記録として残す価値がある——
  QEMU が答えたのは「ロードして版を答えるか」まで、`pidfd_spawnp` はまさに QEMU の
  user-mode が実装していない可能性がある種類の syscall で、⑥ までは rtk が実際に子プロセスを
  起こす経路は踏まれていなかった。だから主張を「動くはず」に留めた（docs/70 §70.9.4 の
  「対応済みと書いてある」と「実際に動く」は別の主張である、の再来を避けるため）。それを
  「動く」に変えたのが ⑥ である。docs/70 §70.3.3 の「arm のメンバーは rtk を使えない」には
  🔴 訂正を添えた。
- **上流の rtk への働きかけは変わらない。** PR #3318 を待つ姿勢はそのままで、**この移行を
  「解決したから取り下げる」根拠に使わない**——musl 変種が無いことは他の利用者にとって
  依然として上流の穴である。
- **全メンバーが python の入れ直しを 1 回だけ踏む**（決定 4）。arm64 だけではない。
- ⚠️ **未検証**: glibc 2.41 は `clone3` / `faccessat2` を通す seccomp を要求する。ECS(AL2023) と
  Fargate は問題ないが、**オンプレ compose 配備の古い docker** が典型的な落ち方である。
  ここからは実機なしに確かめられないので、**リリースノートの最低 docker 版として扱う**。
- 版が上がるもの（すべて前進）: git 2.39 → 2.47 / tmux 3.3a → 3.5a / ripgrep 13 → 14 /
  fd 8.6 → 10.2 / bat 0.22 → 0.25 / jq 1.6 → 1.7.1 / subversion 1.14.2 → 1.14.5 /
  fonts-noto-cjk 2022 → 2024。⚠️ tmux の版文字列の形を見ているコードが 1 箇所ある
  （`workspace/agent/internal/sessionx/session_handlers.go`）ので確認する。
- **native rootfs は無関係。** `deploy/release/native/Dockerfile.tools` は `alpine:3.22` で
  bwrap / git / zstd を静的 musl ビルドしており、rootfs は自分の glibc を持ち歩く。
- 焼き込みの MCP サーバ（cloudwatch-mcp / mcp-proxy-for-aws）は **home ではなくイメージの中**に
  あるので再ビルドで 3.13 に作り直され、どちらも 3.13 を宣言している。⚠️ ただし
  `mcp-proxy-for-aws` には **import 検査が無く `test -x` だけ**で、決定 4 が書いた「黙って
  3.13 で起動して import に失敗する」型を捕まえられない。**移行前に import 検査を足すこと。**

## 退けた案

| | 何をする | なぜ退けたか |
|---|---|---|
| **B. rtk を自前で musl ビルド** | Rust の builder stage を足し `aarch64-unknown-linux-musl` を作る | **rtk が動機なら今もこれが正解。** だが動機は chromium の期限なので、これでは期限に答えられない。⚠️ 将来 B をやるなら **amd64 も musl 自前に揃える**こと（片方だけ自前にするとアーキで違うビルドが走る） |
| **C. 上流に aarch64 musl を出してもらう** | PR #3318 を待つ | **出れば効くが、期限に間に合う保証が無い。** 2026-08-22 から動いていない。こちらから追加で押さない方針は維持（6 件目の issue も +1 コメントも純粋なノイズ） |
| **D. 何もしない（現状）** | arm64 は rtk 無しで出し続ける | rtk については成立するが、**chromium の期限には何も答えない**。決定 2 のとおり期限は予告なく来る |
| **E. Amazon Linux にする** | ベースを AL2023 へ | 決定 5。chromium が無く glibc も**古い**（2.34 < 2.36） |
