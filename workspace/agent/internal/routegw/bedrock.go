package routegw

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// The Bedrock upstream. A session's harness speaks Bedrock's own wire to the gateway with the
// route token as its bearer (Claude Code's AWS_BEARER_TOKEN_BEDROCK), and the gateway signs the
// same request with the profile's credentials and forwards it. Only four operations pass, all of
// which need bedrock:InvokeModel* and nothing else (ADR 0128 decision 11, option C2): anything
// else, including listing models, is refused here whatever the profile could do.

const (
	// maxRequestBody bounds a request the gateway buffers: SigV4 signs the payload hash, so the
	// body is held once. A long conversation with images stays well under it.
	maxRequestBody = 32 << 20
	bedrockService = "bedrock"
)

var (
	opPathRe = regexp.MustCompile(`^/model/([^/]+)/(invoke|invoke-with-response-stream|converse|converse-stream)$`)
	// modelIDRe is a model id, an inference profile id or an ARN of either, after unescaping.
	modelIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,299}$`)
)

func bedrockEndpoint(region string) string {
	return "https://bedrock-runtime." + region + ".amazonaws.com"
}

// upstreamClient has no overall timeout, because a streamed answer can run for minutes; the
// request's own context (the caller going away) is what ends a call.
func upstreamClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func writeRefusal(w http.ResponseWriter, r *refusal) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-Errortype", r.code)
	w.WriteHeader(r.status)
	// "message" is the key AWS SDKs read from an error body.
	b, _ := json.Marshal(map[string]string{"message": r.msg, "code": r.code})
	_, _ = w.Write(b)
}

// ServeHTTP handles one model call.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m := opPathRe.FindStringSubmatch(r.URL.EscapedPath())
	if m == nil || r.Method != http.MethodPost {
		writeRefusal(w, &refusal{http.StatusNotFound, "route_operation_not_allowed",
			"the route gateway forwards only POST /model/<id>/{invoke,invoke-with-response-stream,converse,converse-stream}"})
		return
	}
	modelID, err := url.PathUnescape(m[1])
	if err != nil || !modelIDRe.MatchString(modelID) {
		writeRefusal(w, &refusal{http.StatusBadRequest, "route_bad_model", "the model id is not one Bedrock accepts"})
		return
	}
	op := m[2]
	token, ok := bearer(r)
	if !ok {
		writeRefusal(w, &refusal{http.StatusUnauthorized, "route_token_invalid", "a route token is required (Authorization: Bearer)"})
		return
	}
	c, ref := g.authorize(token)
	if ref != nil {
		log.Printf("route gateway: refused (%s)", ref.code)
		writeRefusal(w, ref)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeRefusal(w, &refusal{http.StatusRequestEntityTooLarge, "route_request_too_large", "the request is larger than the gateway forwards"})
		return
	}
	creds, ref := g.credentials(c)
	if ref != nil {
		log.Printf("route gateway: refused (%s)", ref.code)
		writeRefusal(w, ref)
		return
	}

	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, g.d.Endpoint(c.conn.region)+r.URL.EscapedPath(), bytes.NewReader(body))
	if err != nil {
		writeRefusal(w, &refusal{http.StatusBadGateway, "route_upstream_unavailable", "could not build the upstream request"})
		return
	}
	// Only these headers go upstream. The route token is the caller's Authorization and is never
	// forwarded; the credential signs a fresh one.
	up.Header.Set("Content-Type", firstNonEmpty(r.Header.Get("Content-Type"), "application/json"))
	if a := r.Header.Get("Accept"); a != "" {
		up.Header.Set("Accept", a)
	}
	for k, v := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amzn-bedrock-") {
			up.Header[k] = v
		}
	}
	up.Header.Set("User-Agent", "agent-fleet-route-gateway")
	signV4(up, body, creds, bedrockService, c.conn.region, g.d.Now())

	start := g.d.Now()
	resp, err := g.d.Client.Do(up)
	if err != nil {
		log.Printf("route gateway: upstream unreachable (%s)", c.conn.region)
		writeRefusal(w, &refusal{http.StatusBadGateway, "route_upstream_unavailable", "Amazon Bedrock could not be reached from this Workspace"})
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		l := strings.ToLower(k)
		if l == "content-type" || l == "content-length" || l == "retry-after" || strings.HasPrefix(l, "x-amzn-") || strings.HasPrefix(l, "x-amz-") {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(resp.StatusCode)
	obs := newUsageObserver(op)
	copyErr := streamCopy(w, resp.Body, obs)
	g.record(c, modelID, op, resp, obs, copyErr == nil && resp.StatusCode < 400, start)
}

// streamCopy forwards the body and flushes after every read, so an event reaches the harness
// when Bedrock sends it. The observer sees each chunk after it has been written.
func streamCopy(w http.ResponseWriter, body io.Reader, obs *usageObserver) error {
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if fl != nil {
				fl.Flush()
			}
			_, _ = obs.Write(buf[:n])
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// record writes the call's ledger row (ADR 0128 decision 8): the gateway sees every call, so it
// is where Route=bedrock and Billing=metered are known. Counts only, never text. A ledger that
// cannot be written is logged and never fails the call.
func (g *Gateway) record(c *call, modelID, op string, resp *http.Response, obs *usageObserver, ok bool, start time.Time) {
	u := obs.Finish(op, resp.Header)
	measured := usagex.MeasuredNone
	if u.Known {
		measured = usagex.MeasuredExact
	}
	row := usagex.Record{
		TS:      g.d.Now().UTC().Format(time.RFC3339),
		Call:    chatx.RandUUID(),
		Feature: usagex.FeatureRouteGateway,
		Trigger: usagex.TriggerUser,
		Kind:    c.meta.Kind,
		Model:   modelID, ModelRaw: modelID, ModelSrc: usagex.ModelReported,
		Route: usagex.RouteBedrock, Billing: usagex.BillingMetered,
		Ref: c.b.Session,
		In:  u.In, Out: u.Out, CacheRead: u.CacheRead, CacheCreate: u.CacheCreate,
		Spend: usagex.Spend(u.In, u.CacheCreate, u.Out),
		MS:    int(g.d.Now().Sub(start) / time.Millisecond),
		OK:    ok, Measured: measured,
	}
	if err := g.d.Append([]usagex.Record{row}); err != nil {
		log.Printf("route gateway: ledger: %v", err)
	}
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	t := strings.TrimSpace(h[7:])
	return t, t != ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
