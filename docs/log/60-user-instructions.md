# 60. ユーザー指示 — フリート方針とプロジェクト指示の間の「その人の層」

> 状態: **P0〜P2 実装済み**（2026-08-13。ユーザー指示は claude / codex / opencode / copilot / agy /
> kiro の 6 種へ、フリート方針は claude を含む 6 種すべてへ届く。**配れないのは cursor だけ**で、
> それは構造的な理由（§60.3）。実測は §60.3 / §60.4 / §60.17）
> 意思決定: [decisions/0042](../decisions/0042-user-instructions.ja.md)
> 関連: [57-project-tools.md](57-project-tools.md)（配布軸 / 管理軸の区分・本件は**配布軸**） /
> [48-mcp-registry.md](48-mcp-registry.md) §8.2（配布軸の書き込み規約） /
> [39-agent-memory-management.md](39-agent-memory-management.md)（第 3 の場所＝エージェントメモリ） /
> [33-chat-context-usage.md](33-chat-context-usage.md)（毎セッションに乗る文字の重さ） /
> [46-usage-accounting.md](46-usage-accounting.md) / [28-i18n.md](28-i18n.md) / [29-keyboard-system.md](29-keyboard-system.md)
> 対象: Workspace イメージ（entrypoint）/ Workspace Agent（配布器・REST）/ Console（設定モーダル）/ `workspace/workspace-notes.md`

## 60.1 目的

エージェントが常時読む指示には、いま **2 つの層**しかない。

| 層 | 実体 | 所有 | 誰に効くか |
|----|------|------|-----------|
| フリート層 | `workspace/workspace-notes.md`（イメージ焼き込み） | オペレーター | 全ユーザー・全セッション |
| **ユーザー層** | **無い** | — | — |
| プロジェクト層 | 作業コピーの `CLAUDE.md` / `AGENTS.md` | チーム（コミットされる） | そのリポジトリを触る全員 |

だが実際に置き場を欲しがる内容の多くは、**どちらでもない**: 報告の言語と口調、確認を取る粒度、
使ってほしい/使ってほしくない道具、個人的な禁止事項。これらは同僚には押し付けられない
（＝プロジェクト層に置けない）し、他ユーザーにも押し付けられない（＝フリート層に置けない）。

本ドキュメントはその中間層を、**AF が所有する 1 つの成果物**として定義し、各エージェントの
user/global 指示位置へ配る仕組みを決める。

受入条件:

1. 利用者は Console から自分の指示を書け、**対応する全 kind のセッション**に同じ内容が効く。
2. 書いた内容は**コンテナ再起動・recreate・home 掃除を生き残る**。
3. フリート方針を**上書きできない**（優先順位が画面と本文の両方で示される）。
4. 対応していない kind は「対応していない」と画面に出る（黙って効かない、が無い）。
5. リポジトリには一切コミットされない。
6. どの kind の**どのファイル**に、いつ反映されたかが画面で分かる。
7. サイズ超過が起きる前に画面で分かる（§60.9）。

非目標:

- リポジトリ単位・作業グループ単位の出し分け（プロジェクト層＝[57](57-project-tools.md) の 2 号機の仕事）
- エージェントが自分で書き溜めるメモリ（＝[39](39-agent-memory-management.md)。**第 3 の場所**で、本件とは別物）
- ワークスペースをまたぐ共有（v2 の選択肢・§60.14）
- セッション単位の一時指示（それはプロンプトで足りる）

## 60.2 現状の実測（2026-08-13）

配布の実装は 2 箇所しかない。

- `workspace/Dockerfile:520-522` — `workspace-notes.md` を `/usr/local/share/agent-fleet/` へ置き、
  **`/etc/claude-code/CLAUDE.md`（root 所有の managed policy）**へコピー。
- `workspace/entrypoint.sh:566-575` — 毎起動、同じ本文を **`cp -f`** で
  `~/.codex/AGENTS.md` と `~/.config/opencode/AGENTS.md` へ上書き。

本開発環境での実サイズ:

