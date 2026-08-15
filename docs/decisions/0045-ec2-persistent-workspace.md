# 0045. EC2 起動タイプ ＋ インスタンス stop で「速くて永続な home」は組めるが、今は採らない

- 状態: **調査完了・条件付き見送り**（2026-08-15。設計と実測は [docs/64](../64-ec2-persistent-workspace.md)）。
  成立性は AWS sandbox で**端から端まで実測**した（新規作成 → 温起動 → 停止 → 終了 → ボリューム付け替え →
  snapshot 退避 → 復元 → サイズ変更）。着手の判定ゲートは決定 5。
- 関連: [64-ec2-persistent-workspace.md](../64-ec2-persistent-workspace.md) /
  [0044-workspace-sizing.md](0044-workspace-sizing.md) 決定 4（本 ADR を起こした宿題） /
  [63-workspace-sizing.md](../63-workspace-sizing.md) §63.4（EFS の I/O 実測） /
  [62-ecs-start-latency.md](../62-ecs-start-latency.md) §62.5（(d) EC2 起動タイプの却下・本 ADR で改訂） /
  [0012-go-refactor.md](0012-go-refactor.md)（アダプタは CP に状態を持たない）

## 背景

ADR 0044 決定 4 で「Fargate に『速くて永続』は存在しない。本当に必要なときの答えは EC2 起動タイプ ＋
インスタンス stop で、別案件として検討する」と書いた。その検討である。
併せて docs/62 の「(d) EC2 起動タイプ＝却下」が今も有効かを判定する。

## 決定 1 — 技術的には成立する（実測で確認した）

ECS の枠内で「1 ユーザー = 1 インスタンス ＋ 永続 EBS、使わないときは stop」は**組める**。

- コンテナインスタンスは **user-data だけ**でクラスタに参加し、`ECS_INSTANCE_ATTRIBUTES` で名乗った
  属性がそのまま登録される。サービスは `launchType=EC2` ＋ 配置制約
  `memberOf attribute:af-membership == <id>` でそのインスタンスにだけ載る。
  **ASG もキャパシティプロバイダも要らない**（`PlacementConstraint` は Fargate では使えないので、
  これは EC2 起動タイプ固有の道具）。
- **home を追加 EBS に置くと本当に残る**——インスタンスの stop/start でも、**terminate → 新インスタンスへの
  `AttachVolume`** でも中身が保たれた（`DeleteOnTermination=false`）。
- **イメージキャッシュが効く**（pull 31.8s → **0.09s**）。
- **Service Connect は EC2 起動タイプでも動く**（Fargate のクライアント＝ CP 役から疎通確認）。
  `Endpoint()` の契約は変えなくてよい。
- **CP はステートレスのままでよい**（ADR 0012）。インスタンス・ボリューム・スナップショットは
  すべてタグで、配置は ECS 属性で引ける。

## 決定 2 — それでも今は採らない（Fargate ＋ ADR 0044 決定 3 を続ける）

理由は 3 つで、いずれも実測に基づく。

1. **起動は速くならない。** stop→start→タスク RUNNING が **83.5s** で、Fargate の温ホーム再 Start
   （同じ地点まで **~84s**）と差が無い。**pull で浮く 35 秒を、インスタンス起動 19s ＋ ECS 再登録 1s ＋
   配置 13s で使い切る。** 起動時間を理由に EC2 化してはならない。
2. **効き幅の大半を決定 3 が先に取る。** EC2 化の本命は I/O だが、`node_modules` 等をローカルへ逃がす
   ADR 0044 決定 3 が `npm ci` 105s → 11s を**タスク定義 1 行と entrypoint の分岐だけで**取る。
   EC2 化の残りの取り分は「永続領域（`~/repos`・`~/.local`）も速い」「朝の再生成が要らない」に絞られる。
3. **運用対象が 2 種類から 6 種類に増える。** 1 Workspace あたり「サービス ＋ EFS アクセスポイント」から
   「インスタンス・EBS ボリューム・スナップショット・コンテナインスタンス登録・サービス・タスク定義」へ。
   ECS 構成は**実運用実績ゼロ**であり、ここで面を広げる順番ではない。

**費用は見送りの理由ではない**——むしろ EC2 の方が安い（月 160h・home 45 GiB で $28.5 対 $39.6、
完全アイドル月は $7.7 対 $16.3）。ただし **EFS は使った分、EBS は確保した分**の課金なので、
損益分岐は充填率 **26.7%**（$0.096 / $0.36）である点は記録しておく。

## 決定 3 — 採る場合に必ず実装するもの（実測で見つけた罠）

「そのうち作る」ときに忘れると必ず踏むので、ここに固定する。

1. **サイズ変更は 3 手セット。** 停止中に `ModifyInstanceAttribute` でタイプを変えると、ECS エージェントが
   `Container instance type changes are not supported` で **terminal exit** し、クラスタに戻らない。
   `DeregisterContainerInstance --force` ＋ **`/var/lib/ecs/data/*` の削除** ＋ `systemctl restart ecs`
   を踏むこと（実測 46s で復帰し、属性も維持される）。
2. **削除時は `DeregisterContainerInstance` を明示的に呼ぶ。** SDK のドキュメントに
   「停止済み／エージェント切断のインスタンスは terminate しても自動 deregister されない」と明記がある。
   放置するとゴースト登録が積もる。
3. **パブリック IP に依存しない。** awsvpc のタスク ENI はエージェント切断中インスタンスに残り、
   複数 ENI 構成になると **start 時に自動割当パブリック IPv4 が付かない**。実測ではそれで egress を失い、
   エージェントが 11 分再接続できなかった。本番はプライベートサブネット ＋ NAT なので露出しないが、
   前提として明記する。
