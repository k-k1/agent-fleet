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

## レビュー（2026-09-17・P0 着手前）

0071・0076 のレビューと同じ流儀で——各決定は根拠から出ているか、コードは草稿が言うとおりに
なっているか。「確認した出どころ」の `file:line` は全部 `sed -n` で読み直し、両サーバを同じ
コンテナ（docker ランタイム配備・x86_64・cgroup 10 GiB・8 CPU）で起動し直し、未解決の問い 3 を
測った。結論を先に：**P0 は着手してよい。ただし決定 3 は書く前に書き直しが要る**——前提が
コードと違い、依拠しているセッション単位の鍵はマネージド経路に存在しない。決定 1・2・5〜10 は
立つ。うち 2 つに P0 の項目が 1 つずつ増える（クライアントとシム）。2026-09-16 の数字は全部
再現し、2 つは ADR に有利な方へ動いた。

### 前提をコードに当てる

- **引用は全部解決する。** `runtime.go:12` はインタフェースの 1 行上のコメント（`:13`）、
  「docs/log/62 §62.4」は §62.4.1（`docs/log/62-ecs-start-latency.md:198`）。「アダプタ 4 つ」は
  `NewFactory` のプロファイル 4 つ（`local|docker`・`ecs`・`ecs-ec2`・`native|wsl`）で、struct は
  3 種——書いてあるとおりに正しい。
- 🔴 **決定 3 の前提が違う：セッション名はスロット名ではなく、再利用もされない。** 2026-07-03
  （コミット `4ae4424a`）以降、セッション名は乱数スラグ——`"s"` + base32 6 文字、約 30 ビット——で、
  `allocSessionName`（`workspace/agent/internal/sessionx/session_name.go:51`）はメタがある・tmux が
  生きている・jsonl がディスクにある、のどれかに当たるスラグを配らない。その上のコメントは
  スラグを「セッションの不変の同一性」と呼んでいる。`cleanup_ops.go:103` のコメント（「スロット名で
  再利用される」）は 2026-08-19、この変更の後に書かれたもので、7 月以前の `slotNN` 方式を説明
  している。第二の同一性は存在しない：`session.UUID(dir, name)` は名前から決定的に導かれ
  （`internal/session/uuid.go:19`）、`session.Meta` に id 欄は無い。つまり「名前でなく同一性で
  鍵を取る」には取るものが無く、守る相手もいない——**名前が同一性そのもの**。ADR が代わりに
  言うべきこと：データベースの鍵はスラグ。そして drop を `removeSessionSideFiles` に吊るさない——
  そこはメタを消す 5 箇所のうちの 1 つに過ぎない（`cleanup_ops.go:80`・`:98`・
  `internal/gitx/git.go:1701`・`internal/sessionx/session_handlers.go:161`・`:1169`）。`af-db` が
  **突き合わせる**：`up` / `url` / `status` のたびに、メタの無いスラグのデータベースを落とす。
  経路は 1 本で済み、drop を通らずに停止した後も片付く。
- 🔴 **「*このセッション*の接続 URL」はマネージド経路では答えられない。** `AF_SESSION_NAME` が
  プロセス環境に入る場所は tmux 起動の 1 箇所だけ（`internal/sessionx/session_tmux.go:40`）。
  マネージドセッションは kind ごとに 1 本の共有デーモンの中で動く——
  `internal/agents/codex/driver.go:88` がそう書いて実測しており、opencode の `EngineEnv` が
  あるのはデーモンの環境が Workspace 単位だから——ので、マネージドセッションからエージェントが
  回すシェルにはセッション名が無い。codex の前例は作業ディレクトリに退避している。従って
  実用上の既定の鍵は**作業コピー**（`Meta.Dir`）であってセッションではない：`af-db url` は
  `AF_SESSION_NAME` があればそれで、無ければ cwd → 作業コピーで呼び手を解決する。1 つの作業
  コピーを共有する 2 セッションは既にファイルとブランチを共有しており、テスト DB の共有は同じ
  取引。明示的な共有には `--db=<name>` が残る。決定 8 の「Agent が注入する環境変数 2 つ」にも
  同じ限界がある——環境は起動時に固定され、しかも tmux セッションだけ。後から起動したサーバは
  動作中のセッションから環境変数では見えない。常に効くのは `af-db url` / `eval "$(af-db env)"`
  で、注入される変数は tmux セッション向けの便宜に過ぎない。
- **決定 5 の供給に、草稿が名指ししていない穴がある：Zonky はクライアントを同梱しない。**
  残してあった `pg.jar` の中身は `postgres-linux-x86_64.txz` 1 本、展開した `dist/bin` は
  `initdb`・`pg_ctl`・`postgres` の 3 つだけ。帰結は 2 つ。(a) Agent に Postgres のワイヤ
  クライアントが要る——`CREATE DATABASE`・`reset`・上の突き合わせ・アイドル判定
  （`pg_stat_activity`）のために。`workspace/agent/go.mod` には無く、`pgx/v5` は CP のモジュールに
  既にあり pure Go。(b) 利用者はサーバを得るが `psql` を得ない。「手軽に」にとっては機能の半分。
  `.deb` の経路（`.debian.org` は許可済みで、注 2 が動くことを実測している）で
  `postgresql-client-17` と `libpq5` が取れる。P0 に名指しすること。MySQL の `minimal` tarball は
  `mysql`・`mysqladmin`・`mysqldump` ほか `bin/` 下に 29 本を同梱しているので、こちらはシェル
  アウトで足りる。
- **決定 9 は両側 1 行ずつで、新しいものは無い。** `proxy.rest`（`control-plane/proxy.go:148`）は
  `/api/<x>` を Agent の `/<x>` へ汎用に転送し、PUT を Workspace の活動として数える——Start / Stop
  にはそれで正しい。要るのはルートを**両方**に登録すること：`control-plane/routes.go`（`:769` の
  隣）と `workspace/agent/routes.go`（`:355` の隣）。CP の中継は catch-all ではなく明示の許可
  リストで、CP 側の 1 行が無いと Console では無音の 404 になる。
- **決定 5 のピンとシム。** `versions.json` は Dockerfile が ARG から書く平らな map
  （`Dockerfile:500`）で、アーキテクチャ別の sha 鍵はビルド時に選ばれる（`kiro_sha256`・
  `install_kiro.go:61`）。Postgres のメジャー 3 × アーキテクチャ 2 は sha ARG が 6 本。安い形は
  既定メジャーだけをピン（`postgres` + `postgres_sha256`）し、残りは Maven Central の `.sha256`
  サイドカー（17.6.0 で HTTP 200、存在する）で検証すること——`install-go` の前例
  （`install_tools.go:300`）は既に出所から sum を取っている。Zonky の arm64 系列は現在
  16.15 / 17.11 / 18.6 を持つので「16 / 17 / 18、両アーキテクチャ」は成り立つ。それと罠が
  1 つ：`workspace-agent <未知のサブコマンド>` は **Agent を起動する**（`main.go` は
  `os.Args[1] ==` の連鎖で default が無い）。`af-db` は実体の `/usr/local/bin/af-db` シム**と**
  `main.go` のディスパッチ 1 行、両方が要る。
  🔴 2026-09-17: この罠は塞いだ。分岐は `cli.go` の表 1 本になり、表に無い引数は usage を出して
  exit 2、`--version` / `--help` は正式サポート、起動するのは「引数なし」と `serve` だけ。`af-db`
  の要件は「シム＋表の 1 行」のまま。二重起動そのものも無害化した——`serve` は副作用より先に
  listen する。
