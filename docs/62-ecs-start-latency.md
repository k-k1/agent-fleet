# 62. ECS の Workspace 起動レイテンシ — SOCI 遅延ロードの採否

> 状態: 調査（2026-08-14）＋ **P0.5 実装済**（504 の解消・§62.5.1）＋ **P0 計測 完了**（2026-08-15・§62.9）。
> **判定は「P1 へ進まない」**——実測した pull は判定ゲートの両条件を落とした（**35.0s < 40s** かつ
> **総収束の 22% < 40%**）。SOCI の技術的前提は満たしたままだが、**縮める価値のある区間ではなかった**。
> ⚠️ **§62.9.4 で「支配項」と書いた「Start API → タスク作成の 40〜143s」は、計測手順が作った数字だった**
> ——同日の追試（§62.10）で、原因は「前タスクを止めた直後に再 Start したこと」と判明。間隔を空ければ
> **4s** で、実運用の温ホーム再 Start は **~101s**（pull 35s ＝ **35%**）。**SOCI の判定は変わらない。**
> **索引生成のツールチェーンは開発 Workspace で実測済み**（§62.4.1）——起票時に書いた
> 「Docker が無いのでここでは実検証できない」は**誤りだった**。`crane` + `soci --standalone` で
> root も Docker も containerd も無しに index manifest v2 まで生成できる。
> 意思決定: **ADR は起票しない**（P1 に進まないため。0044 は欠番にせず次の議題に回す）
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

### イメージサイズ（2026-08-15 実測・推定を置換）

`af-workspace:0.8.0`（GHCR の配布イメージ）のマニフェストを直接読んだ値:

| 項目 | 実測 |
|---|---|
| compressed 合計 | **962,872,701 B = 918 MiB** |
| 層数 | **34** |
| 最大の層 | **318 MiB** |

**推定していた「0.9GB 前後」は当たっていた**（`0.7.0` は 902MiB で、版が上がっても形は変わらない）。
variant も probe の中から実物で確認した——`/usr/bin/chromium` `/usr/local/go` `/usr/local/aws-cli`
がいずれも存在する＝**`BAKE_AGENT_CLIS=0` かつ `BAKE_OPTIONAL_TOOLS=1`** で、§62.3 が前提にしていた
形そのもの。⚠️ `deploy/release/build.sh` に `BAKE_OPTIONAL_TOOLS=0` を渡す行があるが、あれは
**native の rootfs 用イメージ専用**で、コンテナ像は Dockerfile 既定の `1` のまま。読み違えやすい。

なお **ECR へ入れるのに docker は要らなかった**。GHCR にある配布イメージを
`crane copy ghcr.io/k-k1/agent-fleet/{workspace,control-plane}:0.8.0 → <acct>.dkr.ecr…/af-*:dev`
でレジストリ間コピーすれば、ダイジェストも層構成も保ったまま ECR に載る（§62.4.1 で未検証だった
「crane が ECR に対して素直に振る舞うか」も、これで push 方向の確認が取れた）。

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

### 数字（実測で置換・2026-08-15）

> 以下は**当時の見積り**。実測は §62.9 で、**pull は仮定の 50〜60s ではなく 27〜40s だった**。
> ~~AWS 公称 40〜60%（>750MB のイメージで 60% 超の事例）。pull が仮に 50〜60s なら
> **25〜35s 短縮＝体感 100s → 65〜75s**。~~
> 「実測前に数字を約束しない」を守った結果、**約束しなくて正解だった**側に転んだ:
> 公称 40〜60% を実測 pull 35.0s に掛けても **14〜21s** で、しかもそれは総収束 ~160s の 1 割弱。
> (b) の「~100s の相当部分が pull ではない可能性」は**当たり**で、想定より更に踏み込んで
> **pull でも boot-install でもない区間**が最大項だった（§62.9.4）。

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
| **(b) イメージ縮小** | pull は劇的に短縮（手段は既存: `BAKE_OPTIONAL_TOOLS=0`） | 初回だけ chromium ~1GB 等を **NAT 越え**（S3 GW エンドポイントの外＝$0.062/GB が復活）／EFS に workspace ごと ~1GB（$0.36/GB-月 × 人数）／初回 Start がさらに悪化／native lean 用の経路を ECS 本番が使うことになりテスト面が増える | 中 |
| **(c) UI で吸収** | 待ち時間は **1 秒も縮まない**。「エラーに見える」だけが消える | ほぼゼロ | 高 |
| **(d) EC2 起動タイプ** | 2 回目以降 pull ゼロ（最大） | **scale-to-zero の経済性が消える**。容量プロバイダ / ASG / ドレイン / AMI 更新が増え、「per-workspace は CP がステートレスに」という設計の芯を壊す。しかも**「1 台の VM」形は `deploy/aws/ec2-single` として既に存在する** | 低 |

