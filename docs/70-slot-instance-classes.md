# 70. スロットのインスタンス種別（アーキ / 世代）をテナント・ユーザー毎に選ぶ

> 状態: **P0〜P5 のほぼ全て完了**（2026-08-23）。実デプロイ（<dev-deployment>）で arm64 の
> end-to-end が通り（§70.14）、**難所と言い続けたアーキ跨ぎの home 自己修復も通った**
> （§70.14.5）。**P5 は完了**（agy の OAuth・実セッションも §70.14.8 で通した）。残るは
> **publish 経路を本物のリリースで 1 度出すこと**だけ。arm64 クラスは既定にはまだ載せない。
> ADR は P1 着手時に `0051` として起票する。
> 本書の価格・AMI・ECS API の記述は **ap-northeast-1 の実 AWS（sandbox <account>）で
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
  丸ごと複製するので、sandbox / <prod-deployment> の実デプロイ手順（[ecs-real-deployment](64-ec2-persistent-workspace.md)）は
  変更不要。
- **compose / air-gap の B tar（`docker save`）は単一アーキのまま**にする。
  マニフェストリストは `--load` できないので、ローカル開発と air-gap 配布は
  **ホストのアーキ 1 本**という現状を維持する（`--multi-arch` はリリース経路だけの旗）。

**計測の段取り（2026-08-23・`arm64-image-time.yml`）**。上の未計測がこの経路を止めて
いる唯一のものなので、そこだけを測るワークフローを足した。

⚠️ **`publish-dist` をそのまま走らせて測ることはできない。** あれは GHCR と dist repo
への**公開**までやるもので、ビルド時間を知るためにリリースを作るわけにはいかない。
なので所要が未知の 1 ステップだけを**実経路と同じコマンド**で走らせて捨てる。
実経路（`deploy/compose/release.sh`）と同じに保つのは buildx の `--platform` と
`--provenance=false`・build-arg・コンテキスト・後段の 2 アーキ検証。触らないのは
dist repo と、利用者が pull できるタグ（使い捨ての `armtime-<run id>` へ push するので
manifest list は本物のまま、リリース済みタグの意味は変わらない）。

- **基準値も測る**: QEMU を入れる**前**に amd64 単独を別タグへ。同じジョブで後から
  測るとレイヤが効いて arm64 を実際より安く見せる。
- `timeout-minutes` は**わざと 150**（`publish-dist` の 90 より上）。問いは
  「90 分に入るか」なので、90 で切ると答えが**数字ではなくキャンセル**になる。
- **予算の目安**: `publish-dist` の実績は 9〜11 分（直近 8 回・全 success）で timeout は
  90 分。つまりこのステップに使える余裕は**およそ 80 分**。
- ⚠️ **workflow_dispatch はデフォルトブランチにファイルが無いと起動できない**
  （`--ref` でブランチを指しても 404）。だから**計測は develop へマージした後**になる。

### 70.9.2 ✅ 測った —— 余裕で入る（2026-08-23・run 32612894350）

| | 秒 | 分 |
|---|---:|---:|
| amd64 のみ | 135 | 2:15 |
| **amd64 + arm64（QEMU）** | **728** | **12:08** |
| arm64 の増分 | **+593** | +9:53 |

`architectures: amd64,arm64` も検証ステップで確認。使い捨てタグ 2 本は自動削除され、
GHCR に残っていないことも実物で確認した。

**判定: ジョブを 2 本に割る必要は無い。** `publish-dist` の実績は 9〜11 分なので、
`workspace_arm64: true` を足しても**合計およそ 21 分**——timeout 90 分に対して 4 倍以上の
余裕がある。§70.9 が用意していた分岐（`ubuntu-24.04-arm` との 2 ジョブ化）は**当面不要**で、
ネイティブ arm ランナーは「必要になったときの逃げ道」として書いてあるだけでよい。

- **仮説は当たっていた**: 「イメージはコンパイルより**ダウンロードと apt が支配的**なので
  QEMU の税は dpkg と tar に乗る」。実測の税は **arm64 単体で約 593 秒**で、
  同じイメージの**実機 Graviton ビルドが 118 秒**（§70.14.2）だから **約 5 倍**。
  ⚠️ **5 倍で済んでいるのは中身が I/O 寄りだから**であって、コンパイルが増えれば
  この比率は動く。数字は「いまのイメージの」ものとして扱うこと。
- ⚠️ **依然として一度も「本物の publish」は走っていない。** 測ったのはビルド 1 ステップで、
  `release.sh` を通した publish 全体（CP イメージ・native・dist repo への Release）を
  `workspace_arm64: true` で走らせるのは**次のリリースが最初**になる。

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

### 70.9.2 2 件目: rtk は arm64 で**原理的に動かない**（上流に走る版が無い）

1 件目（§70.9.1）を直して焼き直したら、次はこれで落ちた。

