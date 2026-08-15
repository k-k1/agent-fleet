# 64. ECS の Workspace を EC2 起動タイプ ＋ インスタンス stop/start で持つ

> 状態: 調査完了・**採用（実装中）**（2026-08-15）。決定は
> [ADR 0045](decisions/0045-ec2-persistent-workspace.md)。
> ⚠️ **§64.2〜§64.14 は「見送り」を結論としていた調査の記録である。** 同じ日のうちに利用者の判断で
> **採用に転じた**（ADR 0045 決定 10。§64.10 の判定ゲートは**充足していない**）。
> **採る形はプール型（§64.12）**で、**実装の設計は §64.15**。
> **成立性は AWS sandbox で端から端まで実測した**（deploy → 計測 → teardown を 1 セッションで閉じ、
> 残存リソース 0 を確認済み・§64.14）。**第 2 ラウンドで「プール ＋ EBS 差し替え」と
> 「初回ユーザー用 golden snapshot」も実測した**（§64.12 / §64.13）。
> 関連: [63-workspace-sizing.md](63-workspace-sizing.md)（EFS が遅い実測と `~` の置き場の決定） /
> [ADR 0044](decisions/0044-workspace-sizing.md) 決定 4（「本当に必要なら EC2 ＋ stop」と書いた箇所） /
> [62-ecs-start-latency.md](62-ecs-start-latency.md) §62.5 (d)（EC2 起動タイプを却下していた箇所・本書で改訂） /
> [history/p3-7-aws-adapter.md](history/p3-7-aws-adapter.md) §20b.7（EFS を選んだ凍結仕様）
> 対象（採るなら）: `control-plane/runtime_ecs.go` / `deploy/aws/ecs/cfn/` / `workspace/entrypoint.sh`

## 64.1 なぜ再検討するか

[63](63-workspace-sizing.md) §63.4 の実測で、EFS は**小ファイルに対して 8〜30 倍遅い**（1 ファイル
作成 15.4ms・並列度を上げても vCPU を増やしても改善しない）ことが分かった。そして §63.5.5 で
**Fargate には「速くて永続」が存在しない**ことが API 定義から確定した——ECS 管理 EBS はサービス経路では
必ず削除され、`RunTask` 経路で残しても**既存ボリュームを指す項目が API に無い**ので再アタッチできない。

ADR 0044 決定 4 はそこで「本当に必要になったときの答えは EC2 起動タイプ ＋ インスタンス stop
（停止してもボリュームが残る）」と書いて別案件に送った。本書がその別案件である。

## 64.2 結論（先に）

**技術的には成立する。実測でライフサイクルが端から端まで通った。** ただし**今は採らない**。

> ⚠️ **2026-08-15 追記（第 2 ラウンド）: 形は 1 つではない。**
> 「インスタンスを停止しておいて EBS を差し替えられないか」という問いから、**汎用インスタンスの
> プール ＋ ユーザー毎 EBS の差し替え**を追加で実測した（§64.12）。結果、**起動は 22〜27 秒**まで
> 落ちる——下の「起動は速くならない」は **1 ユーザー = 1 インスタンス固定**の形についての結論であって、
> プール型には当てはまらない。**プール型は Fargate（~105s）の 1/4 で、しかも永続 home を保つ唯一の形**である。
> 併せて**初回ユーザー用 golden snapshot**（§64.13）も実測した（小ファイルではハイドレート税が
> **ゼロ**だった）。結論の骨格（今は採らない）は変わらないが、**採るときの形はプール型**である。

- ✅ **永続 EBS は本当に永続だった。** home を追加 EBS に載せると、インスタンスの stop/start でも、
  インスタンスの **terminate → 新インスタンスへの付け替え**でも中身が残る（§64.4.2）。
- ✅ **イメージキャッシュが効く。** 2 回目以降の pull は **31.8s → 0.09s**（§64.4.2）。
- ✅ **速い。** home 上の小ファイル 2,000 個作成が **0.04s**（EFS は 30.7s）。
- ✅ **安い。** 月 160h 稼働・home 45 GiB で **$28.5 vs 現状 $39.6**、完全アイドル月は
  **$7.7 vs $16.3**、snapshot へ退避すれば **$1.0**（§64.7）。
- ❌ **（1 ユーザー = 1 インスタンス固定なら）起動は速くならない。** stop→start→タスク RUNNING が
  **83.5s** で、Fargate の温ホーム再 Start（~105s のうち同じ地点まで ~84s）と**ほぼ同じ**。
  pull の 35s が消える代わりに、インスタンス起動 ＋ ECS 再登録 ＋ 配置に 33s 払う（§64.4.3）。
  → **プール型ならここが 22〜27s になる**（§64.12）。
- ❌ **代償が多い。** AZ 固定・**インスタンスタイプ変更を ECS が拒否する**・停止インスタンスは
  自動 deregister されない・awsvpc の ENI 残留でパブリック IP が付かなくなる、など**実測で見つけた罠が 4 件**
  （§64.5）。運用対象が 1 Workspace あたり 2 種類（サービス＋EFS AP）から 6 種類（インスタンス・
  ボリューム・スナップショット・コンテナインスタンス登録・サービス・タスク定義）に増える。

**したがって、価値は「起動の速さ」ではなく「I/O と永続と費用」にある。** そして I/O の大半は
ADR 0044 決定 3（`~` の置き場を平均ファイルサイズで分ける）が**先に、はるかに安く**取る。
EC2 化は決定 3 を実装して**なお足りないと実測で言えたとき**に着手する。判定ゲートは §64.10。
**ただし「起動を 30 秒未満にしたい」が要件になった場合だけは別**——それはプール型（§64.12）でしか
満たせないので、単独で着手理由になる。

## 64.3 計測環境

| | |
|---|---|
| リージョン | ap-northeast-1（デフォルト VPC のパブリックサブネット 1a・NAT / ALB / RDS 無し） |
| AMI | `ami-0235d4e680cef92d8`（AL2023 ECS 最適化・**ECS エージェント 1.106.0** / Docker 25.0.16） |
| インスタンス | m7i.large（2 vCPU / 8 GiB）・root 40 GiB gp3・**追加 20 GiB gp3（`DeleteOnTermination=false`）** |
| イメージ | **本番と同じ** `ghcr.io/k-k1/agent-fleet/workspace:0.8.0`（918 MiB）を `crane copy` で ECR へ |
| タスク | ECS サービス・**EC2 起動タイプ**・配置制約 `attribute:af-membership == m-test`・Service Connect 有効 |
| home | 追加 EBS を `/af-home` にマウントし、タスクへ `host` ボリュームで `/home/dev` に bind |

コンテナインスタンスは user-data で `ECS_CLUSTER` と
`ECS_INSTANCE_ATTRIBUTES={"af-membership":"m-test"}` を `/etc/ecs/ecs.config` に書くだけで
クラスタに参加し、**属性もそのまま登録された**（配置制約の材料が user-data だけで揃う）。

## 64.4 実測

### 64.4.1 「1 ユーザー = 1 インスタンス」は組める

| 区間 | 実測 |
|---|---|
| `RunInstances` → EC2 running | 8.0s |
| → **ECS 登録（ACTIVE / agentConnected）** | **24.2s**（running から 16.2s） |
| `CreateService`（初回）→ タスク作成 | 51.0s |
| → タスク RUNNING（**cold pull 込み**） | **119.2s**（provision 24s / **pull 31.8s** / start 12s） |

配置制約 `memberOf attribute:af-membership == m-test` でそのインスタンスにだけ載る。
**`PlacementConstraint` は Fargate では使えない**（SDK のドキュメントに明記）ので、
これは EC2 起動タイプ固有の道具である。ASG もキャパシティプロバイダも**要らなかった**——
`launchType=EC2` は素の登録済みインスタンスに対してそのまま働く。

### 64.4.2 永続とキャッシュ —— ここが本命

`/home/dev/marker.txt` に起動のたび 1 行追記し、行数で永続を確認した。

| 経路 | home の中身 | pull | 所要 |
|---|---|---|---|
| インスタンス **stop → start** | **残る**（3 行 → 8 行と積み上がった） | **0.09s** | §64.4.3 |
| インスタンス **terminate → 新インスタンスへ付け替え** | **残る**（8 行すべて） | 32.8s（root が新品なので再 pull） | terminate 49s ＋ 再構築 82s ＋ タスク 70s ＝ **122s** |

- terminate で **root ボリュームは消え、home ボリュームは `available` で生き残った**
  （`DeleteOnTermination=false`）。新インスタンスへ `AttachVolume` して同じ user-data を通すと、
  fstab の再作成 → マウント → タスク起動まで自動で戻る。
- **イメージキャッシュはインスタンスの root ボリュームに乗る。** stop/start では残り
  （pull 31.8s → **0.09s**）、terminate では失われる。ここが 2 つの経路の分かれ目。

