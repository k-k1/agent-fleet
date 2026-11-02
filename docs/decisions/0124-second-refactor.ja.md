# 0124. 2 度目のリファクタ：先にラチェット、変更の登録先は 1 箇所に、エージェント種別はプラグインに

[English](0124-second-refactor.md) | 日本語

- 状態：**採用**（2026-10-28）。計画であり、まだ何も作っていない。いまの範囲はフェーズ 0〜3 で、
  この順に進める。フェーズ 4〜6 は再発見されないように記録しておくもので、変更頻度に見合うときだけ
  実施する。
- Follow-ups：#2853, #2854, #2855, #2856, #2857, #2858, #2859, #2860, #2861, #2862, #2863, #2864,
  #2865, #2866, #2867, #2868, #2869, #2870, #2871, #2872, #2873, #2874, #2875, #2876
- 関連：[0067](0067-parallel-refactor.ja.md)（1 度目のウェーブ、その検証の作法と残したもの）/
  [0012](0012-go-internal-refactor.ja.md)（層分け・2 つのバイナリ・共有モジュールは作らない）/
  [0011](0011-console-rebuild.ja.md)（feature 単位に切った Console）

## 背景

`main` の `5bcfc0110`（2026-10-28）で測った。「package main」はモジュールのディレクトリ直下にある
テスト以外の `.go` ファイルを指す（0067 と同じ物差し）。測り方のコマンドは「測り方」節にある。

| | 0067 完了時（2026-09-04） | 現在 |
|---|---|---|
| CP の package main | 96 ファイル / 28,321 行 | **198 / 75,582** |
| Agent の package main | 64 / 15,373 | **129 / 37,322** |
| Console の `src/`（テスト以外の TS/TSX） | 122,094 行 | **179,895** |

🔴 **訂正（2026-10-28）：** Console の値は **180,004** 行である。179,895 を出したコマンドは `test` を含む
パスをすべて除いており、`console/src/test/` の補助ファイルまで落としていた。ここでの「テスト以外」は
`*.test.*` だけを除く意味である。コマンドは「測り方」節に追加した。

この間にマージ以外のコミットが 3,898 本入った。再膨張の原因は 1 つの家系ではない。エンジン家系
（ADR 0071〜0099）が CP の main のうち 44 ファイル / 24.0k 行を占め、残りは十数個の機能（OAuth、
egress、開発用 IdP、inbox、scheduler など）で、どれも「main 以外に置け」と言うものが何もなかったから
main に入った。**0067 は main を縮めたが、小さく保つ仕組みは何も置かなかった**。その成果は 2 か月もたな
かった。

ただし、変更を遅くしているのは大きさではない。ホットなファイルと、1 つのものを何箇所にも登録する
作りのほうである。

| 足すもの | いま編集する場所 | 2026-09-04 以降の変更頻度 |
|---|---|---|
| CP が中継する Agent のルート | Agent 側のルートとその golden、さらに CP で最大 4 箇所：`routes.go`、`testdata/routes.golden`、監査の switch `auditActionTarget`（`proxy.go`）、`memberOriginRoutes`（`member_origin.go`） | `routes.go` 119 コミット、`routes.golden` 127。手で写した `, rest)` 行が 334 |
| MCP ツール | `internal/mcpx/mcp_stdio.go`（5,001 行）の 5 箇所：スキーマ表、共有の引数 struct、`mcpCheckCallArgs`、広告側と呼び出し側の 2 つのゲート、switch の分岐。加えて `control-plane/internal/mcpsrv/mcp.go` にもう 1 つの定義 | 93 コミット |
| Console の設定項目 | `lib/settings.ts` の約 1,800 行に散らばる 5 箇所 | 57 コミット |
| エージェント種別 | muse は自身の 2 パッケージ以外で、テストでも Markdown でもないファイルを 139 個触っている | — |

重複は、手間が増えるだけでなく欠陥を生み始めている。

- **2 つの MCP ツール定義はすでにずれている。** Agent の operator 向け `create_session` の説明は、
  muse 以外の全種別を列挙している（`mcp_stdio.go:2293`）。2 つの定義の言語が違うのは意図どおりで
  （`mcp_stdio.go:722-735`：セッション向けの文はトークン節約のため英語、operator 向けは日本語のまま）、
  そこは欠陥ではない。
- **copilot・cursor・kiro の `driver.go` は、種別名をそろえると 83〜90% が同じ**である。`acp.go` は
  207 行中 12 行しか違わない。横断的な修正は、managed ドライバー 7 本すべてに同時に入れる必要が
  あった（`c70e8cb0a`、`2a9b6bbaf`）。
  🔴 **訂正（2026-10-28）：** 「83〜90%」と「207 行中 12 行」は、方法を示さずに 1 組だけを測った値だった。
  3 つの種別名を 1 つのトークンにそろえ、コメントと空行を除くと、`acp.go` のコードはどの組でも
  95〜100% が共通で、`driver.go` は大きいほうのファイルの行の 69〜83%（小さいほう基準では 87〜92%）が
  共通である。コマンドは「測り方」節にある。
- **種別のレジストリが Agent の import グラフの最上部にある**（`internal/sessionx/agent.go:34`）。その
  ため、それより下のパッケージは自前の種別リストを持ち続けている。種別リテラルによる分岐は、Agent の
  `internal/agents` の外に 219、Console の `agents/registry.ts` の外に 66、CSS の 6 ファイルに
  `.kind-<種別>` セレクタが 51 ある。

