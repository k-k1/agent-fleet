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
   → **これは誤りだった。** ADR 0028 は Workspace を対象にしておらず、継ぎ目は**どこにも無い**。
   埋め方は §64.18（決定 13）。
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

> ⚠️ **既存の「アイドル停止」とは別のタイマーである。** 製品には前から reaper があり、
> `AF_WS_IDLE_TIMEOUT` ／ テナント毎の `ws_idle_timeout` で **人を見て Workspace を止める**
> （ランタイム非依存・docker でも同じ）。ここで足すのは **その後に効く 2 段目**で、
> **止まった Workspace が使っていた「箱」を止める**。直列に並ぶ:
> **人が離れる →（reaper）Workspace 停止 →（本節・15 分）スロット停止。**
> 名前も分けた（`AF_ECS_EC2_SLOT_SLEEP_SEC`）——同じ「idle」で括ると、どちらの話をしているのか
> 実装でも運用でも必ず取り違える。

遅延返却だけだと、停止中のユーザーが**起動したままのスロット**を抱え込む。そこで掃除ループが
**`AF_ECS_EC2_SLOT_SLEEP_SEC`（既定 15 分）で `StopInstances`** する。**terminate はしない。**

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
  **`AF_ECS_EC2_SLOT_SLEEP_SEC` が「そのうち何台が *起動* しているか」を決める**。

### 64.17.4 タグ 1 つ増える（状態は増えない）

| タグ | 意味 | 誰が書く |
|---|---|---|
| `af-idle-since` | この home は attach されたまま休眠に入った時刻 | `Stop`（失われたら掃除ループが押し直す） |

休眠の長さも、立ち退きの相手も、停止の判断も**すべてこのタグと AWS の状態から導出**する。
ADR 0012 は保たれたまま。

### 64.17.5 実機で通した結果（第 3 ラウンド d・2026-08-16）と、**計測の下方修正（2 度目）**

遅延返却・アイドル停止・立ち退きを入れたアダプタを、もう一度実 AWS で端から端まで通した
（`~/af-ec2c/` のハーネス・`teardown` 済み残存 0）。**E2E は PASS。**

| 確かめたこと | 結果 |
|---|---|
| `Stop` が home を剥がさない（遅延返却） | ✅ attach されたまま・`af-idle-since` が付く |
| 掃除ループが休眠スロットを**剥がさずに停止**する | ✅ `StopInstances`（home は付いたまま） |
| 休眠スロットを**起こして**復帰する | ✅ 起動 → 再登録 → mount → タスク |
| home が「停止 → 起動」をまたいで残る | ✅ マーカー無傷 |
| 上限到達で**最長休眠を立ち退かせる** | ✅ u2 が u1 のスロットを取り、プールは増えない |
| `Destroy`（返却 → ボリューム削除） | ✅ |

#### ⚠️ 「温かい Start は速い」がもう一段崩れた

| 経路 | 実測（複数 run） |
|---|---|
| 温かい復帰（home がスロットに載ったまま・サービス既存） | **46.4s / 100.0s**（＋ §64.16 の 13.2s は**再現しなかった**） |
| 新規 home をホットスロットへ | **43.2s** |
| 休眠スロットを起こす（`StartInstances` → タスク） | **110.1s / 110.7s** |
| 上限での立ち退き ＋ 差し替え | **109.7s** |
| プールが空 → `RunInstances` から | 135.4s / 138.5s / 151.3s |
| `Stop` → タスク drain（ユーザーは待たない） | 6.4〜16.6s |

**§64.16.3 の 13.2s は計測の産物だった可能性が高い**——同じ条件（既存 home ＋ ホットスロット ＋
既存サービス）を 3 回測って 46.4s / 100.0s / 13.2s で、13.2s だけ再現しない。直前のタスクがまだ
`RunningCount` に数えられていた窓を踏んだと見るのが自然である。**採るべき数字は 43〜110 秒**。

**そうすると、Fargate の温再 Start（~105s）に対する起動時間の優位はほとんど無い。** 内訳から見て
残っているのは「pull 35s が消える」だけで、それを **awsvpc の ENI ＋ Service Connect（§64.4.3 で
約 20s）＋ CP の mount（SSM 往復 10〜20s）＋ サービス更新の伝播**が食い潰している。

**したがって EC2 プール型の正味の価値は、当初 3 つ挙げたうちの 2 つに絞られる:**

1. ~~Start が速い~~ —— **言えない**（43〜110s 対 Fargate ~105s）。22〜27s は bridge ＋ SC 無しの
   数字で、製品の到達手段（`Endpoint()` の契約）とは両立しない。
2. **I/O と永続** —— 小ファイルが 8〜30 倍速く（§64.4.2）、home が本当に残る。ここは実機で確認済み。
3. **サイズ上限の解放** —— Fargate の 16 vCPU / 120 GiB / ephemeral 200 GiB から出られる。

**起動を本当に縮めたいなら削る先は Service Connect**（§64.4.3 の bridge ＋ 固定ホストポートで 22s）
だが、それは `Endpoint()` の契約変更であり、本 P0/P1 の範囲外である。

#### 実機でしか出なかった 2 件（どちらも修正済み）

1. **掃除ループが止めた直後の `stopping` に `StartInstances` して弾かれる**
   （`IncorrectInstanceState`）。「人が戻る」と「スロットを眠らせるタイマー」の競争は日常なので、
   **起こす処理は背景に置き、`stopped` になるまで待ってから start する**。
2. **アイドル停止が罠 §64.5 (3) を日常の経路に変える。** タスク ENI は タスク消滅から少し遅れて
   外れるので、その窓で停止したスロットは**複数 ENI で起動し、自動割当パブリック IPv4 を失う**。
   NAT の無いデプロイ（＝この sandbox）ではそこで egress が消え、**ECS エージェントも SSM
   エージェントも戻ってこなかった**。→ **タスク ENI が残っているスロットは停止しない**ガードを入れた
   （次の掃除で止まる）。本番はプライベートサブネット ＋ NAT なのでこの経路では露出しない。

もう 1 つ、**`runOnSlot` の SSM リトライが無言だった**のを直した（ログを出す）。上記 2 の状態では
mount が延々と再試行され、外からは「`starting` のまま何も起きない」ようにしか見えなかった。

## 64.18 Workspace の破棄と退避（決定 13・第 4 ラウンド）