`/home/dev`（gp3 EBS）上の I/O は §63.4.2 の EFS / Fargate ローカルと比べてこうなる:

| 項目 | **EBS gp3（本計測）** | EFS bursting（§63.4.2） | Fargate ローカル（§63.4.2） |
|---|---|---|---|
| 小ファイル作成 2,000（直列） | **0.04s** | 30.7s | 1.9s |
| `dd` 1 GiB 書き込み（`conv=fsync`） | 7.4s（≒138 MiB/s） | 9.3s | 8.2s |
| 4 GiB 逐次読み（`iflag=direct`） | 31.8s（**135 MB/s**） | — | — |

小ファイルのペナルティは**消える**（NFS の往復が無くなるので当然だが、実測で確認した）。
逐次帯域は gp3 の既定（3,000 IOPS / 125 MiB/s）にきれいに張り付く。それ以上要るなら
IOPS $0.006/IOPS-月・スループット $0.048/MiBps-月 の追加課金で買える（例: 500 MiB/s で +$18/月）。

### 64.4.3 stop/start の所要時間 —— 起動は速くならない

| 区間 | 方式 A（起動 → 登録待ち → desired 1） | 方式 B（desired 1 を先に置く） |
|---|---|---|
| desired 0 → タスク消滅 | 12.9s | 6.9s |
| `StopInstances` → stopped | 48.8s | 79.4s |
| `StartInstances` → EC2 running | 18.7s | 20.9s |
| → ECS 再登録 | 20.0s | 22.0s |
| → タスク作成 | 33.0s | 45.5s |
| → **タスク RUNNING** | **83.5s** | 94.7s |

- **方式 B も成立する**（インスタンスが停止したまま desired 1 を置いても、容量が現れてから
  **約 23 秒**で ECS が配置した）。ただし方式 A の方が 11 秒速い。
- Fargate の温ホーム再 Start は **~105s**（[62](62-ecs-start-latency.md) §62.10.3。内訳は
  API→作成 4〜8s ＋ provision 16s ＋ **pull 35s** ＋ pull→started 25s ＋ entrypoint 21s）。
  同じ地点（タスク started）まで比べると **Fargate ~84s に対し EC2 83.5s** で、**差は無い**。
  **pull が消えた 35 秒を、インスタンス起動 19s ＋ ECS 再登録 1s ＋ 配置 13s で使い切る。**

タスクだけを起こす場合（インスタンスは起動済み）の内訳も測った。ここは**ネットワークモードと
Service Connect が効く**:

| 形 | desired 1 → RUNNING | created → started |
|---|---|---|
| awsvpc ＋ Service Connect（現構成と同じ形） | 40.7s | 36s |
| bridge ＋ Service Connect | — | 34s |
| **bridge ＋ SC 無し（固定ホストポート）** | **22s** | **16s** |

**awsvpc の ENI 割当と Service Connect のセットアップで 20 秒前後を払っている。**
1 インスタンス 1 タスクなら bridge でも分離は落ちない（SG はインスタンスに付く）ので、
起動を縮めたいなら**ここが唯一まとまった削りしろ**。ただし SC を外すと CP は Agent の
アドレスを自前で解決することになり（インスタンスの private IP ＋ 固定ポート）、
`runtime_ecs.go` の `Endpoint()` 契約が変わる。

### 64.4.4 Service Connect は EC2 起動タイプでも動く

**Fargate のクライアントサービス（＝ CP の役）から、EC2 起動タイプのタスクへ
`http://af-ec2probe-ws:7700/` で疎通した**（`ok` を 10 秒おきに取得）。
タスクは自分の ENI（`172.31.2.172`）を持ち、`ecs-service-connect-*` サイドカーが同居し、
`attachments` に `ServiceConnect: ATTACHED` が出る。bridge モードのサービスでも同様に上がった。

→ **CP ↔ Agent の到達手段は作り直さなくてよい**。`Endpoint()` は `http://<name>:7700` のままでよい。

### 64.4.5 CPU/メモリの予約はインスタンスの取り合いになる

タスク定義の `cpu` は EC2 では**インスタンスの CPU units を予約する**。2 vCPU = 2048 units の
インスタンスに `cpu:1024` のタスクを 2 本置いた時点で満杯になり、3 本目は
`has insufficient CPU units available` で置けなかった（実測）。

裏を返すと、**EC2 ではタスクレベルの `cpu` を省略できる**——予約せずインスタンスを丸ごと使える。
Fargate の 74 通りの飛び飛び（§63.2）から解放され、サイズはインスタンスタイプの選択になる。
ディスクも ephemeral の 200 GiB 上限（§63.2）が消え、EBS のオンライン拡張が使える。

## 64.5 実測で見つけた罠（4 件）

### (1) インスタンスタイプの変更を ECS が拒否する —— これが一番痛い

stop 中に `ModifyInstanceAttribute` で m7i.large → m7i.xlarge に変えて start したところ、
ECS エージェントが **terminal exit** して二度とクラスタに戻らなかった:

```
ClientException: Container instance type changes are not supported.
Container instance 3807aad8... was previously registered as m7i.large.
level=critical msg="Agent will terminally exit, unable to register container instance"
```

**復旧手順は実測で確立した**（46 秒で 4096 CPU として再登録され、`af-membership` 属性も維持）:

```
aws ecs deregister-container-instance --cluster <c> --container-instance <ci> --force
# インスタンス側
rm -f /var/lib/ecs/data/*        # エージェントのチェックポイント（旧 ARN と旧タイプ）を捨てる
systemctl restart ecs
```

→ **「サイズ変更」は CP が上の 3 手を踏む機能として実装しなければならない。**
ECS 側のコンテナインスタンス ARN は変わる（配置制約は属性ベースなので影響しない）。

### (2) 停止インスタンスは自動 deregister されない

SDK のドキュメント（`DeregisterContainerInstance`）に明記されている:

> If you terminate a running container instance, Amazon ECS automatically deregisters the instance
> from your cluster (**stopped container instances or instances with disconnected agents aren't
> automatically deregistered when terminated**).

→ **Workspace を削除するとき、CP は `DeregisterContainerInstance` を明示的に呼ぶ必要がある。**
呼ばないとクラスタにゴースト登録が積もり、配置制約がゴーストに一致して「置けるはずなのに置けない」
状態を作る。停止中のコンテナインスタンスは実測でも `status=ACTIVE / agentConnected=false` のまま残っていた。

### (3) awsvpc のタスク ENI が停止をまたいで残り、パブリック IP を奪う

エージェント切断中は、停止済みタスクの ENI がインスタンスに刺さったままになる（実測で 2 本残留）。
その状態で start すると **インスタンスが複数 ENI 構成になり、自動割当パブリック IPv4 が付かない**。
パブリックサブネットで検証していたため egress が消え、**ECS エージェントが 11 分間再接続できなかった**
（主 ENI に EIP を付けたら 11 秒で復帰し、その直後に残留 ENI は自動回収された）。

→ 本番の ws サブネットはプライベート ＋ NAT なので**この経路では露出しない**が、
**「パブリック IP に依存した設計にしない」ことと、ENI スロット（m7i.large は 3 本）を
1 タスクで使い切らないこと**は前提条件として残る。

### (4) 停止直後の再起動は ECS が捌けない（Fargate と同じ）

[62](62-ecs-start-latency.md) §62.10.3 で判明した「前タスクの後片付けと次の配置が重なる」問題は
EC2 でも同じで、`minimumHealthyPercent` を触っても解消しない。運用（reaper が止め、数分〜数時間後に
ユーザーが起こす）ではまず踏まない。

## 64.6 ECS Managed Instances は答えにならない（SDK 一次情報）

2025 年に追加された **ECS Managed Instances**（`CapacityProviderType=MANAGED_INSTANCES` /
`LaunchType=MANAGED_INSTANCES`）は「EC2 の自由度で、インスタンス管理は AWS がやる」という
今回の要望に最も近く見えるが、**永続ボリュームの置き場としては使えない**。
`aws-sdk-go-v2/service/ecs@v1.87.0` の型定義で確認した:

| 型・フィールド | 何が書いてあるか | 帰結 |
|---|---|---|
| `ManagedInstancesStorageConfiguration.StorageSizeGiB` | 「インスタンスの**データボリュームのサイズ**」 | 既存ボリュームを指す項目が無い＝毎回新規 |
| `ManagedInstancesLocalStorageConfiguration.UseLocalStorage` | インスタンスストアを使う | 揮発 |
| `InfrastructureOptimization.ScaleInAfter` | **アイドル/低使用のインスタンスを最適化（＝終了）するまでの秒数** | ECS がインスタンスを**終了**する。stop は無い |
| `AutoRepairConfiguration.ActionsStatus` | IMPAIRED なインスタンスを**自動で置き換える** | 置き換え＝home が消える |
| `InstanceLaunchTemplate.InstanceRequirements` | vCPU/メモリ範囲でタイプを**自動選択** | どのタイプで起きるか決められない＝§64.5 (1) と相性が悪い |

