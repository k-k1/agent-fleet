# 67. メンバー毎の AWS 費用を、コスト配分タグを軸に見える化する

> 状態: 検討完了・**採用（ただし範囲を絞る）**（2026-08-17）。決定は
> [ADR 0048](decisions/0048-member-cloud-cost.md)。
> 関連: [46-usage-accounting.md](46-usage-accounting.md)（**トークン**の台帳。本書とは別軸で、
> 名前が衝突する）／ `control-plane/usage.go`（**稼働秒**の showback。本書はここを金額へ
> 延ばさない）／ [64-ec2-persistent-workspace.md](64-ec2-persistent-workspace.md)
> ＋ [ADR 0045](decisions/0045-ec2-persistent-workspace.md) 決定 8（1 スロット 1 ユーザー専有
> ＝タグが個人に紐づく前提）・決定 21（**効かない項目を画面に出さない**／表示のためだけに
> IAM を増やさない前例）／ [ADR 0044](decisions/0044-workspace-sizing.md) 決定 3（既定オフで
> 出して一度も発火しなかった前例）／ [66](66-tenant-network-restriction.md)（tenant_admin と
> super_admin の線引き・直前の先例）
> 対象: `control-plane/cloudcost.go`（新設）/ `cost_profile.go`（新設）/ `runtime_ecs_ec2.go` /
> `runtime_ecs.go` / `routes.go` / `store*.go` ＋ `migrations*/` /
> `deploy/aws/ecs/cfn/20-platform.yaml` /
> `console/src/features/cost/`（新設）・`features/settings/{AdminTab,TenantDialog,SettingsDialog}.tsx`

## 67.1 問題

「誰がいくら使っているか」を Console から見たい。今あるのは 2 つで、**どちらも金額ではない**。

| 既にあるもの | 何を数えているか | 単位 | 置き場 |
|---|---|---|---|
| `usage.go`（showback） | 実行中ワークスペースの**稼働秒** | 秒／時間 | 管理・テナント設定の「使用量」 |
| [46](46-usage-accounting.md)（usage accounting） | エージェントの**トークン** | token | 個人設定の「使用量」 |

`usage.go` は冒頭に **`No external billing.`** と書いてある。つまり **$ はどこにも無い**。
一方 AWS には実際の請求があり、CP は既にリソースへタグを打っている。
**そのタグを課金の軸として成立させて、実費を人に返せないか**——が本書の問いである。

**結論は採用。ただし「あなたのコスト」とは名乗らない。** 理由は 67.3 の実測にある。

## 67.2 先に名前を固定する（「費用」を 2 つ並べない）

⚠️ Console には既に **「使用量」/ "Usage" というラベルが 3 か所**ある（個人設定＝トークン、
管理＝稼働時間、テナント設定＝稼働時間）。ここに 4 つ目として金額を足すと、
利用者は「使用量」を開いて何が出るか予測できなくなる。**足す前に名前を分ける。**

| 概念 | 新しいラベル（ja / en） | 単位 | 出どころ |
|---|---|---|---|
| エージェントのトークン | **使用量** / Usage（現状維持） | token | Agent の台帳（[46](46-usage-accounting.md)） |
| ワークスペースの稼働 | **稼働時間** / Running time（**改称**） | 時間 | `usage_daily`（`usage.go`） |
| AWS の請求 | **クラウド費用** / Cloud cost（**新設**） | USD | Cost Explorer |

- **改称は 1 か所だけ**（`admin.usage_title` は既に「使用量（ワークスペース稼働時間）」なので、
  括弧の中を表に出すだけ）。タブ名 `tenant.tab_usage` / セクションキーは触らない
  ——deep-link と保存済みの最後のセクションが壊れる。
- **「クラウド費用」は稼働時間の中に入れない。** 同じパネルに「時間」と「$」を並べると、
  片方が実測でもう片方が請求である差が消える。

## 67.3 実測——この請求は何でできているか（sandbox 実データ）

