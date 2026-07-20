# 35. パッケージング & 配布 — 4ターゲット（native / amd64 Linux / EC2-Single / ECS）

> 状態: 📋 設計（本書）→ 実装はフェーズ順（§35.7）/ 対象ブランチ: feat/packaging
> 位置づけ: P3-10「パッケージング & 配布 & アップグレード」（roadmap §12.2）の残作業を
> 4 ターゲットへ具体化する設計。完了判定は各ターゲットの検証ゲート（§35.8）。

## 35.1 現状の棚卸し — 何が既にあり、何が無いか

| ターゲット | いまの配布形 | 状態 |
|---|---|---|
| **amd64 Linux（compose）** | `deploy/compose/release.sh` が「deploy surface tar + air-gap イメージ tar」を生成。P3-10 段5（clean host でバンドルから起動）実証済み | ✅ ほぼ完成。残=チェックサム・版刻印・レジストリ方針 |
| **EC2-Single** | `deploy/aws/ec2-single/cfn.yaml` + compose バンドルを手動 scp。実証済み | △ 動くが、CFN がバンドル外（git clone が要る）|
| **ECS** | `deploy/aws/ecs/cfn/` 4段 + ECR へ手動 tag/push（runbook のコマンド手打ち） | △ 配布物の体裁なし（push スクリプト・タグ規律・更新手順が未整備）|
| **native（WSL）** | 無し。`git clone` + Go/Node ツールチェーン + `run-dev.sh native` のソースビルドのみ | ✗ パッケージ未存在。**本書の最大の新規作業** |

鍵になる既存の性質（パッケージングを軽くする味方）:

- **CP は静的バイナリにできる**（CGO off・modernc.org/sqlite は pure Go・migrations は go:embed 済み。
  `control-plane/Dockerfile` が既にそうしている）→ クロスビルドが自由、native 配布に外部依存なし。
- **workspace-agent も Go 単体**（`workspace/agent`、`run-dev.sh` native が `go build` 一発で作っている）。
- **コアは全ターゲット同一物**（docs/dev/09 §9.2）。差し替わるのは `AF_RUNTIME` 等の seam のみ
  → 配布物の中身はターゲット間でほぼ共有でき、「包み方」だけが違う。
- **docker⇄native はデータ互換**（docs/34 §34.2。同じ `<dataDir>/home` レイアウト）→ native
  パッケージから Docker 構成へ移行してもデータはそのまま。
- **Workspace/CP の Dockerfile は amd64/arm64 両対応済み**（`dpkg --print-architecture` 分岐）。

## 35.2 配布物マトリクス（結論の一覧）

1 リリース = 1 つの `VERSION`（semver、例 `0.2.0`）で以下を生成する。生成器は
§35.6 の release オーケストレータ。

| # | アーティファクト | 中身 | 対象ターゲット |
|---|---|---|---|
| A | `agent-fleet-<v>.tar.gz` | compose deploy surface（compose.yml / Caddyfile / .env.example / backup / restore / load-images / README）**+ `aws/`（ec2-single cfn.yaml・ecs cfn/・release-ecr.sh・README）** | amd64 Linux・EC2-Single・ECS |
| B | `agent-fleet-images-<v>.tar.gz` | `docker save`（CP + Workspace イメージ、air-gap 用）。Workspace は**配布 variant（lean・CLI 抜き）** — §35.4.1 | amd64 Linux・EC2-Single（・ECS も可） |
| C | `agent-fleet-native-<v>-linux-amd64.tar.gz` | `af` ランチャ・`bin/af-cp`・`bin/bwrap`・`bin/git`（いずれも静的）・`rootfs.tar.zst`（**lean rootfs** = workspace イメージの配布 variant。OSS ユーザーランド＝tmux/git/node/chromium 実行時ライブラリ等のみ。エージェント CLI は起動時ピン版インストール、chromium 本体＋CJK フォント・Go toolchain はオンデマンドピン版インストール — §35.3.1・§35.4.1）・`console/`（Vite dist）・`docs/`（ステージング源）・README | native（WSL / 任意の Linux 単一ユーザー）**ホスト追加インストール ゼロ**（§35.3.1） |
| D | `SHA256SUMS` | A〜C のチェックサム | 全部 |

命名・版の規律:

- イメージタグ = `VERSION`（現行 release.sh の流儀を維持）。ECR へは同じタグで push（§35.5.4）。
- バイナリへ `-ldflags "-X main.buildVersion=<v>"` で刻印し、起動ログと `/healthz`（または
  `/api/version`）で確認できるようにする（§35.6.1）。障害報告に「どの版か」を必ず添えられるようにするのが目的。
- ダウングレード非対応（migration 前方のみ）は全ターゲット共通の前提（docs/dev/09 §9.7）。

## 35.3 ターゲット別設計

