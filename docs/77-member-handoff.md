# 77. 同一テナントの別メンバーへセッションを引き継ぐ

- 状態: ✅ **P0 実装済み**（差し出し / 撤回 / 受諾 / 辞退・push ゲート・受信箱）・
  ⏸ P1〜P3 は §77.14。⚠️ 実フリートでの 2 アカウント通しは未実施
- 設計判断: [decisions/0057](decisions/0057-member-handoff.md)
- 関連: [docs/59 セッション共有](59-session-sharing.md)（本機能が乗る土台・ACL と失効の規律）、
  [docs/58 セッション間メッセージ](58-cross-session-messaging.md)（**同一 Workspace 内**の別機能・境界は §77.2）、
  [docs/55 会話の途中から引き継ぐ](55-fork-at-message.md)、
  [docs/30 セッション報告](30-session-report.md)（通知 kind の前例＝`session-report`）、
  [docs/68 セッションが直したファイル](68-session-changed-files.md)（push ゲートの素）

## 77.1 何を作るのか

**A が B に共有しているセッション**について、A が Console から「この続きを B に引き継ぐ」を差し出し、
B が 1 クリックで受け取って、**B の Workspace に**新しいセッションを立てられるようにする。

セッションの実体は動かない。動くのは**文章と git の座標と、既にある会話の読み取り権**だけである。

## 77.2 「引き継ぐ」の 3 つの意味と、作るもの

「引き継ぐ」は日常語では 3 つを指す。混ぜると設計が壊れるので先に分ける。

| | 意味 | 扱い |
| --- | --- | --- |
| (A) | **仕事の引き継ぎ** — 「この続きを B にやってほしい」。B の Workspace で、B のエージェント・モデル・認証で新しいセッションが立つ | **本ドキュメントが作るもの** |
| (B) | **会話の引き渡し** — B が経緯を読める | 既にある（[docs/59](59-session-sharing.md) の共有）。本機能の**前提条件**にする |
| (C) | **セッションそのものの所有権移転** — 動いているセッションが A の Workspace から B の Workspace へ移る | **作らない**（理由は下記） |

(C) を捨てるのは実装の都合ではなく、**可搬でないものが多すぎる**からである。セッションの transcript・
worktree・未コミット変更・エージェント CLI の OAuth はすべて A の home ボリュームにあり、費用は
membership 単位で付き（[docs/67](67-member-cloud-cost.md)）、エージェント認証は A のアカウントのものである。
移せば「B の作業が A の課金と A のアカウントで走る」ことになる。

(C) を捨てた瞬間、**運べる荷物は 3 つしかない**と確定する —— ①文章（引き継ぎプロンプト）
②push 済みの git 状態 ③会話への読み取り権。§77.5 の push ゲートはここから出てくる。

**[docs/58](58-cross-session-messaging.md) との境界**: あちらは**同一 Workspace 内**（＝同一利用者）の
セッション同士へ平文 1 通を割り込ませるもので、宛先はセッション、届くと相手の 1 ターンが止まる。
こちらは**別メンバー**へ仕事を差し出すもので、宛先は人、届いても誰のターンも止まらない（受信箱に載るだけ）。
用語は **引き継ぎ / 共有 / メッセージ** の 3 語を混ぜずに使う。

## 77.3 既にある部品と、空いている穴

| 部品 | 境界 | 今できること |
| --- | --- | --- |
| `propose_session_handoff`（`workspace/agent/session_handoff_proposal.go`） | **自分の Workspace 内** | 次セッションの初回プロンプトを提案。起動は必ず利用者（`console/src/features/mirror/HandoffProposal.tsx:243` の起動ボタン） |
| `send_to_peer_session`（`workspace/agent/session_peer.go`） | 同一 Workspace 内 | 平文 1 通。封筒・intent・レート制限はサーバ側の砦 |
| セッション共有（[docs/59](59-session-sharing.md)） | **同一テナントの別メンバー** | 会話の RO 閲覧。RW は「共有先が提案 → 所有者が承認」 |
| 共有ビューの引き継ぎカード（`control-plane/session_share.go:639` / `console/src/features/sharing/SharedSessionView.tsx:541`） | 同上 | **B は A の引き継ぎ提案の本文を既に読める**（起動ボタンは無い＝RO） |