| ファイル | 実測 | 中身 |
|---|---|---|
| `/etc/claude-code/CLAUDE.md` | 29,521 B | フリート方針そのもの |
| `~/.config/opencode/AGENTS.md` | 29,521 B | 同上 |
| `~/.codex/AGENTS.md` | 29,972 B | 同上 ＋ rtk ブロック 451 B |
| `~/.gemini/AGENTS.md` | **450 B** | **rtk ブロックのみ** |

ここから 2 つの実害が出る。

**実害① — codex / opencode では「ユーザーが書き足せる唯一の場所」が毎起動で消える。**
`cp -f` は利用者の追記を保存しない。ユーザー層が無いだけでなく、**自力で作ることもできない**
（作っても次の起動で失われる。しかも失敗の告知が無い）。

**実害② — agy / cursor / kiro / copilot はフリート方針すら読んでいない**（→ P2 で解消。cursor を除く）**。**
`~/.gemini/AGENTS.md` が rtk ブロック 450 B しか無いことがその証拠。copilot は実測で system prompt が
15.4k トークン（`copilot -p` の使用量表示）で、その中にフリート方針は含まれていない。
**[39](39-agent-memory-management.md) の棚卸し表（「共通」行）が「`AGENTS.md`（codex/opencode/agy）」
としているのは誤り**で、agy は対象外である（本ドキュメントで訂正済み）。
ユーザー層の配線はこの穴と同じ形をしているので、同じ配布器で塞いだ（§60.13 P2）。

## 60.3 各 kind の配り方（実測で確定）

計測対象の版: claude 2.1.229 / codex 0.147.0 / opencode 1.18.18 / copilot 1.0.79 /
agy 1.1.12 / kiro 2.16.0 / cursor 2026.08.11-e8db854。実測手順は §60.17。

| kind | 配り方（採用） | AF が他人のファイルを触るか | 根拠 |
|---|---|---|---|
| claude | `$CLAUDE_CONFIG_DIR/CLAUDE.md`（user memory）を **AF が単独所有**。既定では存在しないファイルなので合成不要 | いいえ | ✅ 実測（カナリア） |
| codex | `$CODEX_HOME/AGENTS.md` に**マーカー合成**。**これが唯一の手**（追加指示ファイルを指す設定キーは 0.147.0 に無い） | はい（合成） | ✅ 実測（`codex debug prompt-input`） |
| opencode | `~/.config/opencode/opencode.json` の **`instructions` 配列に AF 専用ファイルを 1 本足す**。`AGENTS.md` には触らない | 設定 1 キーのみ | ✅ 実測（行動カナリア） |
| copilot | **`$COPILOT_HOME/instructions/agent-fleet-user.instructions.md`**（AF 専用の名前のファイル 1 本） | AF 専用ファイル | ✅ 実測（行動カナリア） |
| agy | `~/.gemini/AGENTS.md` に**マーカー合成** | はい（合成） | [32](32-agy-agent-kind.md) Track A 実測 |
| kiro | **`~/.kiro/steering/agent-fleet-user.md`**（global steering ディレクトリ内の AF 専用ファイル 1 本） | AF 専用ファイル | ✅ 実測（行動カナリア） |
| cursor | **ローカルのユーザー層は存在しない＝未対応**。User Rules はサーバー側（`aiserver.v1.UserRules` protobuf）で、ローカルの rules 収集（`.cursor/rules/**/*.mdc` / `AGENTS.md` / `CLAUDE.md` / `CLAUDE.local.md` / `.cursorrules`）は全て **rootDirectory（プロジェクト）基準** | — | ✅ 静的実測・**対応不可で確定** |

copilot の user スコープは実測で 3 経路とも効いた。採るのは**ディレクトリ内の専用ファイル**:

```
$COPILOT_HOME/copilot-instructions.md                  … 効く。ただし利用者のファイルなので所有しない
$COPILOT_HOME/instructions/**/*.instructions.md        … 効く。★AF 専用の名前で 1 本置く（採用）
COPILOT_CUSTOM_INSTRUCTIONS_DIRS=<dir>                 … 効く。ファイルを一切持たずに済むが不採用
```

