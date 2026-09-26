# 119. Managed opencode の呼び出し元セッションをプラグインで届ける（#989）

- 依頼: #989（#978 の案 b を切り出したもの）。Managed opencode は af MCP 子に「どのセッションか」を
  伝える経路が無く、セッションに結び付く af ツールは cwd と生死の推定に落ちる。同じ worktree で
  2 本が動いていると曖昧として断る。
- 関連: [117](117-managed-af-session-name-delivery.md)（他 kind の経路と、opencode に設定の口が
  無いことの再測定）/ [27](27-agent-managed-driver.md) §9.3.1。
- 版: opencode 1.18.32。

## 測り方（課金なし）

偽の OpenAI 互換エンドポイント（決まった tool_call を返すだけ）を custom provider として登録し、
本物の `opencode serve` に Python の stdio MCP（受け取った `tools/call` の引数と cwd を記録する）を
2 つ繋いだ。1 つは af と同じ形の名前（`af_<8 桁 16 進>`）、もう 1 つは無関係な名前にした。
同じディレクトリに 2 セッションを作り、1 ターンずつ回した。

## 実測結果

1. **`tool.execute.before` は MCP ツールでも発火**し、`input.sessionID` は opencode のセッション ID。
   `output.args` に足した鍵は、MCP 子の `tools/call` の `arguments` にそのまま届いた。
   同じ子を共有する 2 セッションは、それぞれ自分の ID を届けた。
2. **スキーマ検査で弾かれない。** af のツールは `additionalProperties: false` だが、検査はモデル
   出力に対して先に済んでおり、フックはその後に動く。モデル自身が同名の鍵を書いた場合も弾かれず、
   プラグインがあれば上書きされ、**無ければ書いた値がそのまま届く**。
3. 🔥 **足した鍵は opencode のツール記録（`state.input`）に残り、次のターンでモデルに返る**
   （assistant の `tool_calls.arguments`）。`tool.execute.after` で消しても記録は書き込み済みで
   手遅れだった。AF のミラーはツール入力から決まった鍵（command / path など）しか表示しないので、
   画面には出ない。モデルが鍵を覚えて自分で書いても、2 のとおり上書きされる。
4. MCP 子の cwd はセッションのディレクトリ（既存の cwd 推定が前提にしていたことの確認）。

## 実装

- `workspace/opencode-plugin/agent-fleet-caller.js`: ツール名が `^af_[0-9a-f]{8}_` のときだけ
  `args._af_caller_sid = input.sessionID` を**上書き**する。他社の MCP には触らない。旧名の素の
  `af` は `af_` だけでは他のツールと区別できないので対象外（cwd 推定のまま）。entrypoint が起動
  ごとに `*.js` をコピーするので、配布と更新は `agent-fleet-status.js` と同じ経路に乗る。
- `mcpx.mcpStdioCall`: 引数から `_af_caller_sid` を取り除いてから処理する（メモ系は引数を CP へ
  そのまま中継するため、先に除く必要がある）。値は 1 回の呼び出しの間だけ持つ（処理は直列）。
- `mcpx.mcpOwningSession` の順:
  1. `AF_SESSION_NAME`（プロセス単位の事実。呼び出しごとの申告では上書きしない）
  2. `mcpCallerSession`: 作業フォルダ（`Dir` か `CWD()`）が一致し、アーカイブされていない
     opencode セッションのうち、AF の sid ストア（`opencode.SlotSessionID`、Managed ドライバが
     書く）の値が申告と一致するものが**ちょうど 1 つ**あり、しかも Agent が生きていると答えたとき
     だけ採る
  3. 今までの cwd と生死の推定
- 🔥 **tools/list には申告が乗らない。** `generate_image` は一覧を作る時点で持ち主を解決し、
  解決できなければ一覧に出さない（`mcpImageGenAdvertise`）。呼び出し側だけ直しても、同じ worktree
  に 2 本いると道具そのものが見えず、受け入れ条件を満たせない。そこで、フォルダの生きている
  セッションが**全部 Managed opencode**（＝どの呼び出しも申告を運べる）のときに限り、スタジオでない
  1 本を代表にして一覧に出し、誰のものかは呼び出しの申告で決める（`mcpStampedFolderSessions`）。
  スタジオの道具が「持ち主が見分けられないなら出して、呼び出しで理由付きで断る」（ADR 0100）のと
  同じ形。共有された子の一覧はもともと全セッションで 1 つなので、1 本ごとに変えることはできない。
  他 kind が混ざる・生死が読めない・全員スタジオ、のときは今までどおり出さない。
