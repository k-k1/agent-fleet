# docs/41 — SVN チェックアウト対応

決定は [ADR 0024](../decisions/0024-svn-checkout.ja.md)。git だけでなく Subversion のリポジトリでも
作業できるようにする。provider は無く、**URL ＋ 基本認証**でチェックアウトし、特定 path 以下や
複数 path のチェックアウトを素直に扱う。

## 1. モデル

- SVN 作業コピーは git と同じ `~/repos/<name>` 直下のフラットなフォルダ。フォルダ名が id。
- 種別判定: git=`.git`／svn=`.svn`。一覧（`GET /repos`）は両方を認識し、svn は `Repo.Vcs="svn"`、
  `revision` / `url` を返す（branch/ahead/behind/worktree は無し）。
- **特定 path のチェックアウト**＝ URL の一部（`svn` は URL でサブツリーを指す）。モーダルは
  リポジトリ URL ＋任意のサブパス（例 `trunk`, `branches/x`）を連結する。
- **違う path を複数回**＝複数フォルダ。名前は最終セグメントから導出（`trunk` は親名にフォールバック）、
  衝突時は一意名を提案。git のクローン分離と同じ隔離になり、worktree 不在を補う。

## 2. 認証

- 基本認証（ユーザー／パスワード）。`svn --username U --password-from-stdin --non-interactive --no-auth-cache`。
  - stdin 渡し → パスワードがプロセス一覧に出ない（svn ≥1.10、イメージは 1.14）。
  - `--no-auth-cache` → `~/.subversion/auth` に平文を残さない。
- 任意保存（opt-in）: チェックアウト成功後、暗号ストア `secrets.SVN`（`{urlPrefix, username, password}`）へ
  upsert。以後の `svn update` は URL の**最長プレフィックス一致**で creds を再利用。
- 保存 creds の一覧（urlPrefix＋username・パスワードは返さない）は `GET /connections` の `svn`、
  失効は `DELETE /connections/svn?prefix=`。
- **自己署名／未信頼証明書（opt-in）**: チェックアウトモーダルの「自己署名証明書を信頼」で
  `--trust-server-cert-failures=unknown-ca,cn-mismatch,expired,not-yet-valid,other` を付与し、
  そのサーバの証明書検証を無効化する（非対話では信頼できない証明書は既定で失敗するため）。
  証明書信頼は**秘密ではなくサーバの属性**なので、認証とは独立に扱う: 認証情報の保存 opt-in とは
  別に、信頼フラグは checkout 時に必ず `secrets.SVN` の該当 prefix へ永続化し（`{trustCert}`・
  認証なしの公開自己署名リポジトリでは username 空の trust-only エントリ）、以後の `svn update`
  でも同じ証明書を信頼し続ける。旧 `--trust-server-cert`（unknown-ca のみ）ではなくフルセットを
  使うのは、自己署名がほぼ必ず伴うホスト名不一致まで許可するため。トレードオフ＝そのサーバの
  証明書検証は完全に無効化される（ゆえに明示・サーバ単位の opt-in）。

## 3. ロック自己修復

SVN は中断/kill で作業コピーがすぐロックする（`E155004: … run 'svn cleanup'`）。しかもロックは
`svn-update` エンドポイント自体を塞ぐため、エージェント任せにできない。

