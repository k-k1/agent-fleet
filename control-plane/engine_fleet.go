package main

// engine_fleet.go — the Control Plane buys an engine's box itself (ADR 0077 decisions 1, 3, 5).
//
// Until ADR 0075 the purchase was ECS's: a capacity provider held the instance requirements, the
// CP moved the service's strategy, and ECS went shopping when a task could not be placed. Three
// rounds on hardware showed the same defect three times — the buyer and the judge were different
// parties, so the CP had to INFER the outcome from service-event strings. It bought two boxes,
// read one offer's echo as the next offer's answer, and could not declare an offer unbuyable.
//
// Here one offer row is ONE synchronous call: `CreateFleet(type=instant, TotalTargetCapacity=1)`.
// The response carries either the instance that launched or the codes saying why it did not, so
// the budget clock, the event matching and the deployment gate all disappear at this one point.
//
// 🔴 Two invariants hold the cost down, and both are tested:
//
//   - a deployment that declares no offer — or no launch template — makes NOT ONE EC2 call. The
//     fleet is attached in newEngineRegistry only when both exist (ADR 0074 decision 3,
//     inherited through 0075);
//   - the CP must be able to find every box it bought from `describe-instances` TAGS ALONE, with
//     no memory (ADR 0045 decision 29). A box it can only reach through its own process state is
//     a box a restart leaks for ever, at GPU prices.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// engineFleetAPI is the narrow EC2 port, so a test can answer with a fleet of its own. The real
// *ec2.Client satisfies it.
//
// 🔴 It lives in package main because the engine code had no EC2 client at all before this ADR —
// the only one was `ec2API`, unexported in internal/runtime and built by the ecs-ec2 runtime
// alone (ADR 0077 review R9). An engine runs on every flavour, so this one is built on every
// flavour too, and what keeps a Fargate-only deployment from paying for that is the wiring
// (newEngineRegistry), not the client.
type engineFleetAPI interface {
	CreateFleet(context.Context, *ec2.CreateFleetInput, ...func(*ec2.Options)) (*ec2.CreateFleetOutput, error)
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
}

// ⚠️ THREE calls, and the two that ADR 0077 P1 also listed are deliberately absent. P0 measured
// both on hardware:
//
//   - `CreateTags` is not needed. `CreateFleet`'s `TagSpecifications` (ResourceType `instance`)
//     MERGES with the launch template's own tags and lands on the box, so the repair write the
//     draft wanted would be a second call that can only ever say the same thing;
//   - `DeleteFleets` / `DescribeFleets` cannot be used and do not need to be. An instant fleet
//     that launched nothing is refused with `NoTerminateInstancesNotSupported` when asked to be
//     deleted without terminating, and it does not appear in an unfiltered `describe-fleets`
//     either — so nothing accumulates on the CP's path and decision 1's "the CP does not
//     remember the fleet id" stands as written.

// The tags every engine box carries (decision 3). The first two are the slot pool's own words,
// imported rather than re-spelt: one vocabulary means one `describe-instances` answers "whose box
// is this" for slots and engines alike, and it is what `sweepSlotOwnerTags` filters on to leave
// an engine box alone.
const (
	engineTagOffer     = "af-engine-offer" // the offer row this box was bought for
	engineTagBuy       = "af-engine-buy"   // spot | od — what it was bought as
	engineTagManagedBy = "af-managed-by"
	engineManagedBy    = "agent-fleet"
)

// engineRoleAttr is the value of `af-role` for one engine role: the EC2 TAG the CP writes at
// purchase, and the ECS ATTRIBUTE the launch template's user data writes into
// `/etc/ecs/ecs.config`. Deliberately the same string on both sides — the box has to be
// recognisable from EC2 and from ECS, and two spellings would be two ways to lose one.
func engineRoleAttr(key string) string { return "engine-" + key }

// engineFleetBox is one box as EC2 reports it: enough to say whose it is, what it was bought for
// and whether it still exists.
type engineFleetBox struct {
	instanceID   string
	state        string // pending | running | shutting-down | stopping | stopped | terminated
	instanceType string
	offerID      string // af-engine-offer
	buy          string // af-engine-buy
	launchedAt   time.Time
}

// alive reports whether this box is still capable of costing money and of running a task.
func (b engineFleetBox) alive() bool {
	return b.state == "pending" || b.state == "running"
}

