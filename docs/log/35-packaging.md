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
| ~~B~~ | ~~`agent-fleet-images-<v>.tar.gz`~~ | **廃止（[ADR 0037](../decisions/0037-registry-policy.ja.md)・2026-08-02）**。イメージは GHCR（`ghcr.io/k-k1/agent-fleet/{control-plane,workspace}`）で配る。レジストリに到達できないホスト向けに `release.sh --save` で手元生成する自助手順は残す（配布物ではない） | — |
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
  agy は公式installer manifestの不変 GCS object + `agy_build` + `agy_sha256` 検証 —
  P1 実装時にピン化。下の 2026-07-25 追記参照）。
- 仮想 HOME は永続なので **DL は初回起動の一度きり**。2回目以降はオフラインでも起動する。
- self-update opt-in（`AF_AGENT_SELF_UPDATE`）ON なら従来どおり最新へ追従、OFF なら
  ピン版のまま。書き込み先はすべて bind した仮想 HOME 側なので **ro rootfs のまま成立**する。
  - **REPIN（2026-07-28 追補）**: ON で進んだ `~/.local` の版は、opt-in を OFF に戻した
    次の起動（無人起動 `AF_AGENT_SELF_UPDATE_SKIP=1` を除く）に boot-install がピン版を
    再導入して戻す。従来は boot-install のガードが在/不在（`cli_present`）だけだった
    ため、一度 ON にすると OFF＋再起動でもピンへ戻らなかった（kiro の起動ガードで
    直したのと同型の穴）。版比較は npm 4 CLI＝`npm ls` 1 回、rtk＝`--version`、
    agy＝`.agy.version` マーカー（RDRAND 非提示ホストの SIGABRT 回避）、cursor＝
    symlink 先の `versions/<版>/` パス。ピン一致のツールは触らない（DL なし）。

社内・自社運用の Docker イメージも、**既定を lean-CLI（`BAKE_AGENT_CLIS=0`）へ反転した**
（2026-07-23。素の `docker build` でも再配布不可のプロプライエタリ CLI をイメージに含めない
安全既定にする狙い。初回起動時に boot-install がピン版を導入する）。オフライン即起動・
イメージ=検証単位の利点が要る自社デプロイは `BAKE_AGENT_CLIS=1` を明示すれば従来どおり
全焼き込みになる（`run-dev.sh` は `BAKE_AGENT_CLIS=1` env で対応・スモークも追随、
`default-image-gate` は明示 bake でその経路を担保）。`BAKE_OPTIONAL_TOOLS` の既定は 1 の
まま（サイズ系ツールはライセンス無関係）。抜く理由は 2 種類あり、適用範囲が違うので
**Dockerfile の ARG 2 ノブ**に正式化する:

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
4. ~~**レジストリ方針の決定**（§35.9 未決）~~ → **決着（2026-08-02・[ADR 0037](../decisions/0037-registry-policy.ja.md)）**:
   **イメージは GHCR で配り、B は廃止**。予見どおり `release.sh --push` を足すだけで済んだ
   （`REGISTRY` の間接参照が既にあったため）。
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

**自分の側の表示（決定・2026-07-25）。** 監査したところ、著作権表示は `NOTICE`
（`Copyright 2026 k-k1` ＋ Apache-2.0 明記）とルート `README.md` §License にしかなく、
**公開 dist repo には LICENSE も NOTICE も無かった**（README×2 と install スクリプト×2
だけ＝GitHub 上は「ライセンス未設定」に見える）。docs 配下の md に copyright ヘッダが
無いのは Apache-2.0 では正常（ルートの LICENSE + NOTICE が著作物全体を覆う）ので、
per-file ヘッダは**入れない**方針とする。対処:

- **1 次配布元の URL は `NOTICE` に書く。** Apache-2.0 **§4(d)** が「再配布時は NOTICE の
  帰属表示を可読な形で引き継ぐこと」を義務付けているため、NOTICE に書けば再配布者に
  自動で伝播する。README だけに書いてもこの効力は無い。文面は帰属表示＋§4(d)/§4(b) の
  再掲に留め、**「本 NOTICE はライセンスに条件を追加しない」と明記**する（§4(d) は
  追加の帰属表示を認めるが、ライセンスを改変すると解される記述は禁じているため）。
- dist repo へ `LICENSE` / `NOTICE` を seed する。**ソースはリポジトリルート**とし
  dist-repo/ に複製を置かない（tar 同梱物との drift 防止）。
- 配布 tar（A/C）には元から両方入っていたが、release-gate は `NOTICE` しか検査して
  いなかった。`LICENSE` の同梱と、NOTICE 内の 1 次配布元 URL の残存を assert に追加
  （`dist-stub-test.sh` case 12 でも URL を守る）。
- 受領者が読む `deploy/native/README.md` / `deploy/compose/README.md` と dist README
  （en/ja）にも 1 次配布元と §4(d) の一文を追記。

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
| `docs/HANDOFF.md` | **自ホストの稼働情報そのもの**: 運用者コンテナ名（メールアドレス由来）・ポート/パス・起動作法等。配布先には無意味（実入口 URL は公開リポジトリ化に際して本文から外し、`tailscale funnel status` で引く形にした） |
| `docs/CHANGELOG-handoff.md` | 時系列作業ログ。個人メールアドレス・実名ハンドルを含む |
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

#### 35.6.1.1 Console 表示（アカウントメニューの版帯）— 実装済み

§35.6.1 が「後続でよい」とした表示側。アカウントメニュー最下部にはもともと
**Console バンドルのビルド刻印**（`lib/version.ts`・vite define）しか出ておらず、
これは FE の時刻と短 SHA であって CP の版でもイメージでもない。ECS では
**コードはイメージとして届く**（CP も workspace も `30-ingress.yaml` の単一
`ImageTag` から作られ、運用者が上げ下げするのはそのタグ）ので、「版」と「イメージ」は
別の問いになる。障害報告にはどちらも要る。

- **API は `GET /api/version` の追記**（新設ではない）。`{"version": ...}` はそのまま
  残すので既存の読み手（`routes_test.go`・§35.8 の実機手順）が壊れず、新 REST を
  足したときの「agent 側と CP プロキシの allowlist 両方に登録」も発生しない。

  ```json
  { "version": "0.6.0", "runtime": "ecs-ec2",
    "image":           { "repo": "af-control-plane", "tag": "0.6.0", "digest": "sha256:…" },
    "workspace_image": { "repo": "af-workspace",     "tag": "0.6.0" } }
  ```

- **CP 自身のイメージの出どころは ECS タスクメタデータ v4**（`ECS_CONTAINER_METADATA_URI_V4`）。
  CFN は `ImageTag` をコンテナの `Image` に渡すだけで env には入れないので、CP が自分の
  イメージを名乗れる経路はこれしかない。IAM も SDK も不要（link-local）。成功は永久に
  キャッシュ（走っているタスクのイメージは変わらない）、失敗は 60 秒後に再試行。
- **workspace イメージは `AF_ECS_WORKSPACE_IMAGE`**（既存の `WorkspaceImage()` 能力）。
- **判らない項目はキーごと落とす**。docker/native にはどちらの経路も無いので、
  プロファイル文字列で分岐しなくても行が自然に消える（能力で判定する）。Console 側も
  キーの有無だけで描画を決め、`useDeploymentVersion` は**メニューを開いたときだけ**
  取りに行く（起動時 fetch を全タブぶん増やさない）。
- **レジストリ host は CP 側で落として返す**。全メンバーが読む面に AWS アカウント ID を
  載せる理由が無い。タグ + digest 先頭 7 桁で「どの実体か」は言える（`:dev` は MUTABLE
  なのでタグだけでは足りない）。
- ★ **ここで版を比較しない**。CP と Agent の版が意図的にずれるのは正常で、比較すれば
  恒久点灯する（`workspace_stale.go` の禁じ手）。バックエンドのドリフト検知は
  既存の stale 機構（`runtime_ecs_stale.go` → WS バーの「要再起動」バッジ）の担当で、
  版帯は表示だけに徹する。
- 帯の末尾にコピーボタン（版・両イメージ・Console ビルドを 1 ブロック）。
  §35.6.1 の目的が「障害報告に必ず版を添えられるようにする」ことなので、
  スマホで digest を読み上げさせない方をとる。

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
  （lean では ~/.local が boot-install 品そのものなので消してはならない。lean の
  ピン復帰は boot-install の REPIN が担う — 上記 2026-07-28 追補）。
- e2e-smoke の既存バグ修正: `EXPECT_COPILOT` が docker run へ渡っておらず、copilot
  統合後の smoke は set -u で落ちる状態だった。
- **agy も真のピンへ昇格**（P1 追補・2026-07-25）: 焼き込み・boot-install とも
  install.sh（常に latest・ピン不可）を廃し、公式installer manifestが示す不変 GCS object
  を `AGY_VERSION` + `AGY_RELEASE_BUILD` + アセット digest
  （`AGY_SHA256_X64/ARM64` → versions.json `agy_sha256`、build-id は `agy_build`）で検証して
  直接取得する。1.1.7 で GitHub Releases が追随しないことを確認したため、供給元を公式
  manifest/GCSへ統一した。「agy だけビルド時 latest の記録」という例外は解消
  （self-update opt-in の latest 追従は manifest 経路のまま）。
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