```
rtk: /lib/aarch64-linux-gnu/libc.so.6: version `GLIBC_2.39' not found (required by rtk)
```

rtk の arm64 配布は **gnu ビルドしか無く GLIBC_2.39 を要求する**のに、workspace イメージは
Debian 12（**glibc 2.36**）である。**ダウンロードも sha256 検証も通り、起動だけができない。**
[43](43-kiro-agent-kind.md) が kiro で記録したのと同じ型だが、**kiro には aarch64 の musl
変種があり、rtk には無い**（`v0.45.0` と直近の `dev-0.45.1-rc` 全てを確認）。

**直し方はアーキでの決め打ちではなく「実行して確かめてから残す」にした。** 上流が musl
aarch64 を出すか、ベースイメージの glibc が上がれば、コードを触らずに動き出す。
Dockerfile は焼いた後に `--version` を実行し、動かなければ削除して理由を
`/usr/local/share/agent-fleet/rtk-unavailable` に残す。L1 smoke は「不在＋理由あり」を
ok、「不在＋理由なし」を NG とする（rtk が黙って焼かれなくなる回帰は今までどおり落ちる）。

⚠️ **entrypoint の lean boot-install は確かめずに置いていた。** つまり arm64 の lean では
**PATH の先頭に動かない rtk が居座り、失敗するのは使った瞬間**だった。同じ検証を入れた。

⚠️ **帰結: arm64 のメンバーは rtk（トークン削減）を使えない。** これは値段と速さの隣に
並ぶ**第 3 の判断材料**で、§70.3.3 の表には出てこない。arm のクラスを出す運用者は
これを知ったうえで出すこと。

### 70.9.3 rtk を arm64 で動かすには（4 案・一次情報で確認）

| | 何をする | 効くか | 代償 |
|---|---|---|---|
| **A. Debian 13 (trixie) へ上げる** | ベースを `node:22-bookworm-slim` → `node:22-trixie-slim` | ✅ trixie の glibc は **2.41**（≥2.39） | **イメージ全体・両アーキ**に及ぶ |
| **B. rtk を自前で musl ビルド** | Rust の builder stage を足し `aarch64-unknown-linux-musl` を作る | ✅ | 他社の Rust をこちらが build/保守する |
| **C. 上流に aarch64 musl を出してもらう** | ~~issue を出す~~ → **もう出ている**（下記） | ✅（出れば） | **直りが止まっている** |
| **D. arm64 は rtk 無しで出す**（現状） | 何もしない | ❌ | arm のメンバーはトークン削減を失う |

**実測で確認した一次情報**（2026-08-22）:

- trixie `libc6` = **2.41-12+deb13u3**（bookworm は 2.36-9+deb12u14）。**A は確かに効く。**
- `node:22-trixie-slim` は存在する。
- rtk は **Apache 2.0 の公開 Rust**（`Cargo.toml` / `Cargo.lock` あり）。**B は技術的に可能。**
- 上流は **x86_64 musl を既にビルドしている**ので、`aarch64-unknown-linux-musl` の追加は
  向こうの CI のターゲット 1 行である。**C は最も安い本当の修正。**

⚠️ **ただし C は「issue を出す」ではない。既に出ている**（2026-08-22 時点で確認）。

| 種別 | 番号 | 状態 |
|---|---|---|
| bug | [#615](https://github.com/rtk-ai/rtk/issues/615) | open・**priority:high**・2026-03-16 起票。**症状が完全に一致**（Debian Bookworm / glibc 2.36 に GLIBC_2.39 のバイナリ） |
| enhancement | #1331 / #1850 / #2455 / #3241 | 全て open。同じ「aarch64 musl を出して」の**重複 4 件** |
| **PR** | **[#3318](https://github.com/rtk-ai/rtk/pull/3318)** | **open・mergeable・+12/−8・レビュー未依頼・コメント 0・2026-07-30 から動いていない** |

**詰まっているのはレビューであって、要望でも実装でもない。** PR の中身も妥当で、リリース
matrix に `aarch64-unknown-linux-musl` を足し、Homebrew formula の Linux/ARM を musl 側へ
向け替えるだけ。**`aarch64-unknown-linux-gnu` は matrix に残している**ので既存の利用者も
壊れない。

⚠️ **6 件目の issue を出してはいけない。** 純粋なノイズであり、要望の信憑性をむしろ下げる。
できるのは PR #3318 に**新しい情報を持って**一言添えることだけで、それも「+1」ではなく
「別環境での再現」「checksum は通って実行だけ落ちる＝インストーラは成功を報告する」という、
向こうがまだ持っていない事実を渡す場合に限る。

**2026-08-22、その形で 1 度だけ添えた**（[PR #3318 のコメント](https://github.com/rtk-ai/rtk/pull/3318#issuecomment-5377768939)）。
渡した事実は 3 つ: ① Raspberry Pi / RHEL ではなく **Debian 12 コンテナ on AWS Graviton4**
でも同じであること、② **DL と checksums.txt 検証は両方成功し、実行だけが落ちる**ので
sha256 で固定するインストーラは「検証済みの正常インストール」を報告してしまうこと、
③ 同じイメージで `x86_64-unknown-linux-musl` は動いているので **musl 経路は姉妹ターゲットで
実証済み**であること。あわせて Graviton での検証を申し出た。
**これ以上こちらから押さない**——次に動くのは向こうのレビューである。

⚠️ triage ラベルを付けているのは **AI bot（wshm）** で、人間の maintainer が見た形跡は
どのスレッドにも無い。**「triage 済み ＝ 人が判断した」と読まないこと。**

#### E. ベースを Amazon Linux にする（ECS なんだから、という案）—— **却下**

「デプロイが ECS なんだから、コンテナのベースも Amazon Linux にすれば揃うのでは」は自然な
発想だが、実測すると **rtk については後退**で、他も壊れる。

**⚠️ まず前提の整理: スロットのホストは既に Amazon Linux 2023 である。** ECS 最適化 AMI が
それで、`uname -m` も `dnf` もそこの話。しかし**CLI が動くかどうかを決めるのはコンテナの中の
glibc** であって、ホストのそれではない。ホストが AL2023 でも、コンテナが
`node:22-bookworm-slim` なら中は Debian 12 である。**この 2 つは別のレイヤで、混ぜると
「ECS なんだから Amazon Linux」という推論が成立してしまう。**

実測（2026-08-22・`crane export amazonlinux:2023` と AL2023 の repodata）:

| | glibc | chromium パッケージ |
|---|---|---|
| **Amazon Linux 2023** | **2.34** | **無い**（全 14,563 パッケージ中。あるのは `firefox` のみ） |
| Debian 12（現行） | 2.36 | あり・**revision まで固定**（[31](31-container-browser-pane.md)） |
| Debian 13 | 2.41 | あり（150 系＝現行より**古い**） |
| rtk aarch64 gnu が要求 | **2.39** | — |

1. **glibc が Debian 12 より古い（2.34 < 2.36）。** rtk は当然動かないままで、しかも
   **問題の軸で後退する**。[43](43-kiro-agent-kind.md) の kiro は x86_64 gnu が glibc **2.34 以上**を
   要求するので、余裕がちょうどゼロになる。
2. **chromium パッケージが存在しない。** [31](31-container-browser-pane.md) は Debian の
   `chromium` を（revision 固定 ＋ setuid sandbox helper の build 時検証込みで）採ると決めた。
   AL2023 ではその根拠が丸ごと消え、**同文書が比較して退けた** Playwright 配布 / Chrome for
   Testing へ戻ることになる。ブラウザペインは製品機能なので、これは小さくない。
3. **ランタイム毎にベースを変えると、イメージが 2 系統に分岐する。** workspace イメージは
   docker / native / Fargate / ecs-ec2 で**同じ 1 本**という前提で、GHCR のタグも
   `versions.json` のピンも air-gap tar も L1 smoke もその上に乗っている。「ECS だけ別ベース」は
   それを全部 2 倍にし、**同じ製品なのに動く場所によって中身が違う**状態を作る。
   本書はいま「アーキが 2 つになるだけでこれだけ壊れる」（§70.5・§70.9）を記録している最中で
   あり、そこへ**さらに軸をもう 1 本増やす**話になる。

**ベースを動かすなら候補は Debian 13 だけ**（A）。そしてそれも rtk のためにやるものではない。

**⚠️ A を「rtk のために」やってはいけない。** 効くのは事実だが、代償が rtk と無関係に大きい。

- **chromium が 151 → 150 へ「下がる」。** bookworm の pin は `151.0.7922.137-1~deb12u1`、
  trixie は `150.0.7871.100-1~deb13u1`。[31](31-container-browser-pane.md) は
  **Debian revision まで固定し**、setuid sandbox の検証込みで採用を決めている。その pin と
  検証を全部やり直すことになる。
- **python が 3.11 → 3.13 になる。** これは `~/.local/lib/python3.11/…` を持つ**全メンバーの
  永続 home に対する ABI 破壊**で、**arm64 だけでなく amd64 のメンバーも巻き込む**。
  §70.5 の「アーキが変わった人だけ入れ直す」自己修復では拾えない軸である。
- つまり **1 つのツールを 1 つのアーキで動かすために、フリート全体の移行を払う**ことになる。

**決め（推奨）**:

1. **いま: D で出荷する。** arm64 は rtk 無し、理由はイメージの中
   （`/usr/local/share/agent-fleet/rtk-unavailable`）と §70.3.3 の判断材料に書いてある。
   C はこちらから出す段階を過ぎており、できるのは PR #3318 を待つことだけである。
2. **C が動かず、rtk が arm の採用条件になるなら B。** 外科的で、他に何も壊さない。
   ⚠️ そのときは **amd64 も musl 自前ビルドに揃える**こと——片方だけ自前にすると、
   アーキによって「違うビルドの rtk」が走ることになる。
3. **A は独立した作業として、いずれやる。** bookworm の寿命・glibc・各種ツールチェーンの
   ためであって、rtk のためではない。やるときは**両アーキで L1 smoke を通し直す**のが条件で、
   python の ABI 破壊には §70.5 とは別の移行が要る。

### 70.9.4 この 2 件から言えること

**「Dockerfile は両アーキ対応済み」と「イメージが両アーキでビルドできる」は別の主張である。**
前者は 9 か所の `dpkg --print-architecture` 分岐が書いてあるという話で、後者は
**一度も試されていなかった**。試したら 2 件出て、1 件は自分たちのコード、1 件は上流の
配布物の穴だった。どちらも「arm64 対応済み」という記述の下に隠れていた。

### 70.9.5 3 度目で通った —— arm64 イメージは成立する

上の 2 件を直して焼き直した結果（m8g.2xlarge・native・2026-08-22）:

```
build|193            # 3 分 13 秒。QEMU ではなくネイティブなので速い
arch|arm64/linux
size_mb|5717         # BAKE_AGENT_CLIS=1 の焼き込み variant
smoke|PASS
```

**L1 image smoke が全項目 ok。** 9 種のエージェント CLI が **arm64 実機で実際に起動**した:
claude 2.1.238 / opencode 1.18.19 / codex 0.149.0 / copilot 1.0.80 /
**cursor 2026.08.11-e8db854** / **kiro 2.19.0**（musl 変種）/ go 1.26.7 / gh 2.98.0 /
chromium 151.0.7922.137。chromium は **headless の日本語スクリーンショット・setuid sandbox・
2 ページ同時 CDP** まで通った。

- [40](40-cursor-agent-kind.md) §10 が「配布資産は健全と検証したが**実 arm64 ハード起動のみ
  未検証**」として残していた項目は、**これで解消**した。
- [43](43-kiro-agent-kind.md) の「arm64 は musl 変種必須」は正しく、その通りに動いた。
- ✅ **agy も動く**（§70.13 で別途実測）。L1 smoke は agy の `--version` を**意図的に実行
  しない**（[decisions/0008](decisions/0008-antigravity-cli-agent-kind.md) — RDRAND を提示しない
  ホストで SIGABRT するため）ので、イメージのビルドだけでは分からないまま残っていた。

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

## 70.13 agy は Graviton で動く（実測・2026-08-22）

`deploy/aws/ecs/harness/probe-agy-arm64.sh`。agy は Go **BoringCrypto (FIPS)** ビルドで、
x86 では FIPS 乱数の自己テストが **RDRAND を必須**とし、提示しないホストでは起動直後に
`CRNGT failed` → SIGABRT する（[decisions/0008](decisions/0008-antigravity-cli-agent-kind.md)）。
`hostcaps.AgyStatus` はそれを **`GOARCH == "amd64"` のときだけ**課していた——
つまり **「arm64 では課さない」は一度も実行されていない仮定**だった。

⚠️ **1 台で測ってはいけない理由**があった。arm64 の FIPS 乱数が x86 の RDRAND と同じように
**RNDR**（ARMv8.5-RNG）を掴むなら、答えは Graviton の**世代で割れる**——そして割れる先は、
§70.3.3 が費用重視のメンバーに勧めた **m6g** である。なので 3 世代とも測った。

| | CPU | `/proc/cpuinfo` の `rng` | `agy --version` | `agy --help` |
|---|---|---|---|---|
| m8g.large | AWS Graviton4 | **yes** | `1.1.17`・**RC=0** | RC=0 |
| m7g.large | AWS Graviton3 | **yes** | `1.1.17`・**RC=0** | RC=0 |
| **m6g.large** | Neoverse-N1（Graviton2） | **no** | `1.1.17`・**RC=0** | RC=0 |

（agy 1.1.17-5084709148033024・`linux-arm/cli_linux_arm64.tar.gz`・sha256 検証済み・
Workspace イメージと同じ `node:22-bookworm-slim` の中で実行・残存リソース 0）

**決め手は m6g である。** `rng` を**持たないのに動いた**——arm64 の BoringCrypto は乱数を
命令からではなく**カーネルの `getrandom(2)` から**取っている。`hostcaps` の分岐は正しく、
これで**仮定ではなく測った結果**になった（同じ内容をコード側のコメントにも書いた）。

⚠️ **この測り方の限界**: 確かめたのは `--version` と `--help` の 2 つだけで、
**OAuth 認証と実セッションは通していない**。FIPS 自己テストはプロセス起動時に走るので
SIGABRT はこれで捕まるが、「実際に会話ができる」ことの証明ではない。そちらは P5 の仕事。

## 70.14 P5 —— 実デプロイで通した（<dev-deployment>・2026-08-22）

0.10.0 を <dev-deployment>（sandbox <account>）と <prod-deployment> に本デプロイした状態で実施。arm は
**<dev-deployment> だけ**に入れた——<prod-deployment> には実ユーザーの Workspace が動いており、イメージ参照を
変えると「要再起動」バッジがその人に出るため。

### 70.14.1 デプロイしただけで通ったもの

CP の起動ログが、ユニットテストで固定した内容の実機版になっていた。

```
runtime=ecs-ec2 … arm64-ami=(none)
classes=standard(x86_64:m7i.large/…) saver(x86_64:m6i.large/…) default-class=standard
golden[x86_64]: no golden for …:0.10.0; baking one
```

- **出荷した既定の 2 クラス文字列が実 CP で意図どおりパースされた**（既定は `standard`＝誰も動かない）。
- **`golden[x86_64]` の接頭辞**＝アーキ毎 golden の分岐が実機で動き、x86_64 1 本と判定。
- ✅ **`saver` を選んだメンバーが実際に m6i のスロットに載った**（`af-slot-size=m6i.large`）。

### 70.14.2 arm を入れるのに必要だったもの

⚠️ **リリース済みの `0.10.0` タグは触っていない。** 別タグ `0.10.0-arm` を作って `ImageTag` を
差し替えた（切り戻しはパラメータ 1 つ）。index の amd64 側は **released 0.10.0 と同一 digest**。

1. arm64 イメージを **v0.10.0 から** Graviton 実機でビルドし ECR へ push
   （`harness/push-arm64-image.sh`・116 秒・IAM ロールは使い捨てで自動削除）
2. `crane index append` で 2 アーキの index を `0.10.0-arm` に作成
3. `ImageTag=0.10.0-arm` / `Ec2SlotAmiArm64=ami-…` / `Ec2SlotTypes` に arm クラス追加

⚠️ **ここで 1 度間違えた。** 検証用ハーネスから `BAKE_AGENT_CLIS=1` を引き継いでビルドして
しまい、**arm64 だけ焼き込み variant**（2,536 MiB）になった。リリースが publish するのは
**lean**（`release.sh` の既定は 0）で amd64 は 920 MiB。**動くし、テストも通る**——同じタグの
下で amd64 と arm64 が別の製品になるだけである（CLI が `/usr/local` か `~/.local` か）。
気づけたのはサイズが 2.7 倍だったからで、それが無ければ気づかなかった。
`push-arm64-image.sh` の既定を 0 にし、理由をコメントに残した。

### 70.14.3 arm64 の end-to-end（golden ベイカーが自動で走らせた）

**Console を一度も触っていない。** arm クラスを足すと、ベイカーが 2 アーキを見つけて
勝手に両方焼き始める——そしてそれが P5-arm の経路そのものである。

```
golden[arm64]: no golden for …:0.10.0-arm; baking one
golden[arm64]: the seed finished boot-install; stopping it to capture the home
golden[arm64]: releasing the seed's slot before the snapshot
golden[arm64]: snap-04aba… is baked; booting a probe from it before publishing it
golden[arm64]: snap-04aba… is now the golden — a probe started from it cleanly
```

これで通ったもの:

| | 証拠 |
|---|---|
| arm64 スロットが arm64 AMI から起動 | `i-048dd841… m8g.large arm64 running` |
| `RuntimePlatform=ARM64` でタスクが配置された | seed のタスクが動いた（配置に失敗していない） |
| **multi-arch index から arm64 側が pull された** | 同上 |
| entrypoint と boot-install が arm64 で完走 | `the seed finished boot-install` |
| **Agent が arm64 で応答した** | 同上（`markHomeBaked` は `agentSessions` の成功が条件） |
| home が `af-arch=arm64` で snapshot された | `golden`/`arm64` のタグ |
| **履歴の無いメンバーシップで probe が起動した** | `a probe started from it cleanly`（§64.28.3 が必須と決めた検査） |
| **arm64 の publish が x86_64 の golden を消さない** | golden が 2 本共存（`dropSupersededGoldens` のアーキ絞り） |
| 予約鍵のアーキ分け | `af-golden-seed`（x86・従来のまま）と `af-golden-seed-arm64` |
| seed/probe の後片付け | home ボリュームは利用者の 1 本だけ残った |

### 70.14.4 それでも残っている 2 つ

- ✅ → **アーキ跨ぎの home 自己修復（§70.5）は §70.14.5 で通した。**
- ✅ → **agy の OAuth と実セッションも §70.14.8 で通した**（そこで別のバグを 1 件見つけた）。

## 70.14.5 アーキ跨ぎの home 自己修復 —— 通した（<dev-deployment>・2026-08-23）

本書が「難所はここ」と言い続けた経路。**既に home を持つメンバーを x86 → arm へ移す**もので、
golden ベイカーは常に新規 home から始まるため構造的にこの経路を通らず、ここまで一度も
走っていなかった。`ImageTag=0.10.1-dev-b5ea1085`（`temp/sg4h6cj`＝placeHome 修正入り）で実施。

利用者 `af-ws-k1-kami-gmail-com` を `saver`(m6i.large/x86_64) → `arm`(m8g.large/arm64) へ。

```
15:35:29 ecs-ec2: … now needs m8g.large but its home is on i-0711ed3e5de6d168e; releasing that slot first
15:35:35 ecs-ec2: released slot i-0711ed3e5de6d168e from af-ws-k1-kami-gmail-com
15:35:37 ecs-ec2: grew the pool with slot i-05ec4df1eedbfe787 (m8g.large, ap-northeast-1a)
15:38:42 ecs start: service af-ws-k1-kami-gmail-com Agent healthy 163s after Start
```

| | 証拠 |
|---|---|
| 合わなくなったスロットの解放 | `released slot i-0711ed3e5de6d168e`（`e1c3c59b` が実機で効いた） |
| m8g のスロットに載る | `i-05ec4df1eedbfe787` m8g.large arm64・`af-slot-size=m8g.large` |
| **home が同じボリュームのまま付け替わる** | `vol-00753f1f9376b8ee4`（新規作成されていない） |
| task def の矛盾が解消 | `:22` = ARM64＋制約が新 m8g＋image は新タグ（`:21` は **ARM64＋m6i 制約**だった） |
| 刻印を読んで自己修復 | `[entrypoint] arch: この home は amd64 で作られ、いま arm64 の上に居ます` → `削除 ~/.local/bin/{claude,codex,opencode,copilot,rtk,agy,cursor-agent}`・`~/.local/lib/node_modules` |
| boot-install の走り直し | `boot-install ok` → `claude 2.1.238 (Claude Code)` |
| Workspace が正常に上がる | `Agent healthy 163s after Start` |
| **JDK がアーキ毎に解決される（P2）** | 実機で `install-jdk 21` → `downloading Temurin 21 (aarch64)` → `…/jvm/temurin-21-jdk-arm64`・`java -version` が起動。**`pickArchJDK` 前なら glob の先頭＝amd64 を掴んで `Exec format error`** になっていた |
| rtk は arm64 で入らない | `GLIBC_2.39 not found` を検出して**導入せず理由を残す**（§70.13 の設計どおり） |

⚠️ **`~/repos` の保全はこの回では確かめていない。** 対象の home に clone が 1 つも無かった。
entrypoint は `~/repos は触っていません` と言い、削除対象は `~/.local` 配下に限られているが、
**実物で確認したわけではない**ので未検証として残す。

### 70.14.6 ⚠️ 収束しない `starting` は Console から停止できなかった

**この検証にたどり着く前に、まず利用者の Workspace が操作不能になっていた。** 症状は
「停止を押しても起動中のまま進まない」。原因は 4 段の連鎖で、**どれも単体では正しい**:

1. §70.14.2 の placeHome バグが task def `:21` に **`RuntimePlatform=ARM64` ＋
   `placementConstraints: ec2InstanceId == <m6i の箱>`** という矛盾を焼き付けた。
   ECS は永久に配置を拒否する（`missing an attribute required by your task`）。
2. `State()` は `case s.DesiredCount >= 1: return "starting"`。配置できない以上
   `RunningCount` は永久に 0 で、**`starting` から出る経路が無い**。
3. CP の `Start` は `case "starting": return nil`（「Already converging」）。
4. Console の電源トグルは `if (!running) { startWs() }` で、`running` は
   `wsState === "running"` だけ。**`starting` では停止ではなく起動を投げ**、それを 3 が
   捨てる。さらに `wsStartBusy` が `starting` で true になりボタン自体が `disabled`。

**UI から出せる操作が「起動」しか無く、その起動が no-op になる。**復旧には CP を経由しない
`aws ecs update-service --desired-count 0` が要った。

⚠️ 教訓は「**`starting` を一時状態だと決めてかかっていた**」こと。ECS の cold pull は分単位
という想定はしていたが、**終わらない `starting`** は想定になく、二度押し防止という正しい配慮が
そのまま行き止まりになった。placeHome の修正は「次の Start で正しいスロットを選ぶ」ものだが、
**その次の Start が永久に来ない**ので届いていなかった。

直し（`wsPowerStops`）: 電源トグルは `running` に加えて **`starting` でも停止側を向く**。
無効化してよいのは楽観的な `"…"` 遷移だけ（`wsBusy`）で、サーバ報告の `starting` では
押せるままにする。起動のキャンセルはそれ自体まっとうな操作でもある。`wsStartBusy` は
そのままなので**二重 Start は依然として防がれる**（3 者を固定する契約テストを追加）。

✅ **もう一方の弱点も直した: 配置できない理由が Console から見えなかった。** ECS は
イベントに理由を書いているのに CP はそれを捨てて素の `starting` を返しており、今回
突き止められたのは `describe-services` を手で読んだからだった。運用者に同じことは
させられない。

`State()` が `starting` を返すとき、**現デプロイ以降**のイベントに配置失敗があれば
phase に載せる（`blocked: <ECS の原文>`）。Console は接頭辞を訳語の見出しに写し、
原文をその下に技術的詳細として出す**既存の仕組みにそのまま乗る**——原文の方が価値が
ある側で、落ちた制約の名前が書いてある。CP ログにも 1 度だけ出す。AWS 呼び出しは
増やしていない（呼び出し元が既に取った `DescribeServices` の `Events` を使う）。

- ⚠️ **`State()` が書き込みをする唯一の場所**になる。`BootPhase()` は ctx を取らず
  AWS を呼べないので、読み取り経路でここ以外にイベントへ手が届かない。
- ⚠️ **「最近イベントが出たか」では判定できない。** ECS はイベントを重複排除するので
  **詰まりは永続するのにイベントは 1 度きり**（実測: 今回の詰まりもイベント 1 本で
  あとは沈黙だった）。だから現デプロイの `CreatedAt` と突き合わせる。
- ⚠️ 直って再デプロイしても古い苦情はイベント一覧に残る。そこを読むと**通常の
  コールドスタートが偽の診断になる**。
- `running` になったら blocked の phase は消す。**接頭辞に限定する**のは、他の phase は
  進行中の Start のもので、4 秒ごとのポーリングがそれを消すと起動ダイアログが
  起動の最中に真っ白になるため。

### 70.14.7 ⚠️ golden は「image が同じ」だけで選ばれていた（同じデプロイのログで発見）

`ImageTag` を差し替えた直後、ベイカーが 2 アーキ分を同時に焼いた。そのログ:

```
15:17:28 golden[x86_64]: snap-00da9f0a86d2fc7a9 is baked; booting a probe from it before publishing it
15:17:30 ecs-ec2: seeding a new home for af-golden-probe from the golden snapshot snap-0fd69a1e3a4c1384b
15:17:38 golden[arm64]: snap-0fd69a1e3a4c1384b is baked; booting a probe from it …
```

**x86_64 の probe が arm64 の候補から seed されている。** `goldenSnapshot`（新しい home を
どのスナップショットから作るか）は pool・role・**image** で絞っており、**アーキで絞って
いなかった**。golden はアーキ毎に 1 本焼かれて**どれも同じ image スタンプを持つ**ので、
2 つ目のアーキを宣言した瞬間に image が判別力を失い、残る決め手が `snapshotStartedAfter`＝
**「後に焼き終わった方が勝つ」**になる。ベイカー側の `goldenFor` は最初から `arch` を
引数に取っていたので、**読む側だけに穴が空いていた**。

⚠️ **壊れないのが厄介だった。** §70.5 の自己修復が違うアーキの中身を消して boot-install を
やり直すので、この probe は `Agent healthy 96s` で**成功**している。実際 WS ログ側には
対になる `この home は arm64 で作られ、いま amd64 の上に居ます` が残っていた。実害は 2 つ:

- golden の存在意義（boot-install を飛ばすこと）が**まるごと捨てられる**。それでいて
  「golden から seed した」とログは言うので、速くならない理由が読み取れない。
- **§64.28.3 の probe が別アーキのスナップショットを「検証済み」にしてしまう。**
  publish されるのは自分のアーキの候補なので、**起動を確かめていない golden が公開される**。

直し: `goldenSnapshot` も `snapshotArch(s)` で絞る。読む側にも同じ既定を置いた
（`archOrX86`）——タグの無いスナップショットが x86_64 なのと同じ理由でクラス未宣言のプールも
x86_64 であり、**正規化した値どうしを比べないと旧デプロイの golden が一切引けなくなる**。

⚠️ 一般則として: **「同じ image」で一意になる前提は、次元が 1 本増えた瞬間に黙って壊れる。**
壊れ方が例外ではなく「新しい方が勝つ」なので、テストも実機も緑のまま通る。

## 70.14.8 agy の arm64 実セッション —— 通った。ただし別のバグが出た（2026-08-23）

§70.13 が「確かめたのは `--version` と `--help` の 2 つだけ」と書いた残件。arm64 の
実 Workspace（m8g.large）で OAuth からセッション起動まで通した。

**arm64 側は全部問題なかった。**

| | 証拠 |
|---|---|
| CLI が起動する | `agy --version` → `1.1.19` rc=0（FIPS 自己テストは起動時なので SIGABRT ならここで出る） |
| **OAuth が通る** | `POST /connections/agy/start` → `complete`、TUI のヘッダに `k1.kami@gmail.com (Google AI Pro)` |
| **認証付き API 呼び出しが通る** | `agy models` が rc=0 でカタログを返す／`/connections/agy/usage` が応答 |
| セッションが起動して TUI が描画される | tmux のペインにバナー・モデル名・入力欄 |

⚠️ **これで §70.13 の「`getrandom(2)` 由来だから arm64 でも動く」は、起動 2 種ではなく
実際の認証済み通信で裏が取れた。**

### アカウント検証待ちは上流の話

TUI に `⚠ Verifying your account... We're finishing verifying your account eligibility.`
が出ていた。**これは Antigravity 側のアカウント審査**で、アーキにも本製品にも関係ない。
「arm64 だから動かない」に見えてしまうので、切り分けとして記録しておく。