env を採らなかった理由: 効かせるには tmux 起動 / managed ACP ドライバ / 利用者が手で叩く
`copilot` の**3 経路すべて**に export を配る必要があり、1 つ漏れると「そのセッションだけ
効かない」という画面から見えない穴になる。ファイルならどの起動経路でも同じように読まれる。
$COPILOT_HOME 配下に AF 専用ファイルを持つのは rtk（`hooks/rtk.json`）で既に踏んでいる前例。

opencode のバイナリから読み取れた収集規則（1.18.18）:

```
global  = [ <config>/AGENTS.md,  …!disableClaudeCodePrompt ? [ <home>/.claude/CLAUDE.md ] : [] ]
project = [ "AGENTS.md", …!disableClaudeCodePrompt ? ["CLAUDE.md"] : [], "CONTEXT.md" ]   ← 上位へ遡って収集
              （OPENCODE_DISABLE_PROJECT_CONFIG で project 側は無効化される）
```

## 60.4 実測で分かったこと — 罠 A は形を変え、罠 B は否定された

### A. `~/.claude/CLAUDE.md` は **どの kind にも効かない**（当初の想定より単純で、より危険）

- **claude は `$CLAUDE_CONFIG_DIR/CLAUDE.md` を読む。** 両候補に別々のカナリアを置いて
  `claude -p` に列挙させたところ、返ったのは `$CLAUDE_CONFIG_DIR` 側だけだった。
  AF は `CLAUDE_CONFIG_DIR=/var/lib/af/claude` を渡している
  （`control-plane/runtime_docker.go:222` / `runtime_ecs.go:363` / `runtime_native.go:556`）ので、
  **`~/.claude/CLAUDE.md` に書いても claude には届かない**。
- **opencode も拾わなかった。** バンドルには `<home>/.claude/CLAUDE.md` を読む経路があるが（§60.3）、
  実行時のカナリアは `NO`。対照質問（フリート方針の見出しを答えさせる）は正しく答えたので、
  global 指示自体は読めている＝**この経路は本環境では効いていない**。条件は未特定（`disableClaudeCodePrompt`
  の既定値・v2 catalog 経路の可能性）。**上流バンドルの読み取りと実挙動が食い違った例として記録する。**

したがって置き場を誤ると「どこにも効かない」という、画面からは絶対に分からない壊れ方になる。
（`~/.claude` は `workspace/agent/fs.go` の denylist にも入っており、ファイルペインからも見えない。）

### B. codex のバイト予算は global を含まない — **既存バグは無かった**

`codex debug prompt-input`（**API 呼び出しなしでモデル可視プロンプトを JSON 出力する**）で測った:

| 条件 | 結果 |
|---|---|
| global 29,972 B ＋ project 41,045 B | **project 側だけ**が約 32,768 B で切れる（末尾カナリア消失）。global は無傷 |
| `-c project_doc_max_bytes=1000` | **project だけ**が 1,000 B に切れる。global（rtk ブロック含め）は無傷 |
| 一時 `CODEX_HOME` に **42,042 B** の global | **切られない**（先頭・末尾カナリアとも通過） |
| git repo 内で親 20,021 B ＋ 子 19,220 B | 親は全採用、**子が途中で切断**（合計 32,768 B）＝ **チェーン合計の予算**、root→cwd 順 |
| 非 git ディレクトリ | 親の `AGENTS.md` は読まれない（cwd のみ） |

結論: **`project_doc_max_bytes`（既定 32 KiB）はプロジェクト文書チェーンの合計にのみ効き、
`$CODEX_HOME/AGENTS.md` は予算外・上限なし。** フリート方針がプロジェクトの `AGENTS.md` を
圧迫している事実は無く、当初疑った既存バグは存在しない。よってユーザー層の上限は
**切断回避ではなく費用のため**の設計になる（§60.9）。

## 60.5 位置づけ — 配布軸であり、かつ「他人のファイルに書かない」

[57](57-project-tools.md) §0 の 2 軸で言えば、本件は**配布軸**（AF が各 CLI の user/global 設定を
**自動で**書く・所有を明示する）。「プロジェクトファイル憲章 8 条」は適用されない。本機能が守る 6 条:

1. **AF が所有し、所有範囲を明示する。** 合成する場合はマーカーで囲み、マーカー外は温存する
   （`cp -f` による全消しはやめる ＝ 実害①の解消）。
