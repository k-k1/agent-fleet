# 10. 開発 — ビルド・反映・テスト・規約

> 正: コード + CI 定義 / 主な更新トリガ: ビルド・テスト・反映手順の変更 / 最終確認: 2026-07

## 10.1 リポジトリ構成（責務のみ）

| ディレクトリ | 責務 |
|--------------|------|
| `console/` | ブラウザ SPA（React + Vite + zustand）。ビルド成果物 `console/dist` を CP が静的配信 |
| `control-plane/` | Control Plane（Go・単独モジュール）。migrations を埋め込み、起動時に自動適用 |
| `workspace/` | Workspace イメージ（Dockerfile / entrypoint）+ `workspace/agent/`（Agent・独立 Go モジュール）|
| `deploy/` | デプロイ層（local / compose / aws）。runbook は各 README（[09](09-deploy.md)）|

ファイル単位の地図は [90-code-map](90-code-map.md)。

## 10.2 ビルドと反映の早見表

**要点: `docker run` は running 中のコンテナには no-op。新イメージの反映は必ず Stop→Start**
（Start は `rm -f` → 新イメージで `run` ＝確実に入れ替わる）。ホーム（ログイン・接続・repos）は
bind mount で永続し、イメージ更新の影響を受けない。

| 変更したもの | 反映に必要な操作 |
|--------------|------------------|
| Console（`console/src`）| `vite build`（watch 可）→ ブラウザ**リロードのみ**（CP は dist を no-store 配信・CP 再起動不要）|
| CP の Go | CP を再ビルドして再起動（`restart-cp.sh`）。イメージ再ビルド不要 |
| Agent の Go / イメージ焼き込み | イメージ再ビルド → 稼働中 Workspace は**利用者が Console で Stop→Start**（CP からの強制入替はしない）|
| entrypoint が適用する類（設定 seed・TZ 等）| Stop→Start のみ（再ビルド不要）|
| 共有 JVM | 共有 dir を消して再 provision（`deploy/local/provision-jvm.sh`）|

## 10.3 起動スクリプトの責務（`deploy/local/`）

- **`run-dev.sh`** — 一括: Workspace イメージ build → Console build → CP build → CP をホストプロセスで
  起動。git-ignored の `deploy/local/oauth.env` を自動 source し、AUTH / OAuth / 暗号系 env を CP に渡す
  （無ければ dev 素起動。項目は [oauth.env.example](../../deploy/local/oauth.env.example)）。
- **`restart-cp.sh`** — 軽量反映: Console + CP だけ再ビルドし、稼働中の CP プロセスをその場で入れ替えて
  `/healthz` まで検証。**Workspace イメージは再ビルドしない**。`SKIP_CONSOLE=1` で Go のみ。
  env は oauth.env + run-dev.sh と同じ `WS_*` 既定を再現する。

ホスト固有の作法（PATH・docker グループ等）は HANDOFF §2 の領分で、ここには書かない。

## 10.4 テスト

Go は **2 モジュール**（`control-plane/` と `workspace/agent/`）でそれぞれ回す:

```bash
(cd control-plane && go test ./...)
(cd workspace/agent && go test ./...)
```

- CP 側は `httptest` ベースのスモークを多数含む（audit / egress / 内部 git smart-HTTP / LFS /
  store 両実装など）。Postgres 系は `AF_TEST_DATABASE_URL` 未設定なら skip。
- CI（GitHub Actions）は Go リファクタ側ブランチで整備中・未マージ（本ブランチに `.github/workflows/` は無い）。
- Console:

```bash
npm --prefix console test                                      # vitest run（layout エンジン ops ほか純関数）
NODE_OPTIONS=--max-old-space-size=3072 npm --prefix console run build
```

本番 build は Node ヒープを上げないと OOM しうる（メモリ制約ホストでの一般指針は Workspace 配布の
workspace-notes を参照）。gofmt + `go vet` clean・`npm run build` clean が提出前の基準
（[CONTRIBUTING](../../CONTRIBUTING.md)）。

## 10.5 コミット規約・ブランチ運用

[CONTRIBUTING](../../CONTRIBUTING.md) が正。要点:

- 小さく焦点の合ったコミット。メッセージは日本語で運用している（英語も可）。
- **秘密をコミットしない**: `deploy/compose/.env`・`deploy/local/oauth.env`・`allowed-emails.txt` は
  git-ignored。コミット前に diff を確認。
- **コアを deploy 非依存に保つ**: Docker/compose 前提を CP コアに焼き込まず、ポート
  （Runtime / KeyCustodian / Store / AuthGateway）の背後へ（[09 §9.2](09-deploy.md)）。
- migration 追加時は前方互換を確認し（起動時自動適用・ダウングレード非対応）、コミットに明記。
- 検証方法（テスト + 挙動変更は実機での確認）を書き残す。

## 10.6 ドキュメント更新責務

何を変えたらどの dev/ ファイルを更新するかは [dev/README の早見表](README.md)。
