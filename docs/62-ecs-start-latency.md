# 62. ECS の Workspace 起動レイテンシ — SOCI 遅延ロードの採否

> 状態: 調査（2026-08-14）＋ **P0.5 実装済**（504 の解消・§62.5.1）。SOCI 本体は **条件付き採用**＝
> 「~100s の内訳を実測してから SOCI（Seekable OCI）遅延ロードへ進む」で、**P0 / P1 は未着手**。
> 計測（§62.7 P0）と 504 の解消（§62.7 P0.5）は SOCI の採否と独立。
> **索引生成のツールチェーンは開発 Workspace で実測済み**（§62.4.1）——起票時に書いた
> 「Docker が無いのでここでは実検証できない」は**誤りだった**。`crane` + `soci --standalone` で
> root も Docker も containerd も無しに index manifest v2 まで生成できる。
> 意思決定: **ADR は未起票**（§62.7 P1 に進む決定をした時点で 0044 として起こす）
> 関連: [dev/09-deploy.md §9.5](dev/09-deploy.md)（aws ターゲットの縮約） /
> [deploy/aws/ecs/README.md](../deploy/aws/ecs/README.md)（ランブック・"Known behavior: first Start may 504"） /
> [35-packaging.md](35-packaging.md) §35.4.1（`BAKE_AGENT_CLIS` / `BAKE_OPTIONAL_TOOLS` の配布ノブ）
> 対象: `deploy/aws/ecs/release-ecr.sh`（索引生成の置き場）/ `control-plane/runtime_ecs.go`（Start の待ち）/
> `deploy/aws/ecs/cfn/30-ingress.yaml`（ALB idle timeout・`ImageTag`）/ `workspace/entrypoint.sh`（起動パス）
> 前提: ECS 構成は 🚧 **実運用実績なし**（sandbox で deploy→E2E→teardown のみ）。本書の提案は
> その前提を崩さない範囲に限る。

## 62.1 問題

1 Workspace = 1 ECS Service で、Stop = `desiredCount` 0 / Start = 1。**Fargate はタスク間で
イメージキャッシュを持たない**ため、Start のたびに workspace イメージのフル pull が走る。結果:

- 初回 Start は ALB の 60s idle timeout を超えて **HTTP 504**、収束まで **実測 ~100s**
  （`deploy/aws/ecs/README.md` の "Known behavior"）。
- CP は裏で provisioning を続けるので機能的には収束する（Console は `GET /api/workspace` を
  ポーリングして `starting` を出す）が、体感は悪い。
- [dev/09-deploy.md §9.5](dev/09-deploy.md) が書くとおり、`starting` 状態が実質 ECS 専用なのは
  この構造のため。**scale-to-zero の代償を起動レイテンシとして毎回払っている**。

帯域とデータ処理料の話は片付いている: ECR のレイヤ実体（S3）は S3 ゲートウェイエンドポイント
（`cfn/00-network.yaml`）で NAT を経由しない。**残るのは純粋な pull 時間**。

## 62.2 SOCI の前提条件（一次情報での確認結果）

出典は ECS 開発者ガイド ["Lazy loading container images using Seekable OCI (SOCI)"](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/fargate-tasks-services.html)
の Considerations（2026-08-14 に取得して突合）。

| 要件 | この構成の実態 | 判定 |
|------|----------------|------|
| Linux platform version **1.4.0 のみ**（Windows 非対応） | `CreateService` に `PlatformVersion` 未指定 → LATEST = 1.4.0（`runtime_ecs.go`） | ✅ 変更不要 |
| **X86_64 / ARM64 とも対応** | `RegisterTaskDefinition` に `RuntimePlatform` 未指定 → 既定 X86_64。イメージも amd64 | ✅ |
| ECR **private** レジストリのみ | `af-workspace`（`20-platform` 所有） | ✅ |
| **gzip または無圧縮のみ。zstd 非対応** | `docker build` の既定 = gzip | ✅ ⚠️ buildx で zstd 出力に変えると黙って壊れる |
| task definition のフラグ不要・Fargate が自動検出 | — | ✅ **CFN も Go も変更不要** |
| 索引の無いイメージは**コンテナ単位で**フル pull にフォールバック | Service Connect のサイドカーが混ざっても可 | ✅ |
| 250MiB(compressed) 超を推奨 | ws イメージは推定 **0.9〜1.0GB compressed**（§62.3） | ✅ 効く側 |