2. **自動契機で書いてよい。** 起動時 reconcile と Console 保存の 2 契機。冪等で、変化が無ければ書かない。
3. **1 ファイルにライターは 1 人。** 追記者を増やさず、対象ファイルは毎回まるごと組み立て直す（§60.7）。
4. **優先順位は本文に書く。** フラットな 1 ファイルに合成する以上、階層の信号は散文でしか伝わらない。
5. **コミットされる場所には絶対に書かない。** 対象は home / CLI 設定ディレクトリのみ。
6. ★ **「他人のファイルに書く」より「AF 専用のファイル＋参照」を優先する。**
   実測の結果、claude / opencode / copilot は**参照で足りる**ことが分かった（§60.3）。
   合成は参照の手段が無い kind（codex / agy）だけの最後の手段にする。

## 60.6 置き場と永続性

正本は **`~/.config/agent-fleet/user-notes.md`**（`rtk.json` / `toolchains.json` と同居）。

- `control-plane/runtime_docker.go:396` の `homeKeep` に `.config` があるため、
  **「home 掃除」でも消えない**。recreate は `~/repos` しか消さないので当然生き残る。
  ＝ AF のどの製品操作でも失われない（受入条件 2）。
- `workspace/agent/fs.go` の `fsDeny` に `.config/agent-fleet` があるため、
  **ファイルペインからは読めず、編集経路は Console の REST だけ**になる（§60.12）。
- CP の DB には置かない（v1）。ワークスペースを跨いで共有したくなったときに初めて再検討する（§60.14）。

メタデータ（適用先 kind の選択・有効/無効）は同ディレクトリの `user-notes.json` に持つ。

配布物（各 kind へ渡す実体）はすべて **`~/.config/agent-fleet/instructions/` 配下に AF が生成**する:

```
~/.config/agent-fleet/user-notes.md              ← 正本（利用者が書く）
~/.config/agent-fleet/instructions/opencode.md   ← opencode.json の instructions が指す
~/.config/agent-fleet/instructions/copilot/af-user.instructions.md
                                                 ← COPILOT_CUSTOM_INSTRUCTIONS_DIRS が指す
$CLAUDE_CONFIG_DIR/CLAUDE.md                     ← claude は所定位置に置くしかない（AF 単独所有）
```

## 60.7 配布器 — 参照が第一、合成は codex / agy だけ

`agent_rtk.go` の `reconcileAgentRTK` を **`reconcileAgentInstructions`** へ格上げし、
「durable な設定 → 各 kind の artifact」という既存の型（`~/.config/agent-fleet/rtk.json` +
起動時 reconcile + Console からの live 適用）をそのまま使う。kind ごとの手段:

| kind | 手段 | 備考 |
|---|---|---|
| claude | `$CLAUDE_CONFIG_DIR/CLAUDE.md` の `user-notes` ブロック | managed policy には触らない（root 所有） |
| opencode | AF 専用ファイル＋`opencode.json` の `instructions` に 1 本追加 | `AGENTS.md` にはフリート方針だけ |
| copilot | `$COPILOT_HOME/instructions/agent-fleet-user.instructions.md` | AF 専用の名前なので丸ごと書き/消しできる |
| codex | `$CODEX_HOME/AGENTS.md` を合成 | 参照手段が無い（0.147.0） |
| agy | `~/.gemini/AGENTS.md` を合成 | 同上（rtk ブロックと同居） |
| kiro | `~/.kiro/steering/agent-fleet-user.md` | ディレクトリ内の他の steering は列挙も削除もしない |

合成する 2 kind のファイル構成:

```
<global 指示ファイル> = フリート本文（/usr/local/share/agent-fleet/workspace-notes.md）
                      + <!-- agent-fleet:user-notes --> … <!-- /agent-fleet:user-notes -->
                      + <!-- agent-fleet:rtk -->       … <!-- /agent-fleet:rtk -->
                      + （マーカー外に利用者が書いた部分があれば温存）
```