### 35.3.1 native パッケージ（WSL 主対象・Docker 不要・単一ユーザー・**ホスト依存ゼロ**）

**ゴール**: 「素の WSL ディストロに**何も追加インストールしない**。tar を展開して
`./af start`、ブラウザで `http://localhost:8099`」。Go/Node どころか tmux・git・
claude/codex/opencode・chromium もホストに要求しない。

**方式 = workspace イメージの rootfs 同梱 + 静的 bubblewrap の無特権実行**。
docs/34 §34.5 が予告した「bubblewrap + rootfs 配布（前セッション検討の本命案）」の実行で、
native Runtime の lifecycle（pidfile/State/Stop）はそのまま、Start のプロセス起動だけを
bwrap ラップに差し替える（docs/34 の構造がこのために温存してある）。

**中身**（アーティファクト C）:

```
agent-fleet-native-<v>-linux-amd64/
├── af                    # ランチャ（下記）
├── bin/af-cp             # CGO_ENABLED=0 静的ビルド
├── bin/bwrap             # bubblewrap 静的ビルド（リリース工程で自前ビルド）
├── bin/git               # git 静的ビルド（NO_CURL・CP の内部 git provider 用 → 下記）
├── rootfs.tar.zst        # agent-fleet/workspace イメージの書き出し（buildx -o type=tar）
├── console/              # Vite ビルド済み dist（CONSOLE_DIR で指す）
├── docs/                 # CP イメージが焼くのと同じ docs ツリー（stageWorkspaceDocs の源）
└── README.md             # WSL 前提の導入・制約・更新手順
```

- rootfs は workspace イメージの**配布 variant（lean rootfs）**: workspace-agent・tmux・
  git・gh ラッパー・node・chromium の**実行時ライブラリ群**・fontconfig+DejaVu など
  **OSS のユーザーランドだけ**を焼く。エージェント CLI（claude/codex/opencode/agy/rtk）は
  **焼かず**、entrypoint が初回起動時に `versions.json` の**ピン版（= e2e-smoke で動作
  検証した版）**を仮想 HOME の `~/.local/bin` へインストールし、self-update opt-in
  有効時はそのまま最新へ追従する（§35.4.1 — サイズとライセンスの両方の理由）。
  **chromium 本体＋CJK フォントと Go toolchain も焼かず**、使う人だけがオンデマンドで
  ピン版を導入する（下記）。node は残す（CLI の npm boot-install が使うランタイム依存）。
  native 専用の `bin/workspace-agent` 同梱は不要
  （rootfs が `/usr/local/bin/workspace-agent` を焼済み）。
- **console は同梱ディレクトリ、go:embed はしない**。rootfs で tar が必須になった以上、
  単一バイナリ化の利得は完全に消えた。`CONSOLE_DIR` seam の現状維持がコード変更ゼロ。
- **docs/ を同梱する理由**: native では CP イメージが無いので `bakedDocsDefault` が存在しない。
  `AF_DOCS_DIR` をパッケージ内 `docs/` に向ければ role-scoped ステージング
  （workspace_docs.go）がそのまま生きる。同梱範囲は §35.9-1。
- **arch**: rootfs が arch 別ビルドになるため（Dockerfile 自体は amd64/arm64 対応済み）、
  **amd64 先行**。arm64 は QEMU クロスビルドのホスト負荷が重く、需要が出てから（§35.9-5）。

**実行の仕組み（docker run ↔ bwrap の対応）**:

| docker ランタイム | native full（bwrap） |
|---|---|
| `docker run` イメージ | `bwrap --ro-bind <rootfs展開先> / --tmpfs /tmp …` で rootfs を **read-only** の / に（書込先は home/claude-config の bind と tmpfs のみ） |
| bind-mount `<dataDir>/home` → `/home/dev` | `--bind <dataDir>/home /home/dev`（同一レイアウト＝データ互換維持） |
| `<dataDir>/claude-config` → `/var/lib/af/claude` | `--bind` 同様 |
| docs mount `:ro` | `--ro-bind <dataDir>/docs /usr/local/share/agent-fleet/docs` |
| コンテナ uid=1000 (dev) | userns の単一 uid マッピング（実 uid → 1000。WSL 既定ユーザーは実 uid=1000 なので実質恒等） |
| ネットワーク（`-p 127.0.0.1:<port>`） | net namespace は**共有**（unshare しない）→ agent が host loopback に bind。`AGENT_ADDR` 注入は現行 native と同じ |
| ENTRYPOINT `entrypoint.sh` | **同じものを実行**（root 不要設計・USER dev 前提を確認済み）→ settings seed / opencode plugin / toolchains 適用が**復活**。加えて配布 variant では CLI のピン版 boot-install（下記）が走る |
| `docker stop`（SIGTERM→SIGKILL） | `--unshare-pid --die-with-parent` で bwrap が pid1 → プロセスグループ SIGTERM だけで tmux 含め全滅。現行 native の「tmux ソケット掃除」が不要になる（namespace の /tmp ごと消える） |
| DNS / hosts | `--ro-bind /etc/resolv.conf` `--ro-bind /etc/hosts`（ホストのを透過） |

