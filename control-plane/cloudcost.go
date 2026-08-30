// cloudcost.go — the AWS invoice, attributed per member by cost allocation tag
// (docs/log/67, ADR 0048).
//
// This is NOT usage.go. That one samples workspace occupancy in seconds and exists on
// every runtime; this one reads real money out of Cost Explorer and exists only where
// there is a bill. Keeping them apart is the whole design decision: an estimate derived
// from "seconds × an operator-declared unit price" would go stale silently the first
// time instance types or prices changed, and then the Console would show two costs that
// disagree (決定 2).
//
// Three properties drive everything here:
//
//   - Cost Explorer charges $0.01 PER REQUEST. So the Console never calls it; a poller
//     writes days into cloud_cost_daily and every API reads the table (決定 7).
//   - Recent days are `Estimated` and keep moving for about a day. So the poller
//     re-fetches a trailing window and REPLACES those days rather than accumulating.
//   - Cost allocation has NO BACKFILL. Days before the tags were activated are not
//     "zero", they are unknowable — and the API has to be able to say which is which,
//     because a confident zero here is a lie that never corrects itself.
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// costMicro is the fixed-point scale for money. Amounts are integers of one millionth
// of the billing currency: Cost Explorer returns strings like "0.0002819758", and a
// month of per-member-per-service rows summed as float64 does not add up to the
// invoice. Money that does not add up is worse than no money at all.
const costMicro = 1_000_000

// ceTagMembership / ceTagTenant are the cost allocation tag keys, in the form Cost
// Explorer wants them for GroupBy (bare key) and returns them in results
// ("af-membership$<value>", with an empty value for everything untagged).
const (
	ceTagMembership = ec2TagMembership
	ceTagTenant     = ec2TagTenant
)

// costExplorerAPI is the one call this file makes, as a port so the poller is testable
// without AWS (and so nobody adds a second $0.01 call by accident).
type costExplorerAPI interface {
	GetCostAndUsage(context.Context, *costexplorer.GetCostAndUsageInput, ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error)
	// The two below keep the cost allocation tags switched on (cost_tags.go). They are
	// on the same port because they share the client, the cadence and the failure mode:
	// no permission here looks exactly like "this deployment spent nothing".
	ListCostAllocationTags(context.Context, *costexplorer.ListCostAllocationTagsInput, ...func(*costexplorer.Options)) (*costexplorer.ListCostAllocationTagsOutput, error)
	UpdateCostAllocationTagsStatus(context.Context, *costexplorer.UpdateCostAllocationTagsStatusInput, ...func(*costexplorer.Options)) (*costexplorer.UpdateCostAllocationTagsStatusOutput, error)
}

// cloudCostPoller pulls a trailing window out of Cost Explorer on a slow tick and lands
// it in the store. Modelled on usageSampler next door, with one deliberate difference:
// this one costs money to run, so the interval is hours and the window is small.
type cloudCostPoller struct {
	mgr      *manager
	ce       costExplorerAPI
	interval time.Duration
	// window is how many trailing days are re-fetched each run. It has to cover Cost
	// Explorer's restatement lag (~24h) with room to spare, but every extra day is
	// pure cost in the same single request, so it is days and not weeks.
	window int
	now    func() time.Time
	// lastErr is what the API reports when the poller cannot reach CE at all — an
	// AccessDenied here means the deployment never enabled billing access for IAM
	// roles, and the Console must say that instead of drawing an empty chart.
	lastErr atomic.Value // string
	lastRun atomic.Value // string
	// tagState is the last cost-allocation-tag activation result (costTagState).
	// Read by the API so the Console can say "this axis is not switched on yet"
	// instead of drawing a zero.
	tagState atomic.Value // costTagState
}

// lastError is the poller's most recent failure, or "". Read by the API so an
// AccessDenied (the account never enabled IAM access to billing) reaches the screen
// instead of looking like "nothing was spent".
func (p *cloudCostPoller) lastError() string {
	v, _ := p.lastErr.Load().(string)
	return v
}