- **決定 4 は 1 プロファイルの意味論を全部のように書いている。** `AF_WS_SCRATCH` を設定するのは
  ECS アダプタだけ（`entrypoint.sh:246`）で、entrypoint が退避するのはディスクが 30 GiB 以上の
  ときだけ（`AF_WS_SCRATCH_MIN_GB`・`entrypoint.sh:263`）——既定の Fargate 配備は 20 GiB で
  スキップする（ADR 0044 決定 5）。「設定されているとき」と「entrypoint が退避したとき」は別の
  条件。39 MB / 200 MB のデータディレクトリは、この門が守っている数 GiB のキャッシュではない
  ので、変数があれば `$AF_WS_SCRATCH` を使う、と書く。docker と native では既定がホームに落ち、
  **停止しても残る**ので、「Workspace が止まれば消える」は ECS だけの意味論。従って `--persist`
  も ECS でしか意味を持たない。

### 測り直し（2026-09-17・同じコンテナ）

- **Postgres**：`pg_ctl -w start` で 118 ms（ログ上は listening → ready が 8 ms）、常駐 47 MB
  ＝ postmaster 17.6 MB + 子 5 本。`CREATE DATABASE … TEMPLATE` は初回 **57 ms**・2 回目
  **35 ms**、`DROP DATABASE` 19〜86 ms（§10.4 の治具と同じ fsync=off）。草稿の 0.63 s は上限で
  あって費用ではない。
- **MySQL** を `tar xJf` から：dist 446 MB、`--initialize-insecure` 12.9 s、start → ping 1.85 s、
  VmRSS 226 MB（HWM 238 MB）、データディレクトリ 200 MB、JSON の往復が答えた。SIGTERM で約 1 秒
  で正常停止。6 つの数字が全部再現する。
- 🔴 **残っていたログが記録していて、草稿が触れていない危険。** 前回の `mysqld.log` は 105 MB、
  `[ERROR] Unable to open './#innodb_redo/#ib_redo5'` が 6 分 43 秒で 904,817 行——動いている
  mysqld の下からデータディレクトリを消した跡。mysqld は終了せず、毎秒約 2,200 行を書き続ける。
  `af-db down` / `reset` / アイドル停止はデータディレクトリに触る**前に**プロセスを止めること、
  `--log-error` は決して EFS に向けないこと。決定 10（プロセスを持つのは Agent）がこの順序を
  強制できる根拠。順序を明文化する。

### 未解決の問い 3 を測った——P1 の決まり方が草稿の想定と違う

- `mysql-8.4.6-linux-glibc2.28-aarch64.tar.xz` は **909,017,708 バイト**（`archives/` 配下。
  `Downloads/` は 404、aarch64 の `-minimal` はどちらにも無い）、**展開 1,742 MB**、471
  メンバー。`bin/mysqld` だけで **514 MB**。
- x86_64 `minimal` tarball のメンバー集合（440 個）そのものに刈っても **1,245 MB で、446 には
  ならない**——フル tarball は strip されていない。arm64 の `mysqld` を `readelf -S` で見ると
  `.debug_*` セクションが 7 つ、非割当セクション 451 MB のうち debug が 363 MB。割当セクション
  ——`strip` が残す分——の合計は **69 MB**。突き合わせ：x86_64 フル tarball の `mysqld` は 516 MB、
  `minimal` の 78 MB に対して。つまり `minimal` はフルを strip したもの。
- `strip` は **arm64 の Workspace 自身で**走らせる必要がある：このホストの binutils 2.44 は
  AArch64 を拒む（"Unable to recognise the format of the input file"）。イメージは gcc と一緒に
  binutils を積んでいるので、arm64 の導入経路は：909 MB を落とし、`bin/mysqld`・`bin/mysql`・
  `lib/private/*.so`・`lib/plugin/*.so`・`share/` を取り出し、その場で strip、ディスク上は
  200 MB 前後を見込む。費用はディスクでなくダウンロード。
- arm64 `mysqld` の `NEEDED` は libc / libstdc++ / OpenSSL を除くとちょうど `libaio.so.1` と
  `libnuma.so.1`。`libncurses.so.6` が要るのは `mysql` クライアントだけ。決定 5 の「焼くのは
  3 つ」はクライアントには正しく、サーバには 2 つ。
- **Debian trixie の MariaDB** がもう 1 つの候補：`mariadb-server-core_11.8.8-0+deb13u1_arm64.deb`
  は 7.1 MB（導入後 46 MB、`mariadbd` 27 MB）、`mariadb-client-core` は 0.9 MB。`Depends` は
  `libaio1t64` / `libnuma1` に加えてイメージに無い `liburing2` を要求する。`libpcre2-8`・
  `libssl3`・`libsystemd0` はある。同じワイヤプロトコルでダウンロードは 1/130。ただし MySQL 8.4
  ではない——JSON・`CHECK`・ウィンドウ関数が端で違い、「MySQL で」と言った利用者には応えない。
  推奨：MySQL を両アーキテクチャで strip 経路により出す。MariaDB は利用者が名指しで求めたときだけ。
  どちらにせよ、未解決の問い 3 にあった「arm64 は Console のカードでそう言う」という退避は
  もう要らない。

### 完了の定義

- **P0 の「完了」には、検証可能にするために足りないものが 3 つある。** 変数名は
  `AF_TEST_DATABASE_URL`（`store_postgres_test.go:19`）、ソケットの URL の形は
  `postgres://postgres@/postgres?host=<sockdir>&sslmode=disable`（§10.4）、そして
  `-run 'TestPostgres|TestSchemaDialectParity'` の正規表現は **4 つ**のテストに当たり、うち
  `TestPostgresPasswordRotation` は `--auth=trust` だとスキップする
  （`store_postgres_rotation_test.go:91`）。残してあったサーバに対して今日リハーサルした：
  `TestPostgresDeleteCascade` 0.34 s、`TestPostgresStore` 0.66 s、`TestSchemaDialectParity`
  0.55 s——PASS、rotation——SKIP。従って `af-db` は `initdb --auth=scram-sha-256` で生成した
  パスワードを URL に載せる（決定 6 の `127.0.0.1` リスナが trust のスーパーユーザ口にならない
  効果もある）べきで、完了の定義はこう読む：`control-plane/` で
  `AF_TEST_DATABASE_URL="$(af-db url)" go test -count=1 -run 'TestPostgres|TestSchemaDialectParity' ./...`
  が **4 PASS・0 SKIP**。`-count=1` は、キャッシュの `ok` が何も証明しないから。
- 🔴 **このリポジトリに MySQL のテストは無い。** `TestSchemaDialectParity` が比べるのは SQLite と
  Postgres（`store_schema_parity_test.go`）で、`control-plane/` の下に MySQL を話すものは無い。
  従って P1 の MySQL レーンにはリポジトリ内の「完了」が無い。フェーズに何をもって完了とするかを
  書く——利用者のプロジェクトの MySQL スイート、あるいは `e2e/` に足す `go-sql-driver/mysql` の
  スモーク。実測は立つが、受け入れはまだ存在しない。

### 選び方

- **決定 1・2 は「サイドカーでなく利用者ごと」への最短。** 著者が内々に重みを置いたもの——
  手軽さが第一、費用と配備プロファイルの広さが決め手——は、今日の数字がそのまま支える：118 ms、
  47 MB、インフラ無し、native・docker・ECS 2 種で同じ日に同じ機能。今日見つかったもので決定 2 を
  弱めるものは無い。「手軽さ」を弱めるのは上のクライアント欠落で、それは P0 の項目であって
  設計変更ではない。