**§35.7.2-8 boot-install の可視性と「即起動」の扱い**（2026-07-21 の切り分けを凍結・
§35.9-9）: rootfs モードでも entrypoint は bwrap 配下で走り、lean 判定
（`/usr/local/bin/claude` 不在＋versions.json ピンあり — `node`/`versions.json` は rootfs の
`/usr/local` 側に実在し home bind-mount に隠れないため**初回起動では必ず lean 判定**）で
`~/.local` へピン版 CLI をオンデマンド導入する。**初回起動では 6 CLI が確実に DL される**
（`~/.local/bin` は空・`cli_present=false`）。ところが entrypoint 出力は **CP がワークスペース
の `<dataDir>/agent.log` にだけ**書き（`af`／Console のフォアグラウンドには出ない）、しかも
boot-install は `exec workspace-agent` の**前**に同期実行されるため、healthWait（既定 300s）中は
**無表示のまま待つ**画になる。回線が速いと「即起動・ログ無し」に見えるが、これは
**DL が起きていて誰にも見えないだけ**で、rootfs への焼き込みではない（実 rootfs
`4677f9f5a67d` を直接検証して lean 確認 — §35.9-9）。対処: **CP が healthWait 中に agent.log を
tail して `[entrypoint]` 行を `af start` ターミナルへ転写**（`runtime_native.go`
`mirrorBootProgress`＋開始時の一行ヒント）。entrypoint 側も明示ログ（`lean variant: …` ヘッダ
＋初回 DL 行／再起動時は `… already present (skip)`）を持つ。2 回目以降は `~/.local` 永続で
スキップ即起動＝オフライン再起動が成立する理由（§35.8.1 手順 6）。Console 起動オーバーレイ
への tail surface は別タスク（§35.9-9 の残 UX）。

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
  - **その後（2026-08-24）**: この予算は「超えたら失敗」ではなく「超えたら
    `starting` を名乗って返る」に変わった（docs/38 ★6 の恒久対応）。env を設定し
    忘れた lean デプロイが赤いエラーになることは無くなり、値は「同期で待つ猶予」
    だけを決める。docker の既定は **45 秒**（ingress の idle timeout 60 秒の内側。
    無人起動だけ 15 秒）、native rootfs は進捗表示のため 300 秒のまま。
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
  ゲート h での `AuthMode` 追加後（499e8cb）にも最終フル run 29810696327 で
  4 job 全緑を再実証。
- (h) ✅ **sandbox 実走一巡完了**（開発配備のアカウント・ap-northeast-1・
  `af-h.<domain>`）。この Workspace に docker が無いため、イメージは sandbox 内の
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

### 35.7.4 P4 実装仕様（凍結）

P4 = 検証ゲートの残り（§35.8）と配布チャネル（§35.4.2）の実体化。開発 Workspace は
コンテナであり素の WSL2 実機ではないため、P4 は二分する:
**①実機なしで完結する実装**（dist repo publish 一式・CI での代替/実測検証）と、
**②実機でしか測れない項目のチェックリスト化**（ユーザー実施 — §35.8.1）。
chromium CDN の実 DL 検証は dist-gate（CI）に置く。※着手時は「この WS の回線では
PRSS が 400」を再確認して CI を実測地点にしたが、ゲート初回実走で真因は回線ではなく
**「x64 アセットが供給元から消えていた」**と判明した（§35.9-7(a) 参照 — P1 以来の
「別回線で確認」は誤診で、amd64 の install-chromium は全環境で壊れていた）。

| # | 対象 | 変更内容 |
|---|---|---|
| 1 | `deploy/release/publish-dist.sh`（新設） | dist repo への publish。`VERSION=<v> publish-dist.sh [--dist-dir <d>] [--repo <o/r>] [--seed] [--dry-run]`。手順: ①C tar から rootfs.json を抽出し `<r>`/sha256/url を読む ②url が `--repo` の `releases/download/rootfs-<r>/…` と一致することを検証（ROOTFS_URL_BASE 誤りの C を publish する事故を未然に止める）③rootfs リリース `rootfs-<r>`: **既存 tag ならアップロードごとスキップ**（`<r>` 不変リリース＝利用者の再 DL なし、の実装形）・無ければ手元の R を sha256 照合してから `gh release create` ④app リリース `v<v>`: **既存 tag は fail**（リリースは不変・上書きしない）。添付は dist 直下の A・B・C（＋`-bundle` があれば）+ SHA256SUMS。**2GiB 超の資産は GitHub Releases の上限で添付不可 → 警告してスキップ**（B の air-gap はファイル渡し経路が正）⑤完了時に install ワンライナーと Releases URL を表示。`--seed` = repo 存在確認（無ければ `gh repo create --public`）→ `deploy/release/dist-repo/` の README.md / README.ja.md / install.sh / **install-compose.sh** を contents API（PUT・既存は sha 付き更新）で push（冪等） |
| 2 | `deploy/release/dist-repo/`（新設） | dist repo の seed 一式。`README.md`（何が置いてあるか・導入ワンライナー・air-gap 誘導・「成果物は lean＝エージェント CLI 非同梱、初回起動時にピン版を各自が配布元から取得」のライセンス注記）と `install.sh`（§35.4.2 の導入ワンライナー実装: 最新 or `AF_VERSION` 指定版の C を DL → 同リリースの SHA256SUMS で照合 → `~/.local/opt/agent-fleet/<v>/` へ staging→rename の原子展開 → `~/.local/bin/af` symlink。Linux/x86_64 ガード・curl/wget 両対応・`AF_DIST_URL_BASE` で供給元差し替え可＝CI の file:// テスト用。更新も同じコマンド＝版ディレクトリ切替）。加えて **`install-compose.sh`**（Compose 版の取得ヘルパー: 最新 or `AF_VERSION` 指定版の A バンドル + B イメージ tar + SHA256SUMS を DL → 照合 → `./agent-fleet-<v>/` へ展開 → 同梱 `load-images.sh` で `docker load` → 残りの手動手順〈`.env` 編集→`docker compose up`〉を表示。`.env` 手編集が必須なので完全ワンライナーにはならない。B が 2GiB 超で未添付でも `AF_SKIP_IMAGES=1` 相当で継続可・`docker`/`docker compose` の存在チェックあり） |
| 3 | `deploy/local/dist-stub-test.sh`（新設） | fake gh で publish-dist.sh の呼び出し列を固定(新規 publish / `<r>` 再利用スキップ / app tag 衝突 fail / url 不一致 fail / --seed)＋ install.sh の **file:// 実走**（偽 dist レイアウトから DL→sha 照合→展開→symlink→`af` 実在まで。sha 改竄で fail する否定経路含む） |
| 4 | `.github/workflows/release-gate.yml` | **`dist-gate` job 追加**（軽量・docker 不要）: bash -n + shellcheck（publish-dist.sh / install.sh / dist-stub-test.sh）+ dist-stub-test.sh 実走 + **chromium CDN 実 DL 検証**（§35.9-7(a) の消化: versions.json ピン build の zip を第一ホストからフル DL → unzip で `chrome-linux/chrome` 実在 → fallback 2 ホストは ranged GET + 先頭 1KiB が第一ホストと一致＝同一物確認 → noto_cjk raw URL の疎通）。**native-gate へ「ビルド済み C を file:// 経由で install.sh 導入 → 導入先の `af` が動く」step を追加**（インストーラと実成果物の噛み合わせを実 tar で検証）。反復用マーカー `[dist-only]` を追加（dist-gate 以外をスキップ） |
| 5 | `.github/workflows/publish-dist.yml`（新設） | 実 publish の CI 経路（§35.4.2「private Actions の workflow_dispatch」の実装）。inputs: `version`。secret **`DIST_PUBLISH_TOKEN`**（fine-grained PAT・dist repo の Contents RW）必須 — 無ければ設定手順を案内して fail。手順: `build.sh --all` → `publish-dist.sh --seed`。`<r>` 不変リリース（`--rootfs-json`）はローカル実施（§35.8.2 runbook） |
| 6 | docs | §35.8 表の更新（配布チャネル行の追加）+ **§35.8.1 native 実機ゲート チェックリスト（ユーザー実施）**新設（環境記録・導入・起動・E2E・chromium sandbox 実測コマンドと判定・オフライン再起動・報告様式）+ **§35.8.2 実 publish runbook**（初回セットアップ: repo 作成 → PAT → secret → dispatch → 実機 install 検証）。docs/34 §34.6・deploy/native/README.md から相互参照（README にはワンライナー導入を追記） |

**P4 ゲート**:

- (i) dist-gate（hosted CI）: stub 実走全緑・chromium CDN 実 DL 検証・native-gate の
  install.sh 実 tar 導入 step 緑。
- (j) 実 publish 一巡（**ユーザー実施** — §35.8.2）: dist repo 新設 → publish-dist.yml
  dispatch → Releases に `v<v>` / `rootfs-<r>` が付く → 任意の Linux で install
  ワンライナー → `af start` が rootfs を**実 URL から** DL して起動。
- (k) 素の WSL2 実機 E2E（**ユーザー実施** — §35.8.1 チェックリスト）: 通し E2E +
  chromium sandbox 実測 + オフライン再起動。

**P4 実装状況・ゲート結果（2026-07-21・feat/packaging 〜4f2ec3a）**: 表の 6 項目
すべて実装済み。ゲート (i) ✅ = **最終フル run 29813287446 で 5 job 全緑**
（dist-gate 新設 + native-gate の install.sh 実 tar step を含み、既存 compose/native/
default/ecs ゲートも無変更通過）。実走で得た確定事項:

- **ゲートが実バグを 2 件検出**（stub では出ない・実物で出る、の典型）:
  1. `af` ランチャが `BASH_SOURCE` の dirname を PKG とするため、install.sh の
     symlink（`~/.local/bin/af → <pkg>/af`）経由だと rootfs.json を見失う →
     `readlink -f` で実体パスまで辿るよう修正。native-gate に「symlink 経由で
     manifest まで届く」判別 assert を常設。
  2. **amd64 の install-chromium が全環境で失敗する状態だった**（chromium_cft 移行
     — 経緯・修正は §35.9-7(a)）。dist-gate 初回失敗（run 29812636687）が検出。
- dist-gate の CDN 実測は毎 run: CfT 第一経路フル DL + `chrome-linux64/chrome`
  レイアウト + Google バケット直との先頭 1KiB 同一物確認 + arm64 旧 3 経路 +
  noto_cjk 疎通（初回緑 run 29813227165 → フル run で再実証）。
- runner の shellcheck は SC2015(info) でも exit 1（P3 の教訓の再演 — ローカル
  shellcheck は既定で info を出さないので `-S info` で合わせて事前検証する）。
- **workflow_dispatch は `head_commit.message` が空**のため `[dist-only]` 等の
  スキップマーカーが効かず常にフル実走になる（dispatch でフルゲートを兼ねられる。
  部分実行の反復は workflow ファイルを触る push + マーカーで行う）。

**ゲート (j) 結果（2026-07-21・✅）**: §35.8.2 runbook どおりに実 publish 一巡を完走。

