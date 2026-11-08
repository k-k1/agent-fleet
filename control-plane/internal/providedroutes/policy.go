package providedroutes

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The session policy every assumed route session carries (ADR 0129 D2): the least-privilege
// closure of the source's allow-list, built per model from AWS's inference prerequisites (docs,
// not measured). The effective permission is the intersection with the route role's own policy,
// so the role must grant at least this closure; the deployment's route role is defined with
// exactly it (deploy/aws/ecs/cfn).
//
//   - A foundation model called in the call's Region: InvokeModel and
//     InvokeModelWithResponseStream on its foundation-model ARN.
//   - A system inference profile: the same two actions on the inference-profile ARN, and on the
//     profile's foundation model in every Region, conditioned on bedrock:InferenceProfileArn so
//     the model is reachable only through a profile on the list. AWS requires the model in each
//     destination Region of the profile; which Regions those are is the profile's, so the
//     Region is a wildcard and the condition is what narrows it. Plus GetInferenceProfile on the
//     profile, which the prerequisites name.
//
// Converse and ConverseStream are authorized by the same two actions.

// MaxSessionPolicy is AWS's limit on an inline session policy's plaintext (docs: "can't exceed
// 2,048 characters"). A list whose closure does not fit is refused instead of being cut.
const MaxSessionPolicy = 2048

var invokeActions = []string{"bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream"}

// modelProviders are the first segment of a foundation-model id for the families the price table
// carries. A system inference profile id is "<geography>.<provider>.<model>".
var modelProviders = []string{"anthropic", "openai"}

var accountRe = regexp.MustCompile(`^arn:(aws[a-z-]*):iam::([0-9]{12}):role/`)

// SplitModelID says whether id is a system inference profile and names the foundation model it
// routes to. ok is false for an id of neither shape.
func SplitModelID(id string) (profile bool, model string, ok bool) {
	first, rest, found := strings.Cut(id, ".")
	if !found || rest == "" {
		return false, "", false
	}
	if slices.Contains(modelProviders, first) {
		return false, id, true
	}
	second, _, _ := strings.Cut(rest, ".")
	if slices.Contains(modelProviders, second) {
		return true, rest, true
	}
	return false, "", false
}

type policyStatement struct {
	Sid       string                    `json:"Sid"`
	Effect    string                    `json:"Effect"`
	Action    []string                  `json:"Action"`
	Resource  []string                  `json:"Resource"`
	Condition map[string]map[string]any `json:"Condition,omitempty"`
}

type policyDoc struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}

// SessionPolicy builds the closure for allow on a route role (its partition and account are
// the inference profiles') calling region. The result is compact JSON within MaxSessionPolicy.
func SessionPolicy(roleARN, region string, allow []string) (string, error) {
	m := accountRe.FindStringSubmatch(strings.TrimSpace(roleARN))
	if m == nil {
		return "", fmt.Errorf("role ARN %q is not an IAM role ARN", roleARN)
	}
	if !regionRe.MatchString(region) {
		return "", fmt.Errorf("%q is not an AWS Region", region)
	}
	partition, account := m[1], m[2]
	var direct, profiles, viaProfile []string
	for _, id := range NormalizeAllowList(allow) {
		isProfile, model, ok := SplitModelID(id)
		if !ok {
			return "", fmt.Errorf("allow-list entry %q is neither a foundation model nor a system inference profile id", id)
		}
		if !isProfile {
			direct = append(direct, fmt.Sprintf("arn:%s:bedrock:%s::foundation-model/%s", partition, region, id))
			continue
		}
		profiles = append(profiles, fmt.Sprintf("arn:%s:bedrock:%s:%s:inference-profile/%s", partition, region, account, id))
		viaProfile = append(viaProfile, fmt.Sprintf("arn:%s:bedrock:*::foundation-model/%s", partition, model))
	}
	if len(direct)+len(profiles) == 0 {
		return "", fmt.Errorf("the model allow-list is empty")
	}
	doc := policyDoc{Version: "2012-10-17"}
	if invoke := append(slices.Clone(direct), profiles...); len(invoke) > 0 {
		doc.Statement = append(doc.Statement, policyStatement{Sid: "Invoke", Effect: "Allow", Action: invokeActions, Resource: invoke})
	}
	if len(profiles) > 0 {
		doc.Statement = append(doc.Statement,
			policyStatement{Sid: "ProfileModels", Effect: "Allow", Action: invokeActions, Resource: slices.Compact(slices.Sorted(slices.Values(viaProfile))),
				Condition: map[string]map[string]any{"StringEquals": {"bedrock:InferenceProfileArn": profiles}}},
			policyStatement{Sid: "Profiles", Effect: "Allow", Action: []string{"bedrock:GetInferenceProfile"}, Resource: profiles})
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	if len(b) > MaxSessionPolicy {
		return "", fmt.Errorf("the session policy for %d models is %d characters, above AWS's %d; shorten the model allow-list",
			len(direct)+len(profiles), len(b), MaxSessionPolicy)
	}
	return string(b), nil
}