- **却下案は立つ。1 つは理由が違う。** 「共有サーバに利用者ごとの role」を「スーパーユーザが
  無い」で却下しているが、それは書かれたままでは誤り——`CREATEDB` の role と利用者ごとの
  データベースで、拡張も照合順序もデータベース内で試せる。スーパーユーザが要るのは版の選択と
  `ALTER SYSTEM` だけ。立つ理由は草稿が 2 番目に挙げているもの：そのサーバはどこかに存在せねば
  ならず、native / docker 配備は誰からも無料で貰えない。却下は保ち、文言を直す。サイドカーの
  却下には 1 つ足せる：Workspace と別にアイドル停止できない。案 B は費用を 1 つ控えめに書いて
  いる：利用者ごとの ECS サービスは利用者ごとに 2 本目のサービスで、ADR 0045 のタスク数が倍になる。

### このレビュー後の未解決の問い

- **問い 3 は答えが出た**（上）：P1 は arm64 の MySQL を strip 経路で出してよい。MariaDB は退避
  ではなく別の提供。
- **問い 4 は鋭くなった**：`.deb` の経路が賄うのはクライアントとライブラリ 3 つで、サーバでは
  ない。Debian の `postgresql-17` サーバパッケージをホームに移した `.deb` からこのイメージで
  動かせるかは未測。測るまで、上流 2 つは問いに残る。
- 問い 1・2・5 はこの配備では測れない。変更なし。問い 5 に添えて：`memFloorBytes` は 256 MiB
  （`workspace_lifecycle.go:415`）——床サイズの Workspace は MySQL をそもそも動かせず、
  `af-db up mysql` は exit 137 を稼ぐ前にそう言うべき。

### 確認した出どころ（2026-09-17・このリポジトリのコード）

- `workspace/agent/internal/sessionx/session_name.go:31`・`:51`・`:69` — 乱数の不変スラグと
  3 つの拒否条件。`internal/session/uuid.go:19` — UUID は名前の関数。
- `workspace/agent/internal/sessionx/session_tmux.go:40` — `AF_SESSION_NAME` が注入される
  唯一の場所。`internal/agents/codex/driver.go:88` — マネージド経路が運べない理由。
- `workspace/agent/cleanup_ops.go:80`・`:98`・`internal/gitx/git.go:1701`・
  `internal/sessionx/session_handlers.go:161`・`:1169` — メタを消す 5 経路。
- `control-plane/proxy.go:148` — 汎用の中継。`workspace/agent/routes.go:355` —
  `/env/toolchains` の Agent 側。
- `workspace/agent/main.go:56`〜`:100` — default 無しのサブコマンド分岐。`install_kiro.go:61` —
  `versions.json` のアーキテクチャ別 sha。`install_tools.go:300` — 出所から取る sum。
- `workspace/entrypoint.sh:246`・`:263` — `AF_WS_SCRATCH` は ECS 限定で 30 GiB の門。
- `control-plane/internal/store/store_postgres_test.go:19`・`store_schema_parity_test.go:24`・
  `store_postgres_rotation_test.go:91` — 変数名、方言の組、trust 認証のスキップ。
- `control-plane/workspace_lifecycle.go:415` — `memFloorBytes`。
- `~/.local/share/af-pgtest`（Zonky 17 の dist + data）と `~/.local/share/af-dbtest/my.tar.xz`
  — 測り直しに使った残置物。上流のサイズは 2026-09-17 に `cdn.mysql.com`・`repo1.maven.org`・
  `deb.debian.org` へ `curl -I`。

## 上書きする既存の決定と、維持する決定（2026-09-17・レビュー後・P0 前）

著者はレビューを受け入れた。上の決定は書かれたまま残し、ここで名指ししたものはこの節が
置き換える。末尾の契約表は、3 レーン（供給・実行・文書）を別々のセッションが同じ言葉に
向かって組めるように固定したもの。

- **決定 3 → 3′。サーバは Workspace ごとに（エンジン, メジャー）1 本。データベースは*作業コピー*
  ごとに 1 つ。** 鍵は作業コピーのディレクトリ（`Meta.Dir`、git のトップレベル）であってセッション
  ではない——スラグは既に不変で、マネージド経路は自分のセッションを名指しできない。`af-db` は
  呼び手をこう解決する：`AF_SESSION_NAME` があればそのセッションの `Dir`、無ければ cwd の git
  トップレベル、それも無ければ cwd。データベース名は `af_` + ディレクトリのベース名を正規化
  （小文字、`[^a-z0-9]` → `_`、40 文字まで）+ `_` + `sha256(dir)` の先頭 6 hex。レジストリに名前 →
  dir を記録。**突き合わせ**を `up` / `url` / `status` のたびに：記録されたディレクトリがディスクに
  無いデータベースは落とす。`--db=<name>` は明示の共有データベースで、突き合わせは決して落とさない。
  セッション削除 5 経路には吊るさない。
- **決定 4 → 4′。** 変数があれば `$AF_WS_SCRATCH/af-db/`——entrypoint の 30 GiB の退避門とは無関係に。
  無ければ `~/.local/state/af-db/`。docker と native では既定が停止をまたいで残る——「Workspace が
  止まれば消える」は ECS の挙動。`--persist` はホーム側を強制し、`fsync` を戻す。
- **決定 8 → 8′。** 契約は `af-db url` と `eval "$(af-db env)"`。`AF_DB_URL_POSTGRES` の注入は tmux
  起動時だけ、しかもインスタンスが動いていて作業コピーのデータベースが既にあるときだけ。
  マネージドセッションは環境変数では何も受け取らない。`DATABASE_URL` を設定するのは `af-db env`
  だけ。
- **決定 5 に P0 の項目が 3 つ増える。** (a) `workspace-agent install-pg-client`：
  `postgresql-client-<major>` と `libpq5` を Debian trixie の `Packages` 索引からビルド
  アーキテクチャ向けに解決——版と sha256 は索引から取り、Debian が引き上げるファイル名を固定
  しない——`~/.local/share/agent-fleet/pg-client/` に展開し、`LD_LIBRARY_PATH` を設定する
  `~/.local/bin/{psql,pg_dump,pg_restore}` ラッパーを置く。(b) Agent は
  `github.com/jackc/pgx/v5` を取る。(c) 実体の `/usr/local/bin/af-db` シムと、`main.go` の
  `os.Args[1] == "af-db"` 分岐。
- **決定 6 に認証が付く。** `initdb --auth=scram-sha-256 --auth-local=scram-sha-256 -U postgres`、
  パスワードは `initdb` 時に生成して `~/.config/agent-fleet/af-db/postgres-<major>.pass` に
  0600 で置く。`127.0.0.1` のリスナは trust のスーパーユーザ口にならない。
- **決定 7 は維持。機構を名指しする。** Agent のループが 60 秒ごとに
  `SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'`。30 分連続で 0
  なら `pg_ctl stop -m fast`。`lastUsedAt` は `af-db url` のたびにも更新。
- **決定 10 に順序が付く。** 停止 → postmaster の pid が消えるのを待つ → それからデータ
  ディレクトリに触る。サーバのログは `pg_ctl -l` で `<root>/postgres-<major>.log`。
