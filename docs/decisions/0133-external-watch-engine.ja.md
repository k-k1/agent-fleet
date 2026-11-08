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
| `internal/branchpr` | GitHub（GraphQL、まとめ取り）と Bitbucket の PR の状態 | poll。開いた PR は 2 分ごと（`openTTL`）、落ち着いたものは 10 分ごと、待ちは 5 / 15 分 |
| PR フィードバック監視（#3129、`sessionx/pr_feedback.go`） | セッション自身の PR の CI 失敗と変更要求 | `branchpr` のキャッシュを読む。PR を持つセッションに通知を入れる。既定はオフ |
| PR 通知（#3131、`sessionx/session_pr_notices.go`） | マージ可能・マージ済み・クローズ | 同じキャッシュを読む。利用者向けの通知箱に書く |
| stalled-tree 監視（#2397、`sessionx/stalled_tree.go`） | 親と子が全員 idle | 自分のループ。通知を入れる |
| 作業項目の受け箱（ADR 0061、`internal/workitems`） | 利用者向けの GitHub / Bitbucket / Jira の項目 | 自分の問い合わせとキャッシュ |

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
   Poll (Agent side)    Webhook (Control Plane side)
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
    Parse(r *http.Request) ([]Event, error)
}

type Event struct {
    Source  string          // "github"
    Subject SubjectID       // "github:k-k1/agent-fleet#3606"
    Type    string          // shared vocabulary, §3: "pr.checks.completed"
    Version string          // de-duplication key: head SHA, updated_at, delivery ID
    Summary string          // one line a session can act on
    Data    json.RawMessage // provider detail (failed jobs, URLs)
    At      time.Time
}

