# 0122. Workspace の外向き通信は、プロセスが無視できるプロキシ変数ではなく、出口のネットワークで記録・判定する：経路上に egress ゲートウェイを置く

[English](0122-egress-gateway.md) | 日本語

- 状態：**提案**（2026-10-23）。ドキュメントのみ。以下はまだ何も実装していない。コードとテンプレートの
  事実はこの日付のツリーから読んだもの。Squid の挙動は公開文書と AWS のブログ記事から読んだもので、
  **動かしてはいない**。書いている間は Docker もクラウドのアカウントも使えなかった。
- フォローアップ：#1181、#2603、#2604
- 関連：[0047](0047-tenant-network-restriction.ja.md)（受信側の IP 制限。本書は触れない）/
  #1056（認証情報の注入。先にこの fence が要る）/ #1067（OS サンドボックスが答えにならない理由）

## 背景

egress の forward proxy（`control-plane egress-proxy`、docs/log/20）は記録もブロックもできるが、届いた
通信についてだけだ。届くかどうかは Workspace の環境にある `http_proxy` / `https_proxy` 次第で、それを無視する
プロセスは見えないまま外に出る。本書の要件は次のとおり。**Workspace から外への接続はすべて記録されること。
記録されない素通しは、導入組織にとって許容できない。** 現状：

- どのテンプレートもプロキシを起動しない（`deploy/compose`、`deploy/aws`、`deploy/kubernetes`、`deploy/gcp`）。
- 通信をプロキシに通す強制がない。docker のユーザー別ネットワークは internal ではなく、ECS の Workspace 用
  セキュリティグループには egress ルールがなく、Kubernetes の `egress-internet` ポリシーは RFC 1918 以外の
  全アドレスを通す。
- 変数がすべてのターゲットに届くわけではない。`main.go` は `mgr.extraEnv` に入れ、docker・native・Kubernetes
  はそれを読む（`runtime.go`、`runtime_native.go`、`runtime_kubernetes.go`）。ところが `ecsFactory.New`
  （`runtime_ecs.go`）は起動ごとの env しか使わないので、ECS と ecs-ec2 の Workspace にはプロキシ変数が
  まったく入らない。
- 観測が失われうる。`egressBatcher.flush` は POST の前にバッチを空にし、通信エラーや 2xx 以外の応答では
  そのまま捨てる（`egress_proxy.go`）。
- 観測が持つのはホストと件数だけで、どの Workspace の接続かは分からない。

ターゲットを見て分かった制約：

- AWS のスタックは VPC と、固定した Elastic IP を持つ NAT ゲートウェイを自前で作る。導入組織は安定した
  出口アドレスを許可リストに入れられる（`00-network.yaml`）。ECS のタスクは `awsvpc` で、タスクごとに
  ENI とプライベート IP を持つ。
- GKE のスタックは、予約したアドレスを持つ Cloud NAT を自前で作る（`network.tf`）。
- compose では CP がホストネットワークにいて、各 Workspace へ公開した `127.0.0.1` のポート経由で届く。
  docker の `--internal` ネットワークは公開ポートを壊す。マスカレードを切ったブリッジは fence にならない。
  止まるのは NAT であってルーティングではなく、ホスト、メタデータのアドレス、docker の組み込み DNS に
  届いたままになる。
- Cloud NAT と AWS の NAT ゲートウェイは転送して数えるだけで、ホスト名で判定も記録もしない。

## 決定

### 1. fence はネットワークに置く。log-only でも常にオン

Workspace の通信は 1 つの egress ゲートウェイを通って出る。ほかに外へ出る経路はない。プロキシ変数を
すべて無視するプロセスでも、ゲートウェイを通るか、通れずに止まる。そのための Workspace 側の設定は要らず、
`http_proxy` 系の変数は役目を外れる。

ただし変数は残す。ゲートウェイは明示プロキシとしても同じ変数を受ける（決定 2）。プロキシに対応した
クライアントは `CONNECT` でホスト名を自分から名乗るので、Encrypted Client Hello が SNI の読み取りに
残す穴をふさげる。#1056（認証情報の注入）が置かれるのも、明示プロキシ側になる。

### 2. ゲートウェイは自前のプロキシではなく既製のソフトウェア

第一候補は intercept モードの **Squid**。443 を `https_port … intercept ssl-bump` に redirect し、
step 1 の `ssl_bump peek` で TLS Client Hello の SNI を読む。許可された名前は `splice`（復号せず
トンネル）、拒否された名前は `terminate`。80 番は、`Host` ヘッダを見る intercept の `http_port` を通す。
TLS は復号しないので、証明書をピン留めするツールや `SSL_CERT_FILE` に影響しない。