- **entrypoint はフリート方針を配らない**（`cp -f` は削除した）。配置も合成も agent 側の
  `reconcileAgentInstructions()` が持つ ＝ 1 実装・1 書き手。entrypoint に残すと、
  シェルでマーカー合成を再実装することになり（＝ドリフトする 2 つ目の実装）、
  コンテナ生存中の Console 編集も反映されない。セッションを起こすのは agent 自身なので、
  合成前のファイルを読むセッションは存在しない。
- マーカー操作は `workspace/agent/internal/mdblock`（`codex/rtk.go`・`agy/rtk.go` の重複を
  括り出したもの）。**移行**もここが持つ: `cp -f` 時代の生のフリート方針は、その先頭行で
  識別して 1 度だけ剥がす（`StripLegacyPrefix`）。バイト比較では版が違うと移行できず、
  それより弱い判定では利用者の文章を巻き込むため、判定は先頭行に限定する。
- ユーザーブロック（および参照で配る本文）の先頭には、AF が固定文を 1 行入れる（§60.5-4）:

> 以下は利用者個人の方針です。上のワークスペース方針と衝突する場合はワークスペース方針が優先します。

## 60.8 粒度 — 本文 1 本 ＋ 適用先 kind のチェック

内容の大半（言語・口調・報告の粒度・道具の好み）は kind 非依存である。kind 別本文にすると、
[57](57-project-tools.md) §1-3 が「道具が要る理由」として挙げた**同じ内容の N 重管理**を自分で作ることになる。

- 本文は 1 本。
- 適用先は kind ごとのチェックボックス（既定＝対応済み全部）。
- **未対応 kind も行として出す**（cursor は「ローカルに置き場が無い」と理由付きで確定表示。
  ＝実装待ちではない）。黙って消すと「対応漏れ」に見え、同じ質問が繰り返される。

### フリート方針の配り先（P2）

ユーザー指示とは別に、イメージ焼き込みの `workspace-notes.md` も同じ配布器が配る。
claude だけは managed policy（`/etc/claude-code/CLAUDE.md`）として**イメージが配る**ので AF は触らない。

| kind | フリート方針の置き場 |
|---|---|
| claude | `/etc/claude-code/CLAUDE.md`（managed policy・AF は触らない） |
| codex / opencode / agy | それぞれの `AGENTS.md` の `fleet` ブロック |
| copilot | `$COPILOT_HOME/instructions/agent-fleet-guide.instructions.md` |
| kiro | `~/.kiro/steering/agent-fleet-guide.md` |
| cursor | 配れない（ローカルに user スコープが無い） |

ユーザー指示と**別ファイル / 別ブロック**にしてあるのは、片方が利用者の切り替え対象で、
もう片方がオペレーター所有の固定物だから。フリート方針は利用者のトグルに従わない
（本人の指示を全部オフにしても配られる）。

## 60.9 サイズ上限は「費用」の話（切断回避ではない）

罠 B が否定された（§60.4）ので、上限の理由は**トークン費用ひとつ**になる。フリート層だけで
毎セッション約 30 KB（≒8〜10k トークン）が固定費として乗っており、ユーザー層はその上に積む
（[33](33-chat-context-usage.md) / [46](46-usage-accounting.md)）。

- **ハード上限 8 KB**（保存時に拒否）。根拠は費用であって truncation ではない、と UI 文言でも言う。
- エディタに実バイト数を常時表示する。
- codex の予算表示は**不要**（global は予算外と実測済み）。代わりに「1 セッションあたりおよそ何トークン
  増えるか」を出す方が意思決定に効く。
- ⚠️ **P2 で agy / copilot / kiro のセッション開始コストが約 30 KB ぶん増えた**（それまで
  フリート方針を読んでいなかったため）。これは「読んでいなかった」ことの是正であって、
  無料ではない。フリート方針自体を痩せさせる話は §60.15-3 に残っている。

## 60.10 REST（Agent 側だけで閉じる）

| メソッド | パス | 役割 |
|---|---|---|
| GET | `/user-notes` | 本文・バイト数・適用先・kind 別の反映状態（配り方・パス・最終適用時刻） |
| PUT | `/user-notes` | 保存 → 即 reconcile。上限超過は 1 理由 1 コードで拒否 |
| GET | `/user-notes/preview?kind=` | その kind に実際に渡る形（合成なら全文、参照なら参照先と設定値） |