**調査前に持っていた前提のうち、一次情報と食い違った 3 点**（記録価値あり）:

1. **arm64 は対象外ではない** — X86_64 / ARM64 の両方が対象。arm64 移行は SOCI とは独立の議論。
2. **「索引を同じリポジトリに artifact として push する」は v1 の話** — 現行ドキュメントは
   「**新規に SOCI を使う顧客は index manifest v2 しか使えない**」と明記する（既存 v1 利用者だけが
   継続可、ただし移行を強く推奨）。v2 では索引がイメージ側に annotation で結び付くので、
   `soci create` + `soci push` ではなく **`soci convert`（CLI ≥ 0.10.0）＋通常の push** になる。
   **新規に v1 索引を付けても Fargate は lazy load せずフル pull に落ちる**。
3. **「タスク内の全イメージに索引が必要」は古い**（2023-11 以降は混在可）。AWS の
   "Under the hood" ブログには旧記述が残るので、それを根拠にしない。

さらに **v2 固有の性質が 2 つ、パイプラインに直撃する**:

- v2 索引の生成は **image manifest に annotation を足すので digest が変わる**。既に push 済みなら
  **repush が必要**（レイヤ実体は同一なので ECR ストレージは増えない、と明記あり）。
- 索引生成には **イメージが containerd image store に居ること**が必要で、**Docker image store に
  あると見つからない**。本リポジトリは `docker build`（＝Docker store）なので、そのままでは通らない。
  → `soci convert --standalone`（OCI layout 入出力。containerd も sudo も daemon も不要）で回避する。

## 62.3 効果の見積り

### 起動パスが読むもの / 読まないもの

`workspace/entrypoint.sh`（693 行）は全体が `exec "$@"` の前の同期処理。

- **読む**: bash / coreutils、node（`node -e` で `versions.json` を何度もパース）、npm（`npm ls -g`）、
  curl、git、tmux、`workspace-agent`（static Go）
- **読まない**: chromium + `fonts-noto-cjk`（500MB 超）、Go toolchain（~600MB）、
  awscli v2 + SSM plugin（~350MB）、uv tool の MCP venv 3 本、build-essential / python3-dev の大半

**イメージのバイト数の 6〜7 割は起動パスに現れない**。AWS の一般論（「pull が起動時間の 76%、
うち起動に必要なのは平均 6.4%」）に素直に当てはまる形状で、SOCI が効く側。

### イメージサイズの根拠（推定・未実測）

ECS 用 ws イメージは `deploy/compose/release.sh` 経由なので **lean（`BAKE_AGENT_CLIS=0`）だが
`BAKE_OPTIONAL_TOOLS=1`**（chromium / Go / awscli は焼かれている）。[35-packaging.md](35-packaging.md)
に残る実測は「images 1.04GB（CP+WS の tar.gz）」と「lean rootfs 展開 915MB（こちらは
`BAKE_OPTIONAL_TOOLS=0`）」。ここから ws の compressed は 0.9GB 前後と推定。**開発ワークスペースには
Docker が無く実測していない**（推定値として扱うこと）。

### 効果を削る要因（3 つ・ここが判断の肝）

- **(a) entrypoint がメタデータ多発型**。lean variant の REPIN 経路は毎起動 `npm ls -g` を走らせ、
  npm 自身は数千の小ファイル。AWS 自身が「ファイルシステムメタデータへの頻繁なアクセスは
  効果が薄い」と書く形そのもの。span walk のオーバーヘッドが乗る。
- **(b) ~100s の相当部分が pull ではない可能性**。ECS の ws イメージは lean なので、**新規ホームの
  初回起動は entrypoint が npm / GitHub から CLI を取りに行く**。コード内の実測コメント
  （`entrypoint.sh` の自己更新ブロック）が「4CLI cold で 35s、agy 15s、cursor 6s ＝ 全部で約 60s」。
  **これは NAT 越えの純ネットワーク時間で、SOCI では 1 秒も縮まない。**
  ただし `~/.local` は EFS home に永続するので 2 回目以降は無音スキップ。
  **scale-to-zero で毎回払っているのは pull のほう**で、そこは SOCI の射程内。
