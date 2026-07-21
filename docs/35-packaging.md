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
| C | `agent-fleet-native-<v>-linux-amd64.tar.gz`（**数十 MB**） | `af` ランチャ・`bin/af-cp`・`bin/bwrap`・`bin/git`（いずれも静的）・`rootfs.json`（R の URL + sha256 + rootfs 版のピン manifest — **rootfs 本体は同梱せず初回起動時に DL**）・`console/`（Vite dist）・`docs/`（ステージング源）・README。`--bundle-rootfs` で R 同梱の self-contained 版（air-gap/ファイル渡し用）も生成可 | native（WSL / 任意の Linux 単一ユーザー）**ホスト追加インストール ゼロ**（§35.3.1） |
| R | `agent-fleet-rootfs-<r>-linux-amd64.tar.zst`（zstd 200MB 台目安） | **lean rootfs** = workspace イメージの配布 variant（OSS ユーザーランドのみ。エージェント CLI は起動時ピン版インストール、chromium＋CJK フォント・Go・AWS CLI+SSM plugin・ops MCP 群など利用者限定ツールはオンデマンドピン版インストール — §35.3.1・§35.4.1）。**公開の置き場**（§35.9-2）に置き、`af` が rootfs.json のピンで取得・検証。**版 `<r>` は app 版 `<v>` から分離** — イメージ不変のリリースでは再 DL なし | native（C から参照される） |
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
agent-fleet-native-<v>-linux-amd64/          # 全体で数十 MB（rootfs は含まない）
├── af                    # ランチャ（下記）
├── bin/af-cp             # CGO_ENABLED=0 静的ビルド
├── bin/bwrap             # bubblewrap 静的ビルド（リリース工程で自前ビルド）
├── bin/git               # git 静的ビルド（NO_CURL・CP の内部 git provider 用 → 下記）
├── rootfs.json           # rootfs（R）のピン manifest: url / sha256 / rootfs 版 <r>
├── console/              # Vite ビルド済み dist（CONSOLE_DIR で指す）
├── docs/                 # CP イメージが焼くのと同じ docs ツリー（stageWorkspaceDocs の源）
└── README.md             # WSL 前提の導入・制約・更新手順
```

**rootfs 本体（R）は配布 tar に同梱せず、初回 `af start` が DL する**。lean 化で rootfs が
OSS のみになった（§35.4.1）ため公開の置き場に置ける — これが lean 化のもう一つの配当で、
「配る箱」は自分たちの成果物（af-cp/console/docs/静的小物）だけの数十 MB に縮む。
`af` は rootfs.json の sha256 で検証してから展開する（改竄・欠損は起動前に検出）。
rootfs の版 `<r>` は app 版 `<v>` と独立させ、イメージに変更が無いリリースでは
**既存の展開済み rootfs をそのまま使い再 DL しない**（app だけの更新が数十 MB で済む）。
air-gap・ファイル渡し運用には `--bundle-rootfs` の self-contained tar（従来形）と、
`af start --rootfs <path>`（手動配置した R を使う）の両方を残す。

なお「OCI レジストリから layer を pull する」案（基底イメージの重複排除が効く）は
否決 — 素の HTTPS GET + sha256 で足りるところに whiteout 処理つきの pull 実装を
持ち込む価値がない（否決表参照）。

- rootfs は workspace イメージの**配布 variant（lean rootfs）**: workspace-agent・tmux・
  git・gh ラッパー・node・chromium の**実行時ライブラリ群**・fontconfig+DejaVu など
  **OSS のユーザーランドだけ**を焼く。エージェント CLI（claude/codex/opencode/agy/copilot/rtk）は
  **焼かず**、entrypoint が初回起動時に `versions.json` の**ピン版（= e2e-smoke で動作
  検証した版）**を仮想 HOME の `~/.local/bin` へインストールし、self-update opt-in
  有効時はそのまま最新へ追従する（§35.4.1 — サイズとライセンスの両方の理由）。
  **chromium 本体＋CJK フォント・Go toolchain・AWS CLI+SSM plugin・ops MCP サーバ群も
  焼かず**、使う人だけがオンデマンドでピン版を導入する（下記）。node は残す
  （CLI の npm boot-install が使うランタイム依存）。
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
self-update opt-in の `~/.local/bin` shadow 機構（claude/opencode/codex/rtk/agy/copilot）がある。
これを一般化する:

- ビルド時ピン（Dockerfile ARG → `/usr/local/share/agent-fleet/versions.json`）は配布
  variant でも**書き出しだけ行う**（バイナリは焼かない）。ピンの意味は「**動作を保証する版**」
  （e2e-smoke で検証してから bump する現行運用 — docs/dev/10 §10.2.1 — の対象が
  焼き込み版からピン manifest に変わるだけ）。
- entrypoint は各 CLI について「`/usr/local/bin` にも `~/.local/bin` にも無ければ、
  versions.json のピン版を `~/.local/bin` へインストール」する（claude/codex/opencode は
  rootfs 内の node で `npm install -g`（prefix=`~/.local`）、rtk は checksums 検証付き DL、
  agy は GitHub Releases の versioned アセット + `agy_sha256` 検証 — P1 実装時にピン化。
  下の 2026-07-21 追記参照）。
- 仮想 HOME は永続なので **DL は初回起動の一度きり**。2回目以降はオフラインでも起動する。
- self-update opt-in（`AF_AGENT_SELF_UPDATE`）ON なら従来どおり最新へ追従、OFF なら
  ピン版のまま。書き込み先はすべて bind した仮想 HOME 側なので **ro rootfs のまま成立**する。

なお社内・自社運用の Docker イメージは従来どおり**全焼き込み**を既定のまま維持する
（オフライン即起動・イメージ=検証単位という現行の利点を捨てない）。抜く理由は 2 種類
あり、適用範囲が違うので **Dockerfile の ARG 2 ノブ**に正式化する:

| ノブ | 抜く理由 | 対象 | 0 にする配布物 |
|---|---|---|---|
| `BAKE_AGENT_CLIS=0` | **ライセンス**（§35.4.1） | claude / codex / opencode / agy / rtk | **B（air-gap イメージ）と C（native rootfs）の両方** |
| `BAKE_OPTIONAL_TOOLS=0` | **サイズ**（利用者限定機能の後払い化） | chromium+CJK フォント / Go toolchain / AWS CLI+SSM plugin / ops MCP サーバ群 | **C のみ**（B はサイズ制約が緩く、chromium は SUID sandbox の都合もあり焼き続行） |

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

**ops 系ツールも同様（native rootfs のみ・利用者はさらに限られる）**:

| ツール | 現状 | オンデマンド化 |
|---|---|---|
| AWS CLI v2 + session-manager-plugin（導入後 ~250MB・**削減幅最大**） | kind=`ssm` セッション用に焼き込み。**両方とも「latest」URL で未ピン** | 初回の ssm セッション開始時に agent が versioned URL（awscli は `-2.x.y.zip`、SMP は `plugin/<ver>/`）から home へ導入（aws installer は `--install-dir` 指定可・SMP は deb を `dpkg-deb -x` 相当で展開すれば root 不要）。導入の進捗はその ssm 端末にそのまま流れる。**オンデマンド化で初めてピンが効く**ようになる（現状より改善） |
| awslabs CloudWatch MCP（uv tool venv・~100MB 級） | `mcp-run cloudwatch` の即起動用に焼き込み | 焼き込みが無ければ `uvx awslabs.cloudwatch-mcp-server==<pin>` で実行 — **後入れ機構は uvx に既にあり**（初回 PyPI 取得・`~/.cache` 永続）、焼き込みは初回高速化の最適化に過ぎない。PyPI は全版恒久保存＝ピン供給元として理想的 |
| mcp-grafana（Go 単体 ~40MB） | `mcp-run grafana` 用に焼き込み | 焼き込みが無ければ初回 `mcp-run grafana` 時に GitHub Releases の版固定 asset を `~/.local/share/agent-fleet/bin/` へ DL |

ピンは versions.json へ集約する（mcp-grafana / cloudwatch は現在 ARG のみで manifest に
未記載 → 追記。awscli / SMP は前述のとおり現在未ピン → 新規にピン化）。

**評価の結果「残す」と判断したもの**（後入れ不能・全員が踏む・実行系、のいずれか）:

- **gh**: 透過認証ラッパー（全 git 利用者の導線）の実体。MIT・~50MB と小さい。
- **node**: CLI の npm boot-install の実行系そのもの。
- **python3 + uv/uvx**: uvx が ops MCP のランチャ実行系。python は baseline ツール兼
  pip --user の受け皿。
- **build-essential + pkg-config + python3-dev**: cgo / node-gyp / source wheel が暗黙に
  踏む。削ると壊れ方が見えにくい（前述）。
- **subversion**: clone 機能の svn 対応が使う。bwrap 配下は root になれず apt の後入れが
  **構造的に不可能**なので、apt でしか供給できないものは焼くしかない（chromium を
  playwright CDN 供給に切り替えるのはこの制約の回避でもある）。
- **tmux / git / ripgrep / fd / bat / jq / vim / htop 等の基本ユーティリティ**: セッション
  実行と日常操作の土台。合計しても小さい。

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
| `af start` | 初回のみ rootfs.json のピンに従い R を DL（sha256 検証・進捗表示。`--rootfs <path>` / 同梱 full tar なら DL なし）→ `WS_DATA/shared/rootfs/<r>/` へ展開（同 `<r>` が展開済みなら再利用） → preflight（userns 可否チェック。tmux/git/claude の存在 warn は**廃止** — 全部 rootfs 内）→ `AF_RUNTIME=native` / rootfs モード env / `CONSOLE_DIR=<pkg>/console` / `AF_DOCS_DIR=<pkg>/docs` / `GIT_HTTP_BACKEND=<pkg>` 配線 / `WS_DATA=~/.local/share/agent-fleet`（既定）で `bin/af-cp` を exec |
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

**サイズ**: 配布 tar（C）は**数十 MB**（af-cp ~20MB + console dist + docs + 静的小物）。
rootfs（R）は CLI 群（claude/codex/opencode/agy/copilot + npm 残渣）・chromium 本体 + CJK
フォント・Go toolchain・AWS CLI+SSM plugin・ops MCP 群を落とした lean で、
全焼き込み比 **圧縮 700MB 級の減量**、残は Debian ベース + build-essential/python +
tmux/git/svn + node + chromium 実行時ライブラリ + 基本ユーティリティで
**zstd 200MB 台目安**（要実測）。R は初回起動時の一度きりの DL で、以降のアプリ更新は
C（数十 MB）だけで済む（`<r>` 不変なら再 DL なし）。リリース工程では
`docker buildx build -o type=tar`（コンテナ起動不要）で書き出し、zstd 圧縮する。

**否決した代替案**（検討の記録）:

| 案 | 否決理由 |
|---|---|
| rootfs を持たず、静的 tmux/git の同梱だけで済ませる | 自前ビルドの供給網が tmux/git（https 付き）へ広がる・chromium/フォント/node は諦めになる・entrypoint 初期化のパリティが得られない。**CLI の boot-install だけは本設計に採用**したが、実行基盤（ユーザーランド）は rootfs で丸ごと持つのが正しい分割 |
| proot（ptrace ベース・無特権） | syscall フックで桁級に遅い。git/ビルド作業が主用途の本製品では成立しない |
| `wsl --import` で workspace を別ディストロ化 | WSL 専用（汎用 Linux に効かない）・`wsl.exe` interop への依存・別ディストロへの exec を担う新 Runtime アダプタが必要。bwrap 案はコード差分が Start 1 点で済む |
| 全部入り単一バイナリ（console/rootfs を go:embed） | GB 級 embed は非現実的。tar 配布が前提になった時点で無意味 |
| rootfs を OCI レジストリの layer pull で取得（基底イメージの重複排除狙い） | manifest 解決・layer 順序・whiteout 処理を持つ pull 実装が要る。単一 tar.zst の HTTPS GET + sha256 検証で同じ結果が得られ、節約できるのは基底層の数十 MB のみ。複雑さに見合わない |
| rootfs を配布先で組み立て（deb を DL して debootstrap 相当） | root なしでは postinst/trigger が走らせられず、chromium 依存群のような複雑パッケージで壊れる。ビルド済み rootfs の DL が確実 |

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
     ※create-repository は凍結時に**存在確認のみ**へ変更（§35.7.3-1 — repo の正は
     20-platform CFN で、out-of-band create は後続 deploy を壊す）。
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
| agy（Antigravity CLI） | Google のプロプライエタリ配布（OSS ライセンスなし。GitHub `google-antigravity/antigravity-cli` にも license 表示なし — 2026-07-21 確認） | **不可扱い** |
| copilot（@github/copilot） | `SEE LICENSE IN LICENSE.md` = プロプライエタリ（再配布許諾の明示なし。2026-07-21 確認） | **不可扱い** |
| codex（@openai/codex） | Apache-2.0 | 可（NOTICE 帰属） |
| opencode（opencode-ai） | MIT | 可（同上） |
| rtk（rtk-ai/rtk） | Apache-2.0 | 可（同上） |
| chromium / git / tmux / node / go / フォント / Debian ベース | 各種 OSS（BSD/GPL/ISC/OFL 等） | 可（GPL 系はソース入手手段の案内・帰属表示の義務 — NOTICE を P1 で整備） |

帰結:

- **claude・agy・copilot が焼けない時点で「全焼き込み rootfs の再配布」は成立しない**。よって
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

### 35.4.2 公開 dist repo（決定・2026-07-21）

**`k-k1/agent-fleet-dist`（public）を新設**し、成果物の置き場とする（ユーザー決定）。

- **中身は「README + install.sh + Releases」だけ。source は置かない。** したがって
  dist repo 上で CI ビルドは回らない（無料 Actions の恩恵はソースが public の場合のみ、
  という制約の帰結）。リリースビルドは private 側（ローカル、または private Actions の
  workflow_dispatch）で行い、`gh release create -R k-k1/agent-fleet-dist` で publish する。
- **Releases の構成**: app リリース `v<v>`（C・B・SHA256SUMS を添付）と、rootfs リリース
  `rootfs-<r>`（R を添付）を**別 tag** で切る。C 内の rootfs.json は
  `releases/download/rootfs-<r>/agent-fleet-rootfs-<r>-linux-amd64.tar.zst` の恒久 URL を
  指す。`<r>` 不変のリリースでは既存 rootfs tag をそのまま参照（再アップロードも再 DL もなし）。
- **install.sh（導入ワンライナー）**: `curl -fsSL <raw URL>/install.sh | bash` が最新の
  C を取得・sha256 検証し `~/.local/opt/agent-fleet/<v>/` へ展開、`~/.local/bin/af` に
  symlink。更新も同じコマンド（版ディレクトリ切替）。air-gap はファイル渡し
  （`--bundle-rootfs`）のままで、install.sh は使わない。
- **arm64 の含意**（未決⑤の更新）: dist repo に source が無い以上、公開 repo の無料 arm64
  ランナーでの rootfs ビルドはできない。arm64 を出す時は private repo の arm ランナー
  （$0.005/分）か arm 実機でビルドして publish する。

### 35.4.3 docs の配布範囲 — internal denylist（2026-07-21 精査）

決定①「docs/ 全ツリー同梱」を精査の結果、**「全ツリー − internal denylist」に精密化**する。
ツリー全体を機械スキャン（実ドメイン・メール・IP・アカウント ID・資格情報パターン）した
結果、**設計文書（番号付き・dev/・guide/・decisions/）はクリーン**で、固有情報は以下の
「運用ログ系」に集中していた:

| 除外対象（denylist） | 理由（発見された内容） |
|---|---|
| `docs/HANDOFF.md` | **自ホストの稼働情報そのもの**: 実入口 URL（Tailscale ホスト名）・運用者コンテナ名（メールアドレス由来）・ポート/パス等。配布先には無意味かつ攻撃面の開示 |
| `docs/CHANGELOG-handoff.md` | 時系列作業ログ。個人メールアドレス・実名ハンドルを含む |
| `docs/talk/` | 社内発表資料（Marp・会社名テーマ）。製品ドキュメントではない |
| `docs/history/` | 役目を終えた実装プラン。実ドメイン・メール由来コンテナ名・移行経緯が散在 |

- **適用先は「配布・公開のすべて」で同一**: native tar C の docs/、配布 variant の
  CP イメージ（`stageWorkspaceDocs` の焼き込み源 — 配布先の super_admin にも denylist は
  見せない）、将来の公開 source snapshot。**自社 dev/自社運用ビルドは従来どおり全ツリー**
  （HANDOFF はまさに自ホスト運用のためにある）。
- **実装**: denylist は `docs/.distignore`（1 行 1 パターン）として宣言的に置き、
  release 工程（build.sh が docs をステージングして COPY）で適用する。ハードコードしない。
- **残す判断をしたもの**: `decisions/`（意思決定記録。個人情報なし・設計理解に価値）、
  `roadmap.md`（実ドメイン言及 1 箇所は一般化済み）、`dev/`・`guide/`・番号付き設計docs
  （スキャンでクリーン）。
- **ツリー本体への修正（適用済み）**: 配布物 A に入る `deploy/aws/` から実環境値を除去 —
  CFN `30-ingress.yaml` の `Fqdn`/`HostedZoneId` の Default（実ドメイン・実ゾーン ID）を
  削除して必須パラメータ化、両 README の実ドメイン・実 EIP 例をプレースホルダ
  （example.com / TEST-NET）へ。roadmap の実ドメイン言及も一般化。
- **git 履歴は書き換えない**: 配布は tar（履歴なし）・公開 snapshot は squash 方式
  （orphan コミット）なので、**現在のツリーが綺麗であれば足りる**という前段の判断と整合。

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
  起動ログへ出す。CP には **`/api/version`（認証付き）を新設**する（決定）:
  `/healthz` は `restart-cp.sh` が応答 `ok` を**完全一致比較**しているため変えられず、
  ALB ヘルスチェックも 200 だけ見るので触る理由がない。版情報を無認証で外部に晒さない
  意味でも新設が安全側。Console のオペレーター画面での表示は後続でよい。
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
- `--native` = §35.3.1 の C + R（amd64 先行）: af-cp 静的ビルド + bwrap/git 静的ビルド
  （自前ビルダーイメージ）+ `buildx -o type=tar` で lean rootfs（R）書き出し + zstd 圧縮
  + rootfs.json（R の url/sha256/`<r>`）を焼いた C を組み立て。`--bundle-rootfs` で
  R 同梱の self-contained 版も生成。R の `<r>` は内容ハッシュ由来にして、イメージ不変の
  リリースで同一 `<r>` を再利用（利用者の再 DL なし）。
- 仕上げに `SHA256SUMS`（D）を dist 直下へ。
- ECR push（release-ecr.sh）は**顧客環境側の操作**なのでオーケストレータに含めない
  （バンドル A の `aws/ecs/` に同梱して現地で叩く）。

## 35.7 実装フェーズ

| フェーズ | 内容 | 出口 |
|---|---|---|
| **P1: 共通基盤** | 版刻印（§35.6.1）・release.sh へ `aws/` 同梱 + SHA256SUMS・`deploy/release/build.sh` 骨格・**配布 variant（`BAKE_AGENT_CLIS=0`）と entrypoint のピン版 boot-install 一般化 + NOTICE/帰属整備（§35.4.1）** | `VERSION=x build.sh --compose` で A+B+D が出る（B は lean variant・起動時ピン install がコンテナで通る） |
| **P2: native tar（self-contained）** | `runtime_native.go` の rootfs モード（bwrap ラップ・`AF_NATIVE_ROOTFS`）・ビルダ（--native: lean rootfs 書き出し + 静的 bwrap/git + rootfs.json 生成）・ランチャの R 初回 DL（sha256 検証・`--rootfs` オフライン経路）・`workspace-agent install-chromium` + `findChromiumBinary` 解決順変更（専用ピン dir を playwright cache より先に）・`install-go` + toolchains UI への Go 追加・ops 系オンデマンド（ssm 初回の awscli+SMP 導入 / mcp-run の uvx ピン実行・grafana DL）・ランチャ `af`・README-native（WSL 導入/userns 注記/更新） | **素の WSL2（追加インストールなし）**で tar 展開 → `af start` → clone → claude セッション E2E（ブラウザペインは初回 attach でピン版 chromium が入る） |
| **P3: ECS 配布** | release-ecr.sh・ImageTag/Persistence パラメータ化・更新 runbook・最小 IAM 表 | sandbox で「push → deploy → WS 起動 → タグ更新 → 次回 Start で新イメージ」一巡 |
| **P4: 検証ゲート** | §35.8 の未済分（native 実機が筆頭） | 各ゲート緑 + 第 2 デプロイ再現 |

P1→P2 が本丸（native が唯一のゼロ→イチ）。P3 は独立に並行可。

### 35.7.1 P1 実装仕様（凍結）

| # | 対象 | 変更内容 |
|---|---|---|
| 1 | `control-plane/main.go`・`workspace/agent` | `var buildVersion = "dev"`（`-ldflags -X main.buildVersion=<v>` 注入）+ 起動ログ出力。CP に `GET /api/version`（member 認証配下・`{"version":"..."}`）新設 |
| 2 | 両 Dockerfile | `ARG VERSION=dev` → go build の ldflags へ配線（リリース経路のみ実版。run-dev.sh は "dev" のまま） |
| 3 | `deploy/compose/release.sh` | bundle へ `deploy/aws/` を同梱・dist 直下に `SHA256SUMS` 生成・docker build へ `--build-arg VERSION` |
| 4 | `deploy/release/build.sh`（新設） | 単一入口の骨格。`--compose` は compose/release.sh へ委譲、`--native` は P2 まで未実装エラー。`VERSION` 必須・出力 `deploy/release/dist/` |
| 5 | `workspace/Dockerfile` | `ARG BAKE_AGENT_CLIS=1`（npm 3種・agy・rtk の RUN を条件化）・`ARG BAKE_OPTIONAL_TOOLS=1`（chromium 3pkg + CJK フォント・Go・awscli+SMP・mcp-grafana・cloudwatch を条件化。**chromium 実行時ライブラリと fontconfig/DejaVu は 0 でも apt で明示導入**）。versions.json は常に書き出し、`chromium_dl` / `mcp_grafana` / `cloudwatch_mcp` / `awscli` / `session_manager_plugin` のピンを追記。SUID sweep の chrome-sandbox 前提 test を chromium 不在ビルドでも通るよう条件化 |
| 6 | `workspace/entrypoint.sh` | ピン版 boot-install の一般化: 各 CLI（claude/opencode/codex/agy/copilot/rtk）で「`/usr/local/bin` にも `~/.local/bin` にも無ければ versions.json のピン版を `~/.local` へ導入」。既存 `CLAUDE_INSTALL` fallback と self-update 実装の取得経路を版指定で再利用（npm は `prefix=$HOME/.local`） |
| 7 | `deploy/local/e2e-smoke.sh` | lean イメージ対応: `BAKE_AGENT_CLIS=0` のイメージでは「versions.json が存在し全ピン記載」を検証（boot-install の実走はネット前提なので P1 ゲートの別項で確認） |
| 8 | `NOTICE` | 同梱 OSS の帰属追記（git/tmux/chromium ほか。静的 bwrap/git 分は P2 で追加） |
| 8b | `docs/.distignore`（新設） | internal denylist（§35.4.3: HANDOFF / CHANGELOG-handoff / talk/ / history/）。release 工程の docs ステージングと配布 variant の CP イメージビルドで適用 |
| 9 | `docs/roadmap.md` | Helm chart を「需要が出るまで棚上げ（AWS の答えは ECS+CFN）」へ更新 |

**P1 ゲート**: (a) `VERSION=x deploy/release/build.sh --compose` で A+B+D が生成される。
(b) lean B（`BAKE_AGENT_CLIS=0`）をローカル起動し、boot-install で claude ピン版が
`~/.local/bin` に入りセッションが動く。(c) 既定 ARG（全焼き込み）のビルド・e2e-smoke が
無変更で通る（既存デプロイに影響ゼロ）。

**実装状況（2026-07-21・feat/packaging）**: 表の 9 項目すべて実装済み。実装時の確定事項:

- `release.sh` の配布既定は lean（`BAKE_AGENT_CLIS=0`。自社用全焼き込みは
  `BAKE_AGENT_CLIS=1` を明示）。B（images tar）はバンドル内でなく **dist 直下の独立
  成果物**に変更（§35.2 のマトリクスどおり。ec2-single runbook のパスも更新済み）。
  NOTICE はバンドル A に同梱。
- awscli / session-manager-plugin は焼き込みも versioned URL へ前倒しでピン化
  （§35.9-7(c) の残りは「root なし展開のオンデマンド導入」= P2）。
- `chromium_dl` ピンは playwright build **1228**（= Chromium 149.0.7827.55、
  playwright 1.61.0 安定版同梱）を記録。供給元の最終選定・実 DL 検証は §35.9-7(a)
  のまま P2（この Workspace からは PRSS CDN が 400 を返し検証不能 — 実機/別回線で確認）。
- entrypoint の self-update OFF 時 shadow 掃除は「焼き込み版が存在する時だけ」に限定
  （lean では ~/.local が boot-install 品そのものなので消してはならない）。
- e2e-smoke の既存バグ修正: `EXPECT_COPILOT` が docker run へ渡っておらず、copilot
  統合後の smoke は set -u で落ちる状態だった。
- **agy も真のピンへ昇格**（P1 追補・2026-07-21）: GitHub Releases
  （`google-antigravity/antigravity-cli`）が全旧版の versioned アセットを恒久保存して
  おり、GCS の install.sh 配布物と**バイト同一**（1.1.5 で sha256・manifest sha512 の
  一致を実測）。焼き込み・boot-install とも install.sh（常に latest・ピン不可）を廃し、
  `AGY_VERSION` + アセット digest（`AGY_SHA256_X64/ARM64` → versions.json `agy_sha256`）
  の検証付き直接取得に変更。「agy だけビルド時 latest の記録」という例外は解消
  （self-update opt-in の latest 追従は manifest 経路のまま）。versioned manifest API・
  バケット listing は無し（実測 404/401）なので、供給元は GitHub Releases が唯一。
- 検証済み: CP/agent 全 Go テスト緑（151+451）・shell 構文・docs ステージングの実走
  （114→90 file、denylist 4 種のみ除外）・entrypoint boot-install のサンドボックス実走
  （lean 初回=ピン npm 導入+update 抑止 / 2 回目=no-op / baked=no-op / 失敗時ソフト続行）。

**P1 ゲート結果（2026-07-21・hosted CI 実走 = `.github/workflows/release-gate.yml`、
run 29802263108 全緑）**: 開発 Workspace に docker が無く dev ホストの重ビルドは
フリート OOM リスクがあるため、実ビルド検証は hosted runner で実施（トリガーは
workflow ファイル変更 push と workflow_dispatch のみ = 課金抑制）。

- (a) ✅ `VERSION=0.0.0-ci build.sh --compose` で A+B+D 生成。SHA256SUMS 照合・
  aws//NOTICE 同梱・.env.example 版ピン・鍵類不混入・CP 配布イメージの denylist 適用
  （HANDOFF/talk/history 不在・設計 docs 残存）・`/api/version` が実起動で版を返す・
  workspace-agent の版刻印ログ、全て検証。
- (b) ✅（CI で可能な範囲）lean B は e2e-smoke lean モード全緑（6 CLI 完全不在・
  versions.json 全ピン記載・chromium/SUID/browser smoke 維持）。boot-install ライブ実走で
  claude 2.1.215 / opencode 1.18.3 / codex 0.144.6 / copilot 1.0.73 / rtk 0.43.0 が
  ピン版で `~/.local` に入り、agy 1.1.5 は `--version` 実行まで通過（runner は RDRAND 有）。
  残るのは「実 CP+実認証でセッションが動く」の目視のみ（ホスト側フリート再ビルド時）。
- (c) ✅ 既定 ARG（全焼き込み）の workspace ビルド + e2e-smoke、既定 DOCS_SRC
  （全ツリー docs・HANDOFF 残存）の CP ビルドが無変更で通過（既存デプロイに影響ゼロ）。
- 副産物のバグ修正: 初回ゲート実走（run 29801834913）が **CP の素起動即死**を検出 —
  openSQLite が dbPath 親ディレクトリを作らず、WS_DATA 未作成だと起動不能（実デプロイは
  マウント済み dir で不発・P2 native の初回 `af start` で必ず踏む経路）。CP 側に
  MkdirAll を追加して修正済み。

### 35.7.2 P2 実装仕様（凍結）

P2 = native tar（§35.3.1）の実装一式。§35.7.1 と同じく「表の項目を全部実装したら
ゲートで判定」の形で凍結する。設計の背骨は §35.3.1 のまま、ここでは実装が迷わない
粒度まで確定する。

| # | 対象 | 変更内容 |
|---|---|---|
| 1 | `control-plane/runtime_native.go` | **rootfs モード**追加。`AF_NATIVE_ROOTFS=<展開済み rootfs dir>` が設定されたら Start のコマンドを bwrap ラップ（下記 argv 表）へ切替。bwrap は `AF_NATIVE_BWRAP`（無ければ PATH の `bwrap`）。factory ゲート: AUTH=dev（共通）＋ rootfs 配下に `usr/local/bin/workspace-agent`・`usr/local/bin/entrypoint.sh`・`usr/local/share/agent-fleet/image-env.json` が存在すること（欠けは起動前 fail-fast）。`AF_NATIVE_AGENT_BIN` の従来モード（開発・run-dev.sh native）は無変更で共存 |
| 2 | 同上（env 合成） | docker イメージの ENV（PATH/LANG/LC_ALL/DISABLE_AUTOUPDATER/AGY_CLI_DISABLE_AUTO_UPDATE/COPILOT_AUTO_UPDATE 等）は**イメージ config にあり rootfs tar に乗らない**ため、ビルダーが `docker image inspect .Config.Env` を `usr/local/share/agent-fleet/image-env.json`（JSON 配列）として R 内へ注入し、rootfs モードはこれを env の**土台**に敷く。その上へ既存の明示 env（`HOME=/home/dev`・`AGENT_ADDR`・`CLAUDE_CONFIG_DIR=/var/lib/af/claude`・`AF_TMUX_SOCKET`・`AGENT_TOKEN` 等 — 値はコンテナ内パス）→ extraEnv の順で重ねる（重複キーは後勝ち、従来同様 map 平坦化）。`AGENT_DOCS_DIR` は**設定しない**（docs はコンテナ既定パスへ ro-bind するため） |
| 3 | 同上（lifecycle） | pidfile は bwrap の pid を記録し、`pidAlive` の argv0 照合対象は bwrap バイナリ。Stop は従来の group SIGTERM→SIGKILL のみで完結（`--unshare-pid` により bwrap 死亡＝pid namespace 全滅で tmux も消える。rootfs モードでは tmux kill-server fallback を**スキップ** — ソケットは namespace 内 /tmp で外から不可視）。State/waitAgentHealthy は共通 |
| 4 | `workspace/agent`（chromium） | `workspace-agent install-chromium`: versions.json `chromium_dl`（playwright build 番号）の zip（amd64=`chromium-linux.zip` / arm64=`chromium-linux-arm64.zip`）を候補ホスト順（`cdn.playwright.dev/dbazure/download/playwright/builds/chromium/<pin>/` → `playwright.azureedge.net/builds/chromium/<pin>/` → `playwright.download.prss.microsoft.com/dbazure/...`）に取得し `~/.local/share/agent-fleet/chromium/<pin>/` へ原子展開（install-jdk と同型 staging→rename）。CJK フォントは versions.json **`noto_cjk` ピン新設**（notofonts/noto-cjk GitHub Releases の versioned asset）で `~/.local/share/fonts/` へ＋`fc-cache -f`（無ければ skip）。`findChromiumBinary` 解決順を「env → 焼き込み（/usr/bin/chromium 等 PATH 名群）→ **専用ピン dir**（chromium_dl。無ピンは glob 最新）→ playwright cache」へ変更。ペイン初回 attach で不在＆ピンあり→ページ状態 `installing`（既存 status チャネルで Console へ「準備中（初回のみ ~200MB 取得）」）を流して自動導入。`AF_CHROMIUM_NO_SANDBOX=1` で `--no-sandbox` 起動する knob を追加（既定 off・bwrap 配下 sandbox 実測不可時の README 記載用） |
| 5 | `workspace/agent`（go） | `workspace-agent install-go [<ver>]`: 既定 versions.json `go`。go.dev/dl の versioned tarball を `?mode=json&include=all` の公表 sha256 で検証し `~/.local/share/agent-fleet/go/<ver>/` へ原子展開。toolchains.json に `go` フィールド追加（""/"system"=焼き込み or なし、版指定=on-demand）。entrypoint: 選択あり＆未導入なら install-go、`GOROOT`/PATH を export。`resolvedToolchains`/`toolchainShellPrefix`/`applyToolchainEnv` に go を追加（セッション単位注入 — JAVA_HOME 相同）。`env_tool_versions.go` の go 行は on-demand dir も Baked 候補に見る。Console の toolchains UI（設定モーダル）に Go select 追加（ja/en i18n） |
| 6 | `workspace/agent`（ops） | `workspace-agent install-awscli`: versions.json `awscli`/`session_manager_plugin` ピンで awscli zip（`--install-dir ~/.local/share/agent-fleet/aws --bin-dir ~/.local/bin`）＋ SMP deb を `dpkg-deb -x` で root なし展開→ `~/.local/bin/session-manager-plugin`。`buildSSMProgram` の冒頭に「`command -v aws` 不在なら install-awscli（進捗はその ssm ペインへ流れる）」を前置。`runCloudWatchMCP` の uvx fallback を `awslabs.cloudwatch-mcp-server==<cloudwatch_mcp ピン>` に固定（現状 latest の穴を塞ぐ）。`runGrafanaMCP`: 不在時 versions.json `mcp_grafana` の GH release asset を `~/.local/share/agent-fleet/bin/mcp-grafana` へ DL して exec |
| 7 | `deploy/release/build.sh --native` ＋ `deploy/release/native/` | ビルダー実装。(i) `af-cp` 静的ビルド（golang:1.26 コンテナ・CGO off・ldflags VERSION）(ii) console dist（node:22 コンテナ・heap 4096）(iii) docs ステージング（release.sh と同じ `.distignore` 適用）(iv) **静的 bwrap／git+git-http-backend（NO_CURL）／zstd** を alpine(musl) builder イメージで固定版ソースビルド（`Dockerfile.tools`・版は ARG ピン。zstd を同梱するのは**ホスト側 zstd を前提にできない**ため — R の展開は `bin/zstd` で行う）(v) lean rootfs: workspace を `VERSION`＋`BAKE_AGENT_CLIS=0`＋`BAKE_OPTIONAL_TOOLS=0` でビルド→ `docker create`+`docker export`（flatten）→ image-env.json を tar append → `<r>`=sha256(生 tar) 先頭 12hex → `zstd -T0 -15` で R (vi) rootfs.json 生成（`{version,url,sha256,size}`。url 既定 `https://github.com/k-k1/agent-fleet-dist/releases/download/rootfs-<r>/agent-fleet-rootfs-<r>-linux-amd64.tar.zst`、`ROOTFS_URL_BASE` で上書き）(vii) C 組立（`af`・`bin/{af-cp,bwrap,git,git-http-backend,zstd}`・`rootfs.json`・`console/`・`docs/`・`README.md`・`LICENSE`/`NOTICE`・`VERSION` ファイル）。`--bundle-rootfs` で C 内 `rootfs/` に R を同梱した self-contained tar も生成。`--rootfs-json <path>` で既存 manifest を再利用し R 生成をスキップ（`<r>` 不変リリース用） |
| 8 | `deploy/native/af`（ランチャ・C 直下へ同梱） | bash 実装（run-dev.sh native の移植＋rootfs 管理）。`af start`: rootfs ensure（`--rootfs <tar>` 明示 > `<pkg>/rootfs/` 同梱 > rootfs.json URL を curl/wget で DL・進捗表示）→ `sha256sum -c` → `bin/zstd -d \| tar -x` を staging へ→ `WS_DATA/shared/rootfs/<r>/` へ原子 rename＋`.ok` マーカー（同 `<r>` 展開済みなら再利用・オフライン起動）→ preflight（`bin/bwrap --unshare-user --uid 1000 --gid 1000 --ro-bind / / true`。失敗時は userns sysctl 案内を出して stop。旧 preflight の tmux/git/claude warn は廃止）→ env 配線（`AF_RUNTIME=native` `AF_NATIVE_ROOTFS` `AF_NATIVE_BWRAP=<pkg>/bin/bwrap` `CONSOLE_DIR=<pkg>/console` `AF_DOCS_DIR=<pkg>/docs` `GIT_HTTP_BACKEND=<pkg>/bin/git-http-backend` `GIT_EXEC_PATH=<pkg>/bin` `PATH=<pkg>/bin:$PATH` `WS_DATA`（既定 `~/.local/share/agent-fleet`）`CP_ADDR`（**既定 `127.0.0.1:8099`**）`AUTH=dev` 固定・`<pkg>/oauth.env` read）→ `exec bin/af-cp`。`af reset [--all] [--yes]`: do_reset 移植（docker 分岐なし・pidfile group kill のみ・`--all` は展開済み rootfs 含む WS_DATA 全体）。`af status`: pidfile＋/proc 照合の簡易表示 |
| 9 | `deploy/native/README.md`（C 同梱） | WSL 前提の導入（展開→`./af start`→`http://localhost:8099`）・ホスト要件（**Linux カーネル userns＋bash/coreutils/tar＋curl or wget** — zstd 不要）・Ubuntu 23.10+ の `apparmor_restrict_unprivileged_userns` sysctl 注記・systemd user unit 見本・更新（新 tar 展開→af start・データは WS_DATA で不変）・バックアップ・air-gap（`--bundle-rootfs` / `af start --rootfs`）・制約（単一ユーザー AUTH=dev・メモリ上限なし・chromium sandbox の割り切り） |
| 10 | `NOTICE` | 静的同梱分の帰属追記: bubblewrap（LGPL）・git（GPLv2 — 対応ソース＝上流 tarball 版の入手案内を明記）・zstd（BSD/GPLv2 dual）。P1 の「静的 bwrap/git 分は P2 で追加」の消化 |
| 11 | `.github/workflows/release-gate.yml` | **native ゲート job 追加**（既存 2 job は不変）。内容は下記ゲート (d)(e) |
| 12 | `docs/34-native-runtime.md` | §34.5 制約表へ rootfs モードの帰結を追記（「実行環境はホスト任せ」「焼き込みピン止め無し」「entrypoint 初期化なし」の 3 行が rootfs モードでは解消）。§34.3 に af 経由の導線を注記 |

