# 0103. ブランチ名は 1 つの resolver が 4 つの層（リポジトリ・利用者・テナント・組み込み）から決める

[English](0103-branch-naming-rules.md) | 日本語

- 状態: **proposed**（2026-09-28）。現状の棚卸し、git-flow と Bitbucket の実測、git-flow 運用の repo の実地調査は
  [docs/log/123](../log/123-branch-naming-rules.md)。
  この ADR より前に Issue で 3 つを決めている（#1120「Decisions already made」）:
  組み込みの形（決定 6）、リポジトリが利用者とテナントより強いこと（決定 2）、規則は助言だけであること（決定 8）。
  さらに利用者が、Jira の既定に `{ref}` を使うことと、テンプレートが空の利用者を互換措置なしで新しい既定へ移すことを選んだ。
- Follow-ups: #1124, #1125, #1126 (P0) / #1127 (P1) / #1128, #1129 (P2)
- 関連: [0061](0061-work-item-inbox.ja.md) 決定 12（作業項目の既定 `feature/{key}`。ここで置き換える）／
  [0031](0031-mcp-registry.ja.md)（この ADR のテナント層が真似るテナント配布）

## 背景

ブランチ名を決める場所は 3 つあり、書式はそれぞれ違う。設定できるのはそのうち 1 つだけ。

- **作業項目からの起動。** Console が利用者のテンプレート（`workItemBranchTemplate`、既定 `feature/{key}`）を、
  `{key}` と ASCII だけの `{slug}` で描く。
- **それ以外の起動。** 起動モーダル、引き継ぎ、画像スタジオ、エージェントの `create_session` は、`new_branch` が空なら
  Agent から `temp/<乱数>` を受け取る。
- **改名。** チップは `feat/ fix/ refactor/ chore/ docs/`。AI の提案は prefix を一切付けないよう指示されている。

base は親 clone の現在の HEAD か、手で打った値。リポジトリの既定ブランチも git-flow の役割も誰も読まず、
テナント層もリポジトリ層も無い。af MCP は規則を出さないので、エージェントとスキルは自由に名付ける。

チームごとの規則を共存させたい。発端は Bitbucket Cloud 上で git-flow を運用する社内リポジトリで、
`develop` から `feature/<Jira キー>` を切り、`release/x.y.z` へマージする。一方、この repo の GitHub の
プロジェクトは `<type>/<番号>-<slug>` がよい。実地調査（docs/log/123 §4）で分かったことは 3 つ。

- **clone したばかりの repo には、機械が読める宣言が 1 つも無い。** このチームは clone の後で各自
  `git flow init` を手で走らせる。git-flow のキーは `.git/config` にあり、clone では写らない。Agent Fleet が
  作った clone では誰も `git flow init` を走らせない。
- **Bitbucket の branching model は、未設定のときの初期値を返す。** `development: main` と既定の 4 つの prefix
  が返り、`develop` から切るというこのチームの運用と食い違った。設定された値と初期値を区別するには
  `admin:repository` が要り、af の接続はそれを持たない。
- **`origin/HEAD` は `main` を指す。** 「既定ブランチ」をそこで base にすると間違える。

## 決定

### 決定 1: 規則は `{match, name, base, types}`

```
match = "bitbucket.org/acme/*"      # host/owner/repo の glob。リポジトリ宣言には無い
name  = "{prefix}{ref}-{slug}"      # テンプレート（決定 4）
base  = "head"                      # "head" | "default" | ブランチ名（決定 5）

[types.bugfix]                      # 種類ごとの上書き
prefix = "fix/"
base   = "main"
from   = ["Bug", "Defect", "bug"]   # この種類に対応する課題タイプ／ラベル
```

- **種類は閉じた語彙**: `feature`・`bugfix`・`hotfix`・`release`・`support`・`docs`・`chore`・`refactor`。
  最初の 5 つは git-flow と Bitbucket 自身の種類なので、宣言が一対一で対応する。残り 3 つは、改名のチップが
  出していた conventional-commit の prefix。
- **どの欄も省略できる。** 種類ごとの欄も同じ。`base` だけの規則も有効。
- **リポジトリのファイルとテナント／利用者の保管は、同じ形を使う。** 書式は決定 3 のとおり。テナントと利用者の
  保管は、同じ欄を JSON で持つ。