つまり穴は 2 つしかない。

1. **B 側で受け取って起動する導線**が無い。
2. **A の Workspace の生死に依存する**。`handoffProposals` は所有者 Workspace が停止していると
   `owner_workspace_stopped`(409) を返す（`session_share.go:651`）。⚠️ 引き継ぎは「A が作業を離れるとき」に
   起きる＝**A の Workspace が止まっている確率が最も高い場面**なので、A の Agent を毎回読みに行く今の形の
   ままでは、一番必要なときに何も出ない。

## 77.4 共有を前提にすることで消える設計問題

宛先を「**このセッションを既に見られる人**」に限ると、越境機能につきものの設計問題がまとめて消える。

| 素朴に作ると悩む点 | 共有前提にすると |
| --- | --- |
| 宛先の解決（テナント名簿をどう引くか、モデルに渡すか） | **消える**。候補は ACL の逆引きで、A は既に「この人に見せる」と判断済み |
| 会話を一緒に渡すか（RO 共有を自動で張るか） | **消える**。共有済みが前提条件そのもの |
| エージェントが他人の受信箱へ直接書ける危険（注入・permission laundering） | **消える**。送信は A の Console 操作（§77.6） |
| 引き継ぎ保留中に関係が切れたら | **既存の規律に乗る**。[docs/59](59-session-sharing.md) §2 の「ACL 変更が先なら提案を失効して本文を消す」をそのまま流用 |
| 越境実行の安全（他人の Workspace で何かを起こす） | **消える**。B が**自分の権限で自分の Workspace に**セッションを作るだけ＝既存の起動経路 |

最後が特に効く。**A → B へ「実行」が一切流れない。**流れるのは文章と座標だけで、起動は B が押す。
docs/59 の RW 提案が必要とした owner lease・冪等 ledger・二重実行防止（あちらは CP が所有者 Agent を
叩くため必須だった）は、この機能には**まるごと不要**である。

## 77.5 運べる荷物と push ゲート

共有していても、**B のディスクに A の未コミット変更は無い**。push されていない引き継ぎは、文章が
どれだけ立派でも嘘になる。

- **未 push の commit がある → 送信を止める**（赤）。B の新セッションは「前任の commit がある前提」で
  動き出すので、これは黙って劣化させてはいけない。
- **未コミット（dirty）→ 警告に留める**。意図的に捨てる引き継ぎがあるため。

⚠️ **判定するのは Agent で、モデルでも Console でもない。** `GET /sessions/{name}/handoff-context`
（`workspace/agent/session_handoff_context.go`）が git に聞き、`blocked` / `warning` という**結論**まで
組み立てて返す。CP はそれを送信時に評価し、Console は表示するだけ —— 条件を 3 層に分けて書くと必ずずれる。
repo の座標（remote URL / branch / HEAD sha）も同じ理由でエージェント入力から取らない: 座標を
**モデルが書ける構造化フィールド**にすると、Console がそれをクローン導線に変えた瞬間、「汚染された
リポジトリを読ませるだけで、B に別の remote をクローンさせる」道具になる。

⚠️ **`ahead > 0` だけを見るゲートは、一度も push していないブランチを素通しする。** upstream が無い
ブランチでは `git status --porcelain=v2 --branch` が `# branch.ab` 行そのものを出さないので ahead は 0 に
なる —— 引き継ぎで最も起きる形をちょうど見逃す。`no_upstream` と `detached_head` も止める側へ倒してある
（`TestHandoffContextBlocksBranchNeverPushed`）。

⚠️ **remote URL は資格情報を落としてから載せる**（`sanitizeRemoteURL`）。`https://x-access-token:…@host/…`
のまま offer に入れると、引き継ぎが**トークンの受け渡し**になる。

⚠️ `shared_session_catalog` が持っているのは `working_copy_id / worktree / branch` までで **remote URL は無い**
（`control-plane/store_share.go:335`）。B 側で作業コピーを同定するには remote URL が要るので、offer の
スナップショット（§77.11）に足してある。

## 77.6 MCP ツールは増やさない

当初の出発点は「メンバーへ引き継ぐ MCP ツール」だったが、共有前提に決めた結果、**新しいツールは要らない**。