- **(c)** 背景でフル pull が続くので、起動直後に chromium やビルドを叩けば結局待つ
  （待ちが後ろへずれるだけ。とはいえ体感は改善）。

### 数字

AWS 公称 40〜60%（>750MB のイメージで 60% 超の事例）。pull が仮に 50〜60s なら
**25〜35s 短縮＝体感 100s → 65〜75s**。(a) の分を差し引く必要があり、**実測前に数字を約束しない**。

## 62.4 パイプラインへの組み込み

**置き場所は `deploy/aws/ecs/release-ecr.sh` の `--soci` オプトイン**。ここが「ローカル docker →
ECR」の唯一の関門で、かつ ECS 専用。`deploy/release/build.sh` 側に入れると GHCR 配布物・compose・
native という無関係な成果物にまで soci 依存が波及する。

必要ツールは 2 本。どちらも**単一の静的 Go バイナリ**で、本リポジトリの流儀（版ピン＋sha256）で
`~/.local/bin` へ入る。**root も Docker も containerd も要らない**（§62.4.1 で実測）:

- **`soci` CLI** — `awslabs/soci-snapshotter` の release tarball（現行 **v0.15.0**・2026-07-30）。
  資産名は `soci-snapshotter-<ver>-linux-{amd64,arm64}[-static].tar.gz` と `.tar.gz.sha256sum`
  （`.sha256` ではない）。中身は `soci` / `soci-snapshotter-grpc` / ライセンス。v2 の `convert` には ≥0.10.0。
- **`crane`** — `google/go-containerregistry`（現行 **v0.21.9**）。レジストリ ↔ OCI layout の橋渡し。
  **一次情報の例は `skopeo` を使っているが、こちらでは採らない** — skopeo は apt 依存で
  root 無しの Workspace に入らないのに対し、crane は tarball 1 個で入り、Go 製という点も揃う。

コマンド列（一次情報の standalone 例を crane に置き換えたもの）:

```bash
crane auth login "$ECR_HOST" -u AWS -p "$(aws ecr get-login-password)"
crane pull --format=oci "$ECR_HOST/af-workspace:$VERSION" ws-oci
soci convert --standalone --format oci-dir ws-oci ws-soci-oci
crane push ws-soci-oci "$ECR_HOST/af-workspace:$VERSION"
```

- `soci convert` の既定は**ホストプラットフォームのみ変換**（他アーキのマニフェストは素通し）。
  ws イメージは amd64 単一なので既定でよいが、意識するなら `--platform` / `--all-platforms`。
- **`--prefetch-file` が効きうる**。索引メタデータに「先読みするファイル」を書けるフラグで、
  §62.3 (a) の「entrypoint がメタデータ多発型なので効果が目減りする」懸念を**直接叩けるノブ**。
  起動パスが確実に読むもの（node / npm / git / `workspace-agent`）を候補にして P1 でチューニングする。
- `--min-layer-size` の既定は 10MiB。これ未満の層は zTOC を持たず遅延ロードされない（＝素で pull）。

- **CP イメージは対象外**（小さい・ローリングするだけ）。
- **タグ運用は「未変換を一度も push しない」の一択**。`:$VERSION-soci` という別タグは使えない —
  `30-ingress.yaml` の `ImageTag` は **CP と WS で共有**のパラメータ（`AF_ECS_WORKSPACE_IMAGE` は
  `${EcrWorkspaceUri}:${ImageTag}`）なので、片方だけサフィックスを付けられない。`--soci` 時は
  `docker push` を通さず skopeo に一本化し、同一タグへ変換済みだけを置く。
- ECR コスト増: レイヤ実体は共有なので manifest ＋ zTOC（数 MB オーダー）のみ。**AWS 課金の追加は実質ゼロ**。

