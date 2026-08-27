package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Posting a comment back to the ticket (docs/80 §80.10 / ADR 0061 決定 6).
//
// ★ This is the ONLY write af performs against a tracker, and it is reachable only from
// a human clicking 投稿 on a draft they have just read. There is deliberately:
//   - no MCP tool for it (an agent cannot reach this path),
//   - no automatic trigger (no "session finished → comment"),
//   - no state transition, no close, no assignee change.
//
// The reason is the same one that keeps the ticket body out of the first prompt: a
// ticket's text is written by third parties, so anything that lets that text steer a
// write back into the tracker closes a loop we do not want closed. A human reading the
// draft is the gate.

// handleWorkItemsComment — POST /work-items/comment {provider, key, body}.
func handleWorkItemsComment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
		Body     string `json:"body"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	body := strings.TrimSpace(in.Body)
	key := strings.TrimSpace(in.Key)
	if key == "" || body == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "key and body are required")
		return
	}
	s, err := secrets.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	var url string
	switch strings.TrimSpace(in.Provider) {
	case "", "github":
		e, ok := s.Git["github.com"]
		if !ok || e.Token == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "not_connected", "GitHub is not connected")
			return
		}
		url, err = githubPostIssueComment(e.Token, key, body)
	case "jira":
		if !jiraConnected(s.Jira) {
			httpx.WriteErr(w, http.StatusBadRequest, "not_connected", "Jira is not connected")
			return
		}
		url, err = jiraPostIssueComment(s.Jira, key, body)
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_provider", "unsupported provider: "+in.Provider)
		return
	}
	if err != nil {
		// 502: 相手側の拒否（権限不足・存在しない課題）。af の側の不備と区別できる形にする。
		httpx.WriteErr(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"url": url})
}

// parseGitHubIssueKey splits "owner/name#45" into its parts. The rail's key is the only
// coordinate we keep for an item, so this is what a write has to work from.
func parseGitHubIssueKey(key string) (repo string, number int, ok bool) {
	i := strings.LastIndex(key, "#")
	if i <= 0 {
		return "", 0, false
	}
	repo = key[:i]
	n, err := strconv.Atoi(key[i+1:])
	if err != nil || n <= 0 || !validRemoteRepo(repo) {
		return "", 0, false
	}
	return repo, n, true
}

func githubPostIssueComment(token, key, body string) (string, error) {
	repo, number, ok := parseGitHubIssueKey(key)
	if !ok {
		return "", fmt.Errorf("cannot post to %q (expected owner/name#number)", key)
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	u := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", escapeRepoPath(repo), number)
	req, err := http.NewRequest("POST", u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	githubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			// ⚠️ 読み取りは通るのに投稿だけ 403 になることがある（トークンの scope、
			// または閉じられた/ロックされた課題）。「再接続」と言い切らない。
			return "", fmt.Errorf("github refused the comment (%d) — the token may lack write access, or the issue is locked", resp.StatusCode)
		case http.StatusNotFound:
			return "", fmt.Errorf("github has no %s", key)
		}
		return "", fmt.Errorf("github comment %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.HTMLURL, nil
}

func jiraPostIssueComment(c *secrets.JiraCreds, key, body string) (string, error) {
	payload, _ := json.Marshal(map[string]any{"body": jiraADF(body)})
	u := jiraAPIBase(c) + "/rest/api/3/issue/" + key + "/comment"
	if err := jiraEnsureFresh(c); err != nil {
		return "", err
	}
	raw, status, err := jiraRequest(c, "POST", u, payload)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized && c.AuthKind == "oauth" {
		if rerr := jiraRefreshNow(c); rerr == nil {
			raw, status, err = jiraRequest(c, "POST", u, payload)
			if err != nil {
				return "", err
			}
		}
	}
	if status != http.StatusCreated && status != http.StatusOK {
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden:
			// ⚠️ OAuth では「スコープに write:jira-work が無い」もここに来る。読み取りは
			// 通るのに投稿だけ 403 という形になるので、権限の話だと分かる言い方にする。
			return "", fmt.Errorf("jira refused the comment (%d) — the account or the OAuth app may lack write access", status)
		case http.StatusNotFound:
			return "", fmt.Errorf("jira has no %s", key)
		}
		return "", fmt.Errorf("jira comment %d: %s", status, jiraErrText(raw))
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &out)
	url := c.Site + "/browse/" + key
	if out.ID != "" {
		url += "?focusedCommentId=" + out.ID
	}
	return url, nil
}

// jiraADF wraps plain text in the Atlassian Document Format Jira's REST v3 requires.
//
// ⚠️ v3 does NOT take a plain string for a comment body — posting one is a 400, which is
// the single most likely way this call breaks. Blank lines separate paragraphs; the line
// breaks inside a paragraph become hardBreak nodes, so a pasted list stays a list
// instead of collapsing into one run-on line.
func jiraADF(text string) map[string]any {
	content := []any{}
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		lines := strings.Split(strings.Trim(para, "\n"), "\n")
		nodes := []any{}
		for i, ln := range lines {
			if i > 0 {
				nodes = append(nodes, map[string]any{"type": "hardBreak"})
			}
			if ln == "" {
				continue
			}
			nodes = append(nodes, map[string]any{"type": "text", "text": ln})
		}
		if len(nodes) == 0 {
			continue
		}
		content = append(content, map[string]any{"type": "paragraph", "content": nodes})
	}
	if len(content) == 0 {
		// 空の doc は 400 になる。呼び出し側で本文の空は弾いているが、全部が空行
		// だった場合の保険（段落 1 つに空テキストは置けないので、1 文字だけ残す）。
		content = append(content, map[string]any{"type": "paragraph",
			"content": []any{map[string]any{"type": "text", "text": "-"}}})
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}
