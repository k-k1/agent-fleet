---
audience: "認証・暗号・隔離・監査・egress に触れる人"
source_of_truth: "コード（本書は境界と設計意図）"
updated: "2026-10"
---

# 07. セキュリティ — 脅威モデル・認証・暗号・監査

[English](07-security.md) | 日本語

## 7.1 脅威モデルと信頼境界

各 Workspace では CLI エージェントが**任意コードを実行**する（承認確認のスキップ運用を含む）。
「ユーザーのセッションが untrusted コードを動かす」前提で境界を設計する。守るのは
**他ユーザーのデータ / CP・ホスト基盤 / シークレット / 情報持ち出し**。

この「承認を全部スキップして起動する」は **2026-08 以降は既定であって固定ではない**——利用者が
kind 毎／セッション毎にオフにできる（[decisions/0056](../decisions/0056-tool-permission-choice.ja.md)。
どの kind が選べるかは [ref/agents](../../guide/ref/agents.ja.md)）。
ただし**境界の設計は変えない**: オフにできるのは一部の kind だけで、TUI 内でモードを戻すこともでき、
CLI 自身の設定経路まで塞いではいない。つまり承認確認は**事故を減らす手当**であって隔離境界では
なく、依然として Workspace の境界が唯一の砦である。

```
信頼度 低 ┌──────────────────────────────┐
          │ Workspace の内部              │ ← 任意コード実行を許容する領域
          └──────────────┬───────────────┘
                         │ 主要な隔離境界
信頼度 高 ┌──────────────▼───────────────┐
          │ Control Plane / ホスト / クラウド │ ← Workspace から侵害されてはならない
          └──────────────────────────────┘
```

**前提の限界（正直に明記）**: 1 デプロイ内では CP が全 Workspace の鍵を握るため、
**CP/ホストが侵害されるとそのデプロイ内の分離は一括で破れる**。