- セットアップ: `k-k1/agent-fleet-dist`（public）新設 → fine-grained PAT（対象 =
  dist repo のみ・Contents RW）→ private 側 secret `DIST_PUBLISH_TOKEN` 登録（PAT/
  secret はユーザーがブラウザで実施）。
- **runbook に無かった前提を実測で発見**: `workflow_dispatch` は**ワークフローファイル
  が default branch に存在しないと 404**（実行 ref に在っても不可）。feat/packaging を
  develop へマージ（f818877・コンフリクトなし・合流後 build/test 緑）して解消。
- publish: `gh workflow run publish-dist.yml -f version=0.1.0` → run 29818785407 全緑
  （preflight → stub self-check → build A+B+C+R+D → publish → summary）。成果物:
  Releases `v0.1.0`（app 35KB / images 1.04GB / native 27MB / SHA256SUMS）+
  `rootfs-fc943ac06dfa`（linux-amd64）+ main に install.sh / README seed。
- install 検証（この開発 WS = Docker コンテナ・amd64 で実施）: raw URL の install.sh
  ワンライナー → latest 解決（releases/latest リダイレクト）→ native tar 実 URL DL →
  sha256 検証 → 展開 → symlink まで ✅。続く `af start` は rootfs を**実 URL から** DL
  → sha256 検証 → 915MB 展開まで ✅ し、bwrap 無特権 userns がコンテナ内で使えず
  設計どおりの案内メッセージで停止（想定内の環境制約 — 起動自体は CI ゲート d/e で
  実証済み。実機起動はゲート k の領分）。

**publish 履歴**: v0.1.0 → v0.1.1（英語化・アシスタントタブ等）→ **v0.1.2（2026-07-22・
run 29850262365 success）= 入力全断修正（ecd9c9f・workspace image に `GIT_TERMINAL_PROMPT=0`）
反映リリース**。workspace image 変更で **rootfs は `4677f9f5a67d` → `1aadff3b24b7` に更新**
（＝修正が配布 rootfs に載った証跡）。CI 経路 `gh workflow run publish-dist.yml --ref develop
-f version=0.1.2` で発火。install 検証（この開発 WS・scratchpad 隔離・AF_VERSION=0.1.2）:
実 URL DL → sha256 → 展開 → symlink → `af help` ✅、同梱 rootfs.json が新 rootfs
`1aadff3b24b7` の URL/sha256 を指すことを確認（`af start` の実起動はコンテナ userns 制約で
対象外 = 実機はゲート k で実証済み）。

ゲート (k) = **完了（2026-07-22・ユーザー実機・v0.1.1 / rootfs `4677f9f5a67d`）**。
手順 1〜7（手順 7 = systemd 常駐化の任意項目含む）全クリア。詳細と填まりどころは
§35.8.1 の「最終結果」を参照。全ゲート (a)〜(k) 消化済み。

### 35.7.5 出荷物の禁止語ゲート（ゲート l・2026-08-01）

**動機（実害が出た）**: public 化準備で全履歴から除去した社名が、**0.1.0〜0.5.0 の
出荷物すべてに入ったまま公開されていた**。経路は Console の Marp テーマ:
`MarpView.tsx` がテーマ CSS を `import.meta.glob(..., { query: "?raw" })` で読むため、
CSS が**コメントごと文字列リテラルとして**バンドルに焼き込まれる。**minify は文字列
リテラルの中身を触らない**ので、コメントはそのまま残る。焼き込まれた Console は
CP イメージ（`control-plane/Dockerfile` の `COPY --from=console`）にも native tar の
`console/` にも入るので、`agent-fleet-images-*.tar.gz` と
`agent-fleet-native-*.tar.gz` の両方が汚染された（A バンドルと rootfs は清白）。

**教訓は「ソースを見ても分からない」こと。** 履歴書き換え（filter-repo）はソースを
綺麗にしたが、**それより前にビルドされて公開済みの成果物には効かない**し、ソース側の
grep はビルドが生成・埋め込みする物を原理的に見られない。**成果物そのものを見る**
ゲートが要る。

**実装**:

| # | 対象 | 内容 |
|---|---|---|
| 1 | `deploy/release/forbidden.sha256`（新設） | 禁止語の台帳。**語そのものは書かない**（本リポジトリは public。平文で置けば「消したい物を自分で公開する」）。1 行 = `sha256(正規形)  正規形の rune 長  Rabin-Karp 値  id`。追加は `printf '%s' '語' \| go run ./deploy/release/scan --ledger /dev/null --add --id <id>`（語がファイルにもプロセス一覧にも残らない） |
| 2 | `deploy/release/scan/`（新設・独立 Go モジュール） | 走査器。**正規形** = 英数字を小文字化・それ以外の連続を空白 1 個へ畳む・両端 trim。成果物の全バイトを同じ規則で畳んだ rune 列に対し、台帳の各長さの窓を滑らせる。→ 表記ゆれ（`Foo Inc.` / `foo-inc` / `FooInc` / `foo\n * Inc`）も**長い語の内部**（`foo` in `foobar`）も**ファイル名**も捕まる。**ドキュメントに実例を書くと自分のゲートに引っかかる**（この行で実際に踏んだ）ので、説明は必ずダミー語で書くこと。判定は 2 段: 64bit Rabin-Karp をローリング更新 → 65536bit ビットマップで篩う → 生き残りだけ sha256 で確定（1 rune あたり定数回の乗加算で済むので GiB 級でも回る） |
| 3 | 同上（アーカイブ展開） | **拡張子ではなく中身で判別**する。`docker save` は OCI レイアウトを吐くのでレイヤは `blobs/sha256/<digest>`＝**拡張子なし**であり、名前で分岐する走査器はここで素通りする。gzip/zstd/xz/bzip2/tar/zip をマジックバイトで見て再帰展開（深さ 8 まで）。展開できない形式は**握り潰さず error** にする（未展開は「清白」に見えてしまう） |
| 4 | `deploy/release/forbidden.allow`（新設） | 誤検知の除外。パスは秘密でないので平文 `<id> <path-glob>`。現状 2 行 = cmudict（CMU 発音辞書・BSD-2 の上流ファイル）に台帳語と同綴りの英単語と人名が単語として載っているだけ、と中身を読んで確認したもの。**自前ビルド物は絶対に除外しない**（自前の出力ならソースを直すのが答え） |
| 5 | `deploy/release/scan-forbidden.sh`（新設） | 呼び出し口。既定で `deploy/release/dist` を走査 |
| 6 | `.github/workflows/publish-dist.yml` | **build と publish の間**に挿入。ここが本丸＝公開が不可逆になる直前の最後の一線 |
| 7 | `.github/workflows/release-gate.yml` | compose-gate（A+B+D・tar を捨てる前）と native-gate（C+R+D）に追加＝**ゲート (l)**。dist-gate の shellcheck 対象にも追加 |
| 8 | `.github/workflows/ci.yml` | `release-scan` job 新設。**仕組みの検証**（gofmt/vet/`go test`）と**ソースの走査**（`git archive` 経由）を毎回。ユニットテストは**架空の語**（`zarquon`）で「検出する／しない」の両方を通すので、**台帳の中身を知らなくてもゲートが空振りでないと言える** |

**設計上の決めどころ**:

- **台帳をハッシュにする代償は「部分一致が原理的にできないこと」** — のはずだが、
  正規形に畳んだ rune 列へ**窓を滑らせて**その窓を毎回ハッシュすれば部分一致になる。
  素朴にやると GiB 級で破綻するので、Rabin-Karp のローリング更新で 1 rune 定数時間に
  落とし、ビットマップで篩ってから sha256 で確定する 2 段にした。
- **語長を台帳に載せる必要がある**（窓幅が要るため）。総当たりの手がかりを少し与える
  が、そもそも辞書語なので隠蔽としては元々弱い。**目的は「grep や検索エンジンで
  引っかからないこと」**であって秘匿ではない。
- **窓はファイルをまたがない**（leaf ごとに状態を reset）。またぐと隣接ファイルの
  末尾と先頭が偶然つながって**偽の一致を作る**。
- **短すぎる語は登録できない**（5 rune 未満は error）。汎用語を入れるとイメージ内の
  第三者コンテンツで恒常的に落ちる。
- **人名はフルネームで、語順ごとに 1 件**登録する。姓だけ・名だけは入れない。
  実測（3.6GiB の実出荷物）で、よくある日本語の名前 1 語は**無関係な第三者
  コンテンツ 4 か所**に一致した（vim の changelog・git-lfs バイナリ・yarn 同梱の
  `cli.js`・Debian の copyright ファイル）。これをパスで除外して回るのは
  **ベースイメージを上げるたびに増える恒常的な保守コスト**になる。フルネームなら
  雑音なしで信号だけ拾える。姓 1 語だけの出現は捕まえられなくなるが、割に合う。
- **既知の限界**: 圧縮ストリームは**丸ごと 1 本の場合だけ**展開する。大きなファイルの
  **内部**に埋まった圧縮 blob（`go:embed` した `.gz` 等）の中身は見えない。自前の
  Console / Go バイナリがユーザーに見せる物はディスク上で非圧縮なので実害はない。

**ゲート (l) の一次検証（2026-08-01）**: 公開済みの
`agent-fleet-native-0.5.0-linux-amd64.tar.gz` を実際に走査し、
`console/assets/index-Bm5SUH-b.js` の offset 1737506 を**語を出力せずに**指して
exit 1 することを確認（同 0.1.0 も同様）。A バンドルと現ソースは clean。

**0.5.1 の publish 実走で分かったこと（ゲートは 3 回目で通した）**:

1. **第三者の壊れたアーカイブで全体が止まる。** workspace イメージは Go の
   ソースツリーを含み、`archive/tar/testdata` には**わざと壊してある** tar が
   9 個入っている。→ メンバの展開失敗は `::warning::` で記録して継続、成果物
   そのもの（最上位）が読めない場合だけ hard error、という二分に変更した。
   同時に fail-open の穴も塞いだ（展開器が本体破損を報告するのは構築時ではなく
   最初の `Peek`。その error を握り潰していたので**壊れた最上位が clean で通って
   いた**）。
2. **姓だけ・名だけは誤検知だらけになる**（上の「人名はフルネームで」）。
3. **ドキュメントに実例を書くと自分のゲートに引っかかる。** §35.7.5 の説明文と
   `walk.go` のコメントに実際の語を例として書いてしまい、docs は CP イメージにも
   native tar にも入るので publish が落ちた。**説明は必ずダミー語で書くこと。**