**bwrap argv（凍結 — docker run ↔ bwrap 対応表 §35.3.1 の実装形）**:

```
bwrap --ro-bind <rootfs> /
      --dev /dev --proc /proc --tmpfs /tmp --tmpfs /run
      --bind <dataDir>/home /home/dev
      --bind <dataDir>/claude-config /var/lib/af/claude
      [--ro-bind <dataDir>/docs /usr/local/share/agent-fleet/docs]     # 存在時
      [--ro-bind <WS_JVM_DIR> /usr/lib/jvm]                            # 設定・存在時
      [--ro-bind /etc/resolv.conf /etc/resolv.conf]                    # 存在時
      [--ro-bind /etc/hosts /etc/hosts]                                # 存在時
      --unshare-user --uid 1000 --gid 1000
      --unshare-pid --die-with-parent
      --chdir /home/dev
      /usr/local/bin/entrypoint.sh workspace-agent
```

- net/uts/ipc は unshare **しない**（agent の loopback bind を CP と共有するのが要件。
  最小の namespace 分離に留める）。`--dev` は devpts 込み（tmux の pty に必要）。
- uid 写像: 実 uid → 1000（dev）。rootfs も home も実 uid で展開・作成されるため、
  namespace 内では全部 1000 所有に見える（WSL 既定ユーザー uid=1000 なら実質恒等）。