- 引き継ぎ本文を書く仕事は `propose_session_handoff` が既にやっている。
- 宛先を決めるのは A（Console）。
- 起動するのは B（Console）。

追加するのは既存 `propose_session_handoff` の**任意 `to` ヒント**（「この続きは共有先の◯◯が適任」）だけに
留める。ツールを 1 本増やすと**全セッションの system prompt に常駐する**（[docs/58](58-cross-session-messaging.md)
§58.14 と同じコスト）うえ、エージェントの権能が増える。この形ならエージェントの権能は 1 ミリも増えない。

## 77.7 状態機械と、A のカードの三態

offer の状態は `pending / accepted / declined / withdrawn / expired`。

**A のローカル提案（`~/.config/agent-fleet/session-handoffs/*.json`）は送信後もそのまま残る。**
引き継ぎは提案の**コピーを差し出している**のであって、提案そのものが出ていくわけではない。だから
「引き継がれなかったら A が自分で起動する」は、**既存のミラーの起動ボタンが生き続けるだけ**の話になり、
新しい導線が要らない。

| offer の状態 | A の起動ボタン |
| --- | --- |
| `pending` | 押せるが確認を挟む。「B が受領待ちです。起動すると引き継ぎは取り下げられます」→ **起動と同時に自動撤回** |
| `declined` / `expired` / `withdrawn` | 注意なしで普通に起動。並べて「別の共有先へ投げ直す」 |
| `accepted` | 起動は可能だが二重作業の警告。禁止はしない（A の Workspace は A のもの） |

⚠️ **撤回は起動の「前」に、`pending → withdrawn` の条件付き更新として通す。** 「A が痺れを切らして
自分で始めた」と「B が受け取った」が競合するのが最悪の形（同じ仕事が 2 つ走る）で、この更新に負けた側
——つまり相手が先に受け取っていた側——は**起動をやめる**。撤回してから起動する順序でなければ、撤回に
失敗したことに気づいた時にはもうセッションが立っている。

**B 側の受諾は「1 クリックで受諾フローに入る」**という意味であって、押した瞬間に起動するのではない。
開くのは前埋めされた起動モーダル（本文は編集可、repo・worktree・エージェント・モデルは B の選択）。
**B の Workspace が起きて B に課金される**以上、この確認は省けない。編集された本文は **B の指示**になる。

## 77.8 本文の正は CP のスナップショット

**送信した時点で、その offer の本文の正は CP のスナップショットになる。**ローカル提案はミラー上の
カード位置（`CreatedAt` 不変ルール —— 2026-08-04 の実障害）と編集の起点として残るだけ。

- A のセッションが消えても（`removeHandoffProposals` はスロット再利用で消す）、台帳から本文を復元して
  起動できる。
- A の Workspace が停止中でも B は受け取れる（§77.3 の穴 2 がこれで塞がる）。
- ⚠️ **送信後にローカルを編集しても offer は変わらない。**変えたければ撤回して投げ直す。効かせようとすると
  **B が読んでいる最中に本文が書き換わる** —— docs/59 の RW 提案が本文を凍結しているのと同じ理由で避ける。
  この非対称は UI に明記する（黙って効かないのが一番悪い）。

## 77.9 通知は流れ物

通知は **CP 側から直接 `InsertNotification` する**。⚠️ 既存のセッション通知は `drainAgentOutbox`
（`control-plane/notification.go:77`）経由＝**Workspace が起動していないと出ない**が、引き継ぎは
A も B も Workspace が止まっている場面が主戦場なので、この経路では成立しない。

| 誰に | kind | いつ | target / 遷移先 |
| --- | --- | --- | --- |
| **B** | `handoff-offer` | A が送信 | `{type:"shared-session", id: catalogId}` → `openSharedSession()`（`console/src/features/sharing/open.ts`）で共有ビューを開く |
| **A** | `handoff-accepted` | B が起動を確定 | `{type:"session", id: 元セッション名}` → 元の会話 |

`openNotificationTarget`（`console/src/features/notifications/store.ts:79`）に、`session-report` /
`submodule-sync` と同じ作法で分岐を 1 本足す。文言は `wording.ts` に 2 kind 追加。

⚠️ **罠 2 件**:

- `InsertNotification` の冪等は **`ON CONFLICT(event_id)`**（`store_sqlite.go:2499`）で、**membership を含まない**。
  A と B の 2 通に同じ event_id を使うと片方が黙って消える。event_id は
  `offer id + イベント種別 + 受信者` で組む。
- `deliver()`（`store.ts:52`）は `n.target.id` をセッション名とみなして `sessionVoiceOpts` を引く。
  共有セッションは自分のセッションではないので、`usage-reset` と同様に **voice を渡さない分岐**が要る。

**辞退・失効の通知は出さない**（A が知りたいのは「引き継がれたかどうか」）。ただし宙に浮いた引き継ぎを
A が忘れる問題は残るので、**失効の直前に A へ 1 回だけ**通知する。理由は求めない。

## 77.10 バッジ（在庫）と台帳（履歴）

通知を流れ物と決めた以上、**流れた後に辿れる場所**が対になる。三者の役割を混ぜない。

| | 役割 | 消える条件 |
| --- | --- | --- |
| **通知** | 気づかせるだけ | 既読で流れる。取りこぼしを厳密に救済しない |
| **バッジ** | **未処理の在庫** | offer が pending でなくなったとき。⚠️ **既読では消さない**（「読んだが決めていない」で消すと引き継ぎが忘れられる） |
| **台帳** | A 側の履歴 | 保持期間まで残る |

**B 側のバッジ**

- `SharedSessionsSection` のヘッダ。⚠️ 既存の `mail` アイコン（`SharedSessionsSection.tsx:59`）は
  「**自分が出した RW 提案の承認待ち**」＝**方向が逆**なので、同じバッジに合流させない。別アイコンで並べる。
- セッション行（`SharedProjectNode.tsx:21` の `SharedSessionRow`）にも未処理バッジ。

**A 側**

- 元セッションの行とミラーの引き継ぎカードに「◯◯へ引き継ぎ済み（受領待ち / 受領済み）」。
  通知より**行のチップ**の方が効く —— 本来の目的は A が同じ仕事を続けてしまう二重作業の防止だから。
- **台帳**は「自分が出した引き継ぎ」一覧。置き場所は `ShareListModal` / `useMySharesStore`
  （`console/src/features/sharing/store.ts:68`）の隣。共有と引き継ぎは親子関係なので同じ面に置ける。
  ここに状態と、辞退されたものからの起動・投げ直しが集まる。

**データの供給**: `api/shared-sessions` の DTO（`SharedSession` インターフェース）に
`handoffOffer: {id, status}` を 1 つ足すだけでよい。これは **CP の DB スナップショットを読むだけ**の
経路なので、追加リクエストゼロ、かつ A の Workspace 停止中でも出る（所有者往復の `?refresh=1` は不要）。

⚠️ 共有解除・アーカイブ・セッション削除で offer が失効すると、**配達済みの通知はクリックしても行き先が無い**。
`openNotificationTarget` が false を返す形になるので、「もう見られません」と示して既読化する。

## 77.11 データモデル

```
session_handoff_offer
  id                       -- ho_<hex>
  catalog_id               -- FK shared_session_catalog（ACL 連動の要）
  owner_membership_id      -- A
  recipient_membership_id  -- B（catalog に対する現在の共有先であること）
  title
  ciphertext, key_ref      -- 引き継ぎ本文（§77.11 暗号化）
  repo_remote, branch, head_sha
  source_session_name, source_session_kind
  status                   -- pending | accepted | declined | withdrawn | expired
  created_at, expires_at, decided_at
  accepted_session_name    -- B が立てたセッション名（accepted のときだけ）
```

- ⚠️ **`session_share_proposal` に相乗りさせない。**あちらは B → A（共有先が提案し所有者が承認）で、
  こちらは A → B（所有者が差し出し共有先が承認）＝**方向が逆**。`owner` / `proposer` の意味が
  入れ替わるので、同じテーブルに載せると必ず読み間違える。失効・ACL 連動の**作法だけ**を流用する。
- `catalog_id` に紐付けることで **ACL 連動が自動**になる（共有解除・アーカイブ・セッション削除の失効は
  docs/59 の既存経路に相乗り。`membershipCascade` も同様 —— `store_sqlite.go:1152` の並びに 1 行足す）。
- **本文は RW 提案と同格に扱う**（他人の作業内容）。tenant key custodian がある環境では暗号化して置き、
  辞退・失効・撤回時に消去する。