ついでに見つかった、欠けている検査：

- CP の中継ルートがすべて Agent のルート表に実在するか、変更系の中継ルートすべてに監査アクションが
  あるかを確かめるものが無い。
- `console/scripts/css-dup.mjs` はあるが CI は一度も走らせていない。67 個のクラス名が複数のファイルで
  ルールを始めている。`.repo-identity` は `scm/scm.css` と `settings/parts/common.css` で違う値が付いて
  おり、読み込み順で勝ち負けが決まる。
- 0067 の別名ファイルが Console に 2 つ残っている（`features/settings/mcpWire.ts`、`mcpForm.tsx`）。
  0067 の「別名ゼロ」は Go だけを数えていた。

CI は `main` で緑、所要は約 6 分なので、この計画が頼るゲートは信用できる。

## 決定

1. **移送より先にラチェット。** どのフェーズも、直したものが戻ってきたら落ちる検査を残す。ラチェット
   自身がホットなファイルになってはならない。変わるのは、それが守っていることを誰かがしたとき
   （main にファイルを足す、種別の分岐を足す）だけで、機能 PR のたびには変わらない。だから package main
   には行数の予算ではなくファイルの許可リストを置く。
2. **大きさではなく「変更頻度 × 触る箇所の数」で選ぶ。** 大きくてもまとまっていて滅多に変わらない
   パッケージ（`imagegen` とその ComfyUI のコード）は待たせる。各候補に問うのは、次の機能が何箇所を
   編集する必要があり、それがどれくらいの頻度で起きるか、である。
3. **純粋な移送と変更は同じ PR に入れない**（0067 決定 9）。ファイル丸ごとの移送は `--find-renames` で
   内容差がゼロになる。ファイルの分割は、トップレベル宣言の多重集合と文字列リテラルの多重集合
   （go/ast の `BasicLit`、または TS の相当物）が前後で一致する。後者は、機械的な改名が MCP の説明文を
   書き換えた件のあと、0067 の作業日誌が採用した検査である。
4. **ホットなファイルは、それを編集している open PR が無いあいだにだけ割る。** git はファイル丸ごとの
   改名なら機能ブランチの編集を運んでくれるが、1 ファイルを 10 個に割ると運べず、すべての hunk が
   衝突になる。割る前にそのファイルに触れる open PR を列挙し、空いた隙に割って速やかにマージする。
5. **移すファイルを誰が読んでいるかを、モジュールをまたいで探す。** Go のテストが Console のソースを
   パスで読んでいる（`settings.ts` のドリフト検査、`lib/fleetgraph.states.json`、ロケールの
   `errors.ts`）。`console_routes_test.go` は Console の API パスが文字列リテラルであることを前提にして
   いる。Go のソースを読む Console のテストもある。移送は両側を同じ PR で更新する。
6. **移さないもの**：CP の `manager`（36 ファイルにまたがる 159 メソッド）とその中核の workspace 家系、
   ハンドラー struct、非公開の error、JSON の塊（0067 の「移さない」形）。どこからでも手が届く場所に
   あるのが理由で、動かすとロックの範囲や配線が変わるのに、登録の手間は 1 つも減らない。
7. **進め方。** 作業パッケージ 1 つにつき Issue 1 つ・PR 1 つ。実装は子セッションが行い、各 Issue に
   その作業が所有するファイルを書く。マージ前に別モデルのレビュー役がすべての PR を読む。マージは
   維持者が行う。フェーズ 1 の項目は所有ファイルが重ならないので並行して走らせる。登録の仕組みを
   変える項目（フェーズ 2・3）は、ホットなファイルごとに 1 つずつ走らせる。
8. **フェーズ 3 は実機一巡で閉じる。** 種別の変更はセッションの開始・再開・停止に触れるので、実配備で
   全種別のセッションを 1 つずつ作成・会話・停止・再開する。0067 の実機一巡は 2 回とも、すべての
   ゲートを通った欠陥を見つけた。

### フェーズ 0：ラチェットと欠けている検査

- CP のテスト：中継ルートがすべて Agent のルート表に実在する／変更系の中継ルートすべてに監査
  アクションがある。
- ラチェット：両 Go モジュールに package main のファイル許可リスト。Agent と Console に、種別リテラル
  分岐の上限（増えたら落ちる）。
  🔴 **追記（2026-10-28）：** 上限が数える種別の集合は種別カタログから取る。または、テストの持つ一覧が
  カタログと食い違ったら落ちるようにする。一覧を固定で持つと新しい種別の分岐は決して数えられず、
  それこそラチェットが止めるべき増え方である。
- Console：CI に `css-dup` を今日の件数を上限として入れる。import 境界の lint（`lib/` は `features/` を
  import しない。今日の例外は列挙する）。別名ファイル 2 つを消す。
- 両 Go モジュールの死んだコードを消す（たとえば `metrics.go` の `dockerContainerID`、`mem.go` の
  `memClampNote`、`workitems.go` の `sortQueriesByPosition` には呼び出し元が無い）。
- operator 向け `create_session` の説明から muse が抜けているのを直す。

### フェーズ 1：パッケージ内でのファイル分割（純粋な移送）