3 回目の実走で 3.6GiB / 81300 ファイルを **1m30s（40.9MiB/s）** で走査し clean。

**出荷済み 0.1.0〜0.5.0 の撤回**: 全 10 リリースの資産（A/B/C/SHA256SUMS）を削除し、
リリース本文に撤回注記を追加。タグとリリースノートは公開記録として残す（リポジトリ内
CHANGELOG と版→commit 台帳が参照しているため）。`rootfs-*` リリースは Console を
含まないので清白＝存置。スクラブ後のソースから **0.5.1** を再ビルドして再公開した。

### 35.7.6 撤回済みリリースの資産復旧（republish）

★**旧タグのソースは既にスクラブ済み**である（filter-repo が全履歴を書き換えたので、
`v0.1.0` の時点でもテーマ CSS のコメントは無害化されている）。したがって**版ごとの
patch は不要**で、タグから素直に再ビルドすれば clean な成果物になる。これが復旧を
現実的にしている前提。

- `deploy/release/republish-dist.sh`（新設）= **既存リリースへの資産再添付専用**。
  `publish-dist.sh` が既存タグを hard fail するのは正しい（リリースは不変）ので、
  そちらは触らず別スクリプトにした。ガードは 4 つ: ①app リリースが存在すること
  ②`rootfs-<r>` が存在すること ③★**app リリースの資産が 0 個であること**
  （＝資産を消された版だけが対象。健全なリリースを再ビルド版で黙って置換するのは
  publish-dist.sh が守っている不変性そのもの）④C に焼かれた rootfs URL が対象と
  一致すること。
- **rootfs は再ビルドしない。** `<r>` は content-addressed で、その rootfs リリースは
  撤回していない。マニフェストは失われた C の中にしか無かったが、**全フィールドが
  復元可能**（hash＝タグ、digest と size＝現存アセットを落として計算）なので、
  再構成して `build.sh --rootfs-json` で再利用する。利用者の再 DL も発生しない。
- `.github/workflows/republish-dist.yml`（新設）= **checkout を 2 つ**行う。
  ソースは**タグ**から、**ゲートは現行ブランチ**から。旧タグは禁止語ゲートより前の
  時点なので、タグだけを checkout すると**唯一の安全装置が無い状態で再ビルドする**
  ことになる。concurrency group は `publish-dist` と共有（dist repo を同時に触らせない）。
- `deploy/local/republish-stub-test.sh`（新設）= fake gh でのスタブ実走。本文書き換えは
  **公開済みリリースを in-place で書き換える**操作なので、①注記を差し替えても
  **下のリリースノートを食わない**こと（ノート本文にも `---` があるため、単純に最初の
  `---` で切ると英語ノートが消える）②再実行が冪等 ③rootfs 不一致・未公開版・
  資産が残っている版はいずれも拒否、を固定した。

**注記は「撤回」から「再ビルド」へ差し替える**（バイナリを並べた上に「バイナリは
削除しました」を残すのは、何も書かないより悪い）。★**再ビルド版は当初公開された
バイト列とは同一にならない**（ベースは浮動タグ、apt は無ピン、モジュールは取得時点
依存）ので、注記にその旨と「`SHA256SUMS` は初回公開時と異なる」ことを明記する。
**推奨版は 0.5.1 以降**である点も併記する。

★★**復旧できるのは「chromium の pin が現行と一致する版」だけ**（実測 2026-08-01）。
旧版は chromium を Debian の**厳密な版で pin** している（0.1.0 = `150.0.7871.124`、
0.2.0〜0.4.0 = `150.0.7871.181`、0.5.0 = `151.0.7922.71`）。**supersede された版は
再ビルドできない。**

★**ここで一度誤診した。** 「pool に .deb が残っているか」を `curl -I` で確認して
3 版とも 200 だったので「今なら通る」と判断したが、**層が違う**。`apt-get install
pkg=version` は **pool ではなく `Packages` インデックス**で解決し、インデックスは
**現行版しか載せない**。実測すると bookworm-security のインデックスに載っている
chromium は `151.0.7922.71-1~deb12u1` の 1 つだけで、pool には
`150.0.7871.181-1~deb12u1` が 200 で残っているのに `Version ... was not found` で
落ちる。**pool の存在は buildability の代理指標にならない。**

したがって 0.5.0 は復旧できた（pin が現行と一致）が、0.1.0〜0.4.0 は**そのままでは
復旧不能**。選択肢は (a) 諦める (b) chromium の pin を現行へ上げる（＝当時出荷して
いない chromium を積むことになる）(c) pool の .deb を直接入れる（＝Dockerfile の
書き換えが要り、かつ**修正済み CVE を含むブラウザを意図的に再公開する**ことになる。
pool もいずれ掃除されるので延命にしかならない）。

**結論（2026-08-01）: (a) を採り、0.5.0 のみ復旧して 0.1.0〜0.4.0 は撤回のまま
据え置く。** pin を上げれば形の上では復旧できるが、それは「当時と違う中身の 0.4.0」を
並べることであり、**当時のビルドは再現できないという事実に対して一番正直なのは、
復旧しないこと**だと判断した。0.5.0 が復旧済みで 0.5.1 が推奨版なので実利の損失も
ほぼない。

**残る性質として覚えておくこと**: 出荷物の再現性は **Debian の索引の寿命に縛られる**。
厳密版 pin は「そのときに publish する」ためのもので、**後から同じ物を作り直す保証には
ならない**。過去版を作り直せることに価値を置くなら、pin だけでなく**取得元の固定**
（snapshot.debian.org 等）が要る——ただし本プロジェクトは過去版の再現に価値を置かない
（撤回して前進する）と決めたので、現状のままとする。

**サポート方針（2026-08-02 決定）: 過去版にセキュリティ更新は提供しない。**
撤回した版を「supersede されたから」という理由で消して回る運用は採らない（それを方針に
すると Releases が恒久的に最新版だけになり、旧版バイナリを残す一般的な慣行からも
外れる）。代わりに**方針を明記する**: 復旧した版の注記には「過去版にセキュリティ更新は
提供しない／同梱ブラウザとシステムパッケージは古くなる／pin が索引から外れた時点で
再ビルドすらできなくなる／最新版を使うこと」を二言語で入れる（`republish-dist.sh` の
`notice()`）。文面を後から変えたときは `AF_REPUBLISH_STAGE=notice` で本文だけ差し替え
られる（資産 0 個ガードは upload 経路だけに掛かる）。

### 35.7.7 「そもそも焼き込むべきか」への回答

上記から自然に出る問い。**答え: 主配布チャネル（native）は既に焼き込んでいない。焼いて
いるのは compose のイメージだけで、それは仕様である。**

| 経路 | `BAKE_OPTIONAL_TOOLS` | chromium |
|---|---|---|
| native（C+R・ワンライナー導入の本線） | `build.sh` が明示的に **0** | 焼かない。初回起動の boot-install が `versions.json` のピンで取得 |
| compose のイメージ（GHCR で配布） | `release.sh` は未指定 → Dockerfile 既定の **1** | 焼く |

★**[ADR 0037](../decisions/0037-registry-policy.ja.md)（B の廃止）はこの行を消していない。**
B は「`docker save` で tar 化して配る」経路が無くなっただけで、**イメージ自体は同じものを
GHCR へ push する**。したがって **compose のイメージは今も chromium を apt の厳密版 pin で
焼いており、`ARG CHROMIUM_VERSION` が Debian の security 更新で腐ると publish のビルド段が
exit 100 で落ちる**（0.4.0 が再ビルド不能だった原因そのもの）。**publish 前の chromium ピン
確認は ADR 0037 後も必須**（§35.8.2 の手順・[cli-version-pin-e2e] の再発警告）。B の廃止で
消えたのは「配布物としての 960MB」であって、この脆さではない。

★**ただし「焼かなければ再現性が保てた」わけではない。** lean 経路も
`CHROMIUM_CFT_VERSION` / `CHROMIUM_DL_VERSION` で**CDN 上のビルドをピン**しており、
そちらも供給元から消える（dist-gate に chromium CDN の実 DL 検証を常設しているのは
まさにこの腐敗を検知するため。§35.9-7(a) では実際に x64 アセットが消えて全環境で
壊れた）。**焼く／焼かないは失敗の時点をビルド時から実行時へ移すだけ**で、古びること
自体は消えない。しかも旧版にとっては実行時失敗の方が悪い（利用者の手元で起動しない）。

したがって:
- **再ビルド可能性**を本当に上げたいなら、レバーは「焼くのをやめる」ではなく
  **取得元の固定**（apt は snapshot.debian.org、chromium は自前ミラー）。
- **セキュリティの陳腐化**は、凍結されたリリースに対しては**どうやっても解けない**。
  解けるのは「新しい版を出す」ことだけ。だから答えはアーキテクチャ変更ではなく
  上記のサポート方針になる。
- compose のイメージが焼くのは、**レジストリから 1 つ pull すれば即使えることが
  compose 版の価値**だから。ここを外すと起動のたびに各ワークスペースが chromium を
  取りに行くことになり、docker の SUID `chrome-sandbox` も失う（§35.4.1・ADR 0037 の
  「採らなかった案」）。

## 35.8 検証ゲート（P3-10 完了判定への接続）

