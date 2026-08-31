---
audience: "はじめてこのリポジトリをビルドする人"
source_of_truth: "コード + CI 定義"
updated: "2026-07"
---

# 10. 開発 — ビルド・反映・テスト・規約

[English](10-development.md) | 日本語

## 10.1 リポジトリ構成（責務のみ）

| ディレクトリ | 責務 |
|--------------|------|
| `console/` | ブラウザ SPA（React + Vite + zustand）。ビルド成果物 `console/dist` を CP が静的配信 |
| `control-plane/` | Control Plane（Go・単独モジュール）。migrations を埋め込み、起動時に自動適用 |
| `workspace/` | Workspace イメージ（Dockerfile / entrypoint）+ `workspace/agent/`（Agent・独立 Go モジュール）|
| `deploy/` | デプロイ層（local / compose / aws）。runbook は各 README（[09](09-deploy.ja.md)）|
| `e2e/` | フリート E2E（独立 Go モジュール・stdlib のみ）。CP + 実コンテナの疎通検証（§10.4）|
| `console-e2e/` | Console UI E2E（Playwright）。ブラウザ → CP → 実コンテナの縦串検証（§10.4）|

ファイル単位の地図は [90-code-map](90-code-map.ja.md)。

## 10.2 ビルドと反映の早見表

**要点: `docker run` は running 中のコンテナには no-op。新イメージの反映は必ず Stop→Start**
（Start は `rm -f` → 新イメージで `run` ＝確実に入れ替わる）。ホーム（ログイン・接続・repos）は
bind mount で永続し、イメージ更新の影響を受けない。

| 変更したもの | 反映に必要な操作 |
|--------------|------------------|
| Console（`console/src`）| `vite build`（watch 可）→ ブラウザ**リロードのみ**（CP は dist を no-store 配信・CP 再起動不要）|
| CP の Go | CP を再ビルドして再起動（`restart-cp.sh`）。イメージ再ビルド不要 |
| Agent の Go / イメージ焼き込み | イメージ再ビルド → 稼働中 Workspace は**利用者が Console で Stop→Start**（CP からの強制入替はしない）|
| 焼き込み CLI（claude / opencode / codex）の版 | **§10.2.1 の runbook に従う**（ARG bump → push → CI 検証 → run-dev.sh → Stop→Start）|
| rtk の版 | CLI 3 種と同じ**イメージ焼き込み・ARG ピン止め**（`workspace/Dockerfile` の `RTK_VERSION`。常時焼き込み `BAKE_RTK=1` 既定）。bump は ARG 書き換え → 再ビルド → Stop→Start。最新への追従は自己更新 opt-in（`AF_AGENT_SELF_UPDATE`）でも可 — entrypoint が CLI 群と一緒に rtk も latest へ更新する（`~/.local/bin` shadow・OFF に戻すと焼き込み版へ復帰）。旧ホスト vendoring（`update-rtk.sh` → `vendor/rtk`）は廃止 |
| entrypoint が適用する類（設定 seed・TZ 等）| Stop→Start のみ（再ビルド不要）|
| 共有 JVM | 共有 dir を消して再 provision（`deploy/local/provision-jvm.sh`）|

### 10.2.1 焼き込みツールの版上げ runbook（定型運用）

CLI 3 種（claude / opencode / codex）・gh・Go の版を上げるときは、この手順どおりに進める。
背景: 版未指定の `npm install -g` は Docker レイヤキャッシュに当たり「再ビルドしても
上がらない」罠があったため ARG ピン化した経緯（[04 §4.9](04-agent.ja.md)）。

