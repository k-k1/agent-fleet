# 0133. 外部の状態を見張るエンジンを 1 つにする：申し込み・ソースアダプタ・push と poll を 1 つのインターフェースの裏に

[English](0133-external-watch-engine.md) | 日本語

- Status: **proposed**（2026-11-08）。利用者がこの抽象化を求めた。まだ何も作っていない。
- Related: [0021](0021-scheduled-execution.ja.md)（定時実行）/ [0061](0061-work-item-inbox.ja.md)（作業項目の受け箱）

## Context

セッションが Workspace の外のものを待つ場面が増えている。PR の CI が終わる、PR がコンフリクトする、マージされる、
チケットが動く、デプロイが落ち着く、など。今はそれぞれを次の 2 つのどちらかで見張っていて、どちらも良くない。

**1. エージェントが手で聞きに行く。** CI が終わるのを知りたいエージェントは、ワンライナーをバックグラウンドで
走らせ（`gh pr checks <N> --watch --interval 60`）、シェルが終わるのを待つ。2026-11-08 の 1 回の Issue ラウンド
（PR #3589・#3590・#3595・#3599・#3600・#3605・#3606）で見たこと：親セッションは PR ごとにこの見張りを走らせ、
赤くなった実行は手で原因を調べて張り直し、コンフリクトは親が自分で `git merge-tree` を回したから見つかった。
見張りはシェルと一緒に死に、止まったセッションを起こせず、1 つのプロバイダしか知らず、同じ PR を気にする
セッションがそれぞれ聞きに行き直す。

**2. Agent の機能ごとに自分の見張りを持つ。** Workspace の Agent にはもういくつもあり、それぞれが自分の
スケジュール・重複除け・届け方を持つ：

| 機能 | 見張るもの | やり方 |
|---|---|---|
| `internal/branchpr` | GitHub（GraphQL、まとめ取り）と Bitbucket の PR の状態 | 求められたときに埋まるキャッシュで、自分のループは持たない（セッション一覧が読まれたとき、または #3129 から）。GitHub：開いた PR は 2 分（`openTTL`）、落ち着いたものは 10 分、待ちは 5 / 15 分。Bitbucket：開いた PR は 5 分、コンフリクトの確認は 15 分、403 のあとは 10 分待つ |
| PR フィードバック監視（#3129、`sessionx/pr_feedback.go`） | セッション自身の PR の CI 失敗・変更要求・未解決のレビュースレッドの増加（レビューの通知は PR ごとに 3 回まで）。セッションが持つ PR リンクのマージ・クローズ | `branchpr` のキャッシュを読む。PR を持つセッションに通知を入れ、そのセッションがアーカイブ済みかプロンプトを受けられない種類なら利用者に伝える。既定はオフ（`prFeedback` のスイッチ） |
| PR 通知（#3131、`sessionx/session_pr_notices.go`） | マージ可能（`pr-ready`）・マージ済み・クローズ | 同じキャッシュが記録されるたびに読む。利用者向けの通知箱に書く。スイッチはない |
| stalled-tree 監視（#2397、`sessionx/stalled_tree.go`） | 親と子が全員 idle | 自分のループ。通知を入れる |
| 作業項目の受け箱（ADR 0061、`internal/workitems`） | 利用者向けの GitHub / Bitbucket / Jira の項目 | スケジュール・保存した問い合わせ・キャッシュは Control Plane が持つ。Agent は利用者のトークンで問い合わせを解くだけ |

どれもセッションから頼めない（「#3606 のチェックが終わったら教えて」）。どれも成功やコンフリクトをセッションに
伝えない（#3129 が伝えるのは失敗だけで、コンフリクトは利用者に伝える）。PR を持たない親からは届かない。
これまで、新しいプロバイダや条件のたびに、ループ・保存先・一度きりの印がもう 1 組増えてきた。

見張るものはこれからも増える。CI と PR のほかに利用者はデプロイを挙げた。ほかの候補は §9 に挙げる。