// engineFleet is one role's purchasing side: the launch template it buys from, the subnets it may
// buy in, and the tags that make the result findable again.
type engineFleet struct {
	api      engineFleetAPI
	key      string   // "image" — the role, and half of the af-role value
	pool     string   // the cluster name, i.e. the af-pool tag value
	template string   // the launch template, by id (lt-…) or by name
	subnets  []string // AF_ECS_SUBNETS, the same list the workspace side is given

	mu     sync.Mutex
	cached []engineFleetBox
	cachAt time.Time
	now    func() time.Time // test seam
}

func newEngineFleet(api engineFleetAPI, key, pool, template string, subnets []string) *engineFleet {
	if api == nil || strings.TrimSpace(template) == "" {
		// No launch template is a role that does not buy boxes (a Fargate engine, or a table
		// written before ADR 0077). Answering nil here is what makes "no EC2 call" structural
		// rather than a branch somebody has to remember.
		return nil
	}
	return &engineFleet{
		api: api, key: key, pool: pool, template: strings.TrimSpace(template),
		subnets: append([]string(nil), subnets...), now: time.Now,
	}
}

// setTemplate takes a replaced launch template live, reporting whether it changed. Like the
// capacity provider name it replaces, it is a destination string and nothing else — the next
// purchase goes to the new one and everything already bought is still found by tag.
func (f *engineFleet) setTemplate(ref string) bool {
	if f == nil {
		return false
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		// A row that dropped its launch template is a change this process cannot take live: the
		// fleet was attached at construction and dropping it would leave a box nobody buys and
		// nobody ends. The reloader says so and asks for a restart.
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.template == ref {
		return false
	}
	f.template = ref
	return true
}

// launchTemplate is the template a purchase goes to, now.
func (f *engineFleet) launchTemplate() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.template
}