func newCloudCostPoller(mgr *manager, ce costExplorerAPI, interval time.Duration, window int) *cloudCostPoller {
	return &cloudCostPoller{mgr: mgr, ce: ce, interval: interval, window: window, now: time.Now}
}

// startCloudCostPoller attaches the poller when this deployment has an AWS bill, and
// does nothing otherwise. The gate is the runtime's own declaration (CostProfile), not
// an operator switch: a docker deployment has no invoice, and there is nothing to
// configure about that.
//
// ⚠️ Cost Explorer is a GLOBAL service and only answers in us-east-1, regardless of
// where the workspaces run. Pointing it at the deployment region returns nothing and
// looks exactly like "no spend".
//
// ⚠️ No opt-out env. ADR 0044 決定 3 is the precedent: a feature shipped off by default
// never fired once. The cost here is ~$1.2/month of Cost Explorer requests, and a
// deployment that does not want the CP reading its bill withholds the IAM permission —
// which the poller reports rather than hides.
func startCloudCostPoller(ctx context.Context, mgr *manager) {
	if !mgr.cloudCostProfile().Available {
		return
	}
	iv := parseDurationOr(os.Getenv("AF_CLOUD_COST_INTERVAL"), 6*time.Hour)
	if iv <= 0 {
		log.Printf("cloud cost: disabled (AF_CLOUD_COST_INTERVAL=0)")
		return
	}
	ac, err := awsConfigFor(ctx, "us-east-1")
	if err != nil {
		log.Printf("cloud cost: no AWS config, cost view will stay empty: %v", err)
		return
	}
	p := newCloudCostPoller(mgr, costexplorer.NewFromConfig(ac), iv, envInt("AF_CLOUD_COST_WINDOW_DAYS", 7))
	mgr.costPoller = p
	go p.run(ctx)
}

func (p *cloudCostPoller) run(ctx context.Context) {
	log.Printf("cloud cost poller: interval=%s window=%dd (Cost Explorer costs $0.01/request)", p.interval, p.window)
	// One immediate pass so a fresh CP does not show an empty screen for six hours.
	p.pollOnce(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *cloudCostPoller) pollOnce(ctx context.Context) {
	// Before reading the bill, make sure the axes are switched on. Not a one-shot at
	// boot: AWS cannot activate a key it has not discovered yet, so this retries on the
	// same tick until each one lands (cost_tags.go).
	p.ensureCostTagsActive(ctx)
	rows, days, err := p.fetch(ctx)
	p.lastRun.Store(p.now().UTC().Format(time.RFC3339))
	if err != nil {
		p.lastErr.Store(err.Error())
		log.Printf("cloud cost: %v", err)
		return
	}
	p.lastErr.Store("")
	// 活性化状態が読めなかったとき（＝ payer にしか触れない linked アカウント）は、
	// いま引いたこの結果そのものが証拠になる（cost_tags.go の noteAttribution）。
	p.noteAttribution(rows)
	if err := p.mgr.store.PutCloudCost(ctx, days, rows); err != nil {
		log.Printf("cloud cost: storing %d rows: %v", len(rows), err)
		return
	}
	log.Printf("cloud cost: %d rows over %d days", len(rows), len(days))
}

// fetch asks Cost Explorer for the trailing window in ONE request, grouped by
// (af-membership, service).
//
// Two axes is Cost Explorer's hard maximum for GroupBy, and this is the pair that
// answers both questions at once: who, and what for. Both metrics come back in the same
// request too — extra metrics are free, extra requests are not.
//
// ⚠️ `End` is EXCLUSIVE in the Cost Explorer API. Passing today as End means today is
// not in the answer; the window therefore runs to tomorrow so the (estimated, moving)
// current day is included and refreshed on every pass.
func (p *cloudCostPoller) fetch(ctx context.Context) ([]CloudCostRow, []string, error) {
	now := p.now().UTC()
	start := now.AddDate(0, 0, -(p.window - 1)).Format(usageDayFmt)
	end := now.AddDate(0, 0, 1).Format(usageDayFmt)

	tenants, err := p.tenantByMembership(ctx)
	if err != nil {
		return nil, nil, err
	}
	// 予約メンバーシップ（種と probe）は共有インフラ扱いにする。引けなければ畳まない
	// だけで、取り込み自体は続ける。
	system, err := p.mgr.systemMembershipIDs(ctx)
	if err != nil {
		log.Printf("cloud cost: system memberships could not be resolved, not folding: %v", err)
	}

	var rows []CloudCostRow
	seen := map[string]bool{}
	var page *string
	for {
		out, err := p.ce.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
			TimePeriod:    &cetypes.DateInterval{Start: aws.String(start), End: aws.String(end)},
			Granularity:   cetypes.GranularityDaily,
			Metrics:       []string{"UnblendedCost", "AmortizedCost"},
			NextPageToken: page,
			GroupBy: []cetypes.GroupDefinition{
				{Type: cetypes.GroupDefinitionTypeTag, Key: aws.String(ceTagMembership)},
				{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String("SERVICE")},
			},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("cost explorer (%s..%s): %w", start, end, err)
		}
		for _, r := range out.ResultsByTime {
			day := aws.ToString(r.TimePeriod.Start)
			seen[day] = true
			estimated := r.Estimated
			for _, g := range r.Groups {
				row, ok := costRowFrom(day, estimated, g, tenants)
				if ok {
					rows = append(rows, row)
				}
			}
		}
		if out.NextPageToken == nil || aws.ToString(out.NextPageToken) == "" {
			break
		}
		page = out.NextPageToken
	}
	days := make([]string, 0, len(seen))
	for d := range seen {
		days = append(days, d)
	}
	return foldSystemMemberships(rows, system), days, nil
}