## Decision

プロバイダに依らない安定したインターフェースを持つ**見張りのエンジンを 1 つ**作り、プロバイダはすべてアダプタの
裏に置く。セッションは 1 つの MCP ツールで申し込み、エンジンがスケジュール・重複除け・届けを受け持ち、
アダプタは 1 種類の外部の状態を読む（または受け取る）方法だけを知る。

### 1. 層

```
 session / member  <-- Sink (notice into a session, notification centre, forward to a parent)
        ^
   Watch engine (shared): subscriptions, authorisation, scheduling, rate limits and backoff,
                          de-duplication, expiry, persistence across restarts
        ^  normalised Event
   Source adapters (per provider): github, bitbucket, jira, aws-cfn, http, ...
        ^                   ^
   Poll (Agent side)  <--  poke (subject only) <-- Webhook (Control Plane side)
```

### 2. インターフェース（形であって、最終的な名前ではない）

```go
// One per provider. Capabilities says what it can do; the engine combines them.
type Source interface {
    Kind() string                // "github", "jira", "aws-cfn", ...
    Capabilities() Caps          // Poll, Webhook, conditional fetch (ETag), batching
    // Resolve normalises a reference ("k-k1/agent-fleet#3606") and checks that the subscriber's
    // connection may read it. It is the only place an adapter decides access.
    Resolve(ctx context.Context, who Principal, ref string) (Subject, error)
}

type Poller interface {
    // cursors carry the last ETag / updated_at per subject; an unchanged subject returns no event.
    // Hint tells the engine when this subject is worth asking about again.
    Poll(ctx context.Context, subs []Subject, cursors map[SubjectID]Cursor) ([]Event, map[SubjectID]Cursor, map[SubjectID]Hint, error)
}

type WebhookParser interface { // Control Plane side
    Verify(r *http.Request, secret []byte) error
    Parse(r *http.Request) ([]SubjectID, error) // a poke, never an Event: §4
}

type Event struct {
    Source  string          // "github"
    Subject SubjectID       // "github:k-k1/agent-fleet#3606"
    Type    string          // shared vocabulary, §3: "pr.checks.completed"
    Version string          // de-duplication key: a fingerprint of the state, §5
    Summary string          // one line composed from fields the adapter controls, rule 6.5
    Data    json.RawMessage // provider detail (failed jobs, URLs)
    At      time.Time
}

type Subscription struct {
    Owner   SessionRef      // owning session: who asked, or the parent when Notify is parent (§5)
    Subject SubjectID
    Types   []string        // patterns: "pr.checks.*"
    Until   UntilRule       // first match | n deliveries | subject closed | deadline
    Notify  SinkRef         // self | parent | member
    Fallback SinkRef        // built-in subscriptions only: where to deliver when the session cannot take it (§5)
}
```

プロバイダを知るのはアダプタ、時間を知るのはエンジン。聞きに行く間隔、エラーや rate limit のあとの待ち、
push と poll のどちらを使うかはエンジンが決め、アダプタの `Hint` を手がかりにする（例「チェック実行中：15 秒後に
また聞く価値あり」「落ち着いた：10 分」「T まで rate limit」）。

### 3. 共通のイベントの語彙

イベントの種類は名前空間つきで、プロバイダをまたいで共通にする。だからセッションは、PR が GitHub でも Bitbucket でも
`pr.checks.completed` を申し込む。最初の組：`pr.checks.completed`（全体の結果と落ちたチェックつき）、
`pr.checks.failed`、`pr.conflict`、`pr.mergeable`、`pr.ready`（開いていて、下書きでなく、チェックが緑で、
マージでき、承認済みかレビューが要らない）、`pr.merged`、`pr.closed`、`pr.review.changes_requested`、
`pr.review.threads_unresolved`、`issue.transitioned`、`deploy.completed`、`deploy.failed`。Bitbucket では、そのソースがマージの可否・承認・
コメントのスレッドを読むようになるまで（段階 2）、`pr.ready`・`pr.mergeable`・`pr.review.threads_unresolved` は
使えない。`watch` は、対象のソースが出せない種類を、決して発火しない見張りとして受け付けず、断る。プロバイダ固有の条件は、2 つ目のプロバイダが必要とする
までプロバイダ名を前に付けた種類にする（`github.workflow_run.completed`）。種類を足すのはコードレビューで決めること
で、アダプタが勝手に足すものではない。