func (f *engineFleet) clock() time.Time {
	if f == nil {
		return time.Time{}
	}
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

func (f *engineFleet) logKey() string {
	if f == nil || f.key == "" {
		return "engine"
	}
	return "engine " + f.key
}

// --- decision 8: what one CreateFleet error means ---------------------------------------

// The answers a failed purchase can have. The first three are what ADR 0075 measured on the
// service events, moved to the one place that now produces them; the fourth is what P0 found
// hiding inside a 200.
const (
	engineFleetNext     = "next"     // this offer cannot be had; try the next row
	engineFleetSkipBuy  = "skip_buy" // the whole purchase option is full; skip every row of it
	engineFleetUnusable = "unusable" // the request itself is wrong; it can never be bought
	// engineFleetRefused is the one answer that is not about the OFFER at all: the deployment
	// cannot launch anything (a missing grant). Walking the list would ask the same impossible
	// question once per row.
	engineFleetRefused = "refused"
)

// engineFleetOutcome is what one error code does to the walk, and what the panel records for the
// offer it happened to.
type engineFleetOutcome struct {
	action string
	result string
}

// engineFleetErrorCodes is decision 8's table: `Errors[].ErrorCode` → one of four answers. ONE
// data table rather than a chain of ifs, because the vocabulary is the part that will need
// editing and a reader has to be able to see the whole of it at once.
//
// 🔴 MEASURED on hardware by ADR 0077's P0 pass, not estimated: `CreateFleet` answers HTTP 200
// and puts one entry in `Errors[]` PER OVERRIDE, with `Instances[]` empty when nothing launched.
// Two entries are unmeasured and marked as such — `SpotMaxPriceTooLow` was never provoked, and
// "no stock" was measured through a stand-in — but both sit in the branch that costs the least
// if wrong (try the next row).
//
// The shape is inherited and was itself measured (ADR 0075 decision 5): no stock is worth moving
// on for, a quota is a wall every row of that purchase option shares — the two quotas are
// separate (`L-DB2E81BA` on-demand, `L-3819A6DF` Spot) — and a request nothing satisfies must be
// declared rather than retried.
var engineFleetErrorCodes = map[string]engineFleetOutcome{
	"MaxSpotInstanceCountExceeded": {engineFleetSkipBuy, engineOfferQuota},
	"VcpuLimitExceeded":            {engineFleetSkipBuy, engineOfferQuota},
	// 🔴 One word for two different mistakes, and P0 could not tell them apart: a MISSPELT
	// instance type and a type that is simply not offered in that Availability Zone come back
	// with the same code and the same message. So the reading is positional rather than
	// semantic — see engineFleetResponseVerdict: every override refused this way means the row
	// itself cannot be asked for, while some of them means the others are still worth having.
	"InvalidFleetConfiguration": {engineFleetUnusable, engineOfferUnusable},
	// Unmeasured (P0 could not provoke it); the cheapest branch to be wrong in.
	"SpotMaxPriceTooLow": {engineFleetNext, engineOfferUnfulfillable},
	// Measured through a stand-in rather than a real stock-out.
	"InsufficientInstanceCapacity": {engineFleetNext, engineOfferInsufficient},
	// 🔴 A MISSING GRANT ARRIVES INSIDE A 200. P0 measured `iam:PassRole` absent from the CP's
	// role: `CreateFleet` succeeds at the HTTP layer and writes `UnauthorizedOperation` into
	// `Errors[]`, in the same shape and the same place as "there was no stock". Reading it as
	// "try the next row" would walk the whole offer list against a deployment that cannot launch
	// anything, once per demand, for ever — so it stops the walk instead, counts as a failed
	// start and logs the message verbatim. A test pins that it is NOT next.
	"UnauthorizedOperation": {engineFleetRefused, engineOfferUnusable},
}

// engineFleetVerdict reads ONE error code. The second result is whether the code was recognised:
// an unknown one moves to the next offer AND says so in the log, because the alternative — giving
// up on the list — would turn a reworded AWS code into an engine that walks its whole offer list
// in one second and cools down for four hours (ADR 0075 decision 5, the same default).
func engineFleetVerdict(code string) (engineFleetOutcome, bool) {
	code = strings.TrimSpace(code)
	if out, ok := engineFleetErrorCodes[code]; ok {
		return out, true
	}
	// A wrapped code still counts. ECS wrapped `MaxSpotInstanceCountExceeded` inside a
	// `ResourceInitializationError` (measured, ADR 0075), and nothing says EC2 never does the
	// same; matching the substring costs one loop and cannot be wrong in the other direction.
	for known, out := range engineFleetErrorCodes {
		if strings.Contains(code, known) {
			return out, true
		}
	}
	return engineFleetOutcome{engineFleetNext, engineOfferUnfulfillable}, false
}

// engineFleetResponseVerdict folds one response's whole `Errors[]` list into a single answer.
//
// 🔴 The list is PER OVERRIDE (measured), so a three-type row can answer three different things
// at once and the fold is where the reading is decided:
//
//   - a refusal of the deployment itself wins over everything. It is not about this row;
//   - then a quota, because it is the one answer that takes other rows off the table too;
//   - `InvalidFleetConfiguration` is `unusable` only when EVERY override said it. One override
//     refused means one type (or one AZ) is unavailable and the rest of the row is still worth
//     asking for — and P0 could not tell "misspelt" from "not offered here" apart, so this
//     positional reading is the only honest one;
//   - otherwise the first recognised code, and "next" when nothing was recognised.
func engineFleetResponseVerdict(codes []string) (engineFleetOutcome, bool) {
	if len(codes) == 0 {
		return engineFleetOutcome{engineFleetNext, engineOfferUnfulfillable}, false
	}
	// `best` deliberately never holds an `unusable`: that one is decided by COUNTING, below.
	best, known, seen, unusable := engineFleetOutcome{}, false, false, 0
	for _, code := range codes {
		out, ok := engineFleetVerdict(code)
		seen = seen || ok
		switch {
		case out.action == engineFleetRefused:
			return out, true
		case out.action == engineFleetUnusable:
			unusable++
		case out.action == engineFleetSkipBuy:
			best, known = out, true
		case ok && !known:
			best, known = out, true
		}
	}
	if unusable == len(codes) {
		return engineFleetOutcome{engineFleetUnusable, engineOfferUnusable}, true
	}
	if !known {
		// Some overrides were refused and the others said nothing this table knows. The row as a
		// whole is not declared unusable on the strength of a minority.
		return engineFleetOutcome{engineFleetNext, engineOfferUnfulfillable}, seen
	}
	return best, true
}

// --- decision 1: one offer row, one instant fleet ---------------------------------------

// buy places one offer's request and answers with the instance that launched, or with what the
// walk is to do next.
//
// Everything that made this hard under Managed Instances is gone here: the call is SYNCHRONOUS,
// so "did this offer produce a box" is the return value rather than a string written into a
// service's event list minutes later, and a response belongs to its own call rather than to
// whichever offer was tried last.
func (f *engineFleet) buy(ctx context.Context, c engineClass) (string, engineFleetOutcome) {
	if f == nil {
		return "", engineFleetOutcome{engineFleetUnusable, engineOfferUnusable}
	}
	if len(f.subnets) == 0 {
		// Nothing to launch into. Refusing loudly beats an `InvalidParameterValue` per offer:
		// every row of the list would answer the same way and the whole list would be spent.
		log.Printf("%s: cannot buy a box: no subnet is configured (AF_ECS_SUBNETS)", f.logKey())
		return "", engineFleetOutcome{engineFleetUnusable, engineOfferUnusable}
	}
	in := f.request(c)
	out, err := f.api.CreateFleet(ctx, in)
	if err != nil {
		// The call itself failed — a throttle, a missing service-linked role
		// (`AWSServiceRoleForEC2Fleet` does not exist until something creates it), or the
		// top-level `SsmAccessDenied` P0 measured when the role could not resolve the AMI
		// parameter. An authorization failure is about the DEPLOYMENT and stops the walk; the
		// rest are not verdicts about this offer, so the next row gets its own chance.
		log.Printf("%s: buying %s (%s) failed: %v", f.logKey(), c.ID, c.buy(), err)
		if engineFleetDenied(err) {
			return "", engineFleetOutcome{engineFleetRefused, engineOfferUnusable}
		}
		return "", engineFleetOutcome{engineFleetNext, engineOfferUnfulfillable}
	}
	f.invalidate()
	if id := engineFleetInstanceID(out); id != "" {
		// The tags are already on it: `TagSpecifications` (ResourceType `instance`) merges with
		// the launch template's own and lands at launch (measured, P0). That is what lets a
		// restarted CP find this box at all (ADR 0045 decision 29).
		log.Printf("%s: offer %s (%s) bought %s", f.logKey(), c.ID, c.buy(), id)
		return id, engineFleetOutcome{}
	}
	codes := make([]string, 0, len(out.Errors))
	messages := make([]string, 0, len(out.Errors))
	for _, e := range out.Errors {
		code := strings.TrimSpace(aws.ToString(e.ErrorCode))
		codes = append(codes, code)
		messages = append(messages, code+": "+strings.TrimSpace(aws.ToString(e.ErrorMessage)))
	}
	verdict, known := engineFleetResponseVerdict(codes)
	switch {
	case verdict.action == engineFleetRefused:
		// 🔴 Verbatim, because this is the one outcome an operator has to act on and the message
		// names the grant that is missing.
		log.Printf("%s: offer %s could not be bought and the deployment cannot launch at all: %s",
			f.logKey(), c.ID, strings.Join(messages, " | "))
	case !known:
		// The one line that makes a reworded code findable. Without it the only symptom is an
		// engine that walks its whole list and cools down.
		log.Printf("%s: offer %s answered with no known fleet error code: %s",
			f.logKey(), c.ID, strings.Join(messages, " | "))
	default:
		log.Printf("%s: offer %s (%s) bought nothing: %s", f.logKey(), c.ID, c.buy(), strings.Join(messages, " | "))
	}
	return "", verdict
}

// request is one offer as EC2 Fleet reads it (decision 1).
//
//   - the overrides are the product "declared type × private subnet". The CP does not choose an
//     AZ: the slot pool's spreadAZs exists for a home volume's AZ affinity and an engine has no
//     home;
//   - `buy=spot` asks for `price-capacity-optimized` — the most available pools, then the
//     cheapest of those. ADR 0075 run 3's complaint (a three-type Spot row delivered a g6e, not
//     the cheapest g6) was Managed Instances having no allocation strategy at all;
//   - ⚠️ `buy=od` asks for `prioritized`, which reads each override's OWN `Priority` field and
//     NOT the order of the list. The declared order is written out as 1, 2, … for that reason,
//     and a test pins it (ADR 0077 done item 6).
func (f *engineFleet) request(c engineClass) *ec2.CreateFleetInput {
	overrides := make([]ec2types.FleetLaunchTemplateOverridesRequest, 0, len(c.Types)*len(f.subnets))
	priority := 1.0
	for _, t := range c.Types {
		for _, subnet := range f.subnets {
			o := ec2types.FleetLaunchTemplateOverridesRequest{
				InstanceType: ec2types.InstanceType(t),
				SubnetId:     aws.String(subnet),
			}
			if c.buy() == engineBuyOnDemand {
				o.Priority = aws.Float64(priority)
			}
			overrides = append(overrides, o)
			priority++
		}
	}
	in := &ec2.CreateFleetInput{
		// instant, and nothing else: `request` and `maintain` are asynchronous, which is the
		// whole defect this ADR removes (ADR 0077 rejected alternatives).
		Type: ec2types.FleetTypeInstant,
		LaunchTemplateConfigs: []ec2types.FleetLaunchTemplateConfigRequest{{
			LaunchTemplateSpecification: engineFleetTemplate(f.launchTemplate()),
			Overrides:                   overrides,
		}},
		TargetCapacitySpecification: &ec2types.TargetCapacitySpecificationRequest{
			TotalTargetCapacity:       aws.Int32(1),
			DefaultTargetCapacityType: ec2types.DefaultTargetCapacityTypeOnDemand,
		},
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags:         f.tags(c),
		}},
	}
	if c.buy() == engineBuySpot {
		in.TargetCapacitySpecification.DefaultTargetCapacityType = ec2types.DefaultTargetCapacityTypeSpot
		in.SpotOptions = &ec2types.SpotOptionsRequest{
			AllocationStrategy: ec2types.SpotAllocationStrategyPriceCapacityOptimized,
		}
	} else {
		in.OnDemandOptions = &ec2types.OnDemandOptionsRequest{
			AllocationStrategy: ec2types.FleetOnDemandAllocationStrategyPrioritized,
		}
	}
	return in
}