type Subscription struct {
    Owner   SessionRef      // who asked
    Subject SubjectID
    Types   []string        // patterns: "pr.checks.*"
    Until   UntilRule       // first match | subject closed | deadline
    Notify  SinkRef         // self | parent | member
}
```

プロバイダを知るのはアダプタ、時間を知るのはエンジン。聞きに行く間隔、エラーや rate limit のあとの待ち、
push と poll のどちらを使うかはエンジンが決め、アダプタの `Hint` を手がかりにする（例「チェック実行中：15 秒後に
また聞く価値あり」「落ち着いた：10 分」「T まで rate limit」）。

### 3. 共通のイベントの語彙

イベントの種類は名前空間つきで、プロバイダをまたいで共通にする。だからセッションは、PR が GitHub でも Bitbucket でも
`pr.checks.completed` を申し込む。最初の組：`pr.checks.completed`（全体の結果と落ちたチェックつき）、
`pr.checks.failed`、`pr.conflict`、`pr.mergeable`、`pr.merged`、`pr.closed`、`pr.review.changes_requested`、
`issue.transitioned`、`deploy.completed`、`deploy.failed`。プロバイダ固有の条件は、2 つ目のプロバイダが必要とする
までプロバイダ名を前に付けた種類にする（`github.workflow_run.completed`）。種類を足すのはコードレビューで決めること
で、アダプタが勝手に足すものではない。

### 4. どこで動くか

- **poll は Workspace の Agent で動く。** 資格情報（設定 > 接続）、セッション、今の `branchpr` のキャッシュがそこに
  あるので、poll に新しい中継が要らない。
- **webhook は Control Plane が受け**、検証して同じ `Event` にし、その対象を申し込んでいる Workspace に転送する。
  webhook は待ち時間を縮めるだけで、エンジンは申し込まれた対象を長い間隔で poll し続ける（webhook は取りこぼす
  ことがあるため）。
- セッションにはどちらの経路でも同じ `Event` が届き、どちらで届いたかは区別できないし、気にする必要もない。

### 5. 届け方

- **セッションへ**：stalled-tree や PR フィードバックと同じ形の通知
  （`[agent-fleet:notice kind=watch source=github type=pr.checks.completed subject=…]` と 1 行の要約）を、
  セッションが idle で入力を受けられるときだけ入れる。止まっているセッションは、#3129 が今しているように
  1 本ずつ起こし、準備ができてから伝える。アーカイブされたセッションの申し込みは終わる。
- **親へ**：子は `Notify: parent` で申し込める。Issue ラウンドの親は、子の PR を持たなくても知らせを受けられる。
- **利用者へ**：#3131 が今しているように通知箱（`internal/notice`）へ。
- **一度だけ**：イベントは申し込みと `Version` ごとに 1 回だけ届け、ディスクに記録する。再起動しても繰り返さない。
- **Workspace が止まっている間**は、エンジンが Agent の中にあるので誰も poll しない。申し込みはディスクに残る。
  Workspace が起きたら、エンジンは生きている申し込みをすべて 1 回ずつ poll してから普段の予定に戻り、対象ごとに
  最新の状態だけを届ける。止まっている Workspace 宛ての webhook のイベントは Control Plane が預かり（対象ごとに
  最新のもの、上限つき）、起動時に渡す。それで Workspace を起こすことはない。Workspace が止まっている間に動く
  必要があるものは ADR 0021 の予定にする（§7）。

### 6. エンジンが守らせる決まり

1. **申し込み時と、届けるたびに権限を確かめる**：`Resolve` が、申し込んだ人自身の接続でその対象を読めるかを
   確かめる。別テナントの対象や、利用者のトークンで見えない対象は断る。webhook のイベントは、そうして権限を
   確かめた申し込みのある Workspace にだけ転送する。
2. **上限**：生きている申し込みはセッションごとに 20、Workspace ごとに 200 まで（`watch` は次の 1 つを断り、
   どの上限に当たったかを言う）。エンジン自身のリクエストは、接続の 1 時間あたりのリクエスト予算の 10% までしか
   使わない（トークン 1 つは、誰が使っても GitHub の rate limit 1 つ分）。GitHub の `304` のようにプロバイダの
   上限に数えない応答は、この予算にも数えない。3 つの数はどれも設定で、ここに書いたのが既定値。
3. **期限**：申し込みにはすべて期限があり（既定 7 日、最長 30 日）、持ち主と一緒に終わる。
4. **間違えるより黙る**：アダプタのエラーは待ってからやり直し、`list_watches` で見えるようにする。起きていない
   イベントを作ることは決してない。
5. **プロバイダの文はデータであって指示ではない。** 通知を読むのはエージェントで、タイトル・コメント・ジョブの
   ログ・外部のソース（§8）が返すものは、利用者以外の誰かが書いたものでありうる。`Summary` は、アダプタが自分で
   決められる項目（状態・結果・件数・番号・対象の参照）から組み立てる。自由な文は `Data` にだけ入れ、決まった
   長さで切り、通知の中で信頼できないデータだと印を付ける。

### 7. 時刻、そして ADR 0021 との関係

エンジンの `time` ソースは、動いている Workspace の中で完結するタイマー（「20 分後に」「15:00 に」）を持ち、
ほかのイベントと同じ届け先に `time.fired` を出す。ADR 0021 のスケジューラを置き換えず、それに**入力する**。
Workspace が止まっている間に生きているのは Control Plane だけで、0021 のスケジューラはもうそこで Workspace を
起こしてセッションを動かしている。

- `wake` つきで頼まれた時刻の見張りはエンジンが持たない。Agent が今あるスケジュールの橋渡しを通して ADR 0021 の
  予定として登録し、その結果は見張りの届け先を通って戻ってくる。
- `wake` なしの時刻の見張りが、Workspace が止まっている間に時刻を迎えたら、次の起動時に遅れた印を付けて届ける。
  その間に申し込みの期限が切れていたら捨てる。
- Workspace を起こすのは、これからも 0021 のスケジューラだけ。

### 8. 利用者が足すソース

利用者は、設定 > 接続 > MCP サーバに登録した MCP サーバを見張りのソースとして登録できる。この機能は、管理者が
有効にするまでテナントではオフになっている。

- **契約**：サーバは §2 の形のツールを 2 つ出す。`watch_resolve(ref)` は対象か拒否を返し、
  `watch_poll(subjects, cursors)` はイベント・カーソル・ヒントを返す。外部のソースは poll だけで、Control Plane は
  それらの webhook を受けない。
- **呼ぶのは誰か**：これらのツールを呼ぶのは Agent の中のエンジンで、セッションではない。サーバが受け取るのは
  対象の参照と自分のカーソルだけ。セッションの中身も、自分の MCP サーバの接続以外の資格情報も受け取らない。
- **名前空間**：イベントの種類は `ext.<source>.*`、対象は `ext:<source>:…` でなければならない。それ以外は捨てて
  エラーとして数える。だから外部のソースは `pr.merged` を名乗れないし、ほかのソースの対象について語れない。
- **届く範囲**：イベントは、登録した利用者のセッションが持つ申し込み（自分、親）か、その利用者にだけ届く。
  Control Plane、ほかの利用者、ほかの Workspace には決して転送しない。
- **出力**：`Summary` は 200 文字、`Data` は 8 KiB までで、どちらも決まり 6.5 のプロバイダの自由な文として扱う。
- **予算**：1 回の呼び出しは 10 秒で打ち切る。対象は `Hint` が何と言っても 1 分に 1 回までしか poll しない。
  1 回の呼び出しで渡す対象は 50 まで、返せるイベントは 20 まで。5 回続けて失敗したらソースを止め、止まったことを
  `list_watches` に出し、利用者が再開する。
- **隔離**：ローカル（stdio）のサーバは、セッションが起動する MCP サーバと同じユーザー・同じ制限で Agent の子
  として動く。つまり Workspace のコンテナがそのサンドボックスで、利用者が動かすほかのプロセスと同じ。ソースで
  あることで特別な権限は得ない。リモートのサーバには、セッションが届くのと同じ方法で届く。

### 9. このエンジンで受け止めるつもりのソース

| 分野 | 例 |
|---|---|
| コードのホスティング | GitHub、Bitbucket、のちに GitLab：チェック、コンフリクト、マージ、レビュー、基底ブランチへの新しいコミット |
| トラッカー | Jira、GitHub / Bitbucket の Issue：状態の遷移、担当の割り当て、セッションの項目に触れた新しいコメント |
| 単独の CI | PR に結び付かない GitHub Actions / Bitbucket Pipelines の実行（夜間、リリースのワークフロー）、リリースタグの公開 |
| デプロイ | CloudFormation のスタックの状態、ECS サービスの安定、Kubernetes のロールアウト、レジストリにコンテナイメージが出たこと |
| Agent Fleet 自身 | ピアのセッションが idle になる・終わる、子のターンが終わる、利用枠が戻る（`limitepoch`）、長いバックグラウンドのコマンドが終わる |
| エンドポイント | デプロイ後に HTTP のヘルスチェックが緑になる、証明書の期限が近い |
| 時刻 | 「03:00 に」「20 分後に」を時刻のソースとして扱う。定時実行（ADR 0021）を置き換えず、それに入力する（§7） |
| セキュリティ情報 | 依存関係への新しい勧告、公開イメージの脆弱性スキャンの新しい検出（#3339） |

この表は方向であって約束ではない。ソースはそれぞれ別の変更として、別にレビューする。

### 10. 段階

1. **エンジン＋`github` ソース＋MCP ツール**（`watch`、`unwatch`、`list_watches`）。`github` の poller は、
   申し込まれた PR について、条件付きリクエスト（`If-None-Match`。`304` は GitHub の primary rate limit に数えない）
   の REST と間隔の調整を足す。セッション一覧用に `branchpr` が使うまとめ取りの GraphQL はそのまま並べて使う。
2. **`bitbucket` ソース**（Bitbucket には ETag が無い：間隔の調整だけ）。
3. **Control Plane の webhook の受け口**（まず GitHub App。App を入れられないところはリポジトリの webhook。
   Bitbucket はリポジトリの webhook）。
4. **#3129 と #3131 をエンジンに移す**。最初から入っている申し込みにし、利用者向けのスイッチは残す。
5. **利用者が足すソース**（§8）。テナントのスイッチの裏に置く。
6. **`time` ソース**（§7）。そのあと §9 のほかのソースを 1 つずつ。

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
- webhook の受け口は、セッションで認証しない公開の口を Control Plane に足す。署名の検証で守り、段階 3 で専用の
  セキュリティレビューをする。
- 利用者が足すソースは、利用者が選んだコードをエンジンの通り道に置く。§8 で出せるものと届く先を絞り、段階 5 は
  段階 3 と同じく専用のセキュリティレビューをする。

## Open questions

残っていない。下書きで挙げた 3 つは、§6（上限と期限）、§7（時刻と ADR 0021）、§8（利用者が足すソース）で答えた。