同じ Squid が明示的な forward proxy（`http_port`。`AF_EGRESS_PROXY_ADDR` が指すアドレス）としても
待ち受けるので、プロキシに対応した通信も無視する通信も、1 つの許可リスト、1 つの判定、1 つのログを
共有する。1 つの Squid が 2 種類のリスナーを意図どおり同時に動かせるかは**試していない**。

同じホストの `nftables` が、Squid の見えない部分を受け持つ。それ以外の TCP と UDP は既定でログに取って
落とす。QUIC（UDP 443）は落として TCP にフォールバックさせ、その試行はログに残す。

### 3. log-only と enforce の意味

- **log-only：** Squid は全部の名前を splice し、本来下したはずの判定をつけてログに取る。記録できない
  もの（非 HTTP や、名前のないアドレスへの直接接続）は fence が止める。止めたこともログに残す。
- **enforce：** 許可リスト外の名前は terminate する。

いまのドキュメントは log-only を「ブロックしない」と書いている。これは直す（「結果」を参照）。

### 4. ポリシーと記録は CP が持ち、ゲートウェイ上の小さなエージェントがつなぐ

許可リスト、提案と承認の流れ、Admin の Egress タブは今のまま。ゲートウェイ上の小さなエージェントが
(a) 有効なポリシーを Squid の許可ファイルにして reload し、(b) ログを**ファイルのオフセットから**既存の
ingest エンドポイントに送る。POST が失敗しても捨てずに再送できる。イベントには送信元アドレスを足し、
CP が帰属できるようにする（「未決事項」を参照）。全ターゲットがゲートウェイを使うようになったら、
`control-plane egress-proxy` は廃止する。

### 5. ターゲットごと

| ターゲット | ゲートウェイ | fence |
|---|---|---|
| ecs、ecs-ec2 | 台数 1 の Auto Scaling グループの EC2。NAT ゲートウェイの代わりにプライベートルートテーブルの既定ルートになる。固定した Elastic IP を引き継ぐ | Workspace のセキュリティグループは、ゲートウェイ・CP・AWS のエンドポイントだけを通す |
| GKE | **未決**（下記） | **未決** |
| compose（docker） | **未決**（下記） | **未決** |

## 却下

- **プロキシ変数だけが経路になる現状**：迂回できるので、記録されない通信が残る。
- **docker の internal ネットワークで CP がコンテナ IP から届く案**、**マスカレードを切る案**、
  **ホストの iptables だけの案**：1 つ目は CP の接続方式を大きく変える上に、結局ゲートウェイが要る。
  2 つ目は fence にならない。3 つ目はホストの root が要り、INPUT・DNS・IPv6 を覆えない。ネットワークが
  経路として通すゲートウェイが、この 3 つの代わりになる。
- **クラウドの NAT ゲートウェイだけ**（AWS、GCP）：数えて転送するだけで、ホスト名では判定も記録もできない。
- **AWS Network Firewall**：SNI で判定してログも出せるマネージドの代替。土台にしない理由は、AWS 専用で、
  エンドポイント時間と GB 単位で課金され、GKE と compose には何も与えないから。AWS のゲートウェイの
  差し替え候補としては残る。
- **VyOS のようなルーター機器**：全ターゲットで 1 つの設計にできるが、名前による規則は DNS で引いた
  アドレスに落ちる。ルーターを同梱して運用する負担は、Squid 1 台より大きい。
- **自前の透過プロキシ**（Client Hello を自分で読む）：Squid がすでにやっている仕事。
- **`control-plane egress-proxy` と Squid を恒久的に並べて動かす**：1 つの許可リストに対して、食い違い
  うる判定経路が 2 本になる。自前のプロキシは、ターゲットがゲートウェイに移るまでだけ残す（ゲートウェイが
  未決の docker と GKE は、それまで使い続ける）。
- **エージェントごとの OS サンドボックス**（#1067）：Workspace コンテナの中では使えない。

## 結果

- **ゲートウェイは全接続の経路上に載る。** 1 台は単一障害点になる。Auto Scaling グループが置き換え、
  ルートと Elastic IP が置き換え後に追従しなければならない。スループットはインスタンスタイプで頭打ちになる。
  マネージドの NAT ゲートウェイにはなかった運用が増える。
