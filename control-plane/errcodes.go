package main

// Console 側で和文ローカライズされるエラーコード（console/src/core/api/client.ts の
// ERR_TEXT と対、docs/23 P0-3）。ここの文字列を変えると Console の文言解決が落ちて
// developer メッセージへフォールバックする — 変更は必ず両側同時に。Agent 側の対は
// workspace/agent/errcodes.go。
const errCodeQuotaSessions = "quota_sessions"
