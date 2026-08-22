# 70. スロットのインスタンス種別（アーキ / 世代）をテナント・ユーザー毎に選ぶ

> 状態: **P0 / P2 / P3 / P4 完了**（2026-08-22）。残るは **P1（arm64 イメージ・仕組みだけ実装済みで
> 一度も焼いていない）** と **P5（実機）**。arm64 クラスは P1 が終わるまで既定に載せない（§70.14-2）。
> ADR は P1 着手時に `0051` として起票する。
> 本書の価格・AMI・ECS API の記述は **ap-northeast-1 の実 AWS（sandbox 722507597273）で
> 2026-08-22 に取得した一次情報**。残存リソースは 0（プローブしたタスク定義 2 本は
> deregister → delete 済み）。
> 関連: [64-ec2-persistent-workspace.md](64-ec2-persistent-workspace.md)（スロットプールの本体・
> [ADR 0045](decisions/0045-ec2-persistent-workspace.md) 決定 8 / 決定 9 / 決定 21）/
> [63-workspace-sizing.md](63-workspace-sizing.md)（サイズ 3 軸・[ADR 0044](decisions/0044-workspace-sizing.md)）/
> [67-member-cloud-cost.md](67-member-cloud-cost.md)（`af-slot-size` はコスト配分タグとして既に Active）/
> [35-packaging.md](35-packaging.md) §35.3.1（**amd64 先行**という現在のパッケージング方針）
> 対象: `control-plane/runtime_ecs_ec2.go`（`slotTypeFor` / `freeSlots` / `launchSlot` / `buildTaskDef`）/
> `control-plane/workspace_sizing.go`・`workspace_lifecycle.go`・`limits.go`・`store*.go`・`migrations/` /
> `control-plane/golden_bake.go` / `deploy/aws/ecs/cfn/40-ec2-pool.yaml`・`30-ingress.yaml` /
> `deploy/compose/release.sh`・`.github/workflows/publish-dist.yml` /
> `console/src/features/settings/tenantMembers.tsx`・`TenantDialog.tsx` / `workspace/entrypoint.sh`

## 70.1 問題

`ecs-ec2` のスロットは **m7i 固定**である。固定しているのは 1 か所、
`newECSEC2Factory` の既定値と CFN パラメータだけ:

```go
// control-plane/runtime_ecs_ec2.go:381
slotSizes: parseSlotSizes(envOr("AF_ECS_EC2_SLOT_TYPES",
             "m7i.large:8192,m7i.xlarge:16384,m7i.2xlarge:32768")),
```

```yaml
# deploy/aws/ecs/cfn/30-ingress.yaml:83
Ec2SlotTypes:
  Default: "m7i.large:8192:2,m7i.xlarge:16384:4,m7i.2xlarge:32768:8"
```

やりたいことは 2 つあり、**難易度が桁で違う**ので最初に分けておく。

1. **もっと安い箱を使えるようにする**（m7g / m6g / m6i）。うち m7g・m6g は **arm64**。
2. **その選択をテナント毎・ユーザー毎に、テナント管理者が行えるようにする。**

1 だけなら `AF_ECS_EC2_SLOT_TYPES` を書き換えるだけで**今日でも通る**——ただし
arm を選んだ瞬間に (a) arm64 の workspace イメージ、(b) arm64 の ECS 最適化 AMI を積んだ
launch template、(c) **既存ユーザーの home に入っている x86 バイナリ**、の 3 つが問題になる。
2 は、それに加えて **1 つのプールに複数アーキが同居する**ことを意味する。

本書の主張は次の 1 行である。

> **難所は「選べるようにする」ことではなく、`~` が永続することである。**
> ecs-ec2 の home は EBS で stop/start をまたいで残り（それが [64](64-ec2-persistent-workspace.md) の目的）、
> その `~` の中には **アーキ依存のバイナリが入っている**。アーキの切り替えは設定変更ではなく
> **移行**であり、それを認めない設計にするか、自動修復を書くかを先に決める必要がある。

## 70.2 いま何が m7i を決めているか（実装の読み取り）

| 場所 | 何をしている | アーキが増えると |
|---|---|---|
| `parseSlotSizes` / `ec2Slot` | `type:memMiB[:vcpu]` の**単一の梯子**を昇順に持つ | 梯子が複数になる |
| `slotTypeFor(memBytes)` (`:461`) | メモリ要求を載せられる**最小の段**を返す | 「どの梯子の」最小段かが要る |
| `ecsEC2Factory.New` (`:437`) | `instanceType` を**ランタイム生成時に確定**しキャッシュ | 変更時のキャッシュ破棄は既存の `evictMembershipCache` に乗る |
| `freeSlots` (`:1438`) | `instance-type` フィルタで**同一タイプの空きスロットだけ**拾う | **そのままで正しく分離される**（アーキ跨ぎの誤配置は起きない） |
| `launchSlot` (`:1628`) | 単一の launch template（`$Latest`）＋ `InstanceType` 上書き | **LT の AMI は x86 なので arm では起動しない** |
| `buildTaskDef` (`:2190`) | `RuntimePlatform` を**指定していない** | 指定は任意（§70.8 で実測）だが明示すべき |
| `goldenSnapshot` (`:2719`) | `(af-pool, af-role=golden, af-image)` で 1 本だけ引く | **アーキの次元が無い → x86 の home を arm に配る** |
| `SizingProfile` (`workspace_sizing.go:107`) | 梯子を Console へ返す（決定 21） | クラスの次元を足す |
| `af-slot-size` タグ (`:1645`) | インスタンスタイプを打つ。**コスト配分タグとして Active**（[ADR 0048](decisions/0048-member-cloud-cost.md) 決定 1） | **何も足さずに費用差が請求に出る** |

