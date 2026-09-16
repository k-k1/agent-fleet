# 0086. e2e テスト用の Postgres / MySQL を利用者ごとに 1 環境——版を固定したサーバを必要時に導入し Workspace 内で `dev` として動かす。契約は後から「利用者専用サービス」に差し替えられる形にする

[English](0086-workspace-databases-for-e2e.md) | 日本語

- Status: **提案**（2026-09-16）。実装は何も無い。
- **何を、どこで測ったか。** 2026-09-16 に Workspace コンテナ——docker ランタイムの開発配備・
  x86_64・cgroup 上限 10 GiB・8 CPU——で両方のサーバを実際に導入して起動し、「実測」節の数字は
  すべてその実行から出ている。**Fargate では何も測っていない**ので、EFS・タスクローカルディスク・
  ユーザ名前空間についての主張はここで決めず「未解決の問い」に置いた。
- 要望は 1 文である。**利用者が手間なく Postgres / MySQL 相手の e2e テストを回せるようにしたい。
  サイドカーではなく、利用者ごとに切り出された環境として。**
- 関連: [0044-workspace-sizing.ja.md](0044-workspace-sizing.ja.md)（`~` がどこに載っているか。
  データディレクトリを置くタスクローカルディスクの出どころ）/
  [0045-ec2-persistent-workspace.ja.md](0045-ec2-persistent-workspace.ja.md)（CP がすでに
  メンバーシップごとに作っている資源＝案 B が写し取る形）/
  [0047-tenant-network-restriction.ja.md](0047-tenant-network-restriction.ja.md)（新しい上流 2 つが
  入ることになる egress 許可リスト）/ [0068-debian-13-base.ja.md](0068-debian-13-base.ja.md)
  （不足する共有ライブラリ 3 本を足す先のイメージ）/
  [0071-self-hosted-inference-engines.ja.md](0071-self-hosted-inference-engines.ja.md)
  （必要時に起こしアイドルで止める形。そして**エンジン**がこの機能の置き場として不適な理由）

## 背景

### いま利用者がぶつかるもの

Workspace には root も `sudo` も Docker もデータベースサーバも無い。全コンテナに配られる運用方針は
それをそのまま書き、エージェントには諦めろと指示している。

> **No Docker / Podman**, and no database servers (`psql`, `sqlite3`, `redis-cli` absent).
> Testcontainers, `docker compose` fixtures and "just start a Postgres" do not work — run
> such tests against a service the user provides, or skip them and say plainly that you did.
> — `workspace/notes/environment.md:81`

この段落は正確であり、そして問題そのものでもある。「利用者が用意したサービスに向けて回せ」とは、
DB を使う e2e テストをしたい利用者全員がどこか別の場所でデータベースを持ち、その資格情報を
コンテナに渡すという意味であり、「飛ばして、飛ばしたと言え」とは、マイグレーションの欠陥を
捕まえたはずのテストでエージェントが止まるという意味である。

### すでに木にある前例

3 つあり、この ADR はほぼその合成である。

1. **必要時に導入する版固定インストーラ。** `workspace/agent/install_tools.go:1` ——lean な
   rootfs は chromium・Go・AWS CLI・ops MCP のバイナリを持たずに出荷され、それぞれ初回利用時に
   永続ホームへ導入される。版は `/usr/local/share/agent-fleet/versions.json` で固定し、staging
   ディレクトリへ落としてから atomic に rename するので、失敗したダウンロードが既存の導入を
   壊すことはない。利用者ごとの導入先は `~/.local/share/agent-fleet`（`install_tools.go:29`）。
2. **Console が書き、以後すべてのセッションが読み直す Workspace 単位の選択。**
   `workspace/agent/env_toolchains.go:25` が node / java / go / タイムゾーンを
   `~/.config/agent-fleet/toolchains.json` に持ち、CP が `GET|PUT /api/env/toolchains` を Agent へ
   proxy し（`control-plane/routes.go:769`）、Console が Workspace 設定で編集する。変更は
   Stop → Start 無しで次のセッション起動から効く。
3. **メンテナはすでにこれを手でやっている。** `docs/build/10-development.md` §10.4 は、展開済み
   バイナリから unix socket で Postgres を起こし、マイグレーションのテスト 3 本をそれに向けて
   回し、止めるよう開発者に指示している——その 3 本だけが「片方の方言にだけ足した」を捕まえる
   場所だからである。**この ADR は、その治具を全利用者向けの製品機能にする話である。**