`af-sandbox`（アカウント <account>・単独アカウント＝自身が payer）の
**2026-08-01〜08-16（UnblendedCost, USD）で合計 $9.0370**。利用種別で割ると:

| 利用種別 | $ | 人に紐づくか |
|---|---:|---|
| `APN1-BoxUsage:m7i.large` | 1.7802 | **確保中のスロットだけ**紐づく。空きプールは共有 |
| `APN1-NatGateway-Bytes` | 1.5751 | **紐づかない**（タグ不可） |
| `HostedZone` | 1.5000 | 紐づかない |
| `APN1-NatGateway-Hours` | 1.2400 | 紐づかない |
| `NoUsageType`（税） | 0.8200 | 紐づかない |
| `APN1-ETDataAccess-Bytes`（EFS） | 0.5662 | **紐づかない**（課金はファイルシステム単位。アクセスポイントに `af-membership` は付いているが請求は割れない） |
| `APN1-Fargate-vCPU-Hours` | 0.3448 | 紐づかない（CP 自身） |
| `APN1-DataTransfer-Regional-Bytes` | 0.2296 | 紐づかない |
| `APN1-LoadBalancerUsage` | 0.2187 | 紐づかない |
| `APN1-InstanceUsage:db.t4g.micro`（RDS） | 0.2017 | 紐づかない |
| `APN1-EBS:VolumeUsage.gp3` | 0.1762 | **home は紐づく**。スロットのルートは共有 |
| `APN1-PublicIPv4:InUseAddress` | 0.1756 | 紐づかない |
| `APN1-Fargate-GB-Hours` | 0.0863 | 紐づかない |
| `APN1-BoxUsage:m7i.xlarge` | 0.0482 | 確保中のみ |
| その他（RDS ストレージ・DNS クエリ・Secrets・スナップショット 他） | 0.0700 | ほぼ共有 |

**紐づけられる上限は $2.01 = 22.3%。残る 77.7% は共有。**
最大の単一項目は **NAT ゲートウェイの $2.82（31.2%）** で、これは
**利用者の `npm install` が作っている実費なのに、タグでは絶対に割れない**
（割るなら VPC フローログ×送信元 IP で、費用も複雑さも本書の範囲を超える）。

> **この 1 枚が本書で一番重要な事実である。** 「あなたの費用は $0.42 です」と出すと、
> 実際に会社が払っている額の 1/5 を指して「あなたの費用」と呼ぶことになる。
> この repo で繰り返し出ている「言っていることが違う」の型そのものなので、
> **画面のラベルは「あなたのワークスペースに直接ひも付く費用」に固定する**（67.9）。
>
> なお 1 人しか使っていない今は固定費の比率が極端に高い。人が増えれば
> BoxUsage が伸びて比率は変わる——が、**NAT と Route53 と ALB と RDS は増えない**ので、
> 「共有が常に存在する」という構造は変わらない。

## 67.4 タグは課金の軸になるか——**今のままではならない**

CP が打っているタグ（`runtime_ecs_ec2.go` 冒頭）は
`af-pool` / `af-role` / `af-membership` / `af-workspace` / `af-slot-size` ほか。
**実機のタグを読んだ結果**（2026-08-17）:

```
EC2 インスタンス i-0ac9ffa24cbf8f2b3 (m7i.large, stopped)
  af-managed-by=agent-fleet  af-role=slot  af-slot-size=m7i.large  af-pool=af-af-ecs-platform
  ⚠️ af-membership が無い

EBS ボリューム vol-07d5994a655a25929 (50 GiB, in-use)
  af-membership=d6e8070a…  af-role=home  af-workspace=af-ws-…  af-pool=…  af-idle-since=…
```

⚠️ **紐づけたい費用のうち 91%（BoxUsage $1.83 / $2.01）が乗っているインスタンスに、
軸のタグが付いていない。** スロットは「プールの箱」で、誰のものになるかは
**home ボリュームを attach した瞬間**に決まる設計（ADR 0045 決定 8）だから、
インスタンス側は誰の物でもないまま残っている。ロジックとしては正しい。**課金の軸としては穴。**