**代替（不採用）**: `awslabs/cfn-ecr-aws-soci-index-builder`（ECR push を EventBridge で拾い Lambda が
索引生成。`SociIndexVersion=V2` パラメータあり）。`release-ecr.sh` は無改造で済むが、**「CFN は静的
基盤のみ・4 段」という現構成の芯に Lambda + EventBridge を足す**ことになり `20-platform` の性格が変わる。
かつ Lambda 側で ~1GB イメージが通るかの検証が別途要る。→ リリース手順側に寄せるほうがこの構成に合う。

### 62.4.1 索引生成は Workspace の中で完結する（2026-08-14 実測）

**起票時に書いた「この開発環境では実検証できない（Docker 無し）。実行は docker のあるリリース
ホストに委ねる」は誤りだった。** Docker は要らない。開発 Workspace で通しで動かして確認した:

```
soci  v0.15.0 (linux-amd64-static, sha256 検証) → ~/.local/bin/soci
crane v0.21.9 (go-containerregistry, sha256 検証) → ~/.local/bin/crane

crane pull --format=oci public.ecr.aws/docker/library/debian:12-slim src   # 20.7s
soci convert --standalone --format oci-dir src dst                          # 1.4s
  → layer sha256:039e6f9f... -> ztoc sha256:b52c942d...
```

出力 layout を検査して確認した点:

- 変換後の image index に **`artifactType: "application/vnd.amazon.soci.index.v2+json"`** の
  マニフェストが増える ＝ **Fargate が新規利用者に要求する index manifest v2 が生成できている**。
- amd64 のイメージマニフェストに **`com.amazon.soci.index-digest` アノテーション**が付く
  ＝ §62.2 の「digest が変わるので repush が要る」が実物で裏付けられた。
- `soci convert --help` に `--standalone`（"without containerd runtime"）が実在する。

**Docker が要らない理由**（＝ここで動く理由）: `soci convert --standalone` は OCI layout の
tar / ディレクトリを読んで書くだけで daemon に触らず、crane も HTTP でレジストリを叩くだけ。
逆に **Docker / podman は原理的にこの Workspace に置けない** — root も `sudo` も無く、rootless に
必要な `newuidmap` / `newgidmap` は **workspace イメージが setuid を全部剥がしてビルド時に
assert している**（`workspace/Dockerfile` の `find / -perm /6000 -exec chmod a-s` ＋ 事後 `test -z`）
ため存在しない。

**まだ未検証の 1 点**: `crane push`（OCI layout → ECR）が SOCI アーティファクトのマニフェストを
保ったまま上がるか。ECR が立っていない状態で試せていない。**P1 の最初の 1 手はこれの確認。**

**外部に残る依存はイメージのビルドだけ**（`docker build`）。ただし P1 が必要とするのは
「既に ECR にあるタグを引いて変換して戻す」ことなので、**ビルドは P1 の経路に入らない**。

## 62.5 代替案の比較

| | 効果 | コスト・複雑さ | 可逆性 |
|---|---|---|---|
| **(a) SOCI** | 定常 Start の pull -40〜60%（公称） | リリース手順に版ピン 2 本。**Dockerfile / CFN / Go は変更ゼロ**。AWS 課金増ほぼゼロ | **高**: 索引なしで再 push するだけで戻る（ドキュメント明記） |
| **(b) イメージ縮小** | pull は劇的に短縮（手段は既存: `BAKE_OPTIONAL_TOOLS=0`） | 初回だけ chromium ~1GB 等を **NAT 越え**（S3 GW エンドポイントの外＝$0.045/GB が復活）／EFS に workspace ごと ~1GB（$0.30/GB-月 × 人数）／初回 Start がさらに悪化／native lean 用の経路を ECS 本番が使うことになりテスト面が増える | 中 |
| **(c) UI で吸収** | 待ち時間は **1 秒も縮まない**。「エラーに見える」だけが消える | ほぼゼロ | 高 |
| **(d) EC2 起動タイプ** | 2 回目以降 pull ゼロ（最大） | **scale-to-zero の経済性が消える**。容量プロバイダ / ASG / ドレイン / AMI 更新が増え、「per-workspace は CP がステートレスに」という設計の芯を壊す。しかも**「1 台の VM」形は `deploy/aws/ec2-single` として既に存在する** | 低 |