これで docs/34 §34.5 の制約表のうち「**実行環境はホスト任せ**」「**焼き込みピン止め無し**」
「**entrypoint 初期化なし**」の3行が消える。残るのは「単一ユーザー限定」と
「メモリ上限なし」（無特権では cgroup を切れない）のみ。

**CLI のピン版 boot-install（lean rootfs の要）**: entrypoint には既に
「焼き込み claude が無ければ起動時にインストールする」fallback（`CLAUDE_INSTALL` seam）と、
self-update opt-in の `~/.local/bin` shadow 機構（claude/opencode/codex/rtk/agy）がある。
これを一般化する:

- ビルド時ピン（Dockerfile ARG → `/usr/local/share/agent-fleet/versions.json`）は配布
  variant でも**書き出しだけ行う**（バイナリは焼かない）。ピンの意味は「**動作を保証する版**」
  （e2e-smoke で検証してから bump する現行運用 — docs/dev/10 §10.2.1 — の対象が
  焼き込み版からピン manifest に変わるだけ）。
- entrypoint は各 CLI について「`/usr/local/bin` にも `~/.local/bin` にも無ければ、
  versions.json のピン版を `~/.local/bin` へインストール」する（claude/codex/opencode は
  rootfs 内の node で `npm install -g`（prefix=`~/.local`）、rtk は checksums 検証付き DL、
  agy は install.sh。いずれも既存の self-update 実装と同じ取得経路の版指定つき再利用）。
- 仮想 HOME は永続なので **DL は初回起動の一度きり**。2回目以降はオフラインでも起動する。
- self-update opt-in（`AF_AGENT_SELF_UPDATE`）ON なら従来どおり最新へ追従、OFF なら
  ピン版のまま。書き込み先はすべて bind した仮想 HOME 側なので **ro rootfs のまま成立**する。

なお社内・自社運用の Docker イメージは従来どおり**全焼き込み**を既定のまま維持する
（オフライン即起動・イメージ=検証単位という現行の利点を捨てない）。lean は「**第三者へ
配布する形**」（native tar と air-gap イメージ tar）に適用する variant であり、切替は
Dockerfile の ARG（例 `BAKE_AGENT_CLIS=0`）1 個で行う（§35.4.1 の根拠）。

**chromium のオンデマンドピン版インストール（native rootfs のみ・利用者限定機能の後払い化）**:
ブラウザペインを使うユーザーは限られるため、native rootfs では chromium 本体＋CJK フォント
（合わせて圧縮 200MB 級 — 最大の可変重量物）も焼かず、初回利用時にピン版を導入する。

- **版固定の現状**: 焼き込み版は Debian 3 パッケージの完全一致ピン
  （`CHROMIUM_VERSION=150.0.7871.124-1~deb12u1`・versions.json 記録）で、ブラウザペインの
  CDP 契約（screencast backpressure 対策 — docs/31 検証群）は **Chromium 150 の実測**で
  固めてある。**ユーザー/エージェントが自分で入れる playwright 等の版とは別物**という
  現行の分離は維持する（下記の解決順で保証）。
- **DL 供給元は Debian ピンでは成立しない**: 旧版 deb はアーカイブから消えるため
  （Dockerfile も「両 arch の同一ビルドが出たら ARG bump」運用）、オンデマンドは
  **版が不変 URL で残る配布元**（playwright CDN の版固定 chromium ビルド。linux
  amd64/arm64 両供給・self-contained・agent が既にレイアウトを知っている）から取得し、
  versions.json に **DL 用ピン（`chromium_dl`）** を別途記録する。ピンの意味は CLI と
  同じ「動作を保証する版」＝ブラウザペイン E2E（docs/31）で検証してから bump。
- **導入先と解決順**: `workspace-agent install-chromium`（`install-jdk` の相同）が
  `~/.local/share/agent-fleet/chromium/<pin>/` へ導入（home 永続・docker⇄native 互換・
  `~/.cache/ms-playwright` とは別置き＝ユーザー管理の playwright と混ざらない）。CJK
  フォント（Noto CJK）は fontconfig のユーザーディレクトリ `~/.local/share/fonts` へ
  同時に導入。agent の `findChromiumBinary` の解決順を
  「env → `/usr/bin/chromium`（焼き込み）→ **専用ピンディレクトリ** → playwright cache
  （最後の手段）」に変更する — 現状は焼き込みが在るから PATH 勝ちで分離できているだけで、
  lean では専用ディレクトリを playwright fallback より先に置かないと**未ピン版を拾う穴**が開く。