## 77.12 1 セッションにつき未処理 1 件・宛先 1 人

共有スコープが `repo` / `worktree` だと、1 セッションの閲覧者が複数いることは普通にある。
複数人へ同時に投げられると**早い者勝ち＋二重作業**になるので、**未処理の offer は 1 セッションに 1 件、
宛先は 1 人**に絞る。渡し直したいときは撤回してから投げ直す（§77.7 の三態にその導線がある）。

## 77.13 やらないこと

- セッション実体の移動（§77.2 の (C)）。
- **共有していない相手への引き継ぎ。**「共有してから引き継ぐ」の一手を意識的に残す —— 1 操作にまとめると
  「引き継ぎのついでに会話を全部開く」ことになる。
- 他メンバーの**動いているセッションへ直接メッセージを送る**経路（docs/58 の越境版）。他人のターンを
  止める行為なので、やるとしても受信箱を経由する別設計になる。
- B のセッション一覧の閲覧、B の在席の可視化、B の Workspace の自動起動（docs/59 §3 と同じ理由）。
- エージェントによる直接送信（§77.6）。

## 77.14 段階

- ✅ **P0**: `session_handoff_offer` テーブル（両方言）＋ CP API（差し出す / 撤回 / 受諾 / 辞退 /
  宛先候補）、Agent の `GET /sessions/{name}/handoff-context`（push ゲートの素）、A の送信 UI
  （共有先ピッカー＋ゲート表示）、B の受信箱と受諾（前埋め起動モーダル）、A のカードの三態。
  通知 3 kind とバッジも P0 に前倒しで入れた —— 受信箱への入口が無いと B は届いたことに
  気づけず、P0 だけでは機能として閉じないため。
- ⏸ **P1**: 台帳（A の「出した引き継ぎ」一覧・`ShareListModal` の隣）。今は A のカードの
  状態行が代わりを務めているが、セッションが消えた引き継ぎは辿れない。
- ⏸ **P2**: `api/shared-sessions` の DTO に `handoffOffer` を載せる（今は受信箱の
  `sessionId` と突き合わせて行バッジを出しているので追加リクエストは無いが、決着済みの
  状態は受信側に出ない）。
- ⏸ **P3**: 失効直前通知の実運用調整、GC、`propose_session_handoff` の任意 `to` ヒント。

## 77.15 採らなかった案

- **新しい MCP ツール（`propose_member_handoff`）を配る** —— 共有前提にした時点で、宛先も送信も
  Console 側に落ちた。ツールを増やすと全セッションの system prompt を食い、エージェントの権能が増える。
- **テナント名簿ツール（`list_tenant_members`）** —— 名簿がモデルの文脈と transcript に残る。共有前提なら
  そもそも要らない。
- **エージェントからの直接送信（設定で ON）** —— A の名前で他人の受信箱に文章が置かれる。汚染された
  リポジトリを読んだセッションがそれをできる形は、docs/58 が封筒で名指しした permission laundering の
  相手が人間になったものに等しい。
- **offer 側にローカル編集を追従させる** —— B が読んでいる最中に本文が変わる（§77.8）。
- **A のローカル提案を送信時に消す（移譲モデル）** —— 辞退されたときに手元に何も残らず、「引き継がれ
  なかったら A が自分で起動する」が別導線になってしまう。

## 77.16 テスト計画

- **ACL 連動**: 共有解除 / RO 降格 / アーカイブ / セッション削除のそれぞれで、pending の offer が失効し
  本文が消えること。逆順（受諾が先）でも整合すること。
- **競合**: A の自分起動（自動撤回）と B の受諾が同時に来たとき、成立するのは片方だけであること。
- **1 件 1 宛先**: 2 件目の差し出しが断られること。
- **push ゲート**: 未 push の commit で送信が止まること、dirty は警告で通ること。
- **通知**: A / B の 2 通が `ON CONFLICT(event_id)` で潰し合わないこと。B の Workspace が未作成 /
  停止中でも `handoff-offer` が届くこと。
- **バッジ**: 既読にしても pending のバッジが消えないこと。失効でバッジが消えること。
- **本文の凍結**: 送信後にローカル提案を編集しても offer の本文が変わらないこと。