### データディレクトリを置ける場所

ADR 0044 決定 3 は `~` を平均ファイルサイズで割った。資格情報・`~/repos`・tarball 形のキャッシュは
EFS に残し、`node_modules`・`target`・`.venv`・ビルドキャッシュは**タスクローカルディスク**
（`AF_WS_SCRATCH`、`workspace/af-scratch.sh`）へ逃がす。後者は速く、そして**Workspace を停止すると
消える**。テスト用のデータベースはまさに後者の形である——小さいファイルが数千、作り直しは安く、
「停止したら消える」はテスト用フィクスチャが元々望む意味論だ。

### コンテナが共有されていること

利用者 1 人の Workspace はコンテナ 1 つだが、**その中で複数のセッションが同時に動く**。だから
「利用者ごと」だけでは足りない。

- **ポートは共有である。** あるセッションのサーバが取った TCP ポートは全員にとって塞がっている。
- **セッション名はスロット名であり再利用される**——`workspace/agent/cleanup_ops.go:103` は、名前を
  鍵にしたセッション付随ファイルが次にそのスロットに入ったセッションへ現れるがゆえに存在する。
- このリポジトリ自身の Postgres テストは冒頭で `DROP SCHEMA public CASCADE` する。1 つの
  データベースを 2 セッションが向いたら、どこにもエラーを出さずに互いの作業を消す。

## 実測（2026-09-16・Workspace コンテナ・x86_64）

| | PostgreSQL 16（Zonky バイナリ） | MySQL 8.4.6（公式 `minimal` tarball） |
|---|---|---|
| 供給 | Maven Central の jar 14 MB → 展開 59 MB | `cdn.mysql.com` 63 MB(xz) → 展開 **446 MB** |
| 初回初期化 | `initdb` **2.6 s**・空データ 39 MB | `--initialize-insecure` **12.8 s**・空データ **200 MB** |
| 起動→受付 | **74 ms** | **1.8 s** |
| 常駐 | 16 MB ＋子 5 本 ≒ 45 MB（`shared_buffers=32MB`） | **226 MB**（`innodb_buffer_pool_size=64M`・`performance_schema=0`） |
| データベースを 1 つ増やす | `CREATE DATABASE` **0.63 s**。`TEMPLATE` で種入りを複製できる | drop / create の後にマイグレーション再実行 |
| アーキテクチャ | amd64 **と** arm64・16 / 17 / 18 すべて有り | `minimal` は x86_64 のみ。**arm64 は 909 MB のフル版しかない** |

1. **どちらも `dev` のまま、root も capability も無しで動く。** Postgres は `-h ''` で unix socket、
   MySQL は `--skip-networking --socket=…`。MySQL 側は `CREATE TABLE … JSON` と
   `SELECT j->>'$.a'` の往復が返ったので、上がっているだけのプロセスではなく動くサーバである。
2. **イメージに無い共有ライブラリが 3 本**: `libaio.so.1`・`libnuma.so.1`・`libncurses.so.6`——
   MySQL の tarball は 3 本ともリンクしている。上の実行は `libaio1t64`・`libnuma1`・`libncurses6`
   を Debian の `.deb` から root 無しで剥がし、`LD_LIBRARY_PATH` を向けて通した（合計 ~250 KB）。
   Postgres は何も要らなかった。
   ⚠️ 版が効く: unstable の `libncurses6` は `GLIBC_2.42` を要求してこのイメージでは落ちる。
   動くのは trixie の版（6.5）である。
3. **コンテナランタイムは、やはり置けない。** ここでは `unshare -Ur` が通る（非特権ユーザ名前空間は
   有効）が、`newuidmap` / `newgidmap` は存在しない——イメージは setuid ビットを全部剥がし、
   その結果をビルド時に assert している（`workspace/Dockerfile:642`）。したがって rootless Docker と
   Podman は不可のままであり、Testcontainers も同じである。（docs/log/62 §62.4 が 2026-08 に同じことを
   書いているが、ここでは引用せず測り直した。）