**ライフサイクルの所有者が ECS 側にある**（stop ではなく terminate、しかも自動で）ため、
「使わないときは止めるが、ディスクは自分のものとして残す」という形は原理的に作れない。
Managed Instances は「Fargate の揮発性を EC2 の価格と自由度で得る」ものであって、永続の話ではない。

## 64.7 長期未使用ユーザーの退避（snapshot）

「使っていないユーザーのボリュームは snapshot に落として volume を消す」を実測した。

| 操作 | 実測 |
|---|---|
| `CreateSnapshot`（20 GiB ボリューム・実データ 1.19 GB） | **106s** で completed |
| 同（実データ 5.45 GB） | **267s**（≒ 20 MB/s。**45 GiB 分なら 30〜40 分**の桁） |
| snapshot → `CreateVolume` → available | **3s** |
| → `AttachVolume` 完了 | **7s** |
| 復元ボリュームの**初回**読み（4 GiB・direct） | **57.4 MB/s** |
| 同じボリュームの 2 回目 | 131 MB/s |
| 元のボリューム | 135 MB/s |

- **復元は速いが、初回アクセスは 2.3 倍遅い**（S3 からの遅延ハイドレート）。45 GiB を全部触ると
  ざっと 14 分の I/O 税だが、実際には触った分だけなので「初日は少し重い」程度に散る。
  潰したいなら `CreateVolume.VolumeInitializationRate`（100〜300 MiB/s・有償）があるが、
  **Fast Snapshot Restore は $0.90/時（＝$648/月）** なので per-user には論外。
- **アーカイブ階層（$0.0125/GB-月）は「即復帰」には使えない。** `ModifySnapshotTier` は
  `archive` のみを受け、戻すには `RestoreSnapshotTier` が要り、**AWS の仕様上復元に 24〜72 時間**
  かかる（最低 90 日課金も付く）。使うなら「休眠アカウント」という別の状態にして、
  Console で「復帰に最大 3 日かかります」と見せる種類の機能になる。
- **退避の判断はサイズではなく最終利用日で行う。** snapshot 課金は**使用ブロックのみ**
  （$0.05/GB-月）で、ボリューム課金は**プロビジョニング量**（$0.096/GB-月）なので、
  実使用 20 GiB / プロビジョン 50 GiB のユーザーなら **$4.80 → $1.00** になる。
- 退避と復帰はどちらも**非同期**にできる（退避は「タスク停止 → terminate → snapshot → volume 削除」、
  復帰は「volume 作成 → 新インスタンス → attach」）。ユーザーを待たせるのは復帰側の
  §64.4.2 の 122 秒だけで、snapshot の 30 分は待たせない。

## 64.8 費用（ap-northeast-1・2026-08-15 に Pricing API で実測）

単価: m7i.large **$0.1302/時** ／ Fargate 2 vCPU+8 GB **$0.14536/時**（vCPU $0.05056・メモリ $0.00553/GB）
／ EBS gp3 **$0.096/GB-月** ／ EFS Standard **$0.36/GB-月** ／ EBS snapshot **$0.05/GB-月**
／ snapshot archive **$0.0125/GB-月**（取り出し $0.03/GB・最低 90 日） ／ FSR **$0.90/時**。

月 160 時間稼働・home 実使用 45 GiB のユーザー 1 人あたり:

| 案 | compute | ストレージ | **計/月** |
|---|---|---|---|
| **A 現状**（Fargate ＋ EFS 45 GiB） | $23.26 | EFS $16.31 | **$39.57** |
| **B ADR 0044 決定 3**（置き場を分割・EFS は 22 GiB） | $23.26 | EFS $7.85 | **$31.11** |
| **C EC2 ＋ 永続 EBS**（root 30 ＋ home 50 GiB） | $20.83 | EBS $7.68 | **$28.51** |
| C を 8 vCPU/32 GiB 相当にした場合 | m7i.2xlarge $83.33 | $7.68 | $91.01 |
| （参考）同じサイズの Fargate（8 vCPU / 32 GiB） | $93.03 | EFS $16.31 | $109.34 |

稼働 0 時間の月（＝しばらく使わなかったユーザー）:

| 案 | **計/月** | 100 人分 |
|---|---|---|
| A 現状 | $16.31 | $1,631 |
| B 決定 3 | $7.85 | $785 |
| C EC2（インスタンス停止・EBS だけ） | **$7.68** | $768 |
| **D C ＋ snapshot 退避**（実使用 20 GiB） | **$1.00** | **$100** |
| D' さらに archive 階層（復帰に 24〜72h） | $0.25 | $25 |

**費用は EC2 案の弱点ではなく、むしろ強み。** ただし性質が変わる点に注意:
**EFS は「使った分」課金、EBS は「確保した分」課金**なので、損益分岐は
**充填率 0.096 / 0.36 = 26.7%**。home をスカスカに大きく取ると EBS の方が高くつく。
（本セッションの Workspace は 45.3 GiB 使用なので、50〜60 GiB 確保なら EBS が圧倒的に有利。）

## 64.9 採るならこの形（設計案）

構成単位は **1 membership = 1 EC2 インスタンス ＋ 1 home EBS**。
ADR 0012 の「アダプタは CP に状態を持たない」は**維持できる**——すべてタグと属性で引けるため。

| 資源 | 引き方 |
|---|---|
| インスタンス | `DescribeInstances` filter `tag:af-membership`（1 件に限定） |
| home ボリューム | `DescribeVolumes` filter `tag:af-membership`, `tag:af-role=home` |
| 退避済み snapshot | `DescribeSnapshots` filter 同上 |
| コンテナインスタンス | 属性 `af-membership`（user-data で名乗る）＋配置制約 |
| サービス / タスク定義 | 現行どおり `ContainerName` |

- **Start**: インスタンス無し → `CreateVolume`（snapshot があればそこから）＋ `RunInstances`
  → stopped → `StartInstances` → **agentConnected を待つ**（実測 20s） → `RegisterTaskDefinition`
  （`placementConstraints: attribute:af-membership == <id>`）→ `UpdateService desired 1`。
- **Stop**: `desired 0` → タスク消滅を待つ（7〜13s）→ `StopInstances`（応答は待たなくてよい）。
- **サイズ変更**: Stop → `ModifyInstanceAttribute` → **`DeregisterContainerInstance --force` ＋
  `/var/lib/ecs/data/*` 削除 ＋ `systemctl restart ecs`**（§64.5 (1)。SSM か user-data 側の
  起動時チェックで踏む）→ Start。
- **退避 / 復帰**: §64.7。
- **削除**: タスク停止 → `TerminateInstances` → **`DeregisterContainerInstance`（自動では消えない）**
  → ボリューム／スナップショット削除。[ADR 0028](decisions/0028-deletion-lock.md) の削除ロックの対象が増える。
- **`State()`**: 「サービスの desired/running」だけでは足りず、**インスタンスの状態との組**で
  `none / stopped / starting / running` へ写す必要がある（インスタンス起動中＝ `starting`）。
- **EFS は捨てなくてよい。** 資格情報・identity（`homeKeep` の 7 つ・100 MiB 未満）だけを EFS の
  アクセスポイントに残し、それ以外を EBS に置く**ハイブリッド**にすると、「単一 AZ の EBS が
  1 本壊れたらログイン情報まで失う」を避けられる。EFS ボリュームは EC2 起動タイプのタスク定義でも
  同じ形で使える（**未実測**）。
- **AZ は固定される**（EBS は AZ を跨げない）。停止インスタンスは AZ を保持するので、
  **その AZ でそのタイプの容量が取れないと start が失敗する**。退避の snapshot は AZ を跨げるので、
  最後の逃げ道は「snapshot から別 AZ に作り直す」（§64.7 の 30 分）。

> **どちらの形を採るか**: 上は「1 ユーザー = 1 インスタンス固定」の形。**起動の速さを取るなら
> §64.12 のプール型**（汎用スロット ＋ EBS 差し替え）で、資源の引き方（タグ）と Stop/Start の
> 骨格はそのまま使える。違いは (a) 配置制約が `af-membership` 属性ではなく **`ec2InstanceId`**、
> (b) CP が `AttachVolume` / `mount` / `umount` / `DetachVolume` を握る、(c) スロットの空き管理が要る、
> の 3 点。**新規ユーザーの初回は §64.13 の golden snapshot から home を作る**のが両者共通の推奨。

## 64.10 判定ゲート（いつ着手するか）

> ⚠️ **このゲートは充足しないまま越えた**（ADR 0045 決定 10）。下の 5 つはどれも実測では言えていない。
> 2026-08-15 に利用者の判断で着手したので、以下は「何を測れば正当化できたか」の記録である。