- **P0 の完了条件**はレビューのもの：`control-plane/` で
  `AF_TEST_DATABASE_URL="$(af-db url)" go test -count=1 -run 'TestPostgres|TestSchemaDialectParity' ./...`
  が、`af-db` を一度も走らせたことのない Workspace で 4 PASS・0 SKIP。
- **未解決の問い 3 はレビューで閉じた**：P1 は arm64 の MySQL を arm64 ホスト上の `strip` で出す。
  MariaDB は別の提供であって退避ではない。

### P0 の契約

| 項目 | 値 |
|---|---|
| 導入先 | `~/.local/share/agent-fleet/postgres/<major>/{bin,lib,share}`。`workspace-agent install-postgres <major>`、`major` ∈ {16, 17, 18}、既定 17。`AF_DB_POSTGRES_ROOT=<dir>` でテスト用に root を上書き（残置の `~/.local/share/af-pgtest/dist` が同じ形）。 |
| ピン | `versions.json`：`postgres` = 既定メジャーの Zonky 版（例 `17.11.0`）、`postgres_sha256` = その jar のビルドアーキテクチャ向け sha（`kiro_sha256` の型）。他のメジャーはその系列の最新 Zonky を Maven Central の `.sha256` サイドカーで検証。jar → `postgres-linux-<arch>.txz` → staging → atomic rename。 |
| シム | `/usr/local/bin/af-db` = `exec workspace-agent af-db "$@"`（`workspace/Dockerfile` で `af-scratch` の隣に焼く）。 |
| レジストリ | `~/.config/agent-fleet/af-db/instances.json`。`fstore` の read-modify-write を `~/.config/agent-fleet/af-db/lock` の下で。インスタンス 1 つ = `{engine, major, root, datadir, sockdir, port, pid, startedAt, lastUsedAt, persist, databases: {name: dir}}`。 |
| データディレクトリ | `<root>/postgres-<major>/data`。`<root>` = `$AF_WS_SCRATCH/af-db` か `~/.local/state/af-db`。 |
| ソケット | `~/.local/state/af-db/run/postgres-<major>/`（短いパス。ファイルは `.s.PGSQL.<port>`）。 |
| ポート | Agent が `127.0.0.1:0` を bind して離し、`-p` で渡し、記録する。 |
| サーバのフラグ | `-k <sockdir> -h 127.0.0.1 -p <port> -c shared_buffers=32MB -c max_connections=50 -c fsync=off`（`--persist` では `fsync=on`）。 |
| URL（既定） | `postgres://postgres:<pw>@/<db>?host=<sockdir>&sslmode=disable` |
| URL（`--tcp`） | `postgres://postgres:<pw>@127.0.0.1:<port>/<db>?sslmode=disable` |
| 動詞 | `af-db up [postgres] [--major N] [--persist]` · `af-db url [--db=NAME] [--tcp]`（未導入なら導入、停止中なら起動、無ければデータベースを作る） · `af-db env [--db=NAME] [--tcp]`（`export AF_DB_URL_POSTGRES=…` と `export DATABASE_URL=…` を出力） · `af-db reset [--db=NAME]`（DROP + CREATE） · `af-db down [--purge]`（停止。`--purge` は pid が消えた後にデータディレクトリも消す） · `af-db status [--json]` |
| 終了コード | 0 正常 · 2 使い方 · 3 導入失敗（メッセージに URL と両方の sha） · 4 サーバ起動失敗（メッセージにログのパス） · 5 未起動（`reset` だけ）。 |
| コード | `workspace/agent/internal/afdb/` に CLI の動詞と Agent のアイドル／突き合わせループ。`workspace/agent/install_postgres.go`・`install_pg_client.go` は `install_kiro.go` に倣う。 |

## P0 受け入れ（2026-09-17）

3 レーンを 3 つのセッションが契約表に向かって組み、それぞれを 4 つ目のセッションが merge 前に
レビューした（レビュー側が指摘を peer メッセージで著者に送り、修正後に受け入れを回し直した）。

| レーン | ブランチ | 指摘 送／修正 | merge |
|---|---|---|---|
| L3 文書（`workspace/notes/environment.md`・`guide/member/03-code`・`docs/build/10-development` §10.4。加えて旧文「DB は無いので skip」が新しいノートと矛盾していた `AGENTS.md` と `workspace/workspace-notes.md`） | `temp/s6arm5b` | 11 / 11 | `a39c4487` |
| L1 供給（`install-postgres`・`install-pg-client`・`Dockerfile` のピンと `af-db` シム） | `temp/s66bqob` | 7 / 7 | `3ad2ce88` |
| L2 実行（`internal/afdb`・`af-db` の動詞・アイドルループ・`session_tmux.go` の注入・pgx） | `temp/sawbl7m` | 13 / 13 | `d3b6f3ce` |

**完了条件は `af-db` を一度も見ていない HOME から緑になった**（このコンテナ・x86_64・merge 後の
ブランチ）：`af-db up` が Maven Central から Zonky 17.11.0 を導入し、scram で `initdb` してサーバを
起動するまで通しで **8.7 秒**。`af-db url` は作業コピーのデータベース
（`af_agent_fleet_wip_szkxzgu_9af42b`）のソケット URL を返した。`control-plane/` で
`AF_TEST_DATABASE_URL="$(af-db url)" go test -count=1 -run 'TestPostgres|TestSchemaDialectParity' ./internal/store/`
→ **4 PASS・0 SKIP**（サーバが trust でなく scram なので `TestPostgresPasswordRotation` も走る）。
`af-db status --json` にパスワードは無く、`af-db down --purge` でサーバ停止とデータディレクトリ削除、
`postgres` プロセスは残らなかった。merge 後の `workspace/agent` で `gofmt`・`go vet`・
`go test -count=1 ./...` はきれい。

### レビューで見つかった契約の訂正（上の表はこれらの行で上書きされる）

- **URL（既定）**: `postgres://postgres:<pw>@/<db>?host=<sockdir>&port=<port>&sslmode=disable`。
  pgx はソケットファイル名 `.s.PGSQL.<port>` をポートから組むので、割り当てポートではソケット URL
  にも `port=` が必須。
- **レジストリ**: `fstore` ではない——`fstore` にはロックが無く read-modify-write を禁じている。
  レジストリは専用 flock（`~/.config/agent-fleet/af-db/lock`）の下で tmp + rename。起動経路は
  （エンジン, メジャー）ごとの第 2 の flock（`postgres-<major>.start.lock`）で直列化する。停止状態
  から `af-db url` を 2 本同時に走らせると `initdb` を取り合い、ロックが無い版では後発が exit 4 に
  なった（実測）。`install-postgres` も同じ理由でメジャーごとの flock を持つ。
- **ログ**: `<root>/postgres-<major>.log` の `<root>` は *state* root（`$AF_WS_SCRATCH/af-db` か
  `~/.local/state/af-db`）であって導入 root ではない。
- **`--major N`** は全動詞が受ける。既定 17。
- **`install-pg-client`**: Debian trixie の `main` にあるのは `postgresql-client-17` だけ。16 / 18 の
  要求は 17 で応え、stderr にそう言う（PGDG なら 3 つ揃うが、既定の egress 許可は `.debian.org`
  のみ）。ラッパーは Debian の `pg_wrapper` を経由せず `/usr/lib/postgresql/17/bin/psql` を直接
  exec するので、`postgresql-client-common` は導入しない。
- **導入時間**: Postgres はどのメジャーも 1.6〜2.1 秒（jar 15 MB → 60 MB）、クライアントは 1.5〜2.0
  秒、各 3 回計測。ガイドの「数分」は保守的。