→ **決定: 確保時にインスタンスへ `af-membership` を打ち、解放時に消す**（ADR 0048 決定 3）。
既存のスロット検索は `af-role` と `af-pool` で絞り、占有判定は**ボリューム側**を見ている
（`freeSlots` / `occupiedInstances`）ので、**タグを 1 つ足してもロジックは何も変わらない**。
`ec2:CreateTags` / `ec2:DeleteTags` は `CpTaskRole` に既にある——**IAM の追加は要らない**。

### 67.4.1 ユーザー軸は既にある（`af-membership` はメール由来ではない）

⚠️ 実機の値 `d6e8070a484b950b4c71474bbbbdd95a` はハッシュに見えるが、実体は
`newID() = randHex(16)`（`control-plane/store.go`）——**メールとは無関係の乱数 32 桁**である。
`Tenant.ID` も同じ生成。したがって:

- **ユーザーへの紐づけは既に成立していて、既に非 PII。** 67.5 で `af-workspace` を外しても
  失うものは無い——`af-membership` → メンバーは CP が DB で解決できる（`usage.go` の
  `ListUsage` が既にやっている join と同じ）。
- ⚠️ **メールのハッシュを新設するのは、この乱数 ID より悪い。** メールのハッシュは
  仮名化された個人データのままで、しかも**総当たりで戻せる**（組織のメールアドレス空間は
  小さく列挙可能）。乱数 ID が既に与えているもの以上は何も買えない。
- **`af-user`（人単位・テナント横断）も足さない。** AWS 側では人を不透明のままにするので
  読みやすさの利得が無く、membership → identity の join で CP が出せる。

### 67.4.2 本当に無いのは**テナント軸**——`af-tenant` を新設する

どの AWS リソースにもテナントのタグが無い。CP は membership → tenant を DB で引けるので
Console の画面は作れるが、**AWS 側だけでテナント別に切れない**（Cost Explorer・請求
コンソール・CSV エクスポート）。

→ **`af-tenant` = テナントの slug**（ADR 0048 決定 3）。**ID ではなく slug** にするのは、
テナント軸を足す唯一の利得が「CP 無しで AWS 側だけで読めること」だからで、不透明 ID では
`af-membership` から導ける以上のものがほぼ無い。slug は**組織名であって個人データではなく**、
変更 API も無い（`CreateTenant` で決まり `UpdateTenant` が存在しない＝実質不変）。

打つ先は `af-membership` を打っている所すべて（home ボリューム / スナップショット /
EFS アクセスポイント / ECS 管理 EBS）＋ 新しくスロットインスタンス。
⚠️ **slug が不明なときは空文字を打たず、タグごと省く**——空のコスト配分タグ値は請求上
「テナント =（空白）」という実在のグループになり、「タグが無い」と読めない。

### 67.4.3 ⚠️ AWS が見たことのないタグキーは、先に有効化できない（実測）

```
$ aws ce update-cost-allocation-tags-status --cost-allocation-tags-status '[{"TagKey":"af-tenant","Status":"Active"}]'
ValidationException: Failed to update Cost Allocation Tag: Tag keys not found: af-tenant
```

順序は **「リソースに打つ → AWS が発見（〜24h）→ 有効化」** で固定されている。そして
**バックフィル無しの時計は打った時ではなく有効化した時から動く**。つまり
**タグ付けのコードを実機に届けるのが遅れた日数が、そのまま永久に取れない日数になる。**
これが「設計を書き終える前に P1 を実装した」理由である。

## 67.5 コスト配分タグの有効化——**設計より先に済ませた**

⚠️ **コスト配分タグは有効化した時点より先にしか効かない。バックフィルは無い。**
1 日遅れれば 1 日分の実費が永久に取れないので、設計を待たずに利用者の確認を取って実行した。

```
$ aws --profile af-sandbox --region us-east-1 ce update-cost-allocation-tags-status …
{"Errors": []}
$ aws … ce list-cost-allocation-tags --status Active
af-pool Active 2026-08-17T05:26:16Z        af-role      Active 2026-08-17T05:26:16Z
af-membership Active 2026-08-17T05:26:16Z  af-slot-size Active 2026-08-17T05:26:16Z
```