4. **どちらの上流も既定の egress 許可リストに無い。** `control-plane/egress_policy.go:18` は git ホスト・
   npm・PyPI・Go proxy・`.debian.org`・`.amazonaws.com` を通すが、`cdn.mysql.com` も
   `repo1.maven.org` も**無い**。`.debian.org` が最初から開いていることが、注 2 の `.deb` 経路を
   `enforce` 配備でも許可リスト変更ゼロで成立させている。
5. **MySQL の重さは調整不足ではなく構造である。** 常駐 226 MB は `performance_schema` を切り、
   バッファプールを縮めた**後**の値である。Postgres はその 1/5 で待機し、起動は 24 倍速い。
   2 つのエンジンを大きさの点で同じものとして扱う設計は、メモリを 5 倍間違える。
6. コンテナ自身: `memory.max` 10 GiB・8 CPU・`AF_WS_SCRATCH` 未設定（この配備は `~` をローカル
   ディスクに置く）。したがって **ADR 0044 の EFS / タスクローカルの数字はここでは測り直していない**。

## 決定

### 1. 契約は `af-db` と URL 1 本。**サーバがどこで動くかはアダプタ**

利用者とエージェントが覚えるのはちょうど 3 つ: `af-db up <engine>`・`af-db url`（**そのセッション用**の
接続 URL）・`af-db down`。この契約のどこにも「サーバはこのコンテナ内のプロセスだ」とは書かない。
P0 はローカルプロセスで実装する（決定 2）。利用者専用のリモートサービス（後述「却下」では
「今はやらない」であって「永久にやらない」ではない）は同じ 3 語の裏の 2 つ目のアダプタにできて、
配備が切り替わってもどのプロジェクトのテスト設定も変わらない。

これは `Runtime` ポートの形である（`control-plane/internal/runtime/runtime.go:12`）——1 つの
インタフェース、4 つのアダプタ、docker と ECS を区別できない呼び出し側。契約を**先に**固定する
理由は、案 B の高い部分が ECS サービスではなく、「利用者全員のテスト治具が socket パスを
直書きしていた」と後から気づくことだからである。

### 2. P0 は Workspace の中で、`dev` として、必要時に導入して動かす

サイドカーコンテナでも共有サーバでもない。理由を順に:

- **すべての配備プロファイルで成立する唯一の答えだから。** native・docker・ECS Fargate・
  ECS on EC2 が同じ日に同じ機能を得る。利用者ごとの ECS サービスは native では存在すらできない。
- **費用が要らないから。** タスクも EFS アクセスポイントも Cloud Map 名も時間課金も無い。代わりに
  払うのは Workspace 自身のクォータから出るメモリで（`resolveWorkspaceMemBytes`、
  `control-plane/workspace_lifecycle.go:534`）、それは決定 7 で見えるようにする。
- **速さが「気づかない」水準だから。** 74 ms（Postgres）。Fargate のタスクがイメージを引いて
  収束するまでの 1〜2 分と比べればよい。
- **インストーラの機構がすでにあるから。** この ADR で唯一「新しいコードではなく新しい表の行」で
  済む部分である（決定 5）。

### 3. サーバは Workspace ごとに（エンジン, 版）1 本。**データベースはセッションごとに 1 つ**

サーバは Workspace 全体で共有する——MySQL の 2 本目は 226 MB を無駄に置くだけである。同時に動く
セッション同士の隔離は、その中の**データベース**で行う。そのセッションで最初に `af-db url` した
ときに作り、セッションが削除されたときに落とす。

- Postgres ならこれは安い: `CREATE DATABASE … TEMPLATE <seed>` の実測が 0.63 s なので、
  「セッションごとに新品のデータベース」は現実的であり、`af-db reset` も同じ操作になる。
- 🔴 **鍵はセッションの同一性であり、名前ではない。** セッション名はスロット名で再利用される
  （`workspace/agent/cleanup_ops.go:103`）。スロット名で名付けたデータベースは、次にそこへ入った
  セッションに中身ごと引き継がれる——あのファイルがまさに防ぐために存在する欠陥そのものだ。
  drop は `removeSessionSideFiles` の隣に置く。
- 複数セッションで 1 つのデータベースを共有したい利用者は、名前で明示的に頼む
  （`af-db url --db=shared`）。既定は共有しないことである。

### 4. データディレクトリはタスクローカルディスクがあればそこ。ホームはオプトイン