4. **AZ が固定される。** EBS は AZ を跨げず、停止インスタンスは AZ を保持するので、その AZ で容量が
   取れないと start が失敗する。逃げ道は snapshot 経由で別 AZ に作り直すことだけ（45 GiB で 30〜40 分）。
5. **`State()` はインスタンス状態との組で写す。** サービスの desired/running だけでは
   「インスタンス起動中」を `starting` と言えない。
6. **資格情報だけは EFS に残すハイブリッドにする。** 単一 AZ・単一ボリュームの EBS が失われたとき、
   ログイン情報まで一緒に失わないため（`homeKeep` の 7 つは 100 MiB 未満）。

## 決定 4 — 長期未使用ユーザーの退避は「standard 階層の snapshot」まで。archive は使わない

- 退避（タスク停止 → terminate → `CreateSnapshot` → ボリューム削除）と復帰（`CreateVolume` →
  新インスタンス → `AttachVolume`）はどちらも実測で通った。**ユーザーを待たせるのは復帰の 122 秒だけ**で、
  snapshot 作成（実データ 5.45 GB で 267s ＝ 約 20 MB/s。45 GiB なら 30〜40 分）は非同期でよい。
- 費用は **実使用 20 GiB / 確保 50 GiB のユーザーで $4.80 → $1.00**（snapshot は使用ブロックのみ課金）。
- **復元直後は 2.3 倍遅い**（4 GiB 読み出しが 57.4 MB/s、通常は 135 MB/s）。触った分だけなので
  「初日が少し重い」程度に散る。`VolumeInitializationRate`（100〜300 MiB/s・有償）で潰せるが、
  **Fast Snapshot Restore は $0.90/時＝月 $648** なので per-user には論外。
- **アーカイブ階層（$0.0125/GB-月）は採らない。** `RestoreSnapshotTier` の復元に **24〜72 時間**かかり、
  最低 90 日課金も付く。使うとしても「休眠アカウント」という別状態として Console に明示する機能であって、
  自動アイドル停止の延長線上には置けない。
- 退避の判定は**サイズではなく最終利用日**で行う（課金差が効くのは「長く使っていない人」だけ）。

## 決定 5 — 着手の判定ゲート

ADR 0044 決定 3 の実装後、**次のいずれかが実測で言えたとき**に EC2 案へ進む。言えないうちは着手しない。

1. 決定 3 の後でも `~/repos` 上の `git status` / `rg` が 1 操作 5 秒超で観測される
2. 朝の再生成（`npm ci` ＋ 初回ビルド）が 5 分超のユーザーが常態化する
3. EFS 課金がユーザーあたり月 $10 を超える
4. **Fargate のサイズ上限（16 vCPU / 120 GiB / ephemeral 200 GiB）に当たる要望が出る**
   —— これだけは決定 3 では解けないので、単独で理由になる

## 決定 6 — ECS Managed Instances は選択肢に入らない

`aws-sdk-go-v2/service/ecs@v1.87.0` の型定義で確認した: `InfrastructureOptimization.ScaleInAfter` は
アイドルなインスタンスを**終了**し、`AutoRepairConfiguration` は不調なインスタンスを**置き換え**、
`ManagedInstancesStorageConfiguration` は**サイズしか指定できない**（既存ボリュームを指す項目が無い）。
**インスタンスのライフサイクルの所有者が ECS 側にあり、stop という状態が存在しない。**
Managed Instances は「Fargate の揮発性を EC2 の価格と自由度で得る」ものであって、永続の話ではない。

## 決定 7 — docs/62 の「(d) EC2 起動タイプ＝却下」を改訂する

当時の却下理由は 4 つだったが、**主たる理由が誤りだった**ので書き換える（docs/62 §62.5 に追記済み）。

| 当時の理由 | 判定 |
|---|---|
| **scale-to-zero の経済性が消える** | ❌ **誤り。** 停止インスタンスは課金されず EBS だけになる。実測費用でも EC2 の方が安い |
| 容量プロバイダ / ASG / ドレインが増える | ❌ **不要だった。** `launchType=EC2` は素の登録済みインスタンスで動く（実測） |
| AMI 更新が増える | ✅ **有効。** これは残る（イメージキャッシュを持つ長寿命インスタンスの patch 経路が要る） |
| 「per-workspace は CP がステートレス」を壊す | ❌ タグと ECS 属性で引けるので**壊れない**。ただし扱う資源の種類は 2 → 6 に増える |
| 「1 台の VM 形は `ec2-single` として既にある」 | ❌ **別物。** `ec2-single` は全部入り 1 台で per-user 分離が無い |

**却下そのものは（起動レイテンシの文脈では）結論として維持する**——実測で 83.5s 対 ~84s と差が無く、
docs/62 の目的に対しては効かないため。ただし**理由は「scale-to-zero が消えるから」ではなく
「起動が速くならないから」**である。EC2 起動タイプの検討軸は起動レイテンシではなく **I/O と永続**であり、
そちらは本 ADR の決定 2 と決定 5 が扱う。

## 影響

- 実装は**無し**（本 ADR は見送りの記録と、着手時の設計・罠・ゲートの固定）
- [docs/62](../62-ecs-start-latency.md) §62.5 — (d) の却下理由を改訂（実施済み）
- [docs/63](../63-workspace-sizing.md) §63.5.5 — 「別セッションで検討する」の結論へのリンク（実施済み）
- 着手する場合の対象: `control-plane/runtime_ecs.go`（EC2 / EBS クライアントの追加・`State()` の写像・
  サイズ変更手順）／ `deploy/aws/ecs/cfn/`（インスタンスプロファイル・起動テンプレート）／
  `workspace/entrypoint.sh` と `workspace/workspace-notes.md`（永続モデルの記述）