- `internal/runtime/runtime_ecs_ec2.go`（7,066 行）を、既にある `// ---` の区切りに沿って。
- `internal/store/store_sqlite.go` を `store_<領域>.go` に。新しい store のコードは既にこの形である。
- `engine_admin.go` をルート・モデル・取り込みに。
- `internal/mcpx/mcp_stdio.go` をツールの家族ごとのファイルと、Agent クライアント、呼び出し元の解決に。
- Console：`app/WsBar.tsx` を `app/wsbar/` に。`adminEngineAdd.tsx` をその 5 つのコンポーネントに。
  `engineTypes.ts` を型とフックに。
- 何でも入れのロケールファイルを機能ごとのファイルに（`tools.ts` に `mem.*` キーが 476、`admin.ts` に
  エンジンとカタログのキーが 426）。

### フェーズ 2：登録先を 1 箇所に

- **CP の中継ルートを宣言的な表にする。** 中継ルート 1 本につき 1 行 `{method, path, audit, signed}`、
  領域ごとに 1 ファイル。登録・監査の分類・member-origin の署名はそこから導く。フェーズ 0 の 2 つの
  テストと既存の `routes.golden` の上に作る。表を Agent のルートから生成するのは後の段で、今回ではない。
- **MCP ツール 1 つにつき `toolSpec{schema, gate, args, handler}` を 1 つ**。フェーズ 1 の分割後、その
  家族のファイルに置く。ツールのスキーマと説明にある種別の列挙は、CP 側も Agent 側も種別カタログ
  （`agentkinds/catalog.json`）から作る。説明文は、2 種類の読み手向けの 2 つの文のままにする。
- **Console の設定項目 1 つにつき記述子 1 つ**（キー・既定値・スコープ・正規化）。サーバー同期の
  エンジン、テーマとフォントを画面に当てるコード、機能ごとの選択肢リストは `settings.ts` から出る。
  `settings.ts` を読む Go のドリフト検査は同じ PR で直す。

### フェーズ 3：エージェント種別をプラグインに

- copilot・cursor・kiro 共通の ACP 基盤。まずほぼ同一の `acp.go`・`mirror.go`・`progress_managed.go`、
  次に種別ごとのフック付きでドライバー本体。
- managed ドライバー 7 本が繰り返している足場（`NewDriver`、`handleFor`、
  `ManagedAlive`/`Busy`/`InFlight`、`DropHandle`、`RemoveLedger`、`ReconcileManaged`、
  `AbortManaged`）を共通化する。
- 種別のレジストリを `sessionx` より下に移し、どのパッケージからも参照できるようにする。ライフ
  サイクル（reconcile・abort・shutdown）、接続状態、生存とモーダルの検出、スキルの配置、モデル一覧を
  `Agent` の任意 interface にし、`main.go`・`shutdown.go`・`connections.go`・`session_io.go`・
  `chat_providers.go` は種別で switch せずレジストリを回す。`internal/agents` は `bridge` を import して
  いるので、import の循環に注意する。
- CP（`mcpsrv/mcp.go`・`tenant_member_defaults.go`・`limit_board.go`）と Console（手で保守している
  7 つの種別リスト、`lib/agentModels.ts` の `isDynamic`）は、カタログとレジストリの能力を読む。種別ごとの
  CSS は種別ごとのカスタムプロパティ 1 つにする。
- **目標：新しい種別が、自身のパッケージ以外で触るテストでも Markdown でもないファイルを 40 以下に**
  （muse は 139）。フェーズ 3 は pi や rovo を作る前に終える。

### フェーズ 4〜6：記録のみ。変更頻度に見合うときに実施

- **4 — パッケージ。** CP のエンジン領域を、そのパッケージが定義する interface の向こう側の
  `internal/engine` に移す。HTTP ハンドラーは main に残す。先に TTS のライフサイクル
  （`engine_control.go`・`engine_ecs.go`）をエンジンのライフサイクルから切り離し、
  `errcodes_catalog_test.go` が新しいパッケージを読むようにする。そうしないとエンジンのエラーコード
  （約 50）が黙って検査から外れる。`manager`・`config`・認証ヘルパー・HTTP エラーヘルパーのどれも参照
  していないエンジンのファイル約 15 個（約 6k 行）から先に移す。Agent では、main への逆依存が少ない家系
  （fs、jira と workitems、usage、svn）。`createSession`（約 600 行）は、それ単独のロジック変更として
  段に分ける。
- **5 — Console のデータ層。** 機能ごとの型付き `api.ts` と、共通の読み込み・保存フック。settings から
  始める（Console の明示的な `any` 223 個のうち 146 個）。利用者が 1 つだけの `lib/` モジュールはその
  機能に戻す。CSS の重複はゼロにして `css-dup` で CI を落とす。badge・chip・card の部品を `ui.css` に。
- **6 — CI とシェル。** CLI のマニフェストを 1 つに：バージョン自体は Dockerfile の `ARG` 1 箇所だが、
  各 CLI は手で保守している 6〜7 個の表（`setup-agent-cli` アクション、`cli-drift-check.sh`、
  `cli-pin-bump.sh`、`cli-release-watch.yml`、`install_*.go` のコード）にも載っている。8 本の
  `*-contract.yml` を再利用ワークフロー 1 本に。boot-install を `entrypoint.sh` から `workspace-agent` へ、
  CLI 1 つずつ、毎回実機の起動で確かめて移す。