### 🔴 本物のバグ: モデル選択が黙って無視されていた（アーキ非依存）

同じ画面にもう 1 本出ていた警告が本物だった。

```
⚠ model gemini-3.5-flash-low    Gemini 3.5 Flash (Low) is not recognized as a known
  model or custom model in settings. Using "Gemini 3.7 Flash (High)" instead.
```

`agy models` の**出力形式が版で変わっていた**。実機で `cat -A` して確かめた区切りは TAB:

```
1.1.17:  Gemini 3.5 Flash (Medium)                        ← 表示名だけ
1.1.19:  gemini-3.5-flash-low<TAB>Gemini 3.5 Flash (Low)  ← id、そして表示名
```

`agy.parseModels` は**行を丸ごと id にしていた**（旧形式では表示名がそのまま
`--model` に通ったので、それで正しかった）。結果、新形式では
`"gemini-3.5-flash-low\tGemini 3.5 Flash (Low)"` を `--model` に渡し、CLI が
「知らないモデル」として**既定へフォールバック**していた。

⚠️ **失敗の形が最悪だった: セッションは起動し、動き、黙って別のモデルだった。**
エラーは TUI の警告 1 行だけで、Console にも API にも何も出ない。**選んだモデルで
課金されていない**ことに、画面を見ない限り誰も気づかない。