読み取れる良い知らせが 2 つある。**空きスロット探索は既にインスタンスタイプで分離されている**ので、
プールに arm と x86 が混ざっても取り違えは起きない。そして **費用の可視化は既に通っている**——
`af-slot-size` が Active な以上、m7g に移った人の請求は [67](67-member-cloud-cost.md) の
メンバー別費用にそのまま安く出る。「効果を測る画面」を作る必要が無い。

## 70.3 価格（Pricing API 実測・ap-northeast-1・2026-08-22）

`aws pricing get-products`（Linux / Shared / preInstalledSw=NA / capacitystatus=Used）。

| インスタンス | vCPU | メモリ | $/時 | $/vCPU-時 | m7i 比 | 730h/月 | 160h/月 ※ |
|---|---|---|---|---|---|---|---|
| **m7i.large**（現行） | 2 | 8 GiB | 0.1302 | 0.0651 | — | $95.05 | $20.83 |
| m6i.large | 2 | 8 GiB | 0.1240 | 0.0620 | **−4.8%** | $90.52 | $19.84 |
| **m8g.large**（Graviton4） | 2 | 8 GiB | 0.11594 | 0.05797 | **−11.0%** | $84.64 | $18.55 |
| **m7g.large**（Graviton3） | 2 | 8 GiB | 0.1054 | 0.0527 | **−19.0%** | $76.94 | $16.86 |
| **m6g.large**（Graviton2） | 2 | 8 GiB | 0.0990 | 0.0495 | **−24.0%** | $72.27 | $15.84 |

※ 実働 8h × 20 日。ecs-ec2 はアイドル 15 分でスロットを stop するので（`AF_ECS_EC2_SLOT_SLEEP_SEC`）、
実際の課金はこちらに近い。停止中の root EBS 100 GiB（約 $9.6/月）はアーキで変わらない。

xlarge / 2xlarge も**完全に線形**（$/vCPU-時 が large と同一）で、[64](64-ec2-persistent-workspace.md) が
m7i で確かめた線形性は 4 ファミリ全てで成り立つ。したがって**梯子の段が上がっても割引率は変わらない**——
クラスの差は全段に等しく効く。

**取得可能性**: m7g / m6g / m8g / m7i いずれも ap-northeast-1a / 1c / 1d の 3 AZ で提供
（`describe-instance-type-offerings` 実測）。[ADR 0045](decisions/0045-ec2-persistent-workspace.md)
決定 15/16 の AZ 分散・容量不足時の他 AZ 退避は**そのまま成り立つ**。

### 70.3.1 性能（実 EC2 で実測・ap-northeast-1・2026-08-22）

`deploy/aws/ecs/harness/bench-instance-classes.sh`。5 ファミリの **large（2 vCPU / 8 GiB）**
に 1 台ずつ、スロットが実際に起動するのと同じ ECS 最適化 AMI で立て、**このリポジトリ自身**を
ビルドさせた（node 22.23.2 / go 1.26.7 をイメージと同じ上流 tarball から導入・`ref=temp/sg4h6cj`）。
5 台とも成果物が一致していること（`node_modules` 20,904 ファイル・`console/dist` 13,459,367 バイト）を
確認済み。残存リソース 0。

| | CPU | npm ci ※ | Console build | go build | go test | 合計 | 時間比 |
|---|---|---|---|---|---|---|---|
| **m7i.large** | Xeon Platinum 8488C | 5 | 19 | 113 | 42 | **174** | 1.00 |
| m6i.large | Xeon Platinum 8375C | 6 | 22 | 127 | 47 | **196** | 1.13 |
| **m8g.large** | AWS Graviton4 | 4 | 16 | **70** | 37 | **123** | **0.71** |
| m7g.large | AWS Graviton3 | 6 | 21 | 89 | 46 | **156** | 0.90 |
| m6g.large | Neoverse-N1（Graviton2） | 9 | 33 | 136 | 61 | **230** | 1.32 |

※ `npm ci` は 4〜9 秒で、計測の分解能（1 秒）に対して小さすぎる。**結論には使わない**
（合計は残り 3 項目）。ちなみに 20,904 ファイルが 5 秒で入るのは異常値ではなく、
[63](63-workspace-sizing.md) §63.4.4 がローカルディスクについて外挿した「約 11s」と同じ桁である。

**順位は 4 項目すべてで一致した**（m8g < m7g < m7i < m6i < m6g）。1 回しか回していないが、
4 つの独立した作業で順序が入れ替わらないこと自体が、差がノイズでないことの証拠になる。

### 70.3.2 実測が起票時の見立てを 2 つ壊した

**① 「一番安い箱が一番安い」は成り立たない。** 同じ仕事を終えるまでの費用（$/時 × 所要時間）で
並べ直すと、順番がひっくり返る。

| | $/時 比 | 時間 比 | **仕事あたりの費用 比** |
|---|---|---|---|
| m7i.large | 1.000 | 1.000 | **1.000** |
| m6i.large | 0.952 | 1.126 | **1.073**（+7.3%） |
| **m8g.large** | 0.890 | 0.707 | **0.629**（−37.1%） |
| m7g.large | 0.810 | 0.897 | **0.726**（−27.4%） |
| m6g.large | **0.760** | 1.322 | **1.005**（±0） |

**m6g は「時間あたり 24% 安い」が「仕事あたりでは m7i と同じ」で、しかも 32% 遅い。**
計算し続ける使い方では、m6g を選ぶ理由は 1 つも無い。

**② m8g は m7i の上位互換である。** **時間あたり 11% 安く、かつ 29% 速い。** 起票時に
「m7i より安く、コア当たりは m7i 以上」と一般論で書いた見立ては、実測では控えめだった
（go build は **38% 速い**）。arm64 のクラスを 1 つだけ出すなら **m7g ではなく m8g** である。

### 70.3.3 それでも m6g と m6i を捨てない —— 開発 Workspace は計算し続けない