| ターゲット | ゲート | 状態 |
|---|---|---|
| compose | clean host でバンドルから起動（P3-10 段5） | ✅ 実証済（ec2-single 上） |
| EC2-Single | 同上 + CFN provision〜teardown | ✅ 実証済 |
| native | **素の WSL2**（Docker なし・**追加インストールなし**）で tar から起動 → 初回 boot-install（ピン版）→ clone → claude セッション E2E → 再起動してオフライン起動（2回目は DL なし）。userns 無特権実行が WSL2 標準カーネルで通ることの確認を含む（§35.3.1 の仮説） | △ CI 分は済（§35.7.2 ゲート d/e 全緑 = hosted runner で af start→bwrap 起動→boot-install ピン実証。install.sh 実 tar 導入 step 含む）。素の WSL2 実機は未 = **ユーザー実施**（§35.8.1 チェックリスト。docs/34 §34.6 と同時に消化する） |
| ECS | E2E ゲート（p3-7 凍結仕様 §20b.7.14）+ タグ更新の一巡 | ✅ タグ更新一巡実証（2026-07-21・§35.7.3 ゲート h: push→deploy→WS起動→ImageTag更新→次回Start新イメージ→撤収）。p3-7 E2E の attach/reaper 項は段階実証のまま |
| 配布チャネル（dist repo） | publish 一巡（§35.7.4 ゲート j: repo 新設 → publish → install ワンライナー → rootfs 実 URL DL 起動）+ chromium CDN 実 DL（ゲート i） | ✅ ゲート i（フル run 29813287446 全緑 = stub/CDN 実 DL/install.sh 実 tar）+ ゲート j（2026-07-21 実 publish 一巡 = v0.1.0/rootfs-fc943ac06dfa 公開 → 実 URL install → rootfs 実 URL DL/検証/展開。起動段のみコンテナ環境制約で bwrap 案内終了 = 実機起動はゲート k で確認。詳細 §35.7.4） |
| 出荷物の禁止語 | 全成果物（A/B/C/R）を再帰展開して禁止語を走査（§35.7.5 ゲート l）。publish-dist の build と publish の間に挿入＝**通らなければ公開されない** | ✅ 実装済（2026-08-01）。公開済み 0.5.0 native tar で検出を実証・現ソースは clean |
| 総合 | 第 2 デプロイをゼロから立てて E2E（decisions/0001） | ✗ 未 |

### 35.8.1 native 実機ゲート チェックリスト（ゲート k・ユーザー実施）

素の WSL2 でしか測れない項目の手順書。所要 ~30 分（rootfs/CLI の DL 時間を除く）。
結果は下の報告様式で持ち帰れば docs へ反映できる。

**前提**: Windows 11 + WSL2 の Ubuntu 系ディストロ（**新規 import 直後が理想** —
docker / node / エージェント CLI を入れていないこと）。確認と記録:

```bash
wsl.exe --version          # (Windows 側) WSL/カーネル版
uname -r && cat /etc/os-release | head -2
command -v docker tmux node claude || true   # 何も出ないのが「素」の証明
systemctl --user is-system-running || true   # systemd 有効か（常駐化検証に使う）
```

**手順とチェック項目**:

| # | 手順 | 期待結果（✓/✗ を記録） |
|---|---|---|
| 1 | 導入: `curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh \| bash`（publish 済みの場合）または tar を手で展開 | `~/.local/bin/af` ができ、`af` が PATH で引ける（WSL 既定の PATH は `~/.local/bin` を含む） |
| 2 | `af start` 初回 | preflight（bwrap userns）が**素通り** = 「WSL2 標準カーネルは AppArmor 制限なし」仮説の実証。rootfs が実 URL から DL・sha256 検証・展開されて CP 起動 |
| 3 | Windows 側ブラウザで `http://localhost:8099` | Console が出る（WSL2 の localhost フォワーディング） |
| 4 | Workspace 起動 → repo clone → claude セッション開始 | 初回 boot-install がピン版 CLI を仮想 HOME へ導入。claude ログイン → 実プロンプト一往復まで |
| 5 | **chromium sandbox 実測**: ブラウザペインを開く（初回は「準備中」表示 → ピン版 chromium 自動 DL ~200MB）。併せてワークスペースのターミナルで下のコマンド | ペインにページが描画される。コマンドが `<html>` を出力すれば **namespace sandbox が bwrap 配下で動く**（`AF_CHROMIUM_NO_SANDBOX` 不要）— 結果を必ず記録 |
| 6 | Ctrl-C で停止 → `af start` 2 回目 | **DL が一切走らず**即起動（rootfs 再利用・オフライン起動の実証。厳密にやるなら `wsl.exe --shutdown` 後にネット断で起動） |
| 7 | （任意）systemd user unit 常駐化（README-native 参照） | `systemctl --user status agent-fleet` が active |

手順 5 の sandbox 実測コマンド（ワークスペース内ターミナルで）:

```bash
c="$(ls -d ~/.local/share/agent-fleet/chromium/*/chrome-linux*/chrome | head -1)"
"$c" --headless=new --disable-gpu --dump-dom about:blank | head -3
```

- `<html>…` が出る → sandbox 有効のまま動く（**期待**: bwrap は net/ipc を unshare
  しない最小分離なので、chromium 自身の user-namespace sandbox は入れ子で動く見込み）。
- `Operation not permitted` / zygote crash 系で落ちる → `AF_CHROMIUM_NO_SANDBOX=1
  ./af start` で再測し、README-native の割り切り（localhost 限定）を確定させる。

**報告様式**（この 4 点で十分）: ①環境（`wsl.exe --version` / ディストロ / 新規 or 既存）
②表 1〜7 の ✓/✗ ③手順 5 の生出力（成功でも失敗でも）④気付き（DL 所要・初回起動時間等）。

**最終結果（ユーザー実機・2026-07-22・v0.1.1 / rootfs `4677f9f5a67d`）= ゲート k 全手順クリア**:

- **手順 1〜6 ✓**（別セッション stpovm6 で実施）。素の WSL2（Windows 11 + WSL2 Ubuntu）で
  ワンライナー導入 → `af start` の preflight 素通り → Console 表示 → clone → claude セッション
  E2E → 再起動オフライン起動まで通し確認。**この過程で Console から起動した claude セッションの
  入力全断バグを発見・修正**（`gh pr view` → `git credential fill` が pty を占有し claude の
  入力を横取り。GitHub 未接続＝Bitbucket 専用環境で常時踏む。修正 = `GIT_TERMINAL_PROMPT=0` を
  image ENV に追加。commit `ecd9c9f`・§35.9-10 参照）。
- **手順 7（任意・systemd user unit 常駐化）✓**（2026-07-22）。`systemctl --user status
  agent-fleet` = **`Active: active (running); enabled`**、Main PID は
  `~/.local/opt/agent-fleet/0.1.1/bin/af-cp`。
  - **填まりどころ**: README-native のユニット例 `ExecStart` が汎用想定パス
    `%h/agent-fleet/af start` だったが、ワンライナー導入では `af` は
    `~/.local/opt/agent-fleet/<v>/` 展開＋`~/.local/bin/af` symlink なので `~/agent-fleet/af`
    は**存在しない**。正しくは `%h/.local/bin/af start`。この填まりどころ修正と、dist repo
    README（en/ja）への systemd user 登録手順の追記を本コミットで実施（tar 手展開時の
    読み替え・linger・port 8099 二重起動注意・bus 未接続時の `[boot] systemd=true` も併記）。

### 35.8.2 実 publish runbook（ゲート j・ユーザー実施）

初回セットアップ（1 回だけ）:

```bash
# 1) dist repo 新設（--seed が無ければ作るが、権限を絞るなら手動作成を推奨）
gh repo create k-k1/agent-fleet-dist --public \
  --description "Agent Fleet — distribution artifacts (no source here)"
# 2) fine-grained PAT を作成: 対象 = k-k1/agent-fleet-dist のみ・権限 = Contents RW
#    https://github.com/settings/personal-access-tokens/new
# 3) private 側（このリポジトリ）へ secret 登録
gh secret set DIST_PUBLISH_TOKEN   # プロンプトに PAT を貼る
```

**GHCR の初回 push だけの追加手順（[ADR 0037](../decisions/0037-registry-policy.ja.md)）**:
コンテナパッケージは**初回 publish 時に private で作られる**（GitHub の仕様。repo が
public でも自動では public にならない）。private のままだと利用者の
`docker compose pull` が `unauthorized` で落ちる＝**compose 版の導入手順が丸ごと
機能しない**ので、初回 push の直後に 2 パッケージとも public へ切り替える:

1. `https://github.com/users/k-k1/packages/container/agent-fleet%2Fcontrol-plane/settings`
   （`…%2Fworkspace` も同様）を開く。パッケージ一覧は repo の Packages からも辿れる。
2. Danger Zone → **Change visibility** → Public（パッケージ名の入力で確認）。
3. 併せて **Inherit access from repository** を有効にしておく（repo の権限を引き継ぐ）。

注意点:
- **public → private へは戻せない**（GitHub の警告どおり）。逆に public パッケージは
  ストレージ・転送とも無料なので、public repo ではこれが既定の姿。
- 切り替えは**パッケージごとに 1 回だけ**。以降の版 push は可視性を引き継ぐので、
  この手順が要るのは初回だけ。
- パッケージと repo の紐付けは両 Dockerfile の
  `org.opencontainers.image.source` ラベルが担う（無いと repo に紐付かない孤児パッケージに
  なり、Packages 一覧にも README にも出ない）。
- **repo が private のうちに push すると、パッケージも private で作られる。**
  public 化より先に publish しないこと（順序については §35.9-12）。

**可視性の実測（docker 不要・2026-08-02 に挙動確認済み）**: GHCR の匿名トークン
エンドポイントは、**public パッケージなら 200＋`token`**、private / 不存在なら
**401 または 403** を返す。既知の public パッケージ（`actions/actions-runner`・
`astral-sh/uv`）で 200 を、まだ push していない自分のパッケージで 403 を確認済み。

```bash
pkg=k-k1/agent-fleet/control-plane   # …/workspace も同様
curl -sS -o /dev/null -w '%{http_code}\n' \
  "https://ghcr.io/token?scope=repository:$pkg:pull&service=ghcr.io"
# 200 = 匿名 pull 可（= public 化できている） / 401・403 = private か未 push
```

docker のあるホストなら実 pull で確かめるほうが確実:

```bash
docker logout ghcr.io
docker manifest inspect ghcr.io/k-k1/agent-fleet/control-plane:<v> >/dev/null && echo public
```

リリース（毎回）:

```bash
# 0) リリースノートを書く（publish の前提。無いと publish は render 時に fail する）
#    deploy/release/notes/<v>.md（英語＝正）と <v>.ja.md。書き方は notes/README.md
VERSION=0.3.0 ROOTFS=<既存の r でよい> deploy/release/notes-body.sh   # 本文プレビュー
# 1) 台帳へ 1 行追加（版 / 公開日 / ビルド元 commit）→ CHANGELOG 再生成 → commit
$EDITOR deploy/release/notes/index.tsv
deploy/release/gen-changelog.sh
# 2) publish（--seed で CHANGELOG も dist repo へ push される）
#    CI 経路（推奨）: Actions → publish-dist → Run workflow（version=0.3.0 等）
gh workflow run publish-dist.yml -f version=0.3.0
#    ローカル経路（docker のあるホスト。<r> 不変リリースはこちら）:
VERSION=0.3.0 deploy/release/build.sh --all [--rootfs-json <既存 rootfs.json>]
VERSION=0.3.0 deploy/release/publish-dist.sh --seed
# 3) ビルド元 commit にタグを打って push（版→commit の対応を git 側にも残す）
git tag -a v0.3.0 <build commit> -m "agent-fleet 0.3.0" && git push origin v0.3.0
```

リリースノートの扱い（2026-07-25 整備）: 本文は `deploy/release/notes/<v>.md`
（＋`.ja.md`）を正とし、`notes-body.sh` が「英語 → 日本語 → 成果物 footer」の順に
組み立てる。footer（asset 名・`rootfs-<r>`・install 一行）は `<r>` がビルド時にしか
確定しないため**ノート側には書かない**。publish はノート未整備を hard error にする
（dist-stub-test の case 10/11 で固定）。release-gate の dist-gate が
`gen-changelog.sh --check` と「台帳の全版にノートがある」ことを検査する。
**アセットは不変だがノート本文はメタデータなので後から差し替え可能**
（`gh release edit v<v> --notes-file -`）。0.1.0〜0.2.3 のノートはこの経路で後追い
整備済み。

検証（publish のたび）: 任意の Linux で
`curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash`
→ `af start` が rootfs を実 URL から DL して起動するところまで（ゲート k の手順 1–2 と同じ）。

注意: app リリース `v<v>` は不変（同 tag への再 publish は fail する仕様）。やり直す
時は版を上げる。rootfs tag `rootfs-<r>` は内容ハッシュなので衝突＝同一物・自動再利用。
また `workflow_dispatch` は **publish-dist.yml が default branch（develop）に存在する
ことが前提**（無いと `gh workflow run` が 404。実行 ref に在るだけでは不可 — ゲート j
実走で確認）。

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

**決定（2026-07-23・ユーザー判断）— dist README に AWS 構成を露出しない**:

11. dist repo の `README.md` / `README.ja.md` から「AWS への導入（ECS / CloudFormation）」
    セクションと版選択表の AWS 行を削除し、**EC2-Single / ECS 構成は公開 README に
    露出しない**。配布物は不変（compose バンドルの `aws/` 一式・`aws/ecs/README.md` は
    同梱のまま、§35.2 / §35.3 のマトリクス・設計も変更なし）＝**公開面の導線を出さない
    だけ**で、バンドルを手にした利用者は従来どおり `aws/` から辿れる。

**決定（2026-08-02）— public 化 → GHCR 初回 push → publish の順を守る**:

12. ADR 0037 で compose 版の導入は GHCR からの pull になったが、**GHCR パッケージは
    初回 push 時に private で作られる**（§35.8.2）。したがって順序に依存関係がある:
    - **① repo を public にする** → ② publish-dist を実走して GHCR へ初回 push →
      ③ 2 パッケージを public へ切り替え → ④ 匿名 pull を実測 → ⑤ リリース公開。
    - private repo のまま publish すると、リリースは出るのに `docker compose pull` が
      `unauthorized` で落ちる＝**0.6.0 の導入手順が全滅する**。1 つ前の版に images tar
      という逃げ道があった 0.5.1 と違い、0.6.0 には代替経路が無い。
    - public 化の前に publish したい場合は、**この版に限り `release.sh --save` の
      images tar を手動でリリースに添付する**のが唯一の回避策になる（ADR 0037 の廃止
      判断を 1 版だけ差し戻すことになるので、素直に順序を守るほうがよい）。

**残る確認事項（実装フェーズ内で消化）**:

6. **native の arm64 rootfs**: 需要待ち。dist repo に source を置かない決定（§35.4.2）に
   より公開 repo の無料 arm ランナーは使えない — 出す時は private repo の arm ランナー
   （$0.005/分）か arm 実機でビルド。
7. **オンデマンド供給元の実装確認**（P2→P4）: (a) chromium の DL 供給元最終選定 →
   **P4 ゲート実走で確定（2026-07-21）**。経緯と真相:
   - P1 の「この WS からは PRSS CDN が 400 → 実機/別回線で確認」は**誤診**だった。
     PRSS は不存在オブジェクトに（404 でなく）400 を返す仕様で、実在するアセット
     （arm64 zip）はこの WS からも 200 で引ける。回線は最初から問題ではない。
   - 真因: **playwright 1.61 は linux-x64 の chromium を `builds/chromium/<build>/`
     から Chrome for Testing（CfT）配布へ移行済み**（playwright-core 1.61.0 の
     DOWNLOAD_PATHS を実物で確認 — x64 は `cftUrl("linux64/chrome-linux64.zip")`、
     旧レイアウトに残るのは arm64 のみ）。つまり P2 が凍結した 3 ホスト×
     `chromium-linux.zip` は **amd64 では実体が存在せず**（バケット直 404・PRSS 400）、
     install-chromium は全環境で失敗する状態だった。CI に出した実 DL ゲートの
     初回失敗がこれを検出した。
   - 修正（P4）: amd64 は **`chromium_cft` ピン（browser version = 149.0.7827.55）**
     新設で CfT 2 経路（`cdn.playwright.dev/builds/cft/…` と Google 公式
     `storage.googleapis.com/chrome-for-testing-public/…` — 真に独立な 2 オリジン）、
     zip レイアウトは `chrome-linux64/chrome`。arm64 は従来の `chromium_dl`（build
     番号）のまま dbazure 2 経路＋バケット直を fallback に追加（PRSS 200 実証済み）。
     dist-gate が毎回「フル DL + レイアウト + 2 経路同一物」を実測する。
   - なお dbazure 系入口（cdn.playwright.dev/dbazure・azureedge）は全て PRSS へ
     リダイレクト収束する = 冗長化になっていなかった（azureedge は候補から削除）。
   (b) bwrap 配下での chromium sandbox 実測（namespace sandbox が通るか。不可なら
   `--no-sandbox` + localhost 限定の割り切りを README 化）→ **§35.8.1 チェックリスト
   手順 5 に凍結**（ユーザー実施）。(c) awscli の versioned zip / SMP の versioned deb
   URL と root なし展開（`dpkg-deb -x` 相当）の確認。
8. **ライセンス見解の確度（配布開始前ゲート）** → **決定（2026-07-22・ユーザー判断）=
   配布物は恒久的に lean のみ。proprietary CLI（claude / agy / copilot）の焼き込み配布は
   将来も採らない**（「規約が許せば焼き込みへ戻す」分岐を放棄）。よって §35.4.1 の帰結が
   単純化し、ライセンス表の保守を配布可否判断に依存させない。
   - **裏取り済み（claude 本文確認）**: 一次調査（npm/GitHub の license ラベル）に加え、
     `anthropics/claude-code` の LICENSE.md 本文＝「© Anthropic PBC. All rights reserved.
     Use is subject to Anthropic's Commercial Terms of Service.」を確認 = **再配布/同梱の
     許諾なしの proprietary で確定**。2026-03-31 にソースが GitHub で可視化された件は
     「source-available ≠ 再配布可」で結論に影響なし。agy / copilot も同様の proprietary。
   - **lean が「当方の再配布に当たらない」根拠の傍証**: 同型 OSS（`openclaw/openclaw`・MIT）も
     proprietary CLI/モデルを同梱せず、ユーザー自身の鍵・OAuth で provider を直叩きする
     BYO 方式。agent-fleet の lean（各デプロイ先が公式配布元から起動時 boot-install）は
     経路こそ違え「proprietary バイナリを配布物に載せない」点で同じ定石。
   - **残**: agy / copilot の規約"本文"精査は lean 確定により moot（焼かない以上、再配布条項の
     可否は配布可否に影響しない）。全焼き込みの自社内イメージは従来どおり（頒布しないので不問）。