// foldSystemMemberships moves the reserved memberships' spend into the SHARED bucket
// (ADR 0048 決定 12). The golden bake's seed and probe are built by the product's ordinary
// Start path, so their workspaces carry `af-membership` like anybody's — and they are not
// anybody. Their money is the deployment keeping its own snapshot warm.
//
// ★ Why the tag is not simply left off at creation instead: `af-membership` is not only a
// cost allocation key, it is the MATCHING key the runtime uses to find a membership's EFS
// access point and home volume again (runtime_ecs_ec2.go の ensureAccessPoint など).
// An empty value there would either fail to match or collide with the next untagged
// resource. So the tag is written exactly as the product writes it, and the fold happens
// here, at ingest.
//
// ⚠️ The sum is NOT optional. PutCloudCost replaces `(day, membership_id, service)`
// wholesale (ON CONFLICT ... DO UPDATE SET unblended=excluded.unblended), so once two
// groups fold onto the same key, the second write would DELETE the first one's money
// rather than add to it. Cost Explorer hands us the seed and the untagged shared line as
// two separate groups of the same (day, service) all the time.
func foldSystemMemberships(rows []CloudCostRow, system map[string]bool) []CloudCostRow {
	if len(system) == 0 {
		return rows
	}
	type key struct{ day, membership, service string }
	out := make([]CloudCostRow, 0, len(rows))
	at := map[key]int{}
	for _, row := range rows {
		if system[row.MembershipID] {
			row.MembershipID = ""
			row.TenantID = "" // 共有バケットにテナントは無い（テナント別画面に出さない）
		}
		k := key{row.Day, row.MembershipID, row.Service}
		if i, ok := at[k]; ok {
			out[i].Unblended += row.Unblended
			out[i].Amortized += row.Amortized
			// 片方でも未確定なら合計も未確定。
			out[i].Estimated = out[i].Estimated || row.Estimated
			continue
		}
		at[k] = len(out)
		out = append(out, row)
	}
	return out
}