**推奨: (c) を先に（ほぼ無料）、本命は (a)。(b) は保留、(d) は却下。**

> ⚠️ **2026-08-15 追記 — (d) の却下理由は実測で書き換わった（結論は維持）。**
> [docs/64](64-ec2-persistent-workspace.md) / [ADR 0045](decisions/0045-ec2-persistent-workspace.md) で
> **EC2 起動タイプ ＋ インスタンス stop** を sandbox で端から端まで実測した結果、上の表の理由のうち
> **成り立っていたのは「AMI 更新が増える」だけ**だった:
> - **「scale-to-zero の経済性が消える」は誤り。** 停止インスタンスは課金されず EBS 代だけになる。
>   月 160h・home 45 GiB の実価格で **EC2 $28.5 対 現状 $39.6**、完全アイドル月は **$7.7 対 $16.3** と
>   **EC2 の方が安い**（EFS $0.36/GB-月 が効く）。
> - **「容量プロバイダ / ASG / ドレイン」は不要だった。** `launchType=EC2` は素の登録済みインスタンスで
>   動き、配置は `memberOf attribute:af-membership` で足りる（ASG もキャパシティプロバイダも使っていない）。
> - **「CP がステートレスを壊す」も壊れない。** インスタンス・ボリューム・スナップショットはタグで、
>   配置は ECS 属性で引ける（ADR 0012 は維持できる）。ただし扱う資源の種類は 1 Workspace あたり 2 → 6 に増える。
> - **「`ec2-single` が既にある」は反論として無効。** あちらは全部入り 1 台で per-user 分離が無い。
>
> **それでも (d) は却下のまま**だが、理由は入れ替わる: **起動が速くならないから**である。
> stop→start→タスク RUNNING が **83.5s** で、§62.10.3 の温ホーム再 Start（同じ地点まで ~84s）と
> **差が無い**——pull で浮く 35 秒を、インスタンス起動 19s ＋ ECS 再登録 1s ＋ 配置 13s で使い切る。
> **この形（1 ユーザー = 1 インスタンス固定）では、本書の目的に対して EC2 起動タイプは効かない。**
>
> ⚠️ **ただし形を変えると効く。** 同日の第 2 ラウンド（docs/64 §64.12）で
> **汎用インスタンスのプール ＋ ユーザー毎 EBS の差し替え**を測ったところ、**Start は 22〜27s**
> （ホットスロット・pull 0.045s）だった。**~105s の 1/4 で、しかも永続 home を保てるのはこの形だけ。**
> したがって「EC2 では速くできない」と一般化してはならない —— (d) の却下は
> **「1 台をユーザーに固定する形なら効かない」**という意味に限る。
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

> **2026-08-15 追記（最終）: 不採用。** 以下の「条件付き採用」は計測前の結論で、条件＝判定ゲートは
> §62.9 の実測で**落ちた**。SOCI の技術的前提は今も全部満たしているが、**pull は総収束の 22%
> （35.0s / ~160s）しかなく、40〜60% 削っても体感は 14〜21s しか動かない**。同じ労力を
> §62.9.4 の最大項（スケジューラ反応 40〜143s）に向けるほうが桁が大きい。

~~**条件付き採用**~~（当時の結論）。SOCI の技術的前提はこの構成が**すべて満たしており**
（変更ゼロで発火する）、イメージ形状も効く側。ただし **~100s のうち pull が何秒かを誰も測って
いない**ので、25〜35s という見積りは仮定の上に乗っている。**計測を先行ゲートにする**。