- `--die-with-parent`: CP 縁切り時の孤児を防ぐ…のではなく**逆**に注意 — Start は
  Setsid で CP から切り離す既存設計のため、bwrap の親は init になる。die-with-parent
  は「bwrap の直接親（=なし）」にしか効かないので害なし・保険として付けるのみ。
  停止の正は従来どおり process group SIGTERM→SIGKILL。

**P2 ゲート**（P1 と同じく hosted CI 実走 — release-gate.yml。native 実機は §35.8 の
P4 ゲートに残す）:

- (d) `VERSION=x build.sh --native` で C＋R＋D が生成される。C の内容検証: `af`・
  `bin/` 5 バイナリ（`ldd` が「not a dynamic executable」= 静的であること）・
  rootfs.json の sha256 が R と一致・console/・docs（denylist 適用済み）・NOTICE。
- (e) hosted runner（sudo で userns sysctl 緩和可）で C を展開し
  `af start --rootfs <R>` → `/healthz`・`/api/version` 応答 → dev 認証で workspace
  作成/起動（API 直叩き）→ bwrap 配下の agent が healthy → rootfs 内 entrypoint の
  boot-install がピン版を仮想 HOME `~/.local/bin` へ導入していること（版一致）→
  `af reset --yes` が残骸なく掃除。
