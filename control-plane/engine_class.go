package main

// engine_class.go — which GPU an engine buys, chosen at runtime (ADR 0074).
//
// ADR 0071 put the box in CloudFormation: a role's capacity provider carries
// `AllowedInstanceTypes` and a VRAM floor, so the card is fixed at deployment. ADR 0072 then
// made the MODEL editable from the Console, which left the two halves out of step — a 23.8 GB
// checkpoint can be taken in with three clicks and lands on a 24 GB card that cannot hold it.
//
// So the box becomes a choice from a LADDER the operator declares (decision 1). Three
// properties are worth keeping in mind when editing this file:
//
//   - the CP asks nobody what a rung means. Neither EC2 nor the Pricing API is called; the
//     numbers are the operator's, exactly as the workspace slot ladder's are (ADR 0045
//     decision 21). A rung nobody declared does not exist;
//   - a change reaches the NEXT box only ("These changes only apply to new Amazon ECS Managed
//     Instances", the API's own words), which is why a switch is stop → wait for the box to
//     leave → start rather than an update;
//   - with no ladder declared, nothing here calls AWS at all. A deployment that never
//     configures this must not start logging AccessDenied for a feature it does not use.

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineClass is one rung: which boxes this role may buy, and how much VRAM the operator says
// they have.
//
// VramMiB does two jobs on purpose (ADR 0074 decision 1). It is the
// `AcceleratorTotalMemoryMiB.Min` handed to the capacity provider — i.e. the filter that
// decides which cards qualify — and it is what a model's demand is compared against. It is
// declared BELOW the card's nominal size (21000 for an L4's 24 GB, as the stack already did),
// so the comparison errs toward warning early rather than late.
//
// UsdPerHour is display-only and optional. Nothing computes with it, and 0 means "not
// declared", which the panel renders as no figure at all rather than as free.
type engineClass struct {
	ID        string
	Label     string
	VramMiB   int
	Types     []string
	VCpuMin   int32
	VCpuMax   int32
	MemMinMiB int32
	MemMaxMiB int32
	// UsdPerHour is what the operator says an hour of this rung costs. 0 = undeclared.
	UsdPerHour float64
}

// label is what a person reads. Falls back to the id, never to an empty string: a select with
// a blank option is a control with no way to tell the rungs apart.
func (c engineClass) label() string {
	if s := strings.TrimSpace(c.Label); s != "" {
		return s
	}
	return c.ID
}