- **トリガー**: toolchains（JDK）と同じ後入れパターン。ブラウザペイン初回 attach で
  未導入なら agent が自動で `install-chromium` を走らせ、ペインに「準備中（初回のみ
  ~200MB 取得）」を出す。明示導入（コマンド/Console 設定）も可。導入済みなら以後
  オフラインで動く。
- **sandbox の非対称（B で chromium を焼き続ける理由）**: 焼き込み版は SUID
  `chrome-sandbox`（4755）で全環境サンドボックス有効。home への DL 版は SUID を
  持てない。**native（bwrap 無特権 userns 配下）では SUID はどのみち無効**なので、
  DL 化しても条件は悪化しない（namespace sandbox の可否は docs/34 §34.5 の既存未検証
  項目のまま — P2 実機で確認し、不可なら pane 用途限定で `--no-sandbox` + 接続先
  localhost 限定という割り切りを README に明記）。一方 **docker コンテナでは SUID
  sandbox が有効に働いている**ため、air-gap イメージ B から chromium を抜くと
  sandbox 強度が下がる。chromium は OSS で再配布適法なので、**B は焼き込み継続**
  （抜く動機がライセンスでなくサイズだけで、イメージ配布ではサイズ制約が緩い）。

**Go toolchain もオンデマンド（native rootfs のみ）**: Go はプロダクトのランタイムでは
どこにも使われない（entrypoint/agent に go の exec なし。agent が触るのは「ツールの
バージョン」表示の baked パス照会だけ）純粋な利用者向け開発ツールチェーンで、JDK と
同じ位置づけ。しかも供給元 go.dev/dl は**全旧版を恒久保存し sha256 を公開**するため、
オンデマンドのピン供給元として理想的（Debian chromium より条件が良い）。

- `workspace-agent install-go`（`install-jdk` の相同）が versions.json の `GO_VERSION`
  ピンを `~/.local/share/agent-fleet/go/<ver>/` へ展開（sha256 検証・home 永続・
  docker⇄native 互換）。Console の toolchains（Java 版選択の既存 UI）に Go を並べ、
  選択時は entrypoint が未導入分を自動導入して GOROOT/PATH を各セッションへ通す
  （JAVA_HOME と同じ配線）。
- cgo / node-gyp 用の C toolchain（build-essential）は rootfs に**残す**。npm の
  boot-install や利用者ビルドが暗黙に踏むため、ここまで削ると壊れ方が見えにくい
  （さらなる減量候補としては認識するが、実測でサイズが問題になってから）。
- 「ツールのバージョン」表示（env_tool_versions.go）の go 行は、baked パス固定でなく
  on-demand ディレクトリも見るよう P2 で追随させる。

**CP 側の残依存の始末**: CP 自体は静的 Go バイナリだが、内部 git provider が
`git-http-backend`（ホストの git-core）を exec する。ここだけは rootfs の外なので、
**NO_CURL の静的 git を `bin/git` として同梱**し、ランチャが `GIT_HTTP_BACKEND`
（env 上書き実装済み — git_http.go）と PATH 前置で配線する。内部 git はローカル操作 +
CGI のみで https を使わないため、静的ビルドの難所（libcurl+openssl）を丸ごと回避できる。
→ 結果、ホスト要件は「**Linux カーネル（user namespaces）+ 標準ユーザーランド（bash/coreutils）**」だけ。

**カーネル要件と WSL での見立て**（P2 の実機ゲートで確認する仮説）:

- bwrap の無特権実行は unprivileged user namespaces に依存。**WSL2 の標準カーネルは
  AppArmor が無効**なので、Ubuntu 24.04 系の「unprivileged userns 制限」（AppArmor で実装）は
  効かず、**素の WSL2 ではそのまま動く見込み**。
- 素の Ubuntu 23.10+ 実機（非 WSL）では `sysctl kernel.apparmor_restrict_unprivileged_userns=0`
  か AppArmor profile が必要になる場合がある — README に注記（sudo 1 回。恒久化は
  `/etc/sysctl.d/`）。
- 万一 userns が使えないホストへの脱出口は用意しない（proot は桁級に遅く、答えにならない。
  その環境は Docker 構成へ誘導する）。

**ランチャ `af`**（`run-dev.sh native` からビルド工程を除き、rootfs 管理を足した移植）:

| サブコマンド | 動作 |
|---|---|
| `af start` | 初回のみ `rootfs.tar.zst` を `WS_DATA/shared/rootfs/<v>/` へ展開 → preflight（userns 可否チェック。tmux/git/claude の存在 warn は**廃止** — 全部 rootfs 内）→ `AF_RUNTIME=native` / rootfs モード env / `CONSOLE_DIR=<pkg>/console` / `AF_DOCS_DIR=<pkg>/docs` / `GIT_HTTP_BACKEND=<pkg>` 配線 / `WS_DATA=~/.local/share/agent-fleet`（既定）で `bin/af-cp` を exec |
| `af reset [--all]` | `run-dev.sh` の `do_reset` 移植（bwrap 化で tmux 掃除は簡素化: プロセスグループ kill のみ）。`--all` は展開済み rootfs も対象 |
| `af status` | pid ファイル + `/proc` 照合で CP / workspace の生存表示（任意・後回し可） |