§70.3.2 の表は **「ビルドを回し続けたときの費用」** であって、**請求書ではない**。
スロットの課金は **箱が running である壁時計時間**に比例し（アイドル 15 分で stop・
`AF_ECS_EC2_SLOT_SLEEP_SEC`）、開発 Workspace の時間の大半は**計算していない**——
読む、考える、エージェントがモデルの応答を待つ。その時間には $/時 がそのまま効く。

つまり **どちらの表が正しいかは人による**。

| その人の時間の使い方 | 効くのは | 得なのは |
|---|---|---|
| ビルド・テストを回し続ける | 仕事あたりの費用 | **m8g**（−37%）。m6g は損 |
| 大半はアイドル（読む・考える・待つ） | $/時 | **m6g**（−24%）。次に m7g（−19%） |
| その中間（普通のメンバー） | 両方 | m8g が無難（どちらの軸でも m7i に勝つ） |

**これが per-member 設定である理由そのものである。** どちらの使い方かは運用者にもテナント
管理者にも一律には決められないし、時期によっても変わる（学習用のテナント、CI を回さない
レビュー専用のメンバー、繁忙期を過ぎた部署）。**割引率が小さい／仕事あたりで損だから選択肢から
外す、という判断はこの機能そのものの否定になる。** m6i も m6g も選択肢として載せる。

⚠️ ただし**画面に「安い」とだけ書かない**。運用者が付ける表示名に速度の含意を入れる
（「最安（旧世代・体感は遅い）」）か、ガイドで §70.3.3 の 2 行を説明する。
「24% 安い」とだけ読んで計算の重い人が m6g を選ぶと、**費用は下がらず時間だけ失う**。

### 70.3.4 m6i の位置づけ（起票時の判断を訂正）

起票時、m6i は「−4.8% しか下がらないので単独で導入する理由は無い」と書いた。**これは
割引率だけを見た誤りである。** m6i は**この表で唯一、前提条件を 1 つも持たない**選択肢である。

| | m6i | m8g / m7g / m6g |
|---|---|---|
| arm64 の workspace イメージ（§70.9） | **不要** | 必須（P1） |
| arm64 の ECS 最適化 AMI（§70.8） | **不要** | 必須 |
| アーキ毎の golden（§70.6） | **不要**（x86_64 の golden をそのまま使う） | もう 1 本焼く |
| 既存 home の入れ直し（§70.5） | **起きない**（同一アーキなので `~` はそのまま動く） | 次回起動で数分 |
| arm64 実機での CLI 動作確認（§70.13） | **不要** | 未（9 種類） |

つまり **m6i は「今日そのまま出せる唯一のクラス」**で、−4.8% は「リスクゼロで得られる
4.8%」である。だから 30-ingress の既定に `saver`（m6i）として載せた。
⚠️ ただし**既定のクラスにはしない**——既定の変更は「選んでいない全員を動かす」ことであり、
それは別の判断である（§70.14-2）。

⚠️ **未計測のまま残ったもの**: エージェント CLI の起動時間と `chromium --headless` の
スクリーンショット。前者は arm64 実機での動作確認（§70.13）と一緒に、後者は arm64 の
workspace イメージができてから測る——どちらも**素の AL2023 では意味のある形で測れない**
（実体がイメージの中にある）。

## 70.4 設計案 — 「スロットクラス」（推奨）

### 70.4.1 なぜインスタンスタイプを直接持たないか

`user_limit` に `instance_type TEXT` を足して "m7g.xlarge" を入れる案は、3 つの理由で採らない。

1. **サイズ 3 軸の runtime 中立性**（[ADR 0044](decisions/0044-workspace-sizing.md) 決定 1）を壊す。
   `user_limit` は docker / native / Fargate / ecs-ec2 で同じ意味を持つ数値の置き場で、
   EC2 の語彙が入ると他ランタイムで意味不明の列になる。
2. **メモリ軸と二重管理になる。** 現在は「メモリ要求 → 載る箱」の一方向で、`slotTypeFor` が
   唯一の決定者。タイプを直接持つと「8 GB と書いたのに 2xlarge」が表現でき、
   決定 21 が作った「Console はどの箱に載るか**を言う**」という説明が破綻する。
3. **検証できない。** 運用者が梯子に載せていないタイプを管理者が書けてしまい、
   起動時に `RunInstances` が落ちるまで誰も気づかない。

### 70.4.2 決めの形 — 運用者が**名前付きの梯子**を宣言し、管理者はその名前を選ぶ

```
AF_ECS_EC2_SLOT_TYPES=
  standard|標準（Intel）|x86_64|m7i.large:8192:2,m7i.xlarge:16384:4,m7i.2xlarge:32768:8
  arm|省コスト（Arm）|arm64|m7g.large:8192:2,m7g.xlarge:16384:4,m7g.2xlarge:32768:8
  econ|最安（Arm・旧世代）|arm64|m6g.large:8192:2,m6g.xlarge:16384:4,m6g.2xlarge:32768:8
```

1 クラス = `id|表示名|アーキ|梯子`。区切りは改行または `;`。

- **後方互換が要件。** `|` を含まない文字列は**今までどおり単一の梯子**として読み、
  `id=standard` / `arch=x86_64` の 1 クラスに畳む。既にデプロイ済みの 30-ingress スタックは
  パラメータを触らずに新 CP へ上がれる（[ADR 0045](decisions/0045-ec2-persistent-workspace.md) は
  「梯子を書く運用者は数字を知っている」を前提に置いた——同じ前提でアーキも運用者が書く）。
- **アーキは宣言させる。導出しない。** `m7g` の末尾 `g` から arm を推定する案は
  `m7gd` / `g4dn` / `x2gd` で崩れる文字列契約で、この repo が何度も踏んだ型
  （`report-arm-pitfalls` の「TUI 文字列契約は毎版壊れる」と同じ）。