- (f) 既定経路の無影響: 既存 compose/default ゲート 2 job が不変で緑・CP/agent の
  全 Go テスト緑（rootfs モードの unit テストは fake bwrap の helper-process で
  argv/env 合成・lifecycle を固定）。

素の WSL2 実機（追加インストールなしの通し E2E・chromium sandbox 実測・
playwright CDN の実 DL 確認）は §35.8 native ゲート（P4）のまま。

**実装時の確定事項（2026-07-21・feat/packaging）**: 表の 12 項目すべて実装済み。追加の確定:

- **agent 健康待ちのノブ化**: Start の `waitAgentHealthy` は docker/native とも 15s
  固定だったが、lean の初回起動は entrypoint の boot-install（npm/GitHub DL・分単位）が
  agent の listen より先に走るため必ず超過する。`AF_AGENT_HEALTH_WAIT_SEC` で上書き
  可能にし、**rootfs モードの既定は 300s**（docker は既定 15s のまま — lean B を CP 配下で
  動かす場合はこの env を設定する）。
- versions.json に **`noto_cjk` ピンを新設**（`NOTO_CJK_VERSION` ARG → install-chromium
  の CJK フォント供給元 = notofonts/noto-cjk の versioned tag。e2e-smoke の全ピン検証にも追加）。