**推奨: (c) を先に（ほぼ無料）、本命は (a)。(b) は保留、(d) は却下。**

- **(d) の却下理由が決定的**。ECS/Fargate 版の存在理由はタスク分離と従量課金であり、レイヤキャッシュの
  ために常時 EC2 を置くなら [ec2-single](../deploy/aws/ec2-single/) を使えばよい。選択肢は既に repo 内にある。
- **(b) は SOCI と排他ではなく、むしろ SOCI の効果を削る**（縮めた先が 250MiB 閾値に近づく）。
  ECS の本番運用実績が出て EFS コストが読めるまで保留。
- **(c) にはまだ塞げる穴が 1 つ残っている**（SOCI の採否と独立に効く。以下は実装前の記述）:
  `Start` が `waitReady` で
  最大 90s ブロックし（`AF_ECS_START_TIMEOUT_SEC` 既定 90）、`30-ingress.yaml` の ALB は
  `idle_timeout` 未設定＝**60s 既定**。**この 90 > 60 が 504 の直接原因**。Start を即時 return にして
  収束を `State()` に委ねるのが筋（ALB の idle を上げるのは対症療法）。→ **§62.5.1 で実装済**。

### 62.5.1 P0.5 の実装（504 の解消・2026-08-14）

**`Start` は `desiredCount 1` まででリターンし、healthz 待ちは背景ゴルーチン
（`runtime_ecs.go` の `watchReady`）へ出した。** 選択肢は「45s へ切り詰めて同期待ちを残す」か
「即時 return」の二択だったが、**後者しか意味を持たない**ことが読解で確定した:

- **同期待ちが成功する経路が存在しない。** `Start` は `running` と `starting` を手前で早期 return
  するので、`waitReady` に到達した時点で必ず**タスクをゼロから起動している**。Fargate はタスク間に
  イメージキャッシュを持たない（§62.1）から、そこから healthz までは常にフル pull ＋ entrypoint。
  旧コメントが待っていた「ウォームなイメージの通常ケース」は**この runtime には最初から無い**
  ——90s 待って必ずタイムアウトし、どのみち `nil` を返していた。45s に切り詰めるのは
  「無駄な待ちを 45s 残す」だけになる。
- **`starting` の意味は変わらない。** 収束を `State()` に委ねるのは `runtime.go` の Runtime 契約
  （「`starting` = launch が収束中。呼び出し側は再 Start もアイドル停止もしない」）そのままで、
  ECS 専用に例外を足していない。docker アダプタとの差（あちらは healthz 失敗で `Start` がエラー）は
  **元からある**もので、今回広がってはいない（ECS は P3-7 段5 finding A 以来 non-fatal）。
- **呼び出し側は既に耐えている。** Console の `start()` は **Start 応答の `state` を読んでおらず**、
  4s の `GET /api/workspace` ポーリングで cold start を `稼働中` まで歩かせる
  （`console/src/core/store/workspace.ts`・ゲートウェイタイムアウトで abort しても固着しない設計）。
  scheduler の wake も `ensureWorkspaceStarted` の後に自前の寛容な `awaitAgentReady` を持つ。
  → **Console 側の変更は不要**。
- **`AF_ECS_START_TIMEOUT_SEC` の意味が変わった**（既定 **90s → 300s**）。誰も待たない背景ポーリングの
  予算になったので、ALB idle より短くする理由が消え、逆に ~100s の収束より短いと毎回 false な
  「not ready」ログを出す。native アダプタの 300s と同じ理屈。
- 残した価値は**ログ 1 行**（`Agent healthy <n>s after Start`）。AWS 側のトレース無しで
  「Start から Agent 応答まで何秒か」をワークスペース単位で残せる＝ **P0 の計測の足しになる**。
- ついでに `30-ingress.yaml` の ALB に `idle_timeout.timeout_seconds: 60` を**明示**した
  （AWS 既定と同値＝挙動は不変）。「暗黙の 60s」が CP の全ハンドラの上限であることを、
  次に長い処理を書く人が見る場所に書いておくため。**idle を上げる対症療法は採っていない。**