- **起動時に検証する。** arm64 のクラスがあるのに `AF_ECS_EC2_AMI_ARM64`（§70.8）が
  空なら **CP は起動を拒否する**。既存の `AF_ECS_EC2_LAUNCH_TEMPLATE is required` と同じ作法。
  黙って画面に出て Start で落ちるクラスを作らない。

### 70.4.3 保存と解決

| 層 | 置き場 | 誰が書く | 空の意味 |
|---|---|---|---|
| デプロイ既定 | `AF_ECS_EC2_DEFAULT_SLOT_CLASS`（既定＝最初のクラス） | 運用者 | — |
| テナント許可 | `tenant.limits.allowed_slot_classes: []string` | **super_admin** | 制限なし（全クラス） |
| テナント既定 | `tenant.limits.slot_class: string` | tenant_admin | デプロイ既定 |
| ユーザー | `user_limit.slot_class TEXT`（migration `0047`） | tenant_admin | テナント既定 |

解決は `resolveWorkspaceSize` の隣に `resolveSlotClass(ctx, ws) (id string, note string)` を置き、
**メモリ・CPU・ディスクと同じクランプの型**にする（[ADR 0044](decisions/0044-workspace-sizing.md)）:

```
user_limit.slot_class → 空なら tenant.limits.slot_class → 空ならデプロイ既定
  → allowed_slot_classes に無ければ テナント既定へ落とす（note を返す）
  → デプロイの梯子に無ければ デプロイ既定へ落とす（note を返す・運用者がクラスを消した場合）
```

⚠️ **クランプで黙って落とさない。** `memClampNote` と同じく、落ちた事実は API の応答と
Console の表示に出す。「arm にしたはずが標準のままだった」は請求を見るまで誰も気づかない。

`Workspace` に `SlotClass string` を足し、`ecsEC2Factory.New` が
`f.pool.slotTypeFor(ws.SlotClass, ws.MemBytes)` を引く。変更の反映は既存の
`evictMembershipCache`（`tenants.go:864`・`mcp.go:1246`）と `evictTenantCache` にそのまま乗る
——**次のコンテナ起動で反映**という既存の意味論を変えない。

### 70.4.4 却下する案

- **案 0「運用者が `AF_ECS_EC2_SLOT_TYPES` を書き換えるだけ」**: 実装ゼロで安くはなるが、
  **フリート全体が同時に arm へ移る**。§70.5 の home 問題が全員に一斉に起き、
  戻す手段が CFN パラメータの巻き戻ししかない。要件（テナント毎・ユーザー毎）も満たさない。
  ただし **§70.5 の修復は案 0 でも必要**なので、そこは共通の前提条件になる。
- **案「メモリ軸のように連続値で持つ（例: 予算 $/月）」**: 段は運用者が宣言した離散集合しか無く、
  連続値にしても最終的に段へ丸める。丸めた結果を説明する UI が増えるだけ。
- **案「クラスをテナント専用にし、ユーザー毎は持たない」**: 実装は軽いが、
  「重い作業をする人だけ標準、他は省コスト」という要望（＝本件の出発点）に答えられない。

## 70.5 ★ アーキが変わると壊れるもの —— home の可搬性

ecs-ec2 の `~` は EBS ボリュームで、stop / start / スロット差し替えをまたいで**残る**。
その中には**アーキ依存の実体**が入っている。x86 の home を arm のスロットに付けると、
ファイルシステムは正常にマウントされ、**壊れているのはバイナリだけ**という状態になる。

| 置き場 | 実体 | x86 → arm で |
|---|---|---|
| `~/.local/bin/{rtk,agy,cursor-agent,kiro}` | ネイティブバイナリ（`entrypoint.sh` の boot-install が arch 別 asset を取得） | `Exec format error` |
| `~/.local/lib/node_modules/**` | npm 配布 4 CLI。ネイティブ addon を含むものがある | 起動時に落ちる |
| `~/.nvm/versions/node/*` | nvm が入れた node（`entrypoint.sh:764-777`） | `Exec format error` |
| `~/.local/share/agent-fleet/jvm/temurin-<major>-jdk-<arch>` | **ディレクトリ名にアーキが入っている**（`jdk.go:178`） | §70.5.1 |
| `~/.cache/ms-playwright`・`~/.local/share/agent-fleet/chromium/<pin>` | Chromium 実体（パスにアーキが無い） | 起動しない |
| `~/repos/*/node_modules`・`target`・`.venv` | ネイティブ addon / コンパイル済み | 動かない。**要再インストール** |
| `~/.cache/go-build`・`~/go/pkg/mod`・`~/.m2`・`~/.npm` | 内部でアーキを分ける or ソース | **無害** |

### 70.5.1 実在するバグ: JDK の解決がアーキを見ない

インストール先は `temurin-21-jdk-amd64` / `temurin-21-jdk-arm64` と**アーキ付き**なのに、
解決側は glob して先頭を取る:

```go
// workspace/agent/jdk.go:96
if m, _ := filepath.Glob(filepath.Join(dir, "temurin-"+major+"-jdk*")); len(m) > 0 {
    sort.Strings(m)
    return m[0]        // "amd64" < "arm64" → 常に amd64 を掴む
}
```

```sh
# workspace/entrypoint.sh:720
jh=$(ls -d "$d"/temurin-"$JAVA_VER"-jdk* 2>/dev/null | head -1)   # 同じ
```

x86 で JDK を入れた人が arm に移ると、**`JAVA_HOME` は x86 の JDK を指したまま**になる。
アーキを増やす前に、両方を `runtime.GOARCH` / `dpkg --print-architecture` で絞る必要がある。
（今日は 1 アーキしか存在しないので顕在化していない。）

### 70.5.2 決めるべきこと —— 3 案