- **`versions.json` のピン経路はイメージを焼き直すまで未通過**: このコンテナの `versions.json` は
  `postgres` キーより古いので、受け入れは Maven metadata の経路（同じ 17.11.0 を選ぶ）を通った。
  ピン経路は単体テストだけが通している。
- **アイドル停止**は `client backend` 行から `pg_backend_pid()` を除いて数える。最初の版は自分の
  プローブを数えていて、永久に止まらなかった。
- **`down --purge`** は postmaster の pid が不明で `pg_ctl stop` が失敗したときデータディレクトリを
  消さない——決定 10 の順序の具体化。

P0 が意図してやらないこと：Console のカード（P1・決定 9）、MySQL（P1）、マネージドセッションへの
`AF_DB_URL_POSTGRES`（8′）。次に測るのは ECS 配備での最初の実利用者の 1 回——作業ディスク上の
データディレクトリ（4′）と、焼き直したイメージのピン経路が、この受け入れで届かなかった 2 経路。

## P1 の契約（2026-09-17・レーン起動前に固定）

P1 も P0 と同じ組み方の 3 レーン（各 1 セッション、各レビュー子、merge は M1 → M2 → M3 の順）。
衝突しないよう、各レーンが触ってよい場所を名指しする。

### M1 — MySQL の供給（`workspace/agent/install_mysql.go`・`workspace/Dockerfile`）

- **イメージ**: `libaio1t64`・`libnuma1`・`libncurses6` を `workspace/Dockerfile` の `apt` で入れる
  （両アーキ、約 250 KB）。それを積んだイメージが配備されるまでは `AF_DB_MYSQL_LIBS=<dir>` を
  `mysqld` / `mysql` 起動時の `LD_LIBRARY_PATH` の先頭に付ける——テスト用の鉤であり、古いイメージ
  向けの文書化された回避策（このコンテナでは `~/.local/share/af-dbtest/libs/usr/lib/x86_64-linux-gnu`）。
- **`workspace-agent install-mysql [8.4]`** → `~/.local/share/agent-fleet/mysql/8.4/{bin,lib,share}`。
  ピン: `mysql` = `8.4.6`、`mysql_sha256` はビルドアーキ別（x86_64 は `minimal` tarball、arm64 は
  フル tarball）。URL は `https://cdn.mysql.com/Downloads/MySQL-8.4/<file>` を試してから
  `https://cdn.mysql.com/archives/mysql-8.4/<file>`。`<file>` は
  `mysql-<ver>-linux-glibc2.28-x86_64-minimal.tar.xz` か `mysql-<ver>-linux-glibc2.28-aarch64.tar.xz`。
  staging ファイルに落として sha 検証してから展開——arm64 は `bin/{mysqld,mysql,mysqladmin,mysqldump}`・
  `lib/private/*.so*`・`lib/plugin/*.so`・`share/` **だけ**を取り出し、イメージの binutils で各 ELF を
  その場で `strip`（`strip` が無ければそのまま置いて stderr に言う）。staging は版ごとの固定 dir を
  開始時に消し、版ごとの flock の下で——`install-postgres` と同じ。sha 不一致は exit 3 で URL と
  両方の sha。ダウンロードが拒まれたときはメッセージに `cdn.mysql.com` と `AF_EGRESS_ALLOWLIST`
  を出す（未解決の問い 4 は開いたまま）。
- 展開後に `ldd bin/mysqld` で `not found` が無いこと。あれば exit 3 でライブラリ名と
  `AF_DB_MYSQL_LIBS` を名指しする。
- `AF_DB_MYSQL_ROOT=<dir>` で root を上書き（`AF_DB_POSTGRES_ROOT` と同じ）。

### M2 — MySQL の実行と Agent API（`workspace/agent/internal/afdb/`・`routes.go`）

- `Instance.Major` を文字列にする（`"17"`・`"8.4"`）。レジストリは P0 で新設で未配備なので移行は
  無し。インスタンス鍵 `mysql-8.4`。state root は Postgres と同じ。datadir `<state root>/mysql-8.4/data`、
  ソケット `~/.local/state/af-db/run/mysql-8.4/mysql.sock`、pid ファイル `<state root>/mysql-8.4/mysqld.pid`、
  ログ `<state root>/mysql-8.4.log`、パスワード `~/.config/agent-fleet/af-db/mysql-8.4.pass`（0600）。
- **初期化**: `mysqld --no-defaults --initialize-insecure --basedir=<root> --datadir=<datadir>`、
  続けてソケット越しに `ALTER USER 'root'@'localhost' IDENTIFIED BY '<pw>'` と
  `CREATE USER 'root'@'127.0.0.1' IDENTIFIED BY '<pw>'` + `GRANT ALL ON *.* … WITH GRANT OPTION`
  （`--skip-name-resolve` だと `127.0.0.1` は `localhost` に一致しない）。
- **起動フラグ**: `--no-defaults --basedir --datadir --socket --pid-file --log-error
  --bind-address=127.0.0.1 --port=<port> --skip-name-resolve --mysqlx=0
  --innodb-buffer-pool-size=64M --performance-schema=0 --innodb-flush-log-at-trx-commit=0`
  （最後が `fsync=off` の相当。`--persist` では外す）。ready はソケットへの `mysqladmin ping`。
- **MySQL と話す**: `<root>/bin/mysql` と `mysqladmin` をシェルアウト（`minimal` にも arm64 の
  部分集合にもある）。Go の依存は増やさない。アイドル計数は
  `SELECT count(*) FROM information_schema.processlist WHERE id <> connection_id() AND user <> 'event_scheduler'`。
  停止は `mysqladmin shutdown` → pid 消滅を待つ → それからデータディレクトリ（Postgres と同じ
  規則。mysqld が止まらず回り続ける危険がその理由）。
- **メモリの門**: `/sys/fs/cgroup/memory.max` が数値で 1 GiB 未満なら `af-db up mysql` は exit 6 で
  拒み、そう言う（`AF_DB_MEM_GATE=0` で無効化）。
- **URL**: ソケット `mysql://root:<pw>@localhost/<db>?socket=<sockpath>`、`--tcp`
  `mysql://root:<pw>@127.0.0.1:<port>/<db>`。`af-db url --format=go-dsn` は `go-sql-driver` の形
  （`root:<pw>@unix(<sock>)/<db>` / `root:<pw>@tcp(127.0.0.1:<port>)/<db>`。Postgres の `go-dsn`
  は URL そのもの）。`af-db env` に `AF_DB_URL_MYSQL` が加わる。作業コピーごとの DB 名・突き合わせ・
  `--db=NAME` は Postgres と全く同じ。
- **`status --json` に追加**: インスタンスごとに `version`（サーバの版文字列）と `rssBytes`
  （`/proc/<pid>/status` の VmRSS。Postgres は postmaster と子の合計）。
- **テスト用の鉤**: `AF_DB_IDLE_SECONDS` で 30 分のアイドル窓を上書き（ループのテストは 5 秒）。
  `AF_DB_MYSQL_ROOT`・`AF_DB_MYSQL_LIBS` は上のとおり。
