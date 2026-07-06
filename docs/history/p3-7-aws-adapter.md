# 20b. P3-7 実装プラン — AWS デプロイ先アダプタ（ECS）

> 🗄 **実装記録** — 現状は [HANDOFF](../HANDOFF.md)、設計は [ロードマップ P3-7](../roadmap.md#p3-7-デプロイ先アダプタオンプレ-docker-既定--自社-aws-任意)、
> AWS 構成の具体像は [reference/aws](../reference/aws.md)、港の思想は [reference/portability](../reference/portability.md)。

[12 Phase 3](../roadmap.md) の P3-7。各社が**自社のデプロイ先を選ぶ**（オンプレ Docker 既定／自社 AWS 任意）。
**コアは無改修、周縁アダプタのみ差し替える**（ports & adapters, docs/09）。Workspace イメージと Agent は
両ターゲットで**同一物**。差分は CP が呼ぶ周縁アダプタだけに閉じる。

> **前提**: このリポジトリの検証ホストでは実 AWS を叩けない（[host-oom-fleet-risk] / no-RDRAND）。ゆえに
> **段1（シーム固め）はこのホストで `go build`/`vet`/`test` で完結**させ、実 AWS を要する段（ecsRuntime 本実装・
> EFS/RDS/ALB・IaC・E2E）は AWS アカウントを持つ環境で回す。**オンプレ compose で足りる社が多い**見込みゆえ、
> P3-7 は「希望する社向けの任意アダプタ」であり、既定パス（local）を一切壊さないことを最優先とする。

## 20b.1 ゴールと不変条件

- **Agent 契約は不変**（`/sessions`・`/repos`・`/connections`・`/healthz`）。ローカルと AWS で同一イメージ・同一挙動。
- **Runtime 港が実体差を吸収**: ローカルは host-published `127.0.0.1:port`、ECS は内部 DNS（Service Connect / awsvpc ENI）。
  `Endpoint()` がこの到達差を隠すので、handlers/manager/reaper/admin/mcp は backend-agnostic のまま。
- **既定 local を壊さない**: `AF_RUNTIME` 未設定 = `local`。ecs は明示 opt-in。unknown profile は**起動時に fail-fast**。
- **scale-to-zero はロジック共通**: P3-9 の二段構えの「第2段 WS 停止」は Runtime の `Stop`/`Start`/`State` に閉じており、
  ローカル `docker stop/start` と ECS `desiredCount 0/1` を同一 reaper が駆動する（Runtime が実体差を吸収）。

## 20b.2 港マッピング（local ↔ aws）

| 港 | `local`（既定・実装済）| `aws`（P3-7）| 備考 |
|----|----------------------|--------------|------|
| **Runtime** | `dockerRuntime`（docker CLI）| `ecsRuntime`（ECS Service desired 0/1）| §20b.3 |
| Volume | bind mount `<dataRoot>/<...>/home` | EFS アクセスポイント（per-membership）| home = コンテナ `~` |
| MetadataStore | SQLite（modernc, 埋め込み）| RDS(Postgres) 単一 or 同居 | `Store` IF は既に Postgres 差し替え前提（store.go:13）|
| KeyCustodian | localCustodian（ファイル KEK）| KMS | 同 `KeyCustodian{Wrap,Unwrap}` IF（custodian.go:17）|
| AuthGateway | CP ネイティブ OAuth（`AUTH=oauth`）| ALB OIDC または同 oauth | 検証 email を `X-Forwarded-Email` に注入する契約は不変 |
| Ingress/TLS | Caddy（自己署名/社内 CA）| ALB + ACM | |
| Agent 認証 | 同一ホスト + Bearer | SG 制限 + Bearer → 将来 mTLS | `Token()` は両者共通 |

## 20b.3 Runtime 港の契約（`ecsRuntime` が満たすべき対応）

`control-plane/runtime.go` の `Runtime` インターフェース（`Start/Stop/State/Endpoint/Token/Name`）を ECS で実装する。
`runtime_ecs.go` の `ecsRuntime` にこの対応を doc コメントで固定済み（段1 スケルトン）:

| メソッド | ローカル（docker）| AWS（ECS, 段2 実装）|
|----------|-------------------|---------------------|
| `Start`  | `docker run -d`（初回）/ 停止残骸を rm→再作成 | `UpdateService desiredCount=1`（初回は Service/TaskDef 作成）。task env に `AGENT_TOKEN`+`AF_SECRET_KEY` 注入。RUNNING かつ Agent `/healthz` 通過まで待つ |
| `Stop`   | `docker rm -f` + per-user network 削除 | `UpdateService desiredCount=0`（home は EFS 永続、次 Start で resume）|
| `State`  | `docker inspect .State.Status` → running/stopped/none | desiredCount/runningCount → running/stopped/none |
| `Endpoint` | `http://127.0.0.1:<port>` | task の内部 Agent URL（Service Connect 名 or ENI IP）|
| `Token`  | `Workspace.AgentToken` | 同左（契約不変）|
| `Name`   | コンテナ名 | ECS service 名（= `Workspace.ContainerName` を流用）|

## 20b.4 段割り

- ✅ **段1 — シーム固め（このホストで検証完結）**:
  - `RuntimeFactory` 港を新設（`runtime.go`）。**Runtime の唯一の生成口**にし、`manager.runtimeFor` はこれに委譲。
  - `manager.go` に散在した `&dockerRuntime{...}` **直生成 5 箇所を factory 経由に統一**（countRunningInTenant /
    workspaceStateByMembership / stopWorkspaceByMembership / cleanHomeByMembership / runtimeFor）。以降 concrete 型は core に漏れない。
  - `main.go` が `AF_RUNTIME`（`local`|`ecs`）で factory を選ぶ。unknown は起動時 fail-fast。boot ログに `runtime=` を追加。
  - `ecsRuntime` **スケルトン**（`runtime_ecs.go`）: 港を満たすが lifecycle は not-implemented で**声高に失敗**（silent no-op 禁止）。
    段2 が実装する対応・`ecsConfig`（region/cluster/subnets/SG/EFS/roles/logGroup）を doc コメントで固定。
  - 検証: `go build ./...` / `go vet ./...` / `go test ./...`（`runtime_test.go` で factory 選択・ws スレッド・ecs 骨格を lock）。
- ▶ **段2 — ecsRuntime 本実装 + EFS Volume**（要 AWS）: AWS SDK for Go v2 で ECS Service/TaskDef を駆動。EFS AP を
  per-membership で払い出し task に mount。`Endpoint` を Service Connect で解決。secretKey/token を task env 注入。
- ▶ **段3 — MetadataStore(RDS) / KeyCustodian(KMS)**（要 AWS）: `Store` の Postgres アダプタ、`KeyCustodian` の KMS 実装。
- ▶ **段4 — IaC + Ingress/Auth**（要 AWS）: Terraform で VPC/ECS/EFS/RDS/ALB(ACM,OIDC)/ECR/SG/roles。`deploy/aws/ecs/`。
- ▶ **段5 — E2E 検証ゲート**（要 AWS）: 実 apply → login → tenant → workspace(Start=desired 1) → session → Stop(desired 0)→resume 通過。

## 20b.5 CP↔Agent 到達（要点）

ECS には publish host:port が無い。Runtime `Endpoint()` が差を吸収する前提で、段2 は **Service Connect**（推奨）か
内部 NLB、または awsvpc 同一 SG 内の ENI IP を返す。CP は同一 VPC 内から Bearer 付きで Agent REST を叩く（既存 proxy 経路不変）。

## 20b.6 段1 で触ったファイル

- `control-plane/runtime.go` — `RuntimeFactory`/`dockerFactory`/`newRuntimeFactory` 追加。
- `control-plane/runtime_ecs.go` — 新規（`ecsRuntime`/`ecsFactory`/`newECSFactory`/`ecsConfig` スケルトン）。
- `control-plane/manager.go` — `rtFactory` フィールド、`runtimeFor` を factory 委譲、直生成 4 箇所を差し替え。
- `control-plane/main.go` — `AF_RUNTIME` で factory 選択、boot ログに profile。
- `control-plane/runtime_test.go` — 新規（factory 選択 / ws スレッド / ecs 骨格の回帰）。

---

## 20b.7 段2 実装仕様（凍結）

> 🧊 **凍結の目的** — 検証用 AWS はすぐ用意できる。ゆえに段2は「まず実装仕様を確定 → 環境が出来次第
> 機械的に埋める」順とする。本節は `ecsRuntime` の各メソッドが叩く AWS 呼び出し・付随リソース・命名・
> IAM・待ち条件を**固定の契約**として凍結する。実装は本契約に対して行い、逸脱する場合は本節を先に改訂する。

### 20b.7.1 凍結した不変条件

段1の不変条件（[§20b.1](#20b1-ゴールと不変条件)）に加え、段2で次を追加固定する:

1. **ECS アダプタは CP DB に状態を持たない**（deterministic naming + tag 引き）。Service / TaskDef / EFS AP /
   Secret はすべて Workspace レコードから決まる名前・タグで**create-or-get** する。ゆえに段2は
   **スキーマ変更ゼロ**（[§20b.7.12](#20b712-スキーマ契約への影響凍結の結論)）。ARN 保管が要る規模になったら
   `ecs_workspace(workspace_id, …)` キャッシュ表が逃げ道だが、段2 では持ち込まない。
2. **Agent 無改修**。CP が `AGENT_TOKEN` / `AF_SECRET_KEY` を task に届ける契約は local と同一
   （local=`-e`、ECS=Secrets Manager `valueFrom`）。Agent から見た env は両者同じ。
3. **既定 local を一切壊さない**。`AF_RUNTIME` 未設定/`local` は AWS SDK 経路に一切入らない。
   ECS 経路は `AF_RUNTIME=ecs` の明示 opt-in でのみ到達する。
4. **secretKey/token は plaintext task env に置かない**（[§20b.7.5](#20b75-シークレット注入secrets-manager-valuefrom)）。

### 20b.7.2 タスクライフサイクル — ECS Service（desiredCount 0/1）

**採用 = 1 Workspace（= 1 membership）に 1 ECS Service、desiredCount を 0/1 で振る**。RunTask は不採用。

- 理由: (a) `Stop=desired 0` / `Start=desired 1` が既存 reaper の `Stop`/`State` にそのまま乗る
  （P3-9 二段構えの第2段が無改修で ECS に効く）。(b) 到達に採る Service Connect は Service 前提。
  (c) task crash 時に Service が自動補充する。RunTask だと停止検知・再起動・SC 登録を CP が自前で持つ。
- `Start`: DescribeServices → 無ければ RegisterTaskDefinition + CreateService（desired 1）、有れば
  UpdateService desired 1。RUNNING かつ Agent `/healthz` 通過まで待つ（[§20b.7.8](#20b78-state--start-待ち--タイムアウト)）。
- `Stop`: UpdateService desired 0。home は EFS 永続ゆえ次 Start で resume。Service は消さない
  （消すと SC 登録・冪等性が壊れる。冷えた Service は desired 0 で課金されない）。

### 20b.7.3 CP↔Agent 到達 — ECS Service Connect

**採用 = ECS Service Connect**。内部 NLB・生 ENI IP は不採用。

- 理由: ENI IP は task 入替の度に変わり `Endpoint()` のキャッシュを壊す。SC は task に依らない
  **安定した内部 DNS 名**を与える。per-workspace NLB はコスト/管理が過大。
- `Endpoint()` = `http://<ContainerName>:7700`（SC の client alias = `ContainerName`、Agent の
  container port=7700 は local の `-p …:7700` と同値）。CP は同一 VPC 内から Bearer 付きで REST/WS を
  proxy する（既存 proxy 経路不変。browser は Agent に直結しない＝Workspace は ALB target にしない）。
- **要件（→ 段4 IaC）**: CP 自身も同 SC namespace の **client** として稼働する ECS Service であること。
  でなければ SC 名を解決できない。詳細は [§20b.7.11](#20b711-cp-自身の配置要件段4-への橋渡し)。

### 20b.7.4 永続ストレージ — EFS アクセスポイント 2 本（CP 動的払い出し）

local の 2 マウント（`home` + `claude-config`、runtime.go:179-181）を EFS AP 2 本に対応させる。

| マウント先 | local | ECS |
|-----------|-------|-----|
| `/home/dev` | bind `<dataDir>/home` | EFS AP root `/home/<membership>` |
| `/var/lib/af/claude` | bind `<dataDir>/claude-config` | EFS AP root `/claude-config/<membership>` |

- **払い出し主体 = CP（動的）**。memberships は動的生成ゆえ IaC で先に列挙できない。local が Start 時に
  `mkdir home` するのと同型に、ECS は Start 時に `CreateAccessPoint`（tag `af-membership=<MembershipID>`,
  `af-role=home|claude`）。既存は DescribeAccessPoints の tag 引きで再利用（**ARN 非保管**）。
- POSIX uid/gid は AP に固定（container の `dev` uid に一致）。root path は AP の `RootDirectory.Path`。
- EFS ファイルシステム自体は IaC 作成（`AF_ECS_EFS_ID`）。AP のみ CP が動的に払い出す。

### 20b.7.5 シークレット注入 — Secrets Manager `valueFrom`

**★ 唯一のセキュリティ姿勢判断（要ラティファイ）**。

- **凍結案 = Secrets Manager 経由**。CP は Start 直前に per-workspace secret（名前は決定的、
  [§20b.7.7](#20b77-命名規約)）へ `AGENT_TOKEN` と**アンラップ済み DEK**（`resolveDEK` の戻り、manager.go:286）を
  PutSecretValue し、TaskDefinition は `secrets: [{name, valueFrom: <arn>}]` で参照する。execRole が
  `secretsmanager:GetSecretValue`。**plaintext task env には置かない**。
- **不採用 = plaintext task env**。local の `-e AF_SECRET_KEY=` は host root（`docker inspect`）だけが読める＝
  CP プロセスと同じ信頼境界。しかし ECS の plaintext env は `ecs:DescribeTaskDefinition` を持つ
  **アカウント内の広い主体**が読める＝境界が広がり、P3-3 封筒暗号の per-tenant crypto-shred を骨抜きにする。
- 副作用: unwrap 済み DEK が SM に at-rest で載る（SM の KMS で暗号化）。封筒の「外」に平文 DEK の写しが
  出る点は許容する（SM = AWS ネイティブ custodian、境界は task に閉じる）。真の per-tenant 失効は段3b KMS
  custodian が担う。

### 20b.7.6 TaskDefinition の中身

- `requiresCompatibilities: [FARGATE]`, `networkMode: awsvpc`, `cpu`/`memory` = Fargate task size
  （`WS_MEMORY` 相当。既定 1vCPU/2GB、[reference/aws §3.5](../reference/aws.md#35-コスト試算20-人-月額--おおよそ)）。
- `executionRoleArn` / `taskRoleArn` = `ecsConfig`（[§20b.7.9](#20b79-iam-最小権限)）。
- container `agent`:
  - `image` = Workspace イメージ（ECR）。local と**同一物**（[§20b.1](#20b1-ゴールと不変条件)）。
  - `portMappings: [{containerPort:7700, name:agent}]`（SC の port name）。
  - `environment`（**非機微のみ**）: `CLAUDE_CONFIG_DIR=/var/lib/af/claude`、`AGENT_SESSION_CMD`、
    `workspaceExtraEnv`（manager.go:574、例 `AF_AGENT_SELF_UPDATE_ALLOWED=1`）の非機微分。
  - `secrets`（機微）: `AGENT_TOKEN`、`AF_SECRET_KEY`（[§20b.7.5](#20b75-シークレット注入secrets-manager-valuefrom)）。
  - `mountPoints`: EFS AP 2 本（[§20b.7.4](#20b74-永続ストレージ-efs-アクセスポイント-2-本cp-動的払い出し)）。
  - `logConfiguration: awslogs`（`AF_ECS_LOG_GROUP`）。
- TaskDef family = `ContainerName`。env に per-workspace 値（secret は ARN 参照ゆえ family は image 変更時のみ
  revision 追加）。存在チェック = DescribeTaskDefinition、無ければ RegisterTaskDefinition。

### 20b.7.7 命名規約

| リソース | 名前/タグ | 備考 |
|---------|-----------|------|
| ECS Service | `ws.ContainerName`（例 `af-ws-<slug>-<key>`）| local のコンテナ名を流用（[§20b.3](#20b3-runtime-港の契約ecsruntime-が満たすべき対応)）|
| TaskDef family | `ws.ContainerName` | image 変更時のみ新 revision |
| EFS AP | tag `af-membership=<MembershipID>` + `af-role=home\|claude` | tag 引きで冪等 |
| Secret | `af-ws/<ContainerName>/agent-token`・`.../secret-key` | prefix `af-ws/` で IAM scope |
| SC client alias | `ws.ContainerName` | `Endpoint()` が組む |

### 20b.7.8 State / Start 待ち / タイムアウト

- **State**（DescribeServices）: `desired==0` → `stopped`。`desired>=1 && running>=1` → `running`。
  `desired>=1 && running==0`（起動中/入替中）→ `stopped`（read paths を graceful degrade。local の
  none/stopped と同じ扱い）。Service 不在 → `none`。
- **Start 待ち**: UpdateService desired 1 → DescribeServices `running>=1` かつ DescribeTasks
  `lastStatus==RUNNING` → その後 `Endpoint()+/healthz` を 200 まで poll（local `waitHealthy`
  と同ロジック、runtime.go:273）。
- **タイムアウト**: Fargate cold start + image pull を見込み **既定 120s**（local は 15s）。
  `AF_ECS_START_TIMEOUT` で上書き可。

### 20b.7.9 IAM（最小権限）

3 ロールに分割する:

- **CP task role**: `ecs:CreateService/UpdateService/DescribeServices/RegisterTaskDefinition/`
  `DescribeTaskDefinition/DescribeTasks/ListTasks`、`elasticfilesystem:CreateAccessPoint/`
  `DescribeAccessPoints/TagResource`（+ workspace 削除時 `DeleteAccessPoint`）、
  `secretsmanager:CreateSecret/PutSecretValue/DescribeSecret`（+ `DeleteSecret`）を **resource ARN を
  `af-ws*` に scope**、`iam:PassRole`（task/exec role へ）。
- **Workspace execRole**（ECS agent が使う）: `ecr:GetAuthorizationToken/BatchGetImage/`
  `GetDownloadUrlForLayer`、`logs:CreateLogStream/PutLogEvents`、`secretsmanager:GetSecretValue`
  （`af-ws/*` scope）。
- **Workspace taskRole**（Agent 実行時）: **原則ゼロ**。Agent は AWS API を叩かない。IMDS ブロック
  （`AWS_EC2_METADATA_DISABLED` 相当 or hop 制限）。egress は SG で git/Anthropic に限定
  （[§20b.7.15](#20b715-スコープ外)）。

### 20b.7.10 AWS SDK フットプリント

- 追加依存 = `aws-sdk-go-v2`（`config` + `service/ecs` + `service/efs` + `service/secretsmanager`）。
- 既定 local バイナリにも常時コンパイルされる（factory switch のため skeleton が既にコンパイル対象）。
  バイナリ増 ~15-25MB。実行時経路には入らないので挙動影響なし。
- 逃げ道: サイズを嫌うなら `//go:build aws` タグで ECS 実装 + stub を分ける手はある。ただし段2 では
  **採らない**（seam の単純さ優先。必要になったら導入）。

### 20b.7.11 CP 自身の配置要件（段4 への橋渡し）

`Endpoint()` の SC 解決は「CP が同 namespace の SC client」であることに依存する。ゆえに段4 IaC は:

- CP+Console を 1 ECS Service として同 VPC・同 SC namespace（client）に置く。
- ALB(OIDC/ACM) → CP。Workspace 群は ALB target にしない（CP からのみ SC 到達）。
- SG: `CP → Agent:7700` を許可、Workspace egress は git/Anthropic のみ。

### 20b.7.12 スキーマ / 契約への影響（凍結の結論）

- **MetadataStore スキーマ変更 = なし**（[§20b.7.1](#20b71-凍結した不変条件) の deterministic naming ゆえ）。
- **Agent 契約変更 = なし**。**Console 変更 = なし**（proxy 経路不変）。
- **CP コア（handlers/manager/reaper/admin/mcp）変更 = なし**。段2 は `runtime_ecs.go` の本実装 +
  `ecsConfig` 拡張（SC namespace, start timeout 等）+ `go.mod` の SDK 追加に**閉じる**。

### 20b.7.13 段2 で埋める場所

- `control-plane/runtime_ecs.go` — `Start`/`Stop`/`State`/`Endpoint` を SDK 実装。EFS AP create-or-get、
  TaskDef register-or-get、Service create-or-update、SM put、waiter。
- `control-plane/ecsConfig` 拡張 — SC namespace ARN、start timeout、task cpu/mem、AP uid/gid。
- `control-plane/go.mod` — aws-sdk-go-v2 modules。
- `control-plane/runtime_ecs_test.go`（新規）— ECS/EFS/SM の各 API を fake client 化し、Start/Stop/State の
  分岐（create-or-get の冪等・待ち・desired 遷移）を AWS 非依存で lock。

### 20b.7.14 検証ゲート（段5 E2E、要 AWS）

実 apply 後、次を green にして段2 完了とする:

1. login（ALB OIDC）→ tenant → workspace Start（Service desired 1、RUNNING、healthz 通過）。
2. session 作成 → 端末アタッチ（CP→SC→Agent 到達）→ 入出力。
3. Stop（desired 0）→ 再 Start で home/claude 状態 resume（EFS 永続）。
4. P3-9 reaper: idle で第1段 halt → 第2段 desired 0、アタッチ中は非発火（Runtime 無関係に同一挙動）。
5. secret が plaintext task env に出ていないこと（DescribeTaskDefinition で env に DEK が無い）。

### 20b.7.15 スコープ外（段2 では触らない）

- **MetadataStore(RDS/Postgres)** = 段3a（AWS 非依存で先取り検証可能。Docker の Postgres で完結）。
- **KeyCustodian(KMS)** = 段3b（`KeyCustodian{Wrap,Unwrap}` IF は確定済、custodian.go:17）。
- **IaC(Terraform)** = 段4（VPC/ECS/EFS/RDS/ALB(ACM,OIDC)/ECR/SG/roles/SC namespace、`deploy/aws/ecs/`）。
- **Egress 統制** = SG egress + NAT（+任意 proxy）。設計は別途（docs/20 系）。段2 の runtime には出さない。

[host-oom-fleet-risk]: の注意は memory 参照。
