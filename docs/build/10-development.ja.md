---
audience: "はじめてこのリポジトリをビルドする人"
source_of_truth: "コード + CI 定義"
updated: "2026-10"
---

# 10. 開発 — ビルド・反映・テスト・規約

[English](10-development.md) | 日本語

## 10.1 リポジトリ構成（責務のみ）

| ディレクトリ | 責務 |
|--------------|------|
| `console/` | ブラウザ SPA（React + Vite + zustand）。ビルド成果物 `console/dist` を CP が静的配信 |
| `control-plane/` | Control Plane（Go・単独モジュール）。migrations を埋め込み、起動時に自動適用 |
| `workspace/` | Workspace イメージ（Dockerfile / entrypoint / opencode プラグイン / workspace notes）+ `workspace/agent/`（Agent・独立 Go モジュール）|
| `deploy/` | デプロイ層（`local` / `compose` / `aws` / `native`）とリリース用ツール（`release/`）。runbook は各 README（[09](09-deploy.ja.md)）|
| `e2e/` | フリート E2E（独立 Go モジュール・stdlib のみ）。CP + 実コンテナの疎通検証（§10.4）|
| `console-e2e/` | Console UI E2E（Playwright）。ブラウザ → CP → 実コンテナの縦串検証（§10.4）|
| `guide/` ・ `docs/` | 全コンテナに同梱される利用ガイドと、開発者向けドキュメント。両方の規範は [CONVENTIONS](../CONVENTIONS.ja.md) |
| `scripts/` | リポジトリの検査: `docs-check.py`（リンク・front matter・`guide/ref` の表）、`vet-build-tags.sh`、`model-id-lint/`（フォールバック登録簿の外のモデル ID） |

ファイル単位の地図は [90-code-map](90-code-map.ja.md)。

## 10.2 変更を反映するには

**要点: 新イメージが効くのは Stop→Start のときだけ。** `docker run` は稼働中のコンテナには
何もしない。docker ランタイムの Start はコンテナを消してから run し直すので、確実に入れ替わる。
ホーム（ログイン・接続・repos）はそこでは bind mount で、イメージ更新の影響を受けない。
Start が使うイメージと Workspace が走らせているイメージが違うと、Console に **要再起動** の
バッジが出る（`control-plane/workspace_stale.go`）。

