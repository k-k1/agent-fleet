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
	if port == 7700 { // the agent itself — forwarding here would loop
		httpx.WriteErr(w, http.StatusBadRequest, "bad_port", "port 7700 is the workspace agent")
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
			// user's process never sees the internal token. X-Forwarded-* set by the
			// CP are preserved (we deliberately do not call SetXForwarded, which would
			// rewrite Host/Proto with the agent hop).
			pr.Out.Header.Del("Authorization")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.WriteErr(w, http.StatusBadGateway, "preview_unreachable",
				"nothing is listening on 127.0.0.1:"+portStr+" inside the workspace")
		},
	}
	rp.ServeHTTP(w, r)
}