**追記（2026-10-29）— フェーズ 4 を「4a」に絞る。** メンテナーは、いま変更頻度が見合う 2 領域にフェーズ 4
を絞った（決定 2：変更頻度 × 触る箇所）。`origin/main` のマージ以外のコミットで 2026-10-29 に計測：

| 領域 | テスト以外のファイル / 行 | 90 日のコミット | 30 日のコミット |
|---|---|---|---|
| CP エンジン（`engine*.go`） | 44 / 約 24k | 196 | 13 |
| TTS（`tts*.go`・`engine_control.go`・`engine_ecs.go`） | 6 / 約 2.7k | 27 | 0 |
| Agent の fs | 11 / 約 4.2k | 40 | 13 |
| **Agent の jira + workitems** | 16 / 約 4.1k | 60 | **42** |
| Agent の usage | 8 / 約 2.5k | 52 | 12 |
| Agent の svn | 4 / 約 1.8k | 13 | 3 |
| **`createSession`**（`internal/sessionx/session_handlers.go:710-1287`） | 1 関数・577 行 | — | **21** |

4a は、Agent の jira と workitems の家系を純粋な移送で package main から出すこと（#2929。workitems が
jira に依存するので、jira の家系を `connections_jira.go` から呼んでいる Jira REST クライアントごと先に
`internal/jirax` へ移す）と、`createSession` を段に分けること（#2930）。CP エンジンと TTS、fs、usage、
svn は変更頻度が見合うまで見送る。

## 退けた案

- **0067 の別名移送を司令塔セッションの下で行う。** フェーズ 1 の分割は 1 つのパッケージの中に閉じ、
  フェーズ 2・3 は形を変えるのであって移すのではない。回収する別名は無く、Issue の一覧ができない調整を
  ハブがする場面も無い。
- **package main の行数予算。** main のファイルを大きくする機能がすべてそれを編集することになり、
  ラチェット自身がホットなファイルになる。
- **`manager` の分割。** 159 メソッドは CP の実行時状態とそのロックである。取り出すとロックの範囲が
  変わり（振る舞いの変更）、登録の手間は 1 つも減らない。
- **TS の型生成や共有 Go モジュールを今やる**（0012 の再検討条件）。JSON の書き出し箇所のうち 372 が
  まだ `map[string]any` を返しており（Agent 223、CP 149）、生成器では型にできない。また Agent イメージの
  ビルドコンテキストは `workspace/agent/` だけである。
  🔴 **訂正（2026-10-28）：** 372 は 2 つの `wiremap.golden` の集約行の数である（Agent 223、CP 149）。
  各行は同じ（ファイル・関数・形）の書き出し箇所を `xN` の個数付きで 1 行にまとめている。書き出し箇所
  そのものは **426**（Agent 264、CP 162）である。
- **リファクタのための凍結。** `main` には 1 日に約 70 コミットが入る。凍結は、一握りのホットなファイルを
  守るためにそのすべてを止める。決定 4 は、まさにそのファイルを、割るのにかかる時間だけ守る。

## 帰結

- フェーズ 0 が入るまでは、新しい main のファイルと種別の分岐が入り続ける。後のフェーズはすべて
  フェーズ 0 を物差しにするので、フェーズ 0 を先にやる。
- ラチェットは正当な変更を拒むこともある。そのときは、その PR で許可リストや上限を編集し、理由を PR の
  説明に書く。
- フェーズ 3 は全種別のセッションの開始・再開・停止に触れるので `needs-live-run` を付ける。

## 測り方

リポジトリのルートで実行する。

```sh
# package main: files / lines (run per module dir)
find control-plane -maxdepth 1 -name '*.go' ! -name '*_test.go' | wc -l
find control-plane -maxdepth 1 -name '*.go' ! -name '*_test.go' -exec cat {} + | wc -l

# kind-literal branches, agent (outside internal/agents) and Console (outside agents/registry.ts)
K='claude|codex|opencode|muse|cursor|kiro|copilot|agy|lcpp'
KC='Claude|Codex|OpenCode|Opencode|Muse|Cursor|Kiro|Copilot|Agy|Lcpp|LCPP'
find workspace/agent -name '*.go' ! -name '*_test.go' ! -path '*/internal/agents/*' ! -path '*/testdata/*' -print0 \
  | xargs -0 grep -hE "(case .*(\"($K)\"|Kind($KC)\b))|((==|!=) *(\"($K)\"|session\.Kind($KC)\b|Kind($KC)\b))|((\"($K)\"|Kind($KC)\b) *(==|!=))" | wc -l
find console/src \( -name '*.ts' -o -name '*.tsx' \) ! -name '*.test.*' ! -path '*/agents/registry.ts' -print0 \
  | xargs -0 grep -hE "(===|!==) *[\"'](${K})[\"']|[\"'](${K})[\"'] *(===|!==)|case [\"'](${K})[\"']" | wc -l
grep -rhoE "\.kind-($K)\b" console/src --include='*.css' | wc -l

# files a kind touches outside its own packages (muse)
git grep -l -i -w muse -- ':!*_test.go' ':!*.test.ts' ':!*.test.tsx' ':!*.md' \
  ':!workspace/agent/internal/agents/muse' ':!workspace/agent/internal/msp' | wc -l

# hand-mirrored relays
grep -cE ', rest\)' control-plane/routes.go
```

