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
> ／ `features/settings/tenantMembers.tsx`（67.15：メンバー詳細）・`console/scripts/shots/`

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

| 何が増えたとき | 追加の有効化が要るか |
|---|---|
| テナントが増えた（`acme` を作った） | **要らない**——`af-tenant` が Active なら値は自動で拾われる |
| メンバーが増えた（人が入った） | **要らない**——`af-membership` が Active なら同上 |
| **AWS アカウント（＝ 1 デプロイ）を新しく立てた** | **要る。全キーを 1 回ずつ、運用者が** |
| **CP が新しいタグキーを増やした** | **要る。そのキーだけ 1 回** |

⚠️ **「要らない」は「そのキーを有効化しなくてよい」ではない。** キーは最初に 1 回
必ず有効化する（`af-membership` も 05:26 にそうした）。要らないのは**値が増えるたびの
追加操作**である。

そもそもテナント管理者は AWS を触らない（触らせない）——[66](66-tenant-network-restriction.md)
が接続元制限を Console 側に作った理由と同じ線である。

運用者向けの注意 2 つ:

- ⚠️ **AWS Organizations 配下なら、有効化できるのは管理（支払い）アカウントだけ。**
  メンバーアカウントからは操作できない。sandbox は単独アカウントなので自分でできた。
- ⚠️ **順序の制約（67.4.3）が効くのは「そのキーが初めて実リソースに現れたとき」だけ。**
  新規デプロイは「デプロイ → ワークスペースを 1 回起動 → AWS が発見（〜24h）→ 有効化」。
  2 人目以降・2 テナント目以降にこの待ちは無い。

### 67.5.1a 決定を覆した——**有効化は CP が自動でやる**（決定 11）

当初は「請求コンソールの操作は人がやるもの」として `ce:UpdateCostAllocationTagsStatus`
を意図的に外していた（決定 8 のコメント）。**覆した。**理由は損害の非対称:

- 忘れた場合 → **恒久的なデータ欠測**。この系で唯一「後から直せない」前提条件である。
- 自動化した場合 → 請求データに列が 5 本増える。

さらに「デプロイ時にはできず、翌日、人が、覚えていれば」という形は
ADR 0044 決定 3 の「誰もやらない手順は存在しない手順」そのものである。

**ただし CP がアカウントの請求設定に書き込む唯一の場所**なので、ポリシーではなく
**コードで 2 つ縛る**（`cost_tags.go`）:

1. **固定の許可リスト 5 キー**（`af-membership` / `af-tenant` / `af-role` / `af-pool` /
   `af-slot-size`）。⚠️ **`af-workspace` は絶対に有効化しない**——値がメール由来
   （決定 1）。テストで縛った。
2. ⚠️ **人が Inactive にしたキーは触らない。** AWS は状態を変えた瞬間に
   `LastUpdatedDate` を打つので、「Inactive かつ `LastUpdatedDate` 有り」は**人の決定**
   であり、戻すのは運用者を自分の請求コンソールで上書きすることになる。
   「Inactive かつ打刻なし」＝一度も設定されていない、だけを有効化する。

⚠️ **一発ではなく再試行**。未発見のキーは有効化できない（67.4.3）ので、
クラウド費用ポーラの 6 時間 tick に相乗りして、全キーが Active か Declined になるまで
試み続ける。落ち着いたら**呼ぶのをやめる**（永久 no-op に $0.01/6h を払わない）。

⚠️ **部分失敗は Go の error ではなく応答の `Errors` に入る。** `err` だけ見ると
拒否されたキーを「有効化した」と記録し、数か月後に列が無いことで初めて気づく。

⚠️ **この権限を外してもクラウド費用の画面は壊れない。** CP は「自動有効化できない」と
報告し、運用者が CLI でやる。

### 67.5.2 ⚠️ タグは **CloudFormation では入らない**（権限は入る）

よくある誤解なので先に潰す。**この機能のうち CFN に入るのは IAM 権限だけである。**