- `docker` では CP が Docker ソケット（ホスト root 相当）を持つ。
- `ecs` / `ecs-ec2` では CP のタスクロール（`deploy/aws/ecs/cfn/20-platform.yaml` の `CpTaskRole`）が、
  Workspace のサービスの作成・削除、全 Workspace の `AGENT_TOKEN` と DEK を載せた `/af-ws/` 配下の
  SSM パラメータの書き込み、home ボリュームの付け替え、`ssm:SendCommand` によるスロット上での
  シェル実行、（エンジンのスタックがあれば）GPU インスタンスの購入を行える。`SendCommand` は
  `AWS-RunShellScript` ドキュメントで、このプールの `af-pool` と `af-role=slot` のタグを持つ
  インスタンスに対してだけ許される（[#1182](https://github.com/k-k1/agent-fleet/issues/1182)）。
  そしてこのロールは既存のインスタンスをその集合へ移せない。タグの書き込みにも柵がある
  （`Ec2Tag*` の statement）。作成時のタグ付けは自分の `RunInstances` / `CreateFleet` /
  `CreateVolume` / `CreateSnapshot` 経由で、このプールの `af-pool` を付ける場合に限る。作成後は、
  すでにこのプールの `af-pool` を持つリソースに限り、`af-pool` キー自体には触れられず、`af-role` は
  `quarantined`（インスタンス）か `golden` / `golden-rejected`（スナップショット）にしか変えられない
  （`slot` へは不可）。`AF-ROLE` のような大文字小文字違いもそのキーとして扱う。したがって CP の不具合でも
  侵害された CP でも、自分のプールへ自分で起動したインスタンス以外へ `ssm:SendCommand` を送れない。アカウント内のほかの SSM 管理下の機械、
  別デプロイのスロット、このデプロイのエンジン機は宛先にならない
  （[#1419](https://github.com/k-k1/agent-fleet/issues/1419)）。これが縛るのは直接のシェルだけだ。
  `Ec2SlotPool` の残り（停止・起動・切り離し・付け替え・終了など）には柵が無く、付けたボリュームは
  読み書きできる。侵害された CP は自分のスロットを起動し、アカウント内の任意のボリュームを付けて
  読み書きできる。ほかのインスタンスを停止して外したルートボリュームも対象で、タグに触れずに、
  そのインスタンスが次に起動したとき動くコードを仕込める。
- どのターゲットでも CP が DEK を unwrap して平文で注入する（§7.6）。

会社間は別デプロイゆえ波及しない——これが提供モデルの強み（[decisions/0001](../decisions/0001-self-host-vs-saas.ja.md)）。
**`ecs` / `ecs-ec2` では、デプロイごとに AWS アカウントを分けている場合に限る。** `CpTaskRole` の範囲は
デプロイではなくアカウントだ。`EcsDrive`・`Ec2SlotPool`・`EcsContainerInstances` は条件なしの
`Resource: "*"` で、`SsmWorkspaceParams` の `parameter/af-ws/*` はアカウント全体で 1 つの接頭辞
（パスにデプロイを含まない）。したがって侵害された CP は、同じアカウントの別デプロイのサービスを
更新・削除し、そのインスタンスとボリュームを停止・終了・スナップショットし、そのボリューム（home も
ルートボリュームも）を自分のスロットへ読み書き可能な形で付け、その Workspace の `AGENT_TOKEN` と DEK を
読み書きできる。できないのは、
そのデプロイのインスタンスのタグを付け替えることと、そのプールへリソースを紛れ込ませることだ。
タグの書き込みは書き手自身の `af-pool` に縛られている。
緩和候補: rootless Docker / socket-proxy / CP ロールの絞り込み。

## 7.2 隔離コントロール

各ターゲットで Workspace が**何であるか**は [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)。
この表は、あるメンバーの Workspace を他のメンバーのもの、そして CP から隔てているものである。

| 対象 | docker（既定） | ecs（Fargate） | ecs-ec2（本番） | kubernetes |
|------|----------------|----------------|-----------------|---|
| ユーザー間ファイル | そのメンバーの home を `<WS_DATA>/[<slug>/]<key>/home` から bind mount。**他ユーザーの home はマウントされない** | membership ごとの EFS アクセスポイント（`/home/<membership>`・`/claude-config/<membership>`）で root dir を固定。uid/gid は全員共通（`AF_ECS_POSIX_UID` / `_GID`） | メンバー専用の EBS ボリューム。**同時に 1 メンバーだけ**に仕えるスロットへ付け（[decisions/0045](../decisions/0045-ec2-persistent-workspace.ja.md) 決定 8）、CP が SSM でマウントする。Claude の状態と保持する dotfile は EFS アクセスポイントに残る | Workspace ごとに claim 2 本。参照するのはその Workspace の StatefulSet だけで、ストレージドライバが対応すれば `ReadWriteOncePod`。メンバーは Workspace の名前空間に Kubernetes API で触れない — そこで Pod を作れる者はどの claim でもマウントできる |
| プロセス / メモリ | 1 membership = 1 コンテナ。`--memory`（`WS_MEMORY` 既定 `1g`・Workspace ごとに上書き可）、指定時は `--cpus` | 1 タスク。Fargate はタスク間でカーネルを共有しない | スロット 1 台に 1 タスク。メモリはスロットの容量より下で頭打ち（`AF_ECS_EC2_HOST_RESERVE_MB`）。スロットのルートボリュームは前のメンバーより長生きするので `/tmp` は tmpfs | 1 Workspace = 1 Pod。CPU・メモリ・ephemeral-storage の request と limit は Workspace のサイジングから。`/tmp` はサイズ上限付きの `emptyDir`。ephemeral-storage の limit は事後の退避で効くので、ノードのディスクの余裕と disk-pressure の警報が残りを受け持つ |
| ネットワーク | Workspace ごとの network（`af-net-…`）で相互到達を遮断。Agent はホストの loopback にだけ publish | `awsvpc`: タスクごとに ENI を持ち Workspace 用 SG に入る。SG は CP の SG からの Agent ポートだけを通し、他の Workspace からは通さない。公開 IP は付けない。外向きは開いている（§7.8） | ecs と同じ。スロット自身の SG は ingress なし | NetworkPolicy（実装する CNI でだけ効く）: Agent のポートは CP の Pod からだけ。egress はクラスタ DNS、CP の Workspace 専用リスナ、私設・リンクローカル・クラスタ自身のレンジを除くインターネット。それ以外の外向きは開いている（§7.8） |
| 権限 | 非特権・`dev` で動く。Chromium の setuid サンドボックスが namespace を作れるよう **bounding set に `SYS_ADMIN` を足す**。他の setuid/setgid バイナリが残るとイメージのビルドが失敗するので、`dev` 自身は実効 capability を持たない | 特権モードなし・capability の追加なし | ecs と同じ | 名前空間に `restricted` の Pod Security Standard を強制（CP には変えられないラベル）: 非特権・非 root・能力の追加なし・ホストの名前空間なし・`hostPath` なし — Fargate の水準で、Chromium 用の `SYS_ADMIN` も無い |
| クラウドの権限 | なし | タスクロール `WsTaskRole` は**ポリシーを 1 つも持たない**。`AGENT_TOKEN` と DEK はタスク定義の `secrets`（SSM・実行ロールが読む）で届く | 同じタスクロール。スロットのインスタンスロールは ECS 登録・SSM 管理・イメージ取得・ログだけ | なし: サービスアカウントのトークンはマウントせず、名前空間とそのサービスアカウントを名指す IAM 付与は無い。GKE では Workspace を載せるプールはすべて GKE メタデータサーバを使い、`169.254.0.0/16` への egress は拒否。`AGENT_TOKEN`・DEK・発行したトークンは Workspace ごとの Secret から `envFrom` で届き、Secret は etcd 内で暗号化（GKE では Cloud KMS） |
| 機微状態の退避 | Agent の平文状態は 2nd mount（`CLAUDE_CONFIG_DIR=/var/lib/af/claude`）で**ファイルブラウザの範囲外**へ。暗号化ストアは home 据置で Agent の denylist（`fsDeny`）の内側 | 同左（イメージ・Agent は共通） | 同左 | 同じ。2 本目の claim の上 |

**`kubernetes` の列は、runbook の前提条件を満たすクラスタでだけ成り立つ**（NetworkPolicy を
強制する CNI・Secret の暗号化・private か承認済みネットワークに限った control plane・kubelet の
読み取り専用ポートの無効化）。CP からはそれが見えない
（[deploy/kubernetes/README.md](../../deploy/kubernetes/README.md)・[decisions/0106](../decisions/0106-kubernetes-runtime.ja.md) 決定 7）。
そこで CP が乗っ取られた場合に届くのは Workspace の名前空間 — すべての Workspace の Secret と、
作れるが特権にはできない Pod — で、読み取り専用のクラスタロールを通じてもそれ以上には届かない。

**`native` にはこのどれも無い。** Agent をサンドボックスしたホストのプロセスとして動かし、
コンテナ境界もメモリ上限も無いので**単一ユーザー専用**: CP は `AUTH` が `dev` 以外なら起動を拒む
（`AF_RUNTIME=native is single-user only`）。

**限界**: Workspace 内の shell は Agent と同じ uid で動くので、本人の BYO トークン・`AF_SECRET_KEY`・
`AGENT_TOKEN` を本人のセッションから不可視にはできない（原理的に不可）。ブラウザ不可視 +
at-rest 暗号 + env 注入で実用十分とする設計判断。セッション内で動くコードにとっての意味は
[SECURITY.md](../../SECURITY.md) に書いてある。

### 7.2.1 エンジン: 到達できることがアクセス制御

デプロイ自前のモデルサーバー——llama.cpp・ComfyUI
（[decisions/0071](../decisions/0071-self-hosted-inference-engines.ja.md)）——はどの Workspace の
外にもある。**Workspace がそれに届くのは CP のゲートウェイ経由だけ**——`/engine/{key}/v1/*` と、
走っているエンジンが起動したときの窓を Agent が読む `GET /engine/{key}/props`（`engine_gateway.go`）:

- llama-server は変更系のエンドポイントを持ち、ComfyUI は認証を一切持たないので、
  **ポートに誰が届くかがアクセス制御**である。AWS ではエンジンの SG（`60-engines.yaml` の
  `EngineSg`）が CP の SG からのエンジンポートだけを通し、Workspace は載っていない。
  llama-server には SSM の API キー（`LlmApiKeySsmParam`）も渡し、CP が上流へ付ける——2 本目の
  鍵で、読めなくても致命にしないのは意図どおり（`readEngineAPIKey`）。
- ゲートウェイはログインの門から除外され、自前で認証する。Workspace は membership ごとの
  発行トークン（`AF_ENGINE_ISSUE_TOKEN`・`afei_…`）を持ち、`POST /internal/engine/token` で
  membership とエンジンのキー 1 つ、それに呼び出し側が指定したときは 1 セッション（lcpp の
  セッションやターミナルの opencode セッション）に縛られたトークン（`afe_…`・有効 30 日）と交換する。
  指定しない経路（opencode の共有 Managed デーモン・画像生成・起動時の問い合わせ）では
  Workspace 全体が単位になる。毎回、membership が生きている
  ことと、テナントがそのエンジンを使えることを確かめ直す。
- `docker` / `native` のエンジンは運用者がネットワーク上で既に動かしているもの
  （[decisions/0076](../decisions/0076-external-image-engine-on-lan.ja.md)）。その bearer は
  `AF_ENGINE_API_KEY_<KEY>` から来て、Workspace が直接届くかどうかは運用者のネットワーク次第。
  メンバー自身の llama.cpp 接続は Workspace が自分で繋ぎ、ゲートウェイを通らない。
- `ecs-ec2` では **CP が GPU インスタンスを自分で買う**
  （[decisions/0077](../decisions/0077-engine-boxes-bought-by-cp.ja.md)）。それで CP のロールに
  増えるのは `60-engines.yaml` の `CpIngestPolicy` だけ: `ec2:CreateFleet` / `DescribeFleets` /
  `DeleteFleets`、エンジンのインスタンスロールの `iam:PassRole`、Spot と EC2 Fleet の
  サービスリンクロール、モデル取り込み用に取り込みタスク定義への `ecs:RunTask` と
  Hugging Face・Civitai のトークン secret への書き込み。エンジンのインスタンスロールは ECS 登録と
  SSM 管理、エンジンのタスクロールはモデルバケットの読み取り。

VOICEVOX はこのゲートウェイの後ろではない: Console が CP に頼み（`/api/tts/*`・通常のログインの
内側）、CP がエンジンを呼ぶ。エンジンの SG は CP だけを通す。

## 7.3 L1 Console 認証（AUTH 3 モード）

`AUTH` env で分岐。いずれも解決した email を sanitize（小文字化・それ以外の文字の連なりを `-`・
40 字上限）して identity のキーにする。

| モード | 仕組み | 用途 |
|--------|--------|------|
| `oauth` | **CP 自身が OIDC クライアント**（§7.3.1）。`/login`・`/login/{slug}`・`/oauth2/{login,callback,logout,link}` を CP が所有し、成功で署名 cookie（HMAC-SHA256・`AF_COOKIE_SECRET`・HttpOnly・SameSite=Lax・公開 URL が HTTPS なら Secure・TTL `AF_SESSION_TTL` 既定 168h）を発行。compose と AWS のテンプレートが設定するのはこれ | セルフホスト本命。エッジで HTTPS 前提 |
| `proxy` | 上流ゲートウェイの email ヘッダ（`AUTH_EMAIL_HEADER`・既定 `X-Forwarded-Email`）を信頼。**ヘッダ欠落は 401**（フォールバック無し）。ヘッダは消さないので、CP にはそのゲートウェイ経由でしか届かないこと | oauth2-proxy など既存ゲートの流用。**SAML IdP の正式な答えもこれ** — ブリッジする（[decisions/0043](../decisions/0043-login-idp.ja.md)）。AWS のテンプレートには無い |
| `dev` | 固定ユーザー（`DEV_USER`・既定 `dev`）で、デプロイ管理者になる | ローカル開発のみ。**`AUTH` 未設定時の既定**。`native` はこれを要求する |

**authGate の要点**（`oauth` モード）:

- 全リクエストを検査し、**受信した識別ヘッダを必ず削除**してから検証済みのものを自ら注入する
  （エッジがヘッダ素通しでも成りすまし不可）。
- 除外パスは 1 箇所で宣言する（`routes.go` と各ルート群の横の `exemptExact` / `exemptPrefix`）:
  ログイン導線・死活と readiness・ブランド資産と web manifest（監視や未認証のページは
  サインインできない）、自前認証を持つ面——`/mcp`（Bearer PAT）・`/git/`（git トークンの Basic）・
  `/engine/`（§7.2.1）・`/internal/`（用途ごとの Bearer トークン: egress・メモ・スケジュール・
  MCP サーバー・docs・AWS プロファイル・git OAuth の更新・エンジントークン）——と旧パスの
  リダイレクト。
- 許可リスト 3 系統の併用可: email（`AF_OAUTH_ALLOWED_EMAILS`）/ ドメイン
  （`AF_OAUTH_ALLOWED_DOMAINS`）/ **判定のたびに読み直すファイル（`AF_OAUTH_ALLOWED_EMAILS_FILE`）
  ＝追加は再起動不要**。provider ごとのリスト（`AF_OIDC_<ID>_ALLOWED_{EMAILS,DOMAINS}`）は、
  その provider ではデプロイ共通リストの**代わりに**使われる。
- **入口の判定は email 軸の「和」**（[decisions/0043](../decisions/0043-login-idp.ja.md)）:

  ```
  ( provider 固有リスト | デプロイ共通リスト )  ∪  ( テナントの自動参加ドメイン | membership 保有 )
  ```

  後半が DB 由来（30 秒キャッシュ・membership やテナントを書くたびに破棄）。**招待された人は
  環境の許可リストに載っていなくても入口を通る**ので、招待運用のデプロイは名簿を 1 箇所に
  寄せられる。**すべて空なら全拒否（fail-closed）は維持**。和を取るのは **email 軸の中だけ**で、
  種類の違う判定（GitHub の org メンバーシップなど）は AND のまま——さもないと membership を
  持つだけで迂回できる。
- **判定はログイン時だけでなく毎リクエスト**。許可リストから消す／membership を無効化すると、
  cookie の期限を待たずに次のリクエストで締め出される。cookie は stateless なので**個別失効は無く**、
  全セッションを即時に切る唯一の手段は cookie secret のローテーション。
- **テナントの門は authGate に置かない。** authGate はテナントを知らない（解決はその先）ので、
  テナント規則を持ち込むと「どのテナントで判定するか」が決まらない。テナント側の判定は解決の
  途中（`resolveFull` / `resolveMembership` がテナントの許可 provider とセッションのものを突合）で
  行い、外れたら **`provider_required`** を返して Console から再サインインへ誘導する
  （403 で終わらせない）。
- **デプロイ管理者リスト（`SUPER_ADMIN_EMAILS`）は起動時に 1 度だけ読み、それが唯一の正。**
  役割のヒントは upgrade-only なので、**降格は起動時の一括処理**（`DemoteSuperAdmins`。email が
  空の identity は名指せないので対象外）。ログイン時同期にしないのは、**退職者は二度とログイン
  しない**ため。

### 7.3.1 ログイン IdP

Google 固定ではなく**汎用 OIDC クライアント 1 本**で、Entra ID / Okta / Keycloak / Auth0 /
Cognito / GitLab が設定だけで載る（`AF_OIDC_PROVIDERS` ＋ `AF_OIDC_<ID>_*`）。Google も同実装の
1 インスタンスで、**env 名（`GOOGLE_OAUTH_*`）は据え置き**＝既存デプロイは無変更。

- ログイン画面には有効な provider の数だけボタンが出る。
- **redirect URI は 1 本のまま**（`/oauth2/callback`）。どの provider かは署名済み state cookie で
  運び、**設定済みの集合と突合してから**分岐する。セッション cookie は provider と subject を
  持ち、毎リクエストの判定はその provider に問う。
- **信頼の根拠に既定値を置かない**（`AF_OIDC_<ID>_TRUST`）。IdP が email を検証済みと言う
  （`email_verified`）か、issuer が単一テナントに固定済み（`issuer`）か。**Entra ID は検証フラグを
  出さない**のでこちら。宣言の無い provider は起動時に無効化される。
- **マルチテナントの issuer（`common` / `organizations` / `consumers`）でテナント制限
  （`AF_OIDC_<ID>_ALLOWED_TIDS`）が空なら起動を止める。** 許すと「Microsoft アカウントを持つ
  全人類」が入口に立ち、個人アカウントは email を付け替えられるので email 許可リストが無意味になる。
- **1 つの IdP の設定ミスで全員を締め出さない**: 設定不足の provider は無効化＋警告で、
  **有効な provider がゼロのときだけ fatal**。
- **id token の署名検証は行わない**（意図どおり）。認可コードフロー・client secret 付き・
  トークンエンドポイントから TLS 直受けのため。テナント id はそのペイロードから読む。
  **フロントチャネルの経路を足すなら JWKS 検証が必須**。JWT ライブラリ依存はゼロ。

**GitHub だけは専用アダプタ**（OIDC ではない）。許可は**独立した 2 つの門の AND**:

1. **org メンバーシップ**（必須・`AF_GITHUB_ALLOWED_ORGS`）。org が未設定なら provider ごと
   無効化する——**この env が GitHub ログインを有効にする合図**で、許可を与えているのが org
   メンバーシップそのものだから。client id だけでは何も有効にならない。
2. **email 許可リスト**（`AF_GITHUB_ALLOWED_{EMAILS,DOMAINS}`）→ 無ければデプロイ共通 →
   どちらも無ければ org が許可リストそのもの。上の DB 由来の項は**この門にだけ**足される。
   email はアカウントの **primary かつ verified** のアドレス、subject は**数値 id**
   （ログイン名は変えられる）。

毎リクエスト再判定は API 呼び出しになるので subject ごとにキャッシュし
（`AF_GITHUB_MEMBERSHIP_TTL`・既定 10 分）、GitHub 到達不能時は**最後の肯定結果を猶予期間だけ
延命**（`AF_GITHUB_MEMBERSHIP_GRACE`・既定 1 時間）して、超えたら拒否する。

**access token はプロセス内メモリにしか置かない**（cookie に載せれば XSS で漏れる）。
したがって **CP 再起動で判定材料が消える**。その人は org のメンバーのままなので、答えは
**「再ログイン」（`reauth`・API には 401）であって「禁止」ではない**——fail-closed は保ったまま、
事実と違うことを言わない。

**テナント定義のサインイン方法**（`tenant_idp`）で、子会社が自前の IdP——OIDC か GitHub の
org——を持ち込める。env の provider と決定的に違うのは**誰が有効化するか**で、そこが安全性の
全体を支えている:

- **書くのはテナント管理者、有効にできるのはデプロイ管理者だけ。** IdP の登録は「誰であるかを
  宣言する」権限で、identity は email をキーにデプロイ全体で 1 つなので、自分の IdP を有効化
  できる人は情シスのアドレスを名乗るトークンを発行できる。**issuer の固定は防波堤にならない
  （issuer が攻撃者自身）。** 悪意が無くても、セルフサインアップ可能なテナントを善意で登録した
  瞬間にデプロイ全体が開く。
- 承認前は**ボタンが出ず、callback もセッションも通らない**。issuer・client id・信頼の根拠・
  種類・結合に使う claim の変更、許可ドメイン・テナント id・org の**拡大**で承認待ちへ戻る。
  テナント管理者は停止も承認待ちへの差し戻しもでき、状態の変更はすべて監査に残る（`tenant_idp.*`）。
- **provider id は名前空間を分ける**（`t:<tenant-slug>:<name>`）。テナントが env の provider を
  上書きする行を作れない。
- **デプロイ役割は取れない。** identity の upsert は決して降格しないので、ここを抜かれると不正な
  provider を消しても役割が残る。
- **email 一致で既存の identity へ結合しない。** 一度もサインインされていない identity
  （招待の placeholder）だけを claim でき、ログイン実績のあるアドレスは拒否する。
- **入口の門は行の許可ドメイン（必須）だけ**で、デプロイ共通リストにも他テナントの名簿にも
  フォールバックしない。1 ドメインは 1 テナントに属し、これがその issuer の名乗ってよい
  アドレスの範囲を縛る。マルチテナントの issuer では許可テナント id も必須。
- **そのセッションは自テナントにしか入れない。**
- client secret は **テナント鍵で封印して DB に置き**（§7.6 の custodian）、UI へは返さず、
  **復号不能は空にせず明示エラー**にする。秘密がデータディレクトリに入るので、**マスター鍵を
  その外に置くルールの重みは増す**（§7.6）。

サインイン後の認可は [05 §5.4](05-api.ja.md): 自分のリソースのみ + membership 検証、admin API は
役割で絞る（デプロイ管理者はデプロイ全体、テナント管理者は自テナント）。役割は identity と
membership の 2 段（[06 §6.2](06-data.ja.md)）。

### 7.3.2 テナントの接続元制限

「誰か」の次に「どこから」を見る門。テナントごとの `allowed_cidrs`
（[decisions/0047](../decisions/0047-tenant-network-restriction.ja.md)）。

⚠️ **これはネットワーク防御ではない。** 要求は CP に届き、セッションが検証された**あとで**
拒否される。認証前の脆弱性・DoS・探索には効かない（そこは ingress の規則——AWS では
`AlbIngressCidr`——と WAF）。守れるのは「資格情報を持った人が、許されていない場所から
データに触る」だけである。

- **送信元アドレスは申告したプロキシのホップ数で決まる**（`AF_TRUSTED_PROXY_HOPS`・既定 0、
  AWS のテンプレートは 1）。0 = ソケットの相手、N = forwarded-for ヘッダの**右から N 番目**。
  このヘッダは誰でも先頭に足せるが、信頼するホップは**右に**追記するので、右から数える限り偽装が
  効かない。**左端を読む実装だけが危険**。読むのは最外周のミドルウェア 1 箇所だけ
  （authGate が識別ヘッダを 1 箇所で消すのと同じ理由）。
- **Workspace が呼ぶ面は対象外**——`/mcp`・`/git/`・`/engine/`・`/internal/`。送信元は本人の
  Workspace であり、人の所在を表さない。入れると自分の Workspace からの呼び出しを全部塞ぐ。
- **締め出しの逃げ道**: デプロイ管理者は対象外。編集者の現在のアドレスを締め出す保存は拒否
  （`would_lock_out`）。プロキシのホップ未申告なのに forwarded-for が届いた場合
  （`proxy_not_configured`）と、チェーンが申告より短い場合（`client_ip_unknown`）も保存を拒否する。

## 7.4 L2 エージェント認証との分離

L2（エージェントを誰として動かすか）はユーザー本人のサインインで、CP は状態の表示と接続の
導線以外に関与しない（[08](08-integrations.ja.md)）。**Workspace を跨いだ認証情報の共有は設計上
禁止**（home 分離がそのまま境界）。メンバーのトークンが CP を通る唯一の場所は git OAuth の更新
（[decisions/0052](../decisions/0052-tenant-git-oauth.ja.md)）: Workspace が refresh token を
`/internal/git-oauth/{bitbucket,jira}/refresh` へ送り、CP がテナントの OAuth アプリの secret を
足して——これで secret を全メンバーのストアへ複写せずに済む——結果を返す。トークンは保存しない。

## 7.5 CP ↔ Agent 認証

- Workspace ごとのトークン（`AGENT_TOKEN`）は Workspace のレコード作成時に発行して永続化する
  （[06](06-data.ja.md)）。Agent へは、`docker run` では 0600 の env ファイル（コマンドラインには
  出さない）、`ecs` / `ecs-ec2` ではタスク定義の `secrets` 経由の SSM SecureString、`native` では
  プロセスの環境変数で届く。レコードの無いコンテナが既にあれば、作り直さず inspect でその
  トークンを採用する。
- すべての中継——REST・SSE・WebSocket・preview・CP 自身から Agent への呼び出し——が Bearer として
  付け、Agent の `RequireToken` が `/healthz` 以外を**定数時間比較**で検証する。未設定なら門は
  開く（開発用のみ）。
- ネットワーク分離（§7.2）との多層防御で、Agent を守る相手は**ネットワークであって自分の
  Workspace ではない**: エージェントのセッションも同じトークンを持つ（af MCP サーバーが要る）ので、
  Agent のどのルートもセッション内のコードから呼べる。人の押下でだけ始まるべき機能は、ルート認証に
  頼らず結果をその押下に結び付ける（[decisions/0102](../decisions/0102-aws-login-through-the-console.ja.md)）。

## 7.6 シークレット管理と封筒暗号

**原則: 秘密はユーザー領域に閉じ、CP はメンバーの資格情報の平文を保持・解釈しない。ログに秘密を
出さない。**

| シークレット | 保管 | 露出範囲 |
|-------------|------|----------|
| git 資格情報とプロバイダのキー（GitHub・Bitbucket・Jira・opencode のプロバイダ・MCP の秘密など） | Workspace home の**暗号化ストア** `~/.config/agent-fleet/secrets.enc`（AES-256-GCM・0600） | 当該ユーザーのみ。git は資格情報ヘルパ（`workspace-agent cred`）が都度復号して stdout に出したものを読み、他のキーは必要なプロセスへ注入する。**平文ファイルを作らない** |
| エージェント自身の資格情報ファイル | エージェントの設定ディレクトリ（home 外・ブラウザの範囲外、§7.2） | 当該ユーザーのみ |
| システム秘密——ログインの client secret・マスター鍵（`AF_MASTER_KEY`）・cookie secret | git 管理外の env ファイル。AWS では `/af-cp/` 配下の SSM SecureString（DB パスワードは RDS 管理の secret） | CP のみ。**マスター鍵はデータ領域の外で保管し、バックアップに含めない**＝失えば crypto-shred |
| テナントの秘密——テナント IdP や git OAuth アプリの client secret、MCP サーバーの資格情報 | DB（テナント鍵で封印） | CP のみ。UI へは返さない |
| GitHub でサインイン中の人の access token | **プロセス内メモリのみ** | CP のみ。再起動で消え、その人は再ログインを求められる |
| PAT | DB に SHA-256 ハッシュのみ | 平文は発行時 1 回だけ表示 |

⚠️ **`AF_MASTER_KEY` が無いと保管庫は平文になる。** マスター鍵が無ければ DEK も無く、agent は同じ
保管庫を暗号化せずに `secrets.json` として書く。`AUTH=dev` ではそれが意図どおり。それ以外の `AUTH` では
CP は起動するが、起動時に `WARNING` をログに出し、super_admin の `GET /api/admin/tenants` の
`deployment_warnings` に `plaintext_secrets` を載せる。Console の管理モーダルはこれを帯で表示する
（`master_key_guard.go`）。拒否でなく警告にしたのは、鍵なしで動いている既存の配備を更新で止めないため。
後から鍵を設定しても透過ではない。動いているワークスペースは起動時の環境のままなので、停止して起動し直す
まで `AF_SECRET_KEY` を持たない（動作中のコンテナへの `Start` は状態を確かめるだけ）。起動し直した後の
agent は `secrets.enc` を読み、`secrets.json` を移行しないので、メンバーは保存していた認証情報を
つなぎ直すことになる。古い `secrets.json` は消すまで各ホームとすべての
バックアップに残るので、その認証情報は漏れたものとして扱い、ローテーションする。

**封筒暗号 + custodian 抽象**（[decisions/0005](../decisions/0005-envelope-custodian.ja.md)）:

- Workspace ごとの DEK をテナントごとの KEK で wrap して保存（`wrapped_dek`）。CP が Workspace
  起動時に unwrap して `AF_SECRET_KEY` として注入する。**Agent は暗号方式に無関心。**
- custodian は interface（`KeyCustodian`）。現実装（`localCustodian`）は KEK をマスター鍵から
  導出する。同じ custodian が上のテナントの秘密と、セッションの引き継ぎ・共有のペイロードも封印する。
- ⚠️ **正直な限界**: KEK がマスター鍵由来で——DEK 自体もマスター鍵とユーザーキーから導出する
  （封筒保存より前に書かれたストアを開けるため）——実効強度は単一のマスター鍵と同等。
  **真のテナント単位の crypto-shred は Vault / KMS の custodian を採ったとき**で、📋——継ぎ目が
  あるだけ。

## 7.7 監査

- 器は監査ログ（`audit_log`・[06](06-data.ja.md)）。actor の種類は user / admin / mcp / system、
  それに Claude 自身のツール呼び出しを転写から取り込むオプトイン（`AF_CLAUDE_AUDIT_INTERVAL`・
  既定オフ）の `claude`。
- **記録するのは変更・破壊操作だけ**で、読み取りの例外が 1 つ: メンバーのメモリを環境の外へ
  持ち出す唯一の経路 `GET /api/agents/memory/export`（対象は形式）。**ターミナルの生ストリームは
  保存しない**（秘密が混ざる）。proxy 層は対象を URL から取るが、`PUT /api/fs/file` だけは検証済みの
  JSON 本文の path を対象にする。**ファイルの内容は記録しない**。
- 書き込み点: CP の proxy 層、admin / テナントの API、MCP の書き込みツール（トークンの id を
  記録し、**役割は呼び出し時に live で解決**）。
- **取り消せない管理操作は依頼を先に記録する**（`store.BeginIrreversible`）: ホームのクリーン、
  ホームのバックアップ削除、ワークスペースの破棄、メンバーシップの除外と削除、テナントの削除、
  プールのスロット終了、エンジンのモデルの purge、テナントのサインイン方法（IdP）の削除、
  内蔵 git のリポジトリの削除と改名は、実行前に `<action>.requested` を書き、
  **書けなければ `503 audit_unavailable` で断る**。終わったら `<action>` に結果と応答したステータスを
  書く。結果の書き込みの失敗はログに出して返さない（操作は済んでおり、依頼の行が誰の依頼かを残す）。
  それ以外の監査の書き込みは、これまでどおり事後のベストエフォート。
- 読み取り: `GET /api/admin/audit` と Console。テナントと役割で絞る。

## 7.8 egress 統制 🚧

実装済み:

- **forward proxy**。同じバイナリのサブコマンド（`egress-proxy`・`AF_EGRESS_LISTEN` 既定 `:3128`）。
  CONNECT / HTTP 要求のホスト名で判定し、**TLS は復号しない**。loopback・link-local（クラウドの
  メタデータのアドレスを含む）・未指定アドレスは、どのモードでも検査して拒否する設計である。
- イベントは CP へ（`POST /internal/egress`・`AF_EGRESS_TOKEN`）送って日次集計（`egress_daily`）、
  ポリシーは `GET /internal/egress/policy` で配る。
- **allowlist は版管理**（`egress_allowlist`: active / proposed / retired）+ デプロイ全体のモード
  （`egress_mode`）。**AI は提案のみ・人間が承認**（自動適用しない）。
- **enforce は proxy で遮断する**: allowlist の外は 403。
- `AF_EGRESS_PROXY_ADDR` がどのターゲットでも全 Workspace に proxy の変数（`http_proxy`・
  `https_proxy`・`no_proxy`）を注入する。

**足りないのは柵である。** Workspace の通信を proxy に強制するものが無い: `docker` に内部専用の
network は無く、AWS の SG に egress 規則も無く、proxy を動かすデプロイのテンプレートも無い。
proxy の変数を無視するプロセスはそのまま外へ出るので、**enforce はまだ Workspace を縛らない**。
段階運用が設計の核なのは変わらない——log-only で実測し、測ったものから allowlist を固め、それから
enforce へ切り替える。

## 7.9 リスクと残課題

1. **承認確認スキップの既定運用** — Workspace の境界が唯一の砦なので §7.2 を厳格に。利用者は
   オフにできる（[decisions/0056](../decisions/0056-tool-permission-choice.ja.md)）が、隔離の代わり
   にはならない（§7.1）。
2. **CP/ホスト侵害 = デプロイ内一括崩壊**（§7.1）。会社間非波及が緩和（AWS ではアカウントを分けた場合だけ）。CP の AWS ロールには
   まだ絞る余地がある（[#1182](https://github.com/k-k1/agent-fleet/issues/1182)）。
3. **長期保持するエージェント資格情報の失効・ローテーション** — 枠組みはあるが、真の失効は
   Vault / KMS 待ち（§7.6）。
4. **サプライチェーン** — Workspace イメージ同梱ツールの出所管理・定期更新（[04](04-agent.ja.md)）。
5. **egress の enforce はまだ Workspace を縛らない** — proxy は遮断するが、Workspace を proxy に
   通す柵が無い（§7.8・[#1181](https://github.com/k-k1/agent-fleet/issues/1181)）。
6. 対外的な脅威モデル・脆弱性報告窓口は [SECURITY.md](../../SECURITY.md)（英語）。