| | 案 A: 切替を禁じる | 案 B: 起動時に自動修復（**推奨**） | 案 C: 移行を明示操作にする |
|---|---|---|---|
| 意味 | クラスは **home 作成時のみ**有効。以後の変更は「ホームの掃除」か作り直しが要る | `~` にアーキの刻印を置き、不一致なら**アーキ依存物だけ**捨てて boot-install をやり直す | 管理者が「アーキ移行」を実行 → home を snapshot → 新規 home を golden から作る |
| 実装量 | 最小（保存時の検証 1 か所） | 中（entrypoint の修復 + 刻印 + 既存の自己修復パターンの延長） | 大（新しいライフサイクル操作・[ADR 0045](decisions/0045-ec2-persistent-workspace.md) 決定 13 系） |
| 利用者の体感 | 「変えられません」 | 初回起動が boot-install ぶん遅い（lean で数分）+ `~/repos/*/node_modules` は各自再作成 | 作業データが消える or 別ボリュームに残る |
| 危険 | 要件を半分しか満たさない | **修復漏れ**（表に無い置き場） | データの取り扱いが重い |

**案 B を推す。** 理由は 3 つ。

1. entrypoint は**既に同種の自己修復を持っている**——壊れた `~/.local/bin/claude` の
   再インストール（`entrypoint.sh:63`）、焼き込み gh を覆う実体の除去（`:76`）。同じ場所に
   同じ形で足せる。
2. 刻印は 1 ファイルで済む（`~/.local/share/agent-fleet/arch`）。`dpkg --print-architecture` と
   不一致なら、§70.5 の表の「壊れる」行だけを `rm -rf` して boot-install に落ちる。
   **`~/repos` には絶対に触らない**（利用者の未コミット作業がある）。
3. `~/repos/*/node_modules` は直せないが、それは**直さないのが正しい**。
   Workspace Guide が既に「install は無条件で走らせる」と書いており（`workspace/workspace-notes.md`）、
   `af-scratch` の symlink で空に見える件と同じ扱いにできる。

⚠️ 案 B でも **Console 側の警告は必須**。クラス変更の保存時に
「次回起動でホーム内のアーキ依存物を入れ直します（数分）。`~/repos` 配下の
`node_modules` / `target` / `.venv` は各自で再作成が必要です」を出して明示的に確定させる。
[ADR 0045](decisions/0045-ec2-persistent-workspace.md) 決定 13 が「利用者のデータを動かす経路は
必ず本人に見える形で」と決めた線の内側に置く。

## 70.6 golden スナップショットはアーキ毎に要る

`goldenSnapshot`（`:2719`）は `(af-pool, af-role=golden)` で引き、`af-image` タグが
デプロイのイメージと一致するものだけを使う。**アーキの次元が無い。**
このまま arm を足すと、**x86 の boot-install 済み home を arm の新規ユーザーに配る**——
§70.5 の壊れ方を、新規ユーザー全員が初回から踏む。

決め:

- golden スナップショットに **`af-arch` タグ**を打ち、`goldenSnapshot` は
  `(pool, role, image, arch)` で引く。**見つからなければ今までどおり `""`**
  ＝空の home を作る（遅いが正しい、という既存の best-effort をそのまま使う）。
- `goldenBaker` は**使われているアーキごとに 1 本ずつ焼く**。予約メンバーの鍵を
  `af-golden-seed` → `af-golden-seed-<arch>` / `af-golden-probe-<arch>` に分け、
  種のワークスペースに `slot_class` を与えて対象アーキのスロットへ載せる。
  焼く順は 1 tick 1 ステップの既存の形を崩さない（[ADR 0045](decisions/0045-ec2-persistent-workspace.md) 決定 9・
  「1 tick 1 ステップで状態は AWS 側」）。
- ⚠️ [`golden-snapshot-bake`](64-ec2-persistent-workspace.md) §64.28-29 の教訓が**アーキ毎に効く**:
  **起動を確かめていない golden は公開しない**。probe は履歴の無いメンバーシップで、
  対象アーキのスロット上で回す。arm の probe が通らないうちに arm の golden を publish しない。
- 費用: golden は焼く度に seed + probe のスロットを一時的に取る。クラスが 3 つなら
  3 倍。`bakeBlocked`（最後のスロットは取らない）はそのまま効くので、**上限を食い潰す危険は無い**が、
  **リリース直後の再焼きが 3 倍時間がかかる**ことは運用の手順に書く。

## 70.7 プールの断片化・上限

`freeSlots` は `instance-type` で絞るので、**m7i の空きスロットは m7g の要求には使えない**。
クラスが増えるほどプールは断片化する。

- `AF_ECS_EC2_MAX_SLOTS`（既定 8）は**プール全体の上限のまま**にする。
  クラス毎の上限は増やさない——「クラス A が上限で B は空いている」という説明不能な失敗を作る。
- 温かいスロットの当たり率は下がる。3 クラス運用なら「よく使うクラスに寄せる」のが運用の答えで、
  それは**テナント既定を 1 つ選ぶ**ことと同義。UI がテナント既定を持つのはこのためでもある。
- スリープ（15 分）と root EBS のみの課金があるので、断片化の実費は
  **停止中インスタンス 1 台につき約 $9.6/月**。クラスを 3 つ用意して各 1 台余るなら約 $19/月の増。
  m7g が 1 人分で月 $18 浮くので、**2 人以上が実際に移れば元が取れる**という規模感。

## 70.8 launch template / AMI / RuntimePlatform（実測）

**AMI**: arm64 の ECS 最適化 AMI は公開 SSM パラメータで解決できる（実測・2026-08-22）。

```
/aws/service/ecs/optimized-ami/amazon-linux-2023/recommended/image_id        → ami-0d8738404b89c4a70 (x86_64)
/aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id  → ami-02dfc6d3adc9fe799 (arm64)
```