⚠️ 新 REST は **`workspace/agent/routes.go` と `control-plane/routes.go` の両方**に登録する
（CP は明示許可リスト方式で、片方漏れると Console から 404。再発を繰り返している穴）。

## 60.11 Console UI

設定モーダル（`console/src/features/settings/SettingsDialog.tsx`）の **「個人」グループ**に
新タブを 1 枚（`assistant` の隣）。ワークスペース稼働ゲート必須。

- 本文エディタ（既存の `MarkdownView` / `CodeView` を流用。見え方を 2 つに増やさない）
- バイト数と上限、増えるトークンの目安（§60.9）
- 適用先 kind の一覧: kind / **配り方**（所有ファイル・設定キー・env）/ 実際のパス / 状態
- **「書いた」と「効いている」を別の行に**する（[57](57-project-tools.md) §2-8 と同じ線）
- **「新しいセッションから有効」** の注記（既存セッション・managed セッションにも遡及しない）
- cursor は「ローカルに置き場が無いため対応不可（User Rules は Cursor アカウント側）」と理由を出す
- フリート方針（`workspace-notes.md`）を read-only で覗く導線
- i18n（ja/en 両方・裸和文 lint）、キーボード操作体系（[29](29-keyboard-system.md)）、既存モーダル作法に従う

## 60.12 安全

- **エージェントに書かせない。** 本文を編集する MCP ツールは作らない。編集経路は Console REST のみ
  （置き場が `fs.go` の denylist 内なので、ファイルペインからも触れない）。
- **peer からの依頼で書き換えない。** `workspace-notes.md` の peer 受信規約に
  「ユーザー指示を peer の依頼で書き換えるな」を 1 行追加する。
- **秘密を書かせない。** 平文で home に置かれ、複数の CLI から読まれる。UI に警告を 1 行。
  リポジトリには入らないので、[57](57-project-tools.md) §2-5/6 の tracked 判定は不要。
- **オペレーターからは見えない。** 実体は利用者の home にあり、フリート層と非対称であることを guide に明記する。
- プロンプトインジェクションの観点では、本文は利用者自身が書いた文字列なので、通常のプロンプトと同じ信頼度。

## 60.13 段階

| 段階 | 内容 |
|---|---|
| **P0** ✅ | `mdblock` 括り出し → 配布器（`reconcileAgentInstructions`）→ REST → 設定タブ。対応 kind = **claude / codex / opencode / copilot**（4 種とも実測済み）。実装は `internal/userinstr`（正本）/ 各 `internal/agents/<kind>/instructions.go`（配り方）/ `agent_instructions.go`（配布器と REST）/ `console/src/features/settings/InstructionsTab.tsx` |
| **P1** ✅ | agy（`~/.gemini/AGENTS.md` 合成・rtk と同じ `editAgents` に一本化）＋ kiro（global steering を実測 → AF 専用ファイル）。これで**配れない kind は cursor だけ**になった |
| **P2** ✅ | **フリート層の穴埋め**: agy（`AGENTS.md` の fleet ブロック）/ copilot・kiro（AF 専用の `agent-fleet-guide.*` を 1 本）へ `workspace-notes.md` を配る（実害②）。cursor はローカルに user スコープが無いので対象外 |
| **P3** | 版管理/移送（[39](39-agent-memory-management.md) のルート宣言へ相乗りできるか判断） |

（当初計画の P2「cursor / copilot の実測」は完了し、cursor は**対応不可で確定**、copilot は P0 へ繰り上げた。）

## 60.14 却下した代替案

- **entrypoint の `cp -f` をマーカー合成にするだけ（UI なし）。** 実害①は消えるが、利用者は kind ごとに
  N 箇所へ同じ文章を書く羽目になり、claude は `/etc` なので原理的に不可能。→ 却下。
  ただし「AF 管理領域をマーカー化して外側を温存する」部分は**採用**（§60.7）。