// parseEngineClasses reads the ladder one role declares:
//
//	id|label|vramMiB|type[,type…]|vcpuMin-vcpuMax|memMinMiB-memMaxMiB|usdPerHour
//
// separated by ";" or a newline (a CloudFormation parameter is one line; an env file is easier
// to read over several). The last field is optional and so is its whole column.
//
// A malformed rung is DROPPED with a log line rather than defaulted. Every field here becomes
// an instance requirement, and a rung that quietly lost its VRAM floor would buy a cheaper card
// than the operator asked for — which is invisible until a model fails to load on it.
func parseEngineClasses(spec string) []engineClass {
	var out []engineClass
	seen := map[string]bool{}
	for _, entry := range strings.FieldsFunc(spec, func(r rune) bool { return r == ';' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "|")
		if len(parts) < 6 {
			log.Printf("engines: ignoring instance class %q (want id|label|vramMiB|types|vcpuMin-vcpuMax|memMinMiB-memMaxMiB[|usdPerHour])", entry)
			continue
		}
		c := engineClass{
			ID:    strings.TrimSpace(parts[0]),
			Label: strings.TrimSpace(parts[1]),
		}
		if c.ID == "" {
			log.Printf("engines: ignoring instance class %q: no id", entry)
			continue
		}
		if seen[c.ID] {
			// Two rungs of the same id would make "which one is selected" unanswerable, and
			// the setting stores the id.
			log.Printf("engines: ignoring instance class %q: duplicate id %s", entry, c.ID)
			continue
		}
		vram, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil || vram < 0 {
			log.Printf("engines: ignoring instance class %q: vramMiB %q is not a number", entry, parts[2])
			continue
		}
		c.VramMiB = vram
		for _, t := range strings.Split(parts[3], ",") {
			if t = strings.TrimSpace(t); t != "" {
				c.Types = append(c.Types, t)
			}
		}
		if len(c.Types) == 0 {
			log.Printf("engines: ignoring instance class %q: no instance type", entry)
			continue
		}
		var ok bool
		if c.VCpuMin, c.VCpuMax, ok = parseEngineClassRange(parts[4]); !ok {
			log.Printf("engines: ignoring instance class %q: vcpu range %q is not min-max", entry, parts[4])
			continue
		}
		if c.MemMinMiB, c.MemMaxMiB, ok = parseEngineClassRange(parts[5]); !ok {
			log.Printf("engines: ignoring instance class %q: memory range %q is not min-max", entry, parts[5])
			continue
		}
		// The price is the one field a typo must not cost a rung: it is a label, and dropping
		// the rung over it would take the hardware away to protect a number nothing computes
		// with (the same rule the slot ladder's optional vCPU follows).
		if len(parts) > 6 {
			if usd, err := strconv.ParseFloat(strings.TrimSpace(parts[6]), 64); err == nil && usd > 0 {
				c.UsdPerHour = usd
			} else if strings.TrimSpace(parts[6]) != "" {
				log.Printf("engines: instance class %s: ignoring the price %q", c.ID, parts[6])
			}
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	return out
}

// parseEngineClassRange reads "4-8". A single number means both ends, so a rung pinned to one
// size can say so without repeating itself.
func parseEngineClassRange(s string) (int32, int32, bool) {
	s = strings.TrimSpace(s)
	lo, hi, found := strings.Cut(s, "-")
	if !found {
		hi = lo
	}
	a, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil || a <= 0 {
		return 0, 0, false
	}
	b, err := strconv.Atoi(strings.TrimSpace(hi))
	if err != nil || b < a {
		return 0, 0, false
	}
	return int32(a), int32(b), true
}

// engineClassByID finds a rung. The empty id answers the DEFAULT — the first rung, which is the
// one the stack lists first — so "nothing chosen yet" and "chose the default" resolve the same
// way and no second parameter has to declare it.
func engineClassByID(list []engineClass, id string) (engineClass, bool) {
	if len(list) == 0 {
		return engineClass{}, false
	}
	if id = strings.TrimSpace(id); id == "" {
		return list[0], true
	}
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	return engineClass{}, false
}

// The three answers to "how much VRAM does this model need". Which one was used travels with
// the number, because they are not equally strong and the panel must not print them alike.
const (
	// engineVramDeclared is the operator's own measurement (engine_models.vram_mib).
	engineVramDeclared = "declared"
	// engineVramFloor is the sum of the files' declared bytes: the WEIGHTS and nothing else.
	// No KV cache, no context, no CUDA context — so it can only ever say "at least this much".
	engineVramFloor = "floor"
	// engineVramUnknown is nobody declared either. 🔴 It is NOT zero, and it must never be
	// drawn as "this fits": a 0 here means the question was not answered.
	engineVramUnknown = "unknown"
)

// engineModelVramNeed is what one model wants in VRAM, and how well that is known.
func engineModelVramNeed(m store.EngineModel) (int, string) {
	if m.VramMiB > 0 {
		return m.VramMiB, engineVramDeclared
	}
	var total int64
	for _, f := range m.Files {
		total += f.Bytes
	}
	if total > 0 {
		return int(total / (1024 * 1024)), engineVramFloor
	}
	return 0, engineVramUnknown
}

// engineVramDemand is the largest demand among the models an engine would load, the id it
// belongs to, and how it was answered.
//
// The MAXIMUM, not the sum, and that is the whole reason this is a function rather than a loop
// at the call site: the llm router runs `--models-max 1` and sd-server holds one checkpoint, so
// what has to fit is one model at a time. Summing would report a deployment with five 8 GB
// models as needing 40 GB and warn about every start.
//
// An `unknown` model does not lower the demand and does not raise it either: the number
// returned is the largest KNOWN one, and the source is `unknown` when anything in the set could
// not be answered — so the panel can say "at least X, and one model did not say".
func engineVramDemand(rows []store.EngineModel) (int, string, string) {
	worst, id, source := 0, "", ""
	anyUnknown, anyFloor := false, false
	for _, m := range rows {
		if !m.Enabled || engineModelIsLora(m) {
			continue
		}
		need, src := engineModelVramNeed(m)
		switch src {
		case engineVramUnknown:
			anyUnknown = true
			continue
		case engineVramFloor:
			anyFloor = true
		}
		if need > worst {
			worst, id, source = need, m.ID, src
		}
	}
	switch {
	case worst == 0:
		return 0, engineVramUnknown, ""
	case anyUnknown || (anyFloor && source == engineVramDeclared):
		// Mixed evidence reads as the weaker of the two: the largest KNOWN demand is a floor
		// when something in the set is unmeasured, whatever the winning row happened to carry.
		return worst, engineVramFloor, id
	default:
		return worst, source, id
	}
}

// engineClassFits reports whether the demand fits the rung, and is deliberately generous with
// the unknown: `false` here asks a human a question, and asking it about every model nobody has
// measured would train people to click through the one that matters.
func engineClassFits(c engineClass, needMiB int) bool {
	return c.VramMiB <= 0 || needMiB <= 0 || needMiB <= c.VramMiB
}

// engineCapacityAPI is the narrow ECS port for reading and re-declaring a Managed Instances
// capacity provider, so a test can answer with a provider of its own.
type engineCapacityAPI interface {
	DescribeCapacityProviders(context.Context, *ecs.DescribeCapacityProvidersInput, ...func(*ecs.Options)) (*ecs.DescribeCapacityProvidersOutput, error)
	UpdateCapacityProvider(context.Context, *ecs.UpdateCapacityProviderInput, ...func(*ecs.Options)) (*ecs.UpdateCapacityProviderOutput, error)
}

// applyEngineClass writes the rung into the role's capacity provider.
//
// A read-modify-write, and the read is not an optimisation (ADR 0074 decision 5):
// `UpdateCapacityProvider` takes `InfrastructureRoleArn` and `InstanceLaunchTemplate` as
// REQUIRED members, so the only way to change four numbers is to hand back everything else
// unchanged. The CP therefore holds no design for the box — it carries the stack's design
// across one call and edits four fields of it.
//
// It is idempotent by construction: writing the same rung twice sends the same values, buys
// nothing and moves no desired count. That is what makes it safe to call again just before
// every start, which is how a CloudFormation update that reverted the provider is undone
// before the next box is bought.
func applyEngineClass(ctx context.Context, api engineCapacityAPI, cluster, provider string, c engineClass) error {
	if api == nil || strings.TrimSpace(provider) == "" {
		return fmt.Errorf("no capacity provider to update")
	}
	// 🔴 NAMES ONLY. `DescribeCapacityProviders` refuses a request that carries both a cluster
	// and a list of names ("Cannot specify both capacity providers and cluster in the same
	// request", InvalidParameterException, measured on the deployment — ADR 0074 P1). Neither
	// the API reference nor the SDK's own comment says so, and every unit test passed because a
	// fake accepts anything. The cluster is checked below, on the answer, instead.
	out, err := api.DescribeCapacityProviders(ctx, &ecs.DescribeCapacityProvidersInput{
		CapacityProviders: []string{provider},
	})
	if err != nil {
		return fmt.Errorf("describing the capacity provider %s: %w", provider, err)
	}
	var cur *ecstypes.CapacityProvider
	for i := range out.CapacityProviders {
		if aws.ToString(out.CapacityProviders[i].Name) == provider {
			cur = &out.CapacityProviders[i]
			break
		}
	}
	if cur == nil || cur.ManagedInstancesProvider == nil {
		// A Fargate or Auto Scaling provider has no instance requirements to move. Refusing
		// beats writing an MI configuration onto something that is not one.
		return fmt.Errorf("capacity provider %s is not a Managed Instances provider", provider)
	}
	// The name was asked for without a cluster, so the answer's own cluster is what says this is
	// the provider this engine runs on. A name that resolved somewhere else is not written to.
	if got := aws.ToString(cur.Cluster); cluster != "" && got != "" && !sameECSCluster(got, cluster) {
		return fmt.Errorf("capacity provider %s belongs to cluster %s, not %s", provider, got, cluster)
	}
	mi := cur.ManagedInstancesProvider
	tpl := mi.InstanceLaunchTemplate
	if tpl == nil {
		return fmt.Errorf("capacity provider %s declares no launch template", provider)
	}
	upd := instanceLaunchTemplateUpdate(tpl)
	upd.InstanceRequirements = engineClassRequirements(tpl.InstanceRequirements, c)
	_, err = api.UpdateCapacityProvider(ctx, &ecs.UpdateCapacityProviderInput{
		Name:    aws.String(provider),
		Cluster: aws.String(cluster),
		ManagedInstancesProvider: &ecstypes.UpdateManagedInstancesProviderConfiguration{
			InfrastructureRoleArn:      mi.InfrastructureRoleArn,
			InstanceLaunchTemplate:     upd,
			AutoRepairConfiguration:    mi.AutoRepairConfiguration,
			InfrastructureOptimization: mi.InfrastructureOptimization,
			PropagateTags:              mi.PropagateTags,
		},
	})
	if err != nil {
		return fmt.Errorf("updating the capacity provider %s: %w", provider, err)
	}
	return nil
}

// sameECSCluster compares two cluster references that may be a name or an ARN. ECS answers with
// whichever form it likes, and the CP is configured with a name.
func sameECSCluster(a, b string) bool {
	name := func(s string) string {
		if i := strings.LastIndex(s, "/"); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	return name(a) == name(b)
}

// instanceLaunchTemplateUpdate carries the launch template ECS returned into the shape the
// update takes. Every field is written out by hand, and engine_class_copy_test.go walks both
// structs by reflection so that a field the SDK grows fails a test here rather than
// disappearing from a live capacity provider.
//
// ⚠️ TWO fields of the read type have NO counterpart in the update type and therefore cannot
// be carried: `CapacityOptionType` (ON_DEMAND / SPOT) and `FipsEnabled`. Whether ECS preserves
// them across an update or resets them to their defaults is not documented and has not been
// measured (ADR 0074 open question 1) — which is why the ADR's P1 verifies the provider after
// the first real switch instead of trusting this.
func instanceLaunchTemplateUpdate(in *ecstypes.InstanceLaunchTemplate) *ecstypes.InstanceLaunchTemplateUpdate {
	if in == nil {
		return &ecstypes.InstanceLaunchTemplateUpdate{}
	}
	return &ecstypes.InstanceLaunchTemplateUpdate{
		CapacityReservations:            in.CapacityReservations,
		Ec2InstanceProfileArn:           in.Ec2InstanceProfileArn,
		InstanceMetadataTagsPropagation: in.InstanceMetadataTagsPropagation,
		InstanceRequirements:            in.InstanceRequirements,
		LocalStorageConfiguration:       in.LocalStorageConfiguration,
		Monitoring:                      in.Monitoring,
		NetworkConfiguration:            in.NetworkConfiguration,
		StorageConfiguration:            in.StorageConfiguration,
	}
}

// engineClassRequirements is the rung applied to the requirements ECS returned: four fields
// replaced, every other one kept.
//
// Keeping the rest is the point. `BurstablePerformance: excluded`, the accelerator manufacturer
// and count, the excluded types — none of them appear on this screen, and a rung change that
// dropped one would not fail: it would buy a slightly different box, once, at some future cold
// start.
func engineClassRequirements(cur *ecstypes.InstanceRequirementsRequest, c engineClass) *ecstypes.InstanceRequirementsRequest {
	out := &ecstypes.InstanceRequirementsRequest{}
	if cur != nil {
		v := *cur
		out = &v
	}
	out.AllowedInstanceTypes = append([]string(nil), c.Types...)
	out.VCpuCount = &ecstypes.VCpuCountRangeRequest{Min: aws.Int32(c.VCpuMin), Max: aws.Int32(c.VCpuMax)}
	out.MemoryMiB = &ecstypes.MemoryMiBRequest{Min: aws.Int32(c.MemMinMiB), Max: aws.Int32(c.MemMaxMiB)}
	// The VRAM floor is only expressible when the role asks for an accelerator at all: ECS
	// refuses AcceleratorTotalMemoryMiB without the accelerator fields (measured, ADR 0071),
	// and a CPU-only test engine has none. A rung declaring 0 clears it rather than asking for
	// zero VRAM, which would be a filter no instance passes.
	switch {
	case c.VramMiB <= 0:
		out.AcceleratorTotalMemoryMiB = nil
	case len(out.AcceleratorTypes) == 0 && len(out.AcceleratorManufacturers) == 0 && out.AcceleratorCount == nil:
		out.AcceleratorTotalMemoryMiB = nil
	default:
		out.AcceleratorTotalMemoryMiB = &ecstypes.AcceleratorTotalMemoryMiBRequest{Min: aws.Int32(int32(c.VramMiB))}
	}
	return out
}

// engineClassSettingKey names the row holding the rung an administrator chose. Separate from
// engineSettings' three (mode, modeAt, demandAt) because those are the controller's and this is
// not: the VOICEVOX engine shares that struct and has no ladder.
func engineClassSettingKey(key string) string { return "engine_" + key + "_class" }

// engineClassSwapWaitMax bounds how long a start is held back waiting for a box of the previous
// rung to leave.
//
// The wait itself is decision 4: a draining box and a new one exceed the G-family vCPU quota on
// a deployment that has 8 of them, and the failure is a placement that silently never happens.
// The BOUND is here because the wait's end condition belongs to AWS — `scaleInAfter: -1` means
// "never tidy up", and an unbounded gate would turn that into an engine that can never start
// again, with a log line as the only evidence. Measured drain: 427-477 s, about 8 minutes from
// desired 0 to the instance being gone, so 20 minutes waits out a slow one and still gives up.
const engineClassSwapWaitMax = 20 * time.Minute

// classes reports the ladder. Nil is the normal case.
func (e *engineRuntimeState) classList() []engineClass {
	if e == nil {
		return nil
	}
	return e.classes
}

// selectedClassID is the rung an administrator chose, or "" for the stack's default. The stored
// setting wins over everything (ADR 0074 decision 2) — that is what keeps a CloudFormation
// update from silently taking a deployment back to a smaller card.
func (e *engineRuntimeState) selectedClassID(ctx context.Context) string {
	if e == nil || e.settings == nil {
		return ""
	}
	v, _ := e.settings.GetSetting(ctx, engineClassSettingKey(e.def.Key))
	return strings.TrimSpace(v)
}

// selectedClass resolves that id against the ladder. The second result is false when this
// deployment has no ladder at all, which every caller reads as "this feature is not configured"
// rather than as an error.
//
// ⚠️ A stored id that is no longer in the ladder resolves to the DEFAULT rung and says so in
// the log. The alternative — refusing — would let an edit to the stack's ladder strand an
// engine on a setting nobody can see, and the panel shows the resolved rung either way.
func (e *engineRuntimeState) selectedClass(ctx context.Context) (engineClass, bool) {
	list := e.classList()
	if len(list) == 0 {
		return engineClass{}, false
	}
	id := e.selectedClassID(ctx)
	c, ok := engineClassByID(list, id)
	if !ok {
		log.Printf("engines: %s: the stored instance class %q is not in the ladder; using %s",
			e.def.Key, id, list[0].ID)
		return list[0], true
	}
	return c, true
}

// defaultClass is what the stack declares, i.e. the first rung. It is what the panel compares
// the selection against to decide whether to warn that this deployment is running on something
// other than its default (decision 7).
func (e *engineRuntimeState) defaultClass() (engineClass, bool) {
	list := e.classList()
	if len(list) == 0 {
		return engineClass{}, false
	}
	return list[0], true
}

// lastAppliedClass is the rung this process last wrote to the capacity provider.
func (e *engineRuntimeState) lastAppliedClass() string {
	e.appliedMu.Lock()
	defer e.appliedMu.Unlock()
	return e.appliedClass
}

func (e *engineRuntimeState) noteAppliedClass(id string) {
	e.appliedMu.Lock()
	e.appliedClass = id
	e.appliedMu.Unlock()
}

// applyClass writes the selected rung to the capacity provider. Idempotent, and cheap enough to
// call before every start: ECS API calls are not billed, and re-sending the same requirements
// buys no box and moves no desired count.
func (e *engineRuntimeState) applyClass(ctx context.Context, c engineClass) error {
	if e.capacity == nil {
		return fmt.Errorf("no ECS client for %s", e.def.Key)
	}
	if err := applyEngineClass(ctx, e.capacity, e.cluster, e.def.CapacityProvider, c); err != nil {
		return err
	}
	e.noteAppliedClass(c.ID)
	return nil
}

// engineClassHasType reports whether a running box belongs to this rung.
func engineClassHasType(c engineClass, instanceType string) bool {
	for _, t := range c.Types {
		if t == instanceType {
			return true
		}
	}
	return false
}

// The reasons a start is held back by the class machinery. They are logged and audited beside
// the controller's own reasons, so "why did nothing start" has one place to be answered.
const (
	engineReasonClassSwapWait = "class_swap_wait"    // a box of the previous rung has not gone yet
	engineReasonClassApply    = "class_apply_failed" // the rung could not be written, and it differs
)

// startGate is what the controller asks before it buys a box (ADR 0074 decisions 4 and 5).
//
// Three things happen here and the order matters:
//
//  1. a box of a DIFFERENT rung still registered means the previous one has not gone. Starting
//     now either exceeds the vCPU quota or places the task straight back onto the old card;
//  2. the rung is applied again, idempotently, because a CloudFormation update reverts the
//     capacity provider to the stack's declaration and nothing tells the CP that happened;
//  3. what is about to be loaded is compared with what the card holds, and the sentence is
//     written to the log BEFORE the start. CUDA does not fail in a diagnosable shape, so the
//     one place this can be recorded is in front of it.
//
// A failure at (2) refuses the start only when the rung DIFFERS from the one this process last
// applied. If they are the same the provider already says the right thing and refusing would
// take the engine away over a transient API error.
func (e *engineRuntimeState) startGate(ctx context.Context) (bool, string) {
	if e == nil || len(e.classes) == 0 {
		return true, ""
	}
	sel, ok := e.selectedClass(ctx)
	if !ok {
		return true, ""
	}
	if b, on := e.ecs.box(ctx); on && b.instanceType != "" && !engineClassHasType(sel, b.instanceType) {
		if e.swapWaitExpired() {
			log.Printf("engines: %s: a %s box is still registered after %s; starting on the old class anyway",
				e.def.Key, b.instanceType, engineClassSwapWaitMax)
		} else {
			return false, engineReasonClassSwapWait
		}
	} else {
		e.clearSwapWait()
	}
	if err := e.applyClass(ctx, sel); err != nil {
		if e.lastAppliedClass() != sel.ID {
			log.Printf("engines: %s: not starting — the instance class %s could not be applied: %v",
				e.def.Key, sel.ID, err)
			return false, engineReasonClassApply
		}
		log.Printf("engines: %s: re-applying the instance class %s failed (already applied by this process): %v",
			e.def.Key, sel.ID, err)
	}
	e.logVramFit(ctx, sel)
	return true, ""
}

// swapWaitExpired reports whether the wait for the previous box has run past its bound, and
// starts the clock the first time it is asked.
func (e *engineRuntimeState) swapWaitExpired() bool {
	e.appliedMu.Lock()
	defer e.appliedMu.Unlock()
	if e.swapWaitSince.IsZero() {
		e.swapWaitSince = time.Now()
		return false
	}
	return time.Since(e.swapWaitSince) >= engineClassSwapWaitMax
}

func (e *engineRuntimeState) clearSwapWait() {
	e.appliedMu.Lock()
	e.swapWaitSince = time.Time{}
	e.appliedMu.Unlock()
}

// logVramFit writes the one line that makes a CUDA death readable afterwards (decision 6).
//
// It states the SOURCE of the number as well as the number: "at least 23,000 MiB (weights
// only)" and "23,000 MiB (measured)" are different claims, and an operator reading this after a
// failed start needs to know which one was being relied on.
func (e *engineRuntimeState) logVramFit(ctx context.Context, c engineClass) {
	need, source, id := engineVramDemand(e.catalog.list(ctx))
	switch {
	case source == engineVramUnknown:
		log.Printf("engines: %s: starting on %s (%d MiB VRAM declared); no model declares what it needs",
			e.def.Key, c.ID, c.VramMiB)
	case !engineClassFits(c, need):
		log.Printf("engines: %s: starting on %s (%d MiB VRAM declared) with %s wanting %d MiB (%s) — this may not fit",
			e.def.Key, c.ID, c.VramMiB, id, need, source)
	default:
		log.Printf("engines: %s: starting on %s (%d MiB VRAM declared); largest model %s wants %d MiB (%s)",
			e.def.Key, c.ID, c.VramMiB, id, need, source)
	}
}

// classStartHeld is startGate for the admin route: it asks the same question and says so in the
// log, but never turns a "cannot start yet" into a failed request. The administrator's intent is
// stored either way, and the controller acts on it as soon as the previous box has gone.
func (e *engineRuntimeState) classStartHeld(ctx context.Context) bool {
	if e == nil || len(e.classes) == 0 {
		return false
	}
	ok, why := e.startGate(ctx)
	if !ok {
		log.Printf("engines: %s: mode=on stored, but the start waits (%s)", e.def.Key, why)
	}
	return !ok
}
