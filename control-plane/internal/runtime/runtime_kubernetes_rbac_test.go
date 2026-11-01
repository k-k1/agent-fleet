package runtime

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// cpRBACPath is the manifest the production Role and ClusterRole of the CP come from.
// The overlays (deploy/kubernetes/overlays/*, components/params) only rewrite namespaces,
// the ClusterRole's name and rules.0.resourceNames.0 in it; they never add or change a
// rule, so this base file is the single source of truth for what af-cp may do.
const cpRBACPath = "../../../deploy/kubernetes/base/cp-rbac.yaml"

// cpRBAC is the af-cp Role and ClusterRole as parsed from cp-rbac.yaml. The envtest
// helper installs these very objects, so a rule cannot drift from production.
type cpRBAC struct {
	role, clusterRole           map[string]any
	roleRules, clusterRoleRules []kubeTestRule
}

func loadCPRBAC() (*cpRBAC, error) {
	b, err := os.ReadFile(filepath.FromSlash(cpRBACPath))
	if err != nil {
		return nil, err
	}
	return parseCPRBAC(b)
}

// parseCPRBAC picks the one Role named af-cp and the one ClusterRole out of the documents.
// The namespace is dropped from the Role (the caller installs it per test namespace); the
// ClusterRole keeps its name, which the caller may replace.
func parseCPRBAC(data []byte) (*cpRBAC, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	out := &cpRBAC{}
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		kind, _ := doc["kind"].(string)
		meta, _ := doc["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		switch {
		case kind == "Role" && name == "af-cp":
			if out.role != nil {
				return nil, errors.New("more than one Role af-cp")
			}
			delete(meta, "namespace")
			out.role = doc
		case kind == "ClusterRole":
			if out.clusterRole != nil {
				return nil, errors.New("more than one ClusterRole")
			}
			out.clusterRole = doc
		}
	}
	if out.role == nil || out.clusterRole == nil {
		return nil, errors.New("cp-rbac.yaml has no Role af-cp and/or no ClusterRole")
	}
	var err error
	if out.roleRules, err = expandRules(out.role); err != nil {
		return nil, err
	}
	if out.clusterRoleRules, err = expandRules(out.clusterRole); err != nil {
		return nil, err
	}
	return out, nil
}

func strList(v any) []string {
	var s []string
	l, _ := v.([]any)
	for _, e := range l {
		s = append(s, fmt.Sprint(e))
	}
	return s
}

// expandRules turns each rule into one kubeTestRule per apiGroup x resource.
func expandRules(obj map[string]any) ([]kubeTestRule, error) {
	rules, _ := obj["rules"].([]any)
	if len(rules) == 0 {
		return nil, fmt.Errorf("%v has no rules", obj["kind"])
	}
	var out []kubeTestRule
	for _, r := range rules {
		m, _ := r.(map[string]any)
		groups, resources, verbs := strList(m["apiGroups"]), strList(m["resources"]), strList(m["verbs"])
		if len(groups) == 0 || len(resources) == 0 || len(verbs) == 0 {
			return nil, fmt.Errorf("%v: rule %v lacks apiGroups, resources or verbs", obj["kind"], m)
		}
		for _, g := range groups {
			for _, res := range resources {
				out = append(out, kubeTestRule{group: g, resource: res, names: strList(m["resourceNames"]), verbs: verbs})
			}
		}
	}
	return out, nil
}

func TestCPRBACManifestParses(t *testing.T) {
	c, err := loadCPRBAC()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.roleRules) == 0 || len(c.clusterRoleRules) == 0 {
		t.Fatalf("empty rules: %v / %v", c.roleRules, c.clusterRoleRules)
	}
}

// TestCPRBACManifestStaysNarrow pins the properties that make the grant ADR 0106 decision
// 8's: widening one of them widens what a compromised CP reaches, so it must be a
// deliberate edit of this test as well as of the manifest.
func TestCPRBACManifestStaysNarrow(t *testing.T) {
	c, err := loadCPRBAC()
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range []struct {
		what  string
		rules []kubeTestRule
	}{{"Role", c.roleRules}, {"ClusterRole", c.clusterRoleRules}} {
		for _, r := range set.rules {
			for _, s := range append(append([]string{r.group, r.resource}, r.verbs...), r.names...) {
				if s == "*" {
					t.Errorf("%s: wildcard in %+v", set.what, r)
				}
			}
		}
	}
	for _, r := range c.clusterRoleRules {
		if len(r.verbs) != 1 || r.verbs[0] != "get" {
			t.Errorf("ClusterRole must be get-only, got %+v", r)
		}
		if r.resource == "storageclasses" && len(r.names) != 1 {
			t.Errorf("storageclasses must be restricted to the one configured class by resourceNames, got %+v", r)
		}
	}
	for _, r := range c.roleRules {
		if r.resource == "pods" {
			for _, v := range r.verbs {
				if v == "update" || v == "patch" {
					t.Errorf("pods must not be mutable in place: %+v", r)
				}
			}
		}
	}
	// components/params writes the StorageClass into rules.0.resourceNames.0.
	if first := c.clusterRole["rules"].([]any)[0].(map[string]any); len(strList(first["resourceNames"])) != 1 {
		t.Errorf("ClusterRole rules[0] must be the resourceNames-restricted storageclasses rule (components/params patches rules.0.resourceNames.0)")
	}
}

func TestParseCPRBACRejectsBrokenManifests(t *testing.T) {
	const role = "kind: Role\nmetadata: {name: af-cp, namespace: x}\nrules:\n- {apiGroups: [''], resources: [pods], verbs: [get]}\n"
	const cr = "kind: ClusterRole\nmetadata: {name: c}\nrules:\n- {apiGroups: [''], resources: [persistentvolumes], verbs: [get]}\n"
	if c, err := parseCPRBAC([]byte(role + "---\n" + cr)); err != nil {
		t.Fatal(err)
	} else if _, has := c.role["metadata"].(map[string]any)["namespace"]; has {
		t.Error("Role namespace must be dropped")
	}
	for name, doc := range map[string]string{
		"no ClusterRole": role,
		"no Role":        cr,
		"two Roles":      role + "---\n" + role + "---\n" + cr,
		"rule w/o verbs": strings.Replace(role, "verbs: [get]", "verbs: []", 1) + "---\n" + cr,
		"Role w/o rules": "kind: Role\nmetadata: {name: af-cp}\n---\n" + cr,
	} {
		if _, err := parseCPRBAC([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
