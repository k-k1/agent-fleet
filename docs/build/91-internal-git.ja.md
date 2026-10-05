---
audience: "内部 git プロバイダに触れる人"
source_of_truth: "コード"
updated: "2026-09"
---

# 91. テナント内部 git プロバイダ（bare + smart HTTP）

[English](91-internal-git.md) | 日本語

関連: [01](01-architecture.ja.md) · [03 §3.4](03-control-plane.ja.md#34-起動時の配線-鍵とトークン) ·
[05](05-api.ja.md) · [06](06-data.ja.md) · [07 §7.6](07-security.ja.md#76-シークレット管理と封筒暗号) ·
ADR [0010](../decisions/0010-internal-git-provider.ja.md)（作るかどうか）·
[0003](../decisions/0003-ssh-to-connections.ja.md)（git 認証は接続の一種）

## 91.1 目的

テナントがリポジトリを**フリートの中だけで**持てるようにする。外部アカウントは介さない。
CP がテナントごとの bare リポジトリを smart HTTP で配信し、既存のプロバイダ抽象
（接続・リポジトリピッカー・資格情報ヘルパー・clone・SCM 閲覧）にそのまま載せる。

用途は 3 つ: **チーム内の共有**（同じテナントのメンバー同士で、リポジトリとエージェントが
push したブランチを共有する）、**エージェント用の非公開の作業場**、**コードを外に出さない**
（コンプライアンスや隔離のため。外部の資格情報も持たずに済む）。

**スコープ外**: プルリクエスト・レビュー・CI と、細かい権限。メンバーシップによる読み書きが
モデルのすべて（§91.5）。プルリクエスト・レビュー・CI が必要になったら、これを育てずに既存の
フォージをホストする、と ADR 0010 が決めている。

## 91.2 なぜこの形か

決定と退けた選択肢は ADR 0010 にある。この形が拠って立つのは次の点。

- **CP に置く**のは、**テナントを知っている共有コンポーネントが CP だけ**だから。
  ユーザーごとのコンテナは自分のワークスペースに閉じていて、横断して共有できない。
- **bare リポジトリ＋ git 自身の `git http-backend`** なら、最小のコードで clone・fetch・
  **push** まで成り立ち、既存の SCM ビュー（コミットグラフなど）は clone 先でそのまま動く。
- clone・閲覧・コミットは**もともとホストに依存しない**ので、追加は 3 ブロックで済む:
  CP 側の git サーバ、トークン注入、プロバイダ登録。

## 91.3 全体の形

```
  Console ──(X-AF-Tenant)──▶ Control Plane ───proxy /api──▶ ユーザーごとの agent
     │                          │  ▲                               │  git clone / fetch / push
     │ 「内部リポジトリ」タブ、    │  │ CP ネイティブ（agent を経由しない） ▼
     │ リポジトリピッカー          │  │            <PUBLIC_BASE_URL>/git/<slug>/<repo>.git
     └── /api/internal-git/* ───┘  │                               ▲
                                   └── smart HTTP (git-http-backend) + LFS
        bare リポジトリ: <WS_DATA>/git/<slug>/<repo>.git    Basic 認証。パスワードは
                                                          資格情報ヘルパーが渡す
                                                          メンバーシップごとのトークン
```

- **一覧・作成・閲覧は CP ネイティブ**（`/api/internal-git/*`。登録は `routes.go`
  `registerInternalGitRoutes`）。リポジトリの持ち主は CP なので、「プロバイダはすべて
  agent を経由する」からの**意図的な例外**である。
- **clone と push はワークスペース内の git から**
  `<PUBLIC_BASE_URL>/git/<slug>/<repo>.git` へ行く。共有のコンテナ網ではなく、Console が
  配信されているのと同じ配備の公開アドレスである。`/git/` は自前で認証する（§91.5）ので
  セッションの門から外してある（`exemptPrefix("/git/")`）。`/git/{slug}/{repo}/info/lfs/`
  配下の LFS のルートは、smart HTTP の受け皿より先に登録する。
- **`PUBLIC_BASE_URL` が無いと git としては無効になる**: `/git/` のルート（smart HTTP、
  LFS の転送とロック）はすべて認証の前に 503 を返し（`requireBase`）、リポジトリの作成は
  503 `not_configured`、ワークスペースへのトークン注入も行わない。agent が以前に書き込んだ
  資格情報はストアに残るが、もう何も開けない。セッションで認証する管理 API（一覧・閲覧・
  改名・削除）は動き続けるので、既存のリポジトリを確かめたり消したりはできる。その間の
  clone URL はベースの無い相対パスになる。

## 91.4 ストレージ

- bare リポジトリは `<WS_DATA>/git/<tenant-slug>/<repo>.git` に置く。ワークスペースとは別の
  ツリーである。既定テナントも含め、どのテナントもここでは `<slug>` のディレクトリを持つ
  （ワークスペースのホームでは既定テナントだけ平置きなのと違う）。ディスク上では、URL の
  綴りではなく、トークンのテナントの正規の slug を使う。
- `git` は `WS_DATA` 直下の平らな名前空間の 1 項目で、既定テナントのメンバーのホーム
  （`<WS_DATA>/<user_key>`）、他テナントのディレクトリ（`<WS_DATA>/<slug>/`）、CP 自身の
  ファイルと並ぶ。そうした名前は `control-plane/internal/datalayout` が一覧に持ち、ストアは
  予約名または既定テナントのメンバーのキーと同じ新しいテナント slug と、予約名またはテナント
  slug と同じキーでの新しい既定テナントのメンバーシップを断る（#1214）。保存済みの衝突は起動時に
  ログに出すだけ。`dataRoot` にリテラルを繋ぐコードと、`deploy/` の `$WS_DATA/<name>` で予約に
  無い名前は、モジュール直下のテストが落とす。ECS のプロファイルは影響を受けない。ホームは
  slug やキーではなくメンバーシップ ID で切った EFS のアクセスポイント（`/home/<id>`・
  `/claude-config/<id>`・`/home-keep/<id>`）である。
- **一覧と配信の正はデータベースで、ディレクトリ走査ではない。** `git_repo` テーブル
  （テナントと名前ごとに 1 行。既定ブランチと作成したメンバーシップを持つ）が一覧だけでなく
  smart HTTP ハンドラの門にもなる。例外は GC ジョブで、これはディレクトリツリーを歩く（§91.9）。
- **LFS オブジェクトはリポジトリ自身のディレクトリの中に内容アドレスで置く**
  （`<repo>.git/lfs/objects/<oid[0:2]>/<oid[2:4]>/<oid>`）。改名すれば一緒に動き、削除すれば
  一緒に消える。`lfs_object` テーブル（テナント・リポジトリ・oid・サイズ）でテナントの合計を
  走査でなく 1 回の合計で得る。`lfs_lock` は LFS のロックを持つ。改名と削除は、
  `git_repo` の行と LFS の 2 テーブルを 1 つのトランザクションで変える（`RenameGitRepo` /
  `DeleteGitRepo`）。削除はディレクトリを消す前にこれを確定するので、ストアのエラーなら何も
  変えずに要求を失敗させる。改名は先にディレクトリを移し、トランザクションが失敗すれば戻す。
  アップロードはオブジェクトを公開する前に台帳の行を書き、書けなければ要求を失敗させる。
  公開済みのオブジェクトは二度とアップロードされないからである。
- テーブルそのものの説明は [06](06-data.ja.md) にある。**トークン用のテーブルは意図して
  持たない**。

## 91.5 認証とトークンモデル

認証面は 2 つある。

**管理・閲覧の API** は、通常のセッションの本人確認とテナント解決（`X-AF-Tenant`、
`withMembership` 経由）をそのまま使い、解決したテナントにスコープする。追加の資格情報は
要らない。有効なメンバーシップに加えてロールを確かめる（`canManageRepo`）: リポジトリの
**作成**には push できるロール（`canPush`）が要り、**改名と削除**にはさらに、そのリポジトリを
作った人（`git_repo.created_by`、メンバーシップの ID）か `tenant_admin` であることが要る。
それ以外は監査の依頼を書く前に `403`（`git_repo_create_forbidden` /
`git_repo_manage_forbidden`）で断る。`created_by` の無い行を扱えるのは `tenant_admin` だけ。
一覧はリポジトリごとに `can_manage` を返し、Console は成功する場合にだけ改名と削除を出す。
一覧・閲覧・clone はこれまでどおり全メンバーに開いている。

**git の面**は、**メンバーシップごとの決定的な HMAC トークンを使い、トークンのテーブルは
一切持たない**: `afg_<base64url(membership id)>.<tag>`。tag は、メンバーシップの
**git トークン epoch**（`membership.git_token_epoch`）におけるメンバーシップ ID の
HMAC-SHA256 を切り詰めたもの（`mintGitToken`・`verifyGitToken`）。epoch 0 は ID だけを MAC
し、epoch が無かったころのトークンとバイト単位で同じなので、列の追加で失効したものは無い。
epoch が 1 以上なら `NUL "af-git-token-epoch" NUL <epoch> NUL <id>` を MAC し、NUL を含む ID を
名乗るトークンは断るので、2 つの (epoch, id) の組が同じ入力になることは無い。署名鍵（`gitSignKey`）は
配備のトークン署名用マスターから導出する — `AF_MASTER_KEY`、それが無ければ `WS_DATA` 配下に
置く乱数の鍵（[03 §3.4](03-control-plane.ja.md#34-起動時の配線-鍵とトークン)）。
したがって **CP はトークンを作り直せる**: 注入は冪等で、CP はトークンを保存せず、復元の
問題も無い。（個人アクセストークンの表を流用する案は却下した。平文を復元できないので注入が
冪等にならず、利用者自身のトークン一覧も汚すため。）

ワークスペースの起動のたびに、`workspaceExtraEnv` が `AF_INTERNAL_GIT_HOST`
（`PUBLIC_BASE_URL` のオーソリティ。ポートを含む）と `AF_INTERNAL_GIT_TOKEN` を注入する。
agent は起動時に `seedInternalGit` でこれを通常の git 資格情報（`x-access-token` と
トークン）として自分の資格情報ストアに書き込み、**統一の資格情報ヘルパー
（`runCredHelper`）はストアにあるどのホストにも答える**ので、clone と push はそれ以上
何もしなくても認証が通る。鍵がポートを含むのは、git が clone URL にポートが明示されて
いると `host:port` で問い合わせるからである。リモートを internal と判定する
`gitProviderHost` はポートを除いたホスト名で比べる。

smart HTTP と LFS（batch・転送・ロック）のハンドラは `authorizeGitRepo` を共有し、これが
このトークンの唯一の検証者である。メンバーシップの**現在の** epoch をその場で読み
（`GitTokenEpoch`。有効なメンバーシップのみ、キャッシュなし）、その epoch の tag だけを
受け入れ、メンバーシップを**その場で**引き（`GetMembershipByID`）、**毎リクエスト**次を強制する:

- URL の slug がトークンのテナントと一致すること — **他テナントのリポジトリには届かない**（403）。
- リポジトリ名が正しく、`git_repo` にあること（無ければ 404）。
- 読むには有効なメンバーシップが要り、**push はロールで決まる**（`canPush`）。push できる
  ロールは `member` と `tenant_admin`。列は自由なテキストだが、メンバーシップを作る・
  ロールを変えるコードの経路（参加・招待・ロール API）が書くのはこの 2 つだけなので、
  実際にはこの検査はまだ何も断らない。読み取り専用のロールができたときに断るためにある。

**失効はその場で効く**: メンバーシップを無効にすると、同じ決定的トークンがただちに通らなく
なる。更新すべきトークン表は無い。

**1 つのメンバーシップのトークンの再発行**（#1199）は、メンバー詳細にある管理者の操作
（「git トークンを再発行」、`POST /api/admin/rotate-git-token {tenant_slug,user_key}`）である。
メンバーのテナントの `tenant_admin` か `super_admin` が行え、強制停止や home の掃除と同じ関門で、
監査は依頼を先に書く（`membership.rotate_git_token`）。epoch を 1 文で進めるので、古いトークンは
次のリクエストから通らない。外したメンバーも再発行できる。再招待は同じメンバーシップ ID を
有効に戻すからである。署名用マスターを変えれば、これまでどおり全トークンが一度に変わる。

新しいトークンは、まだ古いトークンを env に持つメンバーのワークスペースに届ける必要がある:

- メモ化したランタイムを捨てる。これはこの CP のメモにしか届かないので、すべての起動で、
  `Start` の直前（起動ロックとライフサイクルのリースを持った状態）に、ランタイムの env が持つトークンの
  epoch（`cachedRT.gitEpoch`。epoch を読めずトークンを注入しなかったときは `-1`）を現在の epoch と比べ、違えばランタイムを作り直す
  （`refreshGitTokenForStart`）。別レプリカのメモも、捨てた後にビルドが書き戻したメモも、
  死んだトークンを注入できない。
- **起動中**のワークスペースにはすぐ渡す: CP が Agent の `/internal-git/token` に epoch と一緒に
  `PUT` し、Agent は `seedInternalGit` と同じすべてのホスト名で保存する。再起動はメンバーのセッションを殺すので採らず、Agent に取りに来させる案は git トークンと
  一緒に漏れる資格情報が要るので採らなかった。Agent は置き換えた env トークンのダイジェストを
  記録するので、同じコンテナ内で Agent が再起動しても死んだトークンを書き戻さない。コンテナを
  新しく起動すれば新しいトークンが来て、通常どおり書き込まれる。
- この経路は **CP 専用ではない**。Console のプロキシは通さないが、関門は Agent の bearer だけで、
  それはワークスペース内のどのセッションも持っている。`PUT /connections/git` と同じ境界である:
  そうしたプロセスは Agent と同じ uid で動き、`AF_SECRET_KEY` を読んで資格情報ストアを直接書き換え
  られるので、CP の署名を足しても敷居は上がらない。呼び出し側にできるのは自分のワークスペースの
  内部 git 資格情報の差し替えだけで、CP が毎回トークンを検証するので得るものは無い。Agent は
  CP の形をした自分のメンバーシップのトークンだけを、4 KiB 以下の本文で受け付ける。
- 2 つのレプリカが同時に再発行すると、push はどちらの順でも届きうる。起動時の env は
  `AF_INTERNAL_GIT_EPOCH` を持ち、Agent は保持しているトークンの epoch を覚え、それより古い epoch の
  push には `superseded` と答えて保存しない。
- 渡すときはワークスペースの起動ロックを取るので、進行中の起動は終わってから渡される。15 秒たっても
  ロックが空かなければ `pending` と答え、裏で渡す。応答の `workspace` は `updated`・`not_running`・
  `pending`・`failed`（ワークスペースを再起動する）・`disabled`（`PUBLIC_BASE_URL` 無し）のいずれか。
- 限界: エンドポイントより前に作られた Agent は 404 を返し、`failed` になる。**別の**レプリカで、
  epoch の比較を再発行の直前に通った起動は古いトークンで上がり、こちらの push はまだ起動中でないと
  見る。再発行はレプリカをまたぐライフサイクルのリースを取らないためで、次の起動で直る。

## 91.6 統合点

行番号ではなく、ファイルとシンボルで指す。

| 箇所 | 持っているもの |
|---|---|
| `control-plane/routes.go` `registerInternalGitRoutes` | smart HTTP の受け皿 `/git/{slug}/{repo...}`、LFS のルート、管理 API `/api/internal-git/*`、`exemptPrefix("/git/")` |
| `control-plane/git_http.go` | `gitServerAPI`、トークンの発行と検証、`authorizeGitRepo`、`canPush`、`git http-backend` の CGI ラッパ |
| `control-plane/internal_git.go` | 一覧・作成・削除・改名・ブランチ一覧、`cloneURL`、リポジトリ数の上限、監査の記録 |
| `control-plane/internal_git_browse.go` | clone なしのツリー・blob・コミット閲覧 |
| `control-plane/git_lfs.go`、`git_lfs_locks.go` | LFS の batch API と basic 転送、ロック API |
| `control-plane/git_gc.go` | GC ジョブ、LFS 孤児の回収、LFS 台帳の突き合わせ |
| `control-plane/internal/store/migrations/` `0014_git_repo.sql`・`0015_lfs_object.sql`・`0016_lfs_lock.sql` | SQLite のテーブル。Postgres では `migrations-pg/0001_init.sql` にある |
| `control-plane/main.go`、`workspace_lifecycle.go` `workspaceExtraEnv` | `PUBLIC_BASE_URL` → `internalGitHost`、起動ごとの `AF_INTERNAL_GIT_HOST` / `AF_INTERNAL_GIT_TOKEN` の注入 |
| `workspace/agent/cred_helper.go` | `seedInternalGit`・`internalGitHost`・`runCredHelper` |
| `workspace/agent/connections.go` `internalGitStatus` | 接続状態の `internal` 項目 |
| `workspace/agent/internal/gitx/git.go` `gitProviderHost` | 注入されたホストのリモートに `internal` のバッジを付ける |
| `console/src/features/repos/RepoPicker.tsx` | `internal` プロバイダのタブ。リポジトリとブランチの一覧は `/api/internal-git/*` から取る |
| `console/src/features/settings/workspace/InternalReposTab.tsx`、`InternalRepoBrowser.tsx` | 設定の「内部リポジトリ」タブ（一覧・作成・改名・削除。ワークスペース停止中も使える）と、その「参照」ボタンの先の閲覧画面 |

`git` は CP と一緒に入る: CP のイメージがインストールし（`control-plane/Dockerfile`）、
native パッケージは静的ビルドの `git` と `git-http-backend` を同梱して `GIT_HTTP_BACKEND`
をそこへ向ける（`deploy/native/af`）。この変数が無ければ CP は
`/usr/lib/git-core/git-http-backend` を使う。

**意図して手を入れていないもの**: agent のリモート一覧の switch
（`internal/gitx/git_remote.go` に internal の分岐は無い。内部の一覧は CP へ直接行く。agent を
経由すると agent → CP の認証が要るため）と、既知ホストの対応表 `connections.go` の
`gitHosts`（内部ホストは実行時にしか分からず、ヘルパーはストアにあるどのホストにも答える）。

`control-plane/git_e2e_test.go` と `git_lfs_e2e_test.go` は本物の `git` と `git-lfs` で
ハンドラを叩き、それらのバイナリが無い環境では skip する。

## 91.7 隔離とセキュリティ

- **テナント越えは毎リクエストで遮断する** — info/refs・upload-pack・receive-pack、
  LFS のすべての操作で同じ。
- **パスの封じ込め**: リポジトリ名は `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` に一致しなければ
  ならず、`..` を含むものは断る。`git-http-backend` は `GIT_PROJECT_ROOT` をテナント自身の
  ディレクトリ（正規の slug から組み立てる）にして動くので、細工したパスでもそこから出られない。
- **保存時のトークン**: agent は資格情報ストアに持ち、ストアは配備が `AF_MASTER_KEY` を設定して
  いれば暗号化される。無い場合（開発用）は平文の JSON になる
  （[07 §7.6](07-security.ja.md#76-シークレット管理と封筒暗号)）。CP はトークンを保存しない。
- **CP に git の実行面が増えるのは新しい攻撃面**である — ref とパスの入力検証は意図して厳しい。
- **LFS はすべての操作でまったく同じ認可**（`authorizeGitRepo`）を使う。オブジェクト ID は
  小文字 16 進 64 文字（SHA-256）に限り、それがパスの封じ込めも兼ねる。アップロードは
  **流しながらハッシュを取り、一致しなければ断る**（422）。容量の上限は batch の時点と
  アップロード中の両方で強制する（507）。共有ホストへの配慮から、オブジェクトはメモリに
  溜めずにストリームで扱う。

## 91.8 データの流れ

- **作成**: `POST /api/internal-git/repos {name}` が
  `git init --bare --initial-branch=<既定ブランチ>`（指定が無ければ `main`）で bare を作り、
  続けて `git_repo` の行を書く（挿入に失敗したらディレクトリを消し戻す）。clone URL を返す。
- **一覧**: リポジトリピッカーの internal タブと設定のタブは、agent ではなく CP の
  `GET /api/internal-git/repos` を呼ぶ。
- **ブランチ**: `GET /api/internal-git/repos/{name}/branches` は bare を `git for-each-ref` で読む。
- **clone**: 既存の clone の流れに clone URL を渡すだけ。トークンは資格情報ヘルパーが渡す。
- **共有**: メンバーは同じ URL にブランチを push し、互いのものを fetch する。
- **clone の後**は、グラフ・状態・チェックアウト・ファイル API など、すべてプロバイダに
  依存せず、もともと動いていたものである。

## 91.9 実装済みのもの

- **git の面**: smart HTTP での clone・fetch・push、トークン注入、作成・一覧・削除、
  プロバイダのタブ。
- **改名**（`POST /api/internal-git/repos/{name}/rename {new_name}`）は bare を移して台帳を
  更新する。台帳の更新に失敗したら移動を戻す。**既存の clone は古いリモート URL のままなので、
  更新が要る。**
- **テナントごとのリポジトリ数の上限**（テナントの制限の `max_git_repos`、0 は無制限）。
  作成時に強制し、超えれば 409 `quota_exceeded`。
- **監査の記録** `internal_git.repo.create`・`internal_git.repo.delete`・
  `internal_git.repo.rename`。削除と改名は取り消せない（bare と LFS オブジェクトが消える／
  既存の clone のリモート URL が切れる）ので、台帳にもディスクにも触れる前に
  `<action>.requested` を書き、**書けなければ `503 audit_unavailable` で断る**。終わったら結果の行を
  書く（[07 §7.7](07-security.ja.md#77-監査)）。作成はこれまでどおり事後のベストエフォート。
- **空のリポジトリも選べて clone できる**: ブランチがまだ無いとき、ブランチ一覧の
  エンドポイントは空の一覧とリポジトリの `default_branch` を返し、リポジトリピッカーが
  その名前を仮のブランチとして選択肢に出す。
- **GC ジョブ**が全 bare に `git gc --auto` を**逐次**かける（メモリへの配慮）。間隔と猶予期間は
  [03 §3.7](03-control-plane.ja.md#37-バックグラウンドジョブ) にある。
- **LFS**: batch API と basic 転送。**アップロード時のダイジェスト検証**と、原子的な公開
  （一時ファイル・fsync・rename）と重複排除（保存済みのオブジェクトを再度上げても何もしない）。
  容量の上限（`max_lfs_bytes`）は **batch の時点（batch 全体を見積もって）とアップロード中の
  両方**で強制する。**ロック API** — 作成・一覧・検証・解除。1 つのパスが持てるロックは
  リポジトリごとに 1 つまで（2 つ目の試みは既存のロックを添えた 409）。検証はロックを自分の
  ものと他人のものに分け、push 前に他人のロックに気づけるようにする。ロックの作成と解除には
  push の権限が要り、他人のロックを強制解除できるのはテナント管理者だけ。
  - **孤児の回収**は同じ GC ジョブに入っており、LFS オブジェクトを持つリポジトリだけが対象。
    参照されている ID の列挙は**git だけで行う**（CP に LFS クライアントは要らない）: オブジェクト
    ストアの小さな blob を、到達可能かどうかにかかわらずすべて読み、ポインタの ID を抜き出す。
    守るべき安全性が 2 つある: **猶予期間の間は新しく書かれたオブジェクトを残す**ので、ref が
    まだ push されていないアップロードは消されない。そして**オブジェクトの一覧か読み出しの開始に
    失敗するか、テナントを引けなければ、何も消さない。** ポインタの中身を読む途中の失敗は検出**されない**:
    それまでに読めた ID を全体とみなし、ポインタを読めなかったオブジェクトは猶予期間を
    過ぎていれば消されうる
    （[#1210](https://github.com/k-k1/agent-fleet/issues/1210)）。消すオブジェクトは
    先に台帳から外し、容量の枠を空ける。外せなければオブジェクトは次の掃除まで残す。
  - **台帳の突き合わせ**が同じパスの最初に、全リポジトリを対象に走る。オブジェクトのファイルが
    無く、`created_at` が同じ猶予期間より古い台帳の行は消してログに残す。公開（rename）に失敗した
    アップロードや、台帳を書いてから rename するまでの間にプロセスが死んだアップロードがこういう行を
    残し、放っておくと同じ oid が再びアップロードされるまでテナントの容量を多く数え続ける。
    アップロードをやり直すと行の `created_at` は今に進み、削除は同じ文の中で古さを確かめ直すので、
    進行中のアップロードの行は消えない。猶予期間が 0 なら突き合わせは走らず、掃除の途中で bare が
    動いた（改名中の）リポジトリには手を出さない。**ストアのエラー（行の一覧・行の削除・テナントの解決）は
    そのリポジトリのパスを終わらせる: それ以上は台帳の行もオブジェクトのファイルも消さない。**
- **クライアント側は何も変えなくてよい**: ワークスペースのイメージは `git-lfs` を同梱し、
  フィルタをシステムの gitconfig に置いている。LFS は同じ資格情報ヘルパーで認証する。
  パッケージ版の native ランタイムは、展開したワークスペースイメージの rootfs
  （`AF_NATIVE_ROOTFS`）の上で agent を動かすので、同じ `git-lfs` を持つ。ホストでビルドした
  agent を動かす開発用のモード（`AF_NATIVE_AGENT_BIN`）は、ホストにあるものを使う。
- **clone なしの閲覧**: bare を直接読む読み取り専用のツリー・blob・コミットの API
  （`GET /api/internal-git/repos/{name}/tree|blob|commits`）。1 MiB を超える blob、バイナリ、
  LFS ポインタは中身を返さずに印を付ける。まだ生まれていないブランチは空の一覧になる。
  **ref はダッシュで始まってはならず `..` を含んではならない。パスは先頭と末尾のスラッシュを
  取り除くので常にリポジトリのルートからの相対になり、`..` の区間と制御文字を含んでは
  ならない** — トラバーサルと、値が引数と取り違えられることの両方を防ぐ。
