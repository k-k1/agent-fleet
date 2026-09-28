# 123. ブランチ命名規則を層で重ねる（ADR 0103 の下調べ）

- 依頼: #1120。決定は [ADR 0103](../decisions/0103-branch-naming-rules.ja.md)。
- 関連: ADR 0061 決定 12（作業項目の既定 `feature/{key}`）。

## 1. いまブランチ名を決めている場所

3 か所がばらばらに決めていて、書式も揃っていない。

| 入口 | 決め方 | 置き場所 |
|---|---|---|
| 作業項目から起動 | 利用者のテンプレート（ui-prefs `workItemBranchTemplate`、空なら `feature/{key}`）。`{key}` は GitHub で `issue-N`、`{slug}` は ASCII だけ（日本語の題では空） | Console の `branchForItem`（`read.ts`） |
| それ以外の起動（起動モーダル・引き継ぎ・画像スタジオ・エージェントの `create_session`） | `new_branch` が空なら `temp/<乱数>` | Agent `session_handlers.go` |
| 改名 | チップは `feat/ fix/ refactor/ chore/ docs/`、AI の提案は prefix を禁じる | `BranchRenameModal.tsx`・`session_title.go` の `BranchSuggestPrompt` |

- base は親 clone の現在の HEAD か、手で打った値。リポジトリの既定ブランチも git-flow の役割も誰も読まない。
- テナント層もリポジトリ層も無い。af MCP も規則を出さないので、エージェントとスキルは自由に名付ける。
- Jira の一覧は `summary,status,assignee,labels,updated` だけを取る（`connections_jira.go`）。課題タイプが無いので `{type}` はまだ作れない。GitHub の REST は issue に `type`（issue types）を返す（未設定なら `null`、この repo の #1120 で確認）。

## 2. git-flow のキー（3 系統を実際に動かした）

`$AF_WORK_DIR` に gitflow-avh・nvie/gitflow・gittower/git-flow-next を clone し、空コミット 1 個の repo で `git flow init -d` を走らせた。

| 系統 | 書くキー |
|---|---|
| gitflow-avh | `gitflow.branch.master=main`、`gitflow.branch.develop=develop`、`gitflow.prefix.{feature,bugfix,release,hotfix,support}`、`gitflow.prefix.versiontag=`、`gitflow.path.hooks=<絶対パス>` |
| nvie（元祖） | avh から `bugfix` と `path.hooks` を除いたもの |
| git-flow-next | 別スキーマ `gitflow.branch.<名前>.{type,parent,prefix,…}` と `gitflow.version`。avh 形式も読む。**コミットできる `.gitflow`（git-config 形式）をリポジトリ直下に置ける**（`internal/config/shared.go` の `SharedConfigFileName`） |

- 本番ブランチのキー名は、中身が `main` でも `gitflow.branch.master`。
- **キーはすべて `.git/config`（ローカル）で、clone すると消える**。init 済みの repo を clone し、`git config --get-regexp '^gitflow'` が exit 1（0 件）になることを確かめた。
- worktree は `.git/config` を共有するので、親 clone に書けば全 worktree に効く。
- Fork（fork.dev）の「Initialize Git Flow」モーダルの欄は Production Branch / Development Branch / Feature / Release / Hotfix Prefix / Version Tag Prefix の 6 つ。nvie と同じ集合で、**bugfix と support の欄は無い**（利用者の画面写真で確認。Fork 本体を動かしてキーを読んだわけではない）。

## 3. Bitbucket Cloud の branching model

- `GET /2.0/repositories/{ws}/{repo}/branching-model` は公開 repo なら匿名で 200。形は
  `development{name, use_mainbranch, branch}`、`branch_types[]{kind, prefix}`、無効なら `production` は無い。
- 必要なスコープは `repository`（`x-accepted-oauth-scopes: repository`）。af の Bitbucket 接続は接続時に `read:repository:bitbucket` を必須にしているので足りる。
- `…/branching-model/settings` は `admin:repository:bitbucket` が要り、af の接続では 403。**「設定された値か、未設定の初期値か」を区別できるのは settings の方だけ**。
- 認証: 接続の資格情報は Basic（`x-bitbucket-api-token-auth` / email）では 401、Bearer で 200 だった。