⚠️ **イメージのピンは 1.1.17 のままで、実機は 1.1.19 だった**（`agy update` が
あり、Workspace は先へ行ける）。**だから両形式に対応させた**——片方に寄せると、
ピン版のままの Workspace かフィールドで更新された Workspace のどちらかが壊れる。

⚠️ これは [[report-arm-pitfalls]] が言う **「TUI / CLI の文字列契約は毎版壊れる」**
そのもので、**arm64 とは何の関係もない**。arm64 の検証をしていなければ、
気づかないまま全員が既定モデルで走り続けていた。

❌ **未検証**: 保存済みのモデル選択が**旧形式の表示名のまま**の利用者がいる。
1.1.19 が表示名も受け付けるのかは確かめていない。受け付けないなら、選び直すまでは
同じ「黙って既定」になる。

## 70.14.9 🔴 agy は自己更新の封じ込めを無視していた（ピンが効いていなかった）

§70.14.8 のモデル取り違えを追ううちに、**なぜ実機が 1.1.19 なのか**が分からなくなった。
イメージのピンは 1.1.17 で、`AGY_CLI_DISABLE_AUTO_UPDATE=1` も設定されている。
agy 自身のログが答えを持っていた:

```
12:28:30  Language server version: 1.1.17
12:28:30  auto_updater.go:305] Spawned background update process with PID 803
12:29:04  Language server version: 1.1.19      ← 34 秒後には別物
```

