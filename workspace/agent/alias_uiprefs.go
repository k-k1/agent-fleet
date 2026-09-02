package main

// internal/uiprefs へ移した ui-prefs 層の別名（ADR 0067 のエイリアス移送）。
// 呼び出し側（100 ファイル超）は 1 行も触らずに済ませるための 1 枚で、
// 回収はウェーブ境界の別セッションが行う。
//
// 遠側はすべて **func**（`var x = uiprefs.Y` が写しになる罠は、遠側が var のときだけ。
// 遠側が func / type / const なら安全）。uiprefs 側には var は無い。
//
// この import が uiprefs の init（mcpreg.PeerMessagingEnabled / opencode.UsagePref の
// フック登録）を走らせる唯一の経路でもある。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"

const maxUIPrefsBytes = uiprefs.MaxBytes

var (
	uiPrefsPath       = uiprefs.Path
	uiPrefsBackupPath = uiprefs.BackupPath
	readUIPrefs       = uiprefs.Read
	shrunkPrefKeys    = uiprefs.ShrunkKeys

	autoTitleSuggestEnabled      = uiprefs.AutoTitleSuggest
	assistantTitleSuggestEnabled = uiprefs.AssistantTitleSuggest
	opencodeCatalogPref          = uiprefs.OpencodeCatalog
	peerMessagingPref            = uiprefs.PeerMessaging
	claudeCustomModelsPref       = uiprefs.ClaudeCustomModels

	chatAutoTurnEnabled        = uiprefs.ChatAutoTurn
	chatQuietCompletionEnabled = uiprefs.ChatQuietCompletion
	chatAutoPilotEnabled       = uiprefs.ChatAutoPilot
	chatAutoResumeEnabled      = uiprefs.ChatAutoResume
	rateLimitAutoResumeEnabled = uiprefs.RateLimitAutoResume
	abortAutoResumeEnabled     = uiprefs.AbortAutoResume
	chatAutoCompactEnabled     = uiprefs.ChatAutoCompact
	chatOutputLanguage         = uiprefs.ChatOutputLanguage
	uiLocale                   = uiprefs.Locale
)