**`af-workspace` は意図的に有効化していない。** 値が `af-ws-k1-kami-gmail-com` と
**メールアドレス由来**で、有効化すると請求データ（CUR / CE / 請求書 CSV）に個人情報が入る。
分析上は不透明 ID の `af-membership`（乱数・67.4.1）で足り、メンバーへの解決は CP が
自分の DB でできる。（必要になれば後から足せる。ただし**足した時点より前は取れない**。）

**`af-tenant` は有効化「できない」——まだリソースに付いていないから**（67.4.3）。
P1 を実機に届けて AWS が発見してから有効化する。

### 67.5.1 誰が何回やるのか——**テナントは一切やらない**

有効化の単位は **`TagKey` だけ**である。API のエントリに値の次元が無い（実測）:

```
{"TagKey": "af-membership", "Type": "UserDefined", "Status": "Active", ...}
```

**1 エントリで全メンバー分が割れる。**明日入った人も、来月作ったテナントも、
**何もせずに**そのまま集計対象になる——CE は請求データに現れたタグ値を勝手に列挙する
ので、「値ごとの登録」という概念自体が無い。

| 単位 | 有効化が要るか |
|---|---|
| テナント（`default` / `acme` …） | **不要。一度も、誰も、やらない** |
| メンバー（新しい人が入った） | **不要** |
| **AWS アカウント（＝ 1 デプロイ）** | **1 回だけ。運用者が** |
| CP が新しいタグキーを増やしたとき | そのキーだけ 1 回 |

そもそもテナント管理者は AWS を触らない（触らせない）——[66](66-tenant-network-restriction.md)
が接続元制限を Console 側に作った理由と同じ線である。

運用者向けの注意 2 つ:

- ⚠️ **AWS Organizations 配下なら、有効化できるのは管理（支払い）アカウントだけ。**
  メンバーアカウントからは操作できない。sandbox は単独アカウントなので自分でできた。
- ⚠️ **順序の制約（67.4.3）が効くのは初回だけ。** 新規デプロイは
  「デプロイ → ワークスペースを 1 回起動 → AWS が発見（〜24h）→ 有効化」。
  2 人目以降・2 テナント目以降にこの待ちは無い。

⚠️ **したがって、この機能の「実費」は 2026-08-17 より前を一切表示できない。**
画面はそれを空欄ではなく**「この日より前は取得できません」と書く**（67.9）。

## 67.6 実費（Cost Explorer）か、按分見積（稼働秒 × 単価）か

| | 実費（CE） | 按分見積（稼働秒 × 単価） |
|---|---|---|
| 数字の正体 | **本物の請求額** | 見積 |
| 期間 | タグ有効化以降のみ | **全期間**（`usage_daily` がある） |
| 粒度／遅延 | 日次・**約 24h 遅れ**・確定まで `Estimated` | 即時 |
| 費用 | **1 リクエスト $0.01** | 0 |
| 単価の出どころ | AWS | **運用者が env で申告** |
| 共有費 | そのまま見える | 表現できない |

**決定: 実費だけを採る。稼働秒を金額に変換しない**（ADR 0048 決定 2）。

- 按分は**運用者が申告した単価**に依存する。インスタンスタイプを増やした・リージョンを
  変えた・値上げされた——どれも env を書き換えない限り**静かに嘘をつき続ける**。
  この repo は既に同じ型で 2 回やられている（ADR 0044 決定 3 の「既定オフで一度も発火しない」、
  docs/64 の「画面が逆を言っていた」）。
- 実費が取れるのに見積も並べると、**必ず食い違い、必ず「どっちが本当か」と聞かれる**。
- `usage_daily`（稼働時間）は**そのまま残す**。金額の無いデプロイ（docker / native）で
  唯一意味を持つ数字であり、AWS でも「稼働は長いのに費用が小さい＝停止し忘れではない」
  のような読み方に要る。**ただし金額と同じパネルには置かない。**