- 信頼: 申告はモデルでも書ける（プラグインは利用者が消せる）ので、セッション名としては受け取らず、
  AF の対応表を引く鍵としてだけ使う。一致しなければ黙って 3 に落ちる。つまりプラグインが無い・
  壊れている状態は、今までと同じ結果になる。

## 検証

- 単体（`mcp_stdio_caller_test.go`）: 同じフォルダの 2 本がそれぞれ自分に解決される／偽造・停止中・
  別フォルダ・opencode 以外の申告は採らない／一致しない申告は cwd 推定の答えを保つ／subdir／
  `AF_SESSION_NAME` が勝つ／鍵が Agent へ渡らず次の呼び出しに残らない／同じフォルダの 2 本に
  `generate_image` が一覧に出て、生成はそれぞれ申告した側に付く（申告なしは断る）／他 kind 混在・
  生死不明・全員スタジオでは一覧に出ない。変異 11 本のうち 10 本はどれかのテストが落とした。
  残る 1 本（一覧の代表選びで生死の読み取り失敗を無視する）は、`mcpAliveSessions` が失敗時に空を
  返すので挙動が変わらない等価変異。
- 契約（`contract_caller_plugin_test.go`、`-tags clicontract`、tier A）: 上の 1・2・4 を出荷する
  プラグインそのもので固定した。陰性対照として、全ツールに付けるプラグインと何もしないプラグインの
  どちらでも赤になることを確かめた。
- 残り: 配備後に実 Managed opencode 2 本を同じ worktree で動かし、`propose_session_handoff` /
  `generate_image` がそれぞれ自分に解決されること（#989 の受け入れ条件）。

## レビュー（codex の子セッション）で直したこと

- 🔴 **一覧の監視 goroutine が呼び出しの申告を読んでいた。** `mcpWatchToolList` は 1 分ごとに
  `mcpStdioToolList` を別 goroutine で組み直し、その中の `mcpImageGenAdvertise` /
  `mcpStudioAdvertise` が `mcpOwningSession` → `mcpCallerSID` を読む。`generate_image` の呼び出しは
  最長 33 分ループを占有するので、その間の監視は**データ競合**になり、しかも共有された子の一覧を
  「たまたま呼んでいた 1 本」の持ち主で組み直す。持ち主の解決を呼び出し用（申告を使う）と一覧用
  （`mcpListOwningSession`、申告を見ない）に分けた。`-race` で、一覧側を申告に戻す変異が
  `mcpCallerSession` の競合として検出されることを確かめた（CI は `-race` を使わないので、CI での
  見張りは意味を見るテストの方）。
- 🔴 **プラグインが印を付けない状況で、モデルの書いた値を信じていた。** プラグインが消された・
  af が旧名の `af`（プラグインは乱数付きの名前しか見ない）のとき、モデルが同じフォルダの生きている
  別セッションの `ses_` を書けばそのセッションとして扱われた。申告は「プラグインが入っている かつ
  af の名前が乱数付き」のときだけ使う（`mcpCallerStampTrusted`）。モデルは同じ uid でシェルを持つ
  ので、`AF_SESSION_NAME` を偽って `mcp-stdio` を自分で起動することもでき、これは壁ではない。
  予約引数を「普通のツール呼び出しで開く扉」にしない、というところまで。
- 🟡 **一覧に出したのに毎回断られる形があった。** 一覧側も同じ信頼条件と、生きている全員に sid
  ストアの対応があることを求める。
- 変異 6 本（一覧が申告を読む・常に信頼・対応の確認なし・名前の確認なし・プラグインの確認なし・
  呼び出し側が信頼条件を見ない）はすべてどれかのテストが落とした。

## 範囲外

- opencode の task ツールで作られる子セッションは `ses_` が別なので一致せず、cwd 推定に落ちる
  （悪化はしない）。親をたどるなら opencode のストアの `parent_id` が使える。
- Terminal の opencode は `AF_SESSION_NAME` が先に決まるので、プラグインの申告は除かれるだけ。