`Destroy` は §64.15 で実装したが、**呼び出し元がどこにも無い**（§64.15.9 (3)）。ここを埋める。
決定は [ADR 0045 決定 13](decisions/0045-ec2-persistent-workspace.md#決定-13--workspace-の破棄に継ぎ目を作る自動経路は退避までしかやらない)。

### 64.18.1 配線しようとして分かったこと（3 件）

1. **メンバーシップ削除は継ぎ目にならない。** `DELETE /api/admin/memberships` は
   `status='inactive'` の**論理削除**で、コメントに「the workspace, its home and its encrypted
   secrets survive」と**意図として書いてある**。ここに破棄を足すのは設計に反する。
2. **ADR 0028（削除ロック）は Workspace を対象にしていない。** 守るのはセッション・作業コピー・
   会話で、ロックの置き場は**全部 home の中**。**停止中の Workspace のロックは CP から読めない。**
3. **穴は ecs-ec2 だけではない。** 下の表のとおり、どのアダプタもメンバーシップより長生きする
   資源を持っている。**ecs-ec2 の `Destroy` 自体も不完全だった**（EBS は消すが `base` 側の
   サービス・EFS AP・SSM を残していた）。

| アダプタ | メンバーシップより長生きする資源 |
|---|---|
| docker | コンテナ・per-user ネットワーク（Stop が畳む）・**`dataDir`** |
| native | プロセス（Stop が畳む）・**`dataDir`** |
| ecs（Fargate） | **ECS サービス**・**EFS アクセスポイント ×2**・**SSM SecureString ×2**・**EFS 上の実データ** |
| ecs-ec2 | 上の全部 ＋ **スロットの占有** ＋ **home EBS（課金され続ける）** ＋ snapshot |

### 64.18.2 退避と破棄を分ける

| | 引き金 | 動作 | 可逆 |
|---|---|---|---|
| **退避** | 休眠 N 日（テナントの `home_hibernate_after`・既定は `AF_ECS_EC2_HIBERNATE_AFTER_SEC`・**既定 0 ＝ オフ**） | スロット返却 → `CreateSnapshot` → 完了待ち → ボリューム削除 | ✅ 次の Start で復元 |
| **破棄** | 管理者の明示操作 / `purge=true` | 退避物（snapshot）も含めて全部消す | ❌ |

**自動経路は退避までしかやらない。** 「誰も押していないのに home が消える」経路を作らないため。
退避の中身は決定 4 のとおり（standard 階層・判定は最終利用日・費用は 20 GiB 実使用で $4.80 → $1.00）。

### 64.18.2.0 引き金は reaper、手を動かすのは掃除ループ（決定 14）

**「いつ始めるか」と「どう進めるか」を別の層に置いた。** 掃除ループは EC2 のタグから世界を
導出しており、テナントも CP の DB も見えない（ADR 0012）——だから期限を知らない。
テナントを知っているのは reaper なので、**引き金だけをそちらへ上げた**。

- reaper に tier 3（`hibernateHome`）。継ぎ目は `hibernatingRuntime`（`BeginHibernate`）で、
  `runtimeDestroyer` と同じ**任意インタフェース**。実装は ecs-ec2 だけ——他のランタイムでは
  設定欄ごと出さない（`GET /api/admin/ec2-pool` の `runtime` で判定する。スロットタブと同じ信号）。
- 掃除ループは**開始しなくなった**。走っている退避を進める枝（`att == nil` かつ `af-hibernating`）は
  ゲート無しのまま残す。⚠️ **副作用: `AF_IDLE_SWEEP_INTERVAL=0` のデプロイでは退避が起きない。**

実装していて効いた 3 点（どれも「設定は有効なのに何も起きない」か「余計に課金される」）:

1. **tier 3 は tier 1/2 の `return` の向こう側に置く。** あの 2 つは `running` でない Workspace を
   即 return する。退避の対象は**停止中**なので、同じ関数の中でも置く場所を間違えると
   一度も発火しない（ユニットで固定した）。
2. **`bootTime` を時計に混ぜない。** tier 2 の `idleBase` はプロセス起動時刻を下限に使う——
   分単位では正しい（CP 再起動直後に全部停止させないため）が、退避の窓は日〜週である。
   **CP が窓より高い頻度で再起動するデプロイでは、期限が永遠に後ろへ動く。**
   使えるのは永続化された `last_active_at` だけで、**読めなければ「触らない」**。
3. **`BeginHibernate` は既に `af-hibernating` が刻まれていたら何もしない。** そうしないと
   reaper と掃除ループが同じ退避を同時に進め、`CreateSnapshot` が 2 本走って孤児が課金される。
   「進行中か」の唯一の権威は AWS 側のタグである。

reaper の tier 2 と同じ 3 つの柵（ローカル mutex → lifecycle lease → runtime fence）の中で
決め直してから `BeginHibernate` を呼ぶ。柵を待っている間に持ち主が戻ってくるのが、
まさにこの操作が守るべき場合だからである。

### 64.18.2.1 退避は「1 スイープ 1 手」の再開可能な手続き

45 GiB の home の snapshot は 30〜40 分かかる（決定 4）。掃除ループはそこに座れないので、
**状態は AWS に置き、1 スイープにつき 1 手だけ進める**:

| 見えたもの | 打つ手 |
|---|---|
| snapshot 無し | `af-hibernating` を刻む → スロット返却 → `CreateSnapshot` |
| pending | 何もしない（次のスイープで見る） |
| completed | ボリューム削除（home は snapshot になった） |
| error | その snapshot を捨てて次回やり直す |

- **スロット返却が snapshot より先。** `releaseSlot` は umount → detach なので、捕まえるのは
  静止したファイルシステムになる。スロットも最短でプールへ戻る。
- **`af-hibernating` のタイムスタンプは飾りではない。** 「この休眠を捕まえた snapshot」と
  「同じボリュームの、**前回の**休眠中に撮られた snapshot」を区別する唯一の手段で、
  後者を前者と取り違えると**まだ捕まえていない作業を載せたボリュームを消す**。
  刻印より後に始まった snapshot だけを採用する。
- **`af-idle-since` は使えない。** 退避の第 1 手である `releaseSlot` がそれを消す。
  そのため Start 側の「休眠解除」は `clearDormancy`（両方消す）、`releaseSlot` は
  `clearIdle`（片方だけ）と分けてある。
- **持ち主が戻ったら退避は中止**（Start が `af-hibernating` を消す）。走り出した snapshot は
  止めずに完走させ、次の退避で「刻印より古い」として捨てる——完了しかけの capture に
  `DeleteSnapshot` を撃っても得るものが無い。
- **途中で機能を切っても、走っている退避は最後まで進める。** 中断すると snapshot と
  ボリュームの**両方**が課金され続ける。

### 64.18.3.1 捕獲中の Start は失敗させる

snapshot が pending の間に home ボリュームが無い状態で Start が来たら、**エラーにする**。
「snapshot が無い」と答えて空の home を作ると、本物が捕獲中のまま消える——
速い起動の顔をしたデータ消失になる。

### 64.18.3 復元は `Start` の中に隠す

退避済みユーザーの `Start` は、home ボリュームが**無い**ので P0 の「新規作成」経路に落ちる。
そこに 1 段挟む: **`af-workspace` タグの付いた完了済み snapshot があれば、そこから
`CreateVolume` する**。golden snapshot（決定 9）と**同じ分岐**であり、違うのは元にする
snapshot だけである——だからこの 2 つは一緒に実装する。

- 復元直後は 2.3 倍遅い（決定 4）。触った分だけなので初日に散る。
- 復元できたら snapshot は消す（次の退避でまた作る）。**消すのは volume が `available` になってから。**

### 64.18.4 Fargate で消せないもの（正直に残す）

EFS のディレクトリ（`/home/<membership>`・`/claude-config/<membership>`）は**マウントしないと
削除できない**。アクセスポイントを消してもデータは残り、**課金も残る**。
`Destroy` はこれを**エラーにせず、残置したパスとして返す**——監査ログと管理画面に出し、
「消したつもり」を作らない。EFS を本当に空にするには使い捨てタスクを 1 つ走らせるしかなく、
それは別の決定にする。

### 64.18.5 golden snapshot（決定 9）を退避と同じ分岐に載せる

新規ユーザーの home は 3 通りの作り方があり、**この順で試す**:

1. **本人の退避 snapshot**（§64.18.2）——あれば必ずこれ。無いのに golden を渡すのは
   「本人の home が見つからなかっただけ」の場合に**静かなデータ消失**になる。
2. **golden snapshot** ——`af-role=golden` ＋ `af-pool` で引き、**`af-image` が今動いている
   イメージと一致するものだけ**。boot-install 48s とキャッシュ空の初回 `npm install` を省ける。
3. どちらも無ければ空のボリューム（正しいが遅い）。

**古い golden は使わず、空 home に落とす。** 焼き直しはリリースに紐づく手作業で、忘れたときの
失敗が見えない（**新規ユーザーだけ**が古い CLI で始まり、他は何も変わらない）。CP が突合して
拒否し、警告を出し続ける方が、覚えていたことを前提にするより安全である。

**焼き方は製品に任せる。** `deploy/aws/ecs/bake-golden.sh` がやるのは「立ち上げ済みの種
Workspace の home ボリュームを snapshot に取ってタグを刻む」だけで、boot-install も
キャッシュの温めも製品の entrypoint に通させる——スクリプトが entrypoint を再実装すると、
「焼いた home」と「製品が作る home」が静かにズレていく。付いたままのボリュームは撮らない
（マウント中のクラッシュ一貫コピーを全ユーザーの初期状態にする理由が無い）。

golden は `af-membership` を持たないので、**Workspace 破棄の per-membership 掃除には
巻き込まれない**（ユニットで固定した）。

### 64.18.6 運用者に見せる面（Console の「スロット」タブ）

`GET /api/admin/ec2-pool`（super_admin のみ）と Console の管理モーダルに 1 項目（左レール「デプロイ全体 › スロット」）。
このランタイムだけが持ち込む 3 つの問いに答える:

1. **いま何台ぶん払っているか** —— 確保中 / 起動中（時間課金）/ 休止中（root EBS のみ）/ 空き。
2. **どれが眠っているか** —— 停止中のスロットは `stopped` ではなく「休止中」と出す。
   異常ではなく設計どおりの状態なので、警告色は使わない。
3. **誰の home がどこにあるか** —— スロット上 / 未接続 / 退避中 / 退避済み（snapshot）。

黙って損をする 2 つは、数字ではなく**文章**にした:

- **上限に達している** ⇒「増えません」ではなく「**次に起動する人は他人のスロットを取り上げます**」。
  運用者が知りたいのは在庫ではなく立ち退きが起きることである。
- **golden が古い** ⇒「一致しません」ではなく「**この golden は使われず、焼き直すまで新規 home は
  空から作られます**」。焼き直し忘れは、新規ユーザーだけが古い CLI で始まるという見えない失敗で、
  この画面以外に気づく場所が無い。

**退避済みの home は volume が無い**ので、ボリューム一覧だけを見せると当人が一覧から消える
（＝「home が無くなった」に読める）。snapshot を畳み込んで 1 行として出す。

**他のランタイムではタブごと出さない。** 空の表は Fargate のデプロイで「スロットが全部消えた」に
読める。API は `{"runtime":"other"}` を返し、Console はそれでタブを隠す。

Workspace 破棄はメンバー詳細の操作に置いた。**外したメンバーにだけ**ボタンが出る（サーバも
409 で拒む）。確認ダイアログには「取り消せない」「削除ロックを越える」「Fargate では EFS の実体が
残り課金も残る」の 3 点を書き、実行後は消せなかったものをトーストで出す。退職処理の側には
チェックボックス（既定オフ）で `purge` を足した。

## 64.19 本番相当（プライベートサブネット ＋ NAT）で通す（第 4 ラウンド・2026-08-16）

§64.18 までで実機に当てていなかった 4 つ——**本番相当のネットワーク**・**退避と復元**・
**golden snapshot**・**Destroy が base 側まで畳むこと**——を 1 セッションで通した。
ハーネスは `AF_HARNESS_NAT=1`（専用 VPC ＋ プライベートサブネット ＋ NAT ゲートウェイ）。
**E2E は PASS。teardown 後の残存は 15 項目すべて空**（vpc / nat / eip を含む）。

### 64.19.1 通ったこと

| 確かめたこと | 結果 |
|---|---|
| プライベートサブネット（パブリック IP 無し）でスロットが ECS に登録される | ✅ NAT 経由で ECS / SSM エージェントとも接続 |
| 同じ経路で ECR から pull できる | ✅ 初回 145s のうちの pull を含む |
| awsvpc ＋ Service Connect | ✅ `ATTACHED` |
| **§64.5 (3) の罠（タスク ENI 残留 → パブリック IPv4 消失）** | ✅ **再現しない**。失う IPv4 がそもそも無く、停止 → 起動を 3 回まわして毎回戻った |
| 退避（snapshot 完了 → ボリューム削除） | ✅ |
| **復元**（snapshot から home を作り直す・マーカー無事） | ✅ |
| golden snapshot から新規ユーザーの home を作る | ✅ タグ引きとイメージ突合 |
| `Destroy` が service・EFS AP ×2・SSM ×2 まで畳む | ✅（非同期なので消えるまで待って確認） |
| 退避中に持ち主が戻る／古い snapshot を掴まない | ✅ **前回実行の snapshot を「supersede」として落とすログが実機で出た** |

### 64.19.2 実測（NAT 構成・m7i.large・home 8 GiB）

| 経路 | 実測 |
|---|---|
| 初回（新規 home ＋ プール空） | **145.2s**（別 run で 154.9s） |
| 温かい復帰（home がスロットに載ったまま） | **84.0s**（80.7 / 87.2 / 90.2 / 97.1） |
| 休眠スロットを起こす | **91.5s**（116.2 / 128.3 / 134.9） |
| 上限での立ち退き ＋ 差し替え | **106.4s** |
| `Stop` → タスク drain | 6.4〜11.5s |
| **退避**（スロット返却 → snapshot 完了 → ボリューム削除） | **54.6〜179.2s**（8 GiB のほぼ空の home。45 GiB の実データなら 30〜40 分・決定 4） |
| **復元**（snapshot → CreateVolume → attach → mount → タスク） | **43.2 / 44.2 / 53.3s** |

**決定 12 は動かない。** 温かい復帰は 84〜97s で、パブリックサブネットで測った 43〜110s と
同じ幅に収まる。**NAT にしても起動は速くならないし、遅くもならない。**

⚠️ **復元が温かい復帰より速い（43〜53s 対 84s）のは、比較しているものが違うため。**
復元の計測はホットで登録済みのスロットとサービスが揃っている状態から始まっており、
インスタンスを起こす時間もサービス作成も含まない。「退避したほうが速い」ではない。

### 64.19.3 実機で出た誤り 4 件（1 件は製品、3 件はテストの測り方）

1. **`teardown.sh` が CFN スタックを 3 つ同時に消していた（製品側の欠陥）。**
   `-plat` / `-net` は `-pool` が import する export を publish しているので、
   CloudFormation は**削除をキャンセルする**（`Cannot delete export ... as it is in use by
   af-ec2c-pool`）。3 つまとめて delete を投げると後ろ 2 つは黙って何もせず、
   wait ループはキャンセル済みの削除を待ち続ける。**pool を消して待ってから**残り 2 つ、に直した。
2. **退避の snapshot を golden に流用していた（テスト）。** 復元が成功すると製品はその
   snapshot を消す——ボリュームと古いコピーの両方に課金させないため。`InvalidSnapshot.NotFound`
   で落ちて分かった。`bake-golden.sh` と同じ手順（停止 → スロット返却 → detached を撮る）に直した。
3. **削除の非同期性を待たずに数えていた（テスト）。** ECS はしばらく DRAINING → INACTIVE を返し、
   EFS のアクセスポイントは `deleting` のまま一覧に載る。**片付いているのに「残っている」と
   報告していた**（実状態を AWS CLI で見て初めて分かった）。
4. **立ち退きは「上限に達している」だけでは起きない（テスト）。** 前の実行のスロットが空いていれば
   u2 はそこに乗る——これが正しい。空きが無い場合だけ立ち退きを検証し、そうでない実行では
   **「立ち退きは検証していない」と明示的にログへ出す**ようにした（黙って緑にしない）。

### 64.19.4 まだ実機に当たっていないもの（§64.20 で 3 件中 2 件を消化）

- **`AF_WS_KEEP` の symlink 化**。sandbox のイメージは `workspace:0.8.0` で、まだ入っていない。
  **残っている。次にイメージを焼いたら実機で確認する。**
- ~~**`bake-golden.sh` 本体**~~ → §64.20.2 で実走。
- ~~**golden から作った home で実際にタスクを起こすところ**~~ → §64.20.2 で起動まで確認。

## 64.20 規模・スクリプト・運用画面を実機に当てる（第 5 ラウンド・2026-08-16）

同じ本番相当のハーネス（NAT ＋ **プライベートサブネット 2 本 = 2 AZ**）で、
§64.19.4 の残りと「複数ユーザー・長時間」を通した。**ライフサイクル E2E・スケール E2E とも PASS。
teardown 後の残存は 15 項目すべて空。**

### 64.20.1 何を足したか

| | 中身 |
|---|---|
| ハーネス | 2 本目のプライベートサブネット（1c）＋**その AZ の EFS マウントターゲット**。EBS は AZ に固定されるので、1 本では「別 AZ に home がある人」の経路が一度も通らない |
| `TestECSEC2LiveLifecycle` | golden の段を `bake-golden.sh` の**実走**に差し替え、**その home で実際にタスクを起こす**まで見る |
| `TestECSEC2LiveScale`（新設・`AF_ECS_EC2_LIVE_SCALE=1`） | 同時 Start 3 人／2 AZ／掃除ループ 18 分常駐／reaper 側の入口（`BeginHibernate`）|

### 64.20.2 golden は本当に効く（実測 148.5s → 91.9s）

`bake-golden.sh` を実際に走らせ、書いたタグを **CP 側の `goldenSnapshot()` が拾える**ことを
確認したうえで、新規ユーザーを起こした。

| 経路 | 実測 |
|---|---|
| 空の home から新規ユーザー（start #1・同じ run） | **148.5s** |
| **golden から作った home で新規ユーザー** | **91.9s** |

差は **56.6s** で、§64.13 の boot-install 48s ＋ キャッシュ分とほぼ一致する。
スロット上で `~/.local/bin` を覗くと、起動直後から
`agy / claude / codex / copilot / cursor-agent / opencode / rtk` が揃っている。
**種の home に書いたマーカーも新規ユーザーの home にそのまま出た**（＝中身が本当に運ばれている）。

### 64.20.3 規模（3 人同時・4 スロット・18 分）

| 確かめたこと | 結果 |
|---|---|
| 3 人が**同時に** Start（スロット確保に錠は無く、`AttachVolume` が唯一の調停役） | ✅ 3 人とも別々のスロット。**全員 running まで 143.2s**（1 人だけのときの 148.5s と同じ） |
| 別 AZ に home を持つ 4 人目 | ✅ その AZ にスロットが立ち、attach・mount・タスクまで通る（150.9s） |
| 掃除ループを 45 秒間隔で **18 分**回しっぱなし（4 ユーザー分） | ✅ 使用中の 1 人は最後まで無傷。プールは 4 のまま増減なし。停止した 2 人はスロットを**保持したまま**約 4 分で休止（`asleep`）へ |
| 18 分眠ったあとの復帰 | ✅ **82.7s**。同じスロット・同じ home |
| **掃除ループが自分から退避を始めないこと**（決定 14） | ✅ `hibernateAfter=1m` を罠として仕込んだうえで 18 分回して、**一度も始まらなかった** |
| reaper 側の入口 `BeginHibernate` | ✅ 刻印 → スロット返却 → capture。**pending 中にもう一度呼んでも 2 本目を撮らない**。掃除ループが完走させ、復元 **73.7s** |

⚠️ **同時 Start が「速い」のではない。** 3 人ぶんが 143.2s に収まるのは、3 台の EC2 が
並列に立ち上がるからで、1 人あたりの時間は縮んでいない。**採用理由は相変わらず I/O と永続で、
起動時間ではない**（決定 12）。

### 64.20.4 AZ について分かったこと（分散はしない・しかも並び順でもない）

- **新規 home の AZ は `anyAZ()`＝設定されたサブネットのうち ID の昇順で最初のもの**に決め打ちで、
  分散はしない。`AF_ECS_SUBNETS` に書いた順ではない——**1a を先に書いても、ID が小さい 1c が
  選ばれた**（実測）。テストはこれを失敗とも機能とも読ませないよう、明示的にログへ出す。
- ⚠️ **その AZ に容量が無いと、そのデプロイでは新規 home が作れなかった。** → **§64.20.8 で直した。**
- 既存 home の AZ は正しく尊重される（EBS は AZ を越えないので、ここが壊れると attach で落ちる）。
- **EFS のマウントターゲットは AZ ごとに要る。** 2 本目の AZ にスロットを立てるなら、
  資格情報側の EFS もそこにマウントターゲットが無いとタスクが上がらない。

### 64.20.5 運用画面を実データで見た（Console）

実際の CP を sandbox のプールに向けて起動し、こちら側の headless Chromium で操作した
（`console-e2e/scripts/ec2-pool-shots.mjs`）。**ホスト側の再ビルドを待たずに見るための道具**で、
通常の E2E ではない。

通ったもの: スロットタブが 4 台・2 AZ・`running`／`asleep`（「休止中」で、警告色は使わない）を
出すこと／上限に達した文章が出ること／退避中が `hibernating (pending)`、退避済みが
「ボリューム無し＋ hibernated (snapshot)」の 1 行として**一覧から消えないこと**／
golden が無いときの焼き直し案内／破棄の確認ダイアログに 3 点（取り消せない・削除ロックを越える・
Fargate では EFS が残る）が出て、**外したメンバーにだけ**ボタンが出ること。

**目で見て初めて分かった文言の誤り 2 件**（どちらもユニットでは緑）:

1. **退避が既定オフのとき「…after never」と出た。** 「しない」を「…の後に退避します」の穴に
   入れたため。0 のときは別の文にした。**既定はオフなので、これが運用者の見る普通の状態である。**
2. **4 枚のタイルが「確保中 4／起動中 3／休止中 1／空き 1」と並び、足が合わないように見えた。**
   起動中・休止中は状態の分割だが、**空きは占有の軸**で、上の 2 つと重複して数える。
   説明文を「上のうち home が付いていないもの」に変えた。

### 64.20.6 ついでに直した実機由来の 1 件

**`AF_IDLE_SWEEP_INTERVAL=0` で reaper が止まらなかった。** 読み出しの `parseDurationOr` は
「正の duration でなければ既定値」なので、`0` は未設定と同じ扱いになる——そのすぐ横のコメントが
「0 で reaper を完全に止める」と書いているのに、である（実測: `0` を渡して `interval=1m0s` で
起動した）。明示的な 0 を尊重する `intervalOff` を足した。壊れた文字列は従来どおり既定へ落とす
（綴り間違いで安全側の掃除が黙って止まるほうが悪い）。決定 14 で退避の引き金が reaper に移った
ぶん、この off スイッチは前より重い意味を持つ。

### 64.20.7 ユーザーを別の AZ へ移すには（「移動」という操作は無い・要らない）

**EBS ボリュームは AZ を越えられず、`ModifyVolume` にも AZ の項目は無い。** 移動という
操作はそもそも AWS に無く、実体は必ず **snapshot 経由の作り直し**である。
そしてそれは製品に既にある——**退避（hibernate）がまさにそれ**で、
snapshot には AZ が無く、復元は「その時スロットが取れる AZ」に作る。

**手順（運用者がやること）**

1. その Workspace を停止する（reaper のアイドル停止でもよい）。
2. **退避させる。** テナント設定の「使われない home の退避」に短い値（例 `1h`）を入れて
   reaper に拾わせる。⚠️ **これはそのテナント全員に効く**ので、1 人だけ動かしたいなら
   下の「直接やる」を使う。
3. 移したい AZ 以外に空きスロットが無い状態にして **Start** する。復元は
   `placeHome` の通常経路に乗るので、**空いているスロットの AZ**（無ければ立てられた AZ）に home ができる。
4. 元の AZ に戻したくなったら同じことをもう一度やる。

**⚠️ 行き先は指定できない。** 選べるのは「どの AZ に空きを作っておくか」だけで、
「この人を 1a へ」と名指しする API は無い。1 人だけ・行き先も指定したい場合は、
停止してスロットを返してから **AWS CLI で直接やる**のが確実である（製品がやることと同じ 4 手）:

```bash
# 対象の home を特定し（停止中・detached であること）、snapshot を作る
vol=$(aws ec2 describe-volumes --filters Name=tag:af-workspace,Values=<ws> Name=tag:af-role,Values=home \
  --query 'Volumes[0].VolumeId' --output text)
snap=$(aws ec2 create-snapshot --volume-id "$vol" --query SnapshotId --output text)
aws ec2 wait snapshot-completed --snapshot-ids "$snap"
# 行き先の AZ に、同じタグで作り直す（タグが無いと CP から見えない ＝ 新しい home が作られる）
aws ec2 create-volume --availability-zone <target-az> --snapshot-id "$snap" --volume-type gp3 --encrypted \
  --tag-specifications 'ResourceType=volume,Tags=[{Key=af-pool,Value=<pool>},{Key=af-role,Value=home},
    {Key=af-workspace,Value=<ws>},{Key=af-membership,Value=<membership>},{Key=Name,Value=<ws>-home}]'
aws ec2 delete-volume --volume-id "$vol"   # ★ 新しい方が available になってから
aws ec2 delete-snapshot --snapshot-id "$snap"
```

⚠️ **タグは作成と同じ呼び出しで付ける**（§64.15 と同じ理由——タグの無いボリュームは
CP から見えず、次の Start がもう 1 つ作って両方課金される）。
⚠️ **古いボリュームを消すのは新しい方ができてから。** 逆順にすると、失敗したときに何も残らない。

**費用と時間**: snapshot は実使用ブロックのみ（20 GiB 実使用で約 $1.00/月・決定 4）、
45 GiB の home なら capture に 30〜40 分。復元直後は 2.3 倍遅い（触った分だけ）。

### 64.20.8 新規 home は容量のある AZ を探す（§64.20.4 の穴を閉じた）

**直したこと**: 新規 home（＝まだ AZ に縛られていない人）に限り、`RunInstances` が
容量不足を返したら**次の AZ を試す**。既存 home は従来どおり自分の AZ に固定で、
そこが駄目なら失敗させる——動けない先にスロットを立てても、attach できない箱が増えるだけである。

**そのために作成順を入れ替えた（こちらが本体）。** 以前は AZ を `anyAZ()` で決めて
**先にボリュームを作って**いた。空きスロットがある限り同じ答えになるが、
**容量不足のときに他所を試す方法が「作ったボリュームを消してやり直す」しか無くなる**。
空の home なら無害でも、**snapshot から復元した home では消した瞬間にデータ消失**である
（`createHomeVolume` は復元元の snapshot を、ボリュームが使えるようになった時点で消す）。
**行き先が決まるまで作らない**ようにして、この選択自体を無くした。

副作用として、**上限に達したときの立ち退きが AZ を跨いで効くようになった**。以前は
「誰も見ないうちに決めた AZ の中で最も長く休んでいる人」だったので、
*10 分前に離席した人を追い出して、別 AZ の 1 週間放置を残す*ことがあり得た。

ユニットで固定したもの: 容量のある AZ へ移ること／**ボリュームを作り直していないこと**
（`DeleteVolume` が 0 回）／既存 home は他 AZ へ流れないこと／容量以外の失敗
（起動テンプレート不正など）は AZ を変えて撮り直さず、そのまま出すこと／
**どの AZ にも空きが無いときは home を作らずに失敗すること**（起動できなかった人のために
空のボリュームを課金しない）／立ち退きが全 AZ を見ること／
**退避 → Start で home が別 AZ に、snapshot 由来のまま戻ること**（§64.20.7 の runbook）。

⚠️ **実 AWS では未検証。** sandbox で `InsufficientInstanceCapacity` を意図的に起こせないため
（起こせるなら、それは容量が枯れている本番である）。fake は AWS のエラー文字列を模しているが、
**実際の応答がこの文字列で来るかは実機で確かめていない**。

## 64.21 AZ が落ちたとき（第 6 ラウンド・2026-08-16）

「特定 AZ の障害時はどうするか」を、まず**今の実装が実際に何をするか**から確かめた。
以下は挙動をコードで追った結果で、実 AWS で AZ を落として試したものではない。

### 64.21.1 今の実装が実際にすること

| | 起きること |
|---|---|
| **その AZ に home がある人** | **起動できない。** ボリュームは AZ に固定で、`placeHome` はその AZ から動かない。スロットに付いたままなら死んだインスタンスを起こしにいき、`waitBudget`（600s）で諦める。利用者には「起動中」が続き、claim TTL（15 分）が切れて「停止中」に戻る |
| **資格情報** | ✅ **無事。** keep 集合と `~/.claude` は EFS（リージョン資源・両 AZ にマウントターゲット）にある。決定 3-6 が「単一 AZ のボリュームは、悪い日が一度来ればログインごと持っていく」として置いた分離がそのまま効く |
| **掃除ループ** | ✅ 悪さをしない。ゴースト解除は **EC2 が terminated のときだけ**（`instanceAlive`）で、「動いているが応答しない」は触らない |
| **`releaseSlot`** | ✅ umount が失敗したら **detach しない**。マウント中の強制 detach による home 破損は起きない |
| **新規ユーザー** | 決定 15 のフォールバックで健全な AZ へ逃げる——**ただし `RunInstances` が容量エラーを返した場合だけ**。起動要求は通るのに ECS に登録されないまま終わる障害では、`waitSlotRegistered` のタイムアウトまで待たされる |

**運用者の一番の手** は **障害 AZ のサブネットを `AF_ECS_SUBNETS` から外して CP を再起動する**こと。
`poolAZs` から消えるので新規 home もプール拡張もその AZ を選ばなくなり、`subnetIn` が即エラーを
返すので、**死んだ AZ に向かって新しいスロットを立て続けることがなくなる**。

### 64.21.2 直した粗 —— 退避の刻印が残っていた

reaper の退避は `af-hibernating` を刻んでから `releaseSlot`（SSM 経由の umount）へ進む。
スロットに届かない間はここで必ず失敗し、**刻印だけが残っていた**。実害は 3 つ:

1. まだスロットに付いていて無事な home が、管理画面で「退避中」に見える
2. 掃除のたびに同じ失敗が出て、進んでいるのかどうか区別が付かない
3. **刻印が障害より長生きし、復旧後に最初に撮った snapshot が「刻印より古い」と判定される**

**この呼び出しが刻んだ場合に限り、失敗したら刻印を外す**ようにした。
既にあった刻印は触らない——本当に進行中の退避のもので、検証すべき snapshot が既にあるかもしれない。

### 64.21.3 決めたこと 2 つ

**(a) 新規 home を AZ に分散する（決定 16・「分散しない」を覆す）。**
新規 home は決め打ちの第 1 候補に従っていたので、**全員が同じ AZ に集まっていた**——
1 つの AZ が落ちたときの影響範囲が半分ではなく**ほぼ全員**だったということである。
home は退避できない以上、打てる手は「最初から同じ場所に置かない」しかない。
`growPool` が AZ を **home の少ない順**に試すようにした（同数なら従来の安定順）。

⚠️ **ただで手に入るものではない。** 1a の home は 1a のスロットにしか載らないので、
もう一方の AZ の空きスロットはその人には無価値で、**再利用の代わりにプールが伸びる**
（同じ人数でインスタンスが増える）。それが影響範囲を下げる対価である。
だから **placeHome の「空きスロット優先」には手を付けていない**——分散が決めるのは
**新しいスロットをどこに立てるか**だけで、既にあるものを使うかどうかではない。

**(b) home の予備を定期的に取る（決定 17）。**
今まで **home の AZ 外コピーは、たまたま退避が走っていた人にしか存在しなかった**。
AZ の可用性障害なら復旧を待てばよいが、**AZ を喪失したらその home は失われる**。
snapshot はリージョン資源なので、これが唯一の逃げ道である。

- 引き金は reaper の tier 4（テナントの `home_backup_every`）。**アイドルとは無関係**に走る——
  守る相手（AZ の喪失）は人が帰るのを待ってくれない。
- **使用中のまま撮る。クラッシュ一貫の写しになる。** 稼働中インスタンスの snapshot と同じ保証で、
  電源が落ちた直後と同じ状態である。静止させるには**働いている人の home を時間で取り上げる**ことに
  なり、そちらの方が製品として悪い。⚠️ `bake-golden.sh`（§64.18.5）が「付いたままは撮らない」と
  している のとは**逆の判断**で、理由も逆である——golden は全員の**初期状態**なので綺麗である権利がある。
- **自動では戻さない。** 予備は定義上 home より古く、**黙って古い home を渡す**のはこのファイルが
  ずっと避けてきた失敗そのものである。戻すのは運用者の操作（下の手順）。
- **CP に状態を置かない。** 次はいつかは最新の予備の `af-backup-at` から読む（ADR 0012）。
  2 レプリカが同じ窓で撃つと余分な 1 本ができるが、増分なので安く、保持数がすぐ落とす。
- `af-role=backup` という**第 3 の役割**にした。既存の引き方はすべて `af-role` で絞っているので、
  復元にも退避の「古い capture 掃除」にも golden 探しにも**引っかからない**（ユニットで固定）。
- **`Destroy` は予備も消す。** 役割が違うので他の掃除からは見えず、放置すると退職者のぶんが
  永久に課金される。
- 保持数は運用者の設定（`AF_ECS_EC2_BACKUP_KEEP`・既定 3）、**間隔はテナントの設定**
  （どれだけ巻き戻ってよいか＝そのテナントの判断）。**完了済みだけを数えて刈る**ので、
  差し替えが走っている間に本数が設定より減ることはない。
- 管理画面の home 一覧に「予備」列を足した。**「無し」と「さっき取った」は正反対の答え**なので、
  同じ空欄にまとめず、無いものは警告色で出す。

### 64.21.4 予備から home を戻す（運用者の手順）

自動では戻らないので、AZ を失ったあと当人を動かすのは明示的な操作になる。
中身は §64.20.7 と同じ 4 手で、元にする snapshot が予備になるだけである。

```bash
# その人の最新の予備を選ぶ（完了しているものだけ）
snap=$(aws ec2 describe-snapshots --owner-ids self \
  --filters Name=tag:af-membership,Values=<membership> Name=tag:af-role,Values=backup \
            Name=status,Values=completed \
  --query 'sort_by(Snapshots,&StartTime)[-1].SnapshotId' --output text)
# 生きている AZ に、home のタグで作り直す（★ af-role は home。backup のままでは CP から見えない）
aws ec2 create-volume --availability-zone <healthy-az> --snapshot-id "$snap" \
  --volume-type gp3 --encrypted \
  --tag-specifications 'ResourceType=volume,Tags=[{Key=af-pool,Value=<pool>},{Key=af-role,Value=home},
    {Key=af-workspace,Value=<ws>},{Key=af-membership,Value=<membership>},{Key=Name,Value=<ws>-home}]'
```

⚠️ **古い home のボリュームが残っているなら、先に消すかタグを外すこと。** 同じ
`af-membership` の home が 2 つあると、どちらが本物か CP には区別できない。
⚠️ **予備は本人の作業より古い。** 何分ぶん巻き戻るかは管理画面の「予備」列に出る。

### 64.21.5 やらなかったこと

- **健全性を見て AZ を避ける**（起動や ECS 登録が続けて失敗する AZ を一定時間スキップする）。
  容量エラーを返さない障害では新規ユーザーが死んだ AZ に流れ込んで待たされるが、
  今回は入れていない。運用者が `AF_ECS_SUBNETS` から外す方が確実で、判断も明示的である。
- **実 AWS での検証。** AZ 障害も容量不足も sandbox では起こせない。上の挙動表はコードを
  追った結果であり、**実機で AZ を落として確かめたものではない**。

## 64.22 新しいスロットに pull を払わせない（第 7 ラウンド・2026-08-16）

> ⚠️ **§64.24 で実測により撤回し、実装ごと撤去した。** 以下は「なぜそう考えたか」の記録である。

「プールを各 AZ に作っておけばいいのでは」という問いから出発して、**種インスタンスではなく
AMI**に着地した。

### 64.22.1 種インスタンス案の検討（正しいが、届く範囲が狭い）

案は「各 AZ に停止インスタンスを 1 台ずつ置き、root（＝イメージキャッシュ）だけ温めておく。
プールに入れておく必要は無い」。**3 点とも正しい**:

- 停止インスタンスは root EBS だけの課金（30 GiB ≒ $2.88/月）。
- 価値は root にある。terminate せず stop で留めているのは、まさに**イメージキャッシュが
  root ボリュームに載っている**ためで、pull は **31.8s → 0.09s**（決定 1 の実測）。
- プールに入れる必要は無い。「プールの一員」であることの意味は `poolSize`（＝上限）に
  数えられ、`freeSlots` の候補に出ることだけで、種にはどちらも要らない。

⚠️ ただし **`TerminateInstances` はこのアダプタに存在しない**（`StopInstances` だけ）。
つまりプールは上限まで育ったらそのまま残り、**決定 16 の分散と合わせると
「home のある AZ にはスロットが常駐する」状態に自然と収束する**。
種案が本当に効くのは **各 AZ の最初の 1 人だけ**である。

### 64.22.2 採ったもの —— AMI に焼く（決定 18）

温めたいのはインスタンスではなく root である。そして**インスタンス無しで温めた root を持つ
方法が AMI** で、こちらの方が届く範囲が広い:

| | 種インスタンス | **AMI** |
|---|---|---|
| 効く範囲 | 各 AZ の最初の 1 人 | **以後すべてのスロット作成**（AZ 最初・上限まで伸びるとき・新しい AZ への分散） |
| AZ の数 | AZ ごとに 1 台 | **リージョン資源。1 個で全 AZ** |
| プールの帳尻 | 上限・空きリストから除外する細工が要る | 出てこない |
| 奪い合い | 「どのセッションが種を使うか」の調停が要る（home を先に attach してから昇格させれば同じ裁定器は使える） | 起きない |
| 変更範囲 | アダプタに昇格経路 | **`SlotAmiId` パラメータの差し替えだけ**（`40-ec2-pool.yaml` は既に受けている） |
| 焼く手間 | 不要 | 焼くスクリプトが要る |

`deploy/aws/ecs/bake-slot-ami.sh` を足した。**本番と同じ起動テンプレートで**焼く
（手書きの起動条件で焼くと、焼いた箱と製品が立てる箱が静かにズレる）。

**罠 2 つ**:

1. **焼く前に `/var/lib/ecs/data/*` を消す。** 残すとこの AMI から立てた**すべての**
   インスタンスが「自分は登録済み」と思い込み、クラスタに入り直せない。
   これは決定 3-1 で既に踏んで記録してある罠そのものである。
2. **`af-role=slot` で焼かない。** CP の `freeSlots` / `poolSize` はそのタグから世界を
   作るので、焼いている最中の箱に誰かの home が載る。スクリプトは `af-role=bake` を使う。

**陳腐化は golden とまったく同じ問題**なので、同じ扱いにした: AMI に `af-image` を刻み、
CP が突合して**スロットタブに言い続ける**。焼き直し忘れは壊れず遅くなるだけで、
それ以外に気づく場所が無い。

- 突合は**インスタンスの `ImageId` から**読む（起動テンプレートからではない）。
  テンプレートを直しただけでまだ 1 台も立てていない状態を「改善済み」と報告しないため。
- **混在したら「まだ pull を払っている方」を出す。** 一部が新しいからといって「問題なし」と
  言うと、運用者が打つべき手が消える。
- **一度も焼いていない**（素の ECS-optimized AMI）と**古いものが焼いてある**は文言を分ける。
  どちらも意味は「新しいスロットは pull を払う」だが、次の行動の説明が違う。

### 64.22.3 ついでに見つけた —— CP のタスクロールに snapshot 権限が 1 つも無かった

`ec2:DescribeImages` を足そうとして `20-platform.yaml` を開いて分かった。
`Ec2SlotPool` の Action に **`ec2:DescribeSnapshots` / `CreateSnapshot` / `DeleteSnapshot`
がまったく無い**。つまり実デプロイでは:

- 退避（決定 4/13/14）も予備（決定 17）も **AccessDenied で動かない**。
- golden の引き当て（決定 9）も失敗し、新規 home は静かに毎回空から作られる。
- さらに悪いことに、`createHomeVolume` は**最初に本人の退避 snapshot を探す**ので、
  そこで失敗すると **新規ユーザーの Start がそもそも通らない**。

**なぜ実機 E2E で出なかったか**: ハーネスは CFN のタスクロールではなく
デプロイヤの資格情報で走っているためである。**「実 AWS で通した」は「本番の権限で通した」
ではない。** ここは実機検証の作法（§64.19 / §64.20）が素通りしていた穴で、
記録しておく価値がある。権限は追加した（未実機検証）。

### 64.22.4 実機に当たっていないもの（→ §64.23／§64.24 で全部当てた）

- **`bake-slot-ami.sh` 本体**。焼いていないし、焼いた AMI からスロットを立ててもいない。
  §64.20.2 で `bake-golden.sh` について学んだとおり、**スクリプトは走らせるまで走らない**。
  → **§64.24。走らせた。スクリプトの誤り 3 件と、決定 18 そのものの撤回になった。**
- **追加した IAM 権限**。CFN を当て直していない。 → **§64.23。本番相当のロールで通した。**
- 効果の数字（pull 31.8s → 0.09s）は決定 1 の実測に依っており、AMI 経由では測っていない。
  → **§64.24。AMI 経由でも pull は消えた（0.185s）。消えたうえで遅くなった。**

## 64.23 本番の権限で通す（第 8 ラウンド・2026-08-16）

§64.22.3 で見つけた穴——CP のタスクロールに snapshot 権限が 1 つも無いのに、実機 E2E は
5 ラウンド緑だった——を、**二度と起きない形**にした。

### 64.23.1 製品だけをタスクロールで走らせる

- ハーネス（`setup.sh`）が `20-platform.yaml` の `CpTaskRole` のポリシーを**そのまま取り出して**
  同じ権限のロールを作り、デプロイヤから assume できるようにする。
  ⚠️ **手で書き写さない。** 写した瞬間、それは「テンプレートが与えている権限」ではなく
  「与えていると思っている権限」になる。解決できない組み込み関数（`!GetAtt` の未知の参照など）に
  当たったら**黙って落とさず落ちる**——statement が 1 本欠けたロールは、欠けたぶんだけ本番と
  違う別物である。
- 製品側の AWS 設定生成を `awsConfigFor` 1 か所に集約し、live テストはそこだけを差し替える。
  **テスト自身の確認と後始末はデプロイヤのまま**——テストの目まで権限を絞ると、落ちたときに
  「製品が拒否された」のか「テストが見られない」のか分からなくなる。
- 資格情報は**プロファイル**（`role_arn` + `source_profile`）で渡す。静的な STS 資格情報は
  1 時間で切れ、80 分の E2E が途中から資格情報エラーになる（SDK は自分で取り直す）。
- **未設定なら明示ログを出す**: 「デプロイヤで走った＝IAM の穴は検出できない」。黙って
  デプロイヤに落ちるのが、そもそもこの穴の作られ方だった。

### 64.23.2 通ったこと（`TestECSEC2LiveLifecycle`・CP ロールで PASS）

`arn:aws:sts::…:assumed-role/af-ec2c-cp/…` で製品を動かし、**新規 home 作成 → 退避（snapshot）→
復元 → golden 実走 → Destroy** まで通った。追加した 4 つの権限のうち
`DescribeSnapshots`（新規 Start が最初に踏む）・`CreateSnapshot`（退避・予備）・
`DeleteSnapshot`（Destroy・保持数）・`DescribeImages`（スロットタブ）は**すべて実際に使われた**。

| 実測（同一セッション・本番相当 NAT・2 AZ・m7i.large・home 8GiB） | |
|---|---|
| 新規ユーザー（空 home・冷たいスロット） | **144.0s** |
| 温かい復帰（home はスロットに付いたまま） | **85.4s** |
| 眠ったスロットの起こし直し | **119.8s** |
| 立ち退き＋入れ替え | **112.1s** |
| golden から作った新規ユーザー | **89.6s** |

⚠️ **予備（決定 17）だけは live で一度も撮られていなかった**ので、AMI テストに 1 段足した
（`BackupHome` → `af-role=backup` の snapshot が 1 本 → `Destroy` が消す）。CP ロールで通った。

### 64.23.3 `AF_WS_KEEP` をやっと実機で見た

決定 3-6（資格情報だけ EFS に逃がし home からは symlink で見せる）の entrypoint 側は、
**配布済みイメージに一度も入っていなかった**——`ghcr.io/k-k1/agent-fleet/workspace:0.8.0` を
展開して確認済み（ブロックごと無い）。イメージを焼き直すのを待たずに見るために、
**`crane append` で現在の `workspace/entrypoint.sh` を 1 レイヤーだけ重ねた**イメージを作り、
それでワークスペースを起動した（docker は要らない）。

結果: `~/.config` `.ssh` `.claude` `.codex` `.git-credentials` `.gitconfig` `.claude.json` が
`/var/lib/af/keep/…` への symlink になり、**`~/.config` 越しに書いたものが keep のマウント側に
出た**（`mountpoint` も通る）。ホストから見るとリンクは切れて見えるが、それは正しい——
リンク先はタスクの中にしか無いマウントである。

## 64.24 スロット AMI に焼くのはやめる（決定 19・実測で撤回）

`bake-slot-ami.sh` を実走させ、焼いた AMI でプールを立て直して測った。**狙いは達成されている
のに、通しでは遅くなった。**

| | 素の ECS-optimized AMI | **焼いた AMI** |
|---|---|---|
| 新規ユーザーの Start | **144.0s** | **191.7s ／ 179.0s**（2 回） |
| タスク起動時の pull | 31.8s（決定 1） | **0.185s**（ECS エージェントのログ） |
| 起動 → ECS 登録（**同時に立てた A/B**） | **21s** | **77s** |
| systemd | loader 0.9s / initrd 1.0s / userspace 37.7s | loader 6.1s / initrd 8.9s / userspace **53.1s** |
| root 使用量 | 2.9G | 5.6G |

**pull は本当に消えた**（184.76ms・「Finished pulling image … elapsed=184.760898ms」）。
消えたうえで遅い。差は**起動の側**にあり、内訳（loader・initrd・デバイス待ち）がすべて
ディスク読みに寄っていることから、**新しい snapshot からの遅延読み込み**——自前 AMI の root は
初回アクセスのたびに S3 から取り寄せる——で説明がつく。Amazon の ECS-optimized AMI は
広く使われていて温まっており、こちらは焼いた分だけ**冷たいブロックが多い**。
3 台目でも縮まらなかった（登録まで 77s）。

**やめる**（決定 19）。しかも**残さず撤去した**: `bake-slot-ami.sh`、CP の `af-image` 突合、
Console の「スロットのイメージ」パネルと文言、CP タスクロールの `ec2:DescribeImages`、
ハーネスの後始末まで。**「非推奨と書いて残す」を採らなかった理由**は、既定で使わない機能は
誰も走らせず静かに腐るからである——今回もスクリプトの誤り 3 件は**実走で初めて**出た
（既定 off で出した機能が一度も発火していなかった ADR 0044 決定 3 と同じ形）。

速度以外に残る利点は「タスク起動時にレジストリへ行かない」＝ ECR 障害や pull スロットリング
からの独立だが、それは採用理由ではないし、**このプールはインスタンスを terminate しない**ので
新しいスロットを作る機会そのものが稀である（＝節約の総量が小さい）。必要になった人は
git 履歴から取り出せる。

### 64.24.1 スクリプトは走らせるまで走らない（今回の 3 件）

1. `aws ec2 run-instances --min-count/--max-count` は **CLI v2 に無い**（`--count`）。1 行目で落ちた。
2. `rm -f /var/log/ecs/*` は `exec/` がディレクトリなので «Is a directory» で落ち、
   `set -e` のブロックごと死ぬ——**2.6GB を pull した直後に**。
3. **`SlotAmiId` は AMI ID ではなく SSM パラメータ名を取る型**
   （`AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>`。既定値も ECS-optimized AMI の
   *パラメータ名*である）。スクリプトも README も AMI ID を渡すよう案内していた＝そのままでは
   deploy が落ちる。焼いた ID を `/af-slot-ami/<pool>` に書き、その名前を渡す形に直した。

罠として先に書いてあった 2 つ（`/var/lib/ecs/data/*` を消す・`af-role=bake` で焼く）は
**書いてあったとおりに効いた**: 消したうえで焼いた AMI のインスタンスはクラスタに入り直せたし、
焼いている最中の箱に home は載らなかった。

## 64.25 マウントできないスロットを隔離する（決定 20）

AMI の検証の直後、`AF_WS_KEEP` の検証で**新しいユーザーの Start が「デバイスが無い」で落ちた**。
測り方の誤りを疑ったが、今回は**箱の方が壊れていた**。スロットの上で見えたもの:

- AWS 側は「ボリュームは attach 済み」と言っている。**カーネルは前の（既に削除済みの）
  ボリュームのシリアルを持ったままの `nvme1n1` を見せている。**
- 前の利用者のコンテナが `Up 14 minutes` のまま生きており、その中のプロセスが **D 状態**
  （割り込み不可）。`xfs-cil/nvme1n1` などのワークキューも詰まったまま。
- つまり: home を剥がされたコンテナのプロセスが消えたデバイスへの I/O で固まり、カーネルが
  その名前空間を離さないので、**次に attach したボリュームが `/dev` に現れない**。

⚠️ **本当に悪いのはここから**: そのスロットは**空きのまま**だったので、以後の Start は
全員そこに入り、全員同じように失敗する。1 台のカーネルの詰まりが、黙って全員の障害になる。

直したこと（決定 20・`quarantineSlot`）: マウントに失敗したら **`af-role=quarantined` に打ち直し**
（プールの引き方は全部このタグなので、書き込み 1 回で空き・上限・配置から同時に消える）、
**home を剥がして claim を落とし**（本人が待たずに別のスロットへ行ける）、**箱を停止**し、
**画面には残す**（まだ課金されている箱が画面から消えるのは、気づけない形の請求になる）。

実機で確認した: 壊れた箱でもう一度 Start → **隔離され（`af-quarantine-at` 付き）**、停止に入り、
次の Start は**新しいスロットを立てて成功**した。おまけに、**この停止が詰まった detach を
完了させた**（stopping → stopped でボリュームが `available` に戻った）。

ついでに 1 件: `Destroy` 直後のボリュームは `deleting` のまま**約 40 分**残ることがあり、
その間スロットタブは消えたはずの home を「デタッチ済みの home」として出していた。
`homeVolume()` は昔から `deleting` を飛ばしている——画面も同じにした。

## 64.26 スロットが眠らない構成が既定だった（実デプロイ・2026-08-17）

本デプロイを一晩置いて分かった。**Workspace を起動したままにしていたら、9.4 時間後も
スロット（m7i.large）が起動したまま**だった。故障ではない——**誰も止めろと言っていなかった**。

- reaper は 1 分おきに回っている。しかし `AF_SESSION_IDLE_TIMEOUT` / `AF_WS_IDLE_TIMEOUT` の
  デプロイ既定が **0＝しない**で、`30-ingress.yaml` もこれらを渡していなかった。
  テナント側の `ws_idle_timeout` も未設定だったので、tier 1 も tier 2 も一度も発火しない。
- ⚠️ **この既定が、EC2 プールの費用の話を丸ごと無効にする。** スロットが眠る条件は
  「**タスクが無くなって** 15 分」であり、Workspace が止まらなければタスクは消えない。
  つまり決定 11 の「停止したスロットは root ボリュームだけ」は、**既定のデプロイでは
  一度も起きない**。実測でも 8/16 の EC2 課金は $0.82＋$2.33（EC2-Other）だった。

**直したこと**: デプロイ既定を **セッション 1 時間 / ワークスペース 2 時間** に変えた。
止まって失うものは無い（claude セッションは停止中＝再開可、Workspace は次の訪問で自動起動）。
テナント管理者は Admin UI で上書きでき、**`0` を入れればそのテナントだけ無効**にできる。

⚠️ **既定を非ゼロにした瞬間、`AF_WS_IDLE_TIMEOUT=0` が効かなくなる罠**があった。
読み出しの `parseDurationOr` は「正の duration でなければ既定値」なので、運用者の明示的な
オフが 2h に化ける——`AF_IDLE_SWEEP_INTERVAL` で踏んだのと同じ形（§64.20.6）。
明示的な 0 を尊重する `intervalOff` に替えた。

**セッション停止（tier 1）は claude だけ**である（`s.Kind != "claude"` で弾いている）。
halt は殺すことなので、jsonl で永続していて再開できる claude だけが対象にされた。
codex / opencode / copilot / cursor / kiro とシェルは tier 2（ワークスペースごと停止）で回収される。
他の種を tier 1 に載せるなら、**種ごとに「halt してから本当に再開できるか」を実機で確かめてから**
にすること——できないまま載せると、止めた瞬間に会話を失う。

## 64.27 サイズ選択が `ecs-ec2` と噛み合っていない（2026-08-17）

決定は [ADR 0045](decisions/0045-ec2-persistent-workspace.md) 決定 21。
ここは**そこへ至る調査**である。出発点は「ユーザー／テナントが選ぶのはメモリと CPU なのに、
`ecs-ec2` は `slotTypeFor(memBytes)` でメモリだけを見ている。実態と合っているか」。

### 64.27.1 実装を読んで確定したこと（5 件）

いずれもコードを追って確定した。AWS には当てていない（当てなくても分かる種類の齟齬である）。

| # | 画面に出ているもの | `ecs-ec2` での実態 |
|---|---|---|
| 1 | ワークスペースの CPU（Fargate 単位・`= N vCPU`） | **使われない。** `fargateSize()` は `runtime_ecs.go` にしか無く、EC2 のタスク定義に `cpu` は入らない |
| 2 | ワークスペースのメモリ（＝上限） | **上限ではない。** `memoryReservation: 512`（ソフト）のみ。値は**箱を選ぶ必要量**で、実際には箱の全量が使える |
| 3 | 作業ディスク「停止すると消えます」 | **逆。** `homeGiB()` に入り、**永続 home の EBS サイズ**になる |
| 4 | ディスクを増やす／減らす | **home 作成時のみ有効。** `ModifyVolume` は未実装で、EBS はそもそも縮小できない |
| 5 | 稼働チップ「実行中／停止」と「強制停止」 | **ECS では常に「停止」。** `containerStats()` が `docker inspect` を叩くため。結果として強制停止ボタンが永久に `disabled` |

5 は Fargate（`AF_RUNTIME=ecs`）でも同じで、`ecs-ec2` 固有ではない。**このランタイムの
実デプロイを触ったから見つかった**というだけで、直す範囲は ECS 系の両方になる。

⚠️ 2 は「嘘」の中でも性質が違う。1・3 は**言っていることが違う**が、2 は
**言っていることは合っているが意味が違う**——メモリを 4096 と書いた人は、4 GiB に制限された
とも、4 GiB を確保したとも読める。実際に起きるのは「8 GiB の箱に 1 人で入る」である。
決定 8（1 スロット 1 ユーザー専有・予約を掛けない）が、そのまま UI の語彙の問題になっている。

### 64.27.2 「インスタンスタイプで選ばせる」を検討して落とした

一見すると素直で、`slotTypeFor` を消せる。落とした理由は 2 つ。

- **同じ画面が 4 ランタイムを相手にする。** `m7i.xlarge` は docker / native / Fargate には
  存在しない。ランタイム毎に別の設定画面を持つ設計にはなっていない（テナント上限
  `max_workspace_mem` も、メンバーの `user_limit` も、ランタイム中立な数値で保存される）。
- **選択の自由度は元々「梯子の何段目か」しかない。** 実行できる型は運用者が
  `AF_ECS_EC2_SLOT_TYPES` で閉じている（既定 3 段）。3 段のうちどれか、はメモリで表せる。
  型番を出す価値があるのは**運用者向けのスロットタブ**であって、テナント管理者の画面ではない。

抽象サイズ（S/M/L）を保存の形にする案も落とした。S/M/L は既に `WS_SIZE_PRESETS` として
**画面側にだけ**あり、それが正しい位置である（ADR 0044 決定 1：保存はランタイム中立な 3 軸）。

### 64.27.3 採った形——「ランタイムが意味論を申告し、画面がその通りに言う」

新しい軸も新しい保存形式も足さない。足すのは**申告**である。

```
GET /api/admin/workspace-sizing        (tenant_admin 可)
{ "runtime":"ecs-ec2",
  "cpu_effective": false,              // CPU 欄を出すか
  "mem_meaning": "slot",               // "limit"（上限）か "slot"（箱を選ぶ必要量）か
  "disk_meaning": "home",              // "work"（停止で消える）か "home"（永続）か
  "disk_default_gb": 50, "disk_create_only": true,
  "slots":[{"instance_type":"m7i.large","mem_mib":8192,"vcpu":2}, ...] }
```

他 3 ランタイムでは `{"runtime":"local","cpu_effective":true,"mem_meaning":"limit",
"disk_meaning":"work",...}` を返し、**画面は今のままになる**。`hasPool` で退避・予備の欄を
出し分けているのと同じ型で、判定を画面のハードコードではなく CP の申告に置くのが違い。

**梯子の vCPU は `AF_ECS_EC2_SLOT_TYPES` を `type:memMiB[:vcpu]` に拡張して運用者から受け取る**
（省略時は vCPU を出さない）。`ec2:DescribeInstanceTypes` で AWS に聞けば正確だが、
**表示のためだけに CP のタスクロールへ IAM 権限を 1 つ増やす**ことになり、
20-platform.yaml の更新と実機再デプロイを伴う。運用者が既に知っている 1 語を書く方が安い。

### 64.27.4 「0（未設定）」の意味がランタイムで違う

`slotTypeFor(0)` は `want=0` なので**最小スロットを返す**。一方 Fargate の 0 は
「デプロイ既定（`AF_ECS_TASK_MEMORY`）」であり、docker の 0 は `WS_MEMORY` である。
つまり **0 の意味だけは元から 3 通り**あり、`ecs-ec2` では「最小スロット」が答えになる。
これは仕様として妥当（梯子の下段から使うのが安い）なので変えず、**画面にそう書く**
（`0 = 最小スロット（m7i.large）`）。

### 64.27.5 実機に当てるもの／当てないもの

- 当てない: 齟齬 1〜4 の判定（コードで確定する）。文言と項目の出し分け（Console のテスト）。
- **当てる**: `AF_ECS_EC2_SLOT_TYPES` に vCPU を書いた形で CP が起動すること、
  `GET /api/admin/workspace-sizing` が実デプロイで `ecs-ec2` を申告すること、
  齟齬 5 の修正後にメンバー詳細が実際に「実行中」を出し、強制停止が押せること。
  ——最後の 1 つは**実機でしか確かめられない**（docker が無い環境が前提の不具合なので）。

### 64.27.6 実機に入れた（2026-08-17・<dev-deployment>）

CP イメージを crane で合成し直し（`ghcr.io/…/control-plane:0.8.0` に今ビルドした `af-cp` と
`console/dist` を 1 層）、`30-ingress` を更新して `Ec2SlotTypes` に vCPU を入れた。

- 起動ログ: `control-plane dev-511ac9d6 … runtime=ecs-ec2` /
  `runtime=ecs-ec2 pool=… slots=m7i.large:8192:2,m7i.xlarge:16384:4,m7i.2xlarge:32768:8`。
  **`type:memMiB:vcpu` を書いても CP は起動する**（後方互換の確認）。
- `GET /api/admin/workspace-sizing` は 404 ではなく **401**（ルートは生きている）。
- **走っているタスクのイメージ digest が push した digest と一致**
  （`sha256:bf5e0cf0…`）。⚠️ 前回ここで「Console だけ古い」を踏んでいるので、
  **push 前にレイヤ tar の中身を照合し**（`index.html` が参照する資産が全部入っているか）、
  **push 後に digest を突き合わせる**——「合成したつもり」を残さない。

⚠️ **ここから先はログイン後の画面**なので headless では見られない（Google が弾く）。
残っているのは、メンバー詳細で **CPU 欄が消えていること・メモリ欄が箱を言うこと・
「実行中」が出て強制停止が押せること**の目視で、人が自分のブラウザで見る分担にした。

## 64.28 golden を実デプロイで焼く（<prod-deployment>・2026-08-21）

§64.20.2 で `bake-golden.sh` は実走済み——**ただし live test は、スクリプトを呼ぶ直前に
Go から `u1.releaseSlot(ctx)` を直接叩いていた**（`runtime_ecs_ec2_live_test.go`）。操作者に
その手段は無い。実デプロイ（<prod-deployment> / 0.9.1）で **README どおりの手順だけ**を踏んだら、
2 つとも止まった。段階 B（CP による自動焼き）の仕様は、ここで分かったことで決まる。

### 64.28.1 種は「サインインできる人」でなければならない

`POST /api/workspace/start` は `withResolved` → `resolveFull(id.key, …)` で **常に呼び出し元
自身**に解決される。admin API に**「このメンバーの Workspace を起動する」は無い**
（stop / clean-home / destroy はある。Console も同じ 3 つだけ）。だから
「捨てる前提のアドレスで種メンバーを作る」と、**誰も起動できないメンバーシップ**ができる。
さらにこのデプロイは `AF_OAUTH_ALLOWED_EMAILS` が 1 アドレスなので、2 つ目の Google
アカウントを足す案も最初から通らなかった。

**通った形**: 使い捨てテナントを作り、**自分が持っているアカウント**をそこに足す。同じ
identity の**別メンバーシップ**なので home はまっさらで、テナント選択は `X-AF-Tenant` だけ
——ログインは 1 つで足りる。（プロバイダを 1 つも指定しないテナントは全プロバイダを受ける。）

> 段階 B では消える制約である。CP の中には `resolveByMembership(identityID, membershipID)`
> ＋ `ensureWorkspaceStartedUnattended` という**セッション不要の起動経路**が既にあり、
> 定時実行（`scheduler_wake.go`）がそれで止まっている Workspace を起こしている。

### 64.28.2 「スロットが返るまで待つ」は永遠に来ない（★ 本命）

`Stop()` はボリュームを**付けたままにする**（attachment ＝ その人のスロット、§64.17）。
スイーパーが 15 分後にやるのも**インスタンスの停止だけ**で、ログはそう言っている:

```
09:44:55 ecs-ec2 sweep: af-ws-golden-seed-… has been dormant 19m;
         stopping slot i-033f04adaf5d53f9e (home stays attached)
09:45:23 instance=stopped attach=attached      ← 眠っても付いたまま
```

実際に外すのは eviction / Destroy / ドリフト修復 / 退避だけ。だから手順どおり停止して
19 分待った後でも、`bake-golden.sh` は **「停止してスイーパーを待て」と言って拒否する**
——今やったことをもう一度やれ、という行き止まりである。

**直した形**: 拒否するのは **running なスロットに付いている場合だけ**にした。停止済みなら
撮ってよい——インスタンスの停止は通常のシャットダウンでファイルシステムを umount するから
で、これは製品自身が `releaseSlotSince` で立っている根拠（停止済みスロットは SSM が届かない
ので umount を省く）と同じものである。README の手順も「**インスタンスが stopped になるのを
待つ。ボリュームが外れるのを待つのではない**」に直した。

### 64.28.3 golden から作った home が起動できなかった（★ 起動不能・修正済み）

焼いた golden で新規ユーザーを起こしたら、**タスクが無限に再起動した**。原因はどのログにも
出ない。出ていたのは 1 行だけ:

```
mkdir: cannot create directory ‘/home/dev/.config’: File exists
```

`entrypoint.sh` の identity 退避ループは「もう正しい symlink だ」と判断すると
early-continue し、**向き先を作る `mkdir -p "$dst"` を飛ばしていた**。golden から作った home は
種が張った symlink を丸ごと持ってくる一方、**keep 側（EFS）は新規ユーザーごとに空**なので、
`~/.config` は宙に浮いたままになる。そこへ `mkdir -p "$HOME/.config/opencode"` が来て
`File exists` で落ち、`set -e` で entrypoint ごと死ぬ。

- **空 home では絶対に出ない。** 空 home には実体の `~/.config` があり、それが keep へ
  `mv` されるので向き先が必ずできる。**golden 経由だけの不具合**であり、golden を使わない
  限り誰も踏まない——つまり `bake-golden.sh` を実際に使うまで見えなかった。
- 実機で確かめた: 焼き込まれた symlink 4 本を home から取り除くと、**同じイメージがそのまま
  起動した**。keep ループが全経路を通り、keep 側を作り、貼り直したからである。
- 直しは early-continue の枝でも keep 側のディレクトリを作ること。回帰は
  `deploy/local/keep-relocate-test.sh`（CI の `workspace-agent` job）。修正前のコードに
  当てると**本番と同じエラー文で落ちる**。

⚠️ **段階 B への要求**: 自動焼きは、**起動を確かめていない golden を公開してはならない**。
この不具合は「焼けた」までは全部成功に見え、壊れるのは**次に来た新規ユーザー**であり、
しかも症状は再起動ループだけである。焼いたら種以外で一度起こして、駄目なら
`af-role=golden` を付けない（＝ CP は空 home に落ちるだけで、誰も壊れない）。

### 64.28.4 実測

| 経路 | entrypoint 開始 → Agent 待受 | 備考 |
|---|---|---|
| 空 home（boot-install あり） | **36s** | 4 CLI ＋ rtk ＋ agy ＋ cursor を**ネットワークから**取得 |
| golden | **12s** | `already present (skip)` ×4 ＝ **ネットワーク不要** |

端から端（`POST /api/workspace/start` → 待受）は空 home で **163s**。うち **127s は
EC2 の起動とイメージ pull** で、golden はそこには効かない（プールを新しく生やす経路だったため）。
golden 側の端から端は、起動不能を手で直して測り直した合成値で **≒139s**。

> §64.20.2 の 148.5s → 91.9s と差が小さいのは、**boot-install がその日は速かった**から
> （25s。§64.13 の実測は 48s）で、golden の効き幅はネットワークの状態で動く。**動かないのは
> 「ネットワークに一切依存しない」の方**で、そちらが本来の売りである。

その他:

- **snapshot は 3 分弱で完了した**（50 GiB のボリューム / 実使用 1 GiB 強）。待ち時間は
  ボリュームのサイズではなく**使用ブロック量**で決まる。スクリプトが言っていた
  「45 GiB で 30〜40 分」は退避 snapshot の数字で、種には当てはまらない。
- **種に repo を clone してはいけない。** `~/repos` は home の上なので、clone すると
  **それが新規ユーザー全員に配られる**。§64.13 の golden が `node_modules` 込みだったのは
  検証ハーネスの都合であって、実デプロイで真似する話ではない。焼く範囲は boot-install まで。
- golden は**種の MCP サーバ名**（`af_a6d00334`）も運んでくるが、初回起動の
  `mcp materialize … removed [af_a6d00334]` で掃除される。実害なし。
- identity が golden に載らない設計は実物で確認できた: `AF_WS_KEEP` は
  `/var/lib/af/keep` という**全コンテナ共通の固定パス**なので、焼かれた symlink は
  誰にとっても正しく、種の資格情報は EBS 側に最初から存在しない。

## 64.29 golden を CP が焼く（決定 9-1・2026-08-21）

§64.28 で手順そのものは通った。しかしそれは**手順**であり、守られなかったときに気づく手段は
「CP のログに警告が出続ける」だけだった。**引き金は「デプロイした」ことではなく「イメージが
変わった」ことで、その判定は CP が既に持っている** —— `goldenSnapshot()` が `af-image` を
突合して古い golden を拒否しているのがそれである。持っている判定を行動に変えた。

置き場は deploy スクリプトではなく **CP 本体**。理由は 3 つ:

- トリガが「イメージが変わったこと」で、それを知っているのが CP だけである。CP を別の理由で
  再起動しただけで焼き直してはいけない。
- deploy スクリプトは AWS の資格情報しか持たず、種メンバーの作成は CP のデータベース越しである。
- **§64.28.1 の制約（種はサインインできる人でなければならない）が CP の中では消える。**
  `resolveByMembership` ＋ `ensureWorkspaceStartedRT` というセッション不要の起動経路が既にあり、
  定時実行（`scheduler_wake.go`）がそれで止まっている Workspace を起こしている。

★ **予約テナントと予約メンバーシップは「入れ物」であって人ではない**（→ docs/61 §61.18）。
`af-golden` は焼き直しのたびに使い回され、毎回捨てられるのは workspace と home とスロットだけ。
2026-08-22 に管理画面の一覧・稼働時間の面から外し、種と probe の費用は共有インフラへ寄せた
（タグは打ったまま、取り込みで畳む——`af-membership` は照合キーでもあるため。ADR 0048 決定 13）。
削除もさせない（次のベイクで作り直されるだけ）。**スロットプール画面の golden 表示は別物**なので
そのまま残っている。

### 64.29.1 形は退避と同じ（1 ティック 1 手・状態は AWS）

焼きは数分かかる。どのループも待ってはいけないので、退避（§64.18.2）と同じ流儀にした:
**各手は「もう済んでいれば何もしない」**ので、CP が途中で落ちても次のティックが続きから進める。

| いま AWS に見えているもの | 打つ手 |
|---|---|
| golden 無し・候補無し | 種を起こす（無ければ作る） |
| 種の Agent が応答した | home に `af-bake-ready` を打ち、停止する |
| 種は停止・home はまだ動いているスロット上 | `releaseSlot`（umount ＋ detach） |
| home が外れた | `af-role=golden-candidate` として撮る |
| 候補が完了 | **probe** をその候補から起こす |
| probe の Agent が応答した | 候補を `af-role=golden` へ昇格・旧 golden を削除・種と probe を破棄 |
| probe が期限内に上がらない | 候補を `af-role=golden-rejected` にして理由を刻む |

`releaseSlot` が呼べるのが CP 側の効きどころで、**§64.28.2 の「15 分待ち」も「停止済み例外」も
要らない**。操作者にとってシャットダウンを待つしかなかったものが、ここでは SSM 1 回である。

### 64.29.2 なぜ probe が要るのか・なぜ**新しい**メンバーシップでなければならないのか

§64.28.3 で踏んだとおり、**起動不能な golden は「snapshot completed」まで全部成功に見える。**
壊れるのは次に来た新規ユーザーで、症状はタスクの再起動ループだけである。だから
**何かが実際にそこから起動するまで公開しない。**

そして probe は**履歴の無いメンバーシップ**でなければならない。あの不具合は
**keep 側 EFS が新規のときだけ**出たので、種を自分の golden から起こし直しても捕まらない
（種の identity ディレクトリは種の keep 上に既にある）。probe の Workspace を破棄すると
EFS アクセスポイントごと消えるので、次の probe は本当に「初めての人」から始まる。

候補が誰にも見えないことは**タグの役割名そのもの**で担保した。`goldenSnapshot()` は既定で
`af-role=golden` しか引かず、候補を引くのは自分でそう宣言した probe だけである
（`ecsEC2Runtime.seedRole`）。「先に昇格させて駄目なら降格」も検討したが、その窓の間に来た
新規ユーザーには未検証の golden が配られる——今回踏んだのと同じ形なので採らない。

### 64.29.3 歯止め

- **スロットが 2 つ空いていなければ始めない。** golden が無い代償は初回起動が遅いことだけで、
  誰かを追い出す理由にはならない。⚠️ この判定は**開始時だけ**に効かせる。走り出した焼きを
  「プールが埋まったから」と捨てると、種のスロットを毎ティック握ったまま放置することになる。
- **同じイメージで 2 回失敗したら諦める。** 1 回は AZ の機嫌、2 回はイメージである。
- **拒否した候補は消さない。** それが「このデプロイに golden が無い理由」であり、同時に
  数え札でもある。削除すると壊れたイメージを永久に焼き直し続ける。
- **`AF_ECS_EC2_GOLDEN_AUTOBAKE=0` で切れる。既定は ON。** 切っておくと、これが取り除く失敗
  （リリースのたびの焼き直しを誰も覚えていない）がそのまま残る——**既定 off で出した機能は
  存在しないのと同じ**である（ADR 0044 決定 3 で一度やっている）。
- **idle-stop の reaper には相乗りしない。** `AF_IDLE_SWEEP_INTERVAL=0` は「アイドル停止を
  切る」という意思表示であって、golden について何も言っていない。自前のティッカーで回す。
- **種が起動しきらなかった場合は「諦め」に数えない。** それはスロットについての証拠であって
  イメージについての証拠ではない。種を畳んで、次のティックでやり直す。

### 64.29.4 運用画面に出す

拒否は**イベントではなく状態**なので、プール画面に出し続ける（「用意しています」／「使いません:
理由」）。§64.28.3 で学んだのはまさに「症状が再起動ループしかなく、CP ログの 1 行は流れる」
ことだった。→ この 4 状態では足りなかった（焼きの前半と、焼かれない理由 3 つ）。**§64.30**。

## 64.30 焼き込みの進み具合を管理画面に出す（2026-08-22）

§64.29.4 で出したのは 4 状態（用意しています／使いません: 理由／古い／これを使っています）
だけだった。実際に自動焼きが走るデプロイで見ると、これでは足りない。

**足りなかったこと 1: 焼きの前半は「何も無い」と表示されていた。** 端から端で約 11 分の焼きの
うち、種の起動・boot-install・スロット解放（§64.29.1 の 3 手）には **snapshot がまだ存在しない**。
「用意しています」は候補 snapshot の有無で判定していたので、その間ずっと「golden はありません」
と出る。**初回起動が遅い理由を調べに来た運用者が読むのは、起きていることの逆**である。

**足りなかったこと 2: 焼かれない理由 3 つがログにしか無かった。** どれも「イベント」ではなく
**状態**で、しかも自然に直るのは 1 つだけ:

| 状態 | 直るか | 以前の見え方 |
| --- | --- | --- |
| まだ何も無い（次のティックで始まる） | 直る | 「ありません」 |
| スロット不足で保留（`3/4 slots in use`） | 空くまで直らない | 「ありません」 |
| 2 回失敗して打ち切り | イメージを直すまで直らない | 「使いません: 理由」 |
| `AF_ECS_EC2_GOLDEN_AUTOBAKE=0` | 誰かが入れるまで直らない | 「ありません」 |

保留は実デプロイ（<prod-deployment>）で実際に焼きを止めた。歯止めは設計どおり正しく効いていたのに、
効いたことは CP ログの 1 行にしかなく、`Ec2MaxSlots` を上げるまで誰も気づけなかった。

### 64.30.1 6 段の進行として出す

`GET /api/admin/ec2-pool` の `goldens[]` に `phase` を足す。段は状態機械
（golden_bake.go）と同じ順・同じ名前:

```
seed → boot → capture → snapshot → probe → published
種の箱   boot-install  home 切離   EBS コピー  起動確認   公開
```

**判定は「いちばん先の成果物が何か」で、逆順に見る**（`describeBake`）。候補 snapshot は home を
捕った後にしか存在せず、捕られた home は boot-install の後にしか存在しない——だから前の段の
残骸が残っていても、最初に当たった段が本当の段になる。

- **状態は AWS のまま**（ADR 0012）。`phase` はプール画面がすでに読んでいる volume / instance /
  snapshot から導出するだけで、**AWS 呼び出しは 1 本も増えていない**。CP に焼きの進捗を持たせて
  いたら、CP の再起動で画面から進捗が消える。
- **経過時間の起点は、焼き自身の締切と同じ値にする**（種は home ボリュームの `CreateTime`、
  候補は `af-bake-started`）。別の起点で数えると、「4 分経過」と表示しながら 20 分の締切で
  畳まれる、という説明のつかない画面になる。
- **`auto_bake` だけは AWS から導出できない**ので CP（`manager.autoBakeGolden`）が足す。切られて
  いるときの `idle` / `blocked` は `off` に潰す——**焼く気が無いのだから、プールの空きは理由では
  ない**。走っている最中の焼きは潰さない（その資源は実在し、誰かに伝える必要がある）。
- **予約 workspace（種・probe）を画面に出す。** 焼きはスロットを 1 つ握るので、出さないと
  スロット表に「知らない誰かの占有」が並ぶ。`af-golden` テナント自体は管理画面から隠してある
  （0.10.0）が、**スロットの占有は隠してはいけない**。

### 64.30.2 歯止めの数式は 1 本に

「2 つ空いていなければ始めない」を画面側にも書くと、必ず片方だけが変わる。`bakeCapacityBlocked`
に切り出して baker と画面で共有する——**画面の仕事は baker の判断を説明することなので、二重定義は
そのまま嘘になる**。

## 64.31 空きスロットが停止されない（実デプロイ・2026-08-23）

決定は [ADR 0045](decisions/0045-ec2-persistent-workspace.md) 決定 22（決定 11-2 の補追）。

[docs/72](72-cp-arch-and-availability.md) の CP マルチアーキ検証のついでに、<dev-deployment> の EC2 を
数えて見つけた。

```
i-0711ed3e5de6d168e  m6i.large  running  タスク 0   ← 前日から
i-05ec4df1eedbfe787  m8g.large  running  タスク 1   ← 利用者の Workspace（正常）
i-048dd841688dfc854  m8g.large  running  タスク 0   ← 前日から
i-06277484b5df5d620  m7i.large  running  タスク 0   ← 前日から
```

`aws logs filter-log-events --filter-pattern '"sleep"'` を 24 時間分かけて **1 件も出ない**。
`af-role=home` のボリュームは利用者の 1 本だけ。**故障ではなく、そもそも止める経路が無かった。**

### 64.31.1 真因——sleep 判定が「home の走査」の中にしか無い

`sweep()` は `af-role=home` のボリュームを起点に回る（`sweepVolume` → `sweepSlotOwnerTags` →
`sweepGhostInstances`）。**`idle < slotSleepAfter` → `StopInstances` は `sweepVolume` の中だけ**に
あるので、**到達できるのは「home が付いたままの」スロットに限られる**。

⇒ **`releaseSlot` された瞬間、そのスロットは sleep 判定の対象から外れる。** `StopInstances` の
呼び出しは製品全体で 2 か所しか無く、もう 1 つは `quarantineSlot`（故障箱の隔離）だった。
`sweepGhostInstances` は **EC2 が既に消えている** container instance を deregister するだけで、
running なインスタンスには何もしない。

踏む経路は 4 つ: **(a) 立ち退き (b) サイズ／クラス変更 (c) `Destroy` (d) golden の seed / probe の
終了**。<dev-deployment> の 3 台は (c) と (d) の残骸である。

> ⚠️ **これは §64.26 と同じ形の失敗の 2 度目**である。あのときは「タスクが消えないので
> スロットが眠る条件に届かない」だった。今度は「タスクも home も消えたので、眠らせる走査から
> 落ちた」。**どちらも「誰も止めろと言っていなかった」**——費用の約束は CFN の説明文に書いてあり、
> それを守る実装が無いことは、請求書を数えるまで誰にも見えない。

### 64.31.2 直し方——スロット起点の走査を足す（`sweepFreeSlots`）

| 対象 | 起点 | 休眠の刻印 | 走査 |
|---|---|---|---|
| home が載ったまま休眠しているスロット | ボリューム（`af-role=home`） | `af-idle-since`（ボリューム） | `sweepVolume`（既存） |
| **home を持たない空きスロット** | **インスタンス（`af-role=slot`）** | **`af-slot-idle-since`（インスタンス）** | **`sweepFreeSlots`（新）** |

- **刻印はインスタンスのタグしかあり得ない。** ADR 0012（状態は AWS に置く・CP はメモリを
  持たない）を保つ以上、空きスロットには `af-idle-since` を書く先（home）が無い。
- **書くのは `releaseSlot`、欠けていたら掃除ループが押し直す。** `af-idle-since` と同じ
  「実行者が書き、掃除ループが直す」分担。**この修復路が無いと、既に走っている 3 台が永遠に
  対象外のまま**になる（刻印を持たないので）。**取られたら消す**——1 スイープ（5 分）より短い
  占有だと、前回の空き時刻が残ったまま次の解放を迎え、猶予がゼロになる。
- **刻印の初回は必ず「押すだけ」で、止めるのは次のスイープ。** これが CP 再起動と、デプロイの
  たびに 51 秒重なる 2 レプリカ（[ADR 0053](decisions/0053-cp-arch-and-availability.md)）を
  跨いで同じ答えになる根拠。`StopInstances` は冪等なので二重に撃っても無害。

**⚠️ 使用中かどうかは EC2 と ECS の両方に聞く。** 「home が付いていない」は
「何も載っていない」と**同じ主張ではない**——baker の probe のように home 無しでタスクが動く
経路がある。だから **(1) ボリュームの attach (2) 生きた `af-claim`（attach 前の Start）
(3) ECS の running + pending タスク数** の 3 つを見て、**1 つでも「使用中」なら止めない**。

**⚠️ `taskENIsAttached` のガードは空き経路にも通す。** §64.17.5 の 2 件目（タスク ENI が残った
まま停止すると MULTI-ENI で復帰しパブリック IPv4 と egress を失う・決定 3-3）は**空きスロットでも
同じ窓に入る**。占有経路と同じ扱いで、残っていれば次のスイープに回す。

**⚠️ 停止直前に占有を読み直す。** `homes` はスイープの先頭で読んでいて、その間に Start が
attach し得る。読み直さないと窓がスイープ 1 回分まるごと開き、**着地したばかりの Workspace が
乗った箱を止める**ことになる（回復はするが `releaseGrace` を待つ）。

### 64.31.3 温かい空きは残さない（決定 11-4 をそのまま延長する）

| 経路 | 実測（§64.17.5） |
|---|---|
| 空き**ホット**スロットへ新規 home | **43.2s** |
| **停止**スロットを起こす | **110.1s** |
| プールを増やす（`RunInstances` から） | **135.4s** |

**箱を停止で維持している時点で「135s を払わない」という価値は既に取れている**（停止スロットは
`freeSlots` の候補に普通に並ぶ）。running のまま置くことが買うのは **43.2s と 110.1s の差＝
67 秒**だけで、対価は 1 台あたり **月 $95 対 $9.6**。

§64.17.2 は既に「**ホットな空きの事前確保（プレウォーム）は入れない**（利用者判断）」と決めて
おり、README も "No hot spare is kept." と書いている。⇒ **今までの実装のほうが、誰も台数を
決めていない無制限のプレウォームになっていた**、というのが正しい整理である。温かい空きが
欲しいデプロイは `Ec2SlotSleepSec=0`（下記）で全台温存できる。

**空き専用の 2 つ目の猶予パラメータは作らない。** 空きスロットの温かさは占有中の休眠より
**価値が低い**（占有中は mount も親和性も保っている）ので、空きだけ長くするのは逆向きになる。

### 64.31.4 ついでに——`Ec2SlotSleepSec=0` も嘘をついていた

CFN の説明文は "Set 0 to keep slots running forever" と書いていたが、実装は
`idle < slotSleepAfter` の裸の比較なので、**0 では常に真にならない＝最初のスイープで即停止**、
つまり**正反対**だった。`hibernateAfter` の 0=OFF と揃え、**0 = 眠らせない**に直した。
§64.26 の `AF_WS_IDLE_TIMEOUT=0` が 2h に化けた罠と同じ形——**運用者の明示的な off は
off として読む**。

### 64.31.5 運用画面

`ec2SlotView.IdleMinutes` は占有中の home からしか埋めていなかったので、**空きスロットは
何時間放置されていても 0 分**と表示されていた。`af-slot-idle-since` から埋める。
運用者がこの画面に来る理由が「タスクの無い箱が running なのはなぜか」である以上、
**その質問に 0 と答える欄は無い方がまし**だった。

### 64.31.6 掃除ループの片側が「起動中」を見ていなかった（同じ round で発見・修正）

§64.31.2 を入れたあとに気づいた**元からある粗**。`sweepFreeSlots` は生きた `af-claim` を
「使用中」に数えるのに、**`sweepVolume` は期限切れ claim を消すだけで、生きた claim を見ていない。**

`Start` は **`clearDormancy`（休眠マークを消す）が先・`upsertService`（desired 1）が最後**で、
その間に **mount の SSM 往復（10〜30 秒）**が入る。この窓にスイープが刺さると:

- `af-idle-since` は消えている ／ サービスはまだ desired 0
- ⇒ `// live workspace` の判定を通り抜け、`marked == false` なので **`markIdle` で刻み直す**

**早すぎる停止は起きない**（刻み直しは時計を*前*に戻すので、むしろ 15 分後ろへずれる＝安全側）。
問題は**マークが起動を生き延びる**ことで、次の `Stop` まで消えない:

1. **運用画面が running の WS を「N 分アイドル」と表示する**（`PoolStatus` は `af-idle-since` から
   `IdleMinutes` を出す）。
2. **`evictLongestIdle` の候補になる。** 実害が出るのは上限到達時で、`releaseSlot` が live な
   サービスを拒否 → `evictLongestIdle` は**次点の犠牲者に進まずそのままエラーを返す**
   ⇒ その Start が「プールが一杯」ではなく reclaim エラーで失敗する。

窓は 10〜30 秒／スイープ 300 秒なので、起動 1 回あたり数〜10% は踏む。

**直し方は 1 行**——`sweepVolume` にも同じ規則を置く（`if rt.claimLive(vol) { return }`）。置き場所に
2 つ条件がある:

- ⚠️ **退避（`att == nil`）の分岐より後**。claim が捕獲を途中で止められてはいけない
  （§64.18.2.1 の「機能を切っても home を半端に取り残さない」と同じ理由）。
- ⚠️ **期限切れ claim は素通しさせる。** `claimLive` は `af-claim-at` を parse して TTL と比べる
  ので、タグが**在ること**では止まらない。ここを「タグがあれば return」と書くと、
  **死んだ launch がスロットを永久に固定する**。

> ⚠️ **教訓は「同じ掃除ループの中で規則が揃っていなかった」こと。** 空き側は最初から claim を
> 見ていて、home 側は見ていなかった。**新しく足した走査の方が正しく、既存の方が間違っていた**
> ——片方だけ直すと、こういう不揃いは次に触った人にしか見えない。