ADR 0044 決定 3（`~` の置き場の分割）を実装したうえで、**次のいずれかが実測で言えたとき**に着手する。
言えないうちは着手しない——理由は §64.2 のとおり、EC2 化の効き幅の大半を決定 3 が先に取るため。

1. **決定 3 の後でも `~/repos` の操作が遅い**。具体的には EFS 上の実リポジトリで
   `git status` / `rg` が体感を壊す水準（例: 1 操作 5 秒超）で観測される。
2. **朝の再生成（`npm ci` ＋ 初回ビルド）が 5 分を超える**ユーザーが常態化する。
3. **EFS 課金がユーザーあたり月 $10 を超える**（＝置き場の分割で減らしきれていない）。
4. **Fargate のサイズ上限（16 vCPU / 120 GiB / ephemeral 200 GiB）に当たる**要望が出る。

なお 4 だけは決定 3 では解けないので、**単独で EC2 化の理由になる**。

**5 つ目のゲート（第 2 ラウンドで追加）: 「Start を 30 秒未満にする」が製品要件になったとき。**
Fargate は温ホームでも ~105s から動かせない（pull 35s ＋ provision 16s ＋ EFS マウント 25s ＋
entrypoint 21s のどれも構造的）。**22〜27s を出せるのはプール型（§64.12）だけ**なので、
この要件が立った瞬間に他の 4 つのゲートと無関係に着手理由になる。

## 64.12 汎用インスタンスのプール ＋ EBS の差し替え（第 2 ラウンド・2026-08-15）

§64.4 の形は「1 ユーザー = 1 インスタンス固定」だった。インスタンスをユーザーに紐づけず、
**汎用のインスタンスを何台か用意して、ユーザー毎の EBS だけを差し替える**とどうなるかを測り直した。
Service Connect は第 1 ラウンドで検証済みなので張らず、bridge ＋ 動的ホストポートで測っている。

### 64.12.1 実測

| 操作 | 実測 |
|---|---|
| **停止中のインスタンスへ `AttachVolume`** | **通る**（`rc=0` で 3 秒で `attached`） |
| 停止インスタンスの start → EC2 running | 19s |
| → ECS 再登録 | 20s |
| → **ユーザーの EBS をマウント**（SSM 経由） | **24s**（マウント自体は 4s） |
| **ホットスワップ（インスタンスは起動したまま）** | |
| 　desired 0 → タスク消滅 | 10s |
| 　`umount` | +4s |
| 　`DetachVolume` → `available` | +10s（ここまで降ろすのに **24s**） |
| 　`AttachVolume` → `attached` | 4s |
| 　`mkfs`（初回のみ）＋ `mount` | +4s |
| 　**次のユーザーのタスク RUNNING** | **22〜27s**（pull **0.045s**） |
| 1 台に 2 ユーザーを同時（別デバイス・別 bind mount） | 技術的には成立（両タスク RUNNING・残 CPU/メモリも期待どおり）。**ただし採らない** — §64.12.2 |
| 配置の狙い撃ち | **`ec2InstanceId == i-xxx` の配置制約で足りる**（属性の書き換え不要） |
| `npm ci`（キャッシュ温・2,377 ファイル/88 MiB） | **3.6〜3.8s**（EFS は 5,643 ファイルで 28.2s） |

**したがって、ユーザーから見た Start はこうなる:**

| 形 | Start（Agent 起動直前まで） | アイドル時の費用 |
|---|---|---|
| 現状 Fargate（温ホーム） | ~105s | EFS のみ |
| 1 ユーザー = 1 インスタンス固定（§64.4） | 83.5s | そのユーザーの EBS のみ |
| **プール（停止スロット）＋ 差し替え** | **~50s**（start 19s ＋ attach/mount 8s ＋ タスク 22s） | EBS ＋ 停止スロットの root |
| **プール（ホットスロット）＋ 差し替え** | **22〜27s** | EBS ＋ **ホット分の EC2 課金** |

**プール型は「速さ」と「永続」を同時に取れる唯一の形**である。しかも root ボリューム
（＝イメージキャッシュ）はスロットの数だけで済むので、ユーザー数分の root を持つ §64.4 の形より
ストレージも安い（30 GiB × スロット数 × $0.096）。

### 64.12.2 スロットは 1 ユーザー排他にする（同居は「できるが採らない」）

「1 台に 2 ユーザーのタスクを同時に置く」は**技術的には成立した**が、**採らない**。
価格を検算すると、ビンパッキングの旨みが存在しないためである（Pricing API 実測・§64.8 と同じ取得）:

| インスタンス | 時間単価 | vCPU 単価 |
|---|---|---|
| m7i.large（2 vCPU） | $0.1302 | **$0.0651** |
| m7i.xlarge（4 vCPU） | $0.2604 | **$0.0651** |
| m7i.2xlarge（8 vCPU） | $0.5208 | **$0.0651** |

**EC2 のオンデマンド価格は vCPU に対して完全に線形**なので、8 人を 1 台の 2xlarge に詰めても
8 台の large に散らしても **compute は 1 円も変わらない**。同居で浮くのは実質 2 つだけ:

- スロット毎の root ボリューム（30 GiB ＝ **$2.88/月**）を人数分持たずに済む
- ホスト固定オーバーヘッドの按分（実測: 8 GiB の箱で ECS が登録したのは **7,783 MiB** ＝
  **409 MiB** が OS とエージェントの取り分）

対価は**カーネルと root ファイルシステムの共有**（§64.12.3）。**月 $3〜4 のために分離水準を落とす
取引**であり、割に合わない。→ **1 スロット 1 ユーザー（排他）**。このときホットスロット数 ≒
同時稼働ユーザー数なので、**Fargate で同時に走らせているのと同じ量の compute しか払わない**
（余るのは待機スロットの分だけ）。

排他にしても**プール型の利点（Start 22〜27s ＋ 永続 home）はそのまま**残る。同居の検証から
残った価値は、設計の制約が確定したことのほうにある:

- `MaximumEbsAttachments` は m7i 系で **32**・`AttachmentLimitType = dedicated`（ENI とは別枠）
  ＝ **プールのサイズはアタッチ上限に縛られない**（awsvpc を使う場合だけ ENI 側が先に効く。
  m7i.large は 3・4xlarge で 8）
- タスクの `cpu` はインスタンスの CPU units を予約する（§64.4.5）＝ **排他なら `cpu` を省いて
  1 台をまるごと使わせるのが正解**

### 64.12.3 プール型が持ち込む代償（排他を前提にしても残るもの）

1. **root ボリュームは前のユーザーと共有される。** イメージ層の共有は狙いどおりだが、
   **前のユーザーのコンテナ書き込み層と `/tmp` が同じ root に残る**。ECS の掃除は
   `ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION` が握っており、**エージェントの README の既定値表で 3h**
   （さらに `ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION_JITTER` が別にある。本計測では 1m に落とした）。
   詰めないと「前の人のコンテナが残っているスロット」を次の人に渡すことになる。
2. **`/tmp` の扱いを決める。** 実行中のコンテナの `/tmp` は mount namespace で分離されるので
   他ユーザーから直接は見えないが、**実体は共有 root ボリューム上**（`/var/lib/docker/overlay2/<id>/diff/tmp`）
   にあり、コンテナが削除されるまで残る。危ないのは 2 つ:
   - **残留** — ホストに手が届いたとき（コンテナ脱出・host bind mount の設定ミス・運用者の SSM）に
     読めてしまう。**Fargate（タスク毎 microVM）には無かった経路。**
   - **容量の共有** — 一人が `/tmp` を埋めると root ボリュームが枯れ、同じスロットのタスク・
     イメージ pull・ECS エージェントがまとめて倒れる。攻撃でなく事故でも起きる。

   **EC2 起動タイプなら潰せる**（Fargate では使えない道具）: タスク定義の
   **`linuxParameters.tmpfs`**（`containerPath` ＋ **`size`（MiB・必須）** ＋ `noexec,nosuid,nodev`）で
   `/tmp` を tmpfs にすると、**ディスクに書かれず・タスク終了で消え・サイズ上限も付く**＝
   残留と容量の両方が同時に消える。併せて書き込み層のクォータ（overlay2 ＋ xfs prjquota の
   `storage-opt size`）とスロット返却時のコンテナ削除を入れる。
3. **CP がインスタンス上で `mount` / `umount` を実行する経路が要る。** 本計測は SSM SendCommand
   （`af-mount <volume-id> <path>` を user-data で置いた）。udev/systemd で LABEL 自動マウントに
   寄せる手もあるが、いずれにせよ「タスク定義の `host.sourcePath` が存在する状態」を CP が作る責任を負う。