| もの | 誰が入れるか |
|---|---|
| `ce:*`（権限） | **CloudFormation**（`20-platform.yaml`）✅ |
| リソースへのタグ（`af-membership` / `af-tenant` …） | **実行中の CP**。CFN ではない |
| コスト配分タグの**有効化** | **実行中の CP**（67.5.1a）。CFN では**表現できない** |

**タグ**: 起動テンプレート（`40-ec2-pool.yaml`）が持つタグは
**`af-managed-by=agent-fleet` の 1 つだけ**である。`af-pool` / `af-role` / `af-slot-size`
は CP が `RunInstances` で、`af-membership` / `af-tenant` はさらに後（スロット確保時）に
打つ。home ボリューム・スナップショット・EFS アクセスポイントも作るのは CP。
README の「静的な土台＝CFN／ワークスペース単位＝実行中の CP」の境界どおり、
**課金タグは全部あとがわ**にある。

**有効化**: そういう CFN リソース型が存在しない（実測）。

```
AWS::CE::CostCategory        → 在る
AWS::CE::CostAllocationTag   → TypeNotFoundException
```

### 67.5.3 新規デプロイでは非対称が消える——**必ず数日欠測する**

67.5.4 の「`af-membership` は即座に通った」は**この sandbox 固有**の事情である
（当初から使われていて AWS が発見済みだった）。**まっさらなアカウントに今日デプロイ
すれば、全キーが `af-tenant` と同じ扱い**になる:

```
CFN をデプロイ（権限が入る）
  → 誰かがワークスペースを 1 回起動（CP がタグを打つ ← ここが CFN ではない）
  → AWS がキーを発見（〜24h）
  → CP が次の tick で自動的に有効化する（67.5.1a・人の作業は無い）
```

⚠️ **それでも、発見待ちの分の実費は必ず欠測する。**バックフィルが無いので避けられない。
自動化が消したのは「翌日に人が覚えている必要」だけである。**最小化の実務は
「デプロイしたらすぐワークスペースを 1 つ起動する」**（誰も使っていなくてよい）——
そこから先は CP がやる。画面は待っている間ずっと、失われつつあることを警告する。

### 67.5.4 なぜ `af-membership` は即座に有効化できて、`af-tenant` はできなかったのか

**同じ種類のキーである。**違ったのは「AWS がそのキーを既に見つけていたか」だけ。

```
$ aws ce list-cost-allocation-tags          # 有効化する前に見えていたもの
af-membership   Inactive   LastUsedDate: 2026-08-01T00:00:00Z
af-pool         Inactive   LastUsedDate: 2026-08-01T00:00:00Z
af-role         Inactive   LastUsedDate: 2026-08-01T00:00:00Z
…
（af-tenant は 1 行も無い）
```

`af-membership` はデプロイ当初から home EBS ボリュームと EFS アクセスポイントに
付いていたので、AWS は請求データの中でそれを見ており、**`Inactive` という行を既に
持っていた**。有効化はその行を `Inactive → Active` へ**倒すだけ**なので即座に通る。

`af-tenant` は今日 CP に実装して初めて打ったキーで、**倒す行が存在しない**。だから
`ValidationException: Tag keys not found` になる。⚠️ **差はキーの種類ではなく、
そのキーが実リソースに現れてからの経過時間である。**将来また新しいタグキーを足せば、
そのキーも同じ待ちを通る。

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

## 67.15 「このユーザーはいくら」を見る場所——メンバー詳細に足す（2026-08-18）

P4 で作った 3 面（管理／テナント設定／個人設定）は揃っているのに、**「この人はいくら
使っているのか」を見に行く場所としては一覧しか無かった**。管理モーダルの
**テナント管理 → メンバー詳細**（`console/src/features/settings/tenantMembers.tsx`）は
mem/CPU/disk とセッション一覧を出していて、そこに強制停止と上限設定のボタンが
並んでいるのに、費用だけが無い。

### 67.15.1 なぜ「合計の再掲」で終わらないか

詳細に費用を置く価値は、一覧の数字を繰り返すことではなく、**同じ画面にある操作と
1 対 1 で対応する読みが手に入ること**にある:

| 詳細で読めること | 同じ画面にある操作 |
|---|---|
| 日次が週末も含めて平らに乗り続けている＝スロットを握りっぱなし | **ワークスペースを強制停止** |
| 内訳が EBS（home ボリューム）に寄っている | **上限を設定**（ディスク GB） |
| 内訳が EC2 Compute（スロット時間）に寄っている | 同（メモリ＝スロット段） |

`attributable` は 3 種（`slot_hours` / `home_volume` / `snapshots`）しかないので、内訳は
**3 行前後**にしかならない。「サービス別は重い」という直感はこの機能には当たらず、
逆に内訳を落とすと詳細は一覧の再掲になる。**採ったのは「合計＋日次推移＋内訳」＋期間入力。**

⚠️ **リソースのタイル（`res-tiles`）に 4 枚目として足さない。** あれは 4 秒ポーリングの
「今」で、費用は約 24 時間遅れの「期間」である。同じカードに時間と $ を並べないのは
ADR 0048 決定 2。**独立した `admin-panel` を、リソースの直後・セッションの前に置く。**

### 67.15.2 API——**広げる前に、導出できるかを確かめた**

`GET /api/admin/cloud-cost` の `members[]` は
`{tenant, membership_id, user_key, email, unblended_micro, amortized_micro}` **だけ**である。つまり:

- **合計だけなら既存応答から絞れる**（`user_key` で引ける）。
- **日次推移とサービス別内訳は、既存のどの応答にも入っていない。** そして 67.15.1 の
  とおり、詳細に置く価値はその 2 つの方にある。

`/stats` に相乗りさせる案は採らない。あれは **4 秒ポーリング**で、費用は 6 時間更新の
DB 読みである（900 倍の頻度で同じ値を読み直すことになる）。加えて `/stats` は
ワークスペースが無いとき `{"running":false}` で早期 return するが、**費用は停止中・破棄後の
メンバーにも存在する**。

→ **`GET /api/admin/tenants/{slug}/members/{key}/cost?from=&to=` を新設**（ゲートは
`/stats` `/sessions` と同じ `tenantAdminFor` ＋ `resolveMember`）。応答は
**`/api/cost/me` と完全に同一の形**で、CP 側は集計を `oneMemberCloudCost` に切り出して
両方から呼ぶ。**新しい DTO は 1 つも増えていない**——既存の形の呼び手が 1 つ増えただけ。
store も追加不要（`ListCloudCost` は既に membership で絞れる）。

⚠️ **`tenantID` には空文字を渡す。** `tenantByMembership` は「今の workspace 行」から
テナントを解決するので（`cloudcost.go`）、**ワークスペースを破棄したメンバーの直近ぶんは
`tenant_id` が空で書き直される**。テナントで絞ると、その人の詳細に**自信たっぷりの
$0.00** が出る。所属は `resolveMember` が既に証明しているので、membership だけで引く。
テストで固定した（`TestMemberCloudCostStillFindsSpendWhoseTenantWasLost`）。

### 67.15.3 二重に持たない

- **`CloudCostAdminView` は使い回せない**——「メンバー行の一覧＋共有カード」で、
  テナント選択欄も持っている。1 人分とは形が違う。
- **`MyCloudCostView` の中身を抽出した**: `useCostOne`（取得）/ `CostRangeBar`（期間）/
  `CostOneBody`（合計・但し書き・日次・内訳）。本人向けとメンバー詳細は**ラッパだけ**が
  違い、変わるのは合計ラベルのキー 1 つ。一覧のツールバーも `CostRangeBar` に寄せた
  （テナント選択は `children` で差し込む）。
- ⚠️ **CSS のスコープ**: `cost.css` は全部 `.cloud-cost` 配下で、そのクラスは
  `admin-stage` に付いていた。詳細側は `section.admin-panel.cloud-cost.member-cost` と
  部品の root 自身に持たせないと、棒グラフも金額の右寄せも効かない。

### 67.15.4 ラベルと RBAC