- **bind 先は既定 `127.0.0.1:8099`**（run-dev.sh の全 IF bind より狭める。WSL2 の
  localhostForwarding は loopback で機能するので Windows ブラウザからの利用は変わらない）。
- oauth.env は `<pkg>/oauth.env`（あれば）を read。**AUTH は dev 固定**（native factory の
  fail-fast — docs/34 §34.5）。
- systemd user unit の見本を README に載せる（WSL2 は systemd 既定有効）。

**CP 側の実装差分**（P2）: `runtime_native.go` に rootfs モードを追加 —
`AF_NATIVE_ROOTFS=<展開先>` が設定されたら、既存の env 組み立て（明示構築・秘密非継承）は
そのままに、コマンドを `bwrap` ラップ（上表のマウント構成 + `--unshare-pid
--die-with-parent`）で entrypoint.sh 起動へ切り替える。State/pidfile/Stop は共通。
`AF_NATIVE_AGENT_BIN` は従来モード（開発・`run-dev.sh native`）用にそのまま残す。

**サイズ**: lean rootfs は CLI 群（claude/codex/opencode/agy + npm 残渣）・
chromium 本体 + CJK フォント・Go toolchain を落とし、全焼き込み比で**圧縮 600MB 級の
減量**を見込む（残るのは Debian ベース + build-essential + tmux/git + node +
chromium 実行時ライブラリ等で、zstd で 200〜300MB 目安・要実測）。リリース工程では
`docker buildx build -o type=tar`（コンテナ起動不要）で書き出し、zstd 圧縮する。

**否決した代替案**（検討の記録）:

| 案 | 否決理由 |
|---|---|
| rootfs を持たず、静的 tmux/git の同梱だけで済ませる | 自前ビルドの供給網が tmux/git（https 付き）へ広がる・chromium/フォント/node は諦めになる・entrypoint 初期化のパリティが得られない。**CLI の boot-install だけは本設計に採用**したが、実行基盤（ユーザーランド）は rootfs で丸ごと持つのが正しい分割 |
| proot（ptrace ベース・無特権） | syscall フックで桁級に遅い。git/ビルド作業が主用途の本製品では成立しない |
| `wsl --import` で workspace を別ディストロ化 | WSL 専用（汎用 Linux に効かない）・`wsl.exe` interop への依存・別ディストロへの exec を担う新 Runtime アダプタが必要。bwrap 案はコード差分が Start 1 点で済む |
| 全部入り単一バイナリ（console/rootfs を go:embed） | GB 級 embed は非現実的。tar 配布が前提になった時点で無意味 |

**アップデート**: 新 tar 展開 → `af start`（新 rootfs は版別ディレクトリへ展開・旧版は
reset で掃除）。データ（`WS_DATA`）は外にあるので触れない。migration は CP 起動時自動・
前方のみ（更新前バックアップ推奨を README に明記）。

### 35.3.2 amd64 Linux（compose — セルフホスト本命）

現行 `release.sh` の成果物（A+B）が既に答え。残作業は「包みの仕上げ」のみ:

1. **`deploy/aws/` をバンドル A に同梱**（§35.2 表）。これで「git clone せずに EC2-Single /
   ECS まで立てられる」一つの箱になる。release.sh の cp 対象に `deploy/aws/` を足すだけ。
2. **SHA256SUMS 生成**を release.sh に追加。
3. **版刻印**（§35.6.1）を CP/agent のビルドに配線。
4. **レジストリ方針の決定**（§35.9 未決）: 既定は air-gap tar（現状の流儀・グループ会社配布に
   十分）。push 先を持つなら release.sh に `--push`（`REGISTRY=ghcr.io/... docker push`）を
   足すだけの構造には既になっている。
5. **multi-arch イメージは今はやらない**。Dockerfile は arch 対応済みだが、buildx の
   マルチアーチビルドはこのホストのメモリ制約（QEMU エミュレーション込みのビルド）に対して
   重すぎる。イメージは amd64 のみ、native tar だけ両 arch（§35.3.1）という割り切りを明記する。

なお「amd64 Linux で単一ユーザー・Docker なし」なら native パッケージ（C）がそのまま使える
（WSL 専用ではない）。README でその選択肢に触れる。

### 35.3.3 EC2-Single（AWS 上の compose）

形態としては compose の変種（docs/dev/09 §9.1）なので、**専用アーティファクトは作らない**。
残作業は導線の整備:

1. **cfn.yaml をバンドル A の `aws/ec2-single/` に同梱**（§35.3.2-1 に含まれる）。
   これで「手元にバンドルだけあれば VM 提供〜起動まで完結」する。
2. **`ship.sh`（任意・薄い便利スクリプト）**: 現 runbook の「scp 2 ファイル → ssh →
   load-images → 展開」を1コマンド化。中身は runbook のコマンド列そのままで、正は
   README に置いたまま（スクリプトが壊れても手順で立てられる）。
3. **採らない案**（検討済みの否決を記録）:
   - **UserData/S3 での自動ブートストラップ**: `.env`（AF_MASTER_KEY・OAuth secret）を
     UserData や S3 に置くことになり秘密の置き場が悪化する。どのみち `.env` の対話編集が
     必要なので、半自動（provision → scp → ssh で編集・起動）が正しい水準。
   - **AMI 焼き込み（Packer）**: 更新のたびに AMI を焼き直す保守コストに対し、
     「Docker イメージが更新単位」という既存の答えと二重になるだけ。cloud-init での
     Docker 導入（現 cfn.yaml）で十分。

### 35.3.4 ECS（ネイティブ AWS アダプタ）

配布物 = 「**ECR 上のイメージ（タグ=VERSION）+ CFN 4段 + runbook**」。CFN・アダプタ本体は
実装済み（P3-7）なので、残作業は**リリース作法**:

1. **`aws/ecs/release-ecr.sh`（新規）**: runbook §ECR push の手打ちコマンドをスクリプト化。
   - `aws ecr create-repository`（idempotent）→ `get-login-password | docker login` →
     CP/WS 両イメージを `:$VERSION` で tag/push。air-gap tar（B）からの `docker load` →
     push も同スクリプトの入口にする（ビルド環境と push 環境が別でも成立）。
   - 引数は `--profile/--region/--account` と `VERSION`。runbook のコマンド列が正、
     スクリプトはその写し、という関係を README に明記。
2. **イメージ参照の版パラメータ化**: `30-ingress.yaml` の CP イメージと、CP が Workspace に
   使う `WS_IMAGE`（ECR URI）を `:dev` 固定でなく `ImageTag` パラメータ（=VERSION）で受ける。
   アップグレード = 「release-ecr.sh で新タグ push → `aws cloudformation deploy
   --parameter-overrides ImageTag=<v>` → CP サービスが入れ替わる」。Workspace 側は
   アダプタがステートレスに TaskDefinition を作るので、**次回 Start から新イメージ**になる
   （稼働中ワークスペースは巻き込まない — この性質を更新 runbook に明記）。
3. **DeletionPolicy のパラメータ化**: sandbox 既定 Delete のままだと本番で事故る。
   `10-data.yaml` の EFS/RDS に `Retain` を選べる `Persistence` パラメータを足し、
   「本番は Retain+Snapshot」を README の標準にする。
4. **最小 IAM の文書化**（P3-10 の宿題）: デプロイ主体に要る権限一覧を README へ。
5. **Helm chart は明示的にスコープ外へ**: roadmap の記述（k8s 希望社向け）は、AWS の答えを
   ECS+CFN に決めた時点で宙に浮いている。需要が出るまで 📋 のまま棚上げする、と roadmap 側を
   更新する（作らないことを決めるのもパッケージングの一部）。

## 35.4 配布チャネルと air-gap

- **既定は「ファイルで渡す」**: A〜D を成果物ディレクトリ（`deploy/release/dist/`）に集め、
  グループ会社へは任意の手段（社内共有・S3・GitHub Releases）で渡す。phone-home なし・
  中央依存なし（P3-10 の非依存原則）はこの形が最も守りやすい。
- GitHub Releases / GHCR を使う場合も**任意の上乗せ**であって前提にしない（air-gap 導入が
  一級市民のまま）。CI（GitHub Actions）は現在支払い停止で使えないため、**リリースビルドは
  ローカル実行が正**。ビルドは1つずつ直列に（共有ホストのメモリ制約 — console build は
  `NODE_OPTIONS=--max-old-space-size` 指定済み、イメージビルドと並行しない）。

### 35.4.1 サードパーティ CLI の再配布とライセンス（lean variant の根拠）

「イメージ/rootfs に焼いて第三者へ配布」は、同梱物ごとに再配布権が要る。グループ会社への
提供も**別法人への頒布**なので同じ扱いが安全側。調査結果（2026-07 時点・npm registry /
GitHub API の license 表示で確認）:

| 同梱物 | ライセンス | 焼いて再配布 |
|---|---|---|
| claude（@anthropic-ai/claude-code） | `SEE LICENSE IN README.md` = プロプライエタリ（再配布許諾の明示なし） | **不可扱い** |
| agy（Antigravity CLI） | Google のプロプライエタリ配布（install.sh 供給・OSS ライセンスなし） | **不可扱い** |
| codex（@openai/codex） | Apache-2.0 | 可（NOTICE 帰属） |
| opencode（opencode-ai） | MIT | 可（同上） |
| rtk（rtk-ai/rtk） | Apache-2.0 | 可（同上） |
| chromium / git / tmux / node / go / フォント / Debian ベース | 各種 OSS（BSD/GPL/ISC/OFL 等） | 可（GPL 系はソース入手手段の案内・帰属表示の義務 — NOTICE を P1 で整備） |

帰結:

- **claude と agy が焼けない時点で「全焼き込み rootfs の再配布」は成立しない**。よって
  配布物（native tar C と air-gap イメージ tar B）は lean variant（CLI 抜き +
  versions.json ピン + 起動時インストール）にする。**boot-install なら各デプロイ先が
  公式配布元（npm / claude.ai / antigravity.google / GitHub Releases）から直接取得**する
  ことになり、当方による再配布に当たらない（各社が各配布元の規約を自ら受諾する形）。
- codex/opencode/rtk は焼いても適法だが、**boot-install 機構を持つ以上は全 CLI を同じ経路に
  統一**する（分岐を減らし、lean の軽さも最大化。焼く/焼かないの判断をライセンス表の保守に
  依存させない）。
- **完全オフライン（air-gap）で CLI まで要る会社**は、lean イメージ + 自社ビルド
  （リポジトリの Dockerfile で `BAKE_AGENT_CLIS=1` を自社環境で焼く = 自社が配布元から
  直接取得）へ誘導する。runbook に手順として明記（これも「当方が再配布しない」形の実現）。
- 社内・自社デプロイ用のイメージ（配布しない）は全焼き込みのままでよい — ライセンス問題は
  「頒布」で生じ、自社内利用では生じない。

## 35.5 アップグレードの共通契約（runbook へ載せる文言の骨子)

全ターゲット共通:

- 更新前に必ずバックアップ（compose: `backup.sh` / native: `WS_DATA` の tar / ECS: EFS+RDS スナップショット）。
- migration は CP 起動時自動・前方のみ。**ダウングレード非対応**。
- `AF_MASTER_KEY` はデータにもバックアップにも含まれない（別金庫）— 復旧手順の最初に確認。

ターゲット別の「差し替え単位」:

| ターゲット | 差し替えるもの | 手順 |
|---|---|---|
| compose / EC2-Single | イメージタグ | `.env` の `VERSION` 更新 → `docker compose pull`（or `load-images.sh`）→ `up -d` |
| native | パッケージディレクトリ | 新 tar 展開 → `af start`（データは外） |
| ECS | ECR タグ + CFN パラメータ | `release-ecr.sh` → `deploy --parameter-overrides ImageTag=` → WS は次回 Start から |

## 35.6 リリース工程の一本化

### 35.6.1 版刻印（前提の小改修）

- `control-plane/main.go` と `workspace/agent` に `var buildVersion = "dev"` を置き、
  起動ログへ出す。CP は `/healthz`（現在 `ok` 固定）を `ok <version>` にするか
  `/api/version` を足す（Console のオペレーター画面での表示は後続でよい）。
- すべてのビルド経路（release オーケストレータ・両 Dockerfile・run-dev.sh）で
  `-ldflags -X` を配線。Dockerfile は `ARG VERSION=dev` で受ける。

### 35.6.2 オーケストレータ

`deploy/release/build.sh`（新設）を単一入口にする:

```
VERSION=0.2.0 deploy/release/build.sh [--compose] [--native] [--save] [--all]
```

- `--compose` = 現 `deploy/compose/release.sh` の処理（イメージビルド + バンドル A、
  `--save` で B）。実装は既存 release.sh を呼ぶ委譲から始める（compose/release.sh は
  当面そのまま残す — ec2-single runbook が参照しているため）。
- `--native` = §35.3.1 の C（amd64 先行）: af-cp 静的ビルド + bwrap/git 静的ビルド（自前
  ビルダーイメージ）+ `buildx -o type=tar` で rootfs 書き出し + zstd 圧縮 + console/docs 同梱。
- 仕上げに `SHA256SUMS`（D）を dist 直下へ。
- ECR push（release-ecr.sh）は**顧客環境側の操作**なのでオーケストレータに含めない
  （バンドル A の `aws/ecs/` に同梱して現地で叩く）。

## 35.7 実装フェーズ