- checkout/update が locked で落ちたら `svn cleanup` を挟んで 1 回だけ自動リトライ（`runSvnAuthedHealing`）。
  判定文字列は `E155037`（中断された取り込み — 「run **'cleanup'**」と書く）を含むこと。詳細は
  [docs/78 §8](78-repo-import-jobs.md#8-自動修復の文字列svnlocked)。
- 明示操作: `POST /repos/{name}/svn-cleanup`（svn 行の「ロックを解除」）。ローカル・認証不要。
- リポジトリ側ロック（`svn lock`/`svn:needs-lock`）はスコープ外。必要ならセッション内で `svn unlock`。

## 4. エンドポイント（3 か所必須）

| ルート | 内容 |
|---|---|
| `POST /repos/svn` | チェックアウトを**開始**（`{url, subpath, name, username, password, save}`）。`202 {job}` — 実処理は取り込みジョブ（[docs/78](78-repo-import-jobs.md)）|
| `POST /repos/{name}/svn-update` | 最新リビジョンへ更新 |
| `POST /repos/{name}/svn-cleanup` | working-copy ロック解除 |
| `DELETE /repos/{name}` | 削除（既存を流用・svn は worktree ロジックを飛ばし `RemoveAll`）|
| `DELETE /connections/svn?prefix=` | 保存 creds の失効 |

Agent（`workspace/agent/routes.go`）・CP 許可リスト（`control-plane/routes.go`）・
監査分類（`control-plane/proxy.go` `auditActionTarget`＝`repo.svn.checkout|update|cleanup`）の
**3 か所すべて**へ登録が必要（CP は明示許可リスト）。

## 5. セッション起動

- svn 作業コピーは一覧に出れば `create_session` の `dir` にそのまま渡せる（cwd はただのフォルダ）。
- **worktree は不可**（SVN に相当機構が無い）。Console は svn 行の起動モーダルで `allowWorktree=false`＝
  その場起動のみ。バックエンドも `ensureWorktree`（非 git 拒否）＋ create_session の明示ガードで二重に防ぐ。
- 並行作業の隔離は「別 path を別フォルダへチェックアウト」で行う。

## 6. Console

- `Repo` 型に `vcs?/revision?/url?`。
- チェックアウトモーダル（`NewRepoModal`）に Git/SVN 切替。SVN は URL・サブパス・ユーザー／パスワード・
  保存チェック・フォルダ名。送信は `svnCheckout()`（`clone.ts`）。**完了の根拠は取り込みジョブの
  `state`** であって POST の応答ではない（[docs/78](78-repo-import-jobs.md)）。走行中の作業コピーは
  `GET /repos` に出ないので、一覧に現れた時点で使える物である。
- `RepoRow`: svn 行は `r<rev>`＋URL ツールチップ、操作は起動（その場のみ）／更新／ロック解除／削除に限定
  （ブランチ切替・SCM・FF は非表示）。
- i18n: `rp.vcs*` / `rp.svn_*` / `repo.svn_update` / `repo.svn_cleanup` / `repo.revision`（ja/en 両方）。

## 7. 実装ファイル

- 追加: `workspace/agent/svn.go`, `workspace/agent/svn_test.go`
- 改修: `workspace/agent/git.go`(一覧/削除/dir 解決), `session_handlers.go`(worktree ガード),
  `routes.go`, `connections.go`, `internal/secrets/secrets.go`
- CP: `control-plane/routes.go`, `control-plane/proxy.go`
- Console: `features/repos/{store,clone,NewRepoModal,RepoRow,RepoRowConnected}.tsx`,
  `features/project/ProjectTree.tsx`, `lib/i18n/locales/{en,ja}.ts`

## 8. 残（環境依存）

- 🔴 セッション内エージェント直 `svn` の透過認証（構造的に非対応・ADR 0024 の限界）。
  → **2026-09-10 に解消**。§9 を参照（PATH ラッパーで、平文を置かずに透過認証する）。
- 実フリート再ビルド後の実機目視（実 SVN サーバでの basic 認証／更新／ロック解除／自己署名証明書の信頼）。

## 9. 追補（2026-09-10）— 再認証の経路と、直 `svn` の透過認証

利用者からの報告 2 件が起点。**「チェックアウト時に保存しなかった場合、認証情報がどこにも無いので
`svn` の認証が通らない。再入力・認証する経路が無い」**。§2 の「任意保存（opt-in）」は**片道**だった
——保存しなければパスワードは 1 回使われて捨てられ、`--no-auth-cache` により `~/.subversion` にも
残らないので、以後の更新は必ず失敗し、入れ直す画面がどこにも無い。作業コピーを消して取り直すのが
唯一の回避だった。

### 9.1 再認証（既存の作業コピーに対して後から入れる）

- `GET /repos/{name}/svn-auth` — この作業コピーがどのサーバを見ているか＋認証情報が当たっているか。
- `POST /repos/{name}/svn-auth` — **サーバに当ててから**保存する。`svn info <url>` が通らなければ
  保存しない。「保存した」が「打ち間違いを保存した」になり得ないこと自体が要件（それが利用者を
  ここへ来させた失敗そのものだから）。
- `PUT /connections/svn` — 設定 › 接続からの追加・修正（作業コピーがまだ無い場合／パスワード変更）。
- **保存先の prefix は、いま効いているエントリがあればそれ**。無ければリポジトリルート
  （`svn info --show-item repos-root-url`）。トランク単位で無効なエントリを積むと、壊れた広い
  エントリが残ったまま影に隠れるだけで直らない。§2 の trust-only エントリ（証明書だけ信頼して
  パスワードは保存しなかった状態）が、まさに「効いているが認証できない」エントリである。
- **`svn` の出力の分類はサーバ側で行う**（`svnAuthFailure`）。`update` は認証で落ちたときだけ
  `401 svn_auth_required` を返し、Console はそれを見て再認証モーダルを出す。ブラウザで
  svn のメッセージを文字列照合しない。⚠️ `E170013`（Unable to connect）は**認証失敗に数えない**
  ——到達不能なホストでも同じ文字列が出るので、ネットワーク障害に「パスワードを入れてください」
  と答えることになる。数えるのは `E170001` / `E215004` / `E175013`。

### 9.2 直 `svn` の透過認証（PATH ラッパー）

git は credential helper、`gh` は PATH ラッパー（`gh-auth-wrapper.sh`）で透過認証している。SVN は
helper プロトコルを持たず、自力で見に行く先は `~/.subversion/auth`（＝平文）だけなので、ADR 0024 は
「非対応」とした。**平文を置かずに済ませる第三の道が `gh` と同じ形**だった: `/usr/local/bin/svn` を
シム（`workspace/svn-auth-wrapper.sh`）にし、`workspace-agent svn-run` が暗号ストアから creds を
引いて**本物の svn に stdin で**渡す。ディスクにも `ps` にも平文は出ない。

判断は Go（`workspace/agent/svn_wrapper.go`・`planSvnWrapper` は純関数）に置く。原則は 3 つ:

1. **注入するか、素通しするか、しかない。** 打たれたコマンドと違うことをする分岐を作らない。
2. **stdin を横取りしない。** `--password-from-stdin` は stdin を食うので、`-F -` / `--targets -` /
   `svn patch -` は素通し。⚠️ **`-m` の無い `commit` も素通し**——エディタが開き、その stdin が
   こちらのパイプだと `vim` が「Input is not from a terminal」でハングする。
3. **明示指定が常に勝つ。** `--password` / `--password-from-stdin` があれば触らない
   （Agent 自身の REST 経路がまさにこの形で svn を呼ぶ）。

⚠️ **svn はオプションを*サブコマンドごとに*検証する**（`svn add --username x` は
「Subcommand 'add' doesn't accept option '--username'」）。したがって注入先は `--username` を
受け付けるサブコマンドの明示リストのみ。ローカル専用（`add` / `revert` / `cleanup` / `patch` …）に
足すと、動いていたローカル操作が usage エラーに変わる。

⚠️ **Agent 自身の svn 呼び出しは `AF_SVN_WRAPPED=1` でシムを迂回する**（`svnCmd`）。PATH は
Agent プロセスにも効くので、放っておくと自分の注入済みコマンドがラッパーを一往復し、
`svn info` が 1 回余計に走る。

### 9.3 検証

- `TestSvnWrapperAuthenticatesAgainstRealServer` — 匿名を拒否する `svnserve` を立て、
  checkout / update / commit / log を**コマンドラインに認証情報を一切書かずに**通す。
  **陽性対照が本体**: 保存前の checkout が `svn_auth_failure` で落ちることを先に確かめる。
  これが無いと「認証を要求しないサーバ」でも緑になる。
- `TestSvnReauthenticateWorkingCopy` — 「保存せずにチェックアウトした」状態から始めて、
  update が 401 `svn_auth_required`、誤ったパスワードは保存されない、正しいものは
  リポジトリルート prefix で保存され update が通る、まで通す。

### 9.4 追加・改修ファイル

- 追加: `workspace/agent/svn_auth.go`, `workspace/agent/svn_wrapper.go`,
  `workspace/svn-auth-wrapper.sh`, `console/src/features/repos/SvnAuthModal.tsx`
- 改修: `workspace/agent/{svn.go,routes.go,main.go}`, `workspace/Dockerfile`,
  `control-plane/{routes.go,proxy.go}`（`repo.svn.auth`）,
  `console/src/features/repos/{RepoRow,RepoRowConnected}.tsx`,
  `console/src/features/settings/connect/GitTab.tsx`（Subversion カード＝一覧・追加・失効）