// engineFleetTemplate accepts either an id (lt-…) or a name, the way the slot pool's
// launchTemplateSpec does — and pins `$Latest` for the same reason: CloudFormation owns the
// template, and a version this process remembered would be the version of the day it started.
func engineFleetTemplate(ref string) *ec2types.FleetLaunchTemplateSpecificationRequest {
	lt := &ec2types.FleetLaunchTemplateSpecificationRequest{Version: aws.String("$Latest")}
	if strings.HasPrefix(ref, "lt-") {
		lt.LaunchTemplateId = aws.String(ref)
	} else {
		lt.LaunchTemplateName = aws.String(ref)
	}
	return lt
}

// tags are what makes a box findable with no memory (decision 3). The offer and the purchase
// option ride along because the panel answers "what is this engine running on" FROM THE BOX now,
// not from a service's strategy — and because the bill is grouped by the same words.
func (f *engineFleet) tags(c engineClass) []ec2types.Tag {
	return []ec2types.Tag{
		{Key: aws.String(runtime.EC2TagPool), Value: aws.String(f.pool)},
		{Key: aws.String(runtime.EC2TagRole), Value: aws.String(engineRoleAttr(f.key))},
		{Key: aws.String(engineTagOffer), Value: aws.String(c.ID)},
		{Key: aws.String(engineTagBuy), Value: aws.String(c.buy())},
		{Key: aws.String(engineTagManagedBy), Value: aws.String(engineManagedBy)},
		{Key: aws.String("Name"), Value: aws.String("af-engine-" + f.key)},
	}
}