`$AF_WS_SCRATCH` があれば `$AF_WS_SCRATCH/af-db/<instance>`、無ければ
`~/.local/state/af-db/<instance>`。Workspace を止めたら消えるテストデータは正しい既定である——
それは文書ではなくフィクスチャだ——し、小さいファイル数千を EFS から遠ざける。ADR 0044 が
EFS のペナルティを 1 ファイルあたり約 14.5 ms と測っている。

`af-db up --persist` はデータディレクトリをホームに置く。手でデータセットを作っている利用者の
ためのものである。**Fargate 配備ではそのホームは EFS＝NFS であり**、InnoDB や Postgres の
データディレクトリを NFS に置くことの性能と正しさは誰もここで測っていない。だから `--persist` は
自分が何をしているかを表示し、MySQL について勧める前に何を測るべきかは下の未解決の問いに書いた。

### 5. 供給は実行時ダウンロード＋`versions.json` の版固定＋SHA-256 検証

既存の鍵の隣に `postgres`・`mysql`、そして成果物ごとの `_sha256` を足す（`workspace/Dockerfile:503`）。
インストーラは `install-jdk` の作法をそのまま踏む——staging ディレクトリ、atomic rename——ので、
途中で終わったダウンロードが導入済みのふりをすることはない。

- **Postgres** は Maven Central の Zonky `embedded-postgres-binaries` jar から: 14 MB、両アーキ、
  16 / 17 / 18、そしてイメージに無いものを何も要求しない。16・17・18 を提示し、既定は 17。
- **MySQL** は公式の `minimal` tarball から。既定は 8.4（LTS）。x86_64 のみ——arm64 は未解決の問い 3。
- **イメージには焼かない**: lean な rootfs は「誰も頼んでいない 450 MB の道具を運ばない」ために
  存在するし、実行時取得は GPL のサーバをプロジェクトが配布するイメージの外に保つことでもある。
- **ただし 3 つは焼く**: `libaio1t64`・`libnuma1`・`libncurses6`（~250 KB）。実行時に `.deb` を
  剥がす手も動く（実測）が、それは利用者全員の導入を Debian のプール構成と「イメージの glibc に
  合う suite を選ぶこと」に依存させる——注 2 で実測した地雷であって、供給網ではない。

### 6. unix socket **と** `127.0.0.1` の両方で待つ

socket が既定なのは、ポートが利用者のセッション間で共有されており、固定ポートは 2 つ目の
セッションを待つ衝突だからである。しかし **JDBC は unix socket を話せない**——Postgres の
ドライバも MySQL のドライバも、別途ネイティブライブラリ無しでは話せない——ので、Java 形の
e2e テストは socket だけでは到達できない。そしてそれはこの ADR が存在する理由である e2e テストの
大きな一角だ。

そこで Agent は両方を bind し、TCP ポートを自分で採番し、`~/.config/agent-fleet/` 配下の
インスタンス登録簿に記録し、`af-db url --tcp` がそれを表示する。bind は `127.0.0.1` のみ:
コンテナは利用者のものだが、この機能のどこもコンテナの外から到達できてはならない。

### 7. 既定でアイドル停止し、メモリは隠さず見せる

待機中のサーバは 45 MB（Postgres）または 226 MB（MySQL）を、利用者のビルドと同じ cgroup から
取っている。動いている MySQL の隣で JVM をビルドするのが、Workspace が exit 137 を稼ぐ道である。

- 接続が 30 分無いインスタンスを Agent が止める。その後の `af-db up` は再導入ではなく
  74 ms / 1.8 s の再起動である。
- Console のカードは状態の隣に常駐サイズを出す。「これを点けたままで安全か」の答えが、runbook
  ではなく画面にあるようにする。
- 「just start a Postgres は無理」と今書いてある運用方針の段落を、やり方を書いた段落に差し替える。
  **そして**重いビルドの前に止めろと書く。

### 8. MCP ツールは増やさない

MCP ツールの説明文は、そのセッションがデータベースに触るか否かに関わらず、**すべての**エージェントの
**すべての**セッションで固定費として払われる。表面は CLI（`af-db`）と、Agent がセッションへ注入する
環境変数 2 本と、エージェントがすでに読んでいる運用方針の 1 段落である。エージェント側の効きは
その段落だ——今それは「諦めろ」と言っており、彼らは正しく従っている。

