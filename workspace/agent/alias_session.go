package main

// alias_session.go — session 家系を `internal/sessionx` へ移した際の**エイリアス移送**
// （ADR 0067 の規律①）。呼び出し側を 1 行も触らないための 1 枚で、**ここに在るのは
// 「別名」だけ**。sessionx → main の逆向きの配線は session_wiring.go に分けてある
// （エイリアスはウェーブ境界で丸ごと剥がれて消えるが、配線は残るため）。
//
// 🔥 **変数の別名は値の写しになる。** 写しても安全なことを 4 本とも確かめてある:
//
//   - errInjectEmpty       sentinel（`errors.Is` は写しの下の実体を比べるので同一と判定される）
//   - peerIntentNames      読み取り専用のスライス（ヘッダの写しは同じ配列を指す）
//   - operatorTurnTimeout  再代入されない Duration
//   - rateLimitStates      `fstore.Store[T]` ——**錠を持たず状態は全部ディスク側**で、
//                          base はストア生成時に注入され呼び出しの都度解決される
//
// 🔥 **「テストが代入する var」は写しにできない**（main 側の代入が sessionx に届かず、
// スタブが黙って効かなくなる）。実測で `dismissRateLimitModal` / `putRateLimitSchedule` /
// `dropRateLimitSchedule` / `rateLimitResetAt` の 4 本が該当したため、**それを差し替える
// テスト（rate_limit_resume_test.go）は sessionx 側に置いた**。この 4 本はここに出て来ない。
// 新しく var を足すときは、**テストが代入していないか**を必ず数えること。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"

// 型
type createReq = sessionx.CreateReq
type replyMsg = sessionx.ReplyMsg

const (
	branchSuggestPersona     = sessionx.BranchSuggestPersona
	markQuoteMaxRunes        = sessionx.MarkQuoteMaxRunes
	replyCounterpartChat     = sessionx.ReplyCounterpartChat
	replyCounterpartSession  = sessionx.ReplyCounterpartSession
	replySuggestTimeout      = sessionx.ReplySuggestTimeout
	sessionTitleMaxRunes     = sessionx.SessionTitleMaxRunes
	titleSuggestTimeout      = sessionx.TitleSuggestTimeout
	turnSourceAutoResume     = sessionx.TurnSourceAutoResume
	turnSourceDiscord        = sessionx.TurnSourceDiscord
	turnSourceOperator       = sessionx.TurnSourceOperator
	turnSourceSchedule       = sessionx.TurnSourceSchedule
	turnSourceScheduleManual = sessionx.TurnSourceScheduleManual
	turnSourceSlack          = sessionx.TurnSourceSlack
)

// 変数（写しになる。上の注意書きを読むこと）
var (
	errInjectEmpty      = sessionx.ErrInjectEmpty
	operatorTurnTimeout = sessionx.OperatorTurnTimeout
	peerIntentNames     = sessionx.PeerIntentNames
	rateLimitStates     = sessionx.RateLimitStates
)