4. **detach の前に必ず umount。** 強制 detach はファイルシステムを壊す。タスクが SIGKILL で残っている
   ときにどうするか（umount 失敗 → リトライ → 最後は fuser -k）の設計が要る。
5. **AZ ごとにプールが要る**（EBS は AZ 固定）。スロットが空いている AZ にユーザーのボリュームが
   無ければ、snapshot 経由で作り直すしかない（§64.7 の 30〜40 分）。

### 64.12.4 「速さ」と「残留」のトレードオフ

| 形 | Start | 残留・分離 |
|---|---|---|
| ホットスロットを**同居**で使う | 22〜27s | root とカーネルを**同時に**共有。**採らない**（§64.12.2） |
| **ホットスロットを排他**（返却時に掃除・`/tmp` は tmpfs） | **22〜27s** | カーネルの同時共有は無し。残留は掃除の設定次第 |
| 停止スロット ＋ 排他 | ~50s | 同上 |
| 使い捨てスロット（返却のたび terminate して作り直す） | ~120s | 残留ゼロ。ただし**イメージキャッシュも捨てる**ので速さの利点が消える |
| 現状 Fargate | ~105s | タスク毎 microVM で残留ゼロ |

**採るなら 2 行目**（ホット ＋ 排他 ＋ tmpfs ＋ 短い cleanup）。

## 64.13 初回ユーザー用の golden snapshot（第 2 ラウンド）

新規ユーザーの home を空から作ると、`entrypoint.sh` の boot-install（**4CLI で 41s ＋ rtk 1s ＋
agy 6s ＝ 48s**・[62](62-ecs-start-latency.md) §62.9.3）とキャッシュが空の初回 `npm install` を毎回払う。
**「boot-install 済み・キャッシュ温」の home を snapshot で焼いておき、新規ユーザーの EBS を
そこから作る**とどうなるかを測った。

| 項目 | 実測 |
|---|---|
| golden の中身 | 23,012 ファイル / 1,092 MiB（`~/.local` 相当の小ファイル 20,000 ＋ npm キャッシュ ＋ node_modules） |
| `CreateSnapshot` → completed | 102s（`FullSnapshotSizeInBytes` 1.23 GB） |
| **golden → `CreateVolume` ＋ attach ＋ mount** | **17〜20s** |
| 復元直後の**メタデータ**走査（`find` 23,012 ファイル） | **0.118s** |
| 復元直後の**フルリード**（819 MB / 20,000 ファイル） | **25.0s** |
| 同じものを 2 回目（ページキャッシュ有） | 0.34s |
| **元のボリュームを drop_caches してフルリード** | **26.1s** |
| 復元ボリュームを drop_caches してフルリード | 26.1s |
| golden home でのタスク起動 → 準備完了 | **17s で RUNNING・その 4s 後に ready**（`npm ci` 3.8s・**ネットワーク不要**） |
| 空 home でのタスク | `npm install` に 15.3s（ネットワーク）＋ 準備完了まで 20s |

**分かったこと:**

1. **小ファイルでは遅延ハイドレートの税がゼロだった。** 復元直後の 25.0s は、**同じ内容を持つ
   元ボリュームをキャッシュ落としして読んだ 26.1s と同じ**。§64.7 で見た 2.3 倍の税は
   **4 GiB の逐次リード**で出たもので、そこでは S3 からの取得が帯域律速になる。小ファイルの
   読み出しは IOPS/レイテンシ律速なので、ハイドレートが**その裏に隠れる**。
   **home ディレクトリという用途では、ハイドレートは問題にならない。**
2. **メタデータは即座に効く**（`find` 23,012 ファイルが 0.118s）。「起動直後にツリーを走査する」型の
   処理（CLI の起動、`git status`）は復元直後から普通に速い。
3. **golden snapshot は初回ユーザーの体験を素直に改善する。** boot-install の 48s とネットワーク依存が
   消え、`npm ci` は復元されたキャッシュから 3.8s（元ボリュームと同値）で通る。
4. **費用は 1 本分だけ。** snapshot は何個でもボリュームを生やせるので、**golden は全ユーザーで 1 本**
   （$0.05/GB-月 × golden のサイズ）。ユーザー毎に持つ必要はない。
5. **更新はリリースに紐づく。** イメージや CLI のピンが上がったら golden を焼き直す必要がある
   （焼き直しは「1 台起こして entrypoint を通し、snapshot を取る」だけなので、
   `release-ecr.sh` の後段に置ける）。**焼き直しを忘れると新規ユーザーだけ古い CLI で始まる**ので、
   golden にはイメージのタグを刻んで CP が突合すること。

## 64.14 計測ハーネスと後始末

`~/af-ec2/`（本セッションの Workspace）に置いた: `setup.sh`（ECR 複製・SG・IAM・クラスタ・
Cloud Map 名前空間）／ `userdata.sh`（追加 EBS のマウントと ECS 参加）／ `launch.sh`（インスタンス起動）／
`mktd.py`・`bench-in-task.sh`（タスク定義生成と I/O 計測）／ `svc.sh`・`warmtask.sh`・`cycle.sh`（起動計測）／
`snap.sh`・`snap2.sh`（退避と復元）／ `resize.sh`（タイプ変更）／ `pricing.sh`（Pricing API）／ `teardown.sh`。

第 3 ラウンド（§64.15.2 のデバイス名排他）はハーネスを作らず、`/tmp/af-devprobe.sh` に
書いた 1 本のスクリプトで deploy → 検証 → teardown を閉じた（t3.micro 1 台 ＋ 1 GiB 3 本・
残存 0 を確認）。**確保の原子性のように「これが崩れると設計ごと崩れる」1 点は、実装より先に
最小構成で潰す**——ECS もクラスタも要らなかった。

第 2 ラウンド（§64.12 / §64.13）は `~/af-ec2b/`: `setup.sh` ／ `userdata.sh`（`af-mount` / `af-umount` を置く
汎用スロット）／ `phase1.sh`（停止中 attach）／ `phase2.sh`（ホットスワップ・2 ユーザー同居）／
`phase3.sh`・`phase3b.sh`（golden snapshot とハイドレート）／ `teardown.sh`。

**後始末は両ラウンドとも完了している**: cluster / instance / volume / snapshot / EIP / ECR /
Cloud Map 名前空間 / ロググループ / SG / IAM ロール / インスタンスプロファイル / タスク定義 / ENI が
**すべて 0 件**であることを確認した（`ec2-single` 等の他のスタックは元から存在しない sandbox）。

計測の教訓を 2 つ:

- **背景実行にしてログをファイルに落とすこと。** 5 分を超える計測をツールのタイムアウトに
  当てると、途中経過が丸ごと失われる（1 回やり直した）。
- **失敗した AWS API 呼び出しを「遅い」と読まないこと**（[62](62-ecs-start-latency.md) §62.10.2 と同じ轍）。
  今回も「エージェントが 11 分繋がらない」の正体は API の遅さではなく、**パブリック IP の消失**だった。
- **`tar cf /dev/null` は中身を読まない。** GNU tar が出力先 `/dev/null` を検出して入力ファイルの
  読み出しを省く最適化があり、819 MB のツリーが 0.349s で「読めた」ことになっていた。
  I/O を測るなら `tar cf - dir | wc -c` のように**実際に流すこと**。

## 64.15 実装設計（P0・プール型・2026-08-15 に採用）

[ADR 0045](decisions/0045-ec2-persistent-workspace.md) 決定 10 で採用に転じた。ここは**実装前に紙で
確定させた設計**である（§64.9 は「1 ユーザー = 1 インスタンス固定」の設計案で、採らない）。

- **形**: §64.12 のプール型（汎用スロット ＋ ユーザー毎 EBS の差し替え・ホット ＋ 排他 ＋ tmpfs ＋ 短い cleanup）
- **置き場**: `control-plane/runtime_ecs_ec2.go` を**新設**し、`AF_RUNTIME=ecs-ec2` で並走。
  **`runtime_ecs.go`（Fargate）は 1 行も変えない**（決定 10-1。退路が profile 1 行）
- **P0 の範囲**: ライフサイクル（確保 → attach → mount → 配置 → umount → detach → 返却）と
  `Start` / `Stop` / `State` / 削除。golden snapshot（決定 9）と退避・復帰（決定 4）は P1

### 64.15.1 資源と引き方（DB スキーマは増やさない）

[ADR 0012](decisions/0012-go-refactor.md)「アダプタは CP に状態を持たない」は**維持できる**。
占有は導出でき、確保の原子性は AWS の API に委ねられるためである。