| フェーズ | 内容 | 出口 |
|---|---|---|
| **P1: 共通基盤** | 版刻印（§35.6.1）・release.sh へ `aws/` 同梱 + SHA256SUMS・`deploy/release/build.sh` 骨格・**配布 variant（`BAKE_AGENT_CLIS=0`）と entrypoint のピン版 boot-install 一般化 + NOTICE/帰属整備（§35.4.1）** | `VERSION=x build.sh --compose` で A+B+D が出る（B は lean variant・起動時ピン install がコンテナで通る） |
| **P2: native tar（self-contained）** | `runtime_native.go` の rootfs モード（bwrap ラップ・`AF_NATIVE_ROOTFS`）・ビルダ（--native: rootfs 書き出し + 静的 bwrap/git）・`workspace-agent install-chromium` + `findChromiumBinary` 解決順変更（専用ピン dir を playwright cache より先に）・`install-go` + toolchains UI への Go 追加・ランチャ `af`・README-native（WSL 導入/userns 注記/更新） | **素の WSL2（追加インストールなし）**で tar 展開 → `af start` → clone → claude セッション E2E（ブラウザペインは初回 attach でピン版 chromium が入る） |
| **P3: ECS 配布** | release-ecr.sh・ImageTag/Persistence パラメータ化・更新 runbook・最小 IAM 表 | sandbox で「push → deploy → WS 起動 → タグ更新 → 次回 Start で新イメージ」一巡 |
| **P4: 検証ゲート** | §35.8 の未済分（native 実機が筆頭） | 各ゲート緑 + 第 2 デプロイ再現 |

P1→P2 が本丸（native が唯一のゼロ→イチ）。P3 は独立に並行可。

## 35.8 検証ゲート（P3-10 完了判定への接続）

| ターゲット | ゲート | 状態 |
|---|---|---|
| compose | clean host でバンドルから起動（P3-10 段5） | ✅ 実証済（ec2-single 上） |
| EC2-Single | 同上 + CFN provision〜teardown | ✅ 実証済 |
| native | **素の WSL2**（Docker なし・**追加インストールなし**）で tar から起動 → 初回 boot-install（ピン版）→ clone → claude セッション E2E → 再起動してオフライン起動（2回目は DL なし）。userns 無特権実行が WSL2 標準カーネルで通ることの確認を含む（§35.3.1 の仮説） | ✗ 未（docs/34 §34.6 の実機未検証と同時に消化する） |
| ECS | E2E ゲート（p3-7 凍結仕様 §20b.7.14）+ タグ更新の一巡 | △ 段階実証済・更新一巡は未 |
| 総合 | 第 2 デプロイをゼロから立てて E2E（decisions/0001） | ✗ 未 |

## 35.9 未決事項（実装前に決める）

1. **native tar への docs/ 同梱範囲**: 全ツリー（内部 dev/decisions/history 込み・単一ユーザー
   なので権限問題なし）か、`guide/` + `dev/` のみに絞るか。tar サイズと「設計文書を配布物に
   含める抵抗感」次第。→ 推奨: まず全ツリー（実装が最短・自分用配布のうちは問題にならない）。
2. **配布チャネル**: 社内共有のみで始めるか、GitHub Releases / GHCR を立てるか（§35.4）。
   → 推奨: P1〜P2 はファイル渡しで完結させ、チャネルは需要が出た時点で。
3. **`/healthz` の版表示 vs `/api/version` 新設**（§35.6.1）: 監視互換（`ok` 文字列を見る
   ものが居ないか）だけ確認して決める。
4. **Helm の棚上げを roadmap に反映するか**（§35.3.4-5）。→ 推奨: 反映する。
5. **native の arm64 rootfs をいつ出すか**: Dockerfile は arch 対応済みだが、rootfs の
   arm64 ビルドは QEMU クロスでホスト負荷が重い。→ 推奨: amd64 のみで出し、WSL on ARM の
   実需要が出た時点で arm64 対応ホスト（実機 or CI）でビルドする。
6. **静的 bwrap / 静的 git（NO_CURL）の自前ビルドを供給網として許容するか**: どちらも
   小物で musl 静的ビルドの実績が豊富、リリース工程の builder イメージ内で source から
   固定版をビルドする（バイナリ拾い食いはしない）。→ 推奨: 許容。ここを嫌うと
   「ホスト依存ゼロ」は成立しない。
7. **chromium オンデマンド化の実装確認 2 点**（方針は §35.3.1 で決定済み: native rootfs は
   on-demand・air-gap B は焼き込み継続）: (a) DL 供給元の最終選定（playwright CDN を第一候補
   に、版不変 URL・arm64 供給・レイアウト互換で比較確認）。(b) bwrap 配下での sandbox 実測
   （namespace sandbox が通るか。不可なら `--no-sandbox` + localhost 限定の割り切りを README 化）。
8. **ライセンス見解の確度**: §35.4.1 は npm registry / GitHub API の license 表示による
   一次調査。配布開始前に claude / agy の利用規約本文（再配布・社内限定配布の条項）を
   読んで確定させる（特にグループ会社配布を「社内」と主張しない前提で設計してあるが、
   規約側が許すなら焼き込み配布へ戻す選択肢が復活する）。
