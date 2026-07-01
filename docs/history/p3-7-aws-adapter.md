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

[host-oom-fleet-risk]: の注意は memory 参照。