| 資源 | 引き方 | タグ |
|---|---|---|
| ユーザーの home ボリューム | `DescribeVolumes` | `af-membership=<id>` ＋ `af-role=home` |
| スロット | `DescribeInstances`（state: running/stopped） | `af-pool=<cluster>` ＋ `af-role=slot` ＋ `af-slot-size=<type>` |
| **スロットの占有** | **導出**（保持しない）: home ボリュームの `Attachments[].InstanceId` | — |
| **確保中**（attach 前の一瞬） | home ボリュームの `af-claim=<instance-id>` ＋ `af-claim-at=<RFC3339>` | 期限切れは回収が剥がす |
| コンテナインスタンス登録 | `ListContainerInstances` → `DescribeContainerInstances` を `ec2InstanceId` で突合 | — |
| サービス / タスク定義 | 現行どおり `ContainerName` | — |
| 資格情報（`homeKeep` の 7 つ） | 現行の EFS アクセスポイント（決定 3-6 のハイブリッド） | `af-membership` ＋ `af-role` |
| golden snapshot（P1） | `DescribeSnapshots` | `af-role=golden` ＋ `af-image-tag=<tag>` |

### 64.15.2 スロット確保の原子性は「固定デバイス名の `AttachVolume`」に委ねる

2 つの Workspace が同時に同じ空きスロットを掴む競合を、CP のロックではなく **AWS の API に解かせる。**

- **ユーザーの home はどのスロットでも常に同じデバイス名**（`/dev/sdf`）に attach する。
  既に何かが `/dev/sdf` に付いているインスタンスへの `AttachVolume` は AWS 側で失敗するので、
  **確保 ＝ `AttachVolume` が成功すること**になる。失敗したら次の候補スロットへ回る。
- これは決定 8 の「1 スロット 1 ユーザー排他」と実装が一致する——**「空きスロット」とは
  「`/dev/sdf` が空いているスロット」**である。別の場所に空き table を持つ必要がない。
- CP 既存の `AcquireWorkspaceOperationFence` は**同じ Workspace の二重 Start**しか防がない。
  **異なる Workspace 同士の競合はここで解ける。**

✅ **実測で確認した（2026-08-15・第 3 ラウンド。t3.micro 1 台 ＋ 1 GiB ボリューム 3 本の最小構成・
teardown 済み残存 0）。** 確保の原子性はここに全体重を預けているので、実装より先に潰した。

| 試したこと | 結果 |
|---|---|
| 1 本目を `/dev/sdf` へ | 通る |
| **2 本目を同じ `/dev/sdf` へ** | **`InvalidParameterValue: Attachment point /dev/sdf is already in use`** |
| 2 本目を `/dev/sdg` へ | 通る（＝ 失敗はデバイス固有であって、インスタンスが埋まっているのではない） |
| **インスタンス停止中に同じ `/dev/sdf` へ 2 本目** | **同じエラーで弾かれる**（停止スロット経由でも排他が効く） |
| 停止中インスタンスへの `AttachVolume`（別デバイス） | 通る・**2.1s**（§64.12.1 の 3s と整合） |
| `DescribeInstances` の `BlockDeviceMappings` | **要求したデバイス名がそのまま出る**（`/dev/xvda` ＝ root と `/dev/sdf` `/dev/sdg`）＝ 占有の導出はこれで足りる |

→ **「空きスロット＝ `/dev/sdf` が空いているスロット」は AWS が保証する。** CP 側に空き table は要らない。

### 64.15.3 Start —— 同期部は ALB の 60s に収める

順序は **AZ の鶏卵**から決まる: **ボリュームが AZ を決め、AZ がスロット候補を決める**（決定 3-4）。

1. `State()` が `running` / `starting` なら return（現行 Fargate と同じ契約）
2. home ボリュームを引く。**無ければ先にスロットを確保し、そのスロットの AZ に `CreateVolume`**
   （P0 は空ボリューム ＋ 既存 `entrypoint.sh` の boot-install 48s。P1 で golden から 17〜20s）
3. **スロット確保**（候補順）
   1. 同 AZ の**ホットスロット**（running・`/dev/sdf` 空き）→ `AttachVolume`（実測 **3s**）
   2. 同 AZ の**停止スロット** → **停止したまま `AttachVolume`**（実測 3s・§64.12.1）→ `StartInstances`
   3. どちらも無ければ `af-claim` を打って `RunInstances`
4. **mount**: SSM SendCommand `af-mount <volume-id> /af-home/<membership>`（実測 4s）
5. `RegisterTaskDefinition` — 配置制約 `memberOf: ec2InstanceId == i-xxx`、**`cpu` は省略**（決定 8）、
   `/tmp` は `linuxParameters.tmpfs`、home は `host` ボリューム、資格情報は EFS アクセスポイント
6. `UpdateService desired 1` → return

**(3-1) だけが同期経路**（attach 3s ＋ mount 4s ＋ API ＝ 10 秒台）で、ALB の 60s idle timeout に対して
安全である。**(3-2)（+19s の起動待ち）と (3-3)（+8s の running 待ち）はバックグラウンドへ逃がす**——
`runtime.go` の `Start` は「COMMITTED で返す」契約であり、収束は `State()` が `starting` で写す。
これは docs/62 §62.5 で Fargate が 504 を返した轍と同じ話である。

配置制約は**サービスではなくタスク定義側**に置く（`RegisterTaskDefinition.PlacementConstraints`）。
毎 Start で登録し直しているので、**スロットが変わるたびに制約も書き換わる**のが自然で、
サービス側の `PlacementConstraints`（`UpdateService` でも更新できる）を触らずに済む。

### 64.15.4 `State()` の写像（決定 3-5）

サービスの desired/running だけでは足りない。**ボリューム・インスタンス・サービスの 3 つ組**で写す。

| home ボリューム | スロット | サービス / タスク | `State()` |
|---|---|---|---|
| 無い | — | 無い | `none` |
| `available`・claim 無し | — | desired 0 / 無し | `stopped` |
| `available`・**claim が期限内** | pending | — | **`starting`** |
| `in-use` | pending / running | desired 1・RUNNING 無し | **`starting`** |
| `in-use` | running | desired 1・RUNNING 1 | `running` |
| `in-use` | running | **desired 0** | `stopped`（後片付け待ち。再 Start は刺さったまま再利用でき、むしろ速い） |

**`af-claim` に期限が要る**——さもないと「スロット起動に失敗したユーザーが永久に `starting`」になり、
Console からは復帰できなくなる（`Start` は `starting` を見て早期 return するため）。

### 64.15.5 Stop —— 同期は `desired 0` まで、あとは非同期

1. `UpdateService desired 0`（**同期・ここで返す**。現行 Fargate の `Stop` と同じ）
2. 非同期: タスク消滅を待つ（実測 7〜13s）→ SSM `af-umount` → `DetachVolume` → **スロット返却**
3. **`umount` は `DetachVolume` の前に必ず**（決定 8 の代償 3。強制 detach はファイルシステムを壊す）。
   タスクが SIGKILL で残って umount が失敗する場合はリトライ → 最後に `fuser -k`
4. 返却時に**前ユーザーの痕跡を消す**（停止済みコンテナの削除）。`/tmp` は tmpfs なので残らない
5. P0 は**返却したスロットをホットのまま残す**（次のユーザーが 22〜27s で乗れる）。
   アイドルスロットの `StopInstances` / terminate による縮退は P1
6. **「Stop の直後に Start」を潰す。** recreate / clean-home のハンドラは `Stop` の直後に
   `Start` を呼ぶので、**後片付けが流れている最中に起動が始まる**。返却が走り切ると
   タスクは home が無いまま上がり、entrypoint が**スロットの共有 root へ書く**ことになる。
   サービスの desired 見張りだけでは、Start が `UpdateService` に到達する前の窓が残るので、
   **Stop の時点で Start 回数を控えておき、umount と detach の直前に見比べて中止する**
   （増えていたら detach せず mount し直す）。プロセス内の目印であって状態ではない
   ——失っても、サービスを見直す掃除ループが気づく側に倒れる。

### 64.15.6 漂流の回収（CP が落ちても直る）

非同期の後片付けは CP の再起動で失われる。**CP に状態を持たないので、回収はタグからの再導出で行う。**

| 症状 | 回収 |
|---|---|
| desired 0 なのに home ボリュームが `in-use` のまま N 分 | umount → detach → 返却 |
| `af-claim` が期限切れ | タグを剥がす（`stopped` に戻す） |
| スロットに刺さっているが対応する Workspace が消えている | detach → ボリュームは削除ロック（ADR 0028）に従う |
| `agentConnected=false` が長く続くコンテナインスタンス | `DeregisterContainerInstance`（決定 3-2） |
| どの Workspace にも紐づかない `af-role=slot` インスタンス | 返却済みとして扱う（terminate は P1） |

### 64.15.7 削除とサイズ変更

- **削除**: タスク停止 → umount → detach → `DeleteVolume`（P1: 先に snapshot）→ EFS AP 削除。
  **スロットは消さない**（スロットはユーザーのものではない）。ADR 0028 の削除ロックの対象が増える。