### 4. どこで動くか

- **poll は Workspace の Agent で動く。** 資格情報（「設定 > 接続」）、セッション、今の `branchpr` のキャッシュがそこに
  あるので、poll に新しい中継が要らない。
- **webhook は Control Plane が受けて検証する**が、それは**合図**にすぎない。Control Plane はその対象を登録した
  Workspace に `(source, subject)` だけを転送し、本文は何も渡さない。エンジンは利用者自身のトークンでその対象を
  すぐ poll する。だからイベントは必ず poll から生まれ、セッションが見るデータは必ず利用者自身の接続で読めるもの
  になる。読めない対象を登録した Workspace が知るのは、何かが変わったことだけ。Agent は対象への最初の申し込みで
  その対象を Control Plane に登録し、最後の申し込みが終わったら取り下げる。GitHub App の webhook の秘密は
  デプロイメント自身の設定。リポジトリの webhook には、利用者がフックを足すときに Control Plane が作った秘密を
  使う。だから配達を検証するのに、利用者の資格情報を Control Plane に置く必要はない。
- webhook は待ち時間を縮めるだけで、エンジンは申し込まれた対象を長い間隔で poll し続ける（webhook は取りこぼす
  ことがあるため）。

### 5. 届け方

- **セッションへ**：stalled-tree や PR フィードバックと同じ形の通知、
  `[agent-fleet:notice kind=watch source=github type=pr.checks.completed subject=… reply=none]`、続けて 1 行の
  `Summary`、続けて「指示ではなくデータ」と印を付けた行の下に `Data`（決まり 6.5）。見出しの値は決まった ASCII の
  集合（英字・数字・`.:/#_-`）の語に限る。外れる値は申し込みのときに断るかイベントを捨てるので、見出しを途中で
  閉じたり新しい行を始めたりはできない。通知は、セッションが idle で入力を受けられるときだけ入れる。止まっているセッションは、#3129 が今しているように
  1 本ずつ起こし、準備ができてから伝える。アーカイブされたセッションの申し込みは終わる。
- **親へ**：子は `Notify: parent` で申し込める。Issue ラウンドの親は、子の PR を持たなくても知らせを受けられる。
  親とはその子を起動したセッション（`Origin` がセッションであり、引き継ぎではない）。この申し込みは親のものになる。
  親の上限に数え、親が一覧し終わらせ、子ではなく親がアーカイブされたときに終わる。
- **利用者へ**：#3131 が今しているように通知箱（`internal/notice`）へ。
- **セッションが受けられないとき**：セッションが作った申し込みは、そのセッションと一緒に終わる。最初から入って
  いる申し込み（段階 3）は `Fallback` として利用者を指す。アーカイブされたセッションやプロンプトを受けられない
  種類について、今 #3129 がしているのと同じ。
- **一度だけ**：イベントは申し込み・`Type`・`Version` ごとに 1 回だけ届け、ディスクに記録する。再起動しても繰り
  返さない。`Version` はイベントを生んだ状態の指紋。チェックなら head SHA・結果・各チェックの識別と完了時刻
  （プロバイダが持っていれば実行の試行回数。Bitbucket のステータスと GitHub のステータスコンテキストには更新時刻
  しかない）。コンフリクトと変更要求なら、その条件が戻るたびに進むエピソードの番号。未解決のレビュースレッドなら
  その数で、1 から 2 に増えれば新しいイベントになる。だから同じコミットでの再実行や 2 度目のコンフリクトは新しい
  イベントになり、同じ状態を 2 回見ても新しいイベントにはならない。