- **Agent の HTTP API**（Console の源。`workspace/agent/routes.go` の `/env/toolchains` の隣に登録）:
  - `GET /env/databases` → `{"engines":[{"engine":"postgres","major":"17","installed":true,
    "state":"absent|installing|starting|running|stopped|error","version":"17.11","rssBytes":n,
    "port":n,"datadir":"…","urlSocket":"…","urlTcp":"…","databases":{"<db>":"<dir>"},
    "lastUsedAt":"…","lastError":""}, {"engine":"mysql","major":"8.4",…}]}`。URL はパスワードを
    含む。CP は中継するだけで本文を保存しない。
  - `POST /env/databases/{engine}/start|stop|reset`（`stop?purge=1`）。`start` は即座に `state` を
    `installing` か `starting` で返し、作業は Agent 内で続ける。`GET` が進捗と `lastError` を返す。
    `stop` と `reset` は同期。
- **受け入れ（M2）**: `af-db` を一度も見ていない HOME で、`AF_DB_MYSQL_ROOT` を展開済みの `minimal`
  tarball に、`AF_DB_MYSQL_LIBS` を剥がしたライブラリに向けて：`af-db up mysql` → `af-db url mysql`
  → `mysql` で `CREATE TABLE … JSON` / `SELECT j->>'$.a'` の往復 → `status --json` の `rssBytes`
  が 226 MB 前後 → `AF_DB_IDLE_SECONDS=5` でループが止める → `down --purge` で `mysqld` も
  データディレクトリも残らない。Postgres の 4 PASS / 0 SKIP は引き続き緑。

### M3 — Console のカード・CP の proxy・文書（`control-plane/routes.go`・`console/`・docs）

- **CP**: `GET /api/env/databases` と `POST /api/env/databases/{engine}/{action}` を `rest` として
  `/api/env/toolchains`（`routes.go:769`）の隣に登録。CP はそれ以外触らない。
- **Console**: Workspace 設定の Env タブに「データベース」カード
  （`console/src/features/settings/workspace/` に `EnvTab.tsx` と並べて新規 `EnvTabDatabases.tsx`）:
  エンジンごとに 1 行——版・状態・常駐サイズ・ポート・コピー付き URL（ソケット／TCP 切替）・
  Start / Stop / Reset（Stop に「データも消す」）、`installing|starting` の間は 5 秒ごとに GET して
  「導入中…」、`lastError` はその場に表示。文言は `console/src/lib/i18n` で en / ja。`oxlint` きれい、
  dom テストは `EnvTabNode.dom.test.tsx` に倣い API はモック、
  `NODE_OPTIONS=--max-old-space-size=3072 npm run build` が通る。
- **文書**（en/ja）: 利用者ガイドにカードと MySQL（メモリ約 226 MB、JVM ビルドの前に止める、arm64 の
  導入は 909 MB のダウンロード）。`workspace/notes/environment.md` に MySQL の 1 文。
  `guide/ref/features.md` に Env タブの機能表があれば 1 行。
- **受け入れ（M3）**: dom テストとビルドが緑。モックした `GET` に対するヘッドレス Chromium の
  スクリーンショットを PR に添える。実カードは次の開発配備の後に確認（M2 を merge した Agent が要る）。

## P1 受け入れ（2026-09-17）

P0 と同じ形：3 レーン、各レビュー子、指摘は peer メッセージで著者へ、merge は M1 → M3 → M2
（M3 が M2 より先でも害は無い：Agent に `/env/databases` が無い間、カードは `lastError` を出すだけ）。

| レーン | ブランチ | 指摘 送／修正 | merge |
|---|---|---|---|
| M1 供給（`install-mysql`・`Dockerfile` の 3 ライブラリと `mysql` ピン） | `temp/svxno7x` | 7 / 7 | `53344c2e` |
| M3 Console（`EnvTabDatabases.tsx`・CP の `rest` ×2・ガイド・環境ノート） | `temp/ski4oxb` | 14 / 13 | `3565b8c5` |
| M2 実行（`internal/afdb` の MySQL エンジン・`Major` の文字列化・`status` の `version`/`rssBytes`・`GET|POST /env/databases`） | `temp/se2o2ag` | 12 / 12 | `3474fea0` |

**受け入れは親が、`af-db` を一度も見ていない HOME から回した**（このコンテナ・x86_64・merge 後の
ブランチ。`AF_DB_MYSQL_ROOT` は展開した `minimal` tarball、`AF_DB_MYSQL_LIBS` は剥がしたライブラリ——
このイメージは `mysql` ピンより古く 3 ライブラリも無いため）：`af-db up mysql` は
`--initialize-insecure` 込みで 15.9 秒。URL 2 形と `--format=go-dsn` は契約どおり。
`CREATE TABLE … JSON` / `SELECT j->>'$.a'` はソケット**と** `127.0.0.1` の両方で `42` を返した
（`root@127.0.0.1` が存在する）。`status --json` は `version` 8.4.6・`rssBytes` 233 MB。パスワードは
ログにもレジストリにも無い。`down mysql --purge` で `mysqld` もデータディレクトリも残らない。続けて
同じ HOME で `af-db up` が Maven Central から Postgres 17.11 を導入して 9.6 秒で起動し、
`TestPostgres|TestSchemaDialectParity` は **4 PASS・0 SKIP**、`down --purge` もきれい。
`AF_DB_IDLE_SECONDS` でのアイドル停止は M2 のレビュー子が観測した（2 秒窓・2 tick 目で停止）。
ここでは回していない——Agent のループが要る。`workspace/agent` と `control-plane` の `gofmt`・
`go vet`・`go test -count=1 ./...` はきれい。ブラウザ捕捉のテスト 1 本（`internal/browserx`、この
ADR は触っていない）が受け入れと並走した負荷で 1 回落ち、単独では 2 回通った。

### レビューで見つかった契約の訂正（上の P1 の契約はこれらの行で上書きされる）

- **M1・arm64 の部分集合**：`lib/private/icudt*l/`（ICU データ。無いと `mysqld` は起動するが毎回
  `MY-013829` を警告）を足し、`lib/plugin/debug/` を明示的に除く——GNU tar の `*` は `/` を跨ぐ。
  strip 前の部分集合の実測：241 ファイル・762 MB（`bin` 534 MB、うち `mysqld` 514 MB。`lib/private`
  136 MB。`lib/plugin` 82 MB。`share` 11 MB）。
- **M1・x86_64 の導入時間**：66 MB → 446 MB で約 10 秒（ガイドの「数分」は arm64 の数字）。
- **M1・今日のイメージ**：配備中のイメージは全部 `mysql` ピンより古く、Postgres と違って metadata の
  退避も無いので、焼き直すまで `install-mysql` は exit 1。Console のカードには `lastError` として出る。
- **M2・生存判定**：pid ファイルが無ければレジストリの pid を見る。`stop` が失敗しプロセスが生きて
  いれば `--purge` を拒む。最初の版は動作中の `mysqld` の下でデータディレクトリを消した（実測：38 秒で
  132 万行のエラー、`SIGKILL` でしか止まらない）——P0 由来の `isPGRunning` も同型で、同時に直した。
  `mysqld` は `Wait()` するので、pid 判定を欺くゾンビにならない。
- **M2・初期化**：`ALTER USER` のパスワードは argv でなく stdin（`/proc/*/cmdline` は共有）。継承した
  `MYSQL_PWD` は捨てる。`.pass` は `ALTER` 成功後にだけ書く。`CREATE DATABASE IF NOT EXISTS`（purge
  状態から `url` を 2 本同時に走らせると `ERROR 1007` を取り合った）。
- **M2・アイドル**：`AF_DB_IDLE_SECONDS` は窓だけでなくループの周期も縮める。窓そのものは P0 の
  二段（`lastUsedAt` から閾値、さらにアイドルを初めて見てから閾値）なので、実効は 30〜60 分＋周期で
  あって 30 分ではない。