- **サイズ変更は §64.5 (1) の 3 手を踏まない。** プール型ではユーザーとインスタンスが紐づいていないので、
  **「別サイズのスロットへ載せ替える」＝ Stop → 別プールのスロットへ attach → Start** で済み、
  `ModifyInstanceAttribute` を呼ぶ場面自体が無い。**プール型の思わぬ利点である**
  （代わりに**プールを AZ × サイズで持つ**必要がある）。
  3 手の手順は**スロットのタイプを運用で変えるとき**のために残すが、その場合も
  「古いスロットを terminate して新しく起こす」で足りる——スロットはユーザーデータを持たない。
- **ディスク拡張**: `ModifyVolume`（オンライン）＋ インスタンス側で `growpart` / `resize2fs`。

### 64.15.8 substrate 側に要るもの

- **スロットの user-data**: `ECS_CLUSTER` ／ `ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION` を詰める（既定 3h・決定 8）
  ／ `af-mount` `af-umount` の設置（`~/af-ec2b/userdata.sh` が原型）／ SSM エージェント
- **CP のタスクロール（追加）**:
  `ec2:DescribeInstances` `DescribeVolumes` `CreateVolume` `DeleteVolume` `AttachVolume` `DetachVolume`
  `CreateTags` `DeleteTags` `RunInstances` `StartInstances` `StopInstances` `TerminateInstances` ／
  `ecs:ListContainerInstances` `DescribeContainerInstances` `DeregisterContainerInstance` ／
  `ssm:SendCommand` `GetCommandInvocation` ／ `iam:PassRole`（スロットのインスタンスプロファイル）
- **スロットのインスタンスプロファイル**: ECS 参加 ＋ SSM ＋ ECR pull ＋ CloudWatch Logs
- **プライベートサブネット ＋ NAT**（決定 3-3。パブリック IP に依存しない）

### 64.15.9 実装したもの（P0）と、設計から動いた 4 点

**入ったもの**: `control-plane/runtime_ecs_ec2.go`（アダプタ本体 ＋ 漂流回収）／
`runtime.go` の `ecs-ec2` profile ／ `runtime_ecs_ec2_test.go`（13 本・フェイクの EC2 が
「同じデバイス名への 2 本目の attach を拒否する」ところまで模す）／
`deploy/aws/ecs/cfn/40-ec2-pool.yaml`（スロットの起動テンプレート・IAM・SG）／
20-platform の CP ロールに EC2/SSM/コンテナインスタンス権限 ／ 30-ingress の `WsRuntime`
と `Ec2*` パラメータ ／ `workspace/entrypoint.sh` の `AF_WS_KEEP`（資格情報の EFS 退避）。

**設計から動いた 4 点**（いずれも実装して初めて分かったこと）:

1. **`/tmp` の tmpfs から `noexec` を既定で外した。** 決定 8 は `noexec,nosuid,nodev` と
   書いたが、ここは**開発コンテナ**で、インストーラやテストランナーが `/tmp` から exec
   するのは日常である。決定が実際に欲しかった 2 つ（共有 root に書かれない・上限が付く）は
   `noexec` 無しで得られる。`AF_ECS_EC2_TMP_OPTS` で戻せる。
2. **サイズ変更で §64.5 (1) の 3 手を踏む場面が無くなった。** プール型はユーザーと
   インスタンスが紐づかないので、サイズ変更は「別サイズのスロットへ載せ替える」だけになる
   （§64.15.7）。**罠 1 はプール型を採った時点で消える**——ただしスロットのタイプを運用で
   変える場合に備え、手順は §64.5 に残す。
3. **削除（`Destroy`）は実装したが、呼び出し元が無い。** CP には Workspace 削除の継ぎ目が
   そもそも無く、Fargate でもサービスとアクセスポイントはメンバーシップより長生きしている。
   ここで放置される物が**課金され続ける EBS ボリューム**である以上、手順は書いておくべきなので
   `runtimeDestroyer` として置き、配線は削除ロック（ADR 0028）側の宿題とした。
4. **P0 ではプールが縮まない。** 返却したスロットはホットのまま残す（次のユーザーが 22〜27s で
   乗れる）。したがって**費用を抑えるのは `AF_ECS_EC2_MAX_SLOTS` だけ**であり、これは
   「同時に働く人数」として設定する必要がある。アイドルスロットの stop/terminate は P1。

**確かめたこと**: スロット確保の土台（同一デバイス名の排他・停止中でも効くこと・
`BlockDeviceMappings` で占有を導出できること）は §64.15.2 の表のとおり**実測で潰した**。

**まだ実測で確かめていないこと（実装の前提として置いた仮定）**:

- EC2 起動タイプ ＋ awsvpc ＋ Service Connect ＋ **タスク定義側**の `ec2InstanceId` 配置制約の
  組み合わせ（第 1・2 ラウンドはサービス側の属性制約と bridge で測っている）。
- `host` ボリュームで EBS を bind した状態での entrypoint 一式（`AF_WS_KEEP` の symlink 化を含む）。
- CFN の `40-ec2-pool` をスタックとして立てたことは無い（user-data の中身は第 2 ラウンドで実証済み）。

## 64.16 実装したアダプタを実 AWS で通す（第 3 ラウンド c・2026-08-15）

§64.15 の実装（`runtime_ecs_ec2.go`）を**本物の ECS / EC2 / EBS / EFS / SSM に対して**動かした。
ハーネスは `~/af-ec2c/`（`setup.sh` → `go test -run TestECSEC2Live` → `teardown.sh`）で、
**基盤は repo の `deploy/aws/ecs/cfn/40-ec2-pool.yaml` をそのまま立てている**
（テンプレート自体を検証したいので、手書きの launch template は使わない）。
イメージも本番と同じ `workspace:0.8.0`。NAT / ALB / RDS は作らない。

### 64.16.1 通ったこと

| 確かめたこと | 結果 |
|---|---|
| `40-ec2-pool` スタックが立つ（LT・IAM・SG・user-data） | ✅ CREATE_COMPLETE |
| user-data が置く `af-mount` / `af-umount` | ✅ 実機で動作（`--mkfs` の blkid ガードも含む） |
| **タスク定義側の `ec2InstanceId ==` 配置制約** | ✅ 狙ったスロットに載る（タスクのコンテナインスタンスと attach 先が一致） |
| **awsvpc ＋ Service Connect（EC2 起動タイプ）** | ✅ `ServiceConnect: ATTACHED`。`Endpoint()` の契約は変えなくてよい |
| **EBS home がコンテナの `/home/dev`** | ✅ entrypoint が書いた `.local` / `.npm` がホストの `/af-home/<id>/dev` に見える |
| **home がスワップをまたいで残る** | ✅ マーカーが u1 → u2 → u1 の往復後も無傷 |
| 1 スロットを 2 ユーザーが順番に使う（プールは増えない） | ✅ |
| EFS アクセスポイント（資格情報ハイブリッド）が EC2 でもマウントできる | ✅ **ただし条件付き** — §64.16.2 (1) |
| `Destroy`（返却 → ボリューム削除） | ✅ ただしリトライが要る — §64.16.2 (4) |

### 64.16.2 実装が間違っていた 4 点（実 AWS でしか出なかった）

1. **EFS アクセスポイントに `PosixUser` を付けると、EC2 起動タイプではタスクが起動しない。**

   ```
   CannotCreateContainerError: failed to copy file info for /var/lib/ecs/volumes/…-claude-…:
   failed to chown …: operation not permitted
   ```

   EC2 では ECS が **EFS を Docker に渡す**ので、Docker が「イメージ側のディレクトリの所有情報を
   空ボリュームへ複製する」段で `lchown` する。ワークスペースイメージの `/var/lib/af/claude` は
   **root 所有**なので `lchown(0,0)` になり、`PosixUser 1000` のアクセスポイントはそれを
   **uid 1000 として**実行する ＝ 権限が無い。**Fargate は EFS を自前でマウントする**ので、
   この経路が存在せず、既存アダプタでは一度も出ていなかった。
   → EC2 側は**専用のアクセスポイント（`claude-ec2` / `keep-ec2`・`PosixUser` 無し）**を作る。
   `rootDirectory` は Fargate と同じパスにしてあるので、プロファイルを切り替えても中身は続く。

2. **「ボリュームは刺さっているがサービスが無い」を `starting` と写すと、Start が永久に no-op になる。**
   `Start` は `starting` で早期 return するので、**起動が attach と CreateService の間で死んだ
   Workspace は誰にも起こせなくなる**。§64.15.4 の表はこの形で書かれていた。
   → **収束中を名乗るのは claim タグだけ**にし（launch 完了で落とす・TTL 5 分）、
   claim の無い attach 済みは `stopped`（＝ Start が拾い直せる）に変えた。