2026-10-28、上の訂正とともに追加：

```sh
# Console non-test TS/TSX lines (excludes *.test.* only)
find console/src \( -name '*.ts' -o -name '*.tsx' \) ! -name '*.test.*' -exec cat {} + | wc -l

# map[string]any write sites: grouped golden entries, then sites (the sum of xN)
for f in control-plane/testdata/wiremap.golden workspace/agent/testdata/wiremap.golden; do
  grep -vE '^(#|$)' "$f" | awk '{n=1; for (i=1;i<=NF;i++) if ($i ~ /^x[0-9]+$/) n=substr($i,2)+0; s+=n} END {print NR, s}'
done

# code shared by the ACP drivers: kind names to one token, comments and blank lines dropped
python3 - <<'PY'
import difflib, itertools, re
def norm(p):
    return [l for l in (re.sub(r'(?i)copilot|cursor|kiro', 'KIND', re.sub(r'//.*', '', x)).strip()
                        for x in open(p)) if l]
d = 'workspace/agent/internal/agents/'
for f in ('acp.go', 'driver.go'):
    for a, b in itertools.combinations(('copilot', 'cursor', 'kiro'), 2):
        A, B = norm(f'{d}{a}/{f}'), norm(f'{d}{b}/{f}')
        m = sum(x.size for x in difflib.SequenceMatcher(None, A, B, autojunk=False).get_matching_blocks())
        print(f, a, b, f'{m / max(len(A), len(B)):.0%} of larger, {m / min(len(A), len(B)):.0%} of smaller')
PY
```

## 追記（2026-10-28）— css-dup の上限はスクリプトが数えるものを数える

背景の「67 個のクラス名が 2 つ以上のファイルでルールの先頭に立つ」は `css-dup.mjs` が出す数ではなく、
そこから再現できなかった。そのため Phase 0 の上限（#2855）は、その時点の `main` でスクリプト自身が
出す 2 つの数を使う。2 つ以上のファイルで定義されたクラス名が 200、そのうち 2 つ以上のファイルで
単独（`.name {}` だけ）に現れるものが 36。CI は `npm run css:dup:ci`（`--max-dups=200 --max-bare=36`）
を走らせ、どちらかが増えると落ちる。減っても落ちない。スクリプトが案内を出すだけで、上限を下げるのは
別の任意の編集なので、ついでに重複を 1 つ解消した PR が `package.json` を触る必要はない（決定 1）。

## 追記（2026-10-28）— ラチェットはレジストリの kind を数える

