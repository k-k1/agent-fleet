package main

import (
	"context"
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

type ssmInstancesReq struct {
	Profile, Region, StartURL, SSORegion, AccountID, RoleName string
}

type ssmInstance struct {
	InstanceID   string `json:"instanceId"`
	Name         string `json:"name,omitempty"`
	ComputerName string `json:"computerName,omitempty"`
	IPAddress    string `json:"ipAddress,omitempty"`
	PlatformName string `json:"platformName,omitempty"`
	PingStatus   string `json:"pingStatus"`
}

// handleSSMInstances lists online SSM managed EC2 nodes with the member's cached
// SSO credentials. Authentication stays inside the workspace; no credentials or
// AWS response are persisted by Agent Fleet.
func handleSSMInstances(w http.ResponseWriter, r *http.Request) {
	var req ssmInstancesReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Profile) == "" || strings.TrimSpace(req.StartURL) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_profile", "SSM profile is required")
		return
	}
	cfg := sessionx.SsmConfigPath("discovery-" + req.Profile)
	meta := session.SSMMeta{Profile: req.Profile, Region: req.Region, StartURL: req.StartURL,
		SSORegion: req.SSORegion, AccountID: req.AccountID, RoleName: req.RoleName}
	if err := sessionx.WriteSSMConfig(cfg, meta); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "config_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "aws", "ssm", "describe-instance-information",
		"--filters", "Key=PingStatus,Values=Online", "--output", "json", "--no-cli-pager")
	cmd.Env = append(os.Environ(), "AWS_CONFIG_FILE="+cfg, "AWS_PROFILE="+req.Profile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if isAWSAccessDenied(msg) {
			httpx.WriteErr(w, http.StatusForbidden, "ssm_search_forbidden",
				"ssm:DescribeInstanceInformation permission is required")
			return
		}
		httpx.WriteErr(w, http.StatusBadGateway, "aws_search_failed", msg)
		return
	}
	var raw struct {
		Items []struct {
			InstanceID, ComputerName, IPAddress, PlatformName, PingStatus string
		} `json:"InstanceInformationList"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "bad_aws_response", "AWS response was not valid JSON")
		return
	}
	ids := make([]string, 0, len(raw.Items))
	for _, v := range raw.Items {
		if strings.HasPrefix(v.InstanceID, "i-") {
			ids = append(ids, v.InstanceID)
		}
	}
	// Name is an EC2 tag and isn't included in DescribeInstanceInformation.
	// Enrich it when the SSO role has ec2:DescribeInstances; lack of that optional
	// permission must not make the SSM search fail.
	names := describeEC2Names(ctx, cmd.Env, ids)
	items := make([]ssmInstance, 0, len(ids))
	for _, v := range raw.Items {
		if strings.HasPrefix(v.InstanceID, "i-") {
			items = append(items, ssmInstance{v.InstanceID, names[v.InstanceID], v.ComputerName, v.IPAddress, v.PlatformName, v.PingStatus})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"instances": items})
}

func isAWSAccessDenied(message string) bool {
	return strings.Contains(message, "AccessDenied") || strings.Contains(message, "UnauthorizedOperation")
}

// describeEC2Names resolves the optional EC2 Name tag in bounded batches. Any
// error (most commonly AccessDenied) returns no enrichment, preserving the
// permission-minimal DescribeInstanceInformation behavior.
func describeEC2Names(ctx context.Context, env, ids []string) map[string]string {
	names := make(map[string]string)
	for start := 0; start < len(ids); start += 100 {
		end := min(start+100, len(ids))
		args := append([]string{"ec2", "describe-instances", "--instance-ids"}, ids[start:end]...)
		args = append(args, "--output", "json", "--no-cli-pager")
		cmd := exec.CommandContext(ctx, "aws", args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			return map[string]string{}
		}
		batch, ok := ec2NamesFromJSON(out)
		if !ok {
			return map[string]string{}
		}
		for id, name := range batch {
			names[id] = name
		}
	}
	return names
}

func ec2NamesFromJSON(data []byte) (map[string]string, bool) {
	var raw struct {
		Reservations []struct {
			Instances []struct {
				InstanceID string `json:"InstanceId"`
				Tags       []struct {
					Key, Value string
				}
			} `json:"Instances"`
		} `json:"Reservations"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil, false
	}
	names := make(map[string]string)
	for _, reservation := range raw.Reservations {
		for _, instance := range reservation.Instances {
			for _, tag := range instance.Tags {
				if tag.Key == "Name" {
					names[instance.InstanceID] = tag.Value
					break
				}
			}
		}
	}
	return names, true
}