- **M2・`version`**：両エンジンとも短い形（`SHOW server_version` → `17.11`、`8.4.6`）。
- **M2・HTTP**：`start` は `installing` → `starting` → `running`。失敗した `start` は `state=error` と
  `lastError` を残し、後の `stop` / `reset` の成功で消える。遷移は compare-and-swap で `start` 2 連打が
  競合しない。全欄を常に出す（`omitempty` 無し）。
- **M3**：URL はパスワードを伏せて表示し、コピーは素の値。`stopped` の行は URL 無しで Start だけ。
  `reset` は running のときだけ。「データも消す」停止は確認を挟む。ポーリングは `installing|starting`
  の間は再アームされ、アンマウントで止まる。カードのあるタブは「Env」でなく「ツールチェーン」で、
  ガイドもそう言う。

見つけたが残したもの：purge 後の `status --json` の `databases` が `null`（見た目だけ）。
`TestMemoryGateParsing` が解析の論理を呼ばず複製している。`ALTER` 失敗後のデータディレクトリ削除が
`SIGKILL` した pid の消滅を待たない。受け入れ中、このコンテナには他セッションのテスト残骸
（`TestMySQLIdleStop` の `mysqld` 1 本と `/tmp/tmp.*` 配下の `postgres` 3 本）が動いていた。触っていない。

**次**：開発配備のイメージを焼き直して配備する（`mysql` ピン・3 ライブラリ・`af-db` シム・`postgres`
のピン経路は、それまで全部未通過）。それから実カードと、作業ディスク上のデータディレクトリでの
ECS の初回。

## 焼き直したイメージの上で（2026-09-17・初の実機）

開発配備のイメージを `9d5effa5`（#719 のマージ）から焼き直し、このコンテナに配備した。P1 が求めた
ものは載っている——`/usr/local/bin/af-db`、`versions.json` の `postgres` / `mysql` ピンとアーキ別の
sha、`libaio1t64` / `libnuma1` / `libncurses6`。P0・P1 の受け入れが届かなかった 2 経路を、`af-db` を
一度も動かしていない HOME から測った。どちらも何か出た。

**ピンによる供給経路は通る**。`install-postgres` 1.9 秒・`af-db up` 3.3 秒、`control-plane` の完了条件は
再び **4 PASS・0 SKIP**。`install-mysql` はピンの 8.4.6 を落とし、`versions.json` の `mysql_sha256` が
実物の tarball を検証した——Postgres と違ってメタデータの代替経路が無い分、ここまで一度も通って
いなかった経路が通ったことになる。

- **ライブラリ 3 本では足りない。`libaio.so.1` は trixie が配るものではない**。MySQL のバイナリは
  `libaio.so.1` を要求するが、`libaio1t64` が入れるのは `libaio.so.1t64` で**互換シンボリックリンクは
  付かない**。そのため焼き直し後のイメージでも `install-mysql` は
  `unresolved libraries: libaio.so.1` で exit 3 のままだった。P1 の受け入れがこれを見なかったのは、
  当時の `AF_DB_MYSQL_LIBS` のディレクトリに、焼き込み前に拾い集めた手製の
  `libaio.so.1 -> libaio.so.1t64.0.2` が入っていたから。64 ビットでは t64 版は ABI が同一なので、
  直し方はそのリンク 1 本——ただし `install-mysql` 自身が、バイナリの `RUNPATH`
  （`$ORIGIN/../lib/private`）にあたる `lib/private` へ張る。実行時に `LD_LIBRARY_PATH` を触る必要が
  無く、同じコードで arm64 も賄える。張り先は `ldconfig -p`（`dev` でも読める）が教える。`t64` の
  相方が無い soname はこれまでどおり `mysqlCheckLDD` が報告する。直した後の実測（`AF_DB_MYSQL_LIBS`
  はどこにも無し）: 導入 7.5 秒、`af-db up mysql` 15.0 秒、ソケット越しの `SELECT j->>'$.a'` が `42`、
  `version` 8.4.6・`rssBytes` 233 MB、`down --purge` はきれい。`AF_DB_MYSQL_LIBS` は手順ではなく
  逃げ道として残す。
- **Console のカードは確かめられなかった。理由はこの ADR と関係が無い**。動いている Agent の
  `GET /env/databases` は **404** を返す（`/env/toolchains` は 200）。`/proc/7/exe` が指しているのは
  `/home/dev/.local/bin/workspace-agent`——P0 の受け入れ中にレーンが置いていった P0 世代の手元
  ビルド（2026-09-17 06:29）だった。entrypoint の `exec workspace-agent` は `PATH` 経由で、
  `~/.local/bin` が先勝ちし、`~/.local` は再起動でも recreate でも消えない。つまり置き忘れた
  ビルド 1 本が、そのコンテナの Agent を恒久的に乗っ取る。`af-db` シム
  （`exec workspace-agent af-db "$@"`）も同じ形で、Go 側は自分の再実行に既に
  `/usr/local/bin/workspace-agent` を直書きしている（`paths.go:126`・`afdb/cmd.go:770`・
  `afdb/mysql.go:77`）。置き忘れは削除してワークスペースを再起動し、`CMD` と `af-db` シムは
  `/usr/local/bin/workspace-agent` を名指すようにした（次のイメージ以降は再発しない）。素の
  `workspace-agent` を呼ぶセッション側のコマンド（`record-exit`・`record-terminal`・
  `install-kiro --if-needed`・`install-awscli`）は同じ形だが、利用者自身のシェルで動き影が見える分
  まだましなので触っていない。焼き直したイメージが配備されるまで、カードの正常系は見えない
  ——届くのは `lastError` だけ。

### 再起動後——カードの API は生き、訂正がもう 2 つ

置き忘れを消してワークスペースを再起動すると、`/proc/7/exe` はイメージの Agent になり、
`GET /env/databases` は全フィールド入りの 200 を返す。叩いてみた結果：`POST …/postgres/start` は
6 秒未満で `starting` → `running`（`version` 17.11・`rssBytes` 47〜49 MB・確保したポート）、
この作業コピーから `af-db url` を打つとその DB が payload に現れる。`POST …/mysql/start` は
`state=error`——このイメージは上の `libaio` 修正より前なので、今日のイメージの利用者が見るのと
同じ姿である。

- **古い版の登録簿があると全部止まる**。再起動後の最初の `start` は
  `registry parse: json: cannot unmarshal number into Go struct field Instance.instances.major`
  で失敗した。P0 の版は `"major": 17` を数値で書き、P1 で string にした。`~/.config` は recreate
  でも消えない。契約の「登録簿は P0 で新設・未配備だから移行不要」はイメージについては正しいが、
  古い版が一度でも動いた HOME には当てはまらない——そして読めない登録簿は、それを直せるはずの
  動詞ごと止める。`Instance.UnmarshalJSON` で両方受けるようにし、解けない時はファイル名を出す。
  （`af-db status` は空の一覧を返してこの問題を隠していた。HTTP 側だけが報告していた。）
- **`lastError` は理由を運ばなければ意味が無い**。カードに出たのは
  `install-mysql 8.4 failed: exit status 3` だけで、説明になる `libaio.so.1` の行は Agent の
  ログにしか無かった。導入コマンドの末尾数行をメッセージに足す。Console が汎用の `*_failed`
  コードで学んだのと同じ教訓。