## 67.7 共有費——**黙って頭割りしない**

77.7% を人に配ると、その瞬間に数字は見積になる。**配らない。**

- **共有は「共有」として、別のカードに、内訳付きで出す。** NAT / Route53 / ALB / RDS /
  EFS / Fargate(CP) / 税。読み手（運用者）にとっては
  **「人に紐づかない固定費がこれだけある」こと自体が一番役に立つ情報**である。
- **空きプールのスロットも共有に落ちる。** `af-role=slot` かつ `af-membership` 無しの
  インスタンス時間＝「誰も使っていないのに回っている箱」。
  これは**プールが大きすぎることの実費**で、今まで数字で見えたことがない。
- **共有カードは super_admin だけ**。tenant_admin にデプロイ全体の ALB / RDS 請求を
  見せるのは、テナントの外の情報を渡すことになる（[66](66-tenant-network-restriction.md) と
  [ADR 0043](decisions/0043-login-idp.md) 決定 24/25 の線）。

## 67.8 ランタイム差——**AWS の請求が無い所にこの画面を出さない**

| ランタイム | AWS 請求 | `af-membership` が付く先 | この機能 |
|---|---|---|---|
| `ecs-ec2` | ある | **インスタンス**（本書で追加）・home EBS・スナップショット | **フル** |
| `ecs`（Fargate） | ある | 現状 **どこにも付かない**（`CreateService` に `Tags` も `PropagateTags` も無い） | タグ伝播を足す。**実機未検証**として出す |
| `docker` / `native` | **無い** | — | **出さない**（タブごと無い） |

先例は `SizingProfile()` → `GET /api/admin/workspace-sizing`（ADR 0045 決定 21）と
`hasPool`。同じ形で **`CostProfile()` を RuntimeFactory の任意能力**として足し、
`GET /api/cost/profile` が「このデプロイに費用の面があるか」を答える。
**能力が無ければ Console はタブを描かない**（「能力が無い＝操作要素を出さない」と同じ原則）。

Fargate の穴（`runtime_ecs.go`）:

```go
// 現状: CreateService に Tags / EnableECSManagedTags / PropagateTags のいずれも無い
_, err = e.ecs.CreateService(ctx, &ecs.CreateServiceInput{
    Cluster: …, ServiceName: …, TaskDefinition: …, LaunchType: LaunchTypeFargate, …
})
```

→ `Tags`（`af-membership` / `af-role`）＋ `EnableECSManagedTags: true` ＋
`PropagateTags: SERVICE` を足す。`ecs:TagResource` は `CpTaskRole` に既にある。
⚠️ **既存のサービスには遡って付かない**ので、`ecs:TagResource` を既存サービスにも 1 回打つ
（起動経路で冪等に）。⚠️ **sandbox は `ecs-ec2` なので、この経路は実機で確かめられない。**
ユニットテストで契約を固定し、**「実機未検証」と ADR とリリースノートに書く。**

## 67.9 誰に何を見せるか

| 見る人 | 見えるもの | 見えないもの |
|---|---|---|
| **メンバー本人** | 自分に直接ひも付く費用（自分の home EBS ＋ 自分がスロットを握っていた時間 ＋ 自分のスナップショット）と日次の推移 | 他人の費用・共有費・デプロイ合計 |
| **tenant_admin** | 自テナントのメンバー別＋テナント合計 | 他テナント・**共有費** |
| **super_admin** | 全テナント・全メンバー・**共有費の内訳**・デプロイ合計 | — |

- ⚠️ **メンバー向けの文言は「あなたのコスト」にしない。** 67.3 の通り、それは実額の
  2 割程度でしかない。ラベルは **「あなたのワークスペースに直接ひも付く費用（共有分は含みません）」**。
  ヒントに「NAT・DNS・ロードバランサ等の共有費は含みません」と書く。
- **単位は AWS が返した通貨（USD）をそのまま出す。円換算しない。**
- 「約 24h 遅れ」「本日分は未確定（`Estimated`）」を**数字の隣に**出す。後注では読まれない。