- **`watch` のあとの最初の poll** は今の状態を評価する。すでに成り立っている条件（今の head のチェックが終わって
  いる）はすぐ届けるので、少し遅れて出した `watch` が永遠に待つことはない。最初から入っている申し込みは逆に、
  最初に見た状態を基準にする。最初に見たときにもうマージ済みの PR は知らせではなく過去だから。
- **Workspace が止まっている間**は、エンジンが Agent の中にあるので誰も poll しない。申し込みはディスクに残る。
  Workspace が起きたら、エンジンは生きている申し込みをすべて 1 回ずつ poll してから普段の予定に戻り、対象と種類
  ごとに最新の状態だけを届ける。合図が伝えたはずのことは起動時の poll が拾うので、Control Plane は止まっている
  Workspace 宛ての合図を捨てる。合図で Workspace を起こすことはない。Workspace が止まっている間に動く
  必要があるものは ADR 0021 の予定にする（§7）。

### 6. エンジンが守らせる決まり

1. **申し込み時と、届けるたびに権限を確かめる**：`Resolve` が、申し込んだ人自身の接続でその対象を読めるかを
   確かめる。別テナントの対象や、利用者のトークンで見えない対象は断る。イベントはどれもその接続で行った poll から
   生まれる（§4）ので、トークンが取り消されたりリポジトリへのアクセスを失ったりしたら、次の poll で届かなくなる。
2. **上限**：生きている申し込みはセッションごとに 20、Workspace ごとに 200 まで（`watch` は次の 1 つを断り、
   どの上限に当たったかを言う）。プロバイダが 1 時間あたりの上限を示している場合、エンジン自身のリクエストは
   接続ごとにその 10% までしか使わない（トークン 1 つは、誰が使っても GitHub の rate limit 1 つ分）。GitHub の
   `304` のようにプロバイダの上限に数えない応答は、この予算にも数えない。二次的な上限の応答（`Retry-After`）には
   従う。Bitbucket の 1 時間 1,000 リクエストは `branchpr` がもう大半を使っているので、そのソースは `branchpr` の
   キャッシュを通して読み、申し込まれた PR についてその予算の中でキャッシュの間隔を縮める。そのために `branchpr`
   には 3 つの変更が要り、段階 1 と 2 で入れる。ブランチだけでなく PR 番号でも引けるようにして、見張りとセッション
   一覧が 1 つのエントリを共有すること。今の参照は決してネットワークを待たないので、`watch` のあとの最初の poll と
   合図のために、エンジンが待てる取り直しを用意すること。申し込まれた Bitbucket の PR のコンフリクト確認の間隔を
   縮めること。1 時間あたりの上限を
   示さないプロバイダ（Jira、CloudFormation、HTTP のエンドポイント）は、接続ごとに 1 分 30 リクエストまで。
   これらの数はどれも設定で、ここに書いたのが既定値。
3. **期限**：申し込みにはすべて期限があり（既定 7 日、最長 30 日）、持ち主と一緒に終わる。
4. **間違えるより黙る**：アダプタのエラーは待ってからやり直し、`list_watches` で見えるようにする。起きていない
   イベントを作ることは決してない。
5. **プロバイダの文はデータであって指示ではない。** 通知を読むのはエージェントで、タイトル・コメント・ジョブの
   ログ・外部のソース（§8）が返すものは、利用者以外の誰かが書いたものでありうる。`Summary` は、アダプタが自分で
   決められる項目（状態・結果・件数・番号・対象の参照）から組み立てる。自由な文は `Data` にだけ入れる。通知の中の
   `Data` は 2 KiB まで。値はそれぞれ引用符で囲み、制御文字と行区切りの文字を取り除き、100 文字で切る。URL は
   `https://` のものだけ残す。

### 7. 時刻、そして ADR 0021 との関係

