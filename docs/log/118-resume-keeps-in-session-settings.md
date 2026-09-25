# 118. 再開でターミナル内のモデル・effort・モード切替を失わない（#987）

- 依頼: #987。TUI（ターミナル）で起動したセッションを止めて再開すると、`/model` などで切り替えた
  設定が起動時の設定に戻る。原因は全 kind 共通で、再開コマンドを `meta.Model` / `meta.Effort` /
  `meta.Mode` から新規起動と同じ形で組み立てており、その起動フラグが会話側の状態に勝つこと。
  Managed は `HandleSessionSettings` が meta に書き戻すので対象外。
- 関連: [117](117-managed-af-session-name-delivery.md)（同じ日の実測の手順）/ [40](40-cursor-agent-kind.md)
  決定 3（cursor の非公開ストアを読まない方針）。
- 版: claude 2.1.282 / codex-cli 0.157.0 / opencode 1.18.32 / GitHub Copilot CLI 1.0.88 /
  cursor-agent 2026.09.23-86fc751 / kiro-cli 2.16.0 / agy 1.2.11。

## 測り方

kind ごとに使い捨てディレクトリで実 TUI を専用 tmux ソケットに立て、AF と同じ起動・再開コマンドで
「切替→応答→終了→再開（AF の形／フラグ無し）」「切替→応答なしで終了→再開」「モードの往復」を
回した。判定は画面下の状態行・バナー（モデルの自己申告は証拠にしない）と、CLI のストアの実物。

資格情報を別ディレクトリに持ち込むと、リフレッシュトークンが回転したときに本物が失効する
（claude / codex）。そのため**実環境の設定で測り、資格情報以外の設定ファイルを事前に控え、
プローブが変えたキーだけを戻した**。

🔥 **プローブの子に `AF_*` を渡さない。** claude のプローブは親ペインの `AF_SESSION_NAME` を
継いだため、フック（`claude/sid.go` の `NormalizeHookSID`）がプローブの plan 承認待ちを
**親セッションのもの**として記録し、利用者の画面に「停止時に承認待ちだった計画」が出た。
`env -u AF_SESSION_NAME …` で起動すること。

## 実測結果

| kind | 再開時の AF フラグ | 切替の記録場所（応答なしでも残るか） | CLI 自身の復元（フラグ無し） |
|---|---|---|---|
| claude | model / effort / plan が勝つ | jsonl の `/model`・`/effort` コマンド行＋`<local-command-stdout>`（残る）。`{"type":"permission-mode"}` は終了時にも書かれる | model は最後の応答の `message.model`、effort は全体既定、plan は**戻らない** |
| codex | `-m` / effort が勝つ（mode は渡さない） | rollout の `event_msg` `thread_settings_applied`（残る）＋`turn_context` | model / effort / plan とも戻る |
| opencode | `--agent` が勝つ。**`--model` は無視される** | 最後の user 行の `model`・`agent`（応答が要る） | model / variant / agent とも戻る |
| copilot | `--mode plan` / `--model` / `--effort` が勝つ | events.jsonl の `session.model_change`（プロンプト送信まで保留・終了で消える）、`session.mode_changed`（残る） | 戻る |
| cursor | `--model` / `--plan` が勝つ | chat の store.db `meta` 行 `lastUsedModel`（プロンプトが要る）・`mode`（残る） | 戻る |
| kiro | `--model` が勝つ（`--agent` は勝たない） | `<sid>.json` の `session_state.rts_model_state.model_info.model_id`・`agent_name`（残る） | 戻る |
| agy | `--model` / `--mode plan` が勝つ | 次の USER_INPUT の `<USER_SETTINGS_CHANGE>`（表示名）と `<USER_REQUEST>` の `/plan ` 接頭辞（応答が要る） | **全体 settings.json の model**（会話の値ではない）、plan は戻らない |

### 全体既定への漏れ（CLI 側の挙動・今回は直さない）

- claude: `/model` は `settings.json` の `model` を、`/effort` は `modelSettings.<model>.effortLevel` を書く
  （`s`＝このセッションだけ、を選べば書かない）。
- codex: `/model` のピッカーで Enter は `config.toml` の `model` / `model_reasoning_effort` を書く（`s` は書かない）。
- agy: `/model` は即座に `~/.gemini/antigravity-cli/settings.json` の `model` を書く。
- cursor: `/model` も AF の `--model` も、フラグ無しの再開（chat の `lastUsedModel` を書き戻す）も
  `cli-config.json` の既定を動かす。
- opencode: `~/.local/state/opencode/model.json` の `recent` / `variant` だけ（既定は変わらない）。
- copilot / kiro: 書かない。

## 直し方

`agents.SettingsRecaller`（任意の口）を足し、TUI のスロットを再開する唯一の経路
`ensureSessionTmux` で、会話が最後に使った設定を meta に畳み込んでから起動コマンドを組む。
変わったときは meta を読み直して書き戻す（同時に来た別の meta 書き込みを潰さないため）。
fork / recreate は meta を引き継ぐので、切替後の設定がそのまま子に渡る。記録が無ければ今までどおり。

| kind | 読むもの | 補足 |
|---|---|---|
| claude | 成功した `/model`・`/effort` と最後の `permission-mode` | `message.model` は使わない（自動フォールバックも出る場所で、応答なしの切替は載らない）。ピッカー形は表示名しか残らないので、ティア別名に戻せるものだけ採る |
| codex | `turn_context` と `thread_settings_applied` の新しい方 | effort が null＝モデル既定なのでフラグを外す |
| opencode | 最後の user 行の model と `mode()` | model は CLI が無視するが meta と fork のために採る |
| copilot | `session.start` / `session.resume` / `session.model_change` / `session.mode_changed` | 使えないモデルを指定すると Auto に落ちる。その `auto` を採れば、Auto に effort を渡し続けて 400 になる事故（`reasoning_effort "low" was provided`）も消える |
| cursor | store.db の `lastUsedModel` / `mode` | 決定 3 の唯一の例外。再開時に 1 回だけ読み、失敗は meta のまま。`lastUsedModel` は引数なしの基底 id なので、meta の id が同じ基底なら据え置き、カタログにそのまま載る id なら採用、どちらでもなければ `--model` を外して CLI に任せる |
| kiro | `<sid>.json` | ミラーの plan 表示とモデル札も同じファイルから引くようにした（meta は起動時の値しか持たない） |
| agy | `<USER_SETTINGS_CHANGE>` の新モデル（表示名→カタログ id）と `/plan ` 接頭辞 | 応答なしの切替は会話に残らない |

モードは claude / copilot / cursor / agy / opencode ではフラグが勝つので必要。codex と kiro は CLI が自分で
戻すが、meta（kiro は `--trust-all-tools` とミラーの plan 表示を決める）を合わせるために読む。

## 残したもの

- 応答なしの切替は opencode / copilot / cursor / agy では CLI 自身が記録しないので救えない。
- 全体既定への漏れは各 CLI の仕様。AF が起動時に明示モデルを渡すセッションには効かない。
- copilot は Free プランのため名前付きモデルの切替を実測できていない（Auto の tier で代用）。
  `session.model_change` の形は起動時の記録から推定。
