# 64. ECS の Workspace を EC2 起動タイプ ＋ インスタンス stop/start で持つ

> 状態: 調査完了・**採用は条件付きで見送り**（2026-08-15）。決定は
> [ADR 0045](decisions/0045-ec2-persistent-workspace.md)。
> **成立性は AWS sandbox で端から端まで実測した**（deploy → 計測 → teardown を 1 セッションで閉じ、
> 残存リソース 0 を確認済み・§64.11）。
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

- ✅ **永続 EBS は本当に永続だった。** home を追加 EBS に載せると、インスタンスの stop/start でも、
  インスタンスの **terminate → 新インスタンスへの付け替え**でも中身が残る（§64.4.2）。
- ✅ **イメージキャッシュが効く。** 2 回目以降の pull は **31.8s → 0.09s**（§64.4.2）。
- ✅ **速い。** home 上の小ファイル 2,000 個作成が **0.04s**（EFS は 30.7s）。
- ✅ **安い。** 月 160h 稼働・home 45 GiB で **$28.5 vs 現状 $39.6**、完全アイドル月は
  **$7.7 vs $16.3**、snapshot へ退避すれば **$1.0**（§64.7）。
- ❌ **起動は速くならない。** stop→start→タスク RUNNING が **83.5s** で、Fargate の温ホーム
  再 Start（~105s のうち同じ地点まで ~84s）と**ほぼ同じ**。pull の 35s が消える代わりに
  インスタンス起動 ＋ ECS 再登録 ＋ 配置に 33s 払う（§64.4.3）。
- ❌ **代償が多い。** AZ 固定・**インスタンスタイプ変更を ECS が拒否する**・停止インスタンスは
  自動 deregister されない・awsvpc の ENI 残留でパブリック IP が付かなくなる、など**実測で見つけた罠が 4 件**
  （§64.5）。運用対象が 1 Workspace あたり 2 種類（サービス＋EFS AP）から 6 種類（インスタンス・
  ボリューム・スナップショット・コンテナインスタンス登録・サービス・タスク定義）に増える。

**したがって、価値は「起動の速さ」ではなく「I/O と永続と費用」にある。** そして I/O の大半は
ADR 0044 決定 3（`~` の置き場を平均ファイルサイズで分ける）が**先に、はるかに安く**取る。
EC2 化は決定 3 を実装して**なお足りないと実測で言えたとき**に着手する。判定ゲートは §64.9。

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

## 64.10 判定ゲート（いつ着手するか）

ADR 0044 決定 3（`~` の置き場の分割）を実装したうえで、**次のいずれかが実測で言えたとき**に着手する。
言えないうちは着手しない——理由は §64.2 のとおり、EC2 化の効き幅の大半を決定 3 が先に取るため。

1. **決定 3 の後でも `~/repos` の操作が遅い**。具体的には EFS 上の実リポジトリで
   `git status` / `rg` が体感を壊す水準（例: 1 操作 5 秒超）で観測される。
2. **朝の再生成（`npm ci` ＋ 初回ビルド）が 5 分を超える**ユーザーが常態化する。
3. **EFS 課金がユーザーあたり月 $10 を超える**（＝置き場の分割で減らしきれていない）。
4. **Fargate のサイズ上限（16 vCPU / 120 GiB / ephemeral 200 GiB）に当たる**要望が出る。

なお 4 だけは決定 3 では解けないので、**単独で EC2 化の理由になる**。

## 64.11 計測ハーネスと後始末

`~/af-ec2/`（本セッションの Workspace）に置いた: `setup.sh`（ECR 複製・SG・IAM・クラスタ・
Cloud Map 名前空間）／ `userdata.sh`（追加 EBS のマウントと ECS 参加）／ `launch.sh`（インスタンス起動）／
`mktd.py`・`bench-in-task.sh`（タスク定義生成と I/O 計測）／ `svc.sh`・`warmtask.sh`・`cycle.sh`（起動計測）／
`snap.sh`・`snap2.sh`（退避と復元）／ `resize.sh`（タイプ変更）／ `pricing.sh`（Pricing API）／ `teardown.sh`。

**後始末は完了している**: cluster / instance / volume / snapshot / EIP / ECR / Cloud Map 名前空間 /
ロググループ / SG / IAM ロール / インスタンスプロファイル / タスク定義 / ENI が**すべて 0 件**であることを
確認した（`ec2-single` 等の他のスタックは元から存在しない sandbox）。

計測の教訓を 2 つ:

- **背景実行にしてログをファイルに落とすこと。** 5 分を超える計測をツールのタイムアウトに
  当てると、途中経過が丸ごと失われる（1 回やり直した）。
- **失敗した AWS API 呼び出しを「遅い」と読まないこと**（[62](62-ecs-start-latency.md) §62.10.2 と同じ轍）。
  今回も「エージェントが 11 分繋がらない」の正体は API の遅さではなく、**パブリック IP の消失**だった。