- ペイン自動導入の Console 側は新 state を増やさず **error code `browser_installing`
  （503）+ 5s 自動リトライ + 「準備中」表示**で実装（controller.ts / BrowserPane.tsx / i18n）。
- Go toolchain の選択肢は `"system"（焼き込み or なし）∪ ビルドピン ∪ 導入済み版`。
  entrypoint は「選択版が home に無く、焼き込みピンとも不一致」の時だけ install-go を走らせる。
- mcp-grafana のオンデマンド導入は `mcp-run grafana` の実行経路内（grafanaMCPPath の
  最終 fallback）で行う。cloudwatch の uvx fallback は `==<cloudwatch_mcp ピン>` 固定に変更。
- 静的ツールの版ピン（Dockerfile.tools の ARG）: bubblewrap 0.11.0 / git 2.47.3 /
  zstd 1.5.7。ビルダー内で `ldd` による静的性検証を行い、CI ゲート (d) でも再検証する。
- **bind マウントポイントは rootfs へ焼く**（初回ゲート実走が検出した真因バグ）: docker は
  `-v` 先ディレクトリをコンテナ内に自動作成するが、bwrap の `--ro-bind rootfs /` 配下は
  read-only で mkdir 不能 → `/var/lib/af/claude`・`/usr/local/share/agent-fleet/docs`・
  `/usr/lib/jvm` が無い rootfs では起動即死する。workspace/Dockerfile で 3 箇所を空のまま
  焼き、CP factory にも「旧イメージ由来 rootfs は起動前に明示エラー」の fail-fast を追加。