- **記録だけ・変えていない**：API の `urlSocket` はパスワードを素で運ぶ。決定 9 のカードが
  「表示は伏せ字・コピーは丸ごと」だからで、つまり CP はワークスペースの資格情報をブラウザまで
  中継する。`af-db status --json` が意図的に載せないのと対照的である。

### カードの URL は誰も持っていないデータベースを指していた（契約変更・M2＋M3）

描画されたカードを最初に見て分かったのは、コピーが渡すのが
`postgres://…/af_dev_12fbd7`——`DBNameFor("/home/dev")`、つまり **Agent プロセス自身の
ディレクトリ**だということだった。その URL で `psql` を打つと
`FATAL: database "af_dev_12fbd7" does not exist`。一方、登録簿にあった唯一のデータベースは
この作業コピーの `af_agent_fleet_wip_szkxzgu_9af42b` である。Agent は「呼び出し側の作業
コピー」を解決できない——自分のものしか持っていない——のだから、エンジン単位の URL は
構造上必ず間違いで、しかも HTTP 経路は自分が宣伝した DB を作りもしない。

そこで `EngineStatus` から `urlSocket` / `urlTcp` を落とし、`databases` を一覧にした：
`[{name, dir, urlSocket, urlTcp}]`、名前順、稼働中のときだけ中身が入る。カードはデータベース
ごとに 1 ブロック（名前・作業コピー・Socket/TCP 切替・コピー）を描き、空のときは「作業コピーで
`af-db url` を実行してください」と言う。空が `[]` になったので、P1 受け入れで残した
`databases: null` の見た目の問題も消えた。ガイドのカードの節も両言語で同じことを言う。
検証は両側の単体テスト（`TestDatabaseEntriesPerWorkingCopy` と、2 行目の URL をコピーする
DOM テスト）。実機のカードに載るのは次の焼き直しから。

### 修正を積んだイメージで確認した（2026-09-17・同日）

#721 のマージから開発配備のイメージを焼き直し、ここへ配備した。上の 2 回が約束しかできな
かったことを、回避策を一切使わずに実測した：

- `/proc/7/exe` は `/usr/local/bin/workspace-agent`、シムも
  `exec /usr/local/bin/workspace-agent af-db "$@"`。PATH の影が Agent を奪うことはもう無い。
- `af-db up mysql` を未経験の HOME から：端から端まで **27.6 秒**。ログに
  `[install-mysql] linked libaio.so.1 -> /lib/x86_64-linux-gnu/libaio.so.1t64 (Debian t64 soname)`
  が出て、`AF_DB_MYSQL_LIBS` は未設定。`down mysql --purge` もきれい。
- `install-pg-client` を新しい HOME へ：**1.8 秒**（17.11-0+deb13u1）。前回は既に入っていて
  取れなかった数字。
- `GET /env/databases` は新しい形（エンジン単位の URL は無く、`databases` が一覧）で、
  **カードがコピーする URL が実際につながる**。`af_agent_fleet_wip_szkxzgu_9af42b` のソケット形・
  TCP 形の両方が `select current_database()` に自分の名前を返した。前のイメージの URL は
  `database "af_dev_12fbd7" does not exist` だった。

未検証のものは未検証のまま：ECS の作業ディスク上のデータディレクトリと、arm64 の MySQL。

### arm64 と ECS をようやく測った（2026-09-17・開発配備）

`0.21.1-dev-18cd6e52` を開発配備へ載せ、メンバーのワークスペースを起こした：`uname -m` は
**aarch64**、`workspace-agent 0.21.1-dev-18cd6e52 (linux/arm64)`、m8g.large（6.87 GB・2 vCPU）。
テナントのスロットクラスが既に `arm` だったので管理側の変更は不要だった。実行は別配備から
`kind=shell` のセッション（エージェントを挟まない素の端末）で駆動し、下の数字はすべて
`GET /api/fs/file` で生バイトとして読み戻したもの。

- **arm64 の MySQL は動く。部分集合がまさに効いている**：`install-mysql` は端から端まで
  **30.9 秒**（909 MB の `aarch64` tarball → 部分展開 → `stripped 176 ELF file(s)` →
  `linked libaio.so.1 -> /lib/aarch64-linux-gnu/libaio.so.1t64`）で、ディスク上 **147 MB**
  （`mysqld` 63 MB）。x86_64 の 446 MB に対してこの大きさ。`lib/private/icudt77l` はあり、
  `lib/plugin/debug` は無い——P1 の訂正 2 点はそのとおりだった。`ldd` は `lib/private` 経由で
  `libaio.so.1` を解決する。`af-db up mysql` は **5.2 秒**、エラーログに `MY-013829` は **0 件**、
  `[ERROR]` も 0 件。`SELECT j->>'$.a'` はソケットでも `127.0.0.1` でも `42`。`version` 8.4.6・
  `rssBytes` 227 MB、`down mysql --purge` で `mysqld` も datadir も残らない。ガイドの arm64
  「数分」は、安全側に外れた記述になった。
- **arm64 の Postgres**：このアーキの `postgres_sha256` でピンされた Zonky の
  `linux-arm64v8` jar。導入＋`initdb`＋起動で **1.6 秒**。`install-pg-client` は **0.7 秒**。
- **決定 4′ はこの ECS 配備では発動しない。`AF_WS_SCRATCH` が設定されていない。** datadir は
  `~/.local/state/af-db/postgres-17/data` に置かれ、停止前に書いた行は**起動し直しても残って
  いた**（`select count(*)` が 1）。つまり「ワークスペースを止めると消える」は、今日の ECS で
  利用者が見る姿ではない——作業ディスクが注入されていれば見る姿ではある。変数が無い間は
  `--persist` の違いは `fsync` だけで、既定も `--persist` も home に落ちる。ワークスペースに
  作業ディスクを与えるかは ADR 0044 の問いでこの ADR の問いではないが、**持っていない ECS の
  挙動を ADR が主張し続けるのはやめる**。
- **停止をまたいで pid ファイルが残り、`af-db up` はそのまま起動する**。再起動後の最初の `up` が
  `pg_ctl: another server might be running; trying to start server anyway` を出した。コンテナごと
  死んだので `postmaster.pid` が残っており、誰も掃除しない。古い pid が死んでいたので 0.2 秒で
  正しく起動したが、利用者に届く合図はこの 1 行だけで、**pid 番号が再利用されていた場合を
  この経路は区別しない**。直さずここに記録する。

### リセットにもデータベース名を渡すようにした（契約変更・M2＋M3）

実機のあとのレビューで、同じ間違いが POST 側に残っていたと分かった。カードのリセットは
engine しか送らず、`resetDB(engine, major, "")` は `DBNameFor(ResolveDir())`——Agent 自身の
ディレクトリ——に落ちる。Postgres では `DROP DATABASE IF EXISTS` のあとに `CREATE DATABASE` を
打つので、**押すと誰のものでもない `af_dev_…` が新規に作られ**、カードの行はどれも無傷のまま
だった。`POST /env/databases/{engine}/reset` は `db=<name>` を必須にし（無ければ 400
`db_required`、`--db` の書式に合わなければ 400 `bad_db`）、リセットのボタンはデータベースの行へ
移した。押した行のデータベースをリセットし、確認ダイアログがその名前を言う。

**次**（この実機で閉じた分を除いて変わらず）：Agent がイメージのものであるワークスペースでの
Console カード、作業ディスク上のデータディレクトリでの ECS 初回、arm64 の MySQL。