| 変更したもの | 反映に必要な操作 |
|--------------|------------------|
| Console（`console/src`）| `npm --prefix console run build`（または `run dev` ＝ `vite build --watch`）→ ブラウザを**リロード**。CP は `console/dist` をディスクから読み、そのキャッシュヘッダ（[05 §5.4](05-api.ja.md#54-横断規約)）によってリロードで新しいビルドが効くので、CP 再起動は不要 |
| CP の Go | CP を再ビルドして再起動（`restart-cp.sh`）。イメージ再ビルド不要 |
| Agent の Go / イメージに入るもの | イメージを再ビルド（`run-dev.sh`）→ 各利用者が Console で **Stop→Start**。CP が稼働中の Workspace を強制的に入れ替えることはない。`native` にはイメージが無く、`run-dev.sh native` が代わりに Agent バイナリを再ビルドする |
| エージェント CLI・`rtk`・`gh`・Go のピン版 | §10.2.1 の runbook に従う |
| entrypoint が適用する類（設定 seed・TZ 等）| Stop→Start のみ（再ビルド不要）|
| 共有 JVM（docker ランタイム）| 共有ディレクトリを消して再 provision（`deploy/local/provision-jvm.sh`。JDK が既にあるディレクトリはスキップする）|

### 10.2.1 ピン版ツールの版上げ runbook（定型運用）

`rtk`・`gh`・Go の版、またはエージェント CLI を手で上げるときはこの手順で進める。エージェント
CLI は通常 §10.2.2 が自動で開く版上げ PR で上がる。下の手順 1〜3 はそれが代わりにやることで、
手順 4 以降はその PR にもそのまま当てはまる。版はすべて
`workspace/Dockerfile` のビルド引数になっている。版未指定の `npm install -g` は Docker
レイヤキャッシュに当たり、**再ビルドしても版が上がらない**ためである。ピンが Workspace に
届く経路（イメージに焼き込む／既定の lean イメージでは版マニフェスト `versions.json` から
起動時に導入する）は [04 §4.9](04-agent.ja.md#49-workspace-イメージと-entrypoint)。

1. **latest を確かめる。** `deploy/local/cli-drift-check.sh [cli]` が、全エージェント CLI と
   `rtk` についてピンと公開 latest を並べて出す（CI と同じ取得元を読む）。gh はその releases
   ページ。Go は `workspace/agent/go.mod` の `go` ディレクティブと歩調を合わせる（go.mod を
   上げないなら据え置く）。
2. **ビルド引数を書き換える**（`workspace/Dockerfile`）。バイナリで配られる CLI（agy・cursor・
   kiro・muse）はアーキテクチャ別の sha256 もピンしている。取り方は各引数の上のコメントにある。
   引数の変更は確実にキャッシュを破るので `--no-cache` は不要。
3. **commit & push** — 小さな diff。メッセージは
   [CONTRIBUTING](../../CONTRIBUTING.md#commits--prs) に従う。
4. **E2E ワークフローの緑を待つ。** `e2e.yml` は **push では走らない**（main への PR・毎晩の
   cron・手動 dispatch のみ）ので、作業ブランチでは自分で起こす:
   `gh workflow run e2e.yml --ref <ブランチ>`。CLI を焼き込んだイメージ
   （`BAKE_AGENT_CLIS=1`）をビルドし、L1（§10.3 に挙げたツールについて**導入された版 ＝ ピン**）→ L2（フリート疎通）→
   L3（Console UI）を検証する。**red のまま先へ進まない。**
5. **（大きめの版上げ）その CLI の contract を回す**（後述）。CLI ごとにワークフローも入力も
   別なので、要るものだけ回す。実ターンを使うものはその CLI のサブスク枠を消費する。`live` 入力は
   どちらも既定 false。`e2e.yml` の `live` は L4（headless の claude ターン）、`codex-contract.yml`
   の `live` は Tier 2（ワークフローの記載で 1 回およそ 45k tokens・実測）。どちらも TUI を
   描かないので、状態検出を見るのは contract ワークフローの仕事で L4 ではない。
6. **ホストに反映**: `run-dev.sh`。ビルド直後にイメージスモーク（L1）が走る。既定の lean
   イメージでは `versions.json` が新しいピンを持ち、CLI が焼かれていないことを確かめる。
   `BAKE_AGENT_CLIS=1` なら導入された版も確かめる。
7. **各 Workspace に反映**: 各利用者が Console で **Stop→Start**。lean イメージでは、自己更新がオフの
   あいだ、メンバー自身の起動で entrypoint が boot-install 対象の CLI を `~/.local` で新しい
   ピンへ進める（ネットワークが要る。失敗は次の起動で再試行）。自己更新がオンなら latest の
   まま（下の補足を参照）。kiro と muse はオンデマンドで導入され、それぞれの導入経路でピンに
   追従する（[04 §4.9](04-agent.ja.md#49-workspace-イメージと-entrypoint)）。home と repos は残る。
8. **（任意）確認**: **設定 → ツールチェーン → ツールのバージョン** で、実効版・イメージ版・
   ピン版が並んで見える。

補足:

- **毎晩の定期実行**（`e2e.yml`、04:00 JST・develop 対象）が、このリポジトリに変更が無くても
  上流 CLI や base image の破壊を検出する。red になったら上流を疑い、手順 4〜5 で切り分ける。
- 再ビルドせず特定メンバーだけ先に進めたいときは、自己更新の opt-in がある。テナント設定で
  許可し、メンバーが 設定 → ツールチェーン で Workspace ごとにオンにする。メンバー自身の起動では latest を
  `~/.local` に入れ、無人の起動（スケジュール実行の wake）では更新を飛ばして導入済みの版を
  保つ。オフにして Stop→Start するとピン版に戻る。

**版上げを知らせる仕組みは生きているか？** 公開版が動いたことに気づくのは
`cli-release-watch.yml` で、contract が最後に通った版は追跡 issue の `tested` 状態である。
`tested` が進まないことは、上流が静かでもジョブが落ちていても同じに見える——2026-09-09 は
後者で、気づいたのは翌朝だった。そこで **設定 → ツールチェーン** のツールのバージョン表の下に
1 行 `上流のリリース監視: 最終成功 <相対時刻>` を出す。警告になるのは、watcher が読めなかった
取得元を名指ししたときと、48 時間クリーンな実行が無いときだけ。CP は issue を匿名で 1 時間に
1 回読んでキャッシュする（`control-plane/cli_release_watch.go`）。GitHub に届かない環境では、
安心させる表示も警告も出さず、行そのものが出ない。**この行が警告しているときは手順 1 だけでは
足りない**——drift の検査は latest が何かを教えるが、contract を dispatch するのは watcher で、
それが止まっている間は何もテストされていない。

### 10.2.2 エージェント CLI の版上げの自動化（`cli-pin-bump.yml`）

`cli-pin-bump.yml` は、新しい版で contract がすでに通ったエージェント CLI のピンをまとめて
上げる PR を、固定ブランチ `automation/cli-pin-bump` から `develop` へ**1 本だけ**保つ。
contract ワークフローが終わったとき、保険として2 時間ごと（奇数時）、手動 dispatch で走る。判断と
書き換えは `deploy/local/cli-pin-bump.sh`、それを固定するのが
`deploy/local/cli-pin-bump-stub-test.sh`。

- **上がる kind。** ピンが公開 latest と違い、**かつ** release-state issue の `tested` が
  その latest とちょうど一致する kind だけ。どの contract も `tested` を記録するのは
  `latest` 実行が通ったときだけで、ピン版の実行では記録しない。claude の contract は
  `claude-tui-contract.yml` で、`workspace/agent/internal/tmuxx/testdata/footers/SOURCE.txt`
  が記すフッター／スピナー検出を実 TUI で確かめる。ドリフトしているのに外した kind は、理由と
  一緒に PR 本文に並ぶ。門番は等値比較で版の大小は比べないので、版上げがマージされる前に次の版が
  出た kind は、新しい latest で contract が通るまで PR から外れる（watcher が同日に dispatch
  する）。上げるものが無くなったときは、開いている版上げ PR を理由付きで閉じる。状態 issue は公開
  なので、数えるのはワークフロー（`github-actions`）か owner・member・collaborator が書いた
  マーカーだけで、しかもワークフローが開いた issue のものに限る。それ以外の人の `tested`
  コメントは無視する。
- **チェックサム**は Dockerfile のコメントが名指しする取得元から取る。agy はアーキ別
  manifest（両アーカイブを落として manifest の sha512 と照合し、sha256 を計算する。release
  build id は manifest の URL から取る）。kiro は stable manifest、muse は版付き release
  manifest で、x86_64 のダウンロードが掲載値と一致しなければならない。cursor は上流が
  チェックサムを出さないので、両 tarball を落としたままハッシュする。不一致、sha256 の形を
  していない値、manifest が別の版に進んでいた場合、その kind はピンのまま。取得元やダウンロードが
  読めなかった場合は別扱いで、その実行はブランチと PR に触らず次の完全な実行を待つ。ネットワークの
  一時的な失敗で kind が PR から落ちたり PR が閉じたりはしない。npm の kind にチェックサムは無い。
- **書き換え**は上げる kind の `ARG` 行だけに触れ、ファイルを置き換える前に 1 行ずつ
  照合する。2 回目の実行は何も変えない。
- **PR** には証拠表が載る — kind ごとの `ピン → latest`、`tested` を記録した contract 実行への
  リンク（`cli-release-state.sh` が実行 URL をマーカーのコメントに書く）、チェックサムの出どころ。
  ブランチの force-push は Dockerfile の書き換えが変わったときだけ。自動ではマージしない。
  レビューして CI を待ち、§10.2.1 の手順 4 から進める。マージ後、他にピンの遅れが無ければ
  `cli-drift.yml` が次の実行で drift issue を閉じる。マージせずに閉じると、その書き換えそのものを
  断ったことになり（本文の id で見分けるので、ブランチを消しても効く）、版が変わるまで再提案しない。

**手作業で残るもの:**

- **cursor と kiro** は資格情報がローテートするので、watcher は contract を dispatch しない。
  secret を更新してから `gh workflow run cursor-contract.yml -f cli_version=latest`
  （または `kiro-contract.yml`）。通れば版上げが続く。
- **claude・copilot・agy** は contract の secret があるときだけ dispatch される。無ければ
  付随 issue のフリート確認を回し、secret が戻ってから contract を dispatch する。
- **rtk** は取得元はあるが contract が無く、`tested` で門番できない。ドリフトとして一覧に
  出るだけで、版上げは手で行う（§10.2.1）。

**トークン（リポジトリ管理者が一度だけ設定する）。** `GITHUB_TOKEN` による push や PR は他の
ワークフローを起動しないので、そのままでは版上げ PR の CI が走らない。そこでジョブは Actions
secret **`CLI_PIN_BUMP_TOKEN`** で push と PR 作成を行い、push するものがあるのに secret が
無ければ、その名前を挙げたエラーで落ちる。**fine-grained personal access token** を作り、
*Repository access* をこのリポジトリだけに絞って、権限は **Contents: Read and write** と
**Pull requests: Read and write** のみにする。同じ 2 権限でこのリポジトリだけに入れた GitHub
App でもよいが、トークンの寿命が 1 時間なので、保存した secret ではなく push の前にトークンを
発行する手順（`actions/create-github-app-token`）が要る。

**Settings → Secrets and variables → Actions → New repository secret** に
`CLI_PIN_BUMP_TOKEN` として保存する。PAT には期限があり、切れると同じメッセージでジョブが
red になる（drift issue はピンの遅れを報告し続ける）。

## 10.3 起動スクリプトの責務（`deploy/local/`）

- **`run-dev.sh`** — **単一のエントリポイント**（サブコマンド式）。Workspace 実行環境の準備
  （共有 JDK・Workspace イメージとそのスモーク）→ Console build → CP build → CP をホスト
  プロセスで起動。git-ignored の `deploy/local/oauth.env` があれば自動で source し
  （雛形は [`oauth.env.example`](../../deploy/local/oauth.env.example)）、認証・暗号系の設定を
  CP に渡す。無ければ dev の素起動。イメージは `BAKE_AGENT_CLIS=1` を付けない限り lean。
  `WS_SMOKE=0` でスモークを飛ばす。

  | サブコマンド | 動き |
  |---|---|
  | （無指定）/ `local` | 開発既定。Docker ランタイム |
  | `wsl` | WSL プリセット（Docker と cgroup の preflight・`AUTH=dev` 固定）|
  | `native` | Docker なしのコンテナレス（単一ユーザー・[ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)）。Agent をホストでビルドして渡す |
  | `reset [--all] [--yes]` | ローカルデータの初期化。既定は dev ユーザーの Workspace だけ（DB と共有 JDK は残す）、`--all` でデータディレクトリ全体。CP 稼働中は拒否し、両ランタイムの残骸を掃除してから消す |

  ⚠️ サブコマンド無しのときは env の `AF_RUNTIME` で決まり、`AF_RUNTIME=wsl` は
  「コンテナレス」の別名である——サブコマンド `wsl`（Docker プリセット）とは別物。紛れるので
  サブコマンドを使う。

- **`restart-cp.sh`** — 軽量な反映: Console と CP だけを再ビルドし、稼働中の CP プロセスを
  その場で入れ替えて `/healthz` を待つ。**Workspace イメージは再ビルドしない。**
  `SKIP_CONSOLE=1` で Go 側のみ。`run-dev.sh` と同じ環境を再現し、`oauth.env` の存在を前提とする。
- **`e2e-smoke.sh`** — イメージスモーク（L1）。ビルド済みイメージに対して `docker run` で
  検証する。焼き込みイメージでは、導入された claude・opencode・codex・copilot・cursor・
  kiro・muse・agy・rtk の版が Dockerfile のピンと一致するか（＝キャッシュが古くないか）を確かめ、
  Go・`gh`・Chromium はどのイメージでも版を突き合わせる。RDRAND の無いホストでは agy を Agent と
  同じ `OPENSSL_ia32cap` マスク付きで問い、rtk は `rtk-unavailable` の印付きで無い場合（arm64）
  も通す。lean イメージでは CLI が焼かれていない
  ことを確かめる。どちらでも `versions.json` をピンと突き合わせ、イメージ自身のファイル
  （Agent・entrypoint・ポリシーの `CLAUDE.md` など）が揃っているかを見る。
  `run-dev.sh` がビルドのたびに実行し、`deploy/local/e2e-smoke.sh [image]` で単体でも回せる。

ホスト固有の作法（PATH・docker グループ等）はホストごとの事情なので、ここには書かない。

## 10.4 テスト

製品は **2 つの Go モジュール** で、それぞれ別に回す（`e2e/` と `deploy/release/scan/` の
リリーススキャナはそれぞれ独立モジュール）:

```bash
(cd control-plane   && go test ./...)
(cd workspace/agent && go test ./...)
```

- CP 側は `httptest` ベースのスモークを多数含む（監査・egress・内部 git の smart-HTTP と LFS
  など）。Postgres のテストは `AF_TEST_DATABASE_URL` が無ければ skip し、CI はこれを設定しない。
- ⚠️ **マイグレーションを足したら、実 Postgres で 1 度回す** — 理由は
  [06 §6.5](06-data.ja.md#65-マイグレーション作法)。下の `-run` パターンは 4 本
  （`TestPostgresStore`・`TestPostgresPasswordRotation`・`TestPostgresDeleteCascade`・
  `TestSchemaDialectParity`）にマッチする。Workspace では `af-db` がインストール・初期化・
  起動を担い、完了の定義は 4 PASS、0 SKIP:

```bash
# Workspace 内（af-db が使える場合）:
(cd control-plane && \
  AF_TEST_DATABASE_URL="$(af-db url)" go test -count=1 \
  -run 'TestPostgres|TestSchemaDialectParity' ./...)
af-db down    # 次の重いビルドの前に止める
```

  `af-db` は scram-sha-256 認証なので `TestPostgresPasswordRotation` も実行される
  （trust 認証のサーバーでは skip する）。`-count=1` はテストキャッシュを無効にする——
  キャッシュの `ok` は何も証明しない。Workspace の外では、自分で立てた使い捨ての Postgres を
  `AF_TEST_DATABASE_URL` で指す。共有ホストでは unix socket にするとポートが衝突しない。
  trust 認証なら 3 PASS・1 SKIP になる。

  テストは `public` に触れない。マイグレーションや行の書き込みをするテストはどれも
  `pgtest.Schema`（`control-plane/internal/pgtest`）から一意な名前の新しいスキーマを受け取り、
  その接続は `search_path` がそのスキーマだけに設定され、テストの終わりに削除される。だから
  重なった実行——1 つのデータベースを共有する 2 セッションや、パッケージを並列に走らせる
  `go test ./...`——が互いのテーブルを消すことはなく、スキーマが残ればテストが落ちる。
  Postgres のテストを足すときは `AF_TEST_DATABASE_URL` を自分で読まずにこのヘルパーを通す。
  URL に `search_path` を含めてはならない。

- **Console**（リポジトリ直下から）:

```bash
npm --prefix console test
npm --prefix console run build
```

  `build` スクリプトは Node ヒープを自分で上げる。テストが `node` と `dom` の 2 プロジェクトに
  分かれていることと、`console/` を作業ディレクトリにして回さなければならない理由は
  [AGENTS.md](../../AGENTS.md#running-the-console-tests)。

- **提出の基準**（`gofmt`・`go vet` が clean、`npm run build` が clean）と、ステージした内容に
  禁止トークンのスキャンをかける pre-commit フックは
  [CONTRIBUTING](../../CONTRIBUTING.md#ground-rules)。**`gofmt` はハードゲート**:
  `go build`・`go vet`・`go test` が通っても、未整形ファイルが 1 つあれば `ci.yml` は落ちる。

- **CI** — `ci.yml` は `main` と `develop` への push と PR のたびに、独立したジョブで回る:

  | ジョブ | 見るもの |
  |---|---|
  | `control-plane`・`workspace-agent` | `gofmt -l`・`go vet`・`go build`（arm64 へのクロスコンパイルも）・`go test`、build tag 付きファイルの `go vet`。Agent 側は `entrypoint.sh` の構文も検査する |
  | `console` | 型検査・lint・i18n lint・モデル ID lint・vitest・実ブラウザの検査 2 つ（`pdf:check`・`doc:check`）・本番ビルド |
  | `deploy-scripts` | デプロイ用スクリプトとリリース監視の判断を、スタブの `aws` / `npm` / `curl` / `gh` に当てて検査。CloudFormation テンプレートが ASCII のみであることも |
  | `secret-scan` | 全履歴に対する資格情報の混入検知（後述）|
  | `release-scan` | 追跡ツリーに対する禁止トークンのゲート——pre-commit フックがステージ内容にかけるのと同じスキャナ |
  | `model-id-lint` | 両 Go モジュールで `workspace/agent/internal/modelfallback` の外にモデル ID の形の文字列リテラルが無いこと。モデルを選ばないものは `// model-id-lint:allow <理由>` で印を付ける |

  PR では、変更したパスがすべて無関係なもの——`docs/` と `guide/`（テストが読む
  `guide/ref/agents{,.ja}.md` と `docs/decisions/0029-usage-accounting{,.ja}.md` を除く）・最上位の `*.md`・docs の検査スクリプト——なら、
  `changes` ジョブが `control-plane`・`workspace-agent`・`deploy-scripts`・`console` を飛ばす。
  一覧と理由は `scripts/ci-changes.sh` にある。`main` / `develop` への push では常に全ジョブが回る。

  `docs.yml` が同じトリガで `scripts/docs-check.py` を回す。E2E ワークフローはイメージの
  build が重いので分けてある。上流 CLI の破壊検知は第 3 の系統（後述）。イメージを焼く
  ワークフロー（たとえば開発配備用の Workspace イメージやエンジンのイメージ）と
  `publish-dist.yml` は dispatch 専用。`release-gate.yml` は dispatch と、packaging ブランチで
  自身を変更する push で走る（[deploy/release/notes](../../deploy/release/notes/README.md) を参照）。

### E2E（イメージスモーク + フリート疎通 + UI + 実 API）

4 層構成。**L1** はイメージスモーク（§10.3・数秒）。**L2** は `e2e/`（独立 Go モジュール・
stdlib のみ）。**L3** は `console-e2e/`（Playwright）。**L4**（`e2e/live_test.go`）は実
クレデンシャルを使い、手動のみ。

- **L2**: CP をヘッドレスで起動し、公開 API だけで Workspace 起動 → **shell セッション**作成 →
  打鍵 → fs API で効果を読み戻し → 停止、を実コンテナで検証する。shell セッションなので
  **LLM クレデンシャルは不要**。
- **L3**: 実ブラウザで Console を開き、セッションを開いて xterm へ打鍵し、効果を fs API で
  観測する（xterm は canvas に描くので DOM から文字が読めないため）。ビルド済みの
  `console/dist` が要り、ブラウザは `E2E_CHROMIUM_PATH` が無ければ `/usr/bin/chromium`。
- **L4**: shell セッション内で `claude -p` を実行し、claude CLI が実際に Anthropic と会話できる
  ことを確かめる。`E2E_ANTHROPIC_API_KEY`（従量課金）か `E2E_CLAUDE_OAUTH_TOKEN`
  （`claude setup-token` のトークン・サブスク枠）があるときだけ動く。**課金かサブスク枠を
  消費するので、自動トリガには載せない。**

```bash
cd e2e && WS_IMAGE=agent-fleet/workspace:dev go test -v -tags e2e -timeout 15m ./...
cd console-e2e && npm ci && npx playwright test
```

- `e2e.yml` は main への PR（関連パス）・develop に対する毎晩の cron・手動 dispatch で走り、
  **push では走らない**。ジョブは `e2e`（L1 → L2）と `ui-e2e`（L3。失敗時は trace と CP ログを
  artifact に残す）が並列、`live-smoke`（L4）は `live` 付きの dispatch のときだけ。イメージの
  ビルドは platform を指定しないので **amd64 のみ**を検証する: arm64 側のアセット（agy・cursor・
  kiro・muse の arch 別 sha256 ピン）はここではビルド検証されない。
- 前提（docker・ビルド済みイメージ、L3 は `console/dist` も）が欠けると skip する。CI は
  `E2E_REQUIRE=1` を立て、それを失敗に格上げする。
- 実フリートが動く開発ホストでも安全: 層ごとに別の開発ユーザー（`e2e`・`e2e-ui`・`e2e-live`）を
  使い、ポートは動的確保、teardown を内蔵している。メモリ制約ホストでは**同時に 1 つ**。

### 上流 CLI の破壊検知（版ドリフト監視 + contract テスト）

**なぜ E2E だけでは足りないか。** `e2e.yml` は版のビルド引数を渡さないので、常に
**ピン版**を検証する。自己更新を opt-in した Workspace は起動時に latest を入れる——
**CI が見ている版と、フリートが走らせる版が別物になる**。加えて L4 は headless で、TUI も
フッタも描かれない。この 2 つの穴のせいで、claude の状態検出
（`workspace/agent/internal/tmuxx`）の破壊は（2026-07-17 時点で）**3 回**、CI 緑のまま
実フリートで人手によって見つかった。

塞ぎ方は補完し合う **2 系統**（片方だけでは機能しない）:

| | 版ドリフト（`cli-drift.yml`） | contract テスト（CLI ごとに 1 ワークフロー） |
|---|---|---|
| 見る物 | 版の**番号**（ピン vs 公開 latest） | 実 CLI に当てた**挙動** |
| 答える問い | 「見に行くべき時か？」 | 「実際に壊れたか？」 |
| 費用 | 無料 | 無料〜サブスク枠（Tier による） |
| 頻度 | 2 時間ごと | main への PR（関連パス）+ 週次 cron + dispatch（claude・copilot・agy・cursor・kiro は dispatch 専用）|
| 赤くなる時 | 検査自体が失敗したときだけ | 契約が破れたとき（外部サービスに依存するステップは報告のみのことがある。例: opencode の live な Tier B ターン）|

ドリフトは**常態**（数日で版が進む CLI もある）なので、ドリフトのワークフローは赤くならない。
追跡 issue を 1 本だけ最新に保ち、ドリフトが解消すれば閉じる。検査する行は
`deploy/local/cli-drift-check.sh` の `TARGETS`: 版をピンしている全エージェント CLI と `rtk`。
lcpp は入らない——セルフホストのエンジンに対してプロセス内で動き、上流の CLI を持たない。

同じワークフローの `apt-pins` ジョブは Debian パッケージのピンを `deploy/local/apt-pin-check.sh`
で確かめる。ピンは `workspace/Dockerfile` の `apt-get install` から読み出す（現状は
`ARG CHROMIUM_VERSION` だけ）。ピンの全パッケージが amd64 **と** arm64 の両方で
trixie・trixie-updates・trixie-security のどれかに載っていること。trixie-security は現行ビルド
しか持たないので、Debian が次の更新を出した時点でピンは消え（片方のアーキテクチャだけ先に
消えることもある）、キャッシュの無い次のイメージビルドが落ちる。これはドリフトの定常状態では
なくビルドが壊れた状態なので、このジョブは赤くなる。出力には両アーキテクチャで配られている
最新版が出るので、ARG はその版へ手で上げる。判定は `deploy/local/apt-pin-check-test.sh` が固定する。

もう 1 本の `cli-release-watch.yml` は 2 時間ごとに（drift が :00、watcher が :30、版上げの保険が次の奇数時）公開版を比べ、**版が実際に変わった CLI だけ**
contract を dispatch する（対象の kind は `deploy/local/cli-release-edges.sh` の `KINDS`）。
状態は 1 本の issue に置く: `tested` と `seen` の印はコメントとして追記する。repository
variables は既定のトークンで書けないためである（`deploy/local/cli-release-state.sh`）。
`tested` を記録するのは contract が成功したときだけなので、`latest` に対して contract が赤で終わると、その版に `red` の印を書き（drift 追跡 issue の
`Red contracts` 欄にも行が出る。この欄は `develop` で直近 2 回続けて落ちた contract
ワークフローも示す: `deploy/local/cli-contract-report.sh`）、watcher は同じ版を再度
dispatch しない。赤い版が contract の枠を 1 日 12 回使うのを防ぐためである。新しい版・
成功した実行（`tested == latest` が優先）・手動 dispatch で解除される。キャンセルや
タイムアウトは印を残さないので再試行される。
クレデンシャルを無人で供給できない CLI は dispatch しない。secret が無い場合と、
クレデンシャルが回転する cursor・kiro がこれに当たる。それらは `seen` を記録し、secret を
更新したあとで contract を手動 dispatch する——「検出した」を「テストした」として記録する
ことはない。muse の contract はクレデンシャルが要らないので無人で dispatch される。

**取得元 1 つが読めなくても watcher は止まらない。** 行ごとに取得し、読めなかった行は
latest 不明として報告して、その行の dispatch と状態更新だけを飛ばし、残りはそのまま進む。
watcher が赤くなるのは、どの取得元も答えなかったときだけ。読めなかった行は job summary に
名前が出て、毎回の実行が自分の生存（最後に全行読めた時刻・最後に失敗した行）を状態 issue の
本文の `watcher` 欄に書く。`tested` が進まないのが「上流がリリースを止めた」のか
「watcher が落ちた」のかを区別するためである。`deploy/local/cli-drift-stub-test.sh` が
この 2 つの判断を固定している。最後の一歩である版上げ PR そのものは `cli-pin-bump.yml`
（§10.2.2）が開く。

**ワークフローは CLI ごとに 1 ファイル**（`claude-tui-contract.yml`、ほかは
`<kind>-contract.yml`）。パス条件も dispatch の入力もワークフロー単位なので、1 ファイルに
まとめると (1) 無関係な変更で走り、(2) 入力が混ざる——実際に codex の Tier 2 と claude の
L4 が 1 つの `live` 入力を共有し、1 回の dispatch で両方の枠が減った。ファイルを分ければこの
結合は構造的に起きない。横断の例外は定期実行の watcher 2 本と、`mcp-config-contract.yml`
（クレデンシャル不要）: レジストリ側の 1 つの契約——af が各 CLI のために書くグローバル MCP
設定ファイルの形——を複数の CLI にまたがって一度に検証する。CI が見るのは claude・codex・
opencode・copilot・cursor で、kiro（ログインが要る）と agy（ランナーで起動しない）はそこでは
見られない。muse と lcpp にはこのファイルが無い——サーバーをワイヤ上とプロセス内で受け取る
（`workspace/agent/internal/mcpreg/materialize.go` の `ServedKinds`）。

共通セットアップ（Go・Node・tmux・実 CLI）は composite action
`.github/actions/setup-agent-cli` にあり、`pinned | latest | <版>`（明示の版は npm の CLI
のみ）で、同じテストを「焼く版」にも「フリートが走らせる版」にも向けられる。muse の contract
はリリースの artifact を自分で導入する。マニフェストのチェックサムを検証すること自体が
テスト対象の一部だからである。

**ビルドタグ。** 素の `go test ./...` が前提にできないものを要する Go テストは、
`workspace/agent` で 3 つのビルドタグのどれかの後ろにある。タグが表すのは走らせる代償で、
どの CLI を相手にするかはテスト名（`TestDriftCodex…`・`TestContract…`・
`Test<Kind>TUIMirrorContract`）が表し、ワークフローはどれも `-run` でテストを選ぶ。

| タグ | 要るもの | 無いとき |
|---|---|---|
| `contract` | `PATH` 上の実 CLI（ペインのテストは tmux も）。実ターンを使うテストの一部は自分のオプトイン（`CLAUDE_CONTRACT_LIVE`・`COPILOT_CONTRACT_LIVE`・`OPENCODE_CONTRACT_LIVE`・`AF_IMAGEGEN_LIVE`）も待つ。TUI のプローブ（`TestClaudeTUIContractLive`・`TestClaudePlanApprovalContractLive`・`Test<Kind>TUIMirrorContract`）は待たず、CLI がサインイン済みなら実ターンを走らせる | skip。`E2E_REQUIRE=1` なら失敗 |
| `contract_live` | 実 codex のクレデンシャル。どのテストも実ターンを使う（`codex-contract.yml` の `live-drift`、dispatch のみ） | 失敗 |
| `contract_manual` | 人: その人が用意するエンジンのエンドポイント（`AF_LCPP_LIVE_*`）か対話のサインイン（`AF_AGY_LOGIN`）。どのワークフローも走らせない | skip |

したがって CLI がサインイン済みの機械で素の `go test -tags contract ./...` を走らせると、複数の
ベンダーの実ターンを一度に使う。ワークフローと同じく `-run` で 1 つの CLI に絞る。

`ci.yml` は 3 つとも `scripts/vet-build-tags.sh` で vet する。このスクリプトは知らないタグが
あれば失敗するので、新しいタグはそこへ足さないと赤くなる。`e2e` モジュールには別に `e2e` タグが
ある（§10.4 の E2E）。

### 公開リポジトリでの CI の前提（秘密情報の扱い）

このリポジトリは公開されている。**secrets そのものはリポジトリには無い**（リポジトリ設定に
暗号化して保管）。そのうえで次を保つ:

- **fork PR には secrets を渡さない。** `pull_request` トリガは secrets を渡さない仕様で、
  これに依存する。**`pull_request_target` と `workflow_run` は使わない**——どちらも
  「fork が書いたコードに secrets 付きで実行権を与える」典型的な穴。self-hosted runner も
  使わない。
- **実クレデンシャルで認証するジョブは `workflow_dispatch` でのみ走る**（たとえば `e2e` の
  `live-smoke`、`codex-contract` の `live-drift`、dispatch 専用の各 contract）。PR が起こす
  ジョブはクレデンシャルの secret を読まないので、fork PR が secrets 不在で赤くなることもない。
  定期実行のリリース watcher は、dispatch の前にそれらの secret があるかを確かめるだけ。
- **`run:` に `${{ github.event.* }}` を展開しない**——PR タイトルからのシェル注入になる。
- **秘密はファイルへ書き、標準出力に出さない。** たとえば kiro の認証 DB は 8 分割の base64 で
  保管し、そのままファイルへ復元する。
- **artifact と run ログは公開物として扱う。** 公開リポジトリでは誰でもダウンロードできる。
  secrets は完全一致でマスクされるが、**そこから派生した値はマスクされない**——たとえば
  ログイン済みセッションの観測 TUI フレームは、アップロード前にアカウント名とメールを伏字化する。
- **`permissions:` を全ワークフローで明示する。** 既定が変わっても最小権限が残るように。

**資格情報の混入検知**（`ci.yml` の `secret-scan`・gitleaks）。公開リポジトリでは混入は
即公開で取り返しがつかないので、差分ではなく**毎回全履歴**を走査する。押さえどころ:

- **`-m` を必ず付ける。** 無いとスキャナは merge commit を飛ばし、**衝突解決で入った内容が
  未走査のまま緑になる**——導入時点でこのリポジトリには該当する merge が 138 件あり、初回の
  スキャンは実際にこの穴を抱えていた。
- **スキャナの版とチェックサムを固定する。** marketplace action に依存しない。
- **偽陽性は値そのものを正規表現で外す——パスだけでの除外はしない**（`.gitleaks.toml`。
  `condition = "AND"` で値を 1 ファイルに絞ることはある）。パスで外すと、そのファイルに
  本物が入っても気づけない。
- 最初の全履歴監査（2026-08-01）は**本物の資格情報ゼロ**。コミットログを辿るのでなく、到達
  可能な全 blob を展開して走査する方法でも裏を取った。

## 10.5 コミットとブランチ

これらの規則は [CONTRIBUTING](../../CONTRIBUTING.md) が持つ。
[Ground rules](../../CONTRIBUTING.md#ground-rules) が秘密情報・pre-commit フック・コアを
デプロイ非依存に保つこと・`gofmt` を、
[Commits & PRs](../../CONTRIBUTING.md#commits--prs) が trunk とリリースのブランチ・
メッセージの形式と言語・マイグレーションに要る前方互換の明記・`Co-Authored-By` の帰属を扱う。
[AGENTS.md](../../AGENTS.md) はエージェントがコミット時に要る部分を繰り返し、そちらを指している。

## 10.6 ドキュメント

何を変えたら何を更新するかは [更新トリガの表](README.ja.md#更新トリガ)。全棚が従う規範は
[CONVENTIONS](../CONVENTIONS.ja.md)。

### 日本語 Console カタログの書き換え（`catalog-diff-check.py`）

`console/src/lib/i18n/locales/ja/<ドメイン>.ts` の言い回しの見直しで変えてよいのは文面だけです。
`scripts/catalog-diff-check.py <ref>` は `<ref>` の内容と作業ツリーを比べ、文字列リテラルの
中身以外の変更（キー・順序・コメント・`+` の構造）、キー集合の変化、`locales/` の `ja/` 以外
への変更を失敗にします。値ごとには、`{プレースホルダ}`・Trans のスロット・数字・ASCII の語・
「…」の中身・`code` スパン・改行・前後の空白・`guide/ref/glossary.ja.md` の用語の多重集合が
前後で同じであることを求めます。UI ラベル（15 文字以下で「。」なし）を言い換えるのは意図した
ときだけで、`--allow-labels` が無いと失敗し、付けると `旧 -> 新` を全部出して目で確かめられる
ようにします。変更した値の旧文面が `guide/**/*.ja.md`・Console のテスト・Go のソース・
`workspace/agent/knowledge/af-usage.md` にまだ引用されていれば PINNED として `file:line` つきで
報告して失敗します。その引用は同じ PR で直します（`--list-pinned` はその場所だけを出します）。短い一般語のラベルは別の用途で引用されていることもあります。中身を読んで Console の文字列の引用でないと確かめたその 1 件だけを `--exempt-pin KEY@PATH[:LINE]` で外せます（EXEMPT として出力・計上され、ほかの一致は失敗のままです）。
文の頭だけの引用や、旧い節を丸ごとは含まない引用は検索されません。書き換えたあとで、変更した値の書き出しをガイドで手検索してください。意味の良し悪しは判定
しません。`scripts/guide-diff-check.py` と同じくローカル専用で CI には載せません。テストは
`python3 -m unittest discover -s scripts -p test_catalog_diff_check.py` です。

#### `--lang en`: 英語カタログ

`--lang en` は同じスクリプトを `locales/en/<ドメイン>.ts` に向け、英語文言の見直しを守ります。
正本は ja で en は ja から派生するため、書き換えは ja との意味の一致を保つ必要があります。
スクリプトが守るのは構造と事実で、意味の良し悪しは人が判断します。フラグなし（既定）は上の
ja モードのままで、結果は変わりません。en モードでは `en/` 以外（`ja/` を含む）の変更は失敗で、
キー・順序・ファイルの扱いは同じです。変更した値ごとに、ASCII の語の規則の代わりに、
`{プレースホルダ}`・Trans のスロット・数字・`code` スパン・改行・前後の空白に加えて、全大文字の語、
識別子（パス、`AF_MASTER_KEY` のような環境変数、snake_case やドット区切りの名前、`--flag`、
`codex` のような CLI・製品名。大文字小文字も含む）、`"…"` の中身、→ と ⚠ の多重集合が前後で
同じであることを求めます。用語は `guide/ref/glossary.md` の Screen 列で、大文字小文字を区別せず
単語境界で数えます（複数形の `s` は同じ語）。ラベルは 30 文字以下で文末記号なしの値です
（`--allow-labels` は従来どおり）。PINNED は旧文面を文末・`{x}`・改行で分けた 20 文字以上の節で、
`*.ja.md` と `README*.md` を除く `guide/**/*.md`・Console のテスト・Go のソースを検索します（日本語文書の `workspace/agent/knowledge/af-usage.md` は対象外）。ラベルは
`**label**`・`"label"`・`'label'`・`` `label` `` の形でも探します。
`--list-pinned` と `--exempt-pin` は ja モードと同じです。変更した値で制限語（only, never, must,
not, cannot, default, required, unless, except。`can't`・`n't` は長い形として数える）の個数が
変わると `WARN restriction` を出して `warnings:` 行に合算します。失敗にはしませんが、
その書き換えは人の目で見る価値があります。`--triples` は変更した値ごとに `key`・現在の ja の値・
旧 en・新 en を TSV（タブと改行はエスケープ）で出して終了コード 0 で終わり、ja との意味の
一致を見るレビュー役に渡します。CI には載せません。

**意図した用語変更の承認**（`--allow-term`、`--allow-terms-file`）。用語の決定を適用する書き換えは
glossary の個数（ja ではさらに英字語の個数：`OFF` → `オフ`）を変えるため、上のチェックが報告します。
その変更だけをキー単位で承認します。

```
--allow-term KEY:OLD>NEW[*N]              繰り返し可。N の既定は 1
--allow-terms-file PATH                   1 行 KEY<TAB>OLD<TAB>NEW[<TAB>N]。空行と # 行は無視
```

承認が成り立つのは、そのキーの変更後の値で OLD の個数がちょうど N 減り、NEW の個数がちょうど
N 増え、その語を数えるすべてのカテゴリ（glossary、ja の英字語規則、en の ALL_CAPS 規則）で
そうなっているときだけです（どのカテゴリも数えない語は直接数えます）。値のそれ以外の部分は
従来のチェックのままで、別のキーで同じ語が変わると失敗し、`--allow-labels`・PINNED・
ほかのカテゴリは緩めません。適用した承認は 1 件ずつ出力します（`ALLOWED term: … OLD -> NEW xN
(OLD a -> b, NEW c -> d)`）。適用されなかった承認（向きや個数が違う、キーが未変更・存在しない）は
見えた個数つきの `FAIL allow` になり、それで説明するはずだった差分も失敗のままです。`failures:`
行の `allow=` は承認を渡したときだけ付くので、フラグ無しの出力は従来と同一です。数え方は
既存の規則のままです（ja は部分文字列なので `既定値` の中の `既定` も数え、en は単語境界）。

2 つのカテゴリが数える語（en の ALL_CAPS の glossary 語 `DEFAULT`：glossary は大文字小文字を区別せず、
caps は区別する）は (カテゴリ, 単位) ごとに合算するので、`default DEFAULT` → `standard standard` には
`Default>standard` と `DEFAULT>standard` の両方が要ります。メッセージにはカテゴリ名
（`[glossary]`・`[caps]`・`[latin]`、どのカテゴリも数えない語は `[direct]`）が付きます。

同じキーで OLD・NEW が同じ承認を重ねると合算されます（`OFF>オフ` 2 つは `*2` と同じ）。同じキーの
ある承認で OLD、別の承認で NEW になる語（逆向き・連鎖）は、互いに相殺するので終了コード 2 で
拒否します。正味の変更を 1 つの承認で書いてください。TSV ファイルは各欄をそのまま使うので語に
`>`・`*`・`:` を含められます。コマンドラインでは `KEY` は最初の `:` まで、`OLD` は最初の `>` まで、
NEW 末尾の `*数字` は個数指定です（そう終わる語はファイルで渡してください）。

例（ja：`既定 OFF。` → `既定ではオフです。`、`デフォルト` → `既定`）：

```
python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
  --allow-term 'agents.note_x:OFF>オフ' --allow-term 'surface_color.default:デフォルト>既定'
```

フラグ無しだと `FAIL latin`（`OFF` が消えた）と `FAIL glossary`（`オフ` 0 → 1、`既定` 0 → 1）、
付けると `ALLOWED term:` が 2 行出て終了コード 0 です。同じ決定を別のキーに適用するなら承認も
もう 1 つ要ります。en も同様です（`--lang en --allow-term KEY:OFF>off`。glossary の語は大文字小文字を
区別せずに照合します）。

**ラベル同期**（`--list-citations`、`--rewrite-guide`、`--allow-split`）。ラベルの
書き換えでは引用も同じ PR で更新します。常時有効な SPLIT 検査は、ファイル引数で検査対象を
絞っても**選択言語の全ドメイン**を調べ、同じ旧ラベルを持つすべてのキーが同じ新文面に
変わることを求めます。不一致は `FAIL split` にキーを列挙し、終了コード 1 です。
独立した用途と確認した分岐だけを `--allow-split KEY,KEY`（繰り返し可）で承認できます。
変更キーも含め、報告された分岐の全参加キーを指定します。不正な指定は終了コード 2、
該当する分岐のない古い指定は失敗です。承認済みでも、引用からキーを特定できないため
分岐ラベルの自動書き換えは行いません。分岐も新フラグもない実行の出力は従来どおりです。

```
python3 scripts/catalog-diff-check.py origin/develop --list-citations
python3 scripts/catalog-diff-check.py origin/develop --allow-labels --rewrite-guide
```

`--list-citations` は読み取り専用で、分岐があっても終了コード 0 です。出力の区分は
`guide`、`console tests`、`af-usage.md`、`af-usage.coverage.tsv`、`Go sources`。
各行は `path:line<TAB>form<TAB>old<TAB>new<TAB>key` で、値中のバックスラッシュ・タブ・
改行はエスケープします。共有キーごとに引用行を出します。この 2 モードと `--list-pinned`、
`--triples` は同時に指定できません。

照合は大文字小文字も含む完全一致です。`「…」`、`**…**`、引用文字列（`"…"`、`'…'`、
バッククォート、曲線の二重引用符）の中身全体、または ` > ` / `→` で区切ったメニューの
1 項目が旧ラベルと同じ場合に一致します。メニュー項目は次の区切り、行末、括弧・読点・
表の縦棒などで終わり、空白と書式の記号は保持します。ガイド本文の裸の一致は前後に
Unicode の文字・数字・アンダースコアがない場合だけです。長い引用・太字の内部は除外するので、
`保存` は `「保存中」`、`**保存中**`、`保存中` に一致しません。Console テストと Go は
引用文字列・括弧引用・太字の完全一致だけを対象にし、知識の台帳は TSV セル全体の一致も
調べます。ATX / setext 見出しは形式に関係なく `heading`、フェンス内のガイドのコードは
`code` とします。本文に埋め込まれた日本語や説明付きのメニュー項目はこの保守的な照合では
拾えないため、別途手検索してください。

`--rewrite-guide` が書き込むのは `guide/**/*.ja.md` だけです。`--lang en` は
`*.ja.md` を除く `guide/**/*.md`（英語 README も含む）に適用します。見出しとコードフェンス
以外の行で、括弧引用・太字・メニュー項目の完全一致だけを置換します。
`guide/ref/settings.ja.md`（英語は `settings.md`）の第 1 列は `table-cell` として
書き換えます。`set.tab_*` / `tenant.tab_*` の変更は `WARN SETTINGS TAB` を出します。
`scripts/docs-check.py` の規則は変えず、そのセルを更新して検査を通します。個人設定の日本語タブは対応する
`guide/member/12-settings.ja.md` の節見出しも手動更新が必要で、表の置換だけでは
検査を通りません。編集はすべて
`path:line old -> new` として出力します。

書き込み前に全対象の git status を確認します。ステージ済み・未ステージ・未追跡の変更が
ある対象は終了コード 2 で拒否するので、内容を確認してから `--force` を使ってください。
2 回目の実行では編集しません。ラベルの連鎖・入れ替えは再実行時の連鎖置換を避けるため
自動書き換えを拒否し、ガイドを手で直します。`guide/` 外へのシンボリックリンクも拒否します。
従来の構造・用語・ラベル検査は引き続き適用されます。

書き換え後には残った引用を同じ TSV 区分で列挙します。見出し（アンカーと、サイトの
`/ja/features/` を含む被リンク）、裸の本文、その他の引用形式、フェンス内のコード、テスト、
知識の 2 ファイル、Go ソースは手動更新です。残った引用があれば終了コード 1 です。
引用とは独立した用途と確認した一致には既存の `--exempt-pin KEY@PATH[:LINE]` を使えます。
EXEMPT を出し、古い指定は警告します。承認で除外したスパンは自動置換からも保護します。
手動更新後に再実行してください。

例：`保存` を `保存する` に変えるなら、まず旧文面が `保存` の**全カタログキー**を変え、
`--list-citations` で確認します。`--allow-labels --rewrite-guide` は `「保存」` と
`**保存**` を新ラベルに変え、`保存中` は保持し、`## 保存` の見出しや `toBe("保存")` の
テストを残件として出します。見出し・リンク・アサーションを同じ PR で更新してから、
ガードと `scripts/docs-check.py` を再実行します。このツールの導入にカタログの書き換えは
不要で、引き続きローカル専用・CI 対象外です。

自動置換は Markdown のソース上の保護対象も判定します。インラインコード（複数の
バッククォート・行をまたぐスパンを含む）、フェンス・字下げしたコード、先頭の YAML
フロントマター、HTML のタグ・属性・コメント、URL、インラインリンクの行き先とタイトル、
参照リンク定義は置換しません。その中の完全一致は `code` / `metadata` として列挙し、
手動確認または `--exempt-pin` を求めます。フェンスは開始記号の種類と長さを保持し、
同じ記号で同じ長さ以上、末尾に内容がない終了行だけで閉じます。引用ブロック・リストの
入れ子も判定します。`「**ラベル**」` / `**「ラベル」**` のような入れ子の完全一致も
列挙し、内部のラベルを 1 回だけ置換します。編集範囲の重なりは拒否します。

ラベル同期モードには比較するカタログファイルの旧版・新版が両方必要です。追加・未追跡・
削除・移動したドメインファイルは空の引用一覧として扱わず、診断と終了コード 2 を返します。
比較ファイル数と変更ラベル数も stderr に出し、0 件の場合は参照に対するカタログ差分が
ない場合と、差分はあるが変更ラベルがない場合を区別します。

引用ブロック・リスト内の見出し（setext 形式も含む）は文面とアンカーを手動扱いにします。
設定の `table-cell` 例外は、変更した `set.tab_*` / `tenant.tab_*` キーについて、
有効な `タブ`（日本語）/ `Tab`（英語）の表見出しと区切り行に続く第 1 列だけです。
概念・層の表やタブ以外のキーは通常の本文として手動扱いです。SPLIT は常時有効なので、
共有ラベルの一部だけを変更すると、新フラグなしでも意図的に失敗の出力と終了コードが変わります。
SPLIT 違反も新フラグもない実行では従来の出力をバイト単位で維持します。

setext 見出しは下線直前の 1 行だけでなく、直前の Markdown 段落の全行を手動扱いにします。
引用・リスト内で行をまたぐ見出しも同様です。参照リンク定義にもコンテナの正規化を適用し、
行き先・次の行に続く行き先・折り返したタイトルを metadata として保護します。空行で
ブロックが終わるため、前後の通常の段落の引用には通常の置換規則を適用できます。

### 日本語表記のドメイン別適用（`ja-notation-normalize.py`、B1）

`scripts/ja-notation-normalize.py` は
`console/src/lib/i18n/locales/ja/<ドメイン>.ts` の表記だけを扱う、決定的な
ローカル専用の計画・編集ツールです。CI には載せず、TypeScript の評価も外部サービスへの
接続も行いません。ツールの導入 PR ではカタログを変更しません。サインイン／ログイン、
配備、端末、利用枠、畳むなどの用語判断は B2、ボタンの動作による表記判断は手動です。
[日本語の表記規則](../CONVENTIONS.ja.md#11-ui-文言とガイドの日本語表記)と
[用語集](../../guide/ref/glossary.ja.md)に従います。

- **R1:** 欧文・数字と日本語の間に半角スペースを挿入します。
  `Gitホスティング` → `Git ホスティング`、`30日後` → `30 日後`。
  プレースホルダの境界は、数値として確認済みの名前
  `n`、`count`、`days`、`profiles`、`hosts`、`bytes`、`applied` だけが対象です。
  `{n}人` は `{n} 人` になります。`{msg}`、`{name}` などは判断せず報告します。
  既存のスペース、文字列の両端、日本語の句読点・括弧（`、。「」（）・：`）は変更しません。
  空白のない 3 文字以下のラベルは見送ります。欧文単位、範囲、時刻、版番号、倍率
  （`30GB`、`30 GB`、`1〜10`、`12:30`、`v1.2`、`3x`、`×1.25`）とその境界も保護します。
- **R2:** `既に` → `すでに`。`無い` → `ない`、`無く` → `なく` は、文法を確認したキーと
  元の値全体が `scripts/ja_notation_contexts.json` に登録されている場合だけです。
  保護・見送り対象も含めて、各出現を
  `CONTEXT R2 キー@位置 語 | 値全体` で列挙します。別のキーや書き換わった文には承認を
  流用しません。`無料`、`無効`、`無制限`、`無視`、`無理`、`無事`、`無限`、`無駄`、`無数`
  などの複合語、名詞の `無し`、動詞の `無くす`／`無くなる` は変更しません。
  許可リストの追加には文法の確認が必要で、ドメイン別の実行担当は追加しません。
- **R3:** 日本語の文中にある独立した `Workspace` を `ワークスペース` にします。
  識別子と、欧文の語に隣接する場合（製品名の可能性。実カタログの `Google Workspace`、
  `Workspace Agent` を含む）は保護し、単独の `Workspace` ラベルも見送ります。
  R3 の一部として、置換後の語と日本語・助詞・句読点の間にあった半角スペースを除きます
  （`Workspace を破棄` → `ワークスペースを破棄`）。欧文・数字・プレースホルダ・code の
  隣のスペースは残します。前後端の空白・タブ・改行も保持します。dry-run はこの最終の
  日本語表記を示します。R1 自体はスペースを削除しません。変更キーごとに
  `KEY<TAB>Workspace<TAB>ワークスペース<TAB>N` を出力し、
  `catalog-diff-check.py --allow-terms-file` に渡せます。それ以外の用語・欧文の個数の
  変化を説明できなければ、その値の提案全体を見送ります。

どの規則も `code` スパン、プレースホルダの中身、Trans スロット全体
（`<n>…</n>`／`<n/>`）、引用（「…」も含む）、識別子、パス、URL、環境変数を保持します。
閉じていないマークアップも見送ります。エージェント向けの `plan.review_prompt_*` と `wi.prompt_*`、
通知の speech（`speech_bare` などの派生形を含む）、`err.*`、`chat.report.*`、
`clean.reason*` は対象外です。`launch.first_prompt_note` のような UI の説明は対象のままです。
Unicode エスケープの正しいサロゲート対は報告・照合時にデコードし、元のエスケープの
バイト列は保持します。対のないサロゲートエスケープはカタログ・成果物の書き込み前に拒否します。
エスケープされた文字や連結リテラルの境界は、元の構文を保持したまま編集できなければ
見送ります。字句走査はカタログガードと同じ方式で、編集は文字列リテラルの中身だけです。
キー、コメント、引用符、エスケープ、改行、順序、`en/` は保持します。未対応の式、キーの
重複、シンボリックリンクのカタログは拒否します。選択したカタログにステージ済み・
未ステージ・未追跡の変更があれば、書き込み前に全対象を検査して拒否します。確認して
`--force` を明示した場合だけ適用でき、表記の除外規則を緩めるものではありません。

報告には全提案の `key | old | new`、見送りの理由、ドメイン別・規則別の置換数と見送り数を
出します。`CANDIDATE (manual only)` は空白・`…`・`を` で違うラベルの組をドメインをまたいで
列挙するだけで、形の選択や「…」の変更はしません。共有ラベルは全カタログで検査し、
SPLIT を起こす提案は相手のキーつきで見送ります。対象範囲が許す場合だけ、関係する
ドメインをまとめて指定します。

```sh
python3 scripts/ja-notation-normalize.py --all
python3 scripts/ja-notation-normalize.py --domain settings --dry-run \
  --report "$AF_WORK_DIR/settings-plan.txt" \
  --allow-terms-out "$AF_WORK_DIR/settings-plan.tsv"
python3 scripts/ja-notation-normalize.py --domain settings --apply \
  --allow-terms-out "$AF_WORK_DIR/settings-applied.tsv" \
  --report "$AF_WORK_DIR/settings-applied.txt"
# Comma-separated domains and a nonempty subset of R1,R2,R3 are supported.
python3 scripts/ja-notation-normalize.py --domain settings,repos --rules R1,R2
```

既定は dry-run で、`--all` は dry-run 専用です。計画時の報告・承認ファイルは任意です。
Workspace を変える適用には `--allow-terms-out` が必須で、無いと書き込み前に拒否します。
出力先は毎回新しいパスにします。既存の成果物、同一の報告・承認パス、カタログ・スクリプト・
git メタデータへの出力は拒否します。`.git` が gitdir ファイルの worktree でも、実際の
個別・共有の Git ディレクトリを保護します。成果物を先に作成するため、その書き込みに失敗しても
カタログは変更しません。適用時の承認ファイルは PR の確認まで保持します。
確認済みの変更に `--force` を付けて再適用しても追加のカタログ編集はありません。
最初の承認ファイルを空の計画で上書きしてはいけません。Managed セッションで
`AF_WORK_DIR` が未設定なら `~/.af-work/<作業コピーのディレクトリ名>/` を使い、後で片付けます。

**ドメイン別の実行手順:**

1. 割り当てられたブランチの変更のないカタログから始めます。`--all` で規模を確認し、
   対象ドメインを dry-run して、新しいパスに報告・承認ファイルを出します。
   **全提案・CONTEXT・SKIPPED を読みます。** 見送りはそのまま残し、文法・製品名の判断を
   レビュー役に渡します。規則や許可リストを広げず、B2 の用語変更も行いません。
2. 同じドメイン・規則を適用し、新しい適用時の承認ファイルを出します。値だけの差分かを
   確認し、次の dry-run の提案が 0 件になることを確かめます。変更済み・未追跡のカタログへの
   `--force` は内容確認後だけです。
3. 出力されたガードを実行します。検査・引用更新の呼び出しにも承認ファイルを渡します。

   ```sh
   python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
     --allow-terms-file "$AF_WORK_DIR/settings-applied.tsv"
   python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
     --allow-terms-file "$AF_WORK_DIR/settings-applied.tsv" --list-citations
   python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
     --allow-terms-file "$AF_WORK_DIR/settings-applied.tsv" --rewrite-guide
   ```

   必須の最終終了コードは **0** です。引用が残っているラベルでは、同期前の PINNED による
   終了コード 1 は想定内ですが、失敗を無視してよい意味ではありません。ほかの失敗は先に
   解消します。ガイド更新も手動対象が残れば 1 になります。見出し・被リンクのアンカー、
   裸の本文、テスト、知識、Go の引用を同じ表記 PR で直します。`--list-citations` を読み、
   `--exempt-pin` は確認済みの独立した用途だけに使います。SPLIT は全参加キーを揃えるか、
   別途確認した `--allow-split` の判断が必要です。短縮された引用はガードがすべて拾えないため、
   文頭も手検索します。
4. ガードを再実行して 0 にし、`python3 scripts/docs-check.py` と関連する Console テストを
   **`console/` から**、ワーカー数を抑えて実行します。規則を直す場合は
   `python3 -m unittest discover -s scripts -p test_ja_notation_normalize.py` と
   `python3 -m unittest discover -s scripts -p test_catalog_diff_check.py` も実行します。
5. PR 本文にはドメイン・規則、提案・変更・見送り数、全提案を確認した精度（誤提案数／提案数）、
   承認行、引用更新・手動判断、検証コマンドと終了コードを記録します。計画の指定どおりに
   ラベルと文のバッチを分けます。pre-commit フックを実行してコミット・push し、
   `develop` 向けの PR を開きます。

R3 の空白処理を含む settings の受け入れ確認は作業用コピーで全 30 変更値を読み、誤提案 0 件
（精度 100%）、R1 が 36 挿入、R2 が 8 置換、R3 が 1 置換、見送りは 9 出現でした。
初期の 32 値の計画には `{msg}` 境界の誤提案が 2 件あり、確認済みの数値名に制限して
除きました。Workspace が欧文だったためのスペースも R3 で除きます。その保持で
日本語の語と助詞の間に余分な空白が残る問題を解消しました。全 23 ドメインの dry-run は
約 1 秒です。作業用コピーの最初のガードは
PINNED が 20 件で終了コード 1、構造・不変条件の失敗は 0 件でした。ガイドの自動更新と
手動の引用同期後の最終ガードは 0 です。受け入れ確認のカタログ変更はコミットしません。
テストは各保護認識器と一部の除外規則を外す変異でも負例が失敗することを確かめます。
実ガードの fixture は Workspace の承認なしで失敗し、生成した承認を付けると通ります。

R3 の空白処理後も全カタログの計画は 16 変更値（Workspace の 17 出現）です。
`ワークスペース`・`すでに`・`ない`・`なく` の両側で日本語の文字との間にある空白を検索すると、
16 件から 0 件になりました。さらに `admin,settings,workitems` の作業用コピーでは
64 値を変更し、13 行の承認がすべて適用されました。最初のガードは PINNED のみ 32 件を
報告し、引用・アンカー・カバレッジ台帳を同期した後のガードと docs-check は終了コード 0
です。2 回目の dry-run は提案 0 件で、検証後に作業用コピーを削除しました。

### 判断をレビューする日本語の用語計画（B2）

B2 は語義の判断と適用を分けます。モデル・人が
`scripts/ja-term-candidates.py` の候補を読み、理由を添えた計画を書き、
`scripts/ja-term-apply.py --check-only` で検証してから適用します。
両ツールは B1 の文字列リテラル tokenizer を使い、TypeScript を実行しません。
ローカル専用で CI には組み込みません。ツールの導入ではカタログ・ガイドの用語を変更しません。

| ファミリー | 判断と許される形 |
|---|---|
| F-login | Agent Fleet 自身の IdP はサインイン。外部の CLI・サービス（サービスとしての GitHub も）はログイン。双方向の置換を許します。テナントのグループ名とガイドの引用も確認します。 |
| F-deploy | 名詞は配備、動詞のデプロイするはそのまま。デプロイ既定 → 配備の既定、デプロイ全体 → 配備全体も含みます。 |
| F-device | ブラウザ・デバイスの範囲はこのブラウザ。この端末 → このブラウザ、端末 → ブラウザ（ほかの端末など）、端末 → このブラウザを明示します。ターミナルエミュレータの端末は保持します。 |
| F-slot | 枠 → 利用枠／子の上限。子の数を誤って利用枠と呼んだ場合の利用枠 → 子の上限も含みます。期間・無料の枠は個別に判断し、スロットは EC2 だけに使います。 |
| F-fold | UI の折りたたみは畳む、セッションを終える意味は停止／終了。畳む・畳まれ・畳んだ・畳んで・畳めの形を明示します。データの集約は別の語義なのでこのバッチでは見送ります。 |
| F-onoff | 本文の ON/OFF → オン/オフ。コントロール自身の名前に合わせてオン/オフ ↔ 有効/無効。欧文は日本語に隣接していても単語全体だけを対象にします。 |
| F-buttons | `scripts/ja_term_common.py` の `BUTTONS` にある 16 組の名詞／する。通常のボタンは名詞、する形は確認ダイアログの実行ボタンだけです。 |
| F-default | デフォルト → 既定。 |
| F-variants | 共通モジュールの `VARIANTS` にある値全体の組（保存中／保存中…、送信／送信…、セッション削除／セッションを削除、APIトークン／API トークンなど）。… は進行中の表示とダイアログを開くボタンだけ。ほかは多数派の形を判断します。 |

置換表は語義を決めず、判断の理由も生成しません。新しい組にはレビューと正例・負例のテストが
必要です。計画で任意の語・空白・並べ替え・本文の … を持ち込めません。同じ値の中で
同一の組を両方向に置換すると、語の交換が用語集の増減に現れないため拒否します。
ON → オンと OFF → オフのような独立した組は適用できます。登録された組でも、
行端の空白を消すなど保護規則に反する置換は拒否します。手動で扱う事例であり、
`--force` で回避する理由にはなりません。

```sh
python3 scripts/ja-term-candidates.py --stats
python3 scripts/ja-term-candidates.py --family F-login,F-deploy --domain settings --format tsv
python3 scripts/ja-term-candidates.py --family F-device,F-fold --format md --limit 20
python3 scripts/ja-term-apply.py --plan "$AF_WORK_DIR/term-plan.tsv" --check-only
python3 scripts/ja-term-apply.py --plan "$AF_WORK_DIR/term-plan.tsv" --dry-run
python3 scripts/ja-term-apply.py --plan "$AF_WORK_DIR/term-plan.tsv" --apply \
  --allow-terms-out "$AF_WORK_DIR/term-allowances.tsv" --report "$AF_WORK_DIR/term-report.txt"
```

候補は出現ごとに 1 行で、ドメインのファイル・キーのソース順・登録されたファミリー順・
デコード後の位置の順に並びます。キー、ドメイン、ラベル／文（15 文字以内で 。 なし）、
値全体、該当語、Unicode コードポイントの `[start,end)`、該当文、前後のキーと値、
英語の値、ガイドの一致数と最初の 2 行、テスト・Go・知識の一致位置、UI ソースのキー参照、同じ値を共有する
全キー、除外、語義の基準を含みます。前後のキーはファイル内の位置で、カードをまたぐ
場合があります。カードの所属は推測しません。英語の空欄はキーの欠落、一致の空欄は
走査器が見つけなかったことだけを示します。ラベルは既存ガードの引用機構、文は改行を
つないで検索する PINNED の句を使います。共通の短い文が偶然一致する場合もあり、
判定者が読んで引用かどうかを確かめます。`--stats` はファミリー・ドメインごとの
出現数・値数・ラベル数・除外出現数を表示し、stderr に実測時間を出します。
`--limit` は候補行だけを制限し、統計は制限しません。除外された値も候補には残します。
UI の参照は引用されたキーの完全一致のファイル・行で、コントロールの役割は推測しません。
組み立てたキー・動的なキーは手動で確認します。

F-fold は追加の活用も見えるように畳み・畳ま・畳ん・畳もを候補に出しますが、
この 4 語幹からの適用の組はありません。そのままにするか、テストを伴う組を別途レビュー
してから扱います。候補に出ることは適用の許可ではありません。F-device の自動検出は
端末とこのブラウザだけで、既存の単独のブラウザは対象外です。ブラウザ → このブラウザも
適用の組ではありません。既存の表現を確認する場合は
`rg -n 'ブラウザ' console/src/lib/i18n/locales/ja` で手動検索します。
端末 → ブラウザは「ほかの」などを保持するための組で、すべてのブラウザをこのブラウザに
変更する許可ではありません。

**判定者の確認事項:** 値全体、該当文、前後のキー、曖昧さを確かめる英語、ガイドの引用、
[`glossary.ja.md`](../../guide/ref/glossary.ja.md) を読みます。
ボタンの動作・コントロール名・範囲が不明ならコンポーネントも読みます。各一致の前後の文字も
確認してください。組は外枠・携帯端末・折り畳む・ログインID などの複合語の内部にも一致します。
機械的な一致だけでは語義は決まりません。ターミナルエミュレータの端末と動詞のデプロイするは
保持し、複合語を個別に判断します。そのキーの語義と、
何を根拠に決めたかを理由に記録します。ラベルと文を別バッチにし、共有ラベルは全キーを
同じ新しい値で揃え、曖昧な場合はそのままにします。`*prompt*`、speech の派生、
`err.*`、`chat.report.*`、`clean.reason*` は変更できません。用語集の行には別途明示的な
判断が必要です。apply が編集するのはカタログのリテラルだけです。

UTF-8 の計画には BOM とヘッダーを付けず、TAB 区切りで次の 5 フィールドを指定します。

```text
key<TAB>old_value<TAB>new_value<TAB>family<TAB>reason
surface_color.default<TAB>デフォルト<TAB>既定<TAB>F-default<TAB>Initial setting; checked glossary and English
```

`<TAB>` は実際のタブです。空行と `#` コメントは無視します。
フィールド内のバックスラッシュ・タブ・改行は `\\`・`\t`・`\n`・`\r` と書きます。
候補も同じ可逆なエンコードです。不明なエスケープは拒否します。現在のデコード済みの値を
そのまま写し、コピー時に正規化しないでください。

書き込む前に全行を検証します。ja にキーが 1 回だけ存在して旧値（適用済みなら新値）と
完全に一致すること、宣言したファミリーの置換だけであること、placeholder・slot・数字・
コード・引用・改行・行端の空白が変わらないこと、除外でないこと、共有ラベルの全キーが
揃うこと、重複キーがないことを確かめます。未対応の構文、symlink、エスケープされた文字や
連結リテラルの境界をまたぐ変更は拒否します。組み直した値とソースの骨格も照合します。
対象ファイルが dirty ならレビュー後の `--force` が必要ですが、検証は緩めません。
適用済みの計画を再度適用しても、前回の編集が dirty のままで追加の変更はありません。
旧値・新値が混在する計画も再開できます。元の計画と承認ファイルは保持してください。

未適用の編集には、新規の承認出力先が必要です（内容が空でも指定します）。
ガードの数え方に合わせた `KEY<TAB>OLD<TAB>NEW<TAB>N` を生成し、用語集・欧文の増減を
すべて検証してから出力します。レポート・承認はカタログの編集より先に排他的に作成し、
既存の成果物、追跡済みのソース、カタログ・スクリプトのパス、計画の別名、symlink、
Git のメタデータへの出力は拒否します。別々の新規パスを使います。`--check-only` は
成果物もカタログも書かず、dry-run はレビュー用の成果物を出力できます。
`AF_WORK_DIR` がない場合は `~/.af-work/<作業コピーのディレクトリ名>/` を使って後片付けします。

用語集・欧文の単位ではない直接の語については、そのキーの承認にある計数単位の内部の出現を
ガードの数から除きます。`枠 → 利用枠` は単独の枠が 1 個減って、用語集の利用枠が 1 個増える
置換です。すでにある利用枠を旧語の枠として重複して数えません。用語集・欧文の計数と
指定した N は厳密なままで、英語の計数は変更しません。

**適用と引用同期:** 生成した承認を、すべてのガード呼び出しに付けます。
手動で引用を解消してから、リポジトリのルートで docs-check を実行します。

```sh
python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
  --allow-terms-file "$AF_WORK_DIR/term-allowances.tsv"
python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
  --allow-terms-file "$AF_WORK_DIR/term-allowances.tsv" --list-citations
python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
  --allow-terms-file "$AF_WORK_DIR/term-allowances.tsv" --rewrite-guide
(cd console && npm test -- --maxWorkers=2)
python3 scripts/docs-check.py
```

引用が古いと最初のガードは PINNED で 1 になります。ほかの失敗はすべて解消します。
`--list-citations` はラベル用で、文は `--list-pinned` と文頭の手検索も使います。
引用を全部読み、見出し・アンカー、テスト、console-e2e、両方の知識ファイル、Go の対応文を
手動で更新し、ガードを再実行して 0 にします。`--rewrite-guide` は見出し・コード・
メタデータを保護します。`--exempt-pin KEY@PATH[:LINE]` は確認済みの独立した用途だけです。
一致 0 件は依存がない証明ではありません。

**B1 の教訓:** 文言バッチでは上の Console の全テストが必須です。組み立てた文字列、
golden、console-e2e の期待値、Go の対応文は PINNED に見えない場合があり、
対象を絞ったテストだけでは CI の失敗を見逃しました。golden は担当テストが定めた
更新フラグだけで再生成して差分を読み、無条件に置き換えないでください。
引用を変更したら関連する console-e2e・Go の検証も実行します。実際の集計行と終了コードを
読まずに成功・失敗を宣言しません（終了コードを隠す `| tail` は使いません）。
ツールを直したら `python3 -m unittest discover -s scripts -p 'test_*.py'`、Python の
コンパイル、docs-check、ステージ済みの pre-commit フックを実行します。B2 のテストには
検証を外す変異と、実ガードによる承認なしの負例・承認ありの正例が含まれます。

#### B2 ツールの受け入れ確認

導入時点の 23 ドメインの統計は 2.8 秒でした。すでに標準の形と除外された値も含む探索の数で、
変更の提案数ではありません。

| ファミリー | 出現数 | 値数 | ラベル数 | 除外出現数 |
|---|---:|---:|---:|---:|
| F-login | 294 | 238 | 52 | 22 |
| F-deploy | 122 | 111 | 12 | 24 |
| F-device | 34 | 29 | 5 | 3 |
| F-slot | 28 | 26 | 7 | 0 |
| F-fold | 10 | 9 | 3 | 1 |
| F-onoff | 242 | 187 | 38 | 10 |
| F-buttons | 81 | 81 | 81 | 0 |
| F-default | 143 | 140 | 51 | 3 |
| F-variants | 83 | 83 | 83 | 0 |

device/fold の全 44 出現行を読みました。下の判断は全キーを含み、複数出現にも同じ判断を
適用します。確認用の例であり、本番の計画や追跡されたカタログの変更承認ではありません。

| ファミリー・キー | 判断と根拠 |
|---|---|
| F-device: `noti.table_note`（2）、`launch.tmpl.err.not_loaded`、`awslogin.close_hint`、`display.per_tenant_note`、`display.pane_layout_note`、`set.prefs_unsynced`、`gcplogin.close_hint` | 端末 → ブラウザ、この端末 → このブラウザ。英語はデバイス単位の設定や別デバイスからのアクセス、前後は設定・ログイン依頼です。7 値・8 出現。 |
| F-device: `assistant.note_appearance`、`noti.os_unsupported`、`ovw.waiting_hint` | すでにこのブラウザなので保持。外観・ブラウザの通知・観測の範囲です。 |
| F-device: `err.plan_pending`、`err.permission_pending`、`err.interaction_pending` | 除外。意味も対話カードがない場合の端末です。 |
| F-device: `admin.idle_hold_watching_row` | このバッチでは保持。打鍵と Console の操作による自動停止の保留で、ブラウザ単位の設定ではありません。追加した UI の参照で保留の種類の描画を確認できます。 |
| F-device: `admin.term_log_hint`、`onb.rtt_title`、`noti.kind_terminal_notification`、`srow.keep_awake_hint`、`agents.skip_permissions_off_note` | 端末出力の保存・エコーの経路・端末からの通知・端末を閉じた作業・端末での許可への返答なので保持します。 |
| F-device: `keys.kt.termPrioTitle`、`keys.kt.termPrioLabel`（2）、`keys.kt.termPrioNote`（2）、`keys.kt.shellPassLabel`、`keys.kt.shellPassNote`（3） | 全 9 出現を保持。前後と英語が shell・tmux・端末の入力を明示します。 |
| F-device: `ssm.term_color`、`ssm.term_preview_title`、`ssm.term_label`、`git.identity_help`、`view.lfs_tip` | 端末の色・プレビュー・コミット・git lfs pull の実行なので保持します。 |
| F-fold: `fgraph.collapse`、`ovw.collapse`、`wsbar.usage.fold_hint`、`wsbar.usage.place_fold`、`wsbar.usage.place_fold_title`、`agents.expand_thinking_note` | UI の折りたたみの 6 出現を保持。英語と次のキーが展開・popover・配置・表示のコントロールを示します。 |
| F-fold: `agents.skip_permissions_off_note`（2） | 確認用の判断は両方の畳まれ → 停止され。対話の期限で実行中のセッションを止め、保留中の会話は持ち越します。許可カードの参照で本文でありエージェント向け prompt でないと分かります。 |
| F-fold: `plan.review_prompt_reply` | セッションを終える意味でもエージェント向け prompt なので除外します。 |
| F-fold: `usage.truncated_hint` | このバッチでは保持。英語と使用量グラフの参照が保持期間後の集約・削除を示し、UI の折りたたみやセッションの終了とは別です。 |

値全体・前後・英語・出典付きの一致で device/fold の判断ができ、曖昧な役割と後のボタンの
判定のために UI のキー参照を追加しました。参照はソース確認の出発点で、ボタンの動作の
証明ではありません。共通の文の一致は読む必要があり、短縮した引用は手検索が必要です。

SAMPLE の device 計画は作業用のアーカイブだけに適用しました。4 ファイル・7 値・8 置換、
承認は 4 行です。再適用は適用済み 7・未適用 0。実ガードは 2,412 値を照合し、4 行全部の
承認を認め、最初の失敗は通知設定の本文の `pinned=1` だけでした。ラベルの引用モードは
4 比較ファイル・変更ラベル 0 を明示しました。手動でガイドの 2 句を直しました。
2 つ目は短縮した引用で PINNED には見えませんでした。最終ガードとガイド書き換えは 0、
失敗の各項目も全部 0 です。作業用の文言・golden の更新はこのツールの PR に含めません。
Console の全テストは後の実際の文言バッチで実行します。
