---
audience: "ecs-ec2 の配備で、スロットに使うインスタンスの種類（クラス）を決める配備管理者"
source_of_truth: "`deploy/aws/ecs/cfn/30-ingress.yaml` の `Ec2SlotTypes`・`Ec2SlotAmiArm64`・`Ec2DefaultSlotClass` と `deploy/aws/ecs/cfn/PARAMETERS.md`、Control Plane の `parseSlotClasses`（`control-plane/internal/runtime/runtime_ecs_ec2_pool.go`）"
---

# スロットのマシンの種類を選ぶ

[English](slot-types.md) | 日本語

`ecs-ec2` の配備で、スロットに使うインスタンスの種類を複数用意し、テナント管理者にテナントごと・メンバーごとに選ばせます。
速さより費用を優先する種類や、Graviton（arm64）の種類を足せます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `Ec2SlotTypes` に名前つきのクラスを書く | テナント管理者の画面に「**マシンの種類**」が出る |
| arm64 のクラスを足す前に、イメージと AMI を用意する | Graviton のスロットが起動できる |
| `Ec2DefaultSlotClass` を決める | 何も選んでいないメンバーが、そのクラスに載る |
| テナント管理者が種類を選ぶ | そのメンバーの次の起動から、そのクラスのインスタンスに載る |

クラスを足しただけでは、どのメンバーもそのクラスへ移りません。

## クラスを書く

`Ec2SlotTypes` には 2 つの書きかたがあります。古いほうもそのまま使えます。

```
instanceType:memoryMiB[:vcpu],…                one unnamed class, x86_64
id|label|arch|<ladder>[;id|label|arch|<ladder>] one class per entry
```

| 欄 | 中身 |
|---|---|
| `id` | クラスの ID。`Ec2DefaultSlotClass` で指す |
| `label` | テナント管理者の画面に出る名前 |
| `arch` | `x86_64` か `arm64`。インスタンスの名前からは推測せず、書いたとおりに扱う。それ以外の値のクラスは捨てる |
| `<ladder>` | `instanceType:memoryMiB[:vcpu]` を小さい順にカンマで並べた段 |

- ワークスペースは、メモリの要求が収まるいちばん小さい段に載り、インスタンスをまるごと使います。クラスが段の並びを選び、メモリがその中の段を選ぶので、「8 GB」の意味はクラスを変えても同じです
- vCPU の欄は省略できます。表示専用で、Console がメモリの値に対応するインスタンスを示すときに使います
- クラスの区切りは `;` か改行です。使える段のないクラスは捨てます

既定では、Intel の 2 クラスが入っています。

```
standard|Standard (Intel)|x86_64|m7i.large:8192:2,…;saver|Lower cost (Intel, previous gen)|x86_64|m6i.large:8192:2,…
```

## 種類を比べる

ap-northeast-1 で m7i と比べて測った値です。価格は Pricing API から取り、速さは各ファミリーでこのリポジトリをビルドした時間で比べています。

| ファミリー | 1 時間あたりの価格 | ビルドの時間 | **ビルド 1 回あたりの費用** |
|---|---|---|---|
| m6i | −4.8% | +13% | +7.3% |
| **m8g**（Graviton4） | **−11.0%** | **−29%** | **−37.1%** |
| m7g（Graviton3） | −19.0% | −10% | −27.4% |
| m6g（Graviton2） | **−24.0%** | **+32%** | ±0 |

- **1 時間あたりがいちばん安いインスタンスが、いちばん安いわけではありません。** m6g は 1 時間あたり 24% 安く、32% 遅いので、CPU を使い続ける作業では得になりません。m8g は m7i より安くて速いので、m7g を選ぶ理由はありません
- **どの列が効くかはメンバーしだいです。** スロットは動いている時間で課金され、その大半はワークスペースが読む・考える・モデルを待つ時間です。読むのが中心のメンバーなら m6g で実際に 24% 安くなります。一日中ビルドするメンバーは、安くなった分を待ち時間で払うことになります。そのためメンバーごとの設定になっています
- **クラスの `label` には、値引きの率ではなくトレードオフを書きます**（「Cheapest — older, slower」など）。「24% 安い」だけだと、代償なしに安くなると誤解されます