## 4. 実例: git-flow 系の社内 repo（Bitbucket Cloud）

利用者の業務 repo 1 本（名前は記録しない）を読み取りだけで調べた。

- `gitflow.*` は 0 件、`.gitflow` も無い。**この運用では clone の後に各自が `git flow init` する**（利用者の説明）。Agent Fleet の親 clone では誰も走らせないので、宣言が無いのが普通の状態になる。
- branching model API は `development: main`（`use_mainbranch: true`）と、`bugfix/ feature/ hotfix/ release/` を返した。これは Bitbucket の初期値そのままで、**実際の運用（`develop` から切る）と食い違う**。
- `origin/HEAD` は `main`。「既定ブランチを base にする」とこの repo では間違える。
- リモートのブランチ 201 本: `feature/<Jira キー>` 137 本（slug なし、キーは大文字のまま）、`release/x.y.z` 60 本、`bugfix/`・`hotfix/` 0 本、綴り違いの `feagure/…` 1 本。
- 流れ: feature は `develop` から切り（`release/x` へのマージ 12 件の分岐点を辿ると、10 件が `develop` の first-parent 上）、**`develop` ではなく `release/x.y.z` へマージする**。release を `main` と `develop` へマージする。
- 規則が書いてあるのは `CLAUDE.md` の散文だけ（「`feature/<キー>` を `develop` ベースで切る」）。人とエージェントは読めるが、機械は読めない。
- 今うまく動いているのは、親 clone がたまたま `develop` をチェックアウトしているから（base = 親の HEAD）。

## 5. ここから言えること（ADR に持ち込んだもの）

1. git-flow が決めるのは「prefix」と「どこから切るか」だけで、prefix の後ろ（`<キー>` か `<番号>-<slug>` か）は決めない。規則は丸ごとではなく**項目ごとに**重ねる必要がある。
2. リポジトリ宣言の中では、clone の後まで残るもの（コミットされたファイル）が強い。Bitbucket API は一番弱く、初期値と区別できない値は宣言と見なさない。
3. 宣言が無いときの base は `origin/HEAD` ではなく、今と同じ親の HEAD。
4. 宣言が無いのが普通なので、読むだけでは足りない。git-flow と同じキーを親 clone に書く「Git Flow を初期化」を Console に置く。
5. 宣言に無い種類（nvie・Fork の bugfix）は feature に落とす。バグの課題でもこのチームの `feature/<キー>` と一致する。
6. マージ先（`release/x` へ PR を出す）は命名とは別の話。

## 6. レビュー 1 巡目で測ったこと

- **`git config --file -`（標準入力）は `include.path` を既定で辿る**。`[include] path = /etc/hostname` を標準入力で渡すと、hostname の中身がキーとして返った。同じ内容でもパスで渡す（`--file f.cfg`）と include は辿らない（`--includes` を付けたときだけ辿る）。`--no-includes` を明示すると標準入力でも辿らない。git 2.47.3。コミットされたファイルを blob から標準入力で読むので、`--no-includes` は必須（ADR 0103 決定 3）。
- ui-prefs は丸ごと置き換える: Console は自分の知っているキーだけを集めて PUT し（`settings.ts` の `serverPrefs`）、Agent はファイルを丸ごと書く（`ui_prefs.go` の `handlePutUIPrefs`）。新しいキーを足すと、古い Console が何か 1 つ保存しただけで消える。利用者層を ui-prefs から外した理由（決定 2）。

## 7. レビュー 2 巡目で測ったこと

- `git check-ref-format --branch` は `feature/`（末尾 `/`）と空文字を exit 128 で拒み、`feature/x` は 0。prefix は単独で検査できないので、`<prefix>x` として検査する。タグの prefix は `refs/tags/<prefix>1.0` で検査する（空の prefix でも `refs/tags/1.0` は 0）。
- `sanitizeBranch` は空のセグメントを捨てるので、`feature/` は `feature` になる。「prefix だけか」の判定はサニタイズの前にする。
- git-flow-next の固有形式は、ブランチごとに `type`（`base` / `topic`）・`parent`・`startPoint`・`prefix` を持つ（`internal/config/config.go` の既定値: feature は topic・parent develop・startPoint develop・prefix `feature/`）。切り出し元は `startPoint`。`gitflow.version` が初期化済みの印。
