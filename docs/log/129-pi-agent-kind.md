# 129. pi エージェント種別の評価 — Stage 0（同一 ChatGPT モデルで codex と比較）

status: **Stage 0 完了（2026-10-03）**。pi 1.0.0 を本 Workspace に入れ、**同じ ChatGPT サブスクリプション・同じモデル
（`gpt-5.5`・reasoning medium）** で codex 0.160.0 と 3 課題 × 2 回ずつ走らせた。正答率は両者とも 100%（差が付かない
小課題）、**入力トークンは pi が codex の約 1/16、価格換算で約 1/3**（§3）。Stage 1 の G1〜G4 は RPC から
**全部実測で通った**（§4）。opencode は ChatGPT 未サインインのため比較に入れていない（§5）。
推奨は **「有望・ただし ADR の前に実課題で Stage 0b」**（§6）。Issue: #1585。

## 0. 対象と環境

- **pi** = earendil-works/pi の `@earendil-works/pi-coding-agent` **1.0.0**（2026-10-01 公開・npm の latest）。
  `npm install -g --ignore-scripts` でユーザーの npm prefix（nvm の Node 22.23.3 配下）に入れた。
- **codex** = codex-cli **0.160.0**（イメージ焼き込み済み・ChatGPT ログイン済み）。
- **opencode** = 1.18.34。モデル一覧は opencode 自身の無料枠のみで、OpenAI/ChatGPT は未設定。
- 資格情報: pi はユーザー本人が `/login` で ChatGPT にサインイン（OpenAI が pi での利用を認めている。#1585 のコメント）。
  **Claude サブスクリプションは pi では使わない**（Anthropic の規約上、OAuth は Claude Code 等の自社アプリ専用）。
- 共有ホスト・メモリ 10 GiB 上限の Workspace。プローブは 1 本ずつ、`env -u AF_SESSION_NAME` で起動。

## 1. 導入時に分かったこと（モデル呼び出しなし）

| 項目 | 結果 | 備考 |
|---|---|---|
| 導入 | ◎ npm 1 コマンド・7 秒・122 パッケージ | バイナリは `~/.nvm/versions/node/<ver>/bin/pi`。**ユーザーの Terminal セッションでは PATH に無かった**（nvm 未読込のシェル）＝焼くなら固定パスが要る |
| `pi --mode rpc` | ◎ 認証なしで起動し `get_state` / `get_commands` / `get_session_stats` に応答 | JSONL・`id` で応答を対応付け |
| プロジェクト信頼 | ◎ RPC / print / json では**プロンプトを出さない** | `.pi/settings.json` を置いても止まらず、既定（`defaultProjectTrust: "ask"`）では**黙ってプロジェクト資源を読まない**。明示は `--approve` / `--no-approve`。`AGENTS.md` / `CLAUDE.md` は信頼と無関係に読む |
| af MCP 登録 | ◎ `pi mcp add <name> -- workspace-agent mcp-stdio --self-report` | af の session 用 MCP は stdio なので**トークンを設定ファイルに書かずに済む**（環境変数を継ぐ）。`pi mcp list --json` で `connected`・9 ツール |
| MCP の見せ方 | 注意 | 既定 exposure は `codemode`（モデルにはツール定義を出さず、スクリプトから探して呼ぶ）。af のように少数ツールなら `--exposure direct` が素直 |
| ChatGPT ログイン | 注意 | 1.0.0 で旧 `OpenAI Codex` プロバイダは **「OpenAI Codex (legacy)」** に改名され、**OpenAI プロバイダの "Sign in with ChatGPT"** が後継。OAuth のコールバックは `127.0.0.1:1455` 固定で、**ブラウザ側からは届かない**＝リダイレクト URL を pi に貼り戻す（device flow もある）。AF の接続カードにするなら codex と同じ「URL 貼り戻し」か device flow |
| 資格情報の露出面 | 注意 | `pi auth print-bearer-token --provider openai` が **OAuth トークンを標準出力に出す**正規コマンド。同 uid のエージェントシェルから読めるのは codex の `auth.json` と同じ性質だが、コマンド一発で出る点は方針に書く必要がある |
| 外向き通信 | 注意 | `enableInstallTelemetry` が**既定 true**（インストール/更新の匿名報告＋プロバイダへの帰属ヘッダ）。`PI_TELEMETRY=0` で止まる。`/share` は `pi.dev` にセッションを上げる |