9. **「初回起動で boot-install の DL ログが出ず即起動」疑いの切り分け（2026-07-21・ゲート k
   中のユーザー実機報告 rootfs `4677f9f5a67d`／v0.1.1）**: 「rootfs に CLI が焼き込まれ、
   ピン版オンデマンド導入をバイパスしているのでは」という疑いを別セッションで調査 →
   **結論: 逸脱なし。rootfs は設計どおり lean。真因は「初回 boot-install の DL が実行された
   が、その出力が agent.log にしか出ず起動 UI のどこにも surface されなかった」観測性の穴。**
   （※初出の記録にあった「2 回目以降だから無音スキップ」という説明は**誤り**。ユーザー
   確認で本件は**新規ワークスペースの真の初回起動**であり再起動ではない。chromium が
   `~/.local/share/agent-fleet/chromium/…` に在った件も、プレビュー（ペイン初回 attach）で
   オンデマンド DL された正常結果と確認済み＝焼込みの証拠ではない。両説明を撤回する。）
   - **実 rootfs を直接検証**（公開 dist の R zstd 258MB を python `zstandard` で stream 展開し
     tar エントリ列挙）: `usr/local/bin` に claude / opencode / codex / copilot / agy / rtk は
     **一切存在せず**、baked agent npm も無し。さらに `usr/bin/chromium`・`usr/lib/chromium`・
     baked Go・aws・SMP・ops MCP・CJK フォントも**全て無し**。`versions.json` は全ピン記載＝
     **lean 確定**（build.sh `--native` の `BAKE_AGENT_CLIS=0`＋`BAKE_OPTIONAL_TOOLS=0` どおり。
     Dockerfile の各 RUN は `BAKE_*=1` ガード下で 0 では焼かれない）。
   - **初回起動で boot-install が「実際に走ったはず」を code＋rootfs で立証（skip バグの否定）**:
     entrypoint は rootfs モードでも bwrap 配下で `/usr/local/bin/entrypoint.sh workspace-agent`
     として起動し（`runtime_native.go`）、`exec workspace-agent` は entrypoint 末尾＝boot-install
     の**後**。lean 判定 `LEAN_CLIS`（`/usr/local/bin/claude` 不在 かつ `vj_pin claude` 非空）は
     初回で**必ず 1**になる: (i) lean なので `/usr/local/bin/claude` は無い、(ii) `vj_pin` は
     `node` と `versions.json` を使うが、両者は rootfs の **`/usr/local` 側**に実在
     （`/usr/local/bin/node`・`/usr/local/share/agent-fleet/versions.json` を rootfs 直接検証で
     確認）で、実行時に上書きされる `home/dev` bind-mount の**外**＝隠れない（rootfs の
     `home/dev` 配下は skel 3 ファイルのみ）。よって初回は `LEAN_CLIS=1`＋`~/.local/bin` 空
     ＝6 CLI 全て `cli_present=false` → **必ず DL が走る**。presence-check の false-positive で
     無音スキップした、という仮説は成立しない。
   - **したがって残るのは可視性のみ**: entrypoint 出力は CP が `<dataDir>/agent.log` に**だけ**
     書き（`cmd.Stdout/Stderr = agent.log`）、`af start` のターミナルにも Console の起動 UI にも
     出ない。加えて boot-install は `exec` 前に同期実行されるので、healthWait（rootfs 既定
     300s）の間ずっと**無表示のまま待つ**画になり、回線が速いと「即起動」に見える。**DL は
     起きていたが誰にも見えなかった**、が真因。確証は実機の初回 agent.log（下記コマンド）。
   - **対処（この調査で実施）**:
     (a) **CP が初回 boot-install の進捗を surface**（`control-plane/runtime_native.go`
     `mirrorBootProgress`）: rootfs モードの Start で healthWait 中に agent.log を tail し、
     `[entrypoint] …` 行（boot-install / install-go / install-jdk / claude repair 等）を CP ログ
     （＝`af start` ターミナル）へ転写。加えて開始時に「初回はピン版 CLI を ~/.local に導入する
     ため数分かかる・進捗は agent.log」の一行を出す。read-only・best-effort、healthy になれば
     即停止。これで「無表示の長い待ち→焼込みと誤認」を根絶。
     (b) `workspace/entrypoint.sh` の lean boot-install に**明示ログ**（`lean variant: ensuring …`
     ヘッダ＋npm/rtk/agy の「already present (skip)」）。初回は DL 行、再起動時はスキップ行が
     agent.log に必ず残る。(c) 本節と §35.7.2-8 を「初回 DL は起きる／可視性が穴だった」に更新。
   - **実機 agent.log で確定（2026-07-21・ユーザー実行）**: 初回 `~/.local/share/agent-fleet/dev/
     agent.log` に `[entrypoint] boot-install (pinned): @anthropic-ai/claude-code@2.1.215
     opencode-ai@1.18.3 @openai/codex@0.144.6 @github/copilot@1.0.73 …` → `boot-install ok` →
     `boot-install agy 1.1.5` が実在。**DL は初回に実行されていた＝観測性の穴で確定**（焼込み
     でも skip バグでもない）。可視性のみが問題という結論を実機ログが裏付けた。
   - **同ログで判明した二次不具合（初回ブート時の一過性 DL 失敗・いずれも自己修復）**:
     (i) `WARN: rtk boot-install failed`＋直前に `sha256sum: WARNING: 1 listed file could not be
     read`＝rtk アセット取得が途中で切れ検証対象を読めず失敗。(ii) `[install-chromium] WARN:
     CJK font download failed … exit status 56`＝noto_cjk（raw.githubusercontent の大きめ .ttc）の
     curl 受信エラー。**どちらも URL/フォーマットは正常**（別ホストから rtk 全手順 sha256 OK・
     CJK URL も 206 到達）で、その初回ブートの一過性ネットワーク失敗。設計上 WARN 継続＋
     「次回起動で再試行」で自己修復するが、gate-k の体験（rtk フック欠落・ペインの CJK グリフ
     欠落）を損ねる。**対処: boot-install/オンデマンド DL の curl に `--retry 3 --retry-delay 2
     --retry-connrefused` を追加**（entrypoint の rtk 2 本＋agy、`install_tools.go` の 6 本＝
     chromium/CJK/Go/awscli/SMP）。単一起動内で一過性ブリップを自己修復し、Stop→Start を待たず
     に済む。
   - **起動中ダイアログ＋停止中ポーリング停止（実装済み）**:
     - **CP**: `GET /api/workspace` に `bootPhase` を追加（`runtime_native.go` `BootPhase()`）。
       `mirrorBootProgress` が最新の `[entrypoint]` 行を `<dataDir>/.boot-phase` に atomic
       書き込み → boot 終了（healthy=mirror 停止）で defer 削除。**native の `State()` は
       pid 生存で即 "running" を返す**（healthy 非依存）ので状態だけでは起動中を判定できず、
       bootPhase が真の信号。type assertion で載せるので native のみ・非空＝起動中。
     - **Console**: `WsStartingDialog`（App 直下）。workspace ストアの `start()` は起動中
       state を触らず bootPhase のみ2秒ポーリング（状態 refresh だと boot 途中で閉じる）。
       `wsPreparing(state,bootPhase)` の間モーダル表示・raw フェーズを friendly i18n へマップ
       ＋生フェーズ併記・Esc/背景/×で閉じられる（起動はサーバ側継続）。ja/en キー追加。
     - **停止中の無駄ポーリング停止**: `wsRunning()` を追加し、エージェント proxy 系ポーラー
       （`startSessionsPolling` 4s・`startReposPolling` 60s）を running ゲート＝停止/起動中は
       502 しか返さないので発火停止（[[ws-boot-view-stuck-retry]] の running ゲートをポーラー
       本体へ適用）。CP側で 502 しない workspace/stats・memos・notifications は据え置き
       （stopped でも OOM/終了理由・通知を運ぶため）。
     - typecheck/build/vitest(stores)/i18n:lint 緑。実描画はゲート k 再ビルド後の目視待ち。
       さらに本格的な可視化（agent.log 全文の tail surface）は必要になれば別タスク。

10. **claude セッションが入力を一切受け付けない（2026-07-21〜22・ゲート k 中のユーザー実機報告
    rootfs `4677f9f5a67d`／v0.1.1）→ 真因特定・実機検証済み・恒久修正**:
    - **症状**: 実リポジトリで管理下 claude セッションを起動すると、welcome TUI は正しく描画され、
      起動直後の Trust 確認は Enter が通る。しかし**メインプロンプトに到達すると一切の入力が通らない**
      （Console のミラー＝「反映待ち」のまま／ターミナル直打ち／raw `tmux send-keys` も不可）。
      **Console フレッシュ新規でも 100% 再現**。Docker/ECS は同じランチャで正常。
    - **切り分け（ミラー/配線/コマンドを全棄却）**: claude は正しいペインに生存（node・State S・
      fd/tty/epoll 武装とも正常 claude と一致＝正しく入力待ち）、端末フォアグラウンドも claude
      （`tpgid==pgrp`）。send-keys 着弾（`PINGME`）判定で、素 `new-session -d 'claude …'`／実 socket
      `af-ws-dev`／`--session-id`+`--name`／`export` prefix／末尾コマンド（fork/exec 差）／`pipe-pane`／
      **実起動コマンドの verbatim 再現**／attach 済み手動／detached-bash（send-keys 機構は健全）は
      **全て REACHED（＝無罪）**。env（`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` 等）も claude の
      `/proc/pid/environ` に届いていたが入力 LOST のまま＝**env は真因でない（当初の結論は誤帰属）**。
      **唯一 LOST は workspace-agent が実起動した管理下セッションだけ**＝実リポで `gh pr view` が発火する
      条件だけが差。
    - **★真因（WSL ローカル claude が strace/proc で特定）**: claude が起動時に **PR ステータス取得の
      `gh pr view`** を実行 → gh 未認証＋この repo は github.com HTTPS リモート → 認証情報を探して
      **`git credential fill` が `/dev/tty`（＝tmux ペインの pty）を open し、ユーザ名/パスワードの
      `read()` でブロック**。結果、claude と credential-fill が**同一 pty の 2 リーダー**になり、
      **send-keys のバイトが認証プロンプト側に食われて claude の `epoll(fd0)` に届かない**。因果確定：
      credential パイプラインを kill する**前は LOST・kill 直後に同一 send-keys が即 REACHED**（他不変）。
    - **★スコープ＝native 固有でなくフリート全体の潜在バグ**: `workspace/agent/cred_helper.go` が
      global credential.helper `!workspace-agent cred` を仕込むが、**GitHub 未接続だと github.com の
      トークンを持たず空を返す**→git が対話プロンプトにフォールバック。`GIT_TERMINAL_PROMPT=0` は
      `internal/gitx` 等で **agent 自身の git にだけ per-command** 付与され、**claude が spawn する
      `gh pr view`→git 子プロセスには効いていない**。cred ヘルパーも同じく Docker/ECS 共通なので、
      **GitHub 未接続ユーザー（例：Bitbucket 専用）は Docker/ECS でも github.com リポで同様に入力全断**
      する。フリートで表面化していないのは大半が GitHub 接続済みだから。
    - **★修正（実機検証済み）**: `workspace/Dockerfile` に **`ENV GIT_TERMINAL_PROMPT=0` と
      `GCM_INTERACTIVE=never`** を追加（image ENV＝native rootfs `image-env.json`/Docker/ECS 全てに効く）。
      git を対話プロンプトに落とさせず即失敗させる＝credential-fill が pty を掴まない。**設定モーダルの
      GitHub 認証は無傷**：資格情報は Console キャプチャ（OAuth device flow／PAT 貼付→暗号化ストア）で、
      git の端末プロンプトは認証フローに一切使わない（cred ヘルパーは `get` のみ実装・`store`/`erase`
      は no-op）。cred ヘルパーが先に走り、空のときだけこの防御が効く純粋な追加。**現行 rootfs の
      image-env.json に手動追記→`af start` やり直し→Console 新規 claude で `hi` 一往復成功を実機確認**
      （2026-07-22）。次の rootfs 再ビルド/publish で恒久反映。
    - **⚠️ 訂正**: 当初「起動時の非必須ネットワーク呼び出しのハング＝env 無効化で解決」と誤結論し、
      commit **798135f** で `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`/`DISABLE_TELEMETRY`/
      `DISABLE_ERROR_REPORTING` を追加した（本節の旧版もその記述だった）。env は claude に届いていたが
      入力 LOST のまま＝**入力の真因でない**（`gh pr view` はテレメトリでないので DISABLE 系では止まらない）。
      当該3キーは制限ネット向け無害ハードニングとして**残置**（コメントを訂正）。真の修正は上記
      `GIT_TERMINAL_PROMPT=0`。
    - **副次フォローアップ（別件）**: native で gh 未認証＝gh 透過認証（GH_TOKEN 注入）が native ランタイム
      で効いていない。`GIT_TERMINAL_PROMPT=0` は「ブロックせず即失敗」にするだけで PR ステータス自体は
      表示されない。native の gh 透過認証配線が別途要る（ただし未接続ユーザーは常に存在しうるので
      `GIT_TERMINAL_PROMPT=0` の防御は接続有無に関わらず必須）。TUI フッターの `tmux focus-events off`
      ヒントは通知/状態検出用で入力主因ではない。

