package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const (
	// Amazon's public ECS-optimized Amazon Linux 2023 AMI pointers, one per architecture.
	ssmSlotAMIX86 = "/aws/service/ecs/optimized-ami/amazon-linux-2023/recommended/image_id"
	ssmSlotAMIArm = "/aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id"

	slotAMITTL         = time.Hour
	slotAMIRetryAfter  = 5 * time.Minute
	slotAMIReadTimeout = 5 * time.Second
)

// Why the CP, and not the launch template, chooses the slot AMI: a template's ImageId moves
// only when its stack is deployed (x86) or never (arm64), so slots missed Amazon's security
// updates between releases, and the template version number — which moved on every x86
// re-resolve — said nothing about the arm64 AMI.

// slotAMIReason values: why a slot is outdated, in the admin wire and the logs.
const (
	slotStaleAMI      = "ami_behind"
	slotStaleTemplate = "template_changed"
)

type ssmParamAPI interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

type slotAMIEntry struct {
	id      string    // last value read from SSM; "" = never read
	readAt  time.Time // when it was read
	nextTry time.Time // after a failed read: not before this
	failing bool      // the last read failed (for log-once)
}

// slotAMIs is the CP's view of Amazon's current slot AMIs. It is nil-safe: a nil *slotAMIs
// knows nothing, so launches use the CloudFormation values and no slot is judged by AMI.
type slotAMIs struct {
	mu  sync.Mutex
	api ssmParamAPI
	now func() time.Time
	arc map[string]*slotAMIEntry
}

func newSlotAMIs(api ssmParamAPI) *slotAMIs {
	return &slotAMIs{api: api, now: time.Now, arc: map[string]*slotAMIEntry{}}
}

func slotAMIParam(arch string) (name, key string) {
	if arch == EC2ArchArm {
		return ssmSlotAMIArm, EC2ArchArm
	}
	return ssmSlotAMIX86, EC2ArchX86
}

// current is the AMI Amazon currently publishes for arch, as last READ from SSM, or "" when
// none has been read. It never returns a CloudFormation value: those are launch fallbacks
// (ec2PoolConfig.launchAMI), and judging a slot against a stale fallback would call newer
// slots outdated.
//
// A refresh is attempted at most once per hour, and after a failure not before
// slotAMIRetryAfter, each bounded by slotAMIReadTimeout — a launch waits at worst that long,
// once. A failure keeps the last value and is logged when it starts, not on every call
// (AccessDenied before the IAM grant is deployed is the expected case).
func (s *slotAMIs) current(ctx context.Context, arch string) string {
	if s == nil || s.api == nil {
		return ""
	}
	name, key := slotAMIParam(arch)
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.arc[key]
	if e == nil {
		e = &slotAMIEntry{}
		s.arc[key] = e
	}
	now := s.now()
	if e.id != "" && now.Sub(e.readAt) < slotAMITTL && !e.failing {
		return e.id
	}
	if now.Before(e.nextTry) {
		return e.id
	}
	rctx, cancel := context.WithTimeout(ctx, slotAMIReadTimeout)
	defer cancel()
	out, err := s.api.GetParameter(rctx, &ssm.GetParameterInput{Name: aws.String(name)})
	var id string
	if err == nil && out != nil && out.Parameter != nil {
		id = strings.TrimSpace(aws.ToString(out.Parameter.Value))
		if !strings.HasPrefix(id, "ami-") {
			id = ""
		}
	}
	if id == "" {
		if !e.failing {
			log.Printf("ecs-ec2: cannot read the %s slot AMI from SSM %s (%v); keeping %q and the CloudFormation value; retrying quietly every %s",
				key, name, err, e.id, slotAMIRetryAfter)
		}
		e.failing = true
		e.nextTry = now.Add(slotAMIRetryAfter)
		return e.id
	}
	switch {
	case e.id == "":
		log.Printf("ecs-ec2: slot AMI %s resolved from SSM: %s", key, id)
	case e.id != id:
		log.Printf("ecs-ec2: slot AMI %s changed in SSM: %s -> %s; slots on the old one are outdated", key, e.id, id)
	case e.failing:
		log.Printf("ecs-ec2: slot AMI %s readable from SSM again: %s", key, id)
	}
	e.id, e.readAt, e.failing, e.nextTry = id, now, false, time.Time{}
	return id
}

// launchAMI is the ImageId to override a slot launch with: the SSM-current AMI, else the
// CloudFormation value (arm64 only; "" lets the template's own x86 ImageId stand).
func (p ec2PoolConfig) launchAMI(ctx context.Context, arch string) string {
	if id := p.amis.current(ctx, arch); id != "" {
		return id
	}
	return p.amiFor(arch)
}

// snapshotAMIs reads both architectures' current AMIs for one judgement.
func (s *slotAMIs) snapshot(ctx context.Context) map[string]string {
	if s == nil {
		return nil
	}
	return map[string]string{
		EC2ArchX86: s.current(ctx, EC2ArchX86),
		EC2ArchArm: s.current(ctx, EC2ArchArm),
	}
}