**launch template は 1 本のまま、AMI だけを差し替える。**（実装時に §70.8 の当初案
「LT を 2 本」から改めた。）`40-ec2-pool.yaml` に `SlotAmiIdArm64` パラメータを足して
Export し、`30-ingress.yaml` が `AF_ECS_EC2_AMI_ARM64` として CP へ渡す。CP は arm64 の
スロットを起動するときだけ `RunInstances` に `ImageId` を渡して LT の値を上書きする
（リクエストのパラメータは launch template より優先される）。x86_64 の起動は
`ImageId` が nil のままで、**今までと 1 バイトも変わらない**。

- **なぜ LT 2 本をやめたか**: スロットのうちアーキで変わるのは AMI **1 フィールドだけ**で、
  インスタンスプロファイル・SG・root ボリューム・user-data（クラスタ join と
  af-mount/af-umount の設置。bash / awk / lsblk / `mkfs.xfs` だけなので AL2023 arm64 に
  全部ある）は完全に共通。CloudFormation は YAML のアンカーを解釈しないので、2 本目の LT は
  **~90 行の丸ごとコピー**になり、以後は「両方直す」を人が覚えている前提になる。
- **それでも IAM は増やさない**。当初 `ImageId` 上書きを却下した理由は「CP が SSM から AMI を
  引くことになる」だったが、**引くのは CloudFormation**（スタック更新時に公開パラメータを
  解決）で、CP はその文字列を env で受け取るだけ。`ssm:GetParameter` は増えないし、
  AMI のパッチ = スタック再デプロイ（決定 7）も**そのまま**である。
- `SlotAmiIdArm64` の型は `AWS::SSM::Parameter::Value<...>` ではなく素の `String`。
  前者は**空にできない**ので、既定が空である必要のあるこのパラメータには使えない。

**`RuntimePlatform`**: EC2 起動タイプのタスク定義で省略した場合に何が起きるかを実測した。

```
register-task-definition (requiresCompatibilities=[EC2], runtimePlatform 省略)
  → taskDefinition.runtimePlatform = null          ← X86_64 に既定化されない
register-task-definition (runtimePlatform={ARM64, LINUX})
  → 受理され、そのまま返る
```

つまり**今のコードのまま arm スロットに載せても placement は落ちない**（Fargate と違い
既定 X86_64 に落ちない）。それでも **明示する**ことを推す: `ec2InstanceId` 制約で
配置は決まっているとはいえ、アーキの不一致は「起動して落ちる」形でしか現れず、
`runtimePlatform` を宣言しておけば ECS が配置段階で弾く。`taskDefFingerprint` に
含まれるのでクラス変更が確実に新リビジョンになる、という副次効果もある（§70.5 の修復を
確実に起動させる）。

## 70.9 workspace イメージの multi-arch 化

**現状**: 公開イメージは単一 amd64 マニフェスト（実測: `crane manifest
ghcr.io/k-k1/agent-fleet/workspace:0.8.0` は `manifest.v2+json`・`architecture: amd64`）。
ビルドは `docker build`（`deploy/compose/release.sh:73-78`）で `buildx --platform` は
どこにも無い。[35](35-packaging.md) §35.3.1 の「amd64 先行」がそのまま生きている。

**良い知らせ**: `workspace/Dockerfile` は**既に両アーキ対応**。`dpkg --print-architecture` で
分岐する箇所が 9 つあり（mcp-grafana / go / awscli+session-manager-plugin / gh / rtk / agy /
cursor-agent / kiro / sha 検証）、arm64 の asset とチェックサムまでピン済み
（[43](43-kiro-agent-kind.md) は arm64 が **musl 変種必須**であることまで実測して書いてある）。
`control-plane/Dockerfile` も同様。**レシピは書けている。一度も焼いていないだけ。**

**もう一つ良い知らせ**: `k-k1/agent-fleet` は既に **public**（[public-release-prep](35-packaging.md) 完了後）。
GitHub ホストの **`ubuntu-24.04-arm` ランナーが無料で使える**ので、
[35](35-packaging.md) §35.9-5 が arm64 を後回しにした理由（「QEMU クロスビルドのホスト負荷」・
§35.4.5「公開 repo の無料 arm64 ランナーでの rootfs ビルドはできない」＝ dist repo に source が
無いことが理由で、**本 repo でビルドする本件には当てはまらない**）が**消えている**。

**決め**（案）:

- **タグは 1 つのまま、中身をマニフェストリストにする。** ECS も docker もホストのアーキで
  正しい方を引く。CP は 1 行も変えなくてよい——`AF_WS_IMAGE` は 1 つ、[62](62-ecs-start-latency.md) が
  書いた「`ImageTag` は CP と WS で共有＝別タグにできない」制約とも衝突しない。
- **ドリフト印は壊れない。** `ecrManifestMediaTypes`（`runtime_ecs_stale.go:87`）は
  **既に index / manifest list を要求している**ので、印は index digest になり、
  アーキに依らず 1 つに定まる。`stampImage` / `Stale` は無改修。
- **CI の形（実装は入れたが、走らせていない）**: `deploy/compose/release.sh` に
  `WS_PLATFORMS` を足した（空＝従来どおり。`linux/amd64,linux/arm64` で `docker buildx
  build --platform … --push`）。`publish-dist.yml` には `workspace_arm64` という
  workflow_dispatch の入力を足し、ON のときだけ QEMU + buildx を用意して
  `WS_PLATFORMS` を渡す。**既定 OFF なので通常のリリースは 1 アーキのまま。**
  - ⚠️ **QEMU での所要時間は未計測**で、このジョブの `timeout-minutes` は 90。
    イメージはコンパイルよりダウンロードと apt が支配的なので税は dpkg と tar に乗る
    はずだが、**それは仮説である**。入らなかったら**タイムアウトを伸ばすのは誤り**で、
    正解はジョブを 2 本（`ubuntu-latest` + **`ubuntu-24.04-arm`**）に割ってダイジェストで
    push し、`docker buildx imagetools create` で合成すること。**この repo は public に
    なったので arm64 のホストランナーは無料で使える。**
  - 押した後に**レジストリへ問い合わせて 2 アーキの index であることを確かめる**手順も
    ジョブに入れた。マニフェストリストは `docker images` に映らないので、
    「arm64 の側もちゃんと出たか」はローカルの docker を見ても答えられない——
    後から 1 アーキの push でタグを上書きしていた場合、症状は Graviton のスロットが
    pull に失敗することだけになる。