## 2. 方法

- 課題（Python・stdlib の unittest のみ。各課題に公開テスト＋**エージェントに見せない隠しチェック**）:
  - **t1 実装**: docstring の仕様どおりに `slugify()` を書く（非 ASCII・区切りでの切り詰め・空入力）。
  - **t2 バグ修正**: `merge_intervals()` に仕込んだバグ（未ソート・接する区間・包含区間・入力破壊）を直す。
  - **t3 リファクタ**: 3 関数に重複する行パースを `parse_orders()` に抽出（隠しチェックは `.split(` が 1 箇所だけか、3 関数が使っているか）。
- 判定: 公開テスト合格 ∧ テストファイル無改変 ∧ 隠しチェック合格。参照解で 3 課題とも全部通ることを先に確認した。
- 各試行は新しい git リポジトリを作って 1 回だけプロンプトを渡す（ヘッドレス・セッション保存なし）。
  - **pi**: `pi --mode rpc --no-session --model openai/gpt-5.5 --thinking medium`。`prompt` を送り、`agent_settled` で
    `get_session_stats` を取る。ツールは既定の 4 つ（read/bash/edit/write）、MCP なし。
  - **pi+guide**: 上に `--append-system-prompt /etc/claude-code/CLAUDE.md`（Workspace の運用規約・14 KB）を足した対照。
    codex は af が配る同等の規約（`~/.codex/AGENTS.md`）を毎回読むので、**ハーネス差と規約の重さを切り分ける**ため。
  - **codex**: `codex exec --json --ephemeral --ignore-user-config --dangerously-bypass-approvals-and-sandbox -m gpt-5.5
    -c model_reasoning_effort="medium"`。`--ignore-user-config` で MCP サーバ設定は外れるが、**規約（AGENTS.md）と
    スキル一覧は残る**（`codex debug prompt-input` で確認: developer 約 14 K 字＋ user 側 AGENTS.md 約 14.7 K 字）。
- トークンの数え方: pi は `input`（キャッシュ外）と `cacheRead` を分けて返す。codex の `input_tokens` は
  キャッシュ分を含む。表の「入力計」は両者とも **キャッシュ込みの総入力**、「うち非キャッシュ」は総入力 − キャッシュ読み。
- 価格換算は pi 同梱カタログの gpt-5.5 単価（入力 $5 / キャッシュ読み $0.5 / 出力 $30・いずれも 100 万トークンあたり）で
  計算した**参考値**。ChatGPT サブスクリプションでは実際の請求は発生せず、プランの利用枠がトークンに比例して減るかは
  **ベンダー非公開**。

## 3. 実測値（2026-10-03・gpt-5.5・medium・全 18 試行）