### 決定 2: 4 つの層を、欄ごとに重ねる

強い順に:

1. **リポジトリ宣言**（決定 3）
2. **利用者**（設定。既存の `workItemBranchTemplate` は `match = "*"` の利用者規則になる）
3. **テナント既定**（決定 10）
4. **組み込み既定**（決定 6）

層は規則単位ではなく**欄単位**で重ねる。`name`、`base`、各種類の `prefix`・`base`・`from` は、それぞれを
設定している最も強い層から取る。git-flow の宣言が知っているのは prefix と切り出し元だけで、prefix の後ろに
何が付くかは言わない（docs/log/123 §5.1）。規則を丸ごと上書きすると、残りを捨ててしまう。

同じ層の中では、**最も具体的な `match` が勝つ**: repo の完全一致、`owner/*`、`host/*`、`*` の順。
同点なら先に書いたものが勝つ。リポジトリ層には `match` が無い。その repo 自身だからである。

### 決定 3: リポジトリ層は、リポジトリがすでに言っていることを読む

情報源は強い順に次のとおり。ここも欄ごとに重ねる。

1. **`.agent-fleet/branches`**（コミットされたもの）。**git-config の書式**で、`git config -f` で読む。

   ```
   [naming]
   	name = {prefix}{key}
   	base = develop
   [type "hotfix"]
   	prefix = hotfix/
   	base = main
   	from = Incident
   ```

   前例は `.agent-fleet/launch-prompts.md`。
2. **`.gitflow`**。git-flow-next の、コミットできる共有設定（git-config の書式、リポジトリ直下）。
3. **clone の config にある `gitflow.*`**。gitflow-avh、nvie/gitflow、git-flow-next の avh 互換、Fork が書くキー:
   `gitflow.branch.master`、`gitflow.branch.develop`、`gitflow.prefix.{feature,bugfix,release,hotfix,support}`。
   git-flow-next 固有の `gitflow.branch.<名前>.{type,parent,prefix}` も読む。worktree はこの config を共有するので、
   親 clone に書いたキーは全 worktree に届く。
4. **Bitbucket Cloud の branching model**（`GET /2.0/repositories/{ws}/{repo}/branching-model`）。Bitbucket の
   リモートで接続がある場合だけ。未設定のときの初期値と違う欄だけを数える:
   - `development` は `use_mainbranch` が false のときだけ。
   - `branch_types` は、既定の `bugfix/ feature/ hotfix/ release/` と違う prefix があるときだけ。

   実地調査で、`develop` から切る git-flow 運用の repo が返したのは、この既定の答えだった。model は repo ごとに
   1 時間キャッシュし、resolve はそれを待たない。起動直後の最初の resolve は model なしで答える。

git-flow の情報源が与えるもの:

- 列挙した各種類の prefix。
- `feature`・`bugfix`・`release`・`support` には `base = <gitflow.branch.develop>`。
- `hotfix` には `base = <gitflow.branch.master>`。

prefix を列挙するリポジトリの情報源は、**チームが使う種類をすべて**列挙しているとみなす。挙がっていない種類は
`feature` として解決する。nvie/gitflow と Fork には bugfix が無いので、そうした repo の Bug は `feature/<キー>`
になる。これは実地調査のチームが実際に付けている名前と同じ。

ファイルは、resolver が渡された作業コピーから読む。起動時は親 clone（`launch-prompts.md` と同じ）、
改名時はそのセッションの worktree。

### 決定 4: プレースホルダ

| プレースホルダ | GitHub `acme/web#1120` | Jira `PROJ-123` |
|---|---|---|
| `{ref}` | `1120` | `PROJ-123` |
| `{num}` | `1120` | `123` |
| `{key}`（従来どおり） | `issue-1120` | `PROJ-123` |
| `{project}` | （空） | `PROJ` |
| `{type}` | 種類（例: `bugfix`） | 同じ |
| `{prefix}` | 種類の prefix（例: `fix/`） | 同じ |
| `{slug}` | 題の ASCII スラグ | 同じ（非 ASCII の題では P2 まで空） |

- **`{key}` は意味を変えない。** 既存のテンプレートは以前と同じ名前を描く。Jira キーは、今と同じく書かれたままの
  大文字小文字を保つ。