- ⚠️ **`--save`（air-gap の images tar）と `WS_PLATFORMS` は排他**にした。
  マニフェストリストはそもそもローカルの docker に載らないので `docker save` できない。
  air-gap の tar は「1 台への手渡し」であって配布経路ではない（ADR 0037）ので、
  ホストのアーキ 1 本のままでよい。
- **`crane` 経路はそのまま通る。** `harness/setup.sh` の `crane copy` はインデックスを
  丸ごと複製するので、sandbox / acrt の実デプロイ手順（[ecs-real-deployment](64-ec2-persistent-workspace.md)）は
  変更不要。
- **compose / air-gap の B tar（`docker save`）は単一アーキのまま**にする。
  マニフェストリストは `--load` できないので、ローカル開発と air-gap 配布は
  **ホストのアーキ 1 本**という現状を維持する（`--multi-arch` はリリース経路だけの旗）。

### 70.9.1 arm64 で最初に落ちたのは他社の CLI ではなく**自分たちのコード**だった

**実測（2026-08-22）**: Graviton4 の実機（m8g.2xlarge・ECS 最適化 arm64 AMI には docker が
載っている）で `docker build --build-arg BAKE_AGENT_CLIS=1 workspace/` を回したところ、
**3 分で落ちた**。原因は 9 種の CLI でも chromium でもなく、`workspace-agent` である。

```
./fs_fd_linux.go:257:29: cannot use st.Blksize (variable of type int32)
                         as int64 value in struct literal
```

`unix.Stat_t` は**アーキごとに別の構造体**で、`Blksize` は amd64 で `int64`・arm64 で
`int32`、`Nlink` は amd64 で `uint64`・arm64 で `uint32` である。`Nlink` は既に
`uint64()` を通していたのに `Blksize` は素で代入していたので、**amd64 では通り arm64 では
落ちる**。`int64()` を通して直した（`control-plane` は元から arm64 で通っていた）。

⚠️ **教訓は「実機を立てる前にクロスコンパイルしろ」である。**
`CGO_ENABLED=0 GOARCH=arm64 go build ./...` はこの開発 Workspace で**一瞬で終わり**、
同じエラーを EC2 を 1 台も立てずに出す。実機が要るのは「動くか」であって「ビルドが通るか」
ではない。`ci.yml` の control-plane / workspace-agent 両 job に arm64 のクロスビルドを
足したので、次は誰かが $0.05 と 3 分を払う前に赤くなる。

⚠️ この 1 件は、**「Dockerfile は両アーキ対応済み」と「イメージが両アーキでビルドできる」は
別の主張である**ことの実例でもある。前者は 9 か所の `dpkg --print-architecture` 分岐が
書いてあるという話で、後者は一度も試されていなかった。

⚠️ **未検証の山はここに残る。** [40](40-cursor-agent-kind.md) §10 は cursor-agent の
arm64 実機起動が未確認（forum #148408）、[32](32-agy-agent-kind.md) の agy は
[`host-no-rdrand-fips-blocker`](64-ec2-persistent-workspace.md) の別バージョンを踏む可能性、
kiro は arm64 だけ musl 変種。**arm スロットは、これら 9 種の CLI が arm64 実機で走る最初の場所**になる。
§70.14 の P1 は「arm イメージを焼く」ではなく「**arm イメージで既存の契約テストを全部通す**」。

## 70.10 Console / API / MCP

- `GET /api/admin/workspace-sizing`（`workspace_sizing.go`）を拡張:
  `slot_classes: [{id, label, arch, default, slots:[{instance_type, mem_mib, vcpu}]}]`。
  **既存の `slots` は「解決されたクラスの梯子」として残す**（古い Console が壊れない）。
- **メンバー詳細**（`tenantMembers.tsx`）: メモリのチップ列の**上**にクラスのチップ列を置く。
  クラスを変えるとメモリのチップ列がそのクラスの梯子で描き直され、
  `landed`（「m7g.xlarge（4 vCPU / 16 GiB）に載ります」）もそのクラスで再計算される。
- **言葉は技術識別子でなくドメイン概念**（`console-ui-plain-preview` の線）。
  画面に出るのは運用者が宣言した**表示名**（「標準（Intel）」「省コスト（Arm）」）で、
  `m7g.xlarge` は決定 21 の「載る箱」の行に**副次情報として**出る。
- **テナント設定**（`TenantDialog.tsx`）: tenant_admin に「既定のマシン種別」、
  super_admin に「このテナントで許可する種別」。`max_workspace_mem` 系と同じ二段の形。
- **警告**: §70.5.2 の確認文。既に home が存在するメンバーのクラスを変えるときだけ出す。
- **i18n**: ja / en 両方（裸和文 lint がある）。
- **MCP**: `set_user_quota` に `slot_class`、`set_tenant_limits` に `slot_class` /
  `allowed_slot_classes`。応答はクランプ後の値をエコー（既存の作法）。
- **利用者側**: メンバー自身の「環境」タブに読み取り専用で現在のクラスを出す
  （どのみち `uname -m` で分かる。決定 21 が `workspace-sizing` を全識別に開けたのと同じ理由）。
- **ガイド**: `docs/guide/admin/02-limits.md` と `.ja.md` の両方（二言語必須）。

## 70.11 費用の可視化 —— 何も足さなくてよい

