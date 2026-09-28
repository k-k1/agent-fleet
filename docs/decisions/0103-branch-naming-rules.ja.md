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

欄を JSON オブジェクトで示す。リポジトリのファイルは、同じ欄を git-config の書式で書く（決定 3）。

```json
{
  "match": "bitbucket.org/acme/*",
  "name":  "{prefix}{ref}-{slug}",
  "base":  "head",
  "types": {
    "bugfix": { "prefix": "fix/", "base": "main", "from": ["Bug", "Defect", "bug"] }
  }
}
```

- `match`: `host/owner/repo` のパターン（下記）。リポジトリ宣言には無い。
- `name`: テンプレート（決定 4）。
- `base`: `head`・`default`・ブランチ名のいずれか（決定 5）。
- `types.<種類>`: 種類ごとの上書き。`from` は、その種類に対応する課題タイプとラベルの一覧。
- **種類は閉じた語彙**: `feature`・`bugfix`・`hotfix`・`release`・`support`・`docs`・`chore`・`refactor`。
  最初の 5 つは git-flow と Bitbucket 自身の種類なので、宣言が一対一で対応する。残り 3 つは、改名のチップが
  出していた conventional-commit の prefix。
- **どの欄も省略できる。** 種類ごとの欄も同じ。`base` だけの規則も有効。
- **リポジトリのファイルとテナント／利用者の保管は、同じ欄を持つ。** ファイルは git-config の書式（決定 3）、
  テナントと利用者の保管は上の JSON で持つ。

### 決定 2: 4 つの層を、欄ごとに重ねる

強い順に:

1. **リポジトリ宣言**（決定 3）
2. **利用者**（下記）
3. **テナント既定**（決定 10）
4. **組み込み既定**（決定 6）

層は規則単位ではなく**欄単位**で重ねる。git-flow の宣言が知っているのは prefix と切り出し元だけで、prefix の後ろに
何が付くかは言わない（docs/log/123 §5.1）。規則を丸ごと上書きすると、残りを捨ててしまう。

**1 つの欄の決め方。** 欄は `name`、`base`、各種類の `prefix`・`base`・`from`。それぞれについて:

1. 層を強い順に試す。
2. 層の中では、一致する規則を具体的な順に試す。
3. その欄を設定している最初の規則が値を与える。

たとえば、repo の完全一致で `base` だけを設定した規則と、`*` で `name` だけを設定した規則は、両方とも効く。
「欄ごとに独立」の例外は 2 つ:

- 種類の集合（決定 3）。
- `base`。種類の値と規則の値を合わせて順序づける（決定 5）。

**`match`。** `origin` の URL から取った `host/owner/repo` を小文字にして照合する。`*` はちょうど 1 セグメントに
一致し、`*` 単独は全 repo に一致する。具体性は文字どおりのセグメントの数で、`bitbucket.org/acme/web` は
`bitbucket.org/acme/*` に勝ち、それは `*` に勝つ。同点なら先に書いた規則が勝つ。リポジトリ層には `match` が無い。
その repo 自身だからである。

**利用者層は ui-prefs ではなく専用の保管に置く。**
- Agent は利用者の規則を、`GET/PUT /branch-rules/user` の後ろにある別のファイルで持つ。
- **ui-prefs に置かない理由:** Console は ui-prefs を丸ごと書き、自分の知っているキーだけを送る。Agent はファイルを
  置き換える。古い Console で何か 1 つ保存するだけで、知らない規則のキーが黙って消える。
- **既存の `workItemBranchTemplate` は ui-prefs に残し、移行しない。** resolver は空でないテンプレートを、利用者の
  規則がもう 1 つあるものとして読む（`{match: "*", name: <テンプレート>}`、保管した規則の後ろに並ぶ）。古い Console は
  今までどおりこれを編集し、自分で描く。新しい Console が古い Agent に当たると 404 になり、同じことをする（決定 7）。

### 決定 3: リポジトリ層は、リポジトリがすでに言っていることを読む

情報源を強い順に、欄ごとに 1 つのリポジトリ規則へまとめる:

1. **`.agent-fleet/branches`**（コミットされたもの）。**git-config の書式**。複数値の `from` は行を繰り返す:

   ```
   [naming]
   	name = {prefix}{key}
   	base = develop
   [type "hotfix"]
   	prefix = hotfix/
   	base = main
   	from = Incident
   	from = Outage
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

   実地調査で、`develop` から切る git-flow 運用の repo が返したのは、この既定の答えだった。

   **待ち方と鮮度:**
   - その repo の写しがまだ無ければ、resolve は最初の取得を最大 3 秒待つ。
   - それを過ぎたら model なしで答え、そのことを `sources.bitbucket: "pending"` と警告で示す。人は、base が
     `head` になった理由を黙って渡されるのではなく、見て分かる。
   - 写しは 10 分保つ。`GET …/branch-rule?refresh=1` で取り直せる。`sources` には各写しを取得した時刻が載る。

git-flow の情報源が与えるもの:

- 列挙した各種類の prefix。
- `feature`・`bugfix`・`release`・`support` には `base = <gitflow.branch.develop>`。
- `hotfix` には `base = <gitflow.branch.master>`。

`gitflow.*` の情報源は、`gitflow.branch.master` と `gitflow.branch.develop` の両方があるときだけ数える。
途中まで書かれた初期化（決定 9）は、半分宣言されたものではなく、宣言なしとして読まれる。

**種類の集合。** リポジトリ層の種類の集合は、情報源が列挙する種類の和集合:
- `.agent-fleet/branches` の `[type "<種類>"]` の節
- git-flow の prefix
- 数えられる場合の Bitbucket の `branch_types`

集合が空でなければ、**集合の外の種類は、弱い層が何と言おうと `feature` として解決する**。どの種類があるかは
リポジトリが決める（決定 2 がリポジトリを最上位に置く）。集合の中の種類の欄は、弱い層が引き続き埋める。
したがって、feature・release・hotfix を列挙する `.gitflow` に利用者の規則 `types.bugfix.prefix = fix/` があっても、
Bug は `feature/…` になる。nvie/gitflow と Fork には bugfix が無いので、そうした repo の Bug は `feature/<キー>`
になる。これは実地調査のチームが実際に付けている名前と同じ。

**ファイルを読む場所。** resolver が渡された作業コピーから読む。起動時は親 clone（`launch-prompts.md` と同じ）、
改名時はそのセッションの worktree。

**コミットされたファイル（`.agent-fleet/branches` と `.gitflow`）は信頼できない入力。** repo に push できる人なら
誰でも書けるので、次の制限の下で読む:
- **ファイルシステムではなく、`HEAD` の blob から読む**（`git cat-file blob HEAD:<path>`）。symlink は
  パスの文字列になり、辿られない。未コミットの編集は数えない。通常ファイルのエントリだけを、16 KiB まで読む。
- **`git config --no-includes --file - --get-regexp …` で解析する。`--no-includes` は必須。** 実測: 標準入力から
  読むと、git は既定で `include.path` を辿る。`/etc/hostname` の include がキーとして返ってきた。パスを引数に
  渡した場合は include は辿らない（docs/log/123 §6）。
- **読むのは既知のキーだけ。**
  - `.agent-fleet/branches` からは `naming.name`、`naming.base`、`type.<種類>.{prefix,base,from}`
    （`<種類>` は語彙の中のもの）。
  - `.gitflow` からは項目 3 の `gitflow.*` のキー。

  それ以外は警告を出して無視する。
- **prefix と base はすべて `git check-ref-format --branch` を通す。** 通らない値は警告を出して捨てる。base は
  `--end-of-options` の後ろでだけ git に渡す。

利用者とテナントの規則、`gitflow.*` のキーにも、同じキーの確認とブランチ名の確認をかける。

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
- **作業項目から種類を決める。** 要求に `kind` が明示されていればそれが勝つ。無ければ resolver は次の値を順に試し、
  最初に対応がつく値が勝つ:
  1. トラッカー自身のタイプ: GitHub は issue の `type`、Jira は `issuetype`（Jira の一覧で取得を始める）。
  2. ラベル（トラッカーの並び順）。

  各値は大文字小文字を区別せずに比べる。`from` を決定 2 の欄の順序（層、次に具体性）で引き、最後に組み込みの
  対応表を引く:
  - `bug`・`defect` → `bugfix`
  - `hotfix` → `hotfix`
  - `documentation`・`docs` → `docs`

  どれにも当たらなければ `feature`。決定 3 の種類の集合は、最後に当てる。
- **描画は今のサニタイズを保つ**（`sanitizeBranch`）: `[A-Za-z0-9._/-]` だけを残し、空のセグメントを詰め、
  空のプレースホルダが残した区切りを落とす。
- **描いた名前が prefix だけの場合。** 描画とサニタイズの後で確かめる。たとえば `{prefix}{key}` は、作業項目が
  無いと `feature/` になる。こうした名前にはスラグを足す（`feature/<slug>`）。スラグも空なら:
  - 起動では `temp/<乱数>` に戻る。
  - 改名では resolver が `name_empty` を返し、Console は人が打てるよう入力欄を残す。
- **英語スラグ（P2）。** 非 ASCII の題は、AI 補助の単発呼び出しで英語スラグを得られる。resolver はそれを待たず、
  決まった形のスラグで答えて名前に `provisional` を付ける。Console は一度だけ聞き直してよい。AI 補助が無ければ、
  決まった形のスラグで確定する。

### 決定 5: base

base は次の順で選ぶ:

1. **人が打った base。** そのまま使う。存在しなければ、起動は今と同じく失敗する。人が明示した選択は置き換えない。
2. **層を強い順に。** 層の中では、種類の `base` が規則の `base` より先で、最初に設定されているものが勝つ。
   リポジトリ層は先に情報源をまとめる（決定 3）ので、そこでは clone の config から来た git-flow の `hotfix` の base が、
   ファイルの一般的な `naming.base` に勝つ。
3. **組み込みの `head`。**

例: リポジトリの `naming.base = develop` と利用者の `types.bugfix.base = main` では、Bug は `develop` になる。
リポジトリ層を先に試すからである。

値の意味:

- `head`: 親 clone の現在のブランチ（今の挙動）。
- `default`: `refs/remotes/origin/HEAD`。
- ブランチ名: そのブランチ（例: `develop`）。

組み込みを `default` でなく `head` のままにするのは、実地調査の repo が `origin/HEAD → main` なのに `develop` から
切るからである。解決した base（打った base ではないもの）がローカルにも `origin` にも無ければ警告を出し、
起動は `head` を使う。

### 決定 6: 組み込み既定

- `name = "{prefix}{ref}-{slug}"`
- `base = "head"`
- prefix: `feature/`、`fix/`（bugfix）、`hotfix/`、`release/`、`support/`、`docs/`、`chore/`、`refactor/`。

GitHub では、Issue が求めた名前になる（`feature/1113-work-item-pr-status`、`fix/1120-…`）。Issue は形を
`{type}/{num}-{slug}` と書いたが、bugfix の種類の prefix は `fix/` なので、テンプレートは `{prefix}` を使う。
Jira ではプロジェクトを保つ（`feature/PROJ-123-…`、日本語の題なら `feature/PROJ-123`）。

ADR 0061 決定 12 の `feature/{key}` を置き換える。テンプレートが空の利用者は、互換措置なしで新しい既定になる。
空でないテンプレートは、利用者の規則として働き続ける（決定 2）。

### 決定 7: resolver は Agent に 1 つ

解決は Agent が行う。Agent は利用者の規則と設定、作業コピー、キャッシュしたテナント層を持っている。

- `GET /repos/{name}/branch-rule` は実効規則を返す:
  - `name`、`base`、`kinds[{kind, prefix, base}]`
  - `sources`: 各欄がどこから来たか（例: `base: gitflow.branch.develop`）
  - `gitflow`: `declared`・`absent`・`suggest`（決定 9）
- `POST /repos/{name}/branch-name` は `{item?, session?, kind?, slug?}` を受け取り、
  `{name, base, kind, provisional, warnings[], sources}` を返す。入力の扱い:
  - `item` は `{provider, key, title, type, labels}`。
  - `session` は、メタに作業項目を記録したセッションの名前（決定 8）。`item` が無ければ、resolver はその項目を使う。
  - 項目が無ければ、`{ref}`・`{num}`・`{key}`・`{project}` は空で描く。
  - `kind` が無ければ項目の種類（決定 4）、それも無ければ `feature`。
  - `slug` が無ければ項目の題のスラグ、それも無ければ空。
  - 結果が空なら `name_empty` を返す（決定 4）。
- `POST /repos/{name}/branch-name/check` は `{name}` を受け取り、`{warnings[]}` を返す。
- `GET/PUT /branch-rules/user` は利用者層を持つ（決定 2）。
- af MCP ツール `branch_name`（P2）は `POST …/branch-name` と同じ入力を取り、作業コピーを名指しする。
  エージェントとスキル（issue-to-pr）は、名前を自作せずにこれを呼ぶ。

`{name}` は、ほかの `/repos/{name}` ルートと同じく `~/repos` 配下の作業コピー。古い Agent が 404 を返したら、
Console は利用者のテンプレートで自前の `branchForItem` を使い続ける。

### 決定 8: 3 つの流儀を 1 つにする。規則は警告するだけで拒まない

- **作業項目からの起動**は、名前と base を resolver に聞く。
- **それ以外の起動は `temp/<乱数>` のまま**: 命名を後に回す方式は変えない。
- **改名:**
  - チップは、決め打ちの一覧ではなく、解決した種類の prefix（`GET …/branch-rule`）にする。今と同じく、押すと
    入力欄の prefix だけを差し替え、何も呼ばない。
  - AI の提案は、解決した種類の集合から 1 つと、英語のスラグを返す。Console はその 2 つを `session` と一緒に
    `POST …/branch-name` へ渡し、名前はそこで組み立てる。
  - 作業項目からの起動は、項目（`provider`・`key`・`title`・`type`・`labels`）をセッションのメタに記録する。
    改名でも `{ref}` と種類が保たれる。`temp/…` のセッションには項目が無いので、改名は `{prefix}<slug>` を描く
    （決定 4）。
- **警告。** 最初のセグメントが解決した prefix のどれでもない名前には、起動・改名のモーダルと `check` で警告を出す。
  `temp/` は対象外。名前を拒むことはしない。

### 決定 9: Console の「Git Flow を初期化」

宣言は無いのが普通なので（決定 3、docs/log/123 §4）、読むだけでは足りない。リポジトリ（親 clone）に
**Git Flow を初期化**を置く。

- **欄**は Fork と同じ 6 つ: 本番ブランチ、開発ブランチ、feature・release・hotfix の prefix、バージョンタグの prefix。
  これに任意の bugfix prefix を足す。`support` は Fork と同じく欄を持たない。無いときだけ `support/` を書く。
  `git flow init -d` と同じ挙動。
- **初期値**は次から埋める:
  - `origin` に `develop`・`main`・`master` のどれがあるか
  - Bitbucket model の prefix
  - すでにあるキー
- **保存すると、git-flow 自身のキーを**親 clone の config へ `git config` で書く:
  - `gitflow.branch.master` と `gitflow.branch.develop`
  - `gitflow.prefix.{feature,bugfix（指定時）,release,hotfix,support,versiontag}`

  同じ clone を `git flow` CLI、Fork、git-flow-next で開いても、同じ設定が見える。`gitflow.path.hooks` は
  git-flow の既定に任せる。
- **書き込みは共有されるので、守りを入れる。** その repo のすべての worktree が同時にこの config を読む。
  モーダルにもそう書く。
  - 書き込みは、親 clone ごとに Agent のロックで 1 つずつにする。
  - 要求には、モーダルを開いたときの値を載せる。現在のキーがそれと違えば 409 を返し、モーダルは読み直す。
  - prefix を先に書き、2 つのブランチのキーを最後に書く。resolver は両方のブランチのキーがそろうまでこの情報源を
    無視する（決定 3）。そのため初回の初期化の間、並行する resolve が見るのは「何も無い」か「新しい状態の全部」の
    どちらか。
  - 既存のキーの上から初期化し直す場合は、書き込みの瞬間に途中の状態が読まれうる。名前は助言にすぎず、次の
    resolve で落ち着くので、これは受け入れる。
  - 失敗したら、どのキーまで書いたかを返す。押し直すと全部を書き直す。
  - 何かを書く前に、すべての値に決定 3 のブランチ名の確認をかける。
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
- **利用者の規則を ui-prefs に置く。** 古い Console は、知っているキーだけで ui-prefs を丸ごと書き、Agent は
  ファイルを置き換える。その Console で何かの設定を初めて保存した時点で、規則が消える。
- **Bitbucket の model が届く前に、黙って解決する。** Bitbucket の `development` しか宣言が無い repo は、Agent を
  再起動するたびに、理由の表示もなく `head` から切ることになる。
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