→ **このゲートの置き方自体は正しかった**。仮定のまま P1 に入っていれば、`release-ecr.sh` に
版ピン 2 本と変換手順を足し、リリース手順を恒久的に複雑にしたうえで、体感 160s → 140s 程度の
改善しか得られなかった。

## 62.7 実装計画

### P0 — 計測（コード変更なし）✅ 実施済（2026-08-15・結果は §62.9）

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

### P1 — SOCI 導入 ❌ **着手しない**（P0 の判定ゲートで落ちた・§62.9.3）

以下は**実施しない**手順として残す（前提が変われば復活しうるため）。復活の条件は 1 つ、
**pull が総収束の 40% 以上かつ 40s 以上になること**——例えば §62.9.4 のスケジューラ反応が
解消されて総収束が 60s 台まで落ちれば、35s の pull は一気に過半になり、そこで再評価する。

6. ~~**最初の 1 手は `crane push` の確認**~~ → **P0 のついでに済んだ**。GHCR → ECR の
   `crane copy` が通り、ダイジェスト・層構成を保ったまま ECR に載ることを確認した（§62.3）。
   残る未検証は「**SOCI アーティファクトを含む**マニフェストが ECR で保たれるか」だけ。
7. ~~`release-ecr.sh` に `--soci` を追加~~（既定 off・版ピン＋sha256）。
8. ~~workspace イメージのみ変換、CP は素通し~~。`--prefetch-file` のチューニング。
9. ~~`deploy/aws/ecs/README.md` に手順追記~~。

### P2 — 検証 ❌ 同上（P1 に入らないので不要）

10. ~~L1 probe の `IMG` を変換済みタグに差し替えて回す~~。**ハーネスは動くことを確認済み**——
    probe のログに `"Snapshotter":"overlayfs"` が出た（＝変換後に `soci` へ変わるかを見る
    A/B の型は、そのまま使える状態で残っている）。
11. ~~L2 の (B) も再実行して差分を取る~~。
12. ~~効果が出なければ索引なしで再 push して即ロールバック~~。

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

## 62.9 P0 の実測（2026-08-15・sandbox で deploy → 計測 → teardown）

### 62.9.1 計測環境

- AWS sandbox（`ap-northeast-1`）に `00-network` → `10-data` → `20-platform` → `30-ingress` を
  deploy し、計測後に全て `delete-stack`（§62.9.6）。**ECS 構成の「実運用実績なし」は崩していない。**
- イメージは **GHCR の配布ビルド `0.8.0` を `crane copy` で ECR へ複製**（`af-workspace:dev` /
  `af-control-plane:dev`）。開発 Workspace に docker は無いが、**レジストリ間コピーに docker は
  要らない**——§62.4.1 で「索引生成に docker は要らない」と分かったのと同じ理屈。
- 認証は `AuthMode=dev`、ALB の SG は計測元の /32 に限定（README の注意書きどおり）。
- 計測は `aws` CLI（§62.7.1 の判断どおり。AWS MCP の書き込みトグルは開けていない）。

### 62.9.2 L1 — pull の孤立計測（probe タスク・3 回）

`af-ws-pullprobe`（entrypoint を潰した使い捨て task def）で同じイメージを 3 回引いた。

| run | provision | **pull** | start | created→started |
|---|---|---|---|---|
| 1 | 9.8s | **27.5s** | 3.0s | 40.4s |
| 2 | 11.5s | **27.3s** | 3.8s | 42.6s |
| 3 | 9.5s | **32.4s** | 2.0s | 43.9s |
| **平均** | **10.3s** | **29.1s** | **2.9s** | **42.3s** |

- **918 MiB を 29.1s ＝ 約 31 MiB/s**。S3 ゲートウェイエンドポイント経由（§62.1）でこの値。
- task metadata v4 に **`"Snapshotter":"overlayfs"`** — SOCI 前のベースラインとして期待どおりで、
  §62.7 が「P2 の A/B ハーネスをそのまま兼ねる」と書いた仕掛けが**実際に動くことも確認できた**。