- 二人称の `cost.my_*` は流用できないので `cost.member_*` を新設。⚠️ **「このメンバーの
  コスト」とは絶対に書かない**——実測で人に紐づくのは 22.3% なので、そう書くと会社が
  払っている額の 1/5 を指すことになる。合計ラベルは
  **「このメンバーに直接ひも付く費用（共有分は含みません）」**で固定し、DOM テストで縛った。
- **`CostNotes` を必ず一緒に運ぶ。** これが無いと、タグ有効化より前の期間の 0 が
  「この人は無料」と読まれ、その 0 は永久に自己訂正しない。
- **共有は構造的に入らない**——共有行は `membership_id` が空なので、実在の membership で
  絞れば定義上 1 行も来ない。「返さないよう気をつける」実装にしていない。
- **能力の確認は部品が自分でやる**（`useCostProfile`）。prop で渡す形にすると、渡し忘れた
  瞬間に**請求の無いデプロイ（docker / native）に金額 0 の面が出る**。

### 67.15.5 目視——管理のドリルダウンは**そもそも描けなかった**

⚠️ `console/scripts/shots` のフィクスチャサーバには**管理のドリルダウンのスタブが
1 本も無かった**（あったのは `/api/tenants` と `/api/admin/cloud-cost` だけ）。
`/api/admin/tenants`・`.../members`・`/stats`・`/sessions`・`/cost` と
`/api/admin/workspace-sizing` を足し、`--admin`（`SHOTS_ADMIN=1`）でだけ入口が出るように
した——⚠️ 既定で出すとアカウントメニューに項目が 1 つ増え、**README のスクショが黙って
変わる**。

自前 Chromium ＋素の CDP で実際に描かせて、直したのは 2 件（どちらも DOM テストでは出ない）:

- **期間の入力欄に大きい金額が貼り付いていた。** 個人向けは「期間」と「数字」が別カード
  なので枠線が間を作るが、詳細は 1 枚なので空けないと読めない（`.member-cost .usage-toolbar`）。
- **導入文が 3 行で、他のカードと並べると浮いていた。** 専用面と同じ長さは詳細には重い。
  規律（共有は含まない）を保ったまま 1 文に詰めた。

実測（1440px）: パネル 774×497、日次 30 本・目盛り 10 本で**重なり 0**、内訳の金額は
3 行とも右端が揃う。1024px でも重ならず、700px では目盛りが設計どおり消えて溢れは無い。
⚠️ **テーマは `prefers-color-scheme` では切り替わらない**（`<html data-theme>` が正）。
メディアだけ切り替えて「明色でも見えた」と言うと、実際には暗色を 2 回撮っただけになる。

### 67.15.6 実機に入れた（2026-08-18・<dev-deployment>）

⚠️ **今回は 67.13.1 の「af-cp の層 1 枚だけ」が使えない**——Console を変えたので、
**`/usr/local/bin/af-cp` と `/app/console` の両方**を 1 レイヤに入れて `crane append` した
（docker 無し）。「Console だけ古い CP」が構造的に起こり得ないのは**同じレイヤに両方
入れたから**であって、前回のように「触っていないから」ではない。

**push 前**（レイヤ tar と**展開後イメージ**の両方で照合）:

- `usr/local/bin/af-cp` の sha256 がビルドした物と一致（`36a813ff…`）——tar 内と、
  `crane export` で全層をマージした後の両方で確認。
- `app/console/index.html` が参照するローカル資産（`assets/index-BzvBUpw8.js` /
  `index-C6zVOAnO.css` / `brand/apple-touch-icon.png` / `brand/icon-192.png` /
  `manifest.webmanifest`）が展開後イメージに**全て実在**。
- ⚠️ **「入れ替わった」ではなく「中身が新しい」まで見る。** そのエントリバンドルに
  `cost.member_total_label` が在り、`af-cp` に `members/{key}/cost` が在ることを実体で確認した。
  資産を差し替えれば digest は必ず変わるので、**digest が変わったことは中身の証明にならない**。