インスタンスが動いているとき `AF_DB_URL_POSTGRES` / `AF_DB_URL_MYSQL` を注入する。**`DATABASE_URL`
は暗黙には設定しない**——利用者自身のプロジェクトが最も読みそうな変数であり、アプリケーションを
黙ってテスト用データベースへ向けることは、この機能のせいにされる欠陥である。
`eval "$(af-db env)"` が、頼んだシェルの中で設定する。

### 9. Console の表面は Workspace 設定のカード 1 枚。toolchains と同じ proxy

`GET|PUT /api/env/databases` を `/api/env/toolchains` とまったく同じように Agent へ proxy し
（`control-plane/routes.go:769`）、Env タブのカードとして描く: エンジン・版・状態・常駐サイズ・
コピーボタン付きの接続 URL・開始 / 停止 / リセット。CP の新概念も、新しいスタックも、新しい IAM も
無い。

### 10. プロセスを持つのは Agent であり、セッションではない

サーバは Agent が detached で起こし、頼んだセッションより長生きし、アイドルまたはコンテナ停止で
Agent が止める。あるセッションの終了が、兄弟セッションの使っているサーバを殺してはならないし、
「データベースはどの端末の中にいるのか」を利用者に考えさせてはならない。

## 却下

- **Workspace の隣に置くサイドカーコンテナ。** 利用者自身の制約であり、実測もそれを支持する:
  使われていようがいまいが常駐し、運用者が選んだ 1 エンジン 1 版であり、Workspace と一緒に死に、
  タスク定義のある配備プロファイルにしか存在しない——native には何も無い。
- **共有サーバ 1 本に、利用者ごとの database と role。** 動かす費用は最も安く、DBA 的な意味では
  確かに「利用者ごとに切り出された」形だが: superuser が無いので拡張・照合順序・版差を試せない。
  1 人の暴走クエリが全員の問題になる。そして共有サーバはどこかに存在しなければならず、native と
  docker の配備にはそれを用意しろと言うことになる。明示的にそれを望む配備向けの 3 つ目の
  アダプタ候補としては覚えておく。
- **CP が払い出す利用者専用 DB サービス（案 B）を、今。** 技術的には平凡である——構造としては
  ADR 0045 がメンバーシップごとにすでにやっていること（ECS サービス、EFS アクセスポイント、
  SSM シークレット、Service Connect 名、止める回収役）と同じだ——し、永続する・大きい・
  Workspace をまたいで共有したいデータベースが要る利用者には正しい答えである。却下は**今は**であり、
  理由は費用と順序: 0.25 vCPU のタスクで 1 人あたり ≒$0.013/h（誰も止めなければ月 ≒$9）、起動は
  ミリ秒ではなく分、ECS 専用、加えて費用按分（ADR 0048）・資格情報のローテーション（ADR 0065）・
  CFN スタック。決定 1 が、インタフェース 1 つの代価でこの扉を開けたままにする。
- **利用者のテストのための RDS / Aurora Serverless。** 別クラスタは「床が高く隔離が薄い案 B」で
  あり、CP 自身のメタデータクラスタは、どんな権限であれテストコードに role を与える場所ではない。
- **Workspace 内の rootless Docker / Podman、そしてそれに伴う Testcontainers。** 2026-09-16 に
  改めて不可と実測した（注 3）: ユーザ名前空間は使えるが `newuidmap` が無く、戻すとは「setuid
  バイナリを 1 本も無いと assert しているイメージに戻す」ことである。Testcontainers を解禁する形は
  **利用者ごとの Docker ホスト**（ADR 0077 が GPU の箱を買うように CP が買い、`DOCKER_HOST` を
  渡す）だけであり、それは EC2 の請求書が付いた別 ADR であって、ここに紛れ込ませる決定ではない。

## 未解決の問い（決める前に測る）

1. **Fargate タスクで非特権ユーザ名前空間は通るか。** この docker ランタイムのコンテナでは通る。
   Fargate は別のランタイムで、答えは分かっていない。P0 には影響しない——上の「利用者ごとの
   Docker ホスト」に安い代替があり得るかを決める問いである。測り方: Fargate の Workspace で
   `unshare -Ur echo ok`。
