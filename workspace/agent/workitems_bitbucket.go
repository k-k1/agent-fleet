package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Bitbucket pull requests as work items (docs/log/80 §80.19 / ADR 0061 決定 17〜19).
//
// The third fetch adapter, and the first one that could NOT keep "one saved query = one
// provider search" for free: Bitbucket Cloud has no account-wide issue/PR search the way
// GitHub has /search/issues and Jira has JQL. What it has is
//
//	GET /2.0/repositories/{workspace}/{repo}/pullrequests   — one repository
//	GET /2.0/workspaces/{workspace}/pullrequests/{user}     — one workspace, AUTHORED BY that user
//
// so the target that GitHub and Jira can leave implicit has to be written down. It goes
// in the query string itself rather than in a new column: a GitHub query already carries
// `repo:owner/name` and a JQL one `project = X`, so "the query field holds that
// provider's own way of saying where to look" is the existing rule, not a new one — and
// it costs no DTO or schema change (ADR 0061 決定 17).
//
// ⚠️ No Bitbucket account exists in this workspace or in CI. What IS pinned here is
// measured against the real api.bitbucket.org through its PUBLIC repositories
// (docs/log/80 §80.19.2): the response shape, `q` overriding the implicit state=OPEN default,
// `sort=-updated_on`, `fields=` projection, and the 400 body for a bad filter. The
// authenticated halves — the workspace endpoint, the `pullrequest` scope refusal — are
// stubbed, and saying which is which is the point of that section.

// bitbucketAPIBase is where the REST calls go. A var so the tests can point the whole
// adapter at a stub (same reason as jiraCloudAPIBase).
var bitbucketAPIBase = "https://api.bitbucket.org"

// bbMeToken is what a member writes instead of their own account UUID. Bitbucket's filter
// language has no `currentUser()` (Jira) and no `@me` (GitHub), and the UUID it wants
// looks like `{b8ceb65c-…}` — nothing a person keeps to hand. Without this substitution
// the single most useful query, "pull requests waiting for my review", is unwritable.
const bbMeToken = "@me"

// bitbucketWorkItemQuery is a parsed saved query: where to look, and Bitbucket's own
// filter expression for what to look for.
type bitbucketWorkItemQuery struct {
	Workspace string
	Repo      string // "" = the whole workspace (authored-by only — Bitbucket's limit)
	Filter    string // verbatim `q=` expression; "" = Bitbucket's default (state=OPEN)
}

// parseBitbucketWorkItemQuery splits "<workspace>[/<repo>] [filter]".
//
// The first whitespace-delimited token is the target because Bitbucket cannot search
// without one; everything after it is handed to Bitbucket untouched. af does not parse,
// validate or rewrite the filter — the same promise the GitHub and Jira adapters make
// about search syntax and JQL.
func parseBitbucketWorkItemQuery(q string) (bitbucketWorkItemQuery, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return bitbucketWorkItemQuery{}, fmt.Errorf("query is empty")
	}
	target, filter, _ := strings.Cut(q, " ")
	out := bitbucketWorkItemQuery{Filter: strings.TrimSpace(filter)}
	ws, repo, hasSlash := strings.Cut(target, "/")
	out.Workspace, out.Repo = strings.TrimSpace(ws), strings.TrimSpace(repo)
	if out.Workspace == "" || (hasSlash && out.Repo == "") || strings.Contains(out.Repo, "/") {
		return bitbucketWorkItemQuery{}, fmt.Errorf(
			"a Bitbucket query starts with the target: `workspace/repo` (a repository) or `workspace` (your own pull requests), then the filter")
	}
	return out, nil
}