| ハーネス | 課題 | 回 | 壁時計 s | 入力計 | うちキャッシュ | 非キャッシュ | 出力 | モデル呼出 | ツール呼出 | 公開テスト | 隠し |
|---|---|---|---|---|---|---|---|---|---|---|---|
| pi | t1 | 1 | 30.2 | 9,720 | 4,096 | 5,624 | 971 | 5 | 5 | ✓ | ✓ |
| pi | t1 | 2 | 42.2 | 10,231 | 4,096 | 6,135 | 1,491 | 5 | 5 | ✓ | ✓ |
| pi | t2 | 1 | 26.0 | 8,123 | 3,072 | 5,051 | 397 | 5 | 5 | ✓ | ✓ |
| pi | t2 | 2 | 21.5 | 10,201 | 3,072 | 7,129 | 395 | 5 | 5 | ✓ | ✓ |
| pi | t3 | 1 | 35.7 | 11,234 | 3,072 | 8,162 | 861 | 6 | 5 | ✓ | ✓ |
| pi | t3 | 2 | 35.7 | 9,684 | 3,072 | 6,612 | 861 | 5 | 5 | ✓ | ✓ |
| pi+guide | t1 | 1 | 27.4 | 27,603 | 13,824 | 13,779 | 792 | 5 | 5 | ✓ | ✓ |
| pi+guide | t1 | 2 | 29.5 | 27,718 | 19,456 | 8,262 | 851 | 5 | 5 | ✓ | ✓ |
| pi+guide | t2 | 1 | 24.8 | 28,527 | 20,480 | 8,047 | 395 | 5 | 5 | ✓ | ✓ |
| pi+guide | t2 | 2 | 20.5 | 26,829 | 23,040 | 3,789 | 394 | 5 | 5 | ✓ | ✓ |
| pi+guide | t3 | 1 | 27.5 | 27,280 | 19,456 | 7,824 | 840 | 5 | 4 | ✓ | ✓ |
| pi+guide | t3 | 2 | 27.5 | 27,258 | 24,064 | 3,194 | 851 | 5 | 4 | ✓ | ✓ |
| codex | t1 | 1 | 62.7 | 223,032 | 204,544 | 18,488 | 2,244 | – | 12 | ✓ | ✓ |
| codex | t1 | 2 | 59.3 | 199,659 | 168,320 | 31,339 | 1,797 | – | 10 | ✓ | ✓ |
| codex | t2 | 1 | 31.6 | 93,192 | 77,696 | 15,496 | 964 | – | 9 | ✓ | ✓ |
| codex | t2 | 2 | 56.6 | 173,768 | 164,224 | 9,544 | 1,815 | – | 14 | ✓ | ✓ |
| codex | t3 | 1 | 48.2 | 178,411 | 166,912 | 11,499 | 1,834 | – | 9 | ✓ | ✓ |
| codex | t3 | 2 | 29.6 | 72,632 | 69,120 | 3,512 | 1,050 | – | 6 | ✓ | ✓ |

6 試行の合計・平均:

| ハーネス | 入力計 合計 | 非キャッシュ 合計 | 出力 合計 | 平均壁時計 s | 平均ツール呼出 | 価格換算 合計（参考） | 正答 |
|---|---|---|---|---|---|---|---|
| pi | 59,193 | 38,713 | 4,976 | 31.9 | 5.0 | $0.35 | 6/6 |
| pi+guide | 165,215 | 44,895 | 4,123 | 26.2 | 4.7 | $0.41 | 6/6 |
| codex | 940,694 | 89,878 | 9,704 | 48.0 | 10.0 | $1.17 | 6/6 |

読み方:

- **初回リクエストの固定費**（システムプロンプト＋ツール定義＋課題文）は pi が **1,195〜1,230 トークン**、
  pi+guide が 4,841〜4,876（実測・各試行の最初の assistant メッセージの usage）。issue の「1k 未満」というベンダーの
  主張はシステムプロンプト＋ツール定義だけの話で、課題文込みでほぼ合う。codex は呼び出しごとの usage を出さないので
  固定費は直接測れていない（`turn.completed` の合計のみ）。
- 入力計の差（codex ÷ pi ≈ 16 倍、規約をそろえた pi+guide 比でも ≈ 5.7 倍）の大半はキャッシュ読みで、価格換算では
  **codex ÷ pi ≈ 3.3 倍、codex ÷ pi+guide ≈ 2.9 倍**。issue に引かれた「2〜3 倍のコスト効率」という**公開比較の主張は、
  この小課題では同程度以上で再現した**。規約の重さを除いても差が残るので、主因はハーネス側（ツール定義・スキル一覧・
  呼び出し回数）。
- codex はツール呼出が約 2 倍（テストを複数回走らせる・ファイルを分けて読む）。pi は全試行で 4〜5 回に収束し、ばらつきも小さい。
- 出力トークン: codex の `reasoning_output_tokens` は全試行で 0 と報告された。推論分が `output_tokens` に含まれるかは
  未確認なので、出力の比較は参考にとどめる。
- 課題が小さすぎて**正答率では差が付かない**。品質の比較はこの測定からは言えない。

## 4. Stage 1 のゲート（RPC から実測・gpt-5.5・thinking low）