エンジンの `time` ソースは、動いている Workspace の中で完結するタイマー（「20 分後に」「15:00 に」）を持ち、
ほかのイベントと同じ届け先に `time.fired` を出す。ADR 0021 のスケジューラを置き換えず、それに**入力する**。
Workspace が止まっている間に生きているのは Control Plane だけで、0021 のスケジューラはもうそこで Workspace を
起こしてセッションを動かしている。

- 時刻の見張りはすべてエンジンが持つ。Workspace が動いている間はエンジンが発火させ、`list_watches` に出て、
  ほかの申し込みと同じように終わる。
- `wake` つきで頼まれた時刻の見張りは、Control Plane にも片割れを持つ。それは、止まっている Workspace を起動する
  ことだけをする予定。プロンプトも Agent へのメッセージも送らない。起動したら、エンジンはほかの申し込みと同じく
  （§5）、時刻を迎えた時刻の見張りを自分の保存先から発火させる。今の予定はどれもプロンプトで終わる
  （`rate_limit_resume.go` は止まったセッションへの reuse のプロンプトを予約する）ので、起動だけの予定は Control
  Plane の新しい宛先になる。段階 6 で作り、利用者の定時実行の一覧には出さない。エンジンは片割れの ID を記録し、
  見張りが発火したとき・`unwatch` で終わったとき・持ち主と一緒に終わったときに消す。今 `rate_limit_resume.go` が
  自分の予定の ID でしているのと同じ。届け方はそのあと §5 に従う（idle のセッションだけ、`Notify: member` も
  含む）。
  Control Plane のスケジューラが動いていないところでは、`watch` は決して発火しないタイマーを受け付けず、`wake` を
  断る。
- `wake` なしの時刻の見張りが、Workspace が止まっている間に時刻を迎えたら、次の起動時に遅れた印を付けて届ける。
  その間に申し込みの期限が切れていたら捨てる。
- Workspace を起こすのは、これからも 0021 のスケジューラだけ。

### 8. 利用者が足すソース

利用者は、「設定 > 接続 > MCP サーバー」に登録した MCP サーバーを見張りのソースとして登録できる。この機能は、管理者が
有効にするまでテナントではオフになっている。

- **契約**：サーバーは §2 の形のツールを 2 つ出す。`watch_resolve(ref)` は対象か拒否を返し、
  `watch_poll(subjects, cursors)` はイベント・カーソル・ヒントを返す。外部のソースは poll だけで、Control Plane は
  それらの webhook を受けない。
- **呼ぶのは誰か**：これらのツールを呼ぶのは Agent の中のエンジンで、エンジンの代わりにセッションが呼ぶことはない。
  見張りのソースであることは定義の上の独立した宛先（`Targets.Watch`）で、`Targets.Session` とは別。利用者は 1 つの
  サーバーを両方に渡せるし、そのサーバーに届くセッションは、もともと何でも送れる。`Resolve` はサーバーの `Kinds` を
  申し込んだセッションの種類と照らす。`ref` は印字できる ASCII の 1 行で 200 文字まで。申し込んだセッション自身が
  そのサーバーに届かない（`Targets.Session` がオフ、またはその種類が `Kinds` にない）ときは、さらに見出しの語の
  集合（§5）に限る。だから `watch` を使って、セッションが届かないサーバーにセッションの文を運ぶことはできない。
  解いた対象・ソース・種類は、いつも見出しの語の集合を使う。
- **サーバーが受け取るもの**：対象の参照と自分のカーソルだけ。セッションの中身も、自分の MCP サーバーの接続以外の
  資格情報も受け取らない。利用者のローカル（stdio）のサーバーは、許可リスト（`PATH`、`HOME`、`LANG`、`TMPDIR`）、
  `mcpreg.ForwardEnvNames` がすべての定義に渡す変数（隔離の印）、自分に設定された変数から組み立てた環境で起動する。
  Agent 自身の環境は決して渡さない。そこには秘密の保存先の鍵と Agent の内部トークンがあるから。今の stdio の接続
  処理と疎通確認はどのサーバーにも Agent の環境をそのまま渡しているので、段階 5 で定義ごとに組み立てるように変える。
  組み込みのサーバーは `ForwardEnvNames` が渡すものをそのまま持つ（組み込みの `af` サーバーは Agent のトークンを、
  運用のサーバーは秘密の保存先の鍵を要る）ので、取り上げられるのは利用者が足したサーバーだけ。