Phase 0 のラチェットを入れた (#2854)。各 Go モジュールの `main_files_test.go` が package main を
`testdata/main-files.txt` に固定する。kind リテラル分岐の上限（`workspace/agent/kind_literal_ceiling_test.go`、
`console/src/test/kindLiteralCeiling.test.ts`）は上の式を使うが、kind はレジストリ（`sessionx.RegisteredKinds()`、
Console はカタログと一致を保たれた `SESSION_KINDS`）から非エージェントの `shell` と `ssm` を除いて取り、Go の識別子は
大文字小文字を問わず `Kind<kind>` で数える。新しい kind は登録した PR から数えられる。上の `K`/`KC` は今日時点の展開で、
どちらも 219 と 66 になる。css-dup と同じく、上限を超えると落ち、下回ると下げるよう案内を出すだけ。

## 追記（2026-10-29）— 「新しい種別が触るファイル」は種別を 1 つ足して測る

フェーズ 3 の目標の数え方を定義し直す（#2927）。目標そのもの（40 以下）は変えない。「計測」の
`git grep -l -i -w muse` は muse の名前が出るファイル（コメント、ワークフロー、golden、UI の文言）を
すべて数え、新しい種別が編集しなければならないファイルを数えていない。フェーズ 3 で Go の種別分岐を
85、Console のものを 9 消しても、152 から 145 にしか動かなかった。

**新しい定義。** スタブの種別 `zz` を足したときに、そのパッケージ（`workspace/agent/internal/agents/zz`）の
外で変わるファイルの数。テスト（`*_test.go`、`*.test.ts(x)`）と Markdown を除き、両 Go モジュールの
テスト、Console の typecheck と vitest、docs-check が通った状態で数える。スタブはチェックリスト
（`docs/build/20-add-an-agent.md` §20.7）が許す最小の種別で、登録されてターミナルで `bash -l` を起動し、
マネージドの driver、MCP、指示の宛先、サインイン、固定する CLI を持たない。マージはしない。手順は
`scripts/stub-kind-count.sh`（`--verify` でテスト群も走らせる）で、同じ章の §20.8 に書いた。

**`aee59bfbe` での実測: 16**（テストと Markdown を含めると 25）。

| 分類 | 数 | ファイル |
|---|---|---|
| Go | 5 | `session.go`（`Kind*`）、`agentreg.go`、`kinds.go`、`agent_instructions.go`（`instrUnsupported`）、`session_move.go`（`moveUnmeasuredTargets`） |
| TS | 3 | `types/session.ts`（`SessionKind`、`SESSION_KINDS`）、`agents/kindCatalog.ts`（`FALLBACK_KIND_FACTS`）、`agents/registry.ts`（記述子） |
| CSS | 1 | `styles/tokens.css`（両テーマのトークンと `.kind-` の対応表の行） |
| データ | 7 | `agentkinds/catalog.json` と、Agent の `mcpx` と CP の `mcpsrv` の MCP ツールの golden `testdata/mcp_tools/{list.json,calls.txt}`（5 つとも生成物で手では編集しない）、`en` と `ja` の `sessions.ts` ロケール |
| CI とスクリプト | 0 | |
| テスト（数えない） | 7 | `kinds_test.go`、`managed_runtime_test.go`、`prompt_paste_test.go`、`session_move_kinds_test.go`、`kind_lists_test.go`、`derivedKindLists.test.ts`、`availability.test.ts`（スタブは接続が要らないので shell と並んで出る） |
| Markdown（数えない） | 2 | `guide/ref/agents.md` と `.ja.md` |

16 のうち 13 はガードかコンパイラが強いた。残る 3 つはガイドに従ったもので、2 つのロケール
（起動のヒントはメッセージキーでなければならないが、既存のキーでもコンパイルは通る）と移動先の一覧
（分類しない種別は `not_an_agent` になり、エージェントには誤り）。§20.7 の種別固有の行は、スタブが
通らないファイルを足す。その大半は、フェーズ 6 がすでに挙げている CI と `deploy/local` での固定 CLI
の登録。レジストリが吸収できる編集は #2933。

## 追記（2026-10-29）— マネージド driver の `threadHandle` メソッドは種別ごとに残す

フェーズ 3 のマネージドの足場（#2869）が共有したのはパッケージ水準の関数だけ。ハンドルが繰り返す
メソッドは**切り出さない**（#2918）。

**`2012d2229` で計測。** copilot、cursor、kiro は `acp.Handle` を埋め込むので、実装は acp、codex、
opencode、muse、lcpp の 5 つ。メソッド本体はコメントと空行を落とし、`h.tq()` を `h.q` と読んで比べた。

| 同一の数 | メソッド |
|---|---|
| 5 つすべて | `Events`、`RemoveQueued`、`DismissDiscard` |
| 4（muse 以外） | `emit`、`Send`、`parkForMove`、`Abort` |
| 4（lcpp 以外） | `Snapshot`、`Alive` |
| 3 | `SendQueued`（acp、codex、lcpp）、`Interrupt` と `interruptAll`（codex、opencode、muse）、`Busy`、`HoldsEntry`、`Steer`（acp、opencode、lcpp） |
| 持つ種別ごとにすべて異なる | `accept`、`pump`、`runTurn`、`Respond`、`UpdateSettings` |

4 つ以上で同一の 9 メソッドは、種別あたり約 60 行。Issue は `29f1b6b57` で 7 メソッド・約 45 行と
測ったが、それは #2869 が `Alive` と `Abort` をハンドルへ移す前。

**費用。** どれもハンドルのロック、キュー、状態、イベント、alive、running のフィールドを読むので、
共有するにはそれらを公開した埋め込みのコアが要る。テスト込みで `h.mu` 683 箇所（codex 150、
opencode 181、muse 264、lcpp 88）とその他のフィールド参照の書き換え、さらに 4 種別ぶんの
インターフェース適合の証明のやり直しになる。決定 2 に照らすと、種別あたり約 60 行ではその変更量に
見合わない。

```
for k in codex opencode muse lcpp; do cat workspace/agent/internal/agents/$k/*.go | grep -o '\bh\.mu\b' | wc -l; done
```

**見直す**のは、種別をまたぐ修正がまたこれらのメソッドに入らなければならないときだけ。

## 追記（2026-11-01）— `mcpreg/materialize.go` の writer はパッケージ内のテーブルへ（#2925 第 3 スライス）

`internal/mcpreg/materialize.go` の `writerFor` が、このファイルの種別リテラル分岐 7 つすべてを
持っていた。各 CLI のネイティブ MCP 設定の書き手を選ぶ `switch` である。#2925 は「MCP config writers」
を capability 候補としていたが、`internal/agents/{claude,codex,muse,lcpp}` が `mcpreg` を import
しているため、`mcpreg` は `agents` を import できない（できれば循環する）。そのため capability には
せず、パッケージ内のテーブルにまとめた。種別ごとの測定リストでも、1 製品固有の挙動でもなく、答えは
「その種別のファイルを書く関数はどれか」だけである。`writers` マップはレジストリの正確なキーで引くので、
別名・別の綴り・未知の種別は従来どおり writer なし（skipped）になる。`kindLiteralCeiling` は 112 → 105
（base の実測はちょうど 112、このスライスで 7 減）。`TestWriterForMatchesOldSwitch` が、登録済みの全種別と
別名・大文字小文字・未知の綴りについて、旧 `switch` のそのままのコピーと引き当てを突き合わせる。
種別ごとに書かれるファイルを見る既存の `materialize*` テストは変更なし。残りの箇所は #2925 に残る。

## 追記（2026-11-01）— `moveTargetKinds` / `moveUnmeasuredTargets` を廃止（#2933、第 3 スライス）

「この種別へセッションを移せるか、それは実測済みか」は、種別自身の宣言 `agents.MoveTarget`
（`MoveTarget() MoveTargetState`。測定済みか未測定か）になった。claude、codex、opencode、copilot、agy、
muse、kiro（測定済み）と cursor（未測定）が、各パッケージの `move_target.go` に根拠とともに持つ。
`sessionx` は両方の一覧をレジストリから完全一致のキーで導くので、別名や未知の種別は従来どおり
`not_an_agent` で断られ、lcpp・shell・ssm は何も宣言せず従来の拒否のまま。1 つのメソッドが 1 つの状態を返すので、
「移動先かつ未測定」は表せない。`TestMoveTargetDeclarationIsAKnownState` が範囲外の状態と `moveRefusals`
との重複を落とし、`TestMoveTargetDeclarationsMatchOldLists` が旧一覧のリテラルの写しと
`moveTargetReason` を全種別の組と別名表記で突き合わせる。#3011 の `TestShippedMoveClassificationHolds` は
独立した固定として残る。プレビューの `anyEligibleTarget` は一覧順ではなくレジストリ順の最初の候補を返すが、
それが通すリクエスト検査は可否だけに依存するので答えは変わらない。`agents.Agent` を埋め込むテスト用ラッパーは
任意の能力を隠すため、`editedUsageLimitAgent` が `MoveTarget` を転送する。`scripts/stub-kind-count.sh`
（スタブは自パッケージで `MoveTarget` を宣言する）: テスト・Markdown 込みで 20 ファイル（それまで 21）、
テスト・Markdown 以外 15 ファイル（それまで 16）。`--verify` の全実行は未実施。手書きのまま残るのは
`session.Kind*`、`agentreg`、`kinds.registered`、Console の一覧、`usageMeasuredByKind` / `noChatProvider`。

Follow-ups: #2933.

## 追記 (2026-11-01): Terminal の停止キーを `agents.TUIInterrupter` の後ろへ (#2925、スライス 4)

`sessionx/session_handlers.go` には kind リテラルの分岐がもう無い（Issue の 7 件は、ファイル分割で
`session_create.go` に移った）。そこで今回は `sessionx/session_turn.go`（6 件）を対象にした。分類:
`driverOf` の codex の切替ガードは 1 製品の挙動なので残す（残り 1 件）。`tuiInterruptFor` の停止キーの
`switch`（4 件）と `interruptLiveState` の cursor/copilot の `case`（2 行）は capability の候補だった。
各 kind が docs/log/133 §1 の実測どおりに自分で宣言する: `agents.TUIInterrupter`
（`TUIInterrupt(inSubagentView)` が `TUIInterruptTraits{Keys, Gap, Measured, NeedsWorking}` を返す）を
claude・codex・kiro（Esc）、opencode（二度押しの Esc。サブエージェント表示の変種と自前の間隔つき）、
cursor・copilot（Ctrl+C、`NeedsWorking`）が実装する。実装の無い kind と未登録の kind は、これまでどおり
単発の Esc（未実測）。引きは registry の完全一致キー。`NeedsWorking` は kind が既に宣言している
`LiveStateTraits.State` を読む（cursor と copilot は `LiveState` をそこに宣言済み）ので、
`interruptLiveState` に kind の一覧は無い。`TestTUIInterruptForMatchesOldSwitch` が、旧 `switch` の
リテラルコピーとの一致を、登録済みの全 kind・別名の綴り・未知の kind、サブエージェント表示の有無の
両方で固定する。`TestUsageLimitWrapperKeepsTUIInterrupt` はテスト用ラッパー `editedUsageLimitAgent` が
capability を転送することを確かめる。`kindLiteralCeiling` は 105 → 100（base の実測は 105）。挙動の変更なし。
実機では未確認。

Follow-ups: #2933.

## 追記 (2026-11-01): `usageMeasuredByKind` と `noChatProvider` を廃止 (#2933、第 4 スライス)

どちらのテスト表もレジストリを写しているだけだった。使用量の精度は種別自身の宣言
`agents.UsageMeasurer`（`UsageMeasured()` が `usagex.MeasuredExact` / `Partial` / `None` を返す）になり、
登録済みの全種別が（none も含めて）実装する。何も言わない種別は黙って none と読まれるのではなく
`TestEveryKindDeclaresItsUsageAccuracy` が落ちる。lcpp と muse の長い実測コメントは答えと一緒に移した。
`usageMeasuredForKind` は registry の完全一致キーで引くので、別名や未知の種別はこれまでどおり none。
`TestUsageMeasuredMatchesOldSwitch` が、当時あった種別について旧 `switch` のリテラルコピーとの一致を固定する。
「ヘッドレスのチャット backend が無い」は、kiro・copilot・shell・ssm のマーカー `agents.NoHeadlessChat`
（backend は chatx のコードで、Agent のパッケージからは import できない）。既存の「ちょうど一方」の
ガードは表の代わりにマーカーを読む。`TestLabels` と `wantRuntime` の nil 行は、上の追記ですでに導出済み。
`scripts/stub-kind-count.sh`（更新した `zz.patch`。スタブは両方を自パッケージで宣言し、`kind_lists_test.go` は
もう編集されない）: テスト・Markdown 込みで 19 ファイル（それまで 20）、テスト・Markdown 以外は 15 ファイル（変化なし）。
編集される Go テストは 1 → 0。`--verify` の全実行は未実施。`usageMeasuredForKind` の `switch` は kind リテラルが 4 行だったので、`kindLiteralCeiling` は 100 → 96。
手書きのまま残るのは `session.Kind*`、`agentreg`、
`kinds.registered`、Console の一覧。

Follow-ups: #2933.

## 追記 (2026-11-01): chat のモデル推奨ルールと、Stop 阻止の判定 (#2925、スライス 5)

`chatx/chat.go` には kind リテラルの分岐が 6 件あった（base はモジュール全体で 96 と実測）。分類:
`chatModelFor` の `kind == claude` の 2 行は 1 製品の挙動（claude の自動ターンのモデルと、従来の
`AF_CHAT_MODEL` 既定）なので残す。`recommendedAssistantModelV` の `case` 4 行は kind ごとのルールだが、
どのルールも `visibility`（chatx 自身の deps の上に作られ、agent パッケージからは名指せない。agent
パッケージは chatx を import できない）を受け取るので、`agents` の capability にはできない。そこで
パッケージ内の表 `assistantModelRules` にした。引きは kind の完全一致で、別名や未登録の kind はこれまでどおり
""。`TestAssistantModelRulesMatchOldSwitch` が、旧 `switch` のリテラルコピーとの一致を、登録済みの全 kind・
別名の綴り・未知の kind・2 通りの visibility で固定する。

chat.go の本物の候補はこれ 1 件だったので、chatx の次のファイル `chat_report_reconcile.go` も対象にした。
`reportStopContinued`（2 行）は `agents.StopContinuer` になり、claude と codex が宣言する（codex の managed
ドライバの除外は自分のメソッドの中）。同ファイルの残り 2 行（`reportPaneBusy`、claude 専用の鮮度信号）は
claude の tmux / transcript の挙動。`TestReportStopContinuedKindsMatchOldSwitch` が旧 `switch` のコピーとの一致を
全 kind・別名・未知の kind で、`TestStopContinuedCapabilityGatesManaged` が codex の除外を、
`TestUsageLimitWrapperKeepsStopContinued` がテスト用ラッパーの転送を確かめる。
意図して残したもの: `stopArmBackgroundBusy`（muse・managed の codex）は `agents.BackgroundReporter` に
見えるが、agy も実装しており、今の arm は agy に false を返すので、載せ替えると挙動が変わる。
`sessionx/session_create.go` の live カタログの連鎖（codex・copilot・opencode・lcpp）は
`codexModelsFn` 系のテスト用の継ぎ目と lcpp の注入カタログに結びついている。
`kindLiteralCeiling` は 96 → 90（base の実測は 96）。挙動の変更なし。実機では未確認。

Follow-ups: #2925.

## 追記 (2026-11-02): kind の一覧と、`Kind()` で引くレジストリ (#2933、スライス 5)

`internal/kinds.registered`（順序と製品名）を `internal/session` の `kindTable` として `Kind*` 定数の隣へ移した。
`kinds.All` / `Known` / `Label` のシグネチャは変えず、表を読む。`session` に新しい import は増えない
（モジュール内の依存は同じ 5 パッケージ）。`agentreg` はコンストラクタの一覧を持ち、各 Agent 自身の `Kind()` で
レジストリを組む。kind の名前をマップのキーとして書き直さなくなり、同じ `Kind()` を返す Agent が 2 つあれば起動時に
panic する。順序は挙動（main.go が managed の kind を再接続し、shutdown.go が止める順）なので、
`TestKindTableMatchesShippedList` が kind・製品名・順序を旧一覧のリテラルコピーに固定する。
`TestConstructorsAndKindTableAgree` は、表の行にコンストラクタが無い（またはその逆）と落ちる
（`muse.New()` を外して落ちるのを確認）。`scripts/stub-kind/zz.patch` は作り直し、`docs-check.py` は定数を
読むままで通る。`scripts/stub-kind-count.sh`: テスト・Markdown 以外 14 ファイル（それまで 15）、含めて 18 ファイル
（それまで 19）。`--verify` の全実行は未実施。新しい kind が編集するのは、session の定数と表の行、
コンストラクタ 1 行。手書きのまま残るのは Console の一覧。

Follow-ups: #2933.

## 追記 (2026-11-02): Console の kind 一覧 (#2933、スライス 6)

`SessionKind` は `(typeof SESSION_KINDS)[number]` になり、`SESSION_KINDS` は `as const` のタプルなので、
Console の kind 一覧と型は 1 か所の編集で済む。`availability.test.ts` は起動ゲートの出力をレジストリから
計算し、固定するリテラルは 2 つ（サインイン不要の kind と、サインインが要る kind）だけ。
`derivedKindLists.test.ts` は `repoLaunchKinds` を一覧全体ではなく「今ある kind の順序」で固定する。
新しい kind はどちらのテストも編集しない。`scripts/stub-kind/zz.patch` は合わせて更新した。stub の数:
テスト・Markdown を除いて 14 ファイル（変わらず）、含めて 16（18 から）。`--verify` の完走はしていない。
意図して残したもの: `kindCatalog.ts` の `FALLBACK_KIND_FACTS` は `catalog.json` との一致テストとともに手書きの
まま（stub はまだこれを編集する）。`catalog.json` から導くには、Console のビルドがディレクトリをまたいで
import するか、生成ファイルが要る。今回は行わない。

Follow-ups: #2933.
