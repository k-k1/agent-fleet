# 115 実装レビュー（2026-09-24）

対象: `git diff origin/develop...HEAD`（`temp/spaub7a`、先頭 `c23552d22`）。
ADR 0101 と log 115 を読んだうえで、実害のある指摘だけを記す。

## 指摘

1. 🔴 **同一セッションの二重削除で、唯一のごみ箱アーカイブが消える。**
   `workspace/agent/cleanup_archive.go:75-76,110-114`、
   `workspace/agent/cleanup_ops.go:119-135`。
   二つの DELETE が同じ秒にメタを読み、同じ名前のアーカイブを作ると、
   `newCleanupID` は両方に同じ ID を返し、後の `os.WriteFile` が前の gz を上書きする。
   一方がメタを消した後、もう一方は `gone` と判定して共有 ID のアーカイブを
   `purgeCleanupArchive` で消す。両方の要求が終わるとメタも gz もなく、会話を復元できない。
   アーカイブ ID に衝突しない乱数を付け、作成を排他的に行う。さらに削除処理を
   セッション名ごとに直列化し、失敗側が自分の所有しないアーカイブを purge しないようにする。

2. 🔴 **退避中の再開を検出せず、生きている会話の新規追記を消し得る。**
   `workspace/agent/cleanup_ops.go:94-110,119-145`、
   `workspace/agent/internal/sessionx/session_ssm.go:43-58`。
   停止中セッションの DELETE が `SessionAlive` を false と読んだ直後、別の要求が
   `/start` や通常の再開を成功させられる。退避がその後に読んだ jsonl より後に
   CLI が追記しても、削除側は生存状態を再確認せず、メタと jsonl を消し、managed
   なら新しい handle も `ForgetRuntime` で落とす。追記分は gz に存在しない。
   再開と削除を同じセッション単位の排他制御に載せ、退避後に世代・生存状態・
   対象ファイルが変わった場合は削除を中止して再退避する。

3. 🔴 **作業コピーの削除ロックを、確認後の競合で越えられる。**
   `workspace/agent/internal/gitx/git.go:1568-1577,1611-1616`、
   `workspace/agent/internal/sessionx/locks.go:92-105`。
   `HandleDeleteRepo` は作業コピーとセッションのロックを一度読んだ後、
   `git worktree remove` まで保護を保持しない。時間のかかる git 操作中に利用者が
   作業コピーまたは配下のセッションをロックすると、その操作は成功したように返るが、
   削除側はそのままフォルダを消す。後段の `shelveSessionsUnder` はロック済みメタを
   残すため、フォルダを失った保護対象のセッションも残る。削除とロック更新を
   共通の排他制御で直列化し、削除開始後のロック要求の扱いも定める。

4. 🟡 **作業コピー削除後のごみ箱失敗を成功として返す。**
   `workspace/agent/internal/gitx/git.go:1611-1617,1653-1672`。
   shell / ssm のメタが残る worktree を MCP または掃除②から削除し、
   gz の書き込みが容量不足などで失敗すると、`shelveSessionsUnder` はログを出すだけで
   HTTP 200 を返す。作業コピーは既に消え、shell / ssm はごみ箱ではなく通常一覧に
   「Folder missing」の行として残る。ADR 0101 決定4と確認文面に反する。
   削除前に shell / ssm の退避を完了させ、失敗時は作業コピー削除を止める。
   少なくとも後段の失敗を HTTP 応答に載せ、成功を偽らない。

5. 🟡 **メタ削除後に、保護されていない書き戻しが行を復活させ、ロックも巻き戻す。**
   `workspace/agent/cleanup_ops.go:119-129`、
   `workspace/agent/internal/sessionx/session_handlers.go:1563-1570`、
   `workspace/agent/internal/sessionx/session_title.go:130-135`。
   `trashSession` は `sessionLockMu` 内でメタを消すが、棚からの復帰や非同期の
   タイトル生成は `ReadMeta` の後に同じロックを取らず `WriteMeta` する。
   その間に削除が終われば、jsonl がごみ箱にある空の会話行が再出現する。
   同様にメタを読んだ後で付けた削除ロックを古いスナップショットが false に戻せば、
   後続の削除が通る。読み書きを同じロックに入れ、対象メタが消えていたら書かない。
   再現条件が明確な二経路のほか、`session.WriteMeta` の他の呼び出しも点検する。

6. 🟡 **新 Console と旧 Agent の組み合わせで、稼働中 shell / ssm の削除が失敗する。**
   `console/src/core/api/client.ts:798-803`、
   `console/src/features/sessions/useSessionActions.tsx:90-93`。
   新 Console は稼働中の shell / ssm に `DELETE ?reclaim=1&stop=1` を送る。
   旧 Agent は `stop` を解釈せず、稼働中なら 409 を返すため、行の「削除」や
   「作業コピーを削除」のセッション整理が進まない。`reclaim=1` による旧 Agent の
   ごみ箱対応は、停止中にしか効かない。409 の際は `/halt` で停止してから
   `DELETE ?reclaim=1` を再試行するなど、旧 Agent でも安全な手順にする。

7. 🟡 **古いアーカイブの完全削除が、まだ復元できる別アーカイブの台帳を消す。**
   `workspace/agent/cleanup_archive.go:282-309`。
   managed セッションを削除→復元→再び削除すると、同じ名前の gz が二つ残る。
   メタが無い状態で古い方だけ purge すると、`dropPurgedLedgers` は新しい gz を
   調べず ClientMessageID 台帳を消す。新しい方を復元して再開した後、既処理の
   メッセージ ID の再送を重複排除できず、同じ指示を二度実行し得る。
   そのセッションを復元可能な gz が一つでも残る間は台帳を保持するか、
   復元に必要な台帳を各アーカイブに入れる。

## 確認した範囲

- `rg` で Go 本番コードの `RemoveMeta` / `RemoveMetaAndLineage` とメタファイル削除を
  検索した。メタを直接消す呼び出しは `trashSession` 内の
  `RemoveMetaAndLineage` 一箇所で、ごみ箱を通らない直接削除経路は見つからなかった。
  ただし上記 1、2 の競合では、そのごみ箱が復元不能になり得る。
- Console の変更対象である `admin`、`repos`、`sessions`、`tools` の ja/en キー集合を
  比較し、不一致はなかった。
- 静的なコード追跡とキー照合を実施した。テストは実行していない。

件数: 🔴 3、🟡 4、🔵 0。