1. **latest 確認**
   - CLI 3 種: `npm view @anthropic-ai/claude-code version`（`opencode-ai` / `@openai/codex` も同様）
   - gh: [cli/cli の releases](https://github.com/cli/cli/releases) の最新
   - Go: `workspace/agent/go.mod` の `go` ディレクティブと**歩調を合わせる**（go.mod を
     上げないなら据え置く）。rtk: [rtk-ai/rtk の releases](https://github.com/rtk-ai/rtk/releases)
     の最新（`ARG RTK_VERSION`。他 CLI と同じ焼き込みピン）。
2. **ARG bump**: `workspace/Dockerfile` の `ARG CLAUDE_CODE_VERSION` / `OPENCODE_VERSION` /
   `CODEX_VERSION`（/ `GH_VERSION` / `GO_VERSION`）を書き換える。ARG 変更が確実に
   キャッシュを破るので `--no-cache` は不要。
3. **commit & push**: 1 行 diff・日本語メッセージ
   （例 `build(workspace): 焼き込み CLI を bump（claude X.Y.Z）`）。
4. **CI green を確認**: push で `.github/workflows/e2e.yml` が発火し、イメージ build →
   L1（**実版 = ピンの一致**・versions.json）→ L2（フリート疎通）→ L3（Console UI）を
   自動検証する。`gh run watch $(gh run list --workflow e2e --limit 1 --json databaseId --jq '.[0].databaseId')`
   で完走を見届ける。red のまま先へ進まない。
5. **（大きめの版上げのみ）実クレデンシャルを使うジョブ**: 枠の消費先が違うので
   ワークフローも入力も**エージェント毎に別**。回したい物だけ立てる（既定は全て false）。
   - claude のメジャー更新や認証・API まわりが疑わしいとき（L4 疎通スモーク）:
     `gh workflow run e2e -f live=true`。secret `E2E_CLAUDE_OAUTH_TOKEN`
     （`claude setup-token` で発行、Max/Pro 枠・追加課金なし）使用。失効時（トークン
     再発行時）は setup-token をやり直して secret を更新する。
     ※ これは headless の `claude -p` ＝ TUI もフッタも描画されない。状態検出の破壊は
     ここでは見えない（それは `gh workflow run claude-tui-contract` の仕事）。
   - codex の版上げで状態検出（Stop hook / rollout / turn 通知）が疑わしいとき（Tier2
     ドリフト検知）: `gh workflow run codex-contract -f live=true`。secret
     `E2E_CODEX_AUTH_JSON` 使用・ChatGPT サブスク枠を実測 ~45k tokens/回 消費する。
     （Tier1＝無料・無認証の方は codex 関連の push で自動的に走る。）
6. **ホスト反映**: ホストで `deploy/local/run-dev.sh`。イメージ再ビルド直後に
   `e2e-smoke.sh`（L1）が自動で走り、版一致を再検証する（rtk 同梱もここで確認される）。
   ⚠️ ホストはメモリ制約 — 重いビルドを並走させない（[HANDOFF §2](../HANDOFF.md)）。
7. **Workspace 反映**: 各利用者が Console で **Stop→Start**（home は永続・repos は残る。
   CP からの強制入替はしない）。
8. **反映確認（任意）**: 再起動後のコンテナ内で
   `EXPECT_… bash deploy/local/e2e-smoke.sh --inner`、または Console の
   設定 → 環境「ツールのバージョン」（実効 / イメージ / ピン差分が見える）。

補足:
- **週次 cron**（e2e.yml、月曜 6:00 JST）がコード無変更でも上流 CLI / base image の破壊を
  検出する。cron が red になったら上流変更起因を疑い、この runbook の 4〜5 で切り分ける。
- 再ビルドせず特定メンバーだけ最新化したい場合は自己更新 opt-in
  （AdminTab の `allow_agent_self_update` ＋ 設定 → 環境のトグル。Stop→Start で焼き込み版に戻る）。

## 10.3 起動スクリプトの責務（`deploy/local/`）

- **`run-dev.sh`** — 一括起動の**単一エントリポイント（サブコマンド式）**:
  Workspace 実行環境の準備 → Console build → CP build → CP をホストプロセスで起動。
  git-ignored の `deploy/local/oauth.env` を自動 source し、AUTH / OAuth / 暗号系 env を CP に渡す
  （無ければ dev 素起動。項目は [oauth.env.example](../../deploy/local/oauth.env.example)）。
  | サブコマンド | 動き |
  |---|---|
  | （無指定）/ `local` | 開発既定。Docker ランタイム |
  | `wsl` | WSL 個人利用プリセット（docker/cgroup preflight・`AUTH=dev` 固定）。旧 `wsl-quickstart.sh` はこれを exec する後方互換ラッパー |
  | `native` | Docker なしコンテナレス（`AF_RUNTIME=native`・単一ユーザー・[ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)）。agent をホストビルドして渡す |
  | `reset [--all] [--yes]` | ローカルデータ初期化。既定は dev ユーザーのみ（DB・共有 JDK 温存）、`--all` で `WS_DATA` 全体。CP 稼働中は拒否し、docker/native 両方の残骸（コンテナ・agent プロセス・専用 tmux）を掃除してから消す |

  サブコマンド無しのときは env `AF_RUNTIME` で後方互換分岐（`native|wsl` → コンテナレス）。
  ※ env の `AF_RUNTIME=wsl` は「コンテナレス」の別名で、サブコマンド `wsl`（Docker プリセット）
  とは別物。紛れるのでサブコマンド指定を推奨。
- **`restart-cp.sh`** — 軽量反映: Console + CP だけ再ビルドし、稼働中の CP プロセスをその場で入れ替えて
  `/healthz` まで検証。**Workspace イメージは再ビルドしない**。`SKIP_CONSOLE=1` で Go のみ。
  env は oauth.env + run-dev.sh と同じ `WS_*` 既定を再現する。
- **`e2e-smoke.sh`** — イメージスモーク（L1）: ビルド済みイメージ内の CLI 実版が Dockerfile の
  ARG ピンと一致するか（＝キャッシュ staleness の検出）、焼き込み一式（agent / entrypoint /
  CLAUDE.md / rtk 等）の存在を `docker run` で検証。run-dev.sh がビルド直後に自動実行
  （`WS_SMOKE=0` でスキップ）。単体でも `deploy/local/e2e-smoke.sh [image]` で実行可。

ホスト固有の作法（PATH・docker グループ等）は HANDOFF §2 の領分で、ここには書かない。

## 10.4 テスト

Go は **2 モジュール**（`control-plane/` と `workspace/agent/`）でそれぞれ回す:

```bash
(cd control-plane && go test ./...)
(cd workspace/agent && go test ./...)
```

- CP 側は `httptest` ベースのスモークを多数含む（audit / egress / 内部 git smart-HTTP / LFS /
  store 両実装など）。Postgres 系は `AF_TEST_DATABASE_URL` 未設定なら skip。
- ⚠️ **マイグレーションを足したときは、実 Postgres でも 1 度回すこと。** skip される 3 本
  （`TestPostgresStore` / `TestPostgresDeleteCascade` / **`TestSchemaDialectParity`**）が
  「片方の系列にだけ足した」を捕まえる唯一の場所で、CI は `AF_TEST_DATABASE_URL` を持たない
  （[06 §6.4](06-data.ja.md)）。Docker が要らない立て方（初回のみ数分）:

```bash
PGT=~/.local/share/af-pgtest    # 無ければ initdb -U postgres --auth=trust で作る
# ★ TCP ポートではなく unix socket で上げる（-h '' で TCP を閉じる）。開発ホストを
#   他のセッションと共有していると、ポートは高確率で衝突する。
nohup "$PGT/dist/bin/postgres" -D "$PGT/data" -k "$PGT/sock" -h '' \
  -c shared_buffers=32MB -c fsync=off > "$PGT/pg.log" 2>&1 &
(cd control-plane && \
  AF_TEST_DATABASE_URL="postgres://postgres@/postgres?host=$PGT/sock&sslmode=disable" \
  go test -run 'TestPostgres|TestSchemaDialectParity' ./...)
"$PGT/dist/bin/pg_ctl" -D "$PGT/data" stop -m fast   # 使い終わったら止める
```
- CI（GitHub Actions）: `ci.yml` が push/PR ごとに 3 コンポーネント（CP / Agent / Console）の
  fmt・vet・test・build を検証。`e2e.yml`（下記 E2E）はイメージ build が重いため分離。
  上流 CLI の破壊検知は別系統（`cli-drift.yml` + エージェント毎の `*-contract.yml`・後述）。
- Console:

```bash
npm --prefix console test                                      # vitest run（layout エンジン ops ほか純関数）
NODE_OPTIONS=--max-old-space-size=3072 npm --prefix console run build
```

本番 build は Node ヒープを上げないと OOM しうる（メモリ制約ホストでの一般指針は Workspace 配布の
workspace-notes を参照）。gofmt + `go vet` clean・`npm run build` clean が提出前の基準
（[CONTRIBUTING](../../CONTRIBUTING.md)）。

> **コミット前に gofmt を必ず回す（ハードゲート）**。`ci.yml` は各 Go モジュールで
> `gofmt -l .` を実行し、未整形ファイルが 1 つでもあれば fail する。`go build`/`go vet`/
> `go test` が通ることは不十分（エディタの自動整形が `_test.go` を取りこぼす事例が実際に
> すり抜けた）。触れた各モジュールで `gofmt -l .` が**何も出力しない**ことを確認し、出たら
> `gofmt -w <file>` で直す:
> ```bash
> (cd control-plane && gofmt -l .)
> (cd workspace/agent && gofmt -l .)
> ```

### E2E（イメージスモーク + フリート疎通 + UI + 実 API）

4 層構成。**L1 = `deploy/local/e2e-smoke.sh`**（§10.3、イメージ焼き込み内容の検証・数秒）、
**L2 = `e2e/`**（独立 Go モジュール・stdlib のみ）、**L3 = `console-e2e/`**（Playwright）、
**L4 = `e2e/live_test.go`**（実 API キー・手動のみ）。

- **L2**: CP をヘッドレス（AUTH=dev）で起動し、公開 API だけで workspace 起動 →
  **shell セッション**作成 → input（echo 打鍵）→ fs API で読み戻し → 停止、を実コンテナで
  検証する。kind=shell なので **LLM クレデンシャル不要**。
- **L3**: 実ブラウザで Console を開き、セッションを開いて xterm へ打鍵 → 効果を fs API で
  観測（xterm は canvas 描画で DOM から文字が読めないため）。CP・コンテナの起動は
  global-setup が行う（L2 の Node 版。DEV_USER=e2e-ui で分離）。
- **L4**: shell セッション内で `claude -p`（headless、TUI オンボーディング非依存）を実行し、
  焼き込み CLI が実際に Anthropic と会話できることを確認。**課金/サブスク枠を伴うため自動
  トリガに載せない** — `E2E_ANTHROPIC_API_KEY`（API キー・従量課金）か
  `E2E_CLAUDE_OAUTH_TOKEN`（`claude setup-token` の OAuth トークン・Max/Pro 枠）の
  どちらかがある時だけ動く。

```bash
cd e2e && WS_IMAGE=agent-fleet/workspace:dev go test -v -tags e2e -timeout 15m   # L2（+L4 は key があれば）
cd console-e2e && npm ci && npx playwright test                                  # L3（console/dist 要ビルド）
```

- 前提（docker + build 済みイメージ、L3 は console/dist も）が無ければ skip
  （CI は `E2E_REQUIRE=1` で fail に格上げ）。
- 実フリートが動く dev ホストでも安全: テスト毎に DEV_USER を分離（e2e / e2e-ui / e2e-live）・
  ポートは動的確保・teardown 内蔵（コンテナ / ネットワーク / 一時データ）。
  ただしメモリ制約ホストなので同時 1 実行。
- CI は `.github/workflows/e2e.yml`: workspace/CP/console/e2e の変更時 + **週次 cron**（コード
  無変更でも上流 CLI・base image の破壊を検出）+ 手動。`e2e` ジョブ（L1→L2）と `ui-e2e` ジョブ
  （L3、失敗時は trace/CP ログを artifact 保存）が並列、`live-smoke`（L4）は workflow_dispatch の
  `live` 入力 + secret（`E2E_ANTHROPIC_API_KEY` または `E2E_CLAUDE_OAUTH_TOKEN`）で明示 opt-in。
  rtk は git-ignored のホスト vendor 品のため CI は「rtk なし」パスの検証になる（rtk 込みは
  ホストで run-dev.sh / 本テストを回して確認）。

### 上流 CLI の破壊検知（版ドリフト監視 + contract テスト）

**なぜ E2E だけでは足りないか**: `e2e.yml` は build-args を渡さないので常に
`workspace/Dockerfile` の **ARG ピン版**を検証する。一方 self-update を opt-in した
Workspace（`AF_AGENT_SELF_UPDATE_ALLOWED=1` かつ `AF_AGENT_SELF_UPDATE=1`）は entrypoint が
起動毎に `@latest` を入れる ＝ **CI が見ている版と実フリートが走らせる版が別物**。さらに
L4 は headless の `claude -p` で TUI もフッタも描画されない。この 2 つの穴のせいで、claude の
状態検出（`internal/tmuxx`）の破壊は 3 回とも CI 緑のまま実フリートで人力発見された。

塞ぎ方は **2 系統**（重複ではなく補完関係。片方だけでは機能しない）:

| | `cli-drift.yml` | `*-contract.yml` |
|---|---|---|
| 見る物 | 版**番号**のズレ（ピン vs 公開 latest） | **挙動**（実 CLI に当てて壊れたか） |
| 答える問い | 「見に行くべき時か？」 | 「実際に壊れたか？」 |
| 費用 | 無料（`npm view` だけ） | 無料〜サブスク枠（Tier で違う） |
| 頻度 | 毎日 cron | 関連パスの push + 週次 cron |
| 赤くなる時 | 検査自体の失敗のみ（ドリフトは issue upsert） | 契約が破れた時 |

ドリフトは**常態**（claude は数日で版が進む）なので `cli-drift.yml` は赤くせず追跡 issue を
1 本 upsert する（解消で自動 close）。対象はミラー対応の7 CLI（claude / codex /
opencode / copilot / agy / cursor / kiro）で、npm・GitHub Releases・各社 manifest を
それぞれの公開版の正本として読む。

`cli-release-watch.yml` は毎日この公開版を前回処理版と比較し、**版が変わった CLI だけ**
contract を dispatch する。状態は専用 issue `CLI release watcher state` の追記型コメント
（`cli-release-state tested|seen <cli>=<version>`）に保存する。repository variables は
`GITHUB_TOKEN` から書けず 403 になるため使わない。contract 成功時だけ `tested` を
追記するので、失敗時は翌日も再試行する。7 CLI 全てに contract ワークフローがあり
（copilot は GH OAuth token で実ターン、cursor / kiro も実ターン契約、agy はターン無しの
pane probe）、release edge で無人 dispatch されるのは credential を安定供給できる
claude / codex / opencode / copilot / agy（secret 未設定なら `seen` に落ちる）。
cursor / kiro は refresh で回転する対話 credential のため自動 dispatch せず `seen` を
記録し、secret 更新後に専用ワークフローを手動 dispatch する
（「検出済み」と「テスト成功」を混同しない）。

**ワークフローはエージェント毎に 1 ファイル**（`claude-tui-contract.yml` /
`codex-contract.yml` / `opencode-contract.yml` / `copilot-contract.yml` /
`agy-contract.yml` / `cursor-contract.yml` / `kiro-contract.yml`）。パス条件も `workflow_dispatch` の入力も
**ワークフロー単位**なので、`e2e.yml` に同居させると (1) 無関係な変更で走り、(2) 入力が
他エージェントと混ざる（実際 codex の Tier2 と claude の live-smoke が 1 つの `live` 入力を
共有し、1 回の dispatch で両方の枠が減っていた）。分離すればこの結合が構造的に起きない。
`cli-drift.yml` と `cli-release-watch.yml` だけは7 CLI横断・毎日実行という性質が違うので
エージェント別 contract から独立させる。同じく横断の例外が `mcp-config-contract.yml`
（認証・課金不要）: MCP レジストリの materialize が書く各 CLI グローバル設定の**形**を、
CLI 自身の `mcp add` に書かせた設定との構造比較（`mcp add` を持たない cursor と
ヘッダを表現できない codex は CLI に af の書いたファイルを読み返させる）で検証する——
検証対象がレジストリ側の 1 契約で複数 CLI に同時に跨がるため 1 本にまとめている（docs/48 §8）。

このほか `release-gate.yml` がパッケージング（docs/35）の検証を担う: 配布物のビルド・
lean variant の boot-install・既定（全焼き込み）ビルド・native の実 bwrap 起動・
ECS リリース手順の静的検査・dist の stub publish/install を hosted runner の実イメージ
ビルドで確認する。dev ホストでの重ビルドはフリートを OOM させ得るため常設トリガを持たず、
ワークフロー自身を変更する push と `workflow_dispatch` でのみ走る。

共通セットアップ（Go / Node / tmux / 実 CLI 導入）は
**`.github/actions/setup-agent-cli`**（composite action）に集約。`version: pinned|latest|<版>`
で「今焼く版」と「フリートが走らせる版」を撃ち分ける。`claude-tui-contract.yml` だけは
実イメージを build してコンテナ内で TUI を起動するため、この action を使わない（意図的）。

> 既知の非対称（未整理）: build tag が codex=`drift`/`driftlive`、opencode=`clicontract`、
> claude=`tui_contract` と 3 流儀。揃えるならテスト側の tag と `-run` の対応も動くため、
> ワークフロー整理とは別で扱う。

### 公開リポジトリでの CI の前提（秘密情報の扱い）

このリポジトリは公開を前提にしている。**secrets そのものはリポジトリ本体に存在せず**
（リポジトリ設定に暗号化保管）、公開しても露出しない。加えて次を満たすように保つ:

- **fork PR には secrets を渡さない。** `pull_request` トリガは fork からの実行に
  secrets を渡さない仕様なので、これに依存する。**`pull_request_target` と
  `workflow_run` は使わない** — どちらも「fork 側が書いたコードに secrets 付きで
  実行権を与える」典型的な穴。self-hosted runner も使わない。
- **実認証を使うジョブは `workflow_dispatch`（＋`inputs.live`）限定にする。**
  `codex-contract` の `live-drift`、`e2e` の `live-smoke`、各 `*-contract` がこれ。
  PR では起動しないので、fork PR が secrets 不在で赤くなることもない。
- **`run:` に `${{ github.event.* }}` を展開しない**（PR タイトル等からのシェル注入）。
- **秘密はファイルへ書くだけにし、標準出力へ出さない。** 例: kiro の認証 DB は
  8 分割 base64 を `printf | base64 --decode > <file>` で復元する（出力しない）。
- **artifact と run ログは公開物として扱う。** 公開リポジトリでは誰でも
  ダウンロードできる。ログ中の secrets は完全一致でマスクされるが、**そこから
  派生した値はマスクされない** — `claude-tui-contract` の観測フレームは
  ログイン済み TUI の画面なので、アップロード前にアカウント名とメールを伏字化する
  ステップを挟んでいる（同種の値は [tmuxx のゴールデン](../../workspace/agent/internal/tmuxx/testdata/footers/)
  でも伏字化済み）。
- **`permissions:` は全ワークフローで明示する**（既定は read だが、既定が変わっても
  最小権限が残るように）。

**資格情報の混入検知（`ci.yml` の `secret-scan`）。** 公開リポジトリでは混入＝即公開で
取り返しがつかないので、差分ではなく**毎回全履歴**を走査する（gitleaks・2436 commit /
64MB で約 12 秒）。押さえどころ:

- **`--log-opts` に `-m` を必ず付ける。** これが無いと gitleaks は merge commit を
  飛ばし、**衝突解決で入った内容が未走査のまま緑になる**（本リポジトリでは該当する
  merge が 138 件あり、初回スキャンで実際に穴が空いていた）。
- gitleaks 本体は**版と sha256 を固定**して取得する（marketplace action に依存しない）。
- 偽陽性は `.gitleaks.toml` で**値そのものを正規表現で**外す。**パスまるごとの除外はしない**
  — そのファイルに本物が入っても気づけなくなる。現在の登録は伏字化テストの偽キー
  （`AKIAQWERTYUIOPASDFGH`・`xoxb-123456789012-…` 等）と、`discord-client-secret` ルールに
  形が似ているだけのエラーコード定数 1 件。
- 初回の全履歴監査（2026-08-01）は**本物の資格情報ゼロ**。到達可能な全 blob 10,222 個を
  展開して走査する方法でも裏を取った（`git log` 経由では拾えない内容を潰すため）。

## 10.5 コミット規約・ブランチ運用

[CONTRIBUTING](../../CONTRIBUTING.md#commits--prs) が正（形式・帰属トレーラの詳細はそちら）。要点:

- 小さく焦点の合ったコミット。形式は `<type>(<scope>): 要約`（Conventional Commits）で
  **subject も body も日本語**（英語で書き始めたら書き直す）。
- エージェントのコミットは末尾に**実行モデル名**の `Co-Authored-By:` を付ける（Claude/Codex/
  opencode 併用のため CLI 名でなくモデルで帰属）。旧 `Claude-Session:` 行は廃止。
- **秘密をコミットしない**: `deploy/compose/.env`・`deploy/local/oauth.env`・`allowed-emails.txt` は
  git-ignored。コミット前に diff を確認。
- **コアを deploy 非依存に保つ**: Docker/compose 前提を CP コアに焼き込まず、ポート
  （Runtime / KeyCustodian / Store / AuthGateway）の背後へ（[09 §9.2](09-deploy.ja.md)）。
- migration 追加時は前方互換を確認し（起動時自動適用・ダウングレード非対応）、コミットに明記。
- 検証方法（テスト + 挙動変更は実機での確認）を書き残す。

## 10.6 ドキュメント更新責務

何を変えたらどの dev/ ファイルを更新するかは [dev/README の早見表](README.ja.md)。