### 62.9.3 L2 — 実ワークスペースの端から端

**(A) 新規ホームの初回 Start**（EFS access point が空）:

| 区間 | 実測 |
|---|---|
| Start API → task 作成 | ~0.5s |
| provision | 14.2s |
| pull | 27.7s |
| pull 完了 → task started | 36.1s |
| entrypoint（boot-install 込み） | ~48s |
| **総収束（Start API → Agent 応答）** | **126.5s** |

boot-install の内訳はログどおり **4CLI で 41s、rtk +1s、agy +6s ＝ 48s**
（`entrypoint.sh` のコメントが言う「約 60s」と同じ桁。**SOCI では 1 秒も縮まない**区間）。

**(B) Stop → 再 Start（温ホーム・3 回）** — 判定に使うのはこちら:

| run | Start API→task 作成 | provision | **pull** | pull→started | 総収束 |
|---|---|---|---|---|---|
| B1 | ~40s（導出） | 16.6s | **39.5s** | 26.5s | 135.4s |
| B2 | 143.2s | 18.9s | **27.9s** | 24.8s | 217.8s |
| B3 | 52.4s | 13.0s | **37.5s** | 25.3s | 128.5s |
| **平均** | **~79s** | **16.2s** | **35.0s** | **25.5s** | **160.6s** |

温ホームであることはログで確認済み（`boot-install: npm CLIs already present in ~/.local (skip)`
以下 4 行）。entrypoint は **21s** で Agent 待受まで到達＝(A) との差 ~27s が boot-install 分。

**判定ゲート（§62.7）の当てはめ**:

| 条件 | 実測 | 判定 |
|---|---|---|
| (B) の pull が **絶対値 40s 以上** | 35.0s（最大でも 39.5s） | ❌ |
| (B) の pull が **総収束の 40% 以上** | 13〜29%（平均 **22%**） | ❌ |

**AND 条件の両方を落とした → P1 には進まない。**

### 62.9.4 最大項は §62.7 のモデルに無かった 2 区間

(B) の ~160s を割ると、**イメージ取得は 35s（22%）だけ**で、残りはこう分かれる:

1. **Start API → ECS がタスクを作るまで 40〜143s（平均 ~79s ＝ 総収束の 30〜66%）**。
   これがこの 3 サンプルでの最大項。CP 側の遅れでないことは CloudTrail で確定した（§62.10.1）。
   ⚠️ **ただしこの区間は「計測が停止直後に再 Start していた」ことの産物で、間隔を空けると 4s になる**
   （§62.10.3）。**実運用の値としては使わないこと。**
2. **pull 完了 → task started が実ワークスペースでは 24.8〜26.5s**（probe は 2.0〜3.8s）。
   差の ~22s は **EFS ボリューム／access point のマウントとコンテナ作成**。これも SOCI の射程外。

⚠️ **1 のばらつき（40s と 143s）はここでは未解明とした。→ §62.10 で決着**:
「同一サービスを短時間に上げ下げしたことが原因」という当時の但し書きが**当たり**で、
**scale-to-zero で毎回払う形ではなかった**（本番は停止から再開まで数分〜数時間空く）。

### 62.9.5 P0.5（504 の解消）は検証できていない

**ECR に載せた CP は GHCR の `0.8.0` で、§62.5.1 の修正（`watchReady` の背景化）より前の版。**
そのため 3 回とも Start API は **60.05〜60.25s で 504** になり、CP ログに
`ecs start: … Agent not ready within 1m30s (agent health wait canceled: context canceled)` が出た。

これは**旧挙動の実測再現**であり、§62.5.1 の診断（90s 同期待ち > ALB idle 60s）が実機でそのとおり
であることの裏付けにはなる。ただし **修正後の挙動（Start が 60s 未満で `starting` を返す・
`Agent healthy <n>s after Start` のログ）は未確認のまま**。確認には P0.5 を含む CP イメージ
（`0.8.1` 以降）を ECR に載せた再走が要る。**§62.5.1 の「実 ECS では未検証」は据え置き。**

