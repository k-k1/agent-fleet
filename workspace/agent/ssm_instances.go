package main

import (
	"context"
	"encoding/json"
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
	cfg := ssmConfigPath("discovery-" + req.Profile)
	meta := session.SSMMeta{Profile: req.Profile, Region: req.Region, StartURL: req.StartURL,
		SSORegion: req.SSORegion, AccountID: req.AccountID, RoleName: req.RoleName}
	if err := writeSSMConfig(cfg, meta); err != nil {
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
	items := make([]ssmInstance, 0, len(raw.Items))
	for _, v := range raw.Items {
		if strings.HasPrefix(v.InstanceID, "i-") {
			items = append(items, ssmInstance{v.InstanceID, v.ComputerName, v.IPAddress, v.PlatformName, v.PingStatus})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"instances": items})
}