- **作業項目から種類を決める。** トラッカー自身のタイプを取る: GitHub は issue の `type`、Jira は `issuetype`
  （Jira の一覧で取得を始める）。無ければラベルを取る。それを大文字小文字を区別せずに各層の `from` と照合し、
  次に組み込みの対応表と照合する:
  - `bug`・`defect` → `bugfix`
  - `hotfix` → `hotfix`
  - `documentation`・`docs` → `docs`
  - それ以外 → `feature`

  決定 3 の「挙がっていない種類」の規則は、最後に当てる。
- **描画は今のサニタイズを保つ**（`sanitizeBranch`）: `[A-Za-z0-9._/-]` だけを残し、空のセグメントを詰め、
  空のプレースホルダが残した区切りを落とす。prefix の後ろに何も無ければ `{slug}` を足す。それも空なら、起動は
  `temp/<乱数>` に戻る。
- **英語スラグ（P2）。** 非 ASCII の題は、AI 補助の単発呼び出しで英語スラグを得られる。resolver はそれを待たず、
  決まった形のスラグで答えて名前に `provisional` を付ける。Console は一度だけ聞き直してよい。AI 補助が無ければ、
  決まった形のスラグで確定する。

### 決定 5: base

base は次の順で選ぶ:

1. 人が打った base。
2. 種類の `base`。
3. 規則の `base`。
4. 組み込みの `head`。

値の意味:

- `head`: 親 clone の現在のブランチ（今の挙動）。
- `default`: `refs/remotes/origin/HEAD`。
- ブランチ名: そのブランチ（例: `develop`）。

組み込みを `default` でなく `head` のままにするのは、実地調査の repo が `origin/HEAD → main` なのに `develop` から
切るからである。解決した base がローカルにも `origin` にも無ければ警告を出し、起動は `head` を使う。

### 決定 6: 組み込み既定

- `name = "{prefix}{ref}-{slug}"`
- `base = "head"`
- prefix: `feature/`、`fix/`（bugfix）、`hotfix/`、`release/`、`support/`、`docs/`、`chore/`、`refactor/`。

GitHub では Issue の `{type}/{num}-{slug}` と同じ名前になる（`feature/1113-work-item-pr-status`、
`fix/1120-…`）。Jira ではプロジェクトを保つ（`feature/PROJ-123-…`、日本語の題なら `feature/PROJ-123`）。
ADR 0061 決定 12 の `feature/{key}` を置き換える。テンプレートが空の利用者は、互換措置なしで新しい既定になる。
空でないテンプレートは、その利用者の `match = "*"` の規則にそのまま移す。

### 決定 7: resolver は Agent に 1 つ

解決は Agent が行う。Agent は利用者設定、作業コピー、キャッシュしたテナント層を持っている。

- `GET /repos/{name}/branch-rule` は実効規則を返す:
  - `name`、`base`、`kinds[{kind, prefix, base}]`
  - `sources`: 各欄がどこから来たか（例: `base: gitflow.branch.develop`）
  - `gitflow`: `declared`・`absent`・`suggest`（決定 9）
- `POST /repos/{name}/branch-name` は `{item?: {provider, key, title, type, labels}, kind?, slug?}` を受け取り、
  `{name, base, kind, provisional, warnings[], sources}` を返す。
- `POST /repos/{name}/branch-name/check` は `{name}` を受け取り、`{warnings[]}` を返す。
- af MCP ツール `branch_name`（P2）は `POST …/branch-name` と同じ入力を取り、作業コピーを名指しする。
  エージェントとスキル（issue-to-pr）は、名前を自作せずにこれを呼ぶ。

`{name}` は、ほかの `/repos/{name}` ルートと同じく `~/repos` 配下の作業コピー。古い Agent が 404 を返したら、
Console は利用者のテンプレートで自前の `branchForItem` を使い続ける。

### 決定 8: 3 つの流儀を 1 つにする。規則は警告するだけで拒まない

- **作業項目からの起動**は、名前と base を resolver に聞く。
- **それ以外の起動は `temp/<乱数>` のまま**: 命名を後に回す方式は変えない。
- **改名:**
  - チップは、決め打ちの一覧ではなく、解決した種類の prefix にする。
  - AI の提案は、解決した種類の集合から 1 つと、スラグ（引き続き英語）を返す。名前は resolver が組み立てる。
  - 起動時に作業項目のキーをセッションのメタへ記録し、改名でも `{ref}` を保つ。