**未検証**: 実 ECS では未確認（Docker も AWS 資格情報も無い開発環境）。検証は Go の単体テスト
（`runtime_ecs_test.go`: 即時 return / 背景予算 / 呼び出し元 ctx の cancel を跨ぐこと）まで。
実機では「Start の応答が 60s 未満で `starting` を返す」ことと、上記ログ行の出力を確認すること。

## 62.6 結論

**条件付き採用**。SOCI の技術的前提はこの構成が**すべて満たしており**（変更ゼロで発火する）、
イメージ形状も効く側。ただし **~100s のうち pull が何秒かを誰も測っていない**ので、
25〜35s という見積りは仮定の上に乗っている。**計測を先行ゲートにする**。

## 62.7 実装計画

### P0 — 計測（コード変更なし）

収束時間は `describe-tasks` の 4 つのタイムスタンプで 3 区間に割れる。これは AWS 自身が推す方法で、
開発者ガイドの "Task lifecycle logging" が「このタイムスタンプで**イメージ取得にどれだけ費やしたかを
評価し、イメージ縮小か SOCI を使うか判断できる**」と明記している。

| 区間 | 計算 | 何の時間か | 効く手 |
|---|---|---|---|
| provision | `pullStartedAt - createdAt` | スケジューリング・ENI attach・EFS mount 準備 | どれも効かない |
| **pull** | **`pullStoppedAt - pullStartedAt`** | **イメージ取得** | **SOCI (a) / 縮小 (b)** |
| boot | `startedAt` 以降 | entrypoint の同期処理＋Agent 起動 | boot-install の非同期化 |

**実行手段は `aws` CLI**（イメージに焼かれている）。AWS MCP でも原理的には可能だが向かない — §62.7.1。

#### L1 — pull だけを孤立させて測る（これを最初にやる）

実ワークスペースを起こすと EFS・Service Connect・entrypoint が混ざる。**entrypoint を潰した使い捨て
タスクで同じイメージを引く**のが precise で安い。Fargate はタスク間キャッシュを持たないので
**run-task は毎回必ずコールド**＝それ自体が再現条件になる。

`run-task --overrides` で上書きできるのは `command` だけで **`entryPoint` は上書きできない**。
`entrypoint.sh` は末尾で `exec "$@"` するので command だけ差し替えても entrypoint は丸ごと走る。
よって**専用の probe タスク定義を登録する**。

```bash
CL=af-af-ecs-platform                 # 20-platform の ClusterName（af-<stack名>）
SUBNETS=subnet-aaa,subnet-bbb         # 00-network の PrivateSubnets
SG=sg-ccc                             # 00-network の WsSgId
EXEC=<20-platform の ExecRoleArn>
IMG=<acct>.dkr.ecr.<rg>.amazonaws.com/af-workspace:<tag>
LG=/af/af-ecs-ingress/ws

cat > probe.json <<JSON
{ "family": "af-ws-pullprobe",
  "requiresCompatibilities": ["FARGATE"], "networkMode": "awsvpc",
  "cpu": "1024", "memory": "2048", "executionRoleArn": "$EXEC",
  "containerDefinitions": [{
    "name": "probe", "image": "$IMG", "essential": true,
    "entryPoint": ["/bin/sh","-c"],
    "command": ["curl -s \$ECS_CONTAINER_METADATA_URI_V4/task"],
    "logConfiguration": { "logDriver": "awslogs", "options": {
      "awslogs-group": "$LG", "awslogs-region": "<rg>", "awslogs-stream-prefix": "probe" }}
  }]}
JSON
aws ecs register-task-definition --cli-input-json file://probe.json

d(){ date -d "$1" +%s; }
for i in 1 2 3; do                    # ばらつきを見るので 3 回
  T=$(aws ecs run-task --cluster $CL --launch-type FARGATE --task-definition af-ws-pullprobe \
      --network-configuration "awsvpcConfiguration={subnets=[$SUBNETS],securityGroups=[$SG],assignPublicIp=DISABLED}" \
      --query 'tasks[0].taskArn' --output text)
  aws ecs wait tasks-stopped --cluster $CL --tasks $T
  J=$(aws ecs describe-tasks --cluster $CL --tasks $T --query 'tasks[0]' --output json)
  c=$(jq -r .createdAt <<<"$J"); ps=$(jq -r .pullStartedAt <<<"$J")
  pe=$(jq -r .pullStoppedAt <<<"$J"); st=$(jq -r .startedAt <<<"$J")
  echo "run$i  provision $(( $(d $ps)-$(d $c) ))s / pull $(( $(d $pe)-$(d $ps) ))s / start $(( $(d $st)-$(d $pe) ))s"
done
```