| ゲート | 結果 | 観測 |
|---|---|---|
| **G1** prompt / steer / follow_up / abort と終端検出 | ◎ | `sleep 60` の bash 実行中に `abort` → ツールは `"Command aborted"`（isError）、assistant は `stopReason:"error"`、`agent_end` → `agent_settled` の**後に** `abort` の応答が来る（「アイドルになってから応答」の仕様どおり）。`sleep` の子プロセスは残らなかった。実行中の `steer` は次のモデル呼び出し前に user メッセージとして差し込まれ（「one / two」両方を返答）、`follow_up` は終わってから処理されて同じ run の中で答え（`agent_end` は 1 回）、最後に `agent_settled`。 |
| **G2** af MCP | ◎ | stdio の `workspace-agent mcp-stdio --self-report` を `--exposure direct` で登録 → モデルが `mcp__af_probe__list_memos` を呼び成功（isError=false）、件数を答えた。HTTP（CP の `/mcp`）経由は試していない。 |
| **G3** 使用量と文脈 | ◎ | `get_session_stats` は `tokens{input,output,cacheRead,cacheWrite,total}`・`cost`・`contextUsage{tokens,contextWindow,percent}`・メッセージ/ツール件数を返す。`contextWindow` は 272,000（gpt-5.5）。モデル未選択時は `contextUsage` が無い。 |
| **G4** 承認拡張 | ◎ | 全ツール呼び出しで `ctx.ui.confirm` を出す 8 行の TypeScript 拡張を `-e` で読ませた（ビルド不要）。RPC に `extension_ui_request{method:"confirm"}` が出て、`extension_ui_response{confirmed:false}` で bash がブロック（`"bash was not approved"`）、モデルが別コマンドを試し `confirmed:true` で実行された。 |

補足: G1 の abort 応答は `agent_settled` の後に来るので、AF のドライバは「abort の応答待ち」と「イベント購読」を
別々に持つ必要がある（応答を待ってからイベントを読むと詰まる）。

## 5. 測っていないこと

- **opencode**: ChatGPT にサインインしていない（`opencode models` は opencode の無料枠のみ）。同一モデル比較から外した。
- **実リポジトリ規模の課題**: 今回は 1 ファイル数十行。大きいリポジトリでの探索効率・正答率・コンパクションは未測。
- **codex の呼び出しごとの usage**: `codex exec --json` は合計しか出さないため、モデル呼び出し回数と固定費は未測。
- **ChatGPT プランの利用枠の減り方**: トークン比例かどうかはベンダー非公開。価格換算は参考値。
- **codex の素の状態**: af が配る規約・スキル一覧を外した codex は測っていない（外すには `CODEX_HOME` の付け替えが要り、
  資格情報ファイルに触れるため見送った）。pi+guide の対照で規約分は近似した。
- **TUI**（Terminal 実行方式での画面契約・状態検出）、**セッションの fork / 再開**、`/share` の無効化手段。
- 試行数は各 6 回で、統計的な差の検定はしていない。

## 6. 推奨

**有望。ただし ADR の前に Stage 0b を一回やる。**

- 採る理由（実測）: 同じモデル・同じ正答で **入力トークン 1/16、価格換算 1/3**。RPC が managed ドライバに要る
  操作（prompt / steer / follow_up / abort / 終端 / 使用量 / 承認往復）を**全部プロトコルとして持つ**。af MCP は stdio で
  トークンを書かずに繋がる。
- まだ決められない理由: 正答率で差が付かない小課題しか測っていない。省トークンが**品質の低下と引き換えでない**ことは、
  実リポジトリの課題（af 自身の小さな Issue 2〜3 件など）で確かめる必要がある。
- ADR で決めること（採用時）: 承認拡張を AF が同梱して常に `-e` で読ませるか、`PI_TELEMETRY=0` と `/share` の扱い、
  プロジェクト信頼の既定（RPC は黙ってスキップ）、`print-bearer-token` を含む資格情報の露出方針、ChatGPT ログインの
  接続カード（URL 貼り戻し or device flow）、バイナリの置き場所（nvm 配下は PATH に乗らないシェルがある）、
  opencode / lcpp との住み分け。