3. **`DetachVolume` が返り、ボリュームが `available` になっても、スロットのデバイスは 8〜9 秒解放されない。**
   その間の `AttachVolume` は `Attachment point /dev/sdf is already in use` で弾かれる
   （＝ §64.15.2 の排他は効いている）。素直に「空きスロット無し」と読むと
   **数秒待てば済むところでインスタンスを 1 台買う**——プールの意味が消える。
   → 返却は**デバイスが返るまで待って**から完了とし、attach 側にも短いリトライを入れた。
   なお「インスタンスの `BlockDeviceMappings`」と「ボリュームの attachment」は**別々に収束する**
   （ボリューム側が先に空く）ので、占有の判定はボリューム側 ＋ リトライに寄せた。

4. **`DeleteVolume` は、`DescribeVolumes` が detach 済みと答えた後でも `VolumeInUse` を返す窓がある。**
   → リトライする。ここで諦めると**課金され続けるボリュームが残る**。

（おまけ）**ECS は削除直後の同名サービス作成を拒否する**（`Create service is not idempotent`）。
CP は今のところサービスを削除しないので実害は無いが、削除を配線するときはこの窓を踏む。

### 64.16.3 計測 —— 「22〜27s」は Service Connect 込みでは出ない

**測ったのは「アダプタの経路」であって「ユーザーの場面」ではない**ので、対応を明示する。
どれが多いかはプールの空き具合で決まる。

| 経路（実測） | 実測 | **これが起きる場面** | 頻度 |
|---|---|---|---|
| 全部温: 既存 home ＋ 空いているホットスロット ＋ 既存サービス | **13.2s** | **同じ人が、自分が使っていたスロットが空いたまま戻る** —— 昼休み後の再開、reaper が止めた直後にまた触る、スケジューラの wake | プールに余裕があるデプロイでは**これが日常** |
| 既存 home を**他人が使っていた**ホットスロットへ載せ替え | **51.8s / 94.3s** | **朝、複数人が順に起きる** ／ 誰かが止めた直後に別の人が起動 | 同時稼働数 ≒ スロット数のとき常態 |
| 新規 home（`CreateVolume` ＋ `mkfs` ＋ 初回 boot-install） | **81.7s** | **新規メンバーの初回起動**（＋ home を作り直したとき） | 1 人につき 1 回 |
| **プールを 1 台増やす**（`RunInstances` → boot → ECS 登録 → attach → mount → タスク） | **135.4s** | **空きスロットが無い**とき —— デプロイ直後の 1 人目、全員停止した翌朝の 1 人目、同時稼働がプール上限に迫ったとき | 朝に 1 回、あるいはプールが埋まるたび |
| Stop → スロット返却（タスク消滅 ＋ umount ＋ detach ＋ デバイス解放） | **7.3〜13.3s** | reaper のアイドル停止、明示的な Stop | 毎回。**ユーザーは待たない**（Stop は desired 0 で即返る）が、**次の人がそのスロットに乗れるまでの遅延**として効く |

補足 2 つ:

- **1 行目と 2 行目は同じ「ホットスロットへ載せる」なのに 13s と 52〜94s に割れる。** 差の分解までは
  していないが、2 行目は**サービスが別**（Service Connect の登録とタスク ENI が別物）で、
  返却直後ならデバイス解放の 8〜9 秒も乗る。**同一条件でも 13〜52s のばらつきを観測している**ので、
  1 行目を「この形の性能」と読まないこと（サンプルは各 1〜2 回）。
- **「停止スロットからの起動（~50s・§64.12.1）」は P0 では発生しない。** 返却したスロットは
  ホットのまま残す設計なので、スロットは「ホット」か「存在しない」かのどちらかである。
  縮退（アイドルスロットの stop）を P1 で入れると、この 4 行目と 1 行目の間にその形が入る。

⚠️ **§64.12.1 の「22〜27s」は bridge ＋ Service Connect 無しで測った数字である。**
製品は CP → Agent の到達に Service Connect を使う（`Endpoint()` の契約）ので、
§64.4.3 の実測どおり **awsvpc ＋ SC がおよそ 20 秒を足す**。実装を通した実測は
**13〜95 秒の帯**で、Fargate の温再 Start（~105s）より速いが**「1/4」ではない**。

**この帯を 30 秒未満に寄せたいなら、削りしろは Service Connect を外すこと**（§64.4.3 の
bridge ＋ 固定ホストポートで 22s）だが、それは `Endpoint()` の契約変更（CP がインスタンスの
private IP ＋ ポートを自前で解決する）を意味する。P0 では採らない。

### 64.16.4 後始末

`teardown.sh` で全消去し、**インスタンス / ボリューム / スナップショット / EFS / クラスタ /
名前空間 / ECR / スタック / IAM ロール / ENI / SG / ロググループが 0 件**であることを確認した。

## 64.17 スロット親和性（遅延返却）とアイドル停止（P1・2026-08-16）

§64.16 の実測で「Start は何が温まっているかで 13〜95 秒に散る」ことが分かった。**一番速い
13.2 秒は「home が既にスロットに載っていた」ときの数字**である。ならば**載せたままにすればいい**
——というのがここで入れた 2 つの機能で、費用の穴も同時に塞ぐ。

### 64.17.1 遅延返却 —— Stop で剥がさない

**Stop はサービスを desired 0 にするだけで、home はスロットに attach したまま残す。**

- **親和性は「attach そのもの」である。** 「前回どのスロットだったか」を別に覚える必要が無い
  ——`placeHome` は最初に「自分の home が刺さっているか」を見るので、**同じ人は自然に同じ
  スロットへ戻る**。CP に状態を持たない原則（ADR 0012）を保ったまま親和性が手に入る。
- **戻りは最速の経路**（attach も mount も要らない・実測 13.2s）。
- 剥がすのは 3 つの場合だけ: **立ち退き**（§64.17.3）・`Destroy`・漂流の回収。

「先に返却して次回は同じスロットを優先する」形も検討したが**採らない**——どのみち attach と
mount をやり直すので 13 秒にはならず、得られるのは予測可能性だけだった。

### 64.17.2 アイドル停止 —— 眠るときはスロットごと眠る

遅延返却だけだと、停止中のユーザーが**起動したままのスロット**を抱え込む。そこで掃除ループが
**`AF_ECS_EC2_IDLE_STOP_SEC`（既定 15 分）で `StopInstances`** する。**terminate はしない。**

| スロットの状態 | 次に本人が戻るとき | 月額（m7i.large・root 100 GiB） |
|---|---|---|
| 起動したまま | **13.2s**（実測） | $95 ＋ $9.6 |
| **停止（イメージキャッシュは root に残る）** | **~90s**（推定。start 19s ＋ ECS 再登録 20s ＋ mount ＋ タスク） | **$9.6 のみ** |
| 存在しない | 135s（実測・pull 35s 込み） | $0 |

> ⚠️ **これは P0 の設計漏れの修正でもある。** P0 は「返却したスロットはホットのまま残す」
> だったので、**ピーク同時稼働の台数を 24/7 払い続ける**形になっていた。縮退は
> 「あると速い機能」ではなく、**入れないと請求が止まらない**類のものだった。

**停止スロットからの detach は umount しない。** SSM が届かないうえ、マウントは既に
外れている（インスタンス停止は通常のシャットダウンで、systemd がアンマウントする）。
届かない umount を待つと、眠っているスロットが永久に回収できなくなる。

**ホットな空きの事前確保（プレウォーム）は入れない**（利用者判断）。朝の 1 人目は
停止スロットを起こす ~90s か、プールが空なら 135s を払う。

### 64.17.3 立ち退きは「上限に達したときだけ」

- 空きスロットがあればそれを使う。**上限（`AF_ECS_EC2_MAX_SLOTS`）未満ならプールを増やす**
  ——誰も邪魔しないし、アイドル停止が入った今、増えたスロットの待機費用は root EBS だけ。
- **上限に達して初めて、最も長く休眠しているユーザーから取り上げる。** 選ぶのは
  `af-idle-since` が最も古い home で、**稼働中のワークスペースは絶対に選ばない**
  （`releaseSlot` がサービスの desired を見て拒否する）。取り上げは
  「umount（起動中なら）→ detach → 自分の home を attach」。
- したがって **`AF_ECS_EC2_MAX_SLOTS` は「同時に *確保* されるスロット数」の上限**であり、
  **`AF_ECS_EC2_IDLE_STOP_SEC` が「そのうち何台が *起動* しているか」を決める**。

### 64.17.4 タグ 1 つ増える（状態は増えない）

| タグ | 意味 | 誰が書く |
|---|---|---|
| `af-idle-since` | この home は attach されたまま休眠に入った時刻 | `Stop`（失われたら掃除ループが押し直す） |

休眠の長さも、立ち退きの相手も、停止の判断も**すべてこのタグと AWS の状態から導出**する。
ADR 0012 は保たれたまま。
