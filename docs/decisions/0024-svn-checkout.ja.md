# 0024. SVN チェックアウト — プロバイダ無しのフラット作業コピー＋URL/基本認証

[English](0024-svn-checkout.md) | 日本語

- 状態: **採用・実装済み**。設計は [docs/41](../log/41-svn-checkout.md)。
- 関連: [0003](0003-ssh-to-connections.ja.md)（git 認証＝Connections）/ [0005](0005-envelope-custodian.ja.md)（封筒暗号）/
  [0010](0010-internal-git-provider.ja.md)（プロバイダ抽象）

## 背景

git だけでなく **Subversion のリポジトリでも作業したい**要求。git のような provider（GitHub/Bitbucket）は
SVN に存在せず、社内 SVN サーバは **URL ＋ 基本認証**で十分。特定の path 以下（trunk / branches/x など）を
チェックアウトしたい、違う path を複数回チェックアウトしたい、という使い方が中心。

## 決定

**SVN 作業コピーを git と同じ `~/repos/<name>` のフラットな作業コピーとして扱う。** provider 抽象には載せない。

- **フラットモデル**: フォルダ名が id。git の `.git` に対し `.svn` で種別判定。`Repo.Vcs="svn"` で
  Console が git 専用操作を出し分ける。branch/ahead/behind/worktree は持たない。
- **URL がサブツリーを表す**: 「特定 path のチェックアウト」＝ URL の一部。「違う path を複数回」＝
  複数フォルダ。これは git がクローン分離で得る隔離と同じ性質で、**worktree 不在の代替**にもなる。
- **worktree 無し**: SVN に worktree 相当が無いので、セッションはチェックアウトフォルダ内で**直接起動**。
  `ensureWorktree` は非 git を拒否し、create_session も svn dir への worktree 指定を明示エラーにする。
- **認証はストア注入型（git の cred helper は流用不可）**: SVN は git の credential-helper プロトコルを
  使わない。REST の checkout/update が暗号ストア `secrets.SVN`（URL 最長プレフィックス一致）から creds を
  引き、`svn --username … --password-from-stdin --non-interactive --no-auth-cache` で渡す。stdin 渡しで
  パスワードはプロセス一覧に出ず、`--no-auth-cache` で `~/.subversion/auth` に平文を残さない。保存は
  チェックアウト時の任意 opt-in。
- **ロックは自前で自己修復**: checkout/update が working-copy ロック（`E155004`）で落ちたら
  `svn cleanup` を挟んで 1 回リトライ。明示の cleanup 操作も用意（ローカル・認証不要）。

### 捨てた選択肢

- **専用 SVN provider/Settings タブ**: URL＋基本認証には過剰。creds はチェックアウト時に任意保存する軽量案を採用。
- **`~/.subversion/auth` に creds をキャッシュしてエージェント svn も透過認証**: 平文保存になり方針
  「秘密を平文で置かない」に反する。→ REST 経路のみ注入し、**セッション内のエージェント直 svn は
  非透過**（下記の限界）とする。
- **git cred helper の流用**: SVN は git のプロトコルを話さないため不可。

## 帰結

- 追加は Agent 側 `svn.go`（checkout/update/cleanup/info/creds）＋ `git.go` の一覧/削除分岐＋ルート
  （agent／CP 許可リスト／`auditActionTarget`）＋ Console（Repo 型・チェックアウトモーダルの Git/SVN 切替・
  svn 行の update/cleanup・worktree 抑止）。clone/閲覧/セッション起動の git 経路は無改造。
- **限界（意図）**: セッション内でエージェントが自分で叩く `svn`（update/commit）は透過認証されない。
  REST 経路（Console の更新ボタン）は creds を注入するが、対話 svn は creds を都度供給する必要がある。
- **自己署名／未信頼証明書はサーバ単位の opt-in で信頼**: 非対話では既定で失敗するので、「自己署名証明書を
  信頼」opt-in で `--trust-server-cert-failures=unknown-ca,cn-mismatch,expired,not-yet-valid,other`（旧
  `--trust-server-cert` のフルセット版）を付与する。証明書信頼は秘密ではなくサーバ属性なので認証の保存とは
  独立に扱い、checkout 時に必ず永続化して以後の update でも継続する（認証なしの公開自己署名は username 空の
  trust-only エントリ）。トレードオフ＝そのサーバの証明書検証は無効化されるため、明示・サーバ単位の opt-in に
  留める。
- **限界（環境）**: native(WSL) ランタイムはホストに `svn` が要る（不在時は `svn_missing` で明示エラー）。
