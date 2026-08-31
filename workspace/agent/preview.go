package main

import (
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// handlePreview reverse-proxies /proxy/{port}/{rest...} to a service the user
// started inside the container on 127.0.0.1:{port} — e.g. a Spring Boot app or a
// dev server — so it can be previewed through the Console. The Control Plane is
// the only caller (it gates auth and adds X-Forwarded-*); here we just validate
// the port, strip the /proxy/{port} prefix, and forward. Each container sits on
// its own network and the agent shares the container's netns, so localhost
// reaches the user's process without publishing any extra host port.
func handlePreview(w http.ResponseWriter, r *http.Request) {
	portStr := r.PathValue("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_port", "preview port must be 1..65535")
		return
	}
	// Same guard as the browser pane: the default 7700 AND the actual AGENT_ADDR port
	// (they differ when the agent listens elsewhere) — forwarding here would loop.
	if reservedBrowserAgentPort(port) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_port", "this port is the workspace agent")
		return
	}

	host := "127.0.0.1:" + portStr
	prefix := "/proxy/" + portStr
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = host
			p := strings.TrimPrefix(pr.In.URL.Path, prefix)
			if p == "" {
				p = "/"
			}
			pr.Out.URL.Path = p
			pr.Out.URL.RawPath = "" // let net/http re-encode from Path
			pr.Out.Host = host
			// The CP↔Agent bearer is for us, not the previewed app. Drop it so the
			// user's process never sees the internal token.
			pr.Out.Header.Del("Authorization")
			// ★ Re-apply the CP's X-Forwarded-Host / -Proto by hand. In Rewrite mode
			// ReverseProxy DELETES Forwarded / X-Forwarded-For / -Host / -Proto from
			// Out before calling us (they are client-provided as far as it knows), so
			// "we just don't touch them and they pass through" is false — measured on
			// go1.26: only X-Forwarded-Prefix survives, because it is not on that list.
			//
			// That silent drop is not cosmetic. Next.js validates Server Actions by
			// comparing the Origin header against x-forwarded-host and answers 403 when
			// they disagree; with the header gone, every Server Action behind a preview
			// fails. Spring Boot's forward-headers-strategy loses the public host the
			// same way (docs/log/81 §2.5 (c), ADR 0062 決定 9).
			for _, h := range []string{"X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-For"} {
				if v := pr.In.Header.Get(h); v != "" {
					pr.Out.Header.Set(h, v)
				}
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.WriteErr(w, http.StatusBadGateway, "preview_unreachable",
				"nothing is listening on 127.0.0.1:"+portStr+" inside the workspace")
		},
	}
	rp.ServeHTTP(w, r)
}