- **全 kind を AGENTS.md への合成で統一する。** 実装は 1 本化されるが、実測の結果 claude / opencode /
  copilot は**他人のファイルを触らずに配れる**と分かった（§60.5-6）。統一のために共有ファイルへの
  書き込みを増やすのは割に合わない。→ 却下。
- **セッション起動時に `--append-system-prompt` 相当で渡す。** claude しか揃わず、TUI 起動コマンドが
  肥大し、managed 経路と二重になる。→ 却下（copilot の env 注入はこれと違い、CLI の正式な
  ユーザースコープ機構を使っているので採用）。
- **CP の DB に置いてワークスペース跨ぎで共有。** 永続性の要求は home 側で満たせている。v2 の選択肢。
- **kind ごとに別本文（§60.8）。** 二重管理を作るため却下。

## 60.15 残る未決

1. ~~kiro の global steering~~ **→ 解決（2026-08-13 実測）**。`~/.kiro/steering/*.md` は読まれる
   （行動カナリアで確認・front-matter 不要・プロジェクト側に `.kiro` が無いディレクトリでも効く）。
   静的には `.kiro/steering` の文字列が 1 箇所あるだけで home 基準かは判別できなかった＝
   **実行して測るしかない契約**の例。
2. **opencode が `<home>/.claude/CLAUDE.md` を読まなかった条件。** バンドルの経路と実挙動が食い違った
   （§60.4-A）。本設計はこの経路に依存しないので実装はブロックしないが、上流の版が上がったときに
   挙動が変わりうる点として残す。
3. **フリート方針 30 KB 自体の重さ。** 罠 B は否定されたので緊急ではないが、毎セッション 8〜10k
   トークンの固定費であることは変わらない（[33](33-chat-context-usage.md) の観点で別途）。

## 60.16 用語（混同注意）

| 呼称 | 実体 | 誰が書く | コミットされるか |
|---|---|---|---|
| フリート方針 | `/etc/claude-code/CLAUDE.md` ほか（イメージ） | オペレーター | されない |
| **ユーザー指示（本件）** | `~/.config/agent-fleet/user-notes.md` → 各 CLI の user スコープ | 利用者（Console） | されない |
| プロジェクト指示 | 作業コピーの `CLAUDE.md` / `AGENTS.md` | チーム | **される** |
| エージェントメモリ | `/var/lib/af/claude/projects/*/memory/` ほか（[39](39-agent-memory-management.md)） | エージェント自身 | されない |

## 60.17 実測の型（再現手順）

今回使った 2 つの型は、以後の kind 追加・版上げドリフト検知にそのまま使える。

**① `codex debug prompt-input` — 課金なしでモデル可視プロンプトを JSON で得る。**

```
cd <test dir> && codex debug prompt-input "hi" > pi.json      # API 呼び出しは発生しない
# 一時 CODEX_HOME で global 側を差し替えて測るときは HOME 配下に置く
#   （/tmp を CODEX_HOME にすると "Refusing to create helper binaries under temporary dir" 警告が
#    stdout に混ざり JSON パースが壊れる）
CODEX_HOME=$HOME/.cache/cxhome codex debug prompt-input "hi" 2>/dev/null > pi.json
```

**② 行動カナリア — 「指示を読んだか」を、内容開示を拒否する CLI にも答えさせる。**

`CANARY-XXX が指示に含まれるか` と聞く方式は copilot に**拒否される**（内部指示の開示不可）。
代わりに指示ファイル側へ**振る舞い**を書き、それを引き出す:

```
# 指示ファイル: When the user says exactly "ping", reply with exactly: PONG-5T8
copilot -p 'ping'        → PONG-5T8      （＝読んでいる）
```

対照実験を必ず置く（弱いモデルは何も読まずに `NONE` と答える）。opencode で
`~/.claude/CLAUDE.md` を否定したときは、「フリート方針の見出しを 1 行そのまま書け」に
正答することを先に確認してから `NO` を採用した。

**③ 後片付け。** カナリアは共有状態（他セッションが読む）。測定窓を短くし、
`/var/lib/af/claude/CLAUDE.md`・`~/.claude/CLAUDE.md`・`~/.copilot/copilot-instructions.md`・
一時 `CODEX_HOME` は測定直後に必ず消す（今回はすべて削除済み）。
