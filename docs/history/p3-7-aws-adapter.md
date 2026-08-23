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
- ✅ **段2 — ecsRuntime 本実装 + EFS Volume**（コードは AWS 非依存で完結・実 AWS 疎通は段5）: AWS SDK for Go v2 で
  ECS Service(desired 0/1)/TaskDef を駆動。EFS AP を per-membership で払い出し（tag 引きの create-or-get）task に
  mount。`Endpoint` を Service Connect で解決。**token/DEK は SSM SecureString** を `secrets valueFrom` で注入（凍結
  §20b.7.5、plaintext task env は不使用）。`runtime_ecs.go` に実装、狭い AWS クライアント IF（ecsAPI/efsAPI/ssmAPI）を
  fake 化した `runtime_ecs_test.go` で create-or-get 冪等・desired 遷移・SC 配線・dev secret スキップを lock。
  `go build`/`go vet`/`go test`（34 通過）green。**AF_ECS_\*** env で placement 注入（region/cluster/subnets/sg/
  efs/namespace/roles/logGroup/image/cpu/mem/uid/gid/start-timeout）。
- ▶ **段3 — MetadataStore(RDS) / KeyCustodian(KMS)**（要 AWS）: `Store` の Postgres アダプタ、`KeyCustodian` の KMS 実装。
- ◐ **段4 — IaC + Ingress/Auth**（substrate 実証済・残＝CP を substrate に配線）: **CloudFormation** で
  VPC/ECS/EFS/RDS/ALB(ACM)/ECR/SG/roles/SC namespace。`deploy/aws/ecs/cfn/`（`00-network`/`10-data`/`20-platform`/
  `30-ingress`）を **sandbox(ap-northeast-1) で deploy→検証→teardown 実証済**（30 は ACM+ALB+CP Fargate で
  `af-dev.<domain>` に実ログイン到達、認証は CP ネイティブ Google OAuth＝ALB は TLS 終端のみ・OIDC 不使用）。CFN は
  **static substrate のみ**を作り、per-workspace リソース（Service/TaskDef/EFS AP/SSM param）は CP が実行時に動的払い出し（[§20b.7.1](#20b71-凍結した不変条件)）。
  ツールは CFN に寄せた（`ec2-single/cfn.yaml` の前例と一貫、顧客 footprint 最小＝creds+`aws cloudformation deploy` のみ）。
  実地の学び: SG 説明文は `<>` 不可 / 新規アカウントは ECS service-linked role 事前作成 / Fargate は WS_DATA 親ディレクトリを
  scratch volume で用意（README 参照）。残＝CP タスクに `AF_RUNTIME=ecs`＋`AF_ECS_*`＋EFS を配線し段2 を実駆動。
- ✅ **段5 — E2E 検証ゲート（実 AWS 到達確認）**: sandbox で 00-30 substrate＋段2 配線済 CP を立て、実ブラウザで
  login → workspace Start → shell セッションまで到達。確認できたこと: CP が ws ECS サービス(desired 1、RUNNING)＋
  **EFS AP 2本(home/claude, transit 暗号)＋SSM SecureString(token/DEK)** を動的払い出し、**CP→Service Connect→Agent**
  が到達（Agent ログに `POST /sessions` 受理）、task env に DEK/token 平文なし(secrets valueFrom のみ、env は
  CLAUDE_CONFIG_DIR だけ)。**実地の findings（段2 の改善候補）**:
  - **(A) 大容量イメージの cold pull が Start の healthz 待ちを超過**（af-workspace 7.4GB、初回 pull 数分）。**→ 対応済**
    （commit: fix/p3-7-ecs-start-nonfatal）: `ecsRuntime.Start` は upsertService で desired 1 にした後、healthz 待ちを
    **best-effort・非致命**にし（タイムアウトは error でなく log）、成功を返す。ゆえに cold pull 中でも Start は「失敗」に
    ならず、workspace は非同期に収束・Console の State ポーリングが running を拾う。既定 budget も 120→90s に短縮
    （早く "starting" を返す）。unit test `TestECSStartNonFatalWhenAgentNotReady` で回帰固定。
  - **(B) CP の SQLite が ephemeral(/tmp)ゆえ CP 再デプロイで状態消失**→ ws レコードの token 不整合・ログイン状態リセット。
    **→ 対応済（段3a Postgres Store アダプタ）**: EFS-SQLite は WAL が NFS 不可ゆえ避け、**RDS Postgres** を採用。
    共有 `sqlStore`＋`?→$n` rebind ラッパ（`store_sql.go`）で SQLite 実装を無改修流用、Postgres は統合スキーマ
    （`migrations-pg/0001_init.sql`）＋`store_postgres.go`。方言差は placeholder のみ（+accumulate UPSERT の既存値を
    テーブル修飾＝両方言可）。**Docker Postgres で 53 メソッドの conformance テスト green**（`store_postgres_test.go`、
    `AF_TEST_DATABASE_URL`）。CP は `AF_DATABASE_URL` or `AF_DB_HOST/PORT/USER/NAME`＋secret `AF_DB_PASSWORD` から DSN 組成
    （password は RDS 管理 Secrets Manager から valueFrom、plaintext URL に出さない）。CFN 配線: 30-ingress が CP を RDS へ、
    ExecRole に `secretsmanager:GetSecretValue`(rds!*)。CP 状態が task 入替を跨いで永続。残＝実 AWS での再検証（sandbox 再構築時）。
  - **(C) SC 動的ディスカバリは機能**（懸念は否定）: 先に起動していた CP が、後から作られた ws サービスに到達できた。
    初回の "unreachable" は SC ではなく task が pull 中で未 RUNNING だっただけ。
  残＝Stop(desired 0)→resume の明示確認（機構は unit test 済＋AP は tag 引き再利用で決定的）と、上記 (A)(B) の反映。

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
   **スキーマ変更ゼロ**（[§20b.7.12](#20b712-スキーマ--契約への影響凍結の結論)）。ARN 保管が要る規模になったら
   `ecs_workspace(workspace_id, …)` キャッシュ表が逃げ道だが、段2 では持ち込まない。
2. **Agent 無改修**。CP が `AGENT_TOKEN` / `AF_SECRET_KEY` を task に届ける契約は local と同一
   （local=`-e`、ECS=SSM Parameter Store SecureString `valueFrom`）。Agent から見た env は両者同じ。
3. **既定 local を一切壊さない**。`AF_RUNTIME` 未設定/`local` は AWS SDK 経路に一切入らない。
   ECS 経路は `AF_RUNTIME=ecs` の明示 opt-in でのみ到達する。
4. **secretKey/token は plaintext task env に置かない**（SSM SecureString `valueFrom`、[§20b.7.5](#20b75-シークレット注入--ssm-parameter-store-securestring-valuefrom)）。

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

### 20b.7.5 シークレット注入 — SSM Parameter Store SecureString `valueFrom`

**★ セキュリティ姿勢判断＝ラティファイ済（B2 採用）**。

- **凍結 = SSM Parameter Store SecureString 経由**。CP は Start 直前に per-workspace パラメータ（名前は決定的、
  [§20b.7.7](#20b77-命名規約)）へ `AGENT_TOKEN` と**アンラップ済み DEK**（`resolveDEK` の戻り、manager.go:286）を
  `PutParameter`(Type=SecureString) し、TaskDefinition は `secrets: [{name, valueFrom: <param-arn>}]` で
  参照する。execRole が `ssm:GetParameters`。**plaintext task env には置かない**。`AGENT_TOKEN` も同経路で
  扱い、分岐を作らない。
- **不採用① = plaintext task env**。local の `-e AF_SECRET_KEY=` は host root（`docker inspect`）だけが読める＝
  CP プロセスと同じ信頼境界。しかし ECS の plaintext env は `ecs:DescribeTaskDefinition` を持つ
  **アカウント内の広い主体**（および `RegisterTaskDefinition` の CloudTrail イベント）から読める＝境界が広がり、
  P3-3 封筒暗号の per-tenant crypto-shred を骨抜きにする。CloudTrail 漏洩は消せない。
- **不採用② = Secrets Manager**。セキュリティは SSM SecureString と同格（IAM scope で読者限定・TaskDef は ARN
  のみ・CloudTrail に値出ず・KMS at-rest）だが、$0.40/secret/月 ×（2×workspace 数）の固定費が付く。今回は
  rotation/cross-account が不要ゆえ、**Standard パラメータで実質無料**の SSM SecureString を既定にする。
  SM が要る社（集中ローテ等）は同 `valueFrom` 機構で差し替え可＝任意。
- 副作用: unwrap 済み DEK が SSM に at-rest で載る（AWS 管理鍵 `aws/ssm` で KMS 暗号化）。封筒の「外」に
  平文 DEK の写しが出る点は許容する（SSM = tight IAM の正規シークレットストア、境界は task に閉じる）。真の
  per-tenant 失効は段3b KMS custodian が担う。

### 20b.7.6 TaskDefinition の中身

- `requiresCompatibilities: [FARGATE]`, `networkMode: awsvpc`, `cpu`/`memory` = Fargate task size
  （`WS_MEMORY` 相当。既定 1vCPU/2GB、[dev/09 §9.8 コスト特性](../dev/09-deploy.md#98-コスト特性ec2-single--ecs)）。
- `executionRoleArn` / `taskRoleArn` = `ecsConfig`（[§20b.7.9](#20b79-iam最小権限)）。
- container `agent`:
  - `image` = Workspace イメージ（ECR）。local と**同一物**（[§20b.1](#20b1-ゴールと不変条件)）。
  - `portMappings: [{containerPort:7700, name:agent}]`（SC の port name）。
  - `environment`（**非機微のみ**）: `CLAUDE_CONFIG_DIR=/var/lib/af/claude`、`AGENT_SESSION_CMD`、
    `workspaceExtraEnv`（manager.go:574、例 `AF_AGENT_SELF_UPDATE_ALLOWED=1`）の非機微分。
  - `secrets`（機微）: `AGENT_TOKEN`、`AF_SECRET_KEY`（SSM SecureString `valueFrom`、[§20b.7.5](#20b75-シークレット注入--ssm-parameter-store-securestring-valuefrom)）。
  - `mountPoints`: EFS AP 2 本（[§20b.7.4](#20b74-永続ストレージ--efs-アクセスポイント-2-本cp-動的払い出し)）。
  - `logConfiguration: awslogs`（`AF_ECS_LOG_GROUP`）。
- TaskDef family = `ContainerName`。env に per-workspace 値（secret は ARN 参照ゆえ family は image 変更時のみ
  revision 追加）。存在チェック = DescribeTaskDefinition、無ければ RegisterTaskDefinition。

### 20b.7.7 命名規約

| リソース | 名前/タグ | 備考 |
|---------|-----------|------|
| ECS Service | `ws.ContainerName`（例 `af-ws-<slug>-<key>`）| local のコンテナ名を流用（[§20b.3](#20b3-runtime-港の契約ecsruntime-が満たすべき対応)）|
| TaskDef family | `ws.ContainerName` | image 変更時のみ新 revision |
| EFS AP | tag `af-membership=<MembershipID>` + `af-role=home\|claude` | tag 引きで冪等 |
| SSM param | `/af-ws/<ContainerName>/agent-token`・`.../secret-key`（SecureString）| prefix `/af-ws/` で IAM scope |
| SC client alias | `ws.ContainerName` | `Endpoint()` が組む |

### 20b.7.8 State / Start 待ち / タイムアウト

- **State**（DescribeServices）: `desired==0` → `stopped`。`desired>=1 && running>=1` → `running`。
  `desired>=1 && running==0`（起動中/入替中）→ **`starting`**。Service 不在・INACTIVE → `none`。
- **改訂（2026-07-08, fix/ws-starting-state）**: 当初は `desired>=1 && running==0` を `stopped` に
  degrade させていた（read paths を壊さない意図）。しかし af-workspace（約7.4GB）の Fargate cold pull は
  数分かかり、その間「起動処理中なのに停止に見える」— 利用者が二重 Start / 起動失敗と誤認する実害が出たため、
  Runtime 契約を 4 値（`running | starting | stopped | none`）に改訂した。消費者側の扱い:
  - `ensureWorkspaceStarted` / `ecsRuntime.Start`: `starting` は early-return（再 Start は task def
    再登録 + ForceNewDeployment で pull をやり直させてしまうため厳禁）。
  - reaper: `!= "running"` は従来どおり sweep 対象外 → starting を誤 stop しない。
  - quota（`countRunningInTenant`）: `starting` も 1 枠として数える（pull 中の突破を防ぐ）。
  - read paths（sessions list / admin / audit / usage / mcp）: `== "running"` 判定のままで安全に
    degrade（Agent 未到達なので DB ミラー提供・スキップが正）。
  - Console: `starting` を「起動中…」表示し、4 秒ポーリングで `running` へ自動遷移。
  - local(docker) アダプタは従来どおり 3 値のみ返す（起動が秒オーダーで `starting` を観測しない）。
- **停止改訂（2026-07-08, fix/ws-starting-state）**: 停止を graceful な 2 段階に改めた。
  従来 local は `docker rm -f`（即 SIGKILL）、ECS は SIGTERM→stopTimeout(30s)→SIGKILL だが
  Agent が無ハンドラかつ PID 1（`initProcessEnabled` 無し、kernel が default-action シグナルを
  PID 1 に配送しない）のため SIGTERM が黙殺され、実質「30 秒待つだけの SIGKILL」だった。
  - **Agent**（workspace/agent/shutdown.go）: SIGTERM/SIGINT を捕捉 → 全 live pane に
    `tmux send-keys C-c`（= SIGINT。claude は進行中ターンを中断し jsonl を整合確定）→
    status hook が working を報告しなくなるまで待機（AGENT_STOP_GRACE_SEC 予算内）→
    `tmux kill-server` → exit 0。セッションは従来どおり resume 可能（meta/jsonl は home 永続）。
  - **local**: `docker stop -t <grace>` → `docker rm`（「通常の停止 = none」は維持）。
    grace 超過時は docker が SIGKILL（2 段階目内蔵）。`docker stop` 自体の失敗時のみ
    従来の `rm -f` にフォールバック。
  - **ECS**: task def に `stopTimeout=<grace>` と `linuxParameters.initProcessEnabled=true`
    （docker `--init` 対称: zombie reap + シグナル転送）を追加。Stop（desired 0）は不変。
    stopTimeout は登録時に焼き込まれるため grace 変更は次回 Start から反映。
  - **設定**: `AF_STOP_GRACE_SEC`（既定 30、1..120 にクランプ = Fargate stopTimeout 上限）が
    両アダプタを駆動。コンテナへは安全マージンを引いた `AGENT_STOP_GRACE_SEC`（既定 25）を注入。
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
  `ssm:PutParameter/GetParameters/DescribeParameters`（+ `DeleteParameter`）を **resource ARN を
  `/af-ws/*` に scope**、`iam:PassRole`（task/exec role へ）。SecureString の暗号化は AWS 管理鍵
  `aws/ssm` ゆえ CP 側の KMS 権限は不要（CMK に替える場合のみ `kms:Encrypt` を足す）。
- **Workspace execRole**（ECS agent が使う）: `ecr:GetAuthorizationToken/BatchGetImage/`
  `GetDownloadUrlForLayer`、`logs:CreateLogStream/PutLogEvents`、`ssm:GetParameters`
  （`/af-ws/*` scope。SecureString 復号は `aws/ssm` 管理鍵ゆえ追加 KMS 権限不要）。
- **Workspace taskRole**（Agent 実行時）: **原則ゼロ**。Agent は AWS API を叩かない。IMDS ブロック
  （`AWS_EC2_METADATA_DISABLED` 相当 or hop 制限）。egress は SG で git/Anthropic に限定
  （[§20b.7.15](#20b715-スコープ外段2-では触らない)）。

### 20b.7.10 AWS SDK フットプリント

- 追加依存 = `aws-sdk-go-v2`（`config` + `service/ecs` + `service/efs` + `service/ssm`）。
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
  TaskDef register-or-get、Service create-or-update、SSM PutParameter、waiter。
- `control-plane/ecsConfig` 拡張 — SC namespace ARN、start timeout、task cpu/mem、AP uid/gid。
- `control-plane/go.mod` — aws-sdk-go-v2 modules。
- `control-plane/runtime_ecs_test.go`（新規）— ECS/EFS/SSM の各 API を fake client 化し、Start/Stop/State の
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
- **IaC(CloudFormation)** = 段4（VPC/ECS/EFS/RDS/ALB(ACM,OIDC)/ECR/SG/roles/SC namespace、`deploy/aws/ecs/`。static substrate のみ）。
- **Egress 統制** = SG egress + NAT（+任意 proxy）。設計は別途（docs/20 系）。段2 の runtime には出さない。

[host-oom-fleet-risk]: の注意は memory 参照。