`env | grep AGY` で `AGY_CLI_DISABLE_AUTO_UPDATE=1` が実際に入っていることも、
その文字列がバイナリに存在することも確認した上で、**agy は背景更新プロセスを起こして
自分を書き換えた**。バイナリの mtime も OAuth と同じ分に変わっている。

### 🔴 真因は上流ではなく、こちらの値だった

当初これを「上流の挙動なので塞げない」と判断したが、**誤りだった**。同じコマンドを
値だけ変えて実機で走らせると、答えは一行で出た（`last_check.timestamp` を消して
15 分の debounce を外してから実行）:

```
AGY_CLI_DISABLE_AUTO_UPDATE=1     → auto_updater.go:305] Spawned background update process with PID 1698
AGY_CLI_DISABLE_AUTO_UPDATE=true  → auto_updater.go:218] Auto-update disabled via environment variable
```

**受け付ける値は `true` だけで、`1` は黙って無視される。** 公式ドキュメント
（antigravity.google/docs/cli/troubleshooting）も `true` と書いている。`=true` のときは
`last_check.timestamp` が再生成されないことも確認した——チェック自体をしていない。

⚠️ **この誤りが生まれた経緯そのものが教訓**。docs/32 は「バイナリ実測で
`AGY_CLI_DISABLE_AUTO_UPDATE` 環境変数を**発見** → `=1` に設定して封殺」と書いていた。
**env 名を見つけたところで止まっている。** 名前は文字列としてバイナリに転がっているが、
**値の判定はコードの中にあって `strings` には出ない**。設定したという事実は、効いたと
いう証拠ではない。**効いたことをログで一度確かめるまでは、封殺したと書いてはいけない。**