**m6i は何も要りません。** m7i と同じアーキテクチャなので、同じワークスペースのイメージ・同じ AMI・同じ golden snapshot が使え、メンバーが 2 つのあいだを移ってもホームはそのまま使えます。大きく安くなるのは Graviton で、下の準備もすべて Graviton のためのものです。

## Graviton（arm64）のクラスを足す

arm64 のクラスを書く前に、この順で準備します。

1. **ワークスペースのイメージに arm64 のマニフェストを持たせます。** ないと、スロットは起動しても、タスクはその上で動くイメージを pull できません。`crane manifest <image>` で確かめます。`manifest.v2+json` が 1 つだけなら、アーキテクチャは 1 つしかありません。必要なのはマニフェストリスト（OCI インデックス）です
2. **arm64 の AMI を渡します。** 起動テンプレートが固定する AMI は 1 つで、Graviton は x86_64 の ECS 最適化 AMI で起動できません。Control Plane は arm64 の AMI を `RunInstances` の ImageId の上書きとして渡します
   - `40-ec2-pool` の `SlotAmiIdArm64` に、次のコマンドの結果を入れて更新します。Control Plane のロールが起動してよい AMI も、このパラメータで決まります

     ```bash
     aws ssm get-parameter --name /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id --query Parameter.Value --output text
     ```

   - `30-ingress` の `Ec2SlotAmiArm64` に、`40-ec2-pool` の出力 `SlotAmiIdArm64` を渡します
3. **`Ec2SlotTypes` に arm64 のクラスを足します。** たとえば次のように書きます

   ```
   …;arm|Faster and cheaper (Arm)|arm64|m8g.large:8192:2,m8g.xlarge:16384:4,m8g.2xlarge:32768:8
   ```

4. **2 つ目の golden のビルドを待ちます。** golden はバイナリの詰まったホームなので、アーキテクチャごとに 1 つです。arm64 の golden ができるまで、arm64 のクラスの新しいメンバーは空のホームから始まります（遅いだけで、壊れはしません）。golden は「**スロット**」の画面にアーキテクチャごとに表示されます

- arm64 のクラスがあって `Ec2SlotAmiArm64` が空だと、Control Plane は起動を拒みます。起動できないクラスを画面に出さないためです
- `Ec2SlotAmiArm64` は初期値と予備の値にすぎません。Control Plane は同じ SSM パラメータを自分で読んでスロットを起動するので、パッチのためにデプロイし直す必要はありません

## 既定のクラスを決める

`Ec2DefaultSlotClass` は、自分にもテナントにも指定がないメンバーが載るクラスの `id` です。空のときは `Ec2SlotTypes` の最初のクラスになります。

- **動いている配備でこれを変えると、自分で選んでいないメンバー全員が移ります。** アーキテクチャが変わるなら、その全員のホームが、次の起動でアーキテクチャに依存するツールを入れ直します
- 動いているスタックは、パラメータを渡さないかぎり今の `Ec2SlotTypes` を持ち続けます。Control Plane をアップグレードしただけで、メンバーに選択肢が表示されることはありません

## メンバーをアーキテクチャの違うクラスに移す

テナント管理者は、テナントの「**既定のマシン種別**」と、メンバー詳細の「**マシンの種類**」で選びます（[ワークスペースの大きさを決める](../../admin/resources/workspace-size.ja.md#マシンの種類を選ぶ)）。

アーキテクチャをまたぐ移動は安全ですが、入れ直しの手間がかかります。

| 残るもの | 入れ直すもの |
|---|---|
| ホームそのもの | `~/.local` の CLI・nvm の node・Chromium など、アーキテクチャに依存するもの。次の起動で自動で入れ直す |
| `~/repos` の中身 | `node_modules`・`target`・`.venv` など、メンバーがビルドしたもの。メンバーが自分で入れ直す。見つかったものは [af-arch-repair](../../ref/cli/af-arch-repair.ja.md) が通知で名前を出す |

保存する前に、Console が警告を出します。

## 関連

- [EC2 スロットプールを使い始める](slot-pool.ja.md)
- [ワークスペースの大きさを決める](../../admin/resources/workspace-size.ja.md)：テナント管理者がマシンの種類を選ぶ
- [スロットを入れ替える・止める](slot-maintenance.ja.md)：AMI が変わったスロットの入れ替え
- [費用を抑える](cost.ja.md)