// engineFleetDenied reports whether a failed call is an authorization problem, i.e. a fact about
// the deployment rather than about the offer. P0 measured two spellings of the same thing: a
// top-level `SsmAccessDenied` when the role cannot read the AMI parameter, and (inside a 200)
// `UnauthorizedOperation` when it cannot pass the instance role.
func engineFleetDenied(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "AccessDenied") || strings.Contains(msg, "UnauthorizedOperation")
}

// engineFleetInstanceID is the one instance an instant fleet launched, if it launched one.
func engineFleetInstanceID(out *ec2.CreateFleetOutput) string {
	if out == nil {
		return ""
	}
	for _, inst := range out.Instances {
		for _, id := range inst.InstanceIds {
			if id = strings.TrimSpace(id); id != "" {
				return id
			}
		}
	}
	return ""
}

// --- decision 3 and 5: finding and ending a box ------------------------------------------

// engineFleetTTL is the cache in front of DescribeInstances. Same length and the same reasoning
// as engineBoxTTL: this answers a question that moves in minutes, and the admin panel polls it
// every five seconds while an engine is starting.
const engineFleetTTL = 20 * time.Second

// boxes is every instance tagged for this role, cached. `terminated` ones are deliberately
// included: `draining` is now "from the CP's terminate until EC2 says terminated" (decision 5),
// and a box that has only just gone is the difference between that state and `stopped`.
func (f *engineFleet) boxes(ctx context.Context) []engineFleetBox {
	if f == nil {
		return nil
	}
	now := f.clock()
	f.mu.Lock()
	if !f.cachAt.IsZero() && now.Sub(f.cachAt) < engineFleetTTL {
		b := f.cached
		f.mu.Unlock()
		return b
	}
	f.mu.Unlock()

	b := f.describeBoxes(ctx)
	f.mu.Lock()
	f.cached, f.cachAt = b, now
	f.mu.Unlock()
	return b
}