**この probe は P2 の A/B ハーネスをそのまま兼ねる**。`IMG` を SOCI 変換済みタグに差し替えて同じ
ループを回すだけで前後比較になり、`command` が task metadata v4 を叩いているのでログに
`"Snapshotter":"soci"` が出るかも同時に取れる。→ **P2 の「workspace 側にログを 1 行足す」変更は不要**。

L1 は `00`/`10`/`20` と ECR のイメージがあれば動く（`30-ingress` は不要）＝**最短で pull の絶対値が出る**。

#### L2 — 実ワークスペースの端から端（シナリオ 2 つ）

- **(A) 新規ホームの初回 Start**（EFS access point が空）＝ pull ＋ boot-install（~60s）
- **(B) Stop → 再 Start**（ホーム温）＝ pull のみ、boot-install は無音スキップ

**判定に使うのは (B)。** scale-to-zero で毎回払っているのは (B) で、SOCI の射程もそこ。(A) の ~100s を
ゲートにすると boot-install の分だけ pull の割合が薄まり、**SOCI を過小評価する**（§62.3 (b) の罠）。

```bash
SVC=af-ws-<userkey>                   # 既定テナントは af-ws-<key>、それ以外は af-ws-<slug>-<key>
T=$(aws ecs list-tasks --cluster $CL --service-name $SVC --query 'taskArns[0]' --output text)
# ↑ 同じ describe-tasks の差分計算をかける

aws logs tail $LG --since 15m --format short | grep '\[entrypoint\]'   # entrypoint の内訳
aws logs tail /af/af-ecs-ingress/cp --since 15m | grep 'Agent healthy' # §62.5.1 で足した 1 行
```

`[entrypoint] boot-install (pinned): …` → `boot-install ok` の 2 行の差が boot-install 時間。
**停止したタスクが describe できる時間には限りがある**（1 時間程度）ので、task ARN を先に控えること。

#### 判定ゲート

- **(B) の pull が総収束時間の 40% 以上、かつ絶対値 40s 以上** → P1 へ。公称 40〜60% が乗るので
  20〜35s の短縮が見込める。
- **(B) の pull が短く (A) との差が大きい** → 効くのは SOCI ではなく **boot-install の非同期化**
  （Agent を先に上げて CLI 導入は裏で走らせる）。workspace 側の別トラック。
- **provision が支配的** → どの案も効かない。ECS の構造的下限なので `starting` の見せ方 (c) で受ける。

### 62.7.1 計測の実行手段: `aws` CLI か AWS MCP か

フリートには 4 つ目の builtin 連携として **Agent Toolkit for AWS（AWS MCP Server）**があり
（[25-ops-monitoring.md](25-ops-monitoring.md)）、`call_aws` は AWS API 約 15,000 アクションを叩ける。
原理的には P0 も P1 もこれで実行できる。**が、この用途では `aws` CLI を使う。**

- **既定 `--read-only` では `call_aws` 自体が消える**（`awsMCPArgs`。read-only で残るのは
  `read_documentation` / `search_documentation` / `retrieve_skill` / `list_regions` /
  `get_regional_availability` / `get_tasks` の 6 個）。`describe-tasks` は読み取りだが、
  **読み取りだけのために「書き込みツール」トグルを ON にする必要がある** — そして ON にした瞬間
  `call_aws` と `run_script`（任意コード）がセッションに開く。**計測 1 回のために開ける権限として過大。**