## 35.10 v0.1.1 公開後の機能差分（次リリースの README／リリースノート素材・2026-07-23 整理）

範囲: **v0.1.1（merge `098f9f4`・2026-07-21 publish）→ develop `7b66ed1`（2026-07-23）**。
この間に **v0.1.2 を publish 済み**（2026-07-22・rootfs `1aadff3b24b7`・下記「修正」の
claude 入力全断/curl --retry を反映した hotfix）。ただし **dist repo の README seed は
0.1.1 時点のまま**（公開 README に「Scheduled execution」「Automatic updates」等は未掲載
— 2026-07-23 に raw.githubusercontent の実物で確認）＝本節の README 反映分は次の publish
`--seed` でまとめて公開される。

### 新機能（設計 docs あり・README 反映済み）

1. **チャットブリッジ（Discord / Slack）** — [docs/37](37-chat-bridge.md)+ADR0020。
   セッション毎スレッド通知（応答あり/質問・プラン承認/許可リクエスト/異常終了/完了報告の
   5 種・個別トグル）、全文モード（opt-in・秘密自動伏字化・長文分割）、スレッド返信での
   操縦（全 kind・TUI/managed 両対応・opt-in）、質問/プラン承認/許可のボタン回答、
   @メンション→フリート・オペレーター会話（Console と同一会話）、チャット駆動の破壊的
   操作に承認ゲート（fail-closed）。接続はトークン貼付のウィザード（Discord=Bot トークン
   1 本・Slack=xoxb+xapp の Socket Mode）。**Slack も全機能パリティ実装済み**（実機通しのみ残）。
   Console は「接続 › チャット連携」タブ＋「個人設定 › 通知」のサービス毎マスタ ON/OFF。
2. **定時実行** — [docs/38](38-scheduled-execution.md)+ADR0021。オペレーターへの自然言語
   依頼で登録（cron / interval / once・TZ/DST 対応・自前評価器）、停止中 WS を wake して
   実行（完了後は settle 猶予後に元へ戻す）、失敗（wake/注入/quota/membership）は CP 通知
   センターへ surface。左レール「スケジュール」セクション（一覧・実行履歴 50 件・一時停止/
   再開/今すぐ発火/削除。登録・編集は会話側のみ）。**スケジューラ既定 ON**（opt-out=
   `AF_SCHEDULER_INTERVAL=0`）。P6=長寿命セッション再利用（`session_mode=reuse`・ピン留め/
   管理・every_runs/after/calendar ローテーション）。
3. **Cursor CLI（6 種目のエージェント kind）** — [docs/40](40-cursor-agent-kind.md)+ADR0023。
   v1 は login-only（ブラウザ承認・コード貼付なし）、TUI＋managed（`cursor-agent acp`・既定
   managed）、アカウント連動のライブモデルカタログ、版ピン焼込＋公式 auto-update 2 経路封殺。
   使用量チップ・API キー登録・rtk フック等は Track D（未実装）。
4. **SVN チェックアウト対応** — [docs/41](41-svn-checkout.md)+ADR0024。URL＋基本認証
   （stdin 渡し・auth-cache 無効）、サブパス/複数 path チェックアウト、資格情報の暗号
   ストア保存（opt-in・最長プレフィックス一致で再利用）、自己署名証明書のサーバ単位信頼
   （opt-in）、working-copy ロックの自己修復（cleanup+retry＋明示「ロックを解除」）。
   worktree 非対応＝その場起動のみ（並行作業は別 path 別フォルダで隔離）。
5. **ホスト常駐 af の自動更新（native）** — [docs/42](42-native-auto-update.md)+ADR0025。
   stage 自動（`af update`・日次 systemd user timer 既定 ON・sha256 検証）／apply 明示
   （Console「設定 › 環境」の「再起動して適用」or 手動 restart・実行中セッションを不意に
   切らない）。`AF_VERSION` ピンで停止可。README の native 節へ反映済み。

### 改善（README 非掲載の粒度）

- **copilot Track D**（[docs/36](36-copilot-agent-kind.md)）: モデル選択（動的カタログ）・
  WS バー使用量チップ＋プラン表示（copilot_internal/user API）・rtk 決定的フック。
- **エージェント表示の統一**: 3 段命名（short/label/displayName・registry が正）＋kind 色の
  `tokens.css --kind-*` 1 ソース化。
- **設定モーダル IA 再編**: 10 タブ→3 グループ左レール（接続/動作階層・確認/導線統一）。
  「チャット連携」タブ独立＋個人設定「通知」タブ（音声＋サービス通知マスタ）。
- **Console↔CP 通信量削減**: gzip/immutable・ETag 304・WS deflate・SSE 統合 push
  （`/api/events` 1 本化、ポーラーはフォールバック）。実フリート実測済み。
- **ミラー UX 堅牢化**: スクロール追従の正直更新＋「最新へ」ボタン、初期位置の最下部安定、
  返信描画前の空白（finalizing ブリッジ）解消、プラン却下が承認になる不具合修正、
  スラッシュコマンド送信後の「反映待ち」解消、画像ライトボックスの「戻る」対応。
- **キーボード**: 通知トグル群を `n` グループへ再編（音声/Slack/Discord/制限リセット、
  状態トースト付き）。
- **SSM/shell**: 接続名=別名＋日時、接続モーダルのクイック接続カード（頻度＋色ドット）、
  shell・ssm セッションでのリーダーキー透過（Ctrl+K/P 素通し・既定 OFF）。
- **WS 起動中ダイアログ**: native boot-install の進捗フェーズを `GET /api/workspace` に公開し
  起動中モーダルで表示（§35.9-9 の観測性の穴の恒久対処）＋停止中 WS への無駄ポーリング停止。
- **起動モーダル**: スマホからの画像添付＋ピッカー。掃除後の左ペイン即時反映。

### 修正（配布物に効くもの）

- **claude 入力全断の恒久修正** `GIT_TERMINAL_PROMPT=0`/`GCM_INTERACTIVE=never`（§35.9-10。
  **v0.1.2 の rootfs で配布済み**）。
- **boot-install/オンデマンド DL の一過性失敗を curl --retry で自己修復**（§35.9-9。
  同じく v0.1.2 で配布済み）。
- **Remote Control 既定 OFF**（既存 WS にも一度だけ適用）。
- **install-compose.sh 新設**（compose 版の取得ヘルパー・README 反映済み・§35.3.2）。

### README 側の変更（本差分で実施・2026-07-23）

- 「5 種→**6 種のエージェント CLI**」（Cursor 追加）、git 並行セッション節へ **SVN 対応**を
  追記、**チャットブリッジ（Discord / Slack）** bullet を新設（en/ja 両方）。
- **AWS（ECS / CloudFormation・EC2-Single）の露出を削除**（§35.9-11 の決定。版選択表の
  AWS 行＋「AWS への導入」セクションを削除。配布物の `aws/` 同梱は不変）。
- 定時実行・native 自動更新の bullet は 0.1.1 publish 後に seed へ追記済み＝次リリースで
  初公開となる（本節の範囲に含まれる）。

> 本節の内容は 0.2.0 のリリースノート（`deploy/release/notes/0.2.0.md` / `.ja.md`）へ
> 反映済み。「v0.1.2 で配布済み」と注記された修正は 0.1.2 のノート側へ分けてある。

## 35.11 リリース台帳とノート（2026-07-25 整備）

公開済みの版・公開日・**ビルド元 commit** は `deploy/release/notes/index.tsv`。
各版の利用者向けノートは同ディレクトリの `<v>.md`（英語＝正）と `<v>.ja.md`。運用手順は
`deploy/release/notes/README.md`、publish 手順への組み込みは §35.8.2。

**なぜ台帳が必要だったか（再発防止）。** 0.1.0〜0.2.3 は publish 時にタグを打っておらず、
版 → commit の対応が git 上に一切残っていなかった。復元は
`gh run list --workflow publish-dist.yml --json headSha,createdAt` の `head_sha` を
リリース作成時刻順に突き合わせて行った（全 7 版が develop の祖先で時系列順に並ぶことを
`git merge-base --is-ancestor` で確認）。この経路は publish が workflow_dispatch である
ことに依存しており、ログ保持期限を過ぎれば失われる。以後は **publish のたびに
`v<version>` タグを打ち、台帳へ 1 行追加する**（§35.8.2 の手順 1・3）。7 版分のタグは
遡って作成済み。

| 版 | 公開日 | ビルド元 commit | rootfs |
|---|---|---|---|
| 0.1.0 | 2026-07-21 | `f818877e` | `fc943ac06dfa` |
| 0.1.1 | 2026-07-21 | `098f9f46` | `4677f9f5a67d` |
| 0.1.2 | 2026-07-21 | `33fb9b80` | `1aadff3b24b7` |
| 0.2.0 | 2026-07-23 | `a35bd3fd` | `eaa7b0d414de` |
| 0.2.1 | 2026-07-23 | `44e10f74` | `f6f45f69b8ad` |
| 0.2.2 | 2026-07-24 | `58a6b25b` | `8a80051d6c2e` |
| 0.2.3 | 2026-07-24 | `f02b29e0` | `0acd1112b7b0` |

公開済み 7 版のノート本文は、この整備で 1 行のテンプレート文から書き下ろしへ差し替えた
（`gh release edit --notes-file`。アセットは不変だがノートはメタデータなので可能）。