- 参考: 下層に古い資産が 441 個残る（`index.html` は参照しないので無害）。層を重ねる方式の
  当然の帰結で、焼き直しでしか消えない。

**push 後**: 走っているタスクの `imageDigest` が push した digest と一致
（`sha256:e625f33d…`。旧 `sha256:186c0124…` から入れ替わった）。起動ログはクリーンで、
**`cloud cost: 47 rows over 7 days`**——CE の読みは `CpTaskRole` で通ったままである
（権限の証明を同じ経路で取り直した・67.13.2 と同じ型）。`/healthz` 200、新ルート
`/api/admin/tenants/{slug}/members/{key}/cost` は **401**（＝生きていて認証で止まる。
既存の `/stats` と同じ）。

⚠️ **ログイン後の画面は実機では見られない**（Google が弾く）ので、`/app/console` の資産も
外からは 401 で取れない。**「配信されている画面が新しい」ことの根拠は、push 前の展開後
イメージの照合と、走っているタスクの digest 一致の 2 つだけである。** 目視はフィクスチャ
サーバ側で済ませてある（67.15.5）。

**この時点の実データ**（メンバー詳細に出る数字の裏取り）:

```
2026-08-17  af-membership$d6e8070a484b950b4c71474bbbbdd95a   $0.0387
2026-08-17  af-membership$（共有・未タグ）                    $3.4218
```

⚠️ **`af-tenant` はまだ `list-cost-allocation-tags` に 1 行も出ていない**（打ったのは
8/17 06:18 UTC、この確認が 8/18 03:47 UTC ＝約 21.5 時間後）。発見の「〜24h」の中では
あるが、**まだテナント軸の実費は 1 日も溜まっていない**。CP は 6 時間 tick で自動的に
再試行し続ける（決定 11）ので、人の作業は無い。

## 67.16 予約メンバーシップの費用は共有インフラへ（2026-08-22・ADR 0048 決定 13/14）

golden スナップショットの自動焼き直し（docs/64 §64.28）を実デプロイ 2 本で実走させたら、
`af-golden-seed` / `af-golden-probe` が**人のメンバーとして per-member の一覧に出た**。
種と probe は「製品の通常の Start 経路」で workspace を作る——そうでなければ焼けた golden は
「製品が実際に作る home」の複製ではなくなる——ので、他の誰とも同じに `af-membership` が付く。

- 正しい置き場は**共有インフラ（§67.7 の SHARED）**。デプロイが自分のスナップショットを
  温めている費用であって、誰かの仕事ではない。
- ⚠️ **タグを打たない案は採らない。** `af-membership` は配分キーであると同時に、ランタイムが
  EFS アクセスポイントと home ボリュームを引き当てる**照合キー**でもある。空にすると
  引き当てが壊れるか、次に現れた無タグ資源と衝突する。→ **畳むのは取り込み側**。
- ⚠️ **畳んだら合算する。** `PutCloudCost` は `(day, membership_id, service)` を**置き換える**
  ので（§67.10 の「置き換えであって加算ではない」）、足さずに 2 行渡すと**後の行が前の行の
  金額を消す**。CE は種の行と無タグの共有行を同じ (day, service) の別グループとして返す。
- 取り込みの窓は既定 7 日。それより古い既存行は畳まれないので、読み側でも同じ畳み方をする。
  **財務データを書き換えるマイグレーションは書かない。**
- golden スナップショット自体は元から `af-membership` 無し＝すでに shared
  （`deploy/aws/ecs/README.md`）なので、これで種・probe・スナップショットの 3 つが揃う。

**メンバーの行を消しても費用の行は消さない。** 除名済みメンバーの物理削除（docs/61 §61.18）を
入れたが、`cloud_cost_daily` は cascade に入れていない。§67.15 の ⚠️「membership だけで引く」は
**破棄されたメンバーの支出も消えない**ようにするための設計で、行を消すと過去月の合計が後から
変わる。`CloudCostTotal` は元々「membership が消えていれば UserKey/Email が空のまま出る」ので、
表示も破綻しない。