- **警告。** 最初のセグメントが解決した prefix のどれでもない名前には、起動・改名のモーダルと `check` で警告を出す。
  `temp/` は対象外。名前を拒むことはしない。

### 決定 9: Console の「Git Flow を初期化」

宣言は無いのが普通なので（決定 3、docs/log/123 §4）、読むだけでは足りない。リポジトリ（親 clone）に
**Git Flow を初期化**を置く。

- **欄**は Fork と同じ 6 つ: 本番ブランチ、開発ブランチ、feature・release・hotfix の prefix、バージョンタグの prefix。
  これに任意の bugfix prefix を足す。
- **初期値**は次から埋める:
  - `origin` に `develop`・`main`・`master` のどれがあるか
  - Bitbucket model の prefix
  - すでにあるキー
- **保存すると、git-flow 自身のキーを**親 clone の config へ `git config` で書く:
  - `gitflow.branch.master` と `gitflow.branch.develop`
  - `gitflow.prefix.{feature,bugfix（指定時）,release,hotfix,support,versiontag}`

  同じ clone を `git flow` CLI、Fork、git-flow-next で開いても、同じ設定が見える。`gitflow.path.hooks` は
  git-flow の既定に任せる。
- **書くのは人が押したときだけで、ブランチは作らない。** 開発ブランチがローカルにも `origin` にも無ければ断る。
  `git flow init` はそれを作るが、こちらは作らない。
- **提案。** `origin` に `develop` があり、どのリポジトリ情報源も何も宣言していないとき、作業項目の起動が
  これを勧める（`gitflow: suggest`）。勧めるだけで、勝手に初期化はしない。

### 決定 10: テナント層（P1）

- テナント管理者は、`match` 付きの規則の一覧を CP に置く。
- Agent は `GET /internal/branch-rules` を 5 分ごとに引き、CP に届かないときは最後の写しを使い続ける。
  テナントの MCP サーバ（`mcp-tenant.json`）と同じ、fail-open のキャッシュ。
- 「強制」の旗は無い（決定 8）。

### 範囲外

- **ブランチのマージ先。** 実地調査の `release/x.y.z`、PR の base、タグの prefix もここに入る。命名が決めるのは
  どこから切るかで、どこへ着くかではない。
- **名前を拒むこと。**

## 却下

- **テナントの「強制」層。** Issue で決めたとおり、規則は警告する。拒むと、人が意図した改名を止めてしまう。
- **Console だけで解決する。** エージェントとスキルが規則を知れない。Console は作業コピーの git-flow のキーも、
  コミットされたファイルも読めない。
- **Issue の案にあった `.agent-fleet/branches` の TOML。** Agent には TOML のパーサが無い。git-config の書式なら
  `.gitflow` と git-flow のキーがすでに使っている形で、`git config -f` で読め、git を使う人なら誰でも読める。
- **Bitbucket の `development` があれば常に base にする。** 実地調査の repo は `develop` から切るのに `main` が
  返った。両者を区別できる settings のエンドポイントは管理者スコープが要る。
- **組み込みの base を `default`（`origin/HEAD`）にする。** 同じ repo で `main` になる。
- **`git flow init` を走らせる。** Workspace に git-flow は入っておらず、系統ごとに質問が違い、ブランチを作る。
  キーを書けば、init が書くものと同じになる。
- **利用者の代わりに `.gitflow` や `.agent-fleet/branches` を書く。** 利用者のリポジトリへのコミットになる。
- **規則単位で優先する。** git-flow の宣言が、利用者の `name` を消してしまう。

## 結果

- テンプレートが空の利用者は、`feature/{key}` ではなく `{prefix}{ref}-{slug}` になる。
- 改名チップの `feat/` は、種類 `feature` の組み込み prefix である `feature/` になる。
- Agent Fleet で開いた git-flow の repo は、誰かが「Git Flow を初期化」を押すか `.agent-fleet/branches` を
  コミットすれば、`develop` から切るようになる。それまでは今と同じく親の HEAD から切る。
- スラグを付けない名前（`feature/<キー>`）のチームは、利用者・テナント・リポジトリのいずれかに `name` が要る。
  組み込みはスラグを足す。
- Jira の一覧で取得する欄が 1 つ増える（`issuetype`）。