- release-gate.yml はコミットメッセージ `[native-only]` で P1 系 2 job を省略できる
  （native ゲートの反復用。最終検証は無印 push で 3 job）。

**P2 ゲート結果（2026-07-21・hosted CI 実走 run 29805585420 全緑）**:

- (d) ✅ `VERSION=0.0.0-ci build.sh --native` で C＋R＋D 生成（ビルド 8.5 分）。
  静的性（ldd）・rootfs.json sha256 = R 一致・console/docs（denylist 適用）/NOTICE/VERSION
  同梱・image-env.json の tar 内在、全て検証。
- (e) ✅ runner（userns sysctl 緩和のみ）で C 展開 → `af start --rootfs` → CP healthy・
  `/api/version` 一致 → `POST /api/workspace/start` が **29 秒で `state:"running"`**
  （bwrap 配下で rootfs の entrypoint→boot-install→agent healthy まで同期）→ 仮想 HOME に
  claude 2.1.215 / opencode 1.18.3 / codex 0.144.6 / copilot 1.0.73 / rtk / agy がピン版で
  導入済みを実証 → workspace stop → `af reset --all` が WS_DATA を残骸なく削除。
- (f) ✅ 最終フル run（run 29806179614・3 job 全緑）で再実証: native-gate の再現緑に加え、
  P2 の共有ファイル変更（workspace/Dockerfile のマウントポイント焼き込み＋noto_cjk ピン・
  entrypoint の Go toolchain・e2e-smoke）込みで compose-gate（A+B+D 生成・lean
  boot-install ライブ）と default-image-gate（既定全焼き込みビルド + e2e-smoke）が
  無変更で通過 = 既存デプロイに影響ゼロ。