⚠️ ついでに: この env は **`agy --version` では走らない**（軽量パスで updater を
起こさない）ので、検証には実際に仕事をするコマンド（`agy models` など）が要る。
一時 HOME での検証も**認証が要るコマンドでは成立しない**。

### それ自体より悪かったこと: ピンに戻す仕組みが無力だった

`~/.local` は永続 home なので、lean（既定）では `~/.local/bin/agy` が **dev 所有＝
書き込み可能**。焼き込み版の `/usr/local/bin/agy` は root 所有でピンが守られるが、
出荷する既定にその防壁は無い。

そして repin 判定は **`.agy.version` マーカー**を見ていた。⚠️ **マーカーは「AF が最後に
入れた版」であって「いま在る版」ではない。** 自己更新は AF のファイルを動かさないので:

| | marker | 実体 | pin | repin の判断 |
|---|---|---|---|---|
| 発見時 | 1.1.17 | **1.1.19** | 1.1.17 | 「一致」→ **何もしない**（固着） |
| 修正後・同じ状況 | 1.1.17 | 1.1.19 | 1.1.19 | 実体 == pin → 何もしない（正しい） |
| 修正後・次に 1.1.20 へ流れたら | 1.1.19 | 1.1.20 | 1.1.19 | 実体 != pin → **ピンへ戻す** |