なお **Console 側が耐える設計であること**は実測で確かめられた——504 が返っても
`GET /api/workspace` のポーリングは 126〜218s の cold start を最後まで歩き切り、
`running` に到達した（§62.5.1 が「呼び出し側は既に耐えている」と読んだとおり）。

### 62.9.6 後始末（初回計測分）

per-workspace リソース（CFN の外）を先に掃除してから 4 スタックを削除した。**README §Teardown に
書かれていない落とし穴が 1 つ**: `10-data` の出力キーは **`EfsId`**（`FileSystemId` ではない）で、
**EFS access point を消し損ねると `af-ecs-data` の削除が止まる**。掃除の順は
ECS Service → task definition（`af-ws*`）→ **EFS access point** → SSM（`/af-ws/*` `/af-cp/*`）
→ ロググループ → スタック（ingress → platform → data → network）。

## 62.10 「Start API → タスク作成」の切り分け（2026-08-15・同日追試）

§62.9.4 で最大項に見えた **40〜143s** を、極小イメージ(busybox)で pull を消し、
**サービス操作の型とタスクの形だけを変えて**測り直した。すべて `00-network` ＋ `20-platform`
（＋ EFS 条件のみ `10-data`）で回せる。測るのは **API 呼び出し → task の `createdAt`** だけ。

### 62.10.1 まず CloudTrail で CP と ECS を切り分けた

§62.9.4 は「CP は即 desired 1 にしている」とログから読んだが、**それは推測だった**（引いたログ行は
待ちの後に出るもので、`UpdateService` の時刻を示さない）。CloudTrail の `eventTime` で確定させた:

| 実測 | Start API を叩いた時刻 | CP の `UpdateService` | task `createdAt` |
|---|---|---|---|
| B3 | 00:18:59 | **00:19:01**（+2s） | 00:19:51（**+50s**） |
| B2 | 00:14:44 | **00:14:45**（+1s） | 00:17:07（**+142s**） |

CP の前段（EFS access point 2 本・SSM 2 本・`RegisterTaskDefinition`）は**どれも待ちを持たない
API 呼び出し**で、実際 1〜2 秒で `UpdateService` に到達している。**遅いのは ECS 側**——ここは確定。

### 62.10.2 統制実験（14 回）——どの条件でも再現しなかった

| 条件 | 何を変えたか | 実測（各回） |
|---|---|---|
| a | `UpdateService` で **`desiredCount` のみ**（task def 据え置き） | 7.7 / 7.5 / 11.0s |
| **b** | **新リビジョン＋`ForceNewDeployment`**（CP の再 Start と同型） | 9.1 / 6.3 / 7.8 / 7.3 / 4.7 / 5.3s |
| d | b と同型だが **Service Connect 無し** | 8.6 / 3.2 / 6.3s |
| e | b と同型で **1024/2048**（実 ws と同じサイズ） | 6.2 / 7.7 / 7.3s |
| f | e ＋ **EFS ボリューム 2 本＋SSM secrets**（実 ws と同じ形） | 6.4 / 3.7 / 10.9s |
| **g** | f と同型で **イメージだけ実 workspace(918MiB)** | 7.8 / 6.2 / 8.0s |
| c | **`CreateService`**（毎回まっさらな作成） | 52.3s（**有効サンプル 1 回のみ**・下記） |

**API の型でも、タスクのサイズでも、EFS ボリュームでも、secrets でも、Service Connect でも、
イメージサイズでもない。** `desiredCount 0→1` は**通常 4〜11s**で、§62.9 の 50〜142s は
**どの条件でも再現しなかった**。

⚠️ **条件 c の 2 回の「300s 超」は計測側の不良**だった（`list-task-definitions --max-items 1` の
ページング混入で task def ARN が壊れ、`create-service` 自体が実行されていなかった。CloudTrail に
作成イベントが無く、サービスも `MISSING`）。**「CreateService は遅い」と一度書いたが取り下げる**——
残る有効サンプルは 1 回の 52.3s だけで、結論の根拠にはならない。
教訓: **失敗しうるコマンドの stderr を握り潰したまま「遅い」と読んではいけない**
（`grep` でフィルタした表示に、エラー行が最初から出ていなかった）。

