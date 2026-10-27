package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// A positive pre-check of the Merge button for a GitHub App user token.
//
// A classic token reports its scopes; a fine-grained personal access token reports nothing and
// GitHub documents no endpoint that reads its granted permissions back (the
// X-Accepted-GitHub-Permissions header says what an endpoint needs, not what the token holds), so
// it stays "no button". A GitHub App user token (`ghu_`) is different: the merge endpoint needs
// Contents: write of it, and the installation's `permissions` map is readable through
// GET /user/installations, while GET /user/installations/{id}/repositories lists the repositories
// the user can reach through that installation together with the user's own permissions on each.
// The button is drawn only when an installation grants contents: write AND lists this repository
// AND the user has push there. An error, a rate limit, a timeout, a page cap reached without a
// hit, or any other shape is unknown, and unknown is false.
//
// Runs on the detail read only (never the list), bounded by one overall timeout, and a definitive
// answer is cached briefly per token and repository so reopening a panel does not repeat it.

// appPermTimeout is a variable so a test can shorten the overall bound.
var appPermTimeout = 6 * time.Second

const (
	appPermTTL       = time.Minute
	appPermPageSize  = 100
	appPermMaxPages  = 5
	appPermMaxInstal = 10
	appPermCacheMax  = 256
)

type appPermEntry struct {
	can bool
	at  time.Time
}

var (
	appPermMu    sync.Mutex
	appPermCache = map[string]appPermEntry{}
	appPermNow   = time.Now
)

// githubAppUserCanMerge reports whether an App user token is positively known to be able to merge
// in repo ("owner/name").
func githubAppUserCanMerge(token, repo string) bool {
	if !strings.HasPrefix(token, "ghu_") {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	ck := hex.EncodeToString(sum[:]) + "|" + strings.ToLower(repo)
	now := appPermNow()
	appPermMu.Lock()
	if e, ok := appPermCache[ck]; ok && now.Sub(e.at) < appPermTTL {
		appPermMu.Unlock()
		return e.can
	}
	appPermMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), appPermTimeout)
	defer cancel()
	can, definitive := githubAppUserCanWriteContents(ctx, token, repo)
	if definitive {
		appPermMu.Lock()
		if len(appPermCache) >= appPermCacheMax {
			appPermCache = map[string]appPermEntry{}
		}
		appPermCache[ck] = appPermEntry{can: can, at: now}
		appPermMu.Unlock()
	}
	return can
}

// githubAppUserCanWriteContents does the reads. definitive is false when any step failed without
// a positive answer, so a transient error is neither cached nor read as "no".
func githubAppUserCanWriteContents(ctx context.Context, token, repo string) (can, definitive bool) {
	var writers []int64
	unknownShape := false
	for page := 1; page <= appPermMaxPages; page++ {
		var res struct {
			// A pointer to the slice: null or absent is not "zero installations".
			Installations *[]struct {
				ID          int64             `json:"id"`
				Permissions *map[string]string `json:"permissions"`
			} `json:"installations"`
		}
		if err := githubGetAppJSON(ctx, token, fmt.Sprintf("%s/user/installations?per_page=%d&page=%d", githubAPIBase, appPermPageSize, page), &res); err != nil {
			return false, false
		}
		if res.Installations == nil {
			return false, false // not the documented shape: unknown
		}
		for _, in := range *res.Installations {
			if in.Permissions == nil || *in.Permissions == nil {
				unknownShape = true // no permissions map at all: unknown, not "no contents"
			} else if (*in.Permissions)["contents"] == "write" {
				writers = append(writers, in.ID)
			}
		}
		if len(*res.Installations) < appPermPageSize {
			break
		}
		if page == appPermMaxPages {
			return false, false // more installations than we read: unknown
		}
	}
	definitive = !unknownShape
	if len(writers) > appPermMaxInstal {
		writers = writers[:appPermMaxInstal]
		definitive = false // installations left unchecked: a "no" would be a guess
	}
	for _, id := range writers {
		found := false
		for page := 1; page <= appPermMaxPages; page++ {
			var res struct {
				Repositories *[]struct {
					FullName    string `json:"full_name"`
					Permissions *struct {
						Admin    bool `json:"admin"`
						Maintain bool `json:"maintain"`
						Push     bool `json:"push"`
					} `json:"permissions"`
				} `json:"repositories"`
			}
			if err := githubGetAppJSON(ctx, token, fmt.Sprintf("%s/user/installations/%d/repositories?per_page=%d&page=%d", githubAPIBase, id, appPermPageSize, page), &res); err != nil {
				definitive = false
				break
			}
			if res.Repositories == nil {
				definitive = false // not the documented shape: unknown
				break
			}
			for _, r := range *res.Repositories {
				if strings.EqualFold(r.FullName, repo) {
					found = true
					if r.Permissions == nil {
						definitive = false // listed without the user's permissions: unknown
					} else if p := r.Permissions; p.Push || p.Maintain || p.Admin {
						return true, true
					}
				}
			}
			if found || len(*res.Repositories) < appPermPageSize {
				break
			}
			if page == appPermMaxPages {
				definitive = false // not all pages read: unknown
			}
		}
	}
	return false, definitive
}

func githubGetAppJSON(ctx context.Context, token, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	gitx.GithubHeaders(req, token)
	resp, err := workItemHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github answered %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into)
}
