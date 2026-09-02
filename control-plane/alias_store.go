package main

// store 家系は control-plane/internal/store へ移った（ADR 0067 ウェーブ A /
// track CP-STORE）。このファイルはその移送を呼び出し側から見えなくするための
// **エイリアス 1 枚**で、main の約 100 ファイルは 1 行も変わっていない。
//
// ⚠️ これは意図的な一時債務である。`grep alias_` で見えるのがその狙いで、
// ここを剥がす（呼び出し側を store.X 直参照に書き換える）作業はウェーブ境界の
// 別セッションが行う。ここに新しい名前を足すのは、store 側の公開面を増やすのと
// 同じ意味なので、足す前に本当に main から要るのかを確かめること。

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// AWS 資格情報の継ぎ目を store へ渡す。値ではなくクロージャで渡すのは、
// runtime_ecs.go の awsConfigFor がライブ E2E で差し替えられる変数であり、
// 呼び出し時に解決されないと store 側だけが継ぎ目の外に出るため
// （docs/log/64 §64.22.3）。
func init() {
	store.AWSConfigFor = func(ctx context.Context, region string) (aws.Config, error) {
		return awsConfigFor(ctx, region)
	}
}

// --- 型 ---------------------------------------------------------------------

type (
	AllowlistEntry         = store.AllowlistEntry
	AuditLog               = store.AuditLog
	AuditStore             = store.AuditStore
	CloudCostRow           = store.CloudCostRow
	CloudCostTotal         = store.CloudCostTotal
	EgressStat             = store.EgressStat
	EgressStore            = store.EgressStore
	GitRepo                = store.GitRepo
	GitRepoStore           = store.GitRepoStore
	Identity               = store.Identity
	IdentityLink           = store.IdentityLink
	LFSLock                = store.LFSLock
	LFSLockStore           = store.LFSLockStore
	LFSObjectStore         = store.LFSObjectStore
	MCPServerRow           = store.MCPServerRow
	MCPServerStore         = store.MCPServerStore
	MemberInfo             = store.MemberInfo
	Membership             = store.Membership
	MembershipStore        = store.MembershipStore
	MembershipView         = store.MembershipView
	Memo                   = store.Memo
	MemoCategory           = store.MemoCategory
	MemoStore              = store.MemoStore
	Notification           = store.Notification
	NotificationStore      = store.NotificationStore
	PAT                    = store.PAT
	PATStore               = store.PATStore
	SSMHost                = store.SSMHost
	SSMProfile             = store.SSMProfile
	SSMStore               = store.SSMStore
	Schedule               = store.Schedule
	ScheduleRun            = store.ScheduleRun
	ScheduleStore          = store.ScheduleStore
	SessionHandoffOffer    = store.SessionHandoffOffer
	SessionRow             = store.SessionRow
	SessionShare           = store.SessionShare
	SessionShareProposal   = store.SessionShareProposal
	SettingsStore          = store.SettingsStore
	SharedSessionCatalog   = store.SharedSessionCatalog
	Store                  = store.Store
	Tenant                 = store.Tenant
	TenantGitOAuth         = store.TenantGitOAuth
	TenantIdP              = store.TenantIdP
	TenantLoginRules       = store.TenantLoginRules
	TenantRef              = store.TenantRef
	TenantStore            = store.TenantStore
	UsageHourCounters      = store.UsageHourCounters
	UsageHourRow           = store.UsageHourRow
	UsageNotificationState = store.UsageNotificationState
	UsageRow               = store.UsageRow
	UserQuota              = store.UserQuota
	WorkItem               = store.WorkItem
	WorkItemQuery          = store.WorkItemQuery
	WorkItemSession        = store.WorkItemSession
	WorkItemStore          = store.WorkItemStore
	Workspace              = store.Workspace
	WorkspaceStore         = store.WorkspaceStore

	sqlStore = store.SQL
)

// --- 関数と sentinel error ---------------------------------------------------

var (
	openSQLite   = store.OpenSQLite
	openPostgres = store.OpenPostgres
	pgURLFromEnv = store.PGURLFromEnv
	newID        = store.NewID
	nowTS        = store.NowTS

	errIdentityClaimed       = store.ErrIdentityClaimed
	errLastLoginMethod       = store.ErrLastLoginMethod
	errLinkTaken             = store.ErrLinkTaken
	errNoSuchLoginMethod     = store.ErrNoSuchLoginMethod
	errSessionShareOwnerBusy = store.ErrSessionShareOwnerBusy
)