残（P4 実機ゲートへ）: 素の WSL2（追加インストールなし）の通し E2E・実 Console からの
セッション操作の目視・chromium sandbox の bwrap 配下実測・playwright CDN の実 DL 検証・
dist repo への実 publish（rootfs URL の疎通）。

### 35.7.3 P3 実装仕様（凍結）

P3 = ECS 配布のリリース作法（§35.3.4）。CFN・アダプタ本体（P3-7）は不変更で、
「push → deploy → 更新」の作法をスクリプト・パラメータ・runbook に固める。

| # | 対象 | 変更内容 |
|---|---|---|
| 1 | `deploy/aws/ecs/release-ecr.sh`（新設） | runbook §ECR push のスクリプト化。`VERSION=<v> release-ecr.sh --profile <p> --region <r> [--account <acct>] [--images-tar <B>] [--registry <local-prefix>]`。手順: ①`--account` 省略時は `aws sts get-caller-identity` で解決 ②**ECR repo の存在確認のみ**（`describe-repositories` で `af-control-plane`/`af-workspace`。不在なら「20-platform を先に deploy」の案内で fail — §35.3.4-1 の「create-repository（idempotent）」は**凍結時に否決**: repo の正は 20-platform CFN であり、out-of-band create は後続の CFN deploy を AlreadyExists で壊す）③`get-login-password \| docker login` ④`--images-tar`（air-gap B）指定時は `docker load` を前置（ビルド環境と push 環境の分離）⑤ローカル名 `agent-fleet/{control-plane,workspace}:$VERSION` → ECR URI `<acct>.dkr.ecr.<region>.amazonaws.com/af-{control-plane,workspace}:$VERSION` へ tag/push ⑥完了時に次の一手（`cloudformation deploy --parameter-overrides ImageTag=$VERSION`）を表示。runbook のコマンド列が正・スクリプトは写し、の関係を README に明記 |
| 2 | `cfn/30-ingress.yaml` | `CpImageTag`/`WorkspaceImageTag`（既定 dev・個別）を**単一 `ImageTag`（既定 dev）へ統合**。リリースは CP/WS を同一 VERSION で焼く（build.sh）ため版は常に揃う — 片方だけ進める運用は作らない。アップグレード = `aws cloudformation deploy --parameter-overrides ImageTag=<v>`（他パラメータは previous value 維持）。CP サービスは rolling replace、Workspace はアダプタがステートレスに TaskDefinition を作るため**次回 Start から新イメージ**（稼働中 WS は巻き込まない） |
| 3 | `cfn/10-data.yaml` | `Persistence` パラメータ（`delete`（既定・sandbox）/`retain`）。`Transform: AWS::LanguageExtensions` + Condition で分岐: EFS = DeletionPolicy/UpdateReplacePolicy `Retain`、RDS = 同 `Snapshot`＋`BackupRetentionPeriod` 0→7＋`DeletionProtection` true。「本番は `Persistence=retain`」を README の標準にする |
| 4 | `deploy/aws/ecs/README.md` | ①§ECR push を release-ecr.sh 前提に書き換え（手打ちコマンド列は正として残す）②**§Upgrade（更新 runbook）新設**: release-ecr.sh → deploy `ImageTag=<v>` → CP 入替・WS は次回 Start・DB/EFS/稼働中 WS 不変・事前バックアップ（EFS+RDS snapshot）推奨 ③**§最小 IAM 表**（P3-10 宿題）: デプロイ主体に要る権限をサービス単位で一覧 ④`Persistence` の説明と stand-up 例の `ImageTag` 反映 |
| 5 | `.github/workflows/release-gate.yml` | **`ecs-gate` job 追加**（docker/AWS 不要の軽量ジョブ）: cfn-lint で 4 テンプレ検証（LanguageExtensions 対応）+ `bash -n`/shellcheck + **fake-aws/fake-docker stub で release-ecr.sh を実走**し AWS CLI 呼び出し列（sts→describe-repositories→login→(load)→tag×2→push×2）と ECR URI 組み立てを固定（--images-tar 経路含む）。compose-gate に「バンドル A に release-ecr.sh が実行ビット付きで同梱」の assert を追加 |