[ADR 0048](decisions/0048-member-cloud-cost.md) 決定 1 で **`af-slot-size` は
コスト配分タグとして Active**、決定 3 でスロットに `af-membership` が付いている。
`launchSlot` は `af-slot-size` にインスタンスタイプをそのまま打つ（`:1645`）ので、
**m7g へ移った人の実費は [67](67-member-cloud-cost.md) のメンバー別費用に自動的に安く出る。**

⚠️ ただし決定 2 の線を守る——**稼働秒 × 単価の見積を新たに作らない**。
§70.3 の価格表は**設計判断のための一次情報**であって、製品の画面に焼き込む単価表ではない。
Console に出すなら「約 19% 安い」のような**運用者が宣言した文言**（クラスの表示名）に留め、
$ は Cost Explorer 由来の実費だけにする。

## 70.12 影響しないと確認したもの

- **AZ 分散 / 容量不足時の他 AZ 退避**（決定 15/16）: `freeSlots` はタイプ + AZ で絞るので
  クラス毎に独立に成り立つ。4 ファミリとも 3 AZ で提供（§70.3）。
- **スロット隔離**（決定 20 / マウント失敗の quarantine）: タグ由来でアーキ非依存。
- **home の hibernate / restore / backup**: snapshot はブロックデバイスの複製で、
  アーキに依存しない。**復元先のアーキが変わる場合だけ** §70.5 の修復が要る。
- **Service Connect / `Endpoint()`**: 変更なし。
- **`af-mount` / `af-umount` の SSM 経路**: AL2023 arm64 に同じコマンドがある。
- **ドリフト印 / 要再起動バッジ**: §70.9 のとおり無改修。

## 70.13 フェーズ

| | 内容 | 出口 |
|---|---|---|
| **P0** ✅ | §70.3.1 の性能実測（5 ファミリ × このリポジトリのビルド一式・`harness/bench-instance-classes.sh`） | 済。**m8g が m7i の上位互換**、**m6g は計算し続けるなら得しない**、**どちらの表が正しいかは人による**（§70.3.2-3） |
| **P1** ◐ | multi-arch イメージのリリース経路（§70.9・**仕組みは入れたが一度も走らせていない**）＋ `40-ec2-pool.yaml` の arm64 AMI（§70.8・済）＋ 契約テストを arm64 で回す（**未**） | `AF_ECS_EC2_SLOT_TYPES` を手で arm に書き換えれば**フリート全体が arm で動く**（＝案 0 が成立する） |
| **P2** ✅ | §70.5 の home 修復（刻印 + 自己修復 + JDK glob の修正）。**arm を誰かに配る前に必ず**入れる | x86 の home を arm スロットに載せても壊れない |
| **P3** ✅ | スロットクラス本体（parse / 解決 / クランプ / `RuntimePlatform` / golden のアーキ次元 / CFN パラメータ） | CP が複数クラスを持てる |
| **P4** | Console / API / MCP / ガイド（§70.10） | テナント管理者が画面で選べる |
| **P5** | 実 AWS で端から端まで（[ec2-live-harness](64-ec2-persistent-workspace.md) の手順・**プールを空にしてから**）。x86 → arm の切替を実機で 1 回通す | 実運用に出せる |

⚠️ **P2 を P3 より先に置いているのは意図的**。クラスが選べるのに home が壊れる状態を
一瞬でも出荷すると、壊れた `~` を持つ人が生まれ、後から入れた修復では**もう遅い**
（`~/repos` の中身は戻せない）。

## 70.14 未決

1. **どのファミリをクラスとして出すか。** §70.3.1〜70.3.3 の実測で決まった。
   x86_64 は `standard = m7i` と `saver = m6i`（前提条件ゼロ・出荷済み）。arm64 は
   **`arm = m8g`**（m7i の上位互換なので m7g を選ぶ理由が無い）と、$/時 の最安が要るなら
   **`econ = m6g`**（⚠️ 計算し続ける人には損。表示名で速度の含意を伝えること）。
   **m7g は出さない**——m8g に対して安くも速くもない。
2. **既定クラスを将来動かすか。** 既存デプロイの既定を arm に変えると全員が §70.5 を踏む。
   「新規テナントの既定だけ arm」は分岐が増える。当面**既定は `standard` のまま**を推す。
   ⚠️ 起票時はここに「[ADR 0044](decisions/0044-workspace-sizing.md) 決定 3（**既定オフで
   出した機能は存在しないのと同じ**）の裏返しで、クラスの選択肢自体は 30-ingress の既定に
   載せて出荷する」と書いたが、**実装時に取り下げた**。arm64 クラスは (1) arm64 マニフェスト
   を持つ workspace イメージ（P1・未）と (2) `Ec2SlotAmiArm64` の両方が要り、前者が無いまま
   既定に載せると「スロットは起動するがタスクが pull できない」クラスを配ることになる。
   さらに (2) が空のまま arm64 クラスを宣言すると CP は**起動を拒否する**ので、既定に載せた
   時点で新規デプロイが全部落ちる。**P1 が終わるまで既定は x86_64 単独**、有効化は
   `Ec2SlotTypes` の 1 行差し替え（30-ingress のパラメータ説明にそのまま書いてある）。
3. **§70.5.2 の A / B / C。** 本書は B を推すが、修復対象の表（§70.5）に漏れが無いことは
   実機で 1 回確かめないと言い切れない。
4. **クラス毎のスロット上限を持つか。** §70.7 では持たない方に倒したが、
   「econ は 10 台まで、standard は 2 台まで」という運用要求が出たら再検討。
5. **Fargate（`ecs`）側にも同じ軸を出すか。** Fargate は `runtimePlatform.cpuArchitecture=ARM64` で
   Graviton を選べ、価格も安い。本書は ecs-ec2 に限定したが、保存の形（`slot_class`）を
   ランタイム中立の「不透明な id」にしておけば、Fargate 側は
   「クラス = ARM64 かどうか」として後から乗れる。**そのために id を EC2 のタイプ名にしない**
   （§70.4.1）。