### 62.10.3 正体は「前タスクの後片付けと次の配置が重なること」

統制実験で唯一再現しなかった条件を潰していったら、**変数は API でもタスクの形でもなく
「前のタスクを止めてから次を start するまでの間隔」**だった。

| 条件（いずれも実 ws のタスク定義・SC 有・1024/2048） | Start API → task 作成 |
|---|---|
| 停止直後（1〜2 分）に再 Start ＝ **今回の計測ループがやっていたこと** | **40〜143s** |
| **十分な間隔（5 分）を空けて再 Start** | **4.0s** |
| busybox（前タスクが数秒で消えるので重なりようがない） | 4〜11s |

実 ws イメージは pull だけで ~35s かかるので、`desiredCount 0` にした直後の再 Start は
**前タスクがまだ PROVISIONING/PENDING を引きずっている最中**に次のデプロイを重ねることになる。
ECS はその 1 サイクルを捌くまで次のタスクを作らない。実際、遅い回の直前には ECS 自身が
こう言っていた:

```
(service af-ws-dev) was unable to stop or start tasks during a deployment because of the
service deployment configuration. Update the minimumHealthyPercent or maximumPercent value…
```

⚠️ ただし **`deploymentConfiguration` を `minimumHealthyPercent=0,maximumPercent=100` にしても
解消しなかった**（45.4 / 8.0 / 73.8s）。メッセージが指すノブそのものは効かない。
効くのは間隔で、**本番の使われ方（reaper が止め、ユーザーが数分〜数時間後に起こす）では
この重なりは起きない**。

**したがって §62.9.4 の「最大項は Start API → タスク作成の 40〜143s」は、計測手順が作り出した
数字だった。** 実運用の温ホーム再 Start はこうなる:

| 区間 | 実測 | 効く手 |
|---|---|---|
| Start API → task 作成 | **~4〜8s**（間隔を空けた場合） | — |
| provision（ENI attach） | 16s | 無し（Fargate の下限） |
| **pull** | **35s** | SOCI / イメージ縮小 |
| pull→started（EFS マウント・コンテナ作成） | 25s | EFS ボリュームの持ち方 |
| entrypoint（温） | 21s | Agent の待受を前倒し |
| **計** | **~105s** | |

これは README が元々書いていた **「~100s」とよく一致する**。**SOCI の判定は変わらない**——
pull 35s は ~101s の **35%** で 40% ゲートに届かず、絶対値も 40s 未満。ただし
**「pull は総収束の 22%」という §62.9 の書き方は、計測手順が作った 79s を分母に入れた分だけ
SOCI を不当に小さく見せていた**ので、**35%** に読み替えること（判定は変わらないが、境界には近い）。

### 62.10.4 計測の教訓

- **「A/B で条件を変えて再現しない」ときは、条件表に無い変数を疑う。** ここでは 14 回の統制実験が
  すべて速く、実 CP だけが遅かった。差は測り方（stop 直後に start）にあり、**計測ループ自身が
  被測定系を汚していた**。
- **停止→起動を繰り返すベンチマークは、間隔を条件として明示すること。** 間隔を書かない
  「再 Start は N 秒」は再現しない。
- **ECS のサービスイベントを最初から取ること**（`describe-services` の `events[]`）。
  ECS は理由を文章で言う。今回もそこに手掛かりがあった（ただし指されたノブは効かなかった）。

### 62.10.5 副産物: Start が 2 回走っている疑い

間隔を空けた計測（B6）で、**1 回の Start に対してタスクが 2 つ作られた** —
`02:14:40` に 1 個目、`02:15:20` に 2 個目、1 個目は `02:16:54` に停止。CP へ投げた Start は 1 回。
`ensureWorkspaceStartedRTLocked` は Start 以外のハンドラからも呼ばれるので、**`starting` の窓の中で
2 本目の `ForceNewDeployment` が飛んでいる可能性**がある。飛んでいれば cold pull を最初から
やり直すことになり、体感を 1 分近く悪化させる。**未確認の観測**として記録する（次に見るべき筋）。