## 67.10 取り方・置き場（$0.01/req をどう扱うか）

**Console から CE を直接叩かない。** `usageSampler` と同じ形で、CP がバックグラウンドで
CE を引き、日次行を自分の DB に落とし、API は DB だけを読む。

- 1 回のリクエストで `GroupBy = [TAG af-membership, DIMENSION SERVICE]`（CE は 2 軸まで）。
  **これで「誰の・どのサービス」が 1 リクエストで揃う。** 未タグは `af-membership$` で返る
  （実測でキー形式を確認済み）。
- `Metrics = [UnblendedCost, AmortizedCost]`（メトリクスを増やしてもリクエスト数は増えない）。
  **表示は UnblendedCost（＝請求額）**。RI / Savings Plan を買って両者が乖離したときだけ
  注記を出す。
- **6 時間おき ×（直近 7 日を上書き取得）** → **1 日 4 リクエスト = $0.04/日 ≒ $1.2/月**。
  24h 遅れと `Estimated` の確定を吸収するために直近数日を毎回引き直す（UPSERT）。
- 保存は `cloud_cost_daily(day, membership_id, tenant_id, service, unblended_cents_micro,
  amortized_…, estimated)`。**membership_id が空の行＝共有**。
  金額は浮動小数で持たない（マイクロ単位の整数）。
- Console の「更新」ボタンは**再取得ではなく DB の読み直し**。手動再取得は super_admin のみ・
  レート制限付き（$0.01 が押し放題になる）。

## 67.11 IAM を 1 つ増やすか——**増やす**

前例は ADR 0045 決定 21 で、**vCPU を表示するためだけに IAM を足すのを却下**した
（env 申告に落とした）。今回は判断が逆になる。理由:

- あのときは**同じ数字を別の手段で正しく出せた**。今回の代替は「運用者申告の単価 × 秒」で、
  それは**別の数字**（見積）である。実費に代替は無い。
- 足すのは `ce:GetCostAndUsage` **1 アクション・読み取りのみ**。CE はリソース単位の
  スコープを持たないので `Resource: "*"` になるが、**返るのは集計金額だけ**で
  リソースにも秘密にも触れない。
- ⚠️ ただし「このアカウントの請求総額が CP から読める」ことは事実なので、
  **アカウントを agent-fleet 以外と共有しているデプロイでは、その分も共有費に混ざる**。
  これは**画面に書く**（「このアカウントの請求全体を集計しています」）。

`20-platform.yaml` の `CpTaskRole` に:

```yaml
- Sid: CostExplorerRead
  Effect: Allow
  Action: [ ce:GetCostAndUsage ]
  Resource: "*"        # CE はリソーススコープを持たない
```

⚠️ **これはスタック更新＋実機再デプロイが要る。** そして
docs/64 の実機検証で学んだこと——**「実 AWS で通した」≠「本番の権限で通した」**。
E2E はデプロイヤ資格情報で走るので `AdministratorAccess` では必ず通る。
**`CpTaskRole` のポリシーを抽出して assume した状態で叩く**確認まで含める
（`ec2:CreateSnapshot` が抜けていたのを誰も捕まえられなかったのと同じ穴）。

⚠️ もう 1 つ: **IAM ユーザー／ロールが Billing API を使うには、アカウント設定の
「IAM ユーザー/ロールによる請求情報へのアクセス」が有効である必要がある。**
sandbox では既に有効（`ce list-cost-allocation-tags` が通った）。**別アカウントに
デプロイする運用者向けに、この前提を `deploy/` の手順へ書く。**

## 67.12 精度の限界（画面と ADR の両方に書く）

1. **2026-08-17 05:26 UTC より前は取れない。** バックフィル不可。
2. **共有 77.7% は誰にも配られない。** メンバーに出る額は実額の一部。
3. **CUR の行は時間単位**なので、1 時間の途中でスロットが人から人へ移ると、
   その 1 時間はタグが付いていた方に丸ごと寄る。日次で見れば平均されるが、
   **交代が激しい日は誤差になる**。（`usage_daily` の秒で再按分する手はあるが、
   それは実費を見積で上書きすることなので v1 では採らない。）