- **名前空間**：イベントの種類は `ext.<source>.*`、対象は `ext:<source>:…` でなければならない。それ以外は捨てて
  エラーとして数える。だから外部のソースは `pr.merged` を名乗れないし、ほかのソースの対象について語れない。
- **届く範囲**：イベントは、登録した利用者のセッションが持つ申し込み（自分、親）か、その利用者にだけ届く。
  Control Plane、ほかの利用者、ほかの Workspace には決して転送しない。
- **出力**：`Summary` はサーバーではなくエンジンが `source`・`type`・`subject` から組み立てる。サーバーが返した要約は
  ほかと一緒に `Data` に入れ、すべて決まり 6.5 のプロバイダの自由な文として扱う。
- **予算**：1 回の呼び出しは 10 秒で打ち切る。対象は `Hint` が何と言っても 1 分に 1 回までしか poll しない。
  1 回の呼び出しで渡す対象は 20 まで、返せるイベントは 20 まで。サーバーは、ある対象の待っているイベントが
  すべて返答に入ったときだけ、その対象の新しいカーソルを返す。カーソルが返らなかった対象は、古いカーソルから
  もう一度聞く。エンジンは時刻を迎えた対象を最後に poll したのが古い順に送るので、忙しい 1 つの対象がほかを
  飢えさせることはない。5 回続けて失敗したらソースを止め、止まったことを
  `list_watches` に出し、利用者が再開する。
- **隔離**：上の環境のほか、ローカルのサーバーは、セッションが起動する MCP サーバーと同じユーザー・同じ制限で動く。
  つまり Workspace のコンテナがそのサンドボックスで、利用者が動かすほかのプロセスと同じ。ソースで
  あることで特別な権限は得ない。リモートのサーバーには、セッションが届くのと同じ方法で届く。

### 9. このエンジンで受け止めるつもりのソース

| 分野 | 例 |
|---|---|
| コードのホスティング | GitHub、Bitbucket、のちに GitLab：チェック、コンフリクト、マージ、レビュー、基底ブランチへの新しいコミット |
| トラッカー | Jira、GitHub / Bitbucket の Issue：状態の遷移、担当の割り当て、セッションの項目に触れた新しいコメント |
| 単独の CI | PR に結び付かない GitHub Actions / Bitbucket Pipelines の実行（夜間、リリースのワークフロー）、リリースタグの公開 |
| デプロイ | CloudFormation のスタックの状態、ECS サービスの安定、Kubernetes のロールアウト、レジストリにコンテナイメージが出たこと |
| Agent Fleet 自身 | ピアのセッションが idle になる・終わる、子のターンが終わる、利用枠が戻る（`sessionx/rate_limit_resume.go`）、長いバックグラウンドのコマンドが終わる |
| エンドポイント | デプロイ後に HTTP のヘルスチェックが緑になる、証明書の期限が近い |
| 時刻 | 「03:00 に」「20 分後に」を時刻のソースとして扱う。定時実行（ADR 0021）を置き換えず、それに入力する（§7） |
| セキュリティ情報 | 依存関係への新しい勧告、公開イメージの脆弱性スキャンの新しい検出（#3339） |

この表は方向であって約束ではない。ソースはそれぞれ別の変更として、別にレビューする。

### 10. 段階