**P3 ゲート**:

- (g) 静的（hosted CI・ecs-gate）: cfn-lint 4 テンプレ緑・release-ecr.sh の stub 実走で
  呼び出し列固定・shellcheck 緑・バンドル A への同梱確認。
- (h) sandbox 実走一巡（§35.7 表の出口: push → deploy → WS 起動 → タグ更新 → 次回
  Start で新イメージ）: **要 af-sandbox 実クレデンシャル**（開発 Workspace には未設定）。
  実施時は release-ecr.sh → `00→10→20→30` deploy（`Persistence=delete`）→ WS Start →
  `ImageTag` 更新 deploy → WS Stop/Start で新イメージ確認 → delete-stack 撤収、まで。

**P3 ゲート結果（2026-07-21）**:

- (g) ✅ hosted CI run 29807690647 **フル 4 job 全緑**（ecs-gate 新設分＋既存 3 ゲート
  無変更通過）。初回実走 run 29807545943 の検出 2 件を修正: ①runner の shellcheck は
  info（SC2015）でも exit 1 ②stub テストの `get-login-password | docker login` は
  パイプ両側が並行でログ順序が非決定 → 集合一致＋依存順序 assert へ（5 連続実行で安定）。
  ※副産物の教訓: ジョブスキップマーカー文字列をコミットメッセージ本文に書くと
  head_commit.message 一致で誤発火する（説明文にも書かない）。
- (h) ✅ **sandbox 実走一巡完了**（account 722507597273・ap-northeast-1・
  `af-h.lazmix.jp`）。この Workspace に docker が無いため、イメージは sandbox 内の
  使い捨て EC2（t3.xlarge）で `build.sh --compose` ×2 版（0.0.1-h / 0.0.2-h・lean 配布
  variant）をビルドし、**release-ecr.sh 実物**で push（repo 存在確認→login→tag/push。
  認証は region-only profile → IMDS チェーン = 「ビルド環境と push 環境が別」の実証）。
  - `00→10→20→30` deploy。10-data の `Persistence=delete` が Processed テンプレートで
    EFS/RDS `Delete`・Backup 0・DeletionProtection false に解決 =
    **LanguageExtensions 分岐の実機実証**（retain 側は cfn-lint 検証）。
  - **`AuthMode` パラメータを 30-ingress へ追加**（ゲート実施のための追加。既定 oauth
    不変・`dev` は sandbox/E2E 用 — ALB SG を自 IP に制限してから使う旨を README 化。
    ゲートでも deploy 前に ALB SG を workspace egress IP /32 へ制限した）。
  - CP 起動 → `/api/version` = 0.0.1-h（版刻印がビルド→ECR→ImageTag→Fargate まで通貫）。
    WS Start → task def `:0.0.1-h`・running。**発見: 初回 Start は ALB 60s idle timeout で
    HTTP は 504 になるが Start はサーバ側で継続し ~100s で running 収束**（アダプタの
    非同期収束設計どおり。Console は state ポーリングで吸収 — README に注記済み）。
  - `ImageTag=0.0.2-h` **のみ**上書き deploy → 他パラメータ previous value 維持で CP が
    ロール（`/api/version` = 0.0.2-h）、**稼働中 WS は同一タスク ARN・`:0.0.1-h` のまま
    無傷**。WS Stop→Start → 新 task def **`:0.0.2-h`** で running（EFS home 持続により
    boot-install 再走なし = 更新契約 §35.5 の「WS は次回 Start から」を実証）。
  - 撤収: af-ws* sweep（service / task def ×2 / EFS AP ×2 / SSM param ×2）→ stacks
    30→20/10→00 削除・EC2/keypair/IAM role 掃除。SSM `/af-cp/*` 3 param は P3-7 以来の
    既存物のため温存。

## 35.8 検証ゲート（P3-10 完了判定への接続）

| ターゲット | ゲート | 状態 |
|---|---|---|
| compose | clean host でバンドルから起動（P3-10 段5） | ✅ 実証済（ec2-single 上） |
| EC2-Single | 同上 + CFN provision〜teardown | ✅ 実証済 |
| native | **素の WSL2**（Docker なし・**追加インストールなし**）で tar から起動 → 初回 boot-install（ピン版）→ clone → claude セッション E2E → 再起動してオフライン起動（2回目は DL なし）。userns 無特権実行が WSL2 標準カーネルで通ることの確認を含む（§35.3.1 の仮説） | △ CI 分は済（§35.7.2 ゲート d/e 全緑 = hosted runner で af start→bwrap 起動→boot-install ピン実証）。素の WSL2 実機は未（docs/34 §34.6 と同時に消化する） |
| ECS | E2E ゲート（p3-7 凍結仕様 §20b.7.14）+ タグ更新の一巡 | ✅ タグ更新一巡実証（2026-07-21・§35.7.3 ゲート h: push→deploy→WS起動→ImageTag更新→次回Start新イメージ→撤収）。p3-7 E2E の attach/reaper 項は段階実証のまま |
| 総合 | 第 2 デプロイをゼロから立てて E2E（decisions/0001） | ✗ 未 |

## 35.9 決定事項と残る確認事項

**決定済み（2026-07-21・ユーザー判断）**:

1. **docs/ 同梱範囲 = 全ツリー − internal denylist**（HANDOFF / CHANGELOG-handoff /
   talk/ / history/ を除外。精査結果と denylist の設計は §35.4.3。自社ビルドは全ツリーのまま）。
2. **配布チャネル = 公開 dist repo `k-k1/agent-fleet-dist` を新設**（具体設計は §35.4.2）。
   完全ファイル渡しは `--bundle-rootfs` で併存。
3. **版表示は認証付き `/api/version` 新設**（§35.6.1。`/healthz` は restart-cp.sh の
   `ok` 完全一致比較があるため不変更）。
4. **Helm chart は棚上げを roadmap に反映**（P1 の作業項目）。
5. **静的 bwrap / 静的 git（NO_CURL）の自前ビルドを許容**（builder イメージ内で source
   から固定版をビルド。バイナリ拾い食いはしない）。

**残る確認事項（実装フェーズ内で消化）**:

6. **native の arm64 rootfs**: 需要待ち。dist repo に source を置かない決定（§35.4.2）に
   より公開 repo の無料 arm ランナーは使えない — 出す時は private repo の arm ランナー
   （$0.005/分）か arm 実機でビルド。
7. **オンデマンド供給元の実装確認**（P2）: (a) chromium の DL 供給元最終選定（playwright CDN
   を第一候補に、版不変 URL・arm64 供給・レイアウト互換で比較確認）。(b) bwrap 配下での
   chromium sandbox 実測（namespace sandbox が通るか。不可なら `--no-sandbox` + localhost
   限定の割り切りを README 化）。(c) awscli の versioned zip / SMP の versioned deb URL と
   root なし展開（`dpkg-deb -x` 相当）の確認。
8. **ライセンス見解の確度（配布開始前ゲート）**: §35.4.1 は npm registry / GitHub API の
   license 表示による一次調査。配布開始前に claude / agy / copilot の利用規約本文（再配布・社内限定
   配布の条項）を読んで確定させる（規約側が許すなら焼き込み配布へ戻す選択肢が復活する）。