- **識別情報を持たない Workspace の通信は、アドレスでしか帰属できない。** 「未決事項」を参照。
- **名前はクライアントの申告どおり。** SNI はクライアントの主張で、悪意あるプロセスは許可された名前を
  名乗って別のアドレスに接続できる（ドメインフロンティング）。記録には申告された名前と宛先アドレスが
  残る。Squid のホスト検証で狭められるかもしれないが、ここでは評価していない。
- **SNI がなければ名前もない。** Encrypted Client Hello と素のアドレスへの接続は、使える名前を持たない。
  アドレスで記録し、enforce では許可しない。
- **非 HTTP は通らなくなる。** SSH 経由の git と、Workspace が選んだリゾルバへの直接 DNS。どちらも
  フォローアップ。
- **実装時に直すドキュメント：** `guide/operate/04-secure.md`（「Operating egress control」。log-only は
  もう「何もブロックしない」ではなく、「まだ作っていない」の注記も直す）、`docs/build/07-security.md` §7.8、
  `docs/build/09-deploy.md`（変数を受け取るのは docker と native だけと書いているが、Kubernetes も受け取る）、
  `docs/build/03-control-plane.md`、およびそれぞれの日本語版。
- **ゲートウェイを出す前に、SSM ログインを実際のアカウントで確かめる。** 固定した session-manager
  プラグインは `HTTPS_PROXY` を尊重する（読んだだけで、動かしてはいない）。ゲートウェイなら変数は
  まったく要らないが、SSO の開始 URL のホスト（通常は `awsapps.com` 配下）は `defaultEgressAllowlist` に
  ない。

## 未決事項

1. **GKE。** Pod の通信はノードから出る。Workspace のノードプールを転送用 VM 経由にするには、ネットワーク
   タグで絞ったルートが要り、CP 自身の Pod が巻き込まれてはならない。GKE 自前の FQDN 対応ネットワーク
   ポリシーや Cloud NGFW が代替になる。評価していない。
2. **compose。** CP がエージェントに届く方式を変えずに、ユーザー別ネットワークが経由するゲートウェイの
   コンテナか VM。試せるホストが要る。
3. **帰属。** Squid は送信元アドレスをログに取る。CP が Workspace を特定するには、起動時に各 Workspace の
   アドレスを記録しなければならない。ecs-ec2 ではタスクの ENI アドレスが取れるが、ほかは設計していない。
4. **非 HTTP：** 名前で指定したホストへの SSH git を通すかどうか。直接 DNS をどう記録するか
   （AWS では Route 53 Resolver のクエリログ）。
5. **pause と resume。** `deploy/aws/ecs/pause.sh` は時間課金を避けるために NAT ゲートウェイを削除する
   （#2386）。ゲートウェイのインスタンスにも同じ扱いが要り、resume は Workspace が起動する前にルートと
   Elastic IP を戻さなければならない。
6. **既存のデプロイ：** 既定ルートを NAT ゲートウェイからゲートウェイへ移すのは、稼働中の VPC への変更で、
   ロールバックの手順が要る。
7. **導入組織が許可リストに入れる出口アドレス。** 今は NAT ゲートウェイに付いた、保持された 1 つの
   Elastic IP（`NatEip`、`DeletionPolicy: Retain`）。`NatEipAllocationId` は新規 stand-up 専用で、稼働中の
   スタックに渡すと CloudFormation が NAT ゲートウェイを置換する（デプロイ済みのスタックに
   `--no-execute-changeset` で測った）。ゲートウェイのインスタンスは同じアドレスを引き継ぐ必要があるが、
   NAT ゲートウェイが持っている間はインスタンスに付けられない。だから切り替えには、計画しない限り
   egress の空白が生じる（切り替え用の 2 つ目のアドレスか、短いメンテナンス時間帯）。
8. **ゲートウェイ 1 台は 1 つの Availability Zone。** NAT ゲートウェイはすでに単一 AZ で、台数 1 の
   Auto Scaling グループのインスタンスもそれを引き継ぐ。ゾーンごとにゲートウェイを置くと、導入組織が
   許可リストに入れるアドレスが 2 つになる。アドレスを配る前に決めておく必要がある。

## 備考

- Squid `ssl_bump`：https://www.squid-cache.org/Doc/config/ssl_bump/（peek・splice・terminate。復号
  せずに SNI を読む。intercept のリスナー）。
- AWS Security Blog「How to add DNS filtering to your NAT instance with Squid」。
- `peek`/`splice` による許可リスト形式（公開されている例は拒否リスト）、および QUIC と Encrypted Client
  Hello の挙動は**測っていない**。