4. **`Estimated: true` の日は変わる。** 最新日は必ず動くものとして描く。
5. **EFS はファイルシステム単位課金**なので、アクセスポイントにタグがあっても割れない。
6. **NAT の $2.82 は利用者が作った実費なのに割れない。**

## 67.13 実装フェーズ（この文書の時点では未着手）

| | 内容 | 実機で確かめられるか |
|---|---|---|
| **P0** | コスト配分タグの有効化 | ✅ **実行済み**（67.5） |
| **P1** | `ecs-ec2`: 確保時にインスタンスへ `af-membership` ＋ `af-tenant`、解放／隔離時に除去、掃除経路で**双方向に**修復（古いタグは消し、抜けたタグはボリュームから写す）。`Workspace.TenantSlug` を join で運ぶ | ✅ **実装済み・sandbox で確認する** |
| **P1.5** | 実機に届いた後、AWS が `af-tenant` を発見したら有効化 | ✅ P1 の翌日 |
| **P2** | `CpTaskRole` に `ce:GetCostAndUsage`／スタック更新／**本番の権限で疎通** | ✅ **実装済み・実機で確認**（CP 自身に引かせた） |
| **P3** | `cloudcost.go`（CE ポーラ＋`cloud_cost_daily`）／`CostProfile()`／API 3 本 | ✅ **実装済み・実機で疎通**（中身は 8/18 以降） |
| **P4** | Console: 「クラウド費用」（管理・テナント設定・個人）／「使用量」→「稼働時間」改称／en+ja | ✅ **実装済み・headless で目視**（暗色／明色） |
| **P5** | Fargate のタグ伝播（`Tags` ＋ `EnableECSManagedTags` ＋ `PropagateTags: SERVICE`） | ❌ **実装済み・実機未検証で出す** |

⚠️ **P3 の受け入れ確認は P0 の 24 時間後より前にはできない**（CE の反映待ち）。
「実装したが 0 が並ぶ」のは正常なので、**それを異常と読まないよう画面に理由を出す**。

## 67.13.1 実機に入れた（2026-08-17・<dev-deployment>）

P1 を CP に入れ、`crane append` で **af-cp のレイヤ 1 枚だけ**を現行 `:dev` に重ねた。
**Console の層には触っていない**——今回 Console を 1 行も変えていないので、
差し替えなければ「Console だけ古い CP」は構造的に起こり得ない。

- push 前に**レイヤ tar と展開後のイメージを照合**: `usr/local/bin/af-cp` の sha256 が
  ビルドした物と一致（`bbe6d923…`）、`app/console/index.html` が参照する
  ローカル資産（`assets/index-B95pBURp.js` / `index-BucQrwJi.css` / `brand/*` /
  `manifest.webmanifest`）が全て在ることを確認。
- push 後、**走っているタスクの `imageDigest` が push した digest と一致**
  （`sha256:b2666429…`。旧 `sha256:558b87fc…` から入れ替わった）。`/healthz` は 200。
- ⚠️ **`af-tenant` は先に実リソースへ手で打った**（`vol-07d5994a…` に `af-tenant=default`）。
  発見の時計を CP のデプロイより先に動かすため。値はコードが書くのと同じもの。

**そして修復パスが実機で発火した。** home が停止中スロットに attach されたままだったので、
スイーパーの「タグが抜けている箱をボリュームから写す」側が動いた:

```
2026/08/17 06:18:18 ecs-ec2 sweep: slot i-0ac9ffa24cbf8f2b3 holds
  "d6e8070a484b950b4c71474bbbbdd95a" but is billed to ""; restamping
```

```
i-0ac9ffa24cbf8f2b3 のタグ（修復後）
  af-role=slot  af-slot-size=m7i.large  af-pool=af-af-ecs-platform  af-managed-by=agent-fleet
  af-membership=d6e8070a484b950b4c71474bbbbdd95a   ← 新
  af-tenant=default                                 ← 新
```