- `aws` CLI はイメージに焼かれ、資格情報も既に通っている。**追加の権限拡大がゼロ**で、
  L1 のようなループ（run-task → wait → describe → 差分計算）はシェルのほうが素直。
- **MCP が向くのは別の場面**: `search_documentation` / `read_documentation` / `retrieve_skill` は
  **read-only の既定のまま**使えて、SOCI や Fargate の仕様確認を AWS 公式の一次情報として引ける。
  本書 §62.2 の裏取りに使う分にはこちらが適する（Web 検索よりドリフトに強い）。

### P0.5 — 504 の解消（P0 と並行・SOCI と独立）✅ 実装済

5. ~~`runtime_ecs.go` の `Start` を非同期化（即時 return、収束は `State()` に委ねる）。~~
   → **§62.5.1**。`watchReady` を背景ゴルーチンへ。暫定案（`AF_ECS_START_TIMEOUT_SEC` を 45s に
   切り詰めて同期待ちを残す）は**採らなかった**——ECS では同期待ちが成功する経路が無いため。
   実 ECS では未検証（単体テストまで）。

### P1 — SOCI 導入（`release-ecr.sh` のみ・**この Workspace で実行できる**・§62.4.1）

6. **最初の 1 手は `crane push` の確認**。ECR を立て、変換済み OCI layout を push して
   SOCI アーティファクトのマニフェストが保たれるかを見る（§62.4.1 の唯一の未検証点）。
   確認は `aws ecr batch-get-image` で `artifactType == application/vnd.amazon.soci.index.v2+json`。
7. `release-ecr.sh` に `--soci` を追加（既定 off）。`soci` v0.15.0 / `crane` v0.21.9 を版ピン＋sha256 で
   取得（`GH_VERSION` の流儀。soci の checksum 資産は `.tar.gz.sha256sum`）。
8. workspace イメージのみ変換、CP は素通し。`--soci` 時は `docker push` を通さず crane 一本化
   （＝未変換を絶対に push しない）。`--prefetch-file` のチューニングはここで詰める。
9. `deploy/aws/ecs/README.md` の §ECR push / §Upgrade に手順追記。

### P2 — 検証

10. P0 の L1 probe の `IMG` を変換済みタグに差し替えて回す。**workspace 側のコード変更は不要**
    （probe の `command` が task metadata v4 を叩き、ログの `"Snapshotter"` が `soci` か `overlayfs` かで判る）。
11. L2 の (B) も再実行して P0 と差分を取る。
12. 効果が出なければ索引なしで再 push して即ロールバック。

## 62.8 一次情報

- [Amazon ECS task definition differences for Fargate](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/fargate-tasks-services.html)
  — SOCI 節の Considerations（本書の要件表の出典）
- [soci-snapshotter CLI usage](https://github.com/awslabs/soci-snapshotter/blob/main/docs/cli-usage.md)
  — `soci convert` / `--standalone` / `--min-layer-size`（既定 10MiB）
- [Improving Amazon ECS deployment consistency with SOCI Index Manifest v2](https://aws.amazon.com/blogs/containers/improving-amazon-ecs-deployment-consistency-with-soci-index-manifest-v2/)
- [Under the hood: Lazy Loading Container Images with Seekable OCI and AWS Fargate](https://aws.amazon.com/blogs/containers/under-the-hood-lazy-loading-container-images-with-seekable-oci-and-aws-fargate/)
  — ⚠️ 「タスク内の全イメージに索引が必要」の旧記述が残る。根拠にしない
- [AWS Fargate Enables Faster Container Startup using Seekable OCI](https://aws.amazon.com/blogs/aws/aws-fargate-enables-faster-container-startup-using-seekable-oci/)
  — 129s → 60s 等の実測値
- [awslabs/cfn-ecr-aws-soci-index-builder](https://github.com/awslabs/cfn-ecr-aws-soci-index-builder)
- [aws-fargate-seekable-oci-toolbox / am-i-lazy](https://github.com/aws-samples/aws-fargate-seekable-oci-toolbox/blob/main/am-i-lazy/README.md)
  — task metadata v4 の `Snapshotter` フィールドでの検証
