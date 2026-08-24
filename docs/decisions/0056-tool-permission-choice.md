# 0056. 権限確認のスキップは利用者が選ぶ。既定は変えない

- 状態: **採用**（2026-08-24）。検討・実測の記録は [docs/76](../76-tool-permission-choice.md)。
- 関連: [0055-idle-stop-and-carried-interactions.md](0055-idle-stop-and-carried-interactions.md)（承認待ちで畳まれたときの受け皿） /
  [dev/07 セキュリティ](../dev/07-security.md) §脅威モデル（コンテナ境界が唯一の砦、という前提そのもの）

## 背景

fleet はどの kind も「ツール承認を全部スキップする」フラグで起動していた（claude
`--dangerously-skip-permissions`、cursor `--force`、copilot `--allow-all`、kiro `--trust-all-tools`、
agy 同、codex の bypass 2 種、opencode `--auto`）。根拠は「コンテナがサンドボックス」と
「承認で止まっても Console から答えられない」の 2 つ。後者は各 kind の承認導線（status hook の
`permission` 状態、ACP の `session/request_permission` → Interaction）が入った時点で失効している。

## 決定 1 — 既定は現状のまま（スキップする）

オフにできるようにするのが目的で、既定を変えるのは目的ではない。**欠落・未設定・壊れた値は
すべて「スキップする」に倒す**（Go の `SkipPermissions` と Console の `skipPermissions !== false`）。
逆に倒すと、この機能より前に書かれた prefs を読んだ端末で全セッションが承認待ちになる —— 一番
気づかれにくい壊し方になる。

## 決定 2 — 値は 3 層。セッションの明示指定 > kind 毎の既定 > true

kind 毎の既定だけだと「普段は承認あり、この 1 本だけ自動で走らせたい」が表現できず、セッション
毎だけだと毎回選ばせることになる。model / effort / startMode と同じ形に揃えた。
`Meta.SkipPermissions` を `*bool`（3 値）にするのが要で、`false` と「未指定」を潰すと
**既定を後から変えても既存の指定が勝ち続ける**。

## 決定 3 — 解決は Agent のプロセス内で行う（Console だけで解かない）

Console は起動リクエストに値を載せられるが、**Console を通らない起動経路**がある: MCP の
`create_session`、定時実行、停止セッションの再起動、fork / recreate。Agent が ui-prefs を直接読む
（`readUIPrefs` — hiddenModels / opencodeCatalog と同じ前例）ことで、どの経路でも同じ既定が効く。

**捨てた案**: Console が常に明示値を送る。設定を変えても、変更前に起動したセッションが古い値を
焼き込んだまま再起動し続ける。実際 Console は**起動ダイアログで触られたときだけ**送る。

## 決定 4 — plan 起動は kind を問わずスキップしない

以前からの挙動を `BypassPermissions`（= `mode != "plan" && SkipPermissions`）1 か所に畳んだ。
各 kind に散っていた `mode == "plan"` のフラグ削り（`ReplaceAll`）は、増えるたびに書き写す形で、
実際に kind ごとに微妙に違っていた。

## 決定 5 — 選べるのは「承認待ちを Console から答えられる kind」だけ

`Caps.PermissionChoice`（Console 側 `caps.permissionChoice`）。フラグを外すのはどの kind でも
できるが、答えようのない承認ダイアログで止まったセッションは、利用者から見れば黙って固まった
のと同じ。**未検証の caps を立てない**（1854d の教訓）を踏襲し、claude / cursor / copilot / kiro /
agy から始める。codex と opencode は導線を作ってから（[docs/76](../76-tool-permission-choice.md) §76.4）。

## 決定 6 — 非対応 kind の「承認あり」は黙って無視せず断る

`POST /sessions` は `permission_choice_unsupported` で 400 を返す。無視すると、頼んだ側は承認ありで
走っていると思い込む。ui-prefs 側は逆に**黙って無視する**（古い / 壊れた設定が原因で起動そのものが
できなくなる方が悪い）——ここは非対称でよい。

## 影響と残り

- 既定 ON のままなので、何も設定しなければ挙動は変わらない。
- ⚠️ **claude 以外を TUI で起動して承認ありにすると、承認待ちのまま `interaction_idle_timeout` を
  超えたときに保留中の対話が失われる**。ADR 0055 の持ち越しは claude の `pending-*` と managed の
  Interaction を覆う（P5）が、claude 以外の TUI はどちらにも入らない。cursor / copilot / kiro は
  Console の既定が managed なので通常の経路は覆われており、残るのは CLI(TUI) を明示した場合と agy。
- 承認ありのセッションは無人運転（定時実行 / オペレーター / MCP drive）では完了しない。設定 UI に
  注記を出している。
- 残り: codex / opencode の導線、一覧・ミラーでの「承認あり」表示、実 TUI での 1 周確認。