1. **エンジン＋`github` ソース＋MCP ツール**（`watch`、`unwatch`、`list_watches`）。`github` の poller は、
   申し込まれた PR について、条件付きリクエスト（`If-None-Match`。`304` は GitHub の primary rate limit に数えない）
   の REST と間隔の調整を足す。チェックが終わっても PR そのものの ETag は変わらないので、PR 1 つに複数の
   エンドポイントを使う：PR（`pulls/{n}`）、その head のチェックランと合成ステータス、レビュー。取ったものは
   `branchpr` のキャッシュに書き戻し、同じ PR を 2 回取らない。段階 3 までは、セッションが自分の PR のチェックや
   レビューに申し込んでいれば、そのセッションと PR については #3129 の通知を止め、2 回伝えないようにする。
2. **`bitbucket` ソース**（Bitbucket には ETag がない：`branchpr` の予算の中で、決まり 6.2 の `branchpr` の変更と
   ともに間隔を調整する。マージの可否・承認・コメントのスレッドを読み、§3 が Bitbucket では使えないとした種類を
   使えるようにする）。
3. **#3129 と #3131 をエンジンに移す**。最初から入っている申し込みにし、`Fallback` は利用者にする。#3129 は
   `prFeedback` のスイッチ、レビュー通知の上限（`Until`：3 回）、持っている PR リンクの見張りを残す。#3131 には
   スイッチがなく、足さない。webhook は要らないので、webhook より先にする。
4. **Control Plane の webhook の受け口**（まず GitHub App。App を入れられないところはリポジトリの webhook。
   Bitbucket はリポジトリの webhook）。転送するのは合図だけで、秘密は §4 のとおり。
5. **利用者が足すソース**（§8）。テナントのスイッチの裏に置き、stdio のサーバーには環境の許可リストを使う。
6. **`time` ソース**（§7）と、Control Plane の起動だけの予定の宛先。そのあと §9 のほかのソースを 1 つずつ。

## Rejected

- **専用の `watch_pr` ツールと速い `branchpr`。** PR の場合はすぐ解けるが、手作りの見張りが 6 つ目になる。次の
  プロバイダで同じ仕事を繰り返す。
- **webhook だけ。** すべてのリポジトリに App か hook の管理権限が要り、Control Plane がインターネットから
  届く必要がある。配達の取りこぼしは気づかれない見落としになる。速い経路として残し、唯一の経路にはしない。
- **Control Plane から poll する。** 利用者の資格情報とセッションは Workspace にある。Control Plane から poll すると、
  そこで要らない機能のために利用者のトークンを中央で持つことになる。
- **ADR 0021 のスケジューラを `time` ソースで置き換える。** エンジンは Agent で動き、Agent は Workspace が
  止まっている間は生きていない。置き換えるには、エンジン（かその写し）を Control Plane に移し、もう動いている
  スケジューラを書き直すことになる。
- **組み込みのソースだけにする。** 隙間のシステム（社内のデプロイツール、チームのステータスページ）ごとに
  Agent Fleet 自体の変更が要る。§8 の名前空間・届く範囲・予算の決まりで、利用者のソースがほかの誰にも影響しない
  ようにする。
- **アダプタにイベントの種類を作らせる。** 同じ条件にプロバイダごとの名前が付き、セッションは相手がどの
  プロバイダかを知らなければならなくなる。

## Consequences

- バックグラウンドで `gh pr checks --watch` を走らせているスキル（issue-rounds、issue-to-pr）は、段階 1 が出たら
  `watch` 1 回に置き換えられる。
- エンジンは Agent のいくつもの機能が頼るものになる。保存と予定組みには専用のテストと計測（接続ごとの 1 時間の
  リクエスト数、届くまでの遅れ）が要る。
- webhook の受け口は、セッションで認証しない公開の口を Control Plane に足す。署名の検証で守り、段階 4 で専用の
  セキュリティレビューをする。
- 利用者が足すソースは、利用者が選んだコードをエンジンの通り道に置く。§8 で出せるものと届く先を絞り、段階 5 は
  段階 4 と同じく専用のセキュリティレビューをする。

## Open questions

残っていない。下書きで挙げた 3 つは、§6（上限と期限）、§7（時刻と ADR 0021）、§8（利用者が足すソース）で答えた。