旧コメントは「agy 自身の自己更新で進んでいたら比較がズレて再導入されるだけで**無害**」と
書いていた。**無害ではなかった**——それが成り立つのは opt-in ON 側（latest と比べる）
だけで、ピン側では marker == pin のまま実体だけが先へ行って**静かに固着する**。
しかも実害は §70.14.8 のとおり「セッションは動くのに黙って別のモデル」だった。

### 直し

- **ピンを 1.1.19 / build `4894004681244672` へ更新**。sha256 は公式 manifest の
  sha512 を検証した上で計算（両アーキとも一致を確認）。`probe-agy-arm64.sh` は
  Dockerfile の ARG を読むので自動追随する。
- **`agy_effective_version()` を入れ、marker ではなく実体を問う**。問えないのは
  RDRAND 非提示の x86 ホストだけで、そこは起動即 SIGABRT（decisions/0008）なので
  marker に落ちる。**arm64 は §70.13 の実測で安全と確定している**（BoringCrypto が
  乱数を命令でなく `getrandom(2)` から取るため、`rng` を持たない Graviton2 でも RC=0）。
  実機で `effective=[1.1.19] / marker=[1.1.17]` を確認済み。
- 副次効果として opt-in ON 側の無駄も消えた。marker が古いだけで**毎回 ~187MB を
  取り直していた**。

