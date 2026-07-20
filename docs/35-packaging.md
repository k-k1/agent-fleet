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
| B | `agent-fleet-images-<v>.tar.gz` | `docker save`（CP + Workspace イメージ、air-gap 用） | amd64 Linux・EC2-Single（・ECS も可） |
| C | `agent-fleet-native-<v>-linux-amd64.tar.gz`（arm64 も同時生成） | `bin/af-cp`・`bin/workspace-agent`（静的）・`console/`（Vite dist）・`docs/`（ステージング源）・`af` ランチャ・README | native（WSL / 任意の Linux 単一ユーザー） |
| D | `SHA256SUMS` | A〜C のチェックサム | 全部 |

命名・版の規律:

- イメージタグ = `VERSION`（現行 release.sh の流儀を維持）。ECR へは同じタグで push（§35.5.4）。
- バイナリへ `-ldflags "-X main.buildVersion=<v>"` で刻印し、起動ログと `/healthz`（または
  `/api/version`）で確認できるようにする（§35.6.1）。障害報告に「どの版か」を必ず添えられるようにするのが目的。
- ダウングレード非対応（migration 前方のみ）は全ターゲット共通の前提（docs/dev/09 §9.7）。

## 35.3 ターゲット別設計

### 35.3.1 native パッケージ（WSL 主対象・Docker 不要・単一ユーザー）

**ゴール**: 「Go も Node も git clone も不要。tar を展開して `./af start`、ブラウザで
`http://localhost:8099`」。docs/34 の native Runtime（実装済）に、欠けている「配る形」を与える。

**中身**（アーティファクト C）:

```
agent-fleet-native-<v>-linux-amd64/
├── af                    # ランチャ（下記）
├── bin/af-cp             # CGO_ENABLED=0 静的ビルド（console/dist は含まない）
├── bin/workspace-agent   # 同上（AF_NATIVE_AGENT_BIN でCPへ渡す）
├── console/              # Vite ビルド済み dist（CONSOLE_DIR で指す）
├── docs/                 # CP イメージが焼くのと同じ docs ツリー（stageWorkspaceDocs の源）
└── README.md             # WSL 前提の導入・制約（docs/34 §34.5 の要約）＋更新手順
```

- **console は同梱ディレクトリ、go:embed はしない**。単一バイナリ化は魅力だが、どのみち
  workspace-agent・docs・ランチャで tar は必要になり、embed の利得（ファイル1個）は消える。
  一方 embed は「console を直したら CP を再ビルド」の結合を生む。`CONSOLE_DIR` seam の現状維持が
  コード変更ゼロで筋がよい。（将来单一バイナリが本当に欲しくなったら build tag で opt-in にする。）
- **docs/ を同梱する理由**: native では CP イメージが無いので `bakedDocsDefault`
  （`/usr/local/share/agent-fleet/docs`）が存在しない。`AF_DOCS_DIR` をパッケージ内 `docs/` に
  向ければ、role-scoped ステージング（workspace_docs.go）がそのまま生きる。同梱しなければ
  ワークスペース内エージェントが環境ドキュメントを引けなくなる（機能的には best-effort なので
  起動は通るが、体験が落ちる）。単一ユーザー（AUTH=dev = super_admin 相当）なので
  内部 docs を含めても閲覧権限の問題はない。ただし tar サイズと「内部設計文書を配布物に含めるか」
  は §35.9 の未決事項。
- **ビルド**: `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build -trimpath -ldflags "-s -w -X main.buildVersion=<v>"`。
  両 arch とも静的なのでクロスビルドは追加コストほぼゼロ → **amd64/arm64 を最初から両方出す**
  （WSL on ARM・Apple Silicon 上の Linux VM を潰しておく。chromium 等の焼き込みが無い native は
  arch 依存物がバイナリ2個だけなので、ここで出し惜しみする理由がない）。

**ランチャ `af`**（`run-dev.sh native` からビルド工程を除いた移植）:

| サブコマンド | 動作 |
|---|---|
| `af start` | preflight（tmux/git/claude の存在 warn — `run-dev.sh` L249-251 と同じ）→ `AF_RUNTIME=native` / `AF_NATIVE_AGENT_BIN=<pkg>/bin/workspace-agent` / `CONSOLE_DIR=<pkg>/console` / `AF_DOCS_DIR=<pkg>/docs` / `WS_DATA=~/.local/share/agent-fleet`（既定）で `bin/af-cp` を exec |
| `af reset [--all]` | `run-dev.sh` の `do_reset`（native 部分）を移植: agent プロセスグループ停止 → `tmux -L af-ws-dev kill-server` → データ削除 |
| `af status` | pid ファイル + `/proc` 照合で CP / workspace の生存表示（任意・後回し可） |

- **bind 先は既定 `127.0.0.1:8099` にする**（run-dev.sh の `:8099` 全 IF bind より狭める）。
  配布物の既定は安全側が正しい。WSL2 の localhostForwarding は loopback で機能するので
  Windows 側ブラウザからの利用は変わらない。`CP_ADDR` env で上書き可。
- oauth.env の読み込み（GitHub device flow の client_id 等）も run-dev.sh と同じ流儀で
  `<pkg>/oauth.env`（あれば）を読む。**AUTH は dev 固定**（native factory が dev 以外を
  fail-fast する — docs/34 §34.5 — のでランチャ側でも上書きさせない）。
- systemd user unit の見本を README に載せる（WSL2 は systemd 既定有効）。ランチャ本体は
  フォアグラウンド実行のまま（tmux/nohup/systemd はユーザーの選択に委ねる）。

**ホスト依存（パッケージが肩代わりしないもの）**: tmux・git・各エージェント CLI
（claude/codex/opencode）・任意で chromium。docs/34 §34.5 の割り切りそのまま。README と
preflight の warn で明示する。**Go / Node が要らなくなるのが本パッケージの成果**。

**アップデート**: 新 tar を展開して差し替え → `af start`。データ（`WS_DATA`）は外に
あるので触れない。migration は CP 起動時自動・前方のみ（更新前バックアップ推奨を README に明記）。

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
- `--native` = §35.3.1 の C を amd64/arm64 分ビルド。
- 仕上げに `SHA256SUMS`（D）を dist 直下へ。
- ECR push（release-ecr.sh）は**顧客環境側の操作**なのでオーケストレータに含めない
  （バンドル A の `aws/ecs/` に同梱して現地で叩く）。

## 35.7 実装フェーズ

| フェーズ | 内容 | 出口 |
|---|---|---|
| **P1: 共通基盤** | 版刻印（§35.6.1）・release.sh へ `aws/` 同梱 + SHA256SUMS・`deploy/release/build.sh` 骨格 | `VERSION=x build.sh --compose` で A+B+D が出る |
| **P2: native tar** | ビルダ（--native）・ランチャ `af`・README-native（WSL 導入/制約/更新） | tar 展開 → `af start` → Console 表示 →（Docker なしホストで）セッション起動 |
| **P3: ECS 配布** | release-ecr.sh・ImageTag/Persistence パラメータ化・更新 runbook・最小 IAM 表 | sandbox で「push → deploy → WS 起動 → タグ更新 → 次回 Start で新イメージ」一巡 |
| **P4: 検証ゲート** | §35.8 の未済分（native 実機が筆頭） | 各ゲート緑 + 第 2 デプロイ再現 |

P1→P2 が本丸（native が唯一のゼロ→イチ）。P3 は独立に並行可。

## 35.8 検証ゲート（P3-10 完了判定への接続）

| ターゲット | ゲート | 状態 |
|---|---|---|
| compose | clean host でバンドルから起動（P3-10 段5） | ✅ 実証済（ec2-single 上） |
| EC2-Single | 同上 + CFN provision〜teardown | ✅ 実証済 |
| native | **素の WSL2**（Docker なし）で tar から起動 → clone → claude セッション E2E | ✗ 未（docs/34 §34.6 の実機未検証と同時に消化する） |
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