// slotArch is the architecture of a slot instance: its own report, else the class that
// declares its instance type. "" when neither says (such a slot is never judged by AMI).
func (p ec2PoolConfig) slotArch(inst ec2types.Instance) string {
	switch inst.Architecture {
	case ec2types.ArchitectureValuesArm64:
		return EC2ArchArm
	case ec2types.ArchitectureValuesX8664:
		return EC2ArchX86
	}
	for _, c := range p.classes {
		for _, s := range c.slots {
			if s.instanceType == string(inst.InstanceType) {
				return c.arch
			}
		}
	}
	return ""
}

// slotAMIBehind reports whether the slot's ImageId differs from the current AMI for its
// architecture. known=false when either side is unknown; such a slot is never outdated.
func (p ec2PoolConfig) slotAMIBehind(inst ec2types.Instance, lt ec2LaunchTemplate) (behind, known bool) {
	have := aws.ToString(inst.ImageId)
	want := lt.amis[p.slotArch(inst)]
	if have == "" || want == "" {
		return false, false
	}
	return have != want, true
}

// slotStale is the whole judgement: AMI behind, or the launch template changed in anything
// but its ImageId. reasons is empty unless outdated; known is true when at least one half
// could be judged.
func (p ec2PoolConfig) slotStale(inst ec2types.Instance, lt ec2LaunchTemplate) (version string, reasons []string, known bool) {
	version, tplOutdated, tplKnown := slotTemplateOutdated(inst.Tags, lt)
	behind, amiKnown := p.slotAMIBehind(inst, lt)
	if amiKnown && behind {
		reasons = append(reasons, slotStaleAMI)
	}
	if tplKnown && tplOutdated {
		reasons = append(reasons, slotStaleTemplate)
	}
	return version, reasons, tplKnown || amiKnown
}

// ltSigCache holds, per launch template, a fingerprint of each version's data without its
// ImageId. Versions are immutable, so an entry is never refreshed; the cache is read again
// only when $Latest is not in it.
type ltSigCache struct {
	mu      sync.Mutex
	byID    map[string]map[int64]string
	nextTry time.Time
	failing bool
}

func newLTSigCache() *ltSigCache { return &ltSigCache{byID: map[string]map[int64]string{}} }

// versions returns a copy of the fingerprints known for lt, reading the template's versions
// when $Latest is missing. An unreadable answer returns what is cached (possibly nothing);
// the template half of the judgement is then unknown, never "changed".
func (c *ltSigCache) versions(ctx context.Context, api ec2API, lt ec2LaunchTemplate) map[int64]string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.byID[lt.id]
	if _, ok := m[lt.latest]; !ok && !time.Now().Before(c.nextTry) {
		if got, err := readLTSigs(ctx, api, lt.id); err != nil {
			if !c.failing {
				log.Printf("ecs-ec2: cannot read the slot launch template's versions (%v); slots are judged by AMI only until it can be read", err)
			}
			c.failing, c.nextTry = true, time.Now().Add(slotAMIRetryAfter)
		} else {
			if c.failing {
				log.Printf("ecs-ec2: the slot launch template's versions are readable again")
			}
			c.failing = false
			if m == nil {
				m = map[int64]string{}
				c.byID[lt.id] = m
			}
			for v, sig := range got {
				m[v] = sig
			}
		}
	}
	out := make(map[int64]string, len(m))
	for v, sig := range m {
		out[v] = sig
	}
	return out
}

func readLTSigs(ctx context.Context, api ec2API, id string) (map[int64]string, error) {
	out := map[int64]string{}
	in := &ec2.DescribeLaunchTemplateVersionsInput{LaunchTemplateId: aws.String(id)}
	for {
		res, err := api.DescribeLaunchTemplateVersions(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, v := range res.LaunchTemplateVersions {
			if v.VersionNumber == nil || v.LaunchTemplateData == nil {
				continue
			}
			out[*v.VersionNumber] = launchTemplateSig(*v.LaunchTemplateData)
		}
		if res.NextToken == nil || *res.NextToken == "" {
			return out, nil
		}
		in.NextToken = res.NextToken
	}
}

// launchTemplateSig fingerprints a version's data with the ImageId left out, so a change in
// the AMI alone (CloudFormation re-resolving SlotAmiId) is not a template change.
func launchTemplateSig(d ec2types.ResponseLaunchTemplateData) string {
	d.ImageId = nil
	b, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// slotJudgeable reports whether both halves of the staleness judgement are known for inst:
// the template version compared with $Latest, and the AMI compared with the current one. A
// pool without an AMI resolver (nil) judges the template only.
func (p ec2PoolConfig) slotJudgeable(inst ec2types.Instance, lt ec2LaunchTemplate) bool {
	if _, _, ok := slotTemplateOutdated(inst.Tags, lt); !ok {
		return false
	}
	if p.amis == nil {
		return true
	}
	_, ok := p.slotAMIBehind(inst, lt)
	return ok
}