// 関数（変数で受ける。Go は関数をエイリアスできない）
var (
	paneMode                     = sessionx.PaneMode
	abortResumeHolds             = sessionx.AbortResumeHolds
	absPath                      = sessionx.AbsPath
	addHandoffProposal           = sessionx.AddHandoffProposal
	agentOf                      = sessionx.AgentOf
	aggregateUsage               = sessionx.AggregateUsage
	approvalLabel                = sessionx.ApprovalLabel
	armOperatorTurn              = sessionx.ArmOperatorTurn
	branchSuggestPrompt          = sessionx.BranchSuggestPrompt
	bridgeAnswerEN               = sessionx.BridgeAnswerEN
	bridgeApprovalGate           = sessionx.BridgeApprovalGate
	cleanBranchName              = sessionx.CleanBranchName
	cleanSuggestedReplies        = sessionx.CleanSuggestedReplies
	cleanSuggestedTitle          = sessionx.CleanSuggestedTitle
	cleanTitle                   = sessionx.CleanTitle
	createOrigin                 = sessionx.CreateOrigin
	disarmOperatorTurn           = sessionx.DisarmOperatorTurn
	driveState                   = sessionx.DriveState
	ensureClaudeSettingsWiring   = sessionx.EnsureClaudeSettingsWiring
	fb                           = sessionx.Fb
	filterVisibleModels          = sessionx.FilterVisibleModels
	handleAcceptSuggestedTitle   = sessionx.HandleAcceptSuggestedTitle
	handleArchiveSession         = sessionx.HandleArchiveSession
	handleChatLock               = sessionx.HandleChatLock
	handleChatPasteImage         = sessionx.HandleChatPasteImage
	handleChatPastedImage        = sessionx.HandleChatPastedImage
	handleCreateSession          = sessionx.HandleCreateSession
	handleDismissSuggestedTitle  = sessionx.HandleDismissSuggestedTitle
	handleForkSession            = sessionx.HandleForkSession
	handleHaltSession            = sessionx.HandleHaltSession
	handleIdempotencyLookup      = sessionx.HandleIdempotencyLookup
	handleListArchived           = sessionx.HandleListArchived
	handleListSessions           = sessionx.HandleListSessions
	handlePasteImage             = sessionx.HandlePasteImage
	handlePastedImage            = sessionx.HandlePastedImage
	handleRecreateSession        = sessionx.HandleRecreateSession
	handleRepoLock               = sessionx.HandleRepoLock
	handleRestoreSession         = sessionx.HandleRestoreSession
	handleSSMLoginStatus         = sessionx.HandleSSMLoginStatus
	handleSessionAnswerQuestion  = sessionx.HandleSessionAnswerQuestion
	handleSessionCarriedAnswer   = sessionx.HandleSessionCarriedAnswer
	handleSessionCatalog         = sessionx.HandleSessionCatalog
	handleSessionCommittedFiles  = sessionx.HandleSessionCommittedFiles
	handleSessionDriver          = sessionx.HandleSessionDriver
	handleSessionHandoffContext  = sessionx.HandleSessionHandoffContext
	handleSessionHandoffProposal = sessionx.HandleSessionHandoffProposal
	handleSessionInput           = sessionx.HandleSessionInput
	handleSessionKeepAwake       = sessionx.HandleSessionKeepAwake
	handleSessionLock            = sessionx.HandleSessionLock
	handleSessionMarks           = sessionx.HandleSessionMarks
	handleSessionMessages        = sessionx.HandleSessionMessages
	handleSessionOutput          = sessionx.HandleSessionOutput
	handleSessionPlanRespond     = sessionx.HandleSessionPlanRespond
	handleSessionRenameBranch    = sessionx.HandleSessionRenameBranch
	handleSessionRespond         = sessionx.HandleSessionRespond
	handleSessionSettings        = sessionx.HandleSessionSettings
	handleSessionSettingsGet     = sessionx.HandleSessionSettingsGet
	handleSessionSkills          = sessionx.HandleSessionSkills
	handleSessionStatus          = sessionx.HandleSessionStatus
	handleSessionSuggestBranch   = sessionx.HandleSessionSuggestBranch
	handleSessionTurn            = sessionx.HandleSessionTurn
	handleSessionsCleanup        = sessionx.HandleSessionsCleanup
	handleSessionsUsage          = sessionx.HandleSessionsUsage
	handleSetTitle               = sessionx.HandleSetTitle
	handleStartSession           = sessionx.HandleStartSession
	handleStopSession            = sessionx.HandleStopSession
	handleSuggestReplies         = sessionx.HandleSuggestReplies
	handleSuggestTitle           = sessionx.HandleSuggestTitle
	handoffProposalPath          = sessionx.HandoffProposalPath
	hiddenModelsFor              = sessionx.HiddenModelsFor
	liveSessionsInDir            = sessionx.LiveSessionsInDir
	lockedRepoDirs               = sessionx.LockedRepoDirs
	lockedSessionsInDir          = sessionx.LockedSessionsInDir
	managedAlive                 = sessionx.ManagedAlive
	modelHidden                  = sessionx.ModelHidden
	modelMatchesHidden           = sessionx.ModelMatchesHidden
	normalizeKind                = sessionx.NormalizeKind
	peerReachableSessions        = sessionx.PeerReachableSessions
	promoteCarriedFor            = sessionx.PromoteCarriedFor
	readHandoffProposals         = sessionx.ReadHandoffProposals
	recordSessionNotification    = sessionx.RecordSessionNotification
	removeHandoffProposals       = sessionx.RemoveHandoffProposals
	removeSessionMarks           = sessionx.RemoveSessionMarks
	replySuggestEnabled          = sessionx.ReplySuggestEnabled
	replySuggestInstructions     = sessionx.ReplySuggestInstructions
	replySuggestLogHeader        = sessionx.ReplySuggestLogHeader
	replySuggestModel            = sessionx.ReplySuggestModel
	replySuggestPersona          = sessionx.ReplySuggestPersona
	replySuggestPrompt           = sessionx.ReplySuggestPrompt
	replySuggestWindow           = sessionx.ReplySuggestWindow
	repoLocked                   = sessionx.RepoLocked
	runSessionStatusHook         = sessionx.RunSessionStatusHook
	savePastedImageTo            = sessionx.SavePastedImageTo
	servePastedImageFrom         = sessionx.ServePastedImageFrom
	sessionAlive                 = sessionx.SessionAlive
	sessionIsShell               = sessionx.SessionIsShell
	sessionMarksPath             = sessionx.SessionMarksPath
	setRepoLock                  = sessionx.SetRepoLock
	shellCreateTarget            = sessionx.ShellCreateTarget
	shellSendTarget              = sessionx.ShellSendTarget
	ssmConfigPath                = sessionx.SsmConfigPath
	startAbortResumeWatch        = sessionx.StartAbortResumeWatch
	startBridgeReceiver          = sessionx.StartBridgeReceiver
	startRateLimitWatch          = sessionx.StartRateLimitWatch
	titleModel                   = sessionx.TitleModel
	titleSuggestFooter           = sessionx.TitleSuggestFooter
	titleSuggestInstructions     = sessionx.TitleSuggestInstructions
	titleSuggestPersona          = sessionx.TitleSuggestPersona
	usageTurns                   = sessionx.UsageTurns
	visibleModel                 = sessionx.VisibleModel
	visibleModelIDs              = sessionx.VisibleModelIDs
	worktreeHasSessions          = sessionx.WorktreeHasSessions
	writeSSMConfig               = sessionx.WriteSSMConfig
	writeSessionMetaKeepingLock  = sessionx.WriteSessionMetaKeepingLock
)