// bitbucketSearchWorkItems resolves one saved query into rail rows.
//
// ⚠️ 401 is refreshed-and-retried exactly once through the shared OAuth path
// (refreshBitbucketAndRetry): the access token lives ~2h and a rail that fetches every 5
// minutes crosses that boundary several times a day, so "re-connect Bitbucket" would be
// the normal message rather than the exceptional one.
func bitbucketSearchWorkItems(s *secrets.Data, queryID, query string) ([]workItemOut, error) {
	q, err := parseBitbucketWorkItemQuery(query)
	if err != nil {
		return nil, err
	}
	auth, err := gitx.BitbucketAuthHeader(s)
	if err != nil {
		return nil, err
	}
	rows, err := bitbucketFetchPullRequests(auth, q, queryID)
	err = gitx.RefreshBitbucketAndRetry(s, err, func(a string) error {
		var e error
		rows, e = bitbucketFetchPullRequests(a, q, queryID)
		return e
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// bitbucketFetchPullRequests performs the one HTTP call the saved query maps to.
//
// ★ One call, not N. Walking the member's repositories to fake a cross-workspace search
// would turn a saved query into an unbounded fan-out and quietly break the budget
// workItemFetchQueries × workItemFetchPerQuery exists to bound (ADR 0061 決定 17).
func bitbucketFetchPullRequests(auth string, q bitbucketWorkItemQuery, queryID string) ([]workItemOut, error) {
	filter := q.Filter
	endpoint := bitbucketAPIBase + "/2.0/repositories/" + url.PathEscape(q.Workspace) + "/" + url.PathEscape(q.Repo) + "/pullrequests"
	if q.Repo == "" || strings.Contains(filter, bbMeToken) {
		uuid, err := bitbucketSelfUUID(auth)
		if err != nil {
			return nil, err
		}
		filter = strings.ReplaceAll(filter, bbMeToken, uuid)
		if q.Repo == "" {
			endpoint = bitbucketAPIBase + "/2.0/workspaces/" + url.PathEscape(q.Workspace) + "/pullrequests/" + url.PathEscape(uuid)
		}
	}
	// ★ fields= is not only a size cut. A pull request object carries its full
	// description, and this process must not carry a body it has promised not to hand to
	// the Control Plane (ADR 0061 決定 2) — asking for the columns the rail draws is that
	// promise written into the request. If Bitbucket ever ignored the projection the
	// mapping below would still read the same named fields, so the downside is bytes.
	params := url.Values{
		"pagelen": {fmt.Sprint(workItemFetchPerQuery)},
		// 取得は 50 件で切るので、どの 50 件かを provider に委ねない（ADR 0061 決定 15 が
		// JQL に ORDER BY を足したのと同じ理由）。
		"sort": {"-updated_on"},
		"fields": {"values.id,values.title,values.state,values.draft,values.updated_on," +
			"values.links.html.href,values.author.display_name,values.author.nickname," +
			"values.destination.repository.full_name"},
	}
	if filter != "" {
		params.Set("q", filter)
	}
	body, status, err := gitx.BitbucketGetStatus(workItemHTTPClient, auth, endpoint+"?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("could not reach %s", bitbucketAPIBase)
	}
	if status != http.StatusOK {
		return nil, bitbucketWorkItemError(status, body, q)
	}
	return parseBitbucketPullRequests(body, queryID)
}

// bitbucketWorkItemError turns a refused fetch into the sentence the member reads on that
// query's row.
//
// ★ 403 is the one that has to be said precisely. Reading pull requests needs Bitbucket's
// `pullrequest` scope (`read:pullrequest:bitbucket` on the API-token path), and a
// connection made for cloning does not have it — the member's own credential is fine and
// re-pasting it changes nothing. So the message names the missing permission and who can
// add it, instead of the generic "re-connect" that would send them round a loop
// (docs/log/80 §80.19.3).
func bitbucketWorkItemError(status int, body []byte, q bitbucketWorkItemQuery) error {
	text := gitx.BitbucketErrText(body)
	switch status {
	case http.StatusUnauthorized:
		return gitx.ErrBitbucketUnauthorized
	case http.StatusForbidden:
		return fmt.Errorf("bitbucket refused this read — the connection is missing the pull request permission " +
			"(`pullrequest` on the OAuth consumer, `read:pullrequest:bitbucket` on an API token). " +
			"A tenant administrator adds it to the app; then re-connect Bitbucket")
	case http.StatusNotFound:
		target := q.Workspace
		if q.Repo != "" {
			target += "/" + q.Repo
		}
		return fmt.Errorf("bitbucket has no %s visible to this connection", target)
	case http.StatusBadRequest:
		// フィルタ式の文法エラーはここ。Bitbucket の説明文をそのまま出すのが一番親切
		// （Jira の JQL 400 と同じ扱い）。
		return fmt.Errorf("bitbucket could not parse the filter: %s", text)
	case http.StatusTooManyRequests:
		return fmt.Errorf("bitbucket rate limit reached")
	}
	return fmt.Errorf("bitbucket answered %d: %s", status, text)
}

// parseBitbucketPullRequests maps a paginated pull request body onto the shared row shape.
// Split from the HTTP call so the mapping — the part that decides what the rail shows —
// is testable without a Bitbucket account (there is none here or in CI).
func parseBitbucketPullRequests(body []byte, queryID string) ([]workItemOut, error) {
	var br struct {
		Values []struct {
			ID        int    `json:"id"`
			Title     string `json:"title"`
			State     string `json:"state"`
			Draft     bool   `json:"draft"`
			UpdatedOn string `json:"updated_on"`
			Author    struct {
				DisplayName string `json:"display_name"`
				Nickname    string `json:"nickname"`
			} `json:"author"`
			Destination struct {
				Repository struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
			} `json:"destination"`
			Links struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		} `json:"values"`
	}
	if err := json.Unmarshal(body, &br); err != nil {
		return nil, err
	}
	out := make([]workItemOut, 0, len(br.Values))
	for _, pr := range br.Values {
		repo := strings.TrimSpace(pr.Destination.Repository.FullName)
		key := fmt.Sprintf("%s#%d", repo, pr.ID)
		if repo == "" {
			key = fmt.Sprintf("#%d", pr.ID)
		}
		out = append(out, workItemOut{
			QueryID: queryID, Provider: "bitbucket", Kind: "pr", Key: key,
			Title: pr.Title, State: normalizeBitbucketPRState(pr.State, pr.Draft),
			URL: pr.Links.HTML.Href,
			// ★ 担当者の欄には作者を入れる。Bitbucket の PR に担当者は無く、レビュー待ちの
			// 一覧で「誰の PR か」は行ごとに違う唯一の情報だからである。自分の PR だけを
			// 並べたクエリでは全行同じ値になるので、uniformMeta が自動的に落とす
			// （docs/log/80 §80.18.2）。
			Assignee: firstNonEmpty(pr.Author.DisplayName, pr.Author.Nickname),
			// Bitbucket の PR にラベルは無い。⚠️ nil スライスは JSON の null になり、
			// Console が配列として扱えず真っ白になる（docs/log/80 §80.17.5）。
			Labels: []string{},
			Repo:   repo, UpdatedAt: bitbucketTimeToRFC3339(pr.UpdatedOn),
		})
	}
	return out, nil
}

// normalizeBitbucketPRState maps Bitbucket's four states onto the shared vocabulary.
//
// A draft is in_progress rather than open, matching the GitHub adapter: "open" would tell
// the reader that someone is waiting on them, which for a draft is untrue. DECLINED and
// SUPERSEDED join MERGED in done — none of them is work anyone is still waiting for, and
// the rail already sinks done rows to the bottom rather than hiding them.
func normalizeBitbucketPRState(state string, draft bool) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "OPEN":
		if draft {
			return "in_progress"
		}
		return "open"
	case "MERGED", "DECLINED", "SUPERSEDED":
		return "done"
	default:
		return "other"
	}
}

// bitbucketTimeToRFC3339 converts Bitbucket's "2026-08-24T07:10:55.049604+00:00" to UTC
// RFC3339.
//
// ⚠️ Not cosmetic. sortWorkItems compares updatedAt as a STRING across providers, so an
// offset-bearing stamp sorts wrong the moment a Bitbucket row meets a GitHub one — and
// the rail's whole claim is "newest first". A value that will not parse is passed through
// rather than dropped (a row with an odd stamp still beats no row), same as Jira's.
func bitbucketTimeToRFC3339(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999-0700"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return v
}

// bitbucketSelfUUID resolves the connected account's UUID for @me.
//
// GET /2.0/user needs the `account` scope, which every af Bitbucket connection already
// carries — it is what the Connections card names the account with (bitbucketAccount).
var bbSelfCache struct {
	mu   sync.Mutex
	auth string // the Authorization header the uuid was resolved with
	uuid string
}

func bitbucketSelfUUID(auth string) (string, error) {
	bbSelfCache.mu.Lock()
	defer bbSelfCache.mu.Unlock()
	// ★ Keyed on the header, and only the last one is kept: an OAuth access token is
	// replaced every couple of hours, so a map keyed by token would grow forever, and a
	// cache that ignored the token would answer for a connection that has been replaced
	// by another account's.
	if bbSelfCache.auth == auth && bbSelfCache.uuid != "" {
		return bbSelfCache.uuid, nil
	}
	body, status, err := gitx.BitbucketGetStatus(workItemHTTPClient, auth, bitbucketAPIBase+"/2.0/user")
	if err != nil {
		return "", fmt.Errorf("could not reach %s", bitbucketAPIBase)
	}
	if status != http.StatusOK {
		if status == http.StatusUnauthorized {
			return "", gitx.ErrBitbucketUnauthorized
		}
		return "", fmt.Errorf("bitbucket would not say who this connection is (%d): %s", status, gitx.BitbucketErrText(body))
	}
	var u struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(body, &u); err != nil || strings.TrimSpace(u.UUID) == "" {
		return "", fmt.Errorf("bitbucket returned no account uuid for @me")
	}
	bbSelfCache.auth, bbSelfCache.uuid = auth, u.UUID
	return u.UUID, nil
}