2. **EFS 上のデータディレクトリはどれだけ遅く、そして安全か。** 決定 4 の `--persist` を勧めるか、
   非推奨にするか、MySQL では拒否するかを決める。ECS と docker の両プロファイルで `initdb` ＋
   1 万行の挿入を測る。
3. **arm64 の MySQL。** `minimal` ビルドが存在せず、フル tarball は圧縮 909 MB。候補は 2 つ:
   展開後にフル版を刈る（`bin/mysqld`・`share/`・`lib/plugin`——`bin/` だけで 222 MB、うち
   `mysqld` が 78 MB）か、Debian から MariaDB を取る（`.debian.org` は既に許可済み。MariaDB 自身は
   arm64 の bintar を出していない）。どちらかを測るまで、P1 は MySQL を x86_64 で出し、導入に
   失敗する版を提示する代わりに Console のカードでそう言う。
4. **既定の egress 許可リストに上流を 2 つ足すか**（`cdn.mysql.com`・`repo1.maven.org`）、それとも
   両エンジンとも `.deb` 経路にして 1 つも足さないか。なお Maven Central が今日そこに無いこと自体、
   `enforce` 配備で JVM プロジェクトをビルドする利用者にとっては別の穴である。
5. **Workspace の既定メモリを上げる必要があるか。** 2 GiB の Workspace で MySQL を動かすと、
   残りは 1.8 GiB である。レバーは ADR 0044 のサイジング。要るデータは「実際に何人が MySQL を
   点けるか」である。

## フェーズ

- **P0 — Postgres・CLI のみ。** Postgres 16/17/18 に対する `af-db up|url|reset|down|status`、
  版固定インストーラ、socket ＋ `127.0.0.1`、決定 3 の同一性を鍵にしたセッションごとのデータベース、
  タスクローカルディスク上のデータディレクトリ、アイドル停止。文書: 運用方針の段落
  （`workspace/notes/environment.md`）、利用者ガイドの手順、そして
  `docs/build/10-development.md` §10.4 を手組みの治具から `af-db` に書き換える。**完了の定義**は、
  新しい Workspace で `af-db url` に向けてこのリポジトリ自身の
  `TestPostgres | TestSchemaDialectParity` が緑になること——ローカル準備の要らない `go test` 一行で。
- **P1 — MySQL と Console。** 共有ライブラリ 3 本をイメージへ、MySQL インストーラ（x86_64。arm64 は
  未解決の問い 3）、常駐サイズ付きの決定 9 の Env タブのカード、運用方針へのメモリの指針。
- **P2 — この ADR ではなく需要が決める。** 決定 1 の裏の 2 つ目のアダプタ: 永続が要る配備向けの
  利用者専用サービス（案 B）か、利用者ごとのデータベースを持つ共有サーバか。エンジン追加
  （Redis・MongoDB）は、頼まれたなら同じインストーラの表の行である。

## 確認した出どころ（2026-09-16・このリポジトリのコード）

- `workspace/notes/environment.md:81` — この機能が置き換えることになる方針の段落。
- `workspace/agent/install_tools.go:1`・`:29` — 必要時導入の版固定インストーラの作法と、利用者ごとの
  導入先。
- `workspace/Dockerfile:503` — `versions.json` はイメージビルドで生成される。
  `workspace/Dockerfile:642` — setuid ビットを全部剥がし、無いことを assert している。
- `workspace/agent/env_toolchains.go:25`・`:29` — データベース登録簿が写し取る Workspace 単位の
  選択ファイル。`control-plane/routes.go:769` — Console がそこへ到達する経路。
- `workspace/agent/cleanup_ops.go:103` — セッション付随ファイルと、決定 3 が引き継ぐスロット名
  再利用の罠。
- `control-plane/egress_policy.go:18` — 既定の許可リストと、そこに無い上流 2 つ。
- `control-plane/internal/runtime/runtime.go:12` — 決定 1 が写すポート／アダプタの形。
- `control-plane/workspace_lifecycle.go:534` — インスタンスが使う Workspace メモリのクォータ。
- `workspace/af-scratch.sh`・`workspace/entrypoint.sh:245` — `AF_WS_SCRATCH` はタスクローカルで、
  停止すると消える。
- `docs/build/10-development.md` §10.4 — この ADR が製品化する、手組みの Postgres 治具。