func (f *engineFleet) invalidate() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.cachAt = time.Time{}
	f.mu.Unlock()
}

// describeBoxes is the uncached walk. Filtered on BOTH tags, so a workspace slot in the same pool
// is not this engine's box and the other role's GPU is not either.
func (f *engineFleet) describeBoxes(ctx context.Context) []engineFleetBox {
	var out []engineFleetBox
	var next *string
	for {
		page, err := f.api.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			Filters: []ec2types.Filter{
				{Name: aws.String("tag:" + runtime.EC2TagPool), Values: []string{f.pool}},
				{Name: aws.String("tag:" + runtime.EC2TagRole), Values: []string{engineRoleAttr(f.key)}},
			},
			NextToken: next,
		})
		if err != nil {
			log.Printf("%s: listing this role's boxes failed: %v", f.logKey(), err)
			// The whole answer is dropped, not the page: a partial one reads as "no box", which
			// is the claim that terminates nothing and starts a second GPU.
			return nil
		}
		for _, r := range page.Reservations {
			for _, inst := range r.Instances {
				b := engineFleetBox{
					instanceID:   aws.ToString(inst.InstanceId),
					instanceType: string(inst.InstanceType),
					offerID:      engineTagValue(inst.Tags, engineTagOffer),
					buy:          engineTagValue(inst.Tags, engineTagBuy),
				}
				if inst.State != nil {
					b.state = string(inst.State.Name)
				}
				if inst.LaunchTime != nil {
					b.launchedAt = *inst.LaunchTime
				}
				out = append(out, b)
			}
		}
		if next = page.NextToken; next == nil {
			return out
		}
	}
}

func engineTagValue(tags []ec2types.Tag, key string) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == key {
			return strings.TrimSpace(aws.ToString(t.Value))
		}
	}
	return ""
}

// terminate ends one box. The ECS half is the caller's (decision 5 keeps the slot pool's order:
// deregister first, so a failed terminate degrades into the ghost case the sweep already
// handles rather than into a live box ECS will not place on).
func (f *engineFleet) terminate(ctx context.Context, instanceID string) error {
	if f == nil || strings.TrimSpace(instanceID) == "" {
		return fmt.Errorf("no box to terminate")
	}
	_, err := f.api.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{instanceID}})
	f.invalidate()
	return err
}

// live is whether any box of this role still exists in a state that bills. It is what `draining`
// asks (decision 5): the service says "stopped" the moment its task goes, while the instance
// behind it lives on — and now that the CP is the one terminating, the fact is EC2's.
func (f *engineFleet) live(ctx context.Context) bool {
	for _, b := range f.boxes(ctx) {
		if b.alive() || b.state == "shutting-down" || b.state == "stopping" {
			return true
		}
	}
	return false
}

// current is the box the panel describes: the newest one that is still alive. More than one is
// not normal — a start buys exactly one — but an interruption's replacement and a terminate that
// has not finished can overlap, and the newest is the one the engine is on.
func (f *engineFleet) current(ctx context.Context) (engineFleetBox, bool) {
	var best engineFleetBox
	found := false
	for _, b := range f.boxes(ctx) {
		if !b.alive() {
			continue
		}
		if !found || b.launchedAt.After(best.launchedAt) {
			best, found = b, true
		}
	}
	return best, found
}