⚠️ **一般則: 自己更新する CLI に対して「自分が最後に入れた版」を記録して比較しても、
ピン管理にはならない。** 記録は自分の行動の記録であって、ディスクの状態ではない。

- ✅ **`AGY_CLI_DISABLE_AUTO_UPDATE` を `true` へ修正**（Dockerfile と
  `agy-contract.yml` の両方）。これで自己更新そのものが止まる。
- ✅ **カタログのドリフト契約を追加**（`TestDriftAgyModelsCatalog`）。
  `cli-release-watch` → `agy-contract` の自動連鎖は既にあったのに、検査していたのが
  TUI ペインのフッタだけで**カタログは誰も見ていなかった**から §70.14.8 を逃した。

⚠️ **実体比較は封殺を直した後も残す。** 封殺が外れる経路は他にもある: 利用者の明示的な
`agy update`、自己更新 opt-in、そして**封殺が効いていなかった時代に焼かれた home** は
その版を抱えたまま永続する（`~/.local` は消えない）。

❌ **残: 既に流れてしまった Workspace。** `~/.local/bin/agy` が 1.1.19 になっている
home は、次の起動で実体比較がピン（同じ 1.1.19）と一致するので、そのまま。害は無いが、
**「封殺が効いていた」ことの証拠にはならない**——新しいイメージで焼き直すまで、
その home は封殺前の世界の産物である。

## 70.15 フェーズ

| | 内容 | 出口 |
|---|---|---|
| **P0** ✅ | §70.3.1 の性能実測（5 ファミリ × このリポジトリのビルド一式・`harness/bench-instance-classes.sh`） | 済。**m8g が m7i の上位互換**、**m6g は計算し続けるなら得しない**、**どちらの表が正しいかは人による**（§70.3.2-3） |
| **P1** ◐ | multi-arch イメージのリリース経路（§70.9・**ビルド 1 ステップは実走して 12 分と測れた**が、**publish 全体はまだ**）＋ `40-ec2-pool.yaml` の arm64 AMI（§70.8・済）＋ 契約テストを arm64 で回す（**未**） | `AF_ECS_EC2_SLOT_TYPES` を手で arm に書き換えれば**フリート全体が arm で動く**（＝案 0 が成立する） |
| **P2** ✅ | §70.5 の home 修復（刻印 + 自己修復 + JDK glob の修正）。**arm を誰かに配る前に必ず**入れる | x86 の home を arm スロットに載せても壊れない |
| **P3** ✅ | スロットクラス本体（parse / 解決 / クランプ / `RuntimePlatform` / golden のアーキ次元 / CFN パラメータ） | CP が複数クラスを持てる |
| **P4** | Console / API / MCP / ガイド（§70.10） | テナント管理者が画面で選べる |
| **P5** ✅ | 実 AWS で端から端まで（§70.14・<dev-deployment>）。**arm64 の golden が probe 込みで publish され**、**x86 → arm の切替も home 自己修復ごと通り**（§70.14.5）、**agy の OAuth と実セッションも通った**（§70.14.8）。道中で 4 件のバグを実機だけが教えてくれた（§70.14.6/7/8） | 実運用に出せる |

⚠️ **P2 を P3 より先に置いているのは意図的**。クラスが選べるのに home が壊れる状態を
一瞬でも出荷すると、壊れた `~` を持つ人が生まれ、後から入れた修復では**もう遅い**
（`~/repos` の中身は戻せない）。

## 70.16 未決

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