// costRowFrom turns one Cost Explorer group into a stored row, or reports that it is not
// worth storing.
//
// The group keys arrive as ["af-membership$<value>", "<Service>"]. An untagged line item
// yields "af-membership$" with nothing after the separator — that is the SHARED bucket,
// and it is the majority of the bill, so it is stored deliberately rather than dropped.
//
// Zero-amount groups are skipped: Cost Explorer returns a row for every service it knows
// about, most of them 0, and keeping them would bloat the table and the response for no
// information.
func costRowFrom(day string, estimated bool, g cetypes.Group, tenants map[string]string) (CloudCostRow, bool) {
	if len(g.Keys) < 2 {
		return CloudCostRow{}, false
	}
	membership := strings.TrimPrefix(g.Keys[0], ceTagMembership+"$")
	if membership == g.Keys[0] {
		// Not the tag key we asked for — refuse rather than guess. A silently
		// mis-parsed key would attribute everyone's cost to one fictional member.
		return CloudCostRow{}, false
	}
	unblended, currency := costAmount(g.Metrics["UnblendedCost"])
	amortized, _ := costAmount(g.Metrics["AmortizedCost"])
	if unblended == 0 && amortized == 0 {
		return CloudCostRow{}, false
	}
	return CloudCostRow{
		Day: day, MembershipID: membership, TenantID: tenants[membership],
		Service: g.Keys[1], Unblended: unblended, Amortized: amortized,
		Currency: currency, Estimated: estimated,
	}, true
}

// costAmount parses one Cost Explorer metric into micro-units of its own currency.
// The currency is passed through verbatim and never converted (決定 6) — a converted
// number is no longer the invoice, and the rate would have to come from somewhere that
// nobody would remember to update.
func costAmount(m cetypes.MetricValue) (int64, string) {
	v, err := strconv.ParseFloat(aws.ToString(m.Amount), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, aws.ToString(m.Unit)
	}
	return int64(math.Round(v * costMicro)), aws.ToString(m.Unit)
}

