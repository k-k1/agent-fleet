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

見張るものはこれからも増える。CI と PR のほかに利用者はデプロイを挙げた。ほかの候補は §7 に挙げる。

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

### 6. エンジンが守らせる決まり

1. **申し込み時と、届けるたびに権限を確かめる**：`Resolve` が、申し込んだ人自身の接続でその対象を読めるかを
   確かめる。別テナントの対象や、利用者のトークンで見えない対象は断る。webhook のイベントは、そうして権限を
   確かめた申し込みのある Workspace にだけ転送する。
2. **上限**：セッションごと・Workspace ごとの申し込み数と、接続ごとのリクエスト予算（トークン 1 つは、誰が使っても
   GitHub の rate limit 1 つ分）。
3. **期限**：申し込みにはすべて期限があり（既定 7 日）、持ち主と一緒に終わる。
4. **間違えるより黙る**：アダプタのエラーは待ってからやり直し、`list_watches` で見えるようにする。起きていない
   イベントを作ることは決してない。

### 7. このエンジンで受け止めるつもりのソース

| 分野 | 例 |
|---|---|
| コードのホスティング | GitHub、Bitbucket、のちに GitLab：チェック、コンフリクト、マージ、レビュー、基底ブランチへの新しいコミット |
| トラッカー | Jira、GitHub / Bitbucket の Issue：状態の遷移、担当の割り当て、セッションの項目に触れた新しいコメント |
| 単独の CI | PR に結び付かない GitHub Actions / Bitbucket Pipelines の実行（夜間、リリースのワークフロー）、リリースタグの公開 |
| デプロイ | CloudFormation のスタックの状態、ECS サービスの安定、Kubernetes のロールアウト、レジストリにコンテナイメージが出たこと |
| Agent Fleet 自身 | ピアのセッションが idle になる・終わる、子のターンが終わる、利用枠が戻る（`limitepoch`）、長いバックグラウンドのコマンドが終わる |
| エンドポイント | デプロイ後に HTTP のヘルスチェックが緑になる、証明書の期限が近い |
| 時刻 | 「03:00 に」「20 分後に」を時刻のソースとして扱う。定時実行（ADR 0021）と「あとで知らせて」が同じ届け方を使える |
| セキュリティ情報 | 依存関係への新しい勧告、公開イメージの脆弱性スキャンの新しい検出（#3339） |

この表は方向であって約束ではない。ソースはそれぞれ別の変更として、別にレビューする。

### 8. 段階

1. **エンジン＋`github` ソース＋MCP ツール**（`watch`、`unwatch`、`list_watches`）。`github` の poller は、
   申し込まれた PR について、条件付きリクエスト（`If-None-Match`。`304` は GitHub の primary rate limit に数えない）
   の REST と間隔の調整を足す。セッション一覧用に `branchpr` が使うまとめ取りの GraphQL はそのまま並べて使う。
2. **`bitbucket` ソース**（Bitbucket には ETag が無い：間隔の調整だけ）。
3. **Control Plane の webhook の受け口**（まず GitHub App。App を入れられないところはリポジトリの webhook。
   Bitbucket はリポジトリの webhook）。
4. **#3129 と #3131 をエンジンに移す**。最初から入っている申し込みにし、利用者向けのスイッチは残す。
5. §7 のほかのソースを 1 つずつ。

## Rejected

- **専用の `watch_pr` ツールと速い `branchpr`。** PR の場合はすぐ解けるが、手作りの見張りが 6 つ目になる。次の
  プロバイダで同じ仕事を繰り返す。
- **webhook だけ。** すべてのリポジトリに App か hook の管理権限が要り、Control Plane がインターネットから
  届く必要がある。配達の取りこぼしは気づかれない見落としになる。速い経路として残し、唯一の経路にはしない。
- **Control Plane から poll する。** 利用者の資格情報とセッションは Workspace にある。Control Plane から poll すると、
  そこで要らない機能のために利用者のトークンを中央で持つことになる。
- **アダプタにイベントの種類を作らせる。** 同じ条件にプロバイダごとの名前が付き、セッションは相手がどの
  プロバイダかを知らなければならなくなる。

## Consequences

- バックグラウンドで `gh pr checks --watch` を走らせているスキル（issue-rounds、issue-to-pr）は、段階 1 が出たら
  `watch` 1 回に置き換えられる。
- エンジンは Agent のいくつもの機能が頼るものになる。保存と予定組みには専用のテストと計測（接続ごとの 1 時間の
  リクエスト数、届くまでの遅れ）が要る。
- webhook の受け口は、セッションで認証しない公開の口を Control Plane に足す。署名の検証で守り、段階 3 で専用の
  セキュリティレビューをする。

## Open questions

- 時刻のソース（§7）が ADR 0021 のスケジューラを置き換えるのか、それに入力するだけなのか。
- 利用者が自分のソースを外部プロセス（例：`poll` を出す MCP サーバ）として足せるようにするか。そのソースを
  どう隔離するか。
- 申し込み数の既定の上限と、既定の期限。