⚠️ **残: `af-tenant` の有効化。** デプロイ直後もまだ
`ValidationException: Tag keys not found: af-tenant` で、AWS 側の発見待ち（〜24h）。
**これを済ませるまで、テナント軸の実費は 1 日も溜まっていない。**

## 67.13.2 P2〜P4 を入れた（2026-08-17・同日）

**P2（IAM）。** `20-platform.yaml` の `CpTaskRole` に `ce:GetCostAndUsage` を足して
スタックを更新し、実ロールに入ったことを確認した
（`iam get-role-policy … Sid==CostExplorerRead`）。

**そして「本番の権限で通った」ことを本番の経路で確かめた。** テストロールを作って
assume するのではなく、**CP 自身にやらせた**——CP は `CpTaskRole` で動いており、
ポーラは起動直後に 1 回引くので、ログがそのまま権限の証明になる:

```
2026/08/17 06:38:30 cloud cost poller: interval=6h0m0s window=7d (Cost Explorer costs $0.01/request)
2026/08/17 06:38:31 cloud cost: 36 rows over 7 days
```

これは docs/64 §64.22 の穴（E2E がデプロイヤ資格情報で走るので `AdministratorAccess`
では必ず通る）を、シミュレーションではなく**同じ経路**で塞いだ形である。

**P3（取得・保存・API）。** `/api/cost/profile` `/api/cost/me`
`/api/admin/cloud-cost` はいずれも実機で 401（＝ルートは生きていて認証で止まる）。

⚠️ **実費の中身はまだ空である。** タグ有効化が 8/17 05:26、CE は約 24h 遅れなので、
この時点で 8/17 の行はまだ無く、8/16 は全額が未タグ（`af-membership$` に $4.94）。
**「実装したのに 0 が並ぶ」のは正常**で、だから画面が理由を書く（67.9）。

**P4（Console）。** ログイン後の画面は headless では見られない（Google が弾く）ので、
**`console/scripts/shots` のフィクスチャサーバに費用のスタブを足し、自前 Chromium＋
素の CDP で実際の Console バンドルを描かせて目視した**（ダーク／ライト両方）。
見つけて直したのは 2 件、どちらも実測でしか出ない類のもの:

- **30 日ぶんの日付ラベルが重なって `08-1708-1808-19…` と読めない。**
  既定の期間が 30 日なので初期表示がこれになる。目盛りを間引き（10 本前後）、
  正確な日付は各棒の `title` へ移した。
- **内訳行の金額が宙に浮いていた。** 稼働時間の行は棒が中央を埋めるが、費用の内訳は
  名前と金額の 2 列しかないので、右端へ寄せないと揃って見えない。

管理側は「$29.14 メンバーにひも付く費用」と「$96.50 共有（割り当てなし）」が別カードで
並び、**紐づく分が共有よりずっと小さい**ことが一目で読める——67.3 の事実がそのまま
画面の形になっている。未確定の日の縞は暗色・明色どちらでも見えた。

## 67.14 実機で確かめたこと（2026-08-17）

- `af-sandbox` は**単独アカウント**（`organizations describe-organization` が
  `AWSOrganizationsNotInUseException`）＝自分が payer。タグ有効化も CE も自分でできる。
- CUR レポート定義は **0 件**、Cost Category も **0 件**。土台は何も無い。
- `ce get-cost-and-usage --group-by Type=TAG,Key=af-membership` は通り、
  キー形式は `af-membership$<値>`（未タグは `af-membership$`）。有効化直後なので
  8/14〜8/16 は全額が未タグ（$0.61 / $1.16 / $4.94）。
- `CpTaskRole` には `ec2:CreateTags` / `ec2:DeleteTags` / `ecs:TagResource` が**既にある**。
  足りないのは `ce:GetCostAndUsage` **だけ**。
- **未発見のタグキーは有効化できない**（`ValidationException: Tag keys not found`）。
  タグ付け → 発見 → 有効化の順で、時計は最後から動く（67.4.3）。