// tenantByMembership maps each membership to its tenant so a stored row can be scoped
// without a join at read time.
//
// ⚠️ This resolves what the CP knows TODAY. A membership deleted since the spend
// happened has no tenant here, so its money lands with an empty tenant_id and stops
// appearing in tenant-scoped views — correct, if unobvious: that spend is no longer
// anyone's to see, and inventing a tenant for it would put a stranger's cost into a
// tenant_admin's screen.
func (p *cloudCostPoller) tenantByMembership(ctx context.Context) (map[string]string, error) {
	tenants, err := p.mgr.store.ListTenants(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	out := map[string]string{}
	for _, t := range tenants {
		wss, err := p.mgr.store.ListWorkspaces(ctx, t.ID)
		if err != nil {
			return nil, fmt.Errorf("list workspaces (%s): %w", t.Slug, err)
		}
		for _, ws := range wss {
			out[ws.MembershipID] = ws.TenantID
		}
	}
	return out, nil
}

// --- API ---

// cloudCostMeta is what every cost response carries so the reader can tell an honest
// zero from a gap. None of it is decoration: without `first_day` a member looking at
// last month sees $0.00 and concludes their workspace was free.
type cloudCostMeta struct {
	Currency string `json:"currency"`
	// FirstDay / LastDay is the coverage that actually exists in the store. Anything
	// before FirstDay predates cost allocation being switched on and can never be
	// recovered — activation is not retroactive.
	FirstDay string `json:"first_day,omitempty"`
	LastDay  string `json:"last_day,omitempty"`
	// Estimated marks that the window includes days Cost Explorer has not finalised.
	Estimated bool `json:"estimated"`
	// LagHours is the delay to state next to the numbers rather than in a footnote.
	LagHours int `json:"lag_hours"`
	// Error is a poller failure surfaced to the reader — an AccessDenied means the
	// account never enabled billing access for IAM roles, which looks exactly like
	// "we spent nothing" if it is swallowed.
	Error string `json:"error,omitempty"`
	// Profile lets one response answer "should this screen exist" too.
	Profile costProfile `json:"profile"`
	// Tags is which cost allocation axes are actually switched on. A key still
	// `pending` means AWS has not discovered it yet and spend on that axis is missing
	// — and will stay missing, because activation is not retroactive.
	Tags costTagState `json:"tags"`
}

const cloudCostLagHours = 24

func (a adminAPI) cloudCostMeta(ctx context.Context, rows []CloudCostRow) cloudCostMeta {
	m := cloudCostMeta{LagHours: cloudCostLagHours, Profile: a.mgr.cloudCostProfile()}
	if first, last, err := a.mgr.store.CloudCostDays(ctx); err == nil {
		m.FirstDay, m.LastDay = first, last
	}
	if a.mgr.costPoller != nil {
		m.Error = a.mgr.costPoller.lastError()
		m.Tags = a.mgr.costPoller.costTags()
	}
	for _, r := range rows {
		if r.Currency != "" {
			m.Currency = r.Currency
		}
		if r.Estimated {
			m.Estimated = true
		}
	}
	return m
}

// cloudCostDay is one point on the member's chart.
type cloudCostDay struct {
	Day       string `json:"day"`
	Unblended int64  `json:"unblended_micro"`
	Estimated bool   `json:"estimated"`
}

// cloudCostService is one row of the "what for" breakdown.
type cloudCostService struct {
	Service   string `json:"service"`
	Unblended int64  `json:"unblended_micro"`
}

// myCloudCost (GET /api/cost/me?from=&to=) — the signed-in member's OWN attributed
// spend, and nothing else.
//
// ⚠️ This number is NOT "what this person costs". It is the spend that carries their
// membership tag, which on the reference deployment is about a fifth of the bill; the
// shared majority (NAT, DNS, load balancer, database, idle pool) is not divided and is
// not included. The Console is required to label it that way (ADR 0048 決定 4), and the
// response deliberately does not expose a deployment total that could be subtracted to
// infer anyone else's.
func (a adminAPI) myCloudCost(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	a.oneMemberCloudCost(w, r, mv.MembershipID)
}

// memberCloudCost (GET /api/admin/tenants/{slug}/members/{key}/cost?from=&to=) — one
// member's attributed spend, for the admin looking at that member's detail page.
//
// It answers a question the per-member list cannot: the list carries a total per member
// and nothing else, so "is this person's slot running every day including weekends" and
// "is it the home volume or the slot hours" are not derivable from it. Those two
// readings are the reason this belongs next to the stop / disk-quota buttons.
//
// ⚠️ Same body as /api/cost/me on purpose — one aggregation, one shape, so the Console
// can render both with the same component and the two can never drift apart.
//
// ⚠️ The store lookup is scoped by MEMBERSHIP ONLY, deliberately: tenantAdminFor plus
// resolveMember have already proved this member belongs to this tenant, and passing the
// tenant as well would hide spend whose row lost its tenant_id — which happens to anyone
// whose workspace was destroyed (tenantByMembership resolves what exists TODAY). That
// would put a confident $0.00 on the screen of a member who did spend.
func (a adminAPI) memberCloudCost(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.tenantAdminFor(w, r, r.PathValue("slug")); !ok {
		return
	}
	mem, _, _, aerr := a.resolveMember(r, r.PathValue("slug"), r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.oneMemberCloudCost(w, r, mem.ID)
}

// oneMemberCloudCost is the shared body of the two endpoints above: the days, the
// services and the total for exactly one membership.
//
// ⚠️ Rows for the SHARED bucket carry an empty membership_id, so filtering by a real
// membership excludes them by construction. That is what keeps a tenant_admin from
// seeing the deployment's own infrastructure bill here (ADR 0048 決定 4) — it is not a
// field this handler has to remember to omit.
func (a adminAPI) oneMemberCloudCost(w http.ResponseWriter, r *http.Request, membershipID string) {
	from, to, aerr := usageRange(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	rows, err := a.mgr.store.ListCloudCost(r.Context(), "", membershipID, from, to)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	byDay := map[string]*cloudCostDay{}
	byService := map[string]int64{}
	var total int64
	for _, row := range rows {
		d, ok := byDay[row.Day]
		if !ok {
			d = &cloudCostDay{Day: row.Day}
			byDay[row.Day] = d
		}
		d.Unblended += row.Unblended
		d.Estimated = d.Estimated || row.Estimated
		byService[row.Service] += row.Unblended
		total += row.Unblended
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from, "to": to,
		"total_micro": total,
		"days":        sortedCostDays(byDay),
		"services":    sortedCostServices(byService),
		"meta":        a.cloudCostMeta(r.Context(), rows),
	})
}

// cloudCost (GET /api/admin/cloud-cost?from=&to=&tenant=) — per-member totals for an
// admin.
//
// ⚠️ The SHARED bucket is super_admin only. It is the deployment's own infrastructure
// bill (ALB, RDS, Route53, NAT, the CP's own task), which is information about the
// deployment rather than about a tenant — handing it to a tenant_admin would be reading
// outside their tenant, the same line ADR 0043 決定 24/25 draws.
//
// ★ 予約メンバーシップ（system_tenant.go）の分もここで SHARED に足す。取り込み側
// （fetch）は既に畳んでいるが、畳むようになったのは窓（既定 7 日）の中だけで、それより
// 古い行は membership 付きのまま残っている。この 1 行が無いと、過去の月を見たときだけ
// 「af-golden-seed」という人がメンバー一覧に現れる。
func (a adminAPI) cloudCost(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := a.tenantScope(w, r)
	if !ok {
		return
	}
	from, to, aerr := usageRange(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	totals, err := a.mgr.store.CloudCostTotals(r.Context(), tenantID, from, to)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// 引けなければ畳まないだけ（費用画面を落とすほどの話ではない）。
	system, _ := a.mgr.systemMembershipIDs(r.Context())
	deploymentWide := tenantID == "" // tenantScope already proved super_admin for this
	members := make([]map[string]any, 0, len(totals))
	var sharedMicro int64
	var sharedSeen bool
	var attributed int64
	for i := range totals {
		t := totals[i]
		if t.MembershipID == "" || system[t.MembershipID] {
			sharedMicro += t.Unblended
			sharedSeen = true
			continue
		}
		attributed += t.Unblended
		members = append(members, map[string]any{
			"tenant": t.TenantSlug, "membership_id": t.MembershipID,
			"user_key": t.UserKey, "email": t.Email,
			"unblended_micro": t.Unblended, "amortized_micro": t.Amortized,
		})
	}
	rows, err := a.mgr.store.ListCloudCost(r.Context(), tenantID, "", from, to)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	resp := map[string]any{
		"from": from, "to": to,
		"members":          members,
		"attributed_micro": attributed,
		"meta":             a.cloudCostMeta(r.Context(), rows),
	}
	if deploymentWide && sharedSeen {
		byService := map[string]int64{}
		sharedRows, err := a.mgr.store.ListCloudCost(r.Context(), "", "", from, to)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		for _, row := range sharedRows {
			if row.MembershipID == "" || system[row.MembershipID] {
				byService[row.Service] += row.Unblended
			}
		}
		resp["shared_micro"] = sharedMicro
		resp["shared_services"] = sortedCostServices(byService)
	}
	writeJSON(w, http.StatusOK, resp)
}

func sortedCostDays(byDay map[string]*cloudCostDay) []cloudCostDay {
	out := make([]cloudCostDay, 0, len(byDay))
	for _, d := range byDay {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

func sortedCostServices(byService map[string]int64) []cloudCostService {
	out := make([]cloudCostService, 0, len(byService))
	for s, v := range byService {
		out = append(out, cloudCostService{Service: s, Unblended: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Unblended > out[j].Unblended })
	return out
}
