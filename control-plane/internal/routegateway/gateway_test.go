package routegateway

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routeprice"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	prTestRole   = "arn:aws:iam::123456789012:role/af-model-route/bedrock"
	prTestModel  = "us.anthropic.claude-opus-5-5"
	prTestModel2 = "us.anthropic.claude-haiku-5-5"
)

// fakeAssumer stands in for STS: no test calls AWS.
type fakeAssumer struct {
	mu   sync.Mutex
	ins  []AssumeInput
	now  func() time.Time
	fail error
}

func (f *fakeAssumer) Assume(_ context.Context, in AssumeInput) (aws.Credentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ins = append(f.ins, in)
	if f.fail != nil {
		return aws.Credentials{}, f.fail
	}
	return aws.Credentials{AccessKeyID: "AKIDROUTETEST", SecretAccessKey: "secret", SessionToken: "session-token-for-" + in.SessionName,
		CanExpire: true, Expires: f.now().Add(time.Hour)}, nil
}

func (f *fakeAssumer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ins)
}

// fakeBedrock is the stand-in upstream. mode picks the answer; every request is recorded.
type fakeBedrock struct {
	mu      sync.Mutex
	reqs    []*http.Request
	bodies  []string
	mode    string
	release chan struct{} // the "slow" mode holds the stream open until this closes
	srv     *httptest.Server
}

func (f *fakeBedrock) setMode(m string) {
	f.mu.Lock()
	f.mode = m
	f.mu.Unlock()
}

func (f *fakeBedrock) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func evMsg(headers [][2]string, payload []byte) []byte {
	var h []byte
	for _, kv := range headers {
		h = append(h, byte(len(kv[0])))
		h = append(h, kv[0]...)
		h = append(h, 7)
		h = binary.BigEndian.AppendUint16(h, uint16(len(kv[1])))
		h = append(h, kv[1]...)
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(12+len(h)+len(payload)+4))
	out = binary.BigEndian.AppendUint32(out, uint32(len(h)))
	out = append(out, 0, 0, 0, 0)
	out = append(out, h...)
	out = append(out, payload...)
	return append(out, 0, 0, 0, 0)
}

func evChunk(event string) []byte {
	p, _ := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(event))})
	return evMsg([][2]string{{":event-type", "chunk"}, {":message-type", "event"}}, p)
}

var (
	evStart = evChunk(`{"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`)
	evDelta = evChunk(`{"type":"message_delta","usage":{"output_tokens":200}}`)
	evStop  = evChunk(`{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":1000,"outputTokenCount":200}}`)
)

func newFakeBedrock(t *testing.T) *fakeBedrock {
	f := &fakeBedrock{mode: "ok", release: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, r)
		f.bodies = append(f.bodies, string(b))
		mode := f.mode
		f.mu.Unlock()
		fl := w.(http.Flusher)
		switch mode {
		case "throttle":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Amzn-Errortype", "ThrottlingException")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"Too many requests, please wait before trying again."}`))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Header().Set("X-Amzn-Requestid", "req-1")
		w.Header().Set("Set-Cookie", "upstream=1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(evStart)
		fl.Flush()
		_, _ = w.Write(evDelta)
		fl.Flush()
		switch mode {
		case "cut":
			// Closed after message_delta: no closing metrics.
			return
		case "slow":
			select {
			case <-f.release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(evStop)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type prEnv struct {
	t       *testing.T
	st      *store.SQL
	g       *Gateway
	srv     *httptest.Server
	up      *fakeBedrock
	assumer *fakeAssumer
	tenant  store.Tenant
	mid     string
	now     atomic.Pointer[time.Time]
	signKey []byte
	epoch   atomic.Int64
	connMu  sync.Mutex
	conns   map[string]map[*int]context.CancelFunc
}

func (e *prEnv) keyAt(epoch int64) []byte {
	if epoch == 0 {
		return e.signKey
	}
	return append(append([]byte{}, e.signKey...), byte(epoch))
}

// cancel ends the membership's tracked requests, as a member removal does.
func (e *prEnv) cancel(mid string) int {
	e.connMu.Lock()
	fns := e.conns[mid]
	delete(e.conns, mid)
	e.connMu.Unlock()
	for _, f := range fns {
		f()
	}
	return len(fns)
}

func (e *prEnv) clock() time.Time { return *e.now.Load() }

func newPREnv(t *testing.T) *prEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e := &prEnv{t: t, st: st}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	e.now.Store(&now)
	tn, err := st.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	e.tenant = tn
	ident, err := st.UpsertIdentity(ctx, "m@x", "key-m", "")
	if err != nil {
		t.Fatal(err)
	}
	e.mid = store.NewID()
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO membership(id, identity_id, tenant_id, role, status, created_at) VALUES(?,?,?,?, 'active', ?)`,
		e.mid, ident.ID, tn.ID, "member", store.NowTS()); err != nil {
		t.Fatal(err)
	}
	e.signKey = SignKey([]byte(strings.Repeat("r", 32)))
	e.conns = map[string]map[*int]context.CancelFunc{}
	e.assumer = &fakeAssumer{now: e.clock}
	e.up = newFakeBedrock(t)
	e.g = New(Deps{
		Store:       st,
		KeyAt:       e.keyAt,
		BridgeEpoch: func(context.Context, string) (int64, bool) { return e.epoch.Load(), true },
		VerifyIssuer: func(_ context.Context, tok string) (string, int64, bool) {
			ep := e.epoch.Load()
			mid, ok := VerifyIssueToken(e.keyAt(ep), tok)
			return mid, ep, ok
		},
		Track: func(ctx context.Context, mid string) (context.Context, func()) {
			ctx, cancel := context.WithCancel(ctx)
			id := new(int)
			e.connMu.Lock()
			if e.conns[mid] == nil {
				e.conns[mid] = map[*int]context.CancelFunc{}
			}
			e.conns[mid][id] = cancel
			e.connMu.Unlock()
			return ctx, func() {
				e.connMu.Lock()
				delete(e.conns[mid], id)
				e.connMu.Unlock()
				cancel()
			}
		},
		Assumer: e.assumer,
		Config:  Config{MaxInFlight: 4, Deadline: time.Minute, Drain: 200 * time.Millisecond},
	})
	e.g.now = e.clock
	e.g.endpoint = func(string) string { return e.up.srv.URL }
	e.register(prTestRole, "us-east-1", true, prTestModel)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/route/token", e.g.Issue)
	mux.HandleFunc(Prefix+"/{path...}", e.g.Serve)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	// Granted, with a ceiling and a member default of $100 each.
	if err := st.SetProvidedRouteGrant(ctx, tn.ID, providedroutes.SourceDeployment, true, ptrI64(100_000_000)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProvidedRouteMemberPolicy(ctx, tn.ID, providedroutes.SourceDeployment, false, 100_000_000); err != nil {
		t.Fatal(err)
	}
	return e
}

func ptrI64(v int64) *int64 { return &v }

func (e *prEnv) register(role, region string, active bool, models ...string) {
	e.t.Helper()
	if _, err := e.g.PutDeploymentSource(context.Background(), SourceInput{RoleARN: role, Region: region, Models: models, Active: &active}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *prEnv) source() store.ProvidedRouteSource {
	e.t.Helper()
	rows, err := e.st.ListProvidedRouteSources(context.Background(), "")
	if err != nil || len(rows) != 1 {
		e.t.Fatalf("deployment sources = %v %v", rows, err)
	}
	return rows[0]
}

func (e *prEnv) token(binding string, ttl time.Duration) string {
	return MintToken(e.keyAt(e.epoch.Load()), Claims{MembershipID: e.mid, Session: "s1", Incarnation: "i1",
		Route: providedroutes.RouteDeployment, Binding: binding, Expires: e.clock().Add(ttl)})
}

const prStreamBody = `{"anthropic_version":"bedrock-2023-05-31","max_tokens":1000,"messages":[{"role":"user","content":"hi"}]}`

func (e *prEnv) call(tok, path, body string, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+Prefix+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func streamPath(model string) string {
	return "/model/" + model + "/invoke-with-response-stream"
}

func (e *prEnv) callRow(id string) store.ProvidedRouteCall {
	e.t.Helper()
	c, err := e.st.GetProvidedRouteCall(context.Background(), id)
	if err != nil {
		e.t.Fatalf("call %s: %v", id, err)
	}
	return c
}

func (e *prEnv) memberPeriod() store.ProvidedRoutePeriod {
	p, err := e.st.GetProvidedRoutePeriod(context.Background(), e.tenant.ID, e.mid, providedroutes.SourceDeployment, "2026-11")
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func reservationOf(t *testing.T, model string, outCap int64) int64 {
	m, ok := routeprice.Lookup(model)
	if !ok {
		t.Fatalf("no row for %s", model)
	}
	r, err := m.UpperBoundMicroUSD(outCap)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A complete stream is signed under the route role for this membership, streamed through, and
// settled once at its priced terminal totals; the credentials are cached for the next call.
func TestProvidedRouteGatewayCompleteStream(t *testing.T) {
	e := newPREnv(t)
	src := e.source()
	if !src.Active || src.Regions[0] != "us-east-1" || src.AllowList[0] != prTestModel {
		t.Fatalf("deployment source = %+v", src)
	}
	tok := e.token(src.Binding(), TokenTTL)
	resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, map[string]string{
		"Accept": "application/vnd.amazon.eventstream", "X-Amzn-Bedrock-Accept": "application/json", "User-Agent": "claude-cli/2"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if string(body) != string(evStart)+string(evDelta)+string(evStop) {
		t.Fatalf("the stream was not passed through as it is (%d bytes)", len(body))
	}
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("X-Amzn-Requestid") != "req-1" {
		t.Fatalf("answer headers = %v", resp.Header)
	}
	callID := resp.Header.Get(CallHeader)
	if callID == "" {
		t.Fatal("no call id on the answer")
	}
	// What reached Bedrock: the SDK's path, a fresh SigV4 for the role session, the caller's
	// content type and accept headers, and nothing of the caller's Authorization.
	up := e.up.reqs[0]
	if up.URL.EscapedPath() != "/model/"+prTestModel+"/invoke-with-response-stream" || up.URL.RawQuery != "" {
		t.Fatalf("upstream path = %s ? %s", up.URL.EscapedPath(), up.URL.RawQuery)
	}
	auth := up.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDROUTETEST/20261108/us-east-1/bedrock/aws4_request") || strings.Contains(auth, "afr_") {
		t.Fatalf("upstream Authorization = %q", auth)
	}
	if up.Header.Get("X-Amz-Security-Token") != "session-token-for-"+e.mid || up.Header.Get("X-Amzn-Bedrock-Accept") != "application/json" ||
		up.Header.Get("User-Agent") != "agent-fleet-cp-route-gateway" {
		t.Fatalf("upstream headers = %v", up.Header)
	}
	if e.up.bodies[0] != prStreamBody {
		t.Fatalf("upstream body = %s", e.up.bodies[0])
	}
	in := e.assumer.ins[0]
	if in.RoleARN != prTestRole || in.SessionName != e.mid || in.Tags["af-membership"] != e.mid || in.Tags["af-tenant"] != "acme" ||
		len(in.Tags) != 2 || in.ExternalID != "" || in.Region != "us-east-1" || !strings.Contains(in.Policy, "inference-profile/"+prTestModel) {
		t.Fatalf("assume input = %+v", in)
	}
	row := e.callRow(callID)
	m, _ := routeprice.Lookup(prTestModel)
	want := m.PriceMicroUSD(routeprice.Usage{In: 1000, Out: 200})
	if row.State != providedroutes.StateSettled || row.FinalMicro == nil || *row.FinalMicro != want || row.ReservedMicro != reservationOf(t, prTestModel, 1000) ||
		row.Binding != src.Binding() || row.Model != prTestModel || row.API != providedroutes.OpInvokeStream || row.Month != "2026-11" {
		t.Fatalf("call row = %+v, want settled at %d", row, want)
	}
	if p := e.memberPeriod(); p.SpentMicro != want || p.ReservedMicro != 0 {
		t.Fatalf("member period = %+v", p)
	}
	// The second call takes the cached session.
	if resp, _ := e.call(tok, streamPath(prTestModel), prStreamBody, nil); resp.StatusCode != 200 || e.assumer.count() != 1 {
		t.Fatalf("second call: %d, %d AssumeRole calls", resp.StatusCode, e.assumer.count())
	}
	// Five minutes before the session's end it is replaced.
	later := e.clock().Add(56 * time.Minute)
	e.now.Store(&later)
	tok = e.token(src.Binding(), TokenTTL)
	if resp, _ := e.call(tok, streamPath(prTestModel), prStreamBody, nil); resp.StatusCode != 200 || e.assumer.count() != 2 {
		t.Fatalf("near expiry: %d, %d AssumeRole calls", resp.StatusCode, e.assumer.count())
	}
}

// A stream cut before its closing metrics, and a stream the member's removal ends, settle at
// the reservation: message_start and message_delta are never a settlement.
func TestProvidedRouteGatewayCutStreamSettlesAtReservation(t *testing.T) {
	e := newPREnv(t)
	tok := e.token(e.source().Binding(), TokenTTL)
	e.up.setMode("cut")
	resp, _ := e.call(tok, streamPath(prTestModel), prStreamBody, nil)
	row := e.callRow(resp.Header.Get(CallHeader))
	if row.State != providedroutes.StateSettled || *row.FinalMicro != row.ReservedMicro {
		t.Fatalf("cut stream row = %+v", row)
	}

	e.up.setMode("slow")
	done := make(chan *http.Response, 1)
	go func() {
		resp, _ := e.call(tok, streamPath(prTestModel), prStreamBody, nil)
		done <- resp
	}()
	waitFor(t, "provided route", func() bool { return e.up.n() == 2 })
	if n := e.cancel(e.mid); n != 1 {
		t.Fatalf("member removal cut %d calls, want 1", n)
	}
	resp = <-done
	waitFor(t, "provided route", func() bool { return e.callRow(resp.Header.Get(CallHeader)).State == providedroutes.StateSettled })
	if row := e.callRow(resp.Header.Get(CallHeader)); *row.FinalMicro != row.ReservedMicro {
		t.Fatalf("removed member's stream row = %+v", row)
	}
}

// Bedrock refusing the call before inference (a whole JSON 429) costs nothing; it is relayed.
func TestProvidedRouteGatewayCleanRefusalSettlesAtZero(t *testing.T) {
	e := newPREnv(t)
	e.up.setMode("throttle")
	resp, body := e.call(e.token(e.source().Binding(), TokenTTL), streamPath(prTestModel), prStreamBody, nil)
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), "Too many requests") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	id := resp.Header.Get(CallHeader)
	// The client has the whole body (it carries a Content-Length) before the handler settles.
	waitFor(t, "settled", func() bool { return e.callRow(id).State == providedroutes.StateSettled })
	if row := e.callRow(id); *row.FinalMicro != 0 {
		t.Fatalf("row = %+v", row)
	}
}

func refusalCode(t *testing.T, resp *http.Response, body []byte) string {
	t.Helper()
	var b struct{ Code, Message string }
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("refusal body %q: %v", body, err)
	}
	if resp.Header.Get("X-Should-Retry") != "false" || resp.Header.Get("X-Amzn-Errortype") != b.Code || b.Message == "" {
		t.Fatalf("refusal headers = %v body %s", resp.Header, body)
	}
	return b.Code
}

// D7's contract gate: each of these is refused and nothing is signed or sent.
func TestProvidedRouteGatewayContractRefusals(t *testing.T) {
	e := newPREnv(t)
	src := e.source()
	good := e.token(src.Binding(), TokenTTL)
	other := MintToken(SignKey([]byte(strings.Repeat("x", 32))), Claims{MembershipID: e.mid, Session: "s1",
		Route: providedroutes.RouteDeployment, Binding: src.Binding(), Expires: e.clock().Add(time.Minute)})
	ctx := context.Background()
	if _, err := e.st.DB().ExecContext(ctx, `UPDATE provided_route_source SET allow_list=? WHERE id=?`,
		`["anthropic.claude-unpriced-x","`+prTestModel+`"]`, src.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, tok, path, body string
		hdr                   map[string]string
		status                int
		code                  string
	}{
		{"forged token", other, streamPath(prTestModel), prStreamBody, nil, 401, "route_token_invalid"},
		{"expired token", e.token(src.Binding(), -time.Second), streamPath(prTestModel), prStreamBody, nil, 401, "route_token_invalid"},
		{"no token", "", streamPath(prTestModel), prStreamBody, nil, 401, "route_token_invalid"},
		{"issuing token as a route token", MintIssueToken(e.signKey, e.mid), streamPath(prTestModel), prStreamBody, nil, 401, "route_token_invalid"},
		{"identity header", good, streamPath(prTestModel), prStreamBody, map[string]string{"X-Af-Membership": "someone-else"}, 400, providedroutes.CodeRequestRefused},
		{"caller tag", good, streamPath(prTestModel), prStreamBody, map[string]string{"X-Amz-Tagging": "af-tenant=other"}, 400, providedroutes.CodeRequestRefused},
		{"caller Region header", good, streamPath(prTestModel), prStreamBody, map[string]string{"X-Aws-Region": "eu-west-1"}, 400, providedroutes.CodeRequestRefused},
		{"caller role header", good, streamPath(prTestModel), prStreamBody, map[string]string{"X-Role-Arn": prTestRole}, 400, providedroutes.CodeRequestRefused},
		{"caller URL in the query", good, streamPath(prTestModel) + "?endpoint=https://evil.example", prStreamBody, nil, 400, providedroutes.CodeRequestRefused},
		{"caller Region in the query", good, streamPath(prTestModel) + "?region=eu-west-1", prStreamBody, nil, 400, providedroutes.CodeRequestRefused},
		{"ARN model id", good, streamPath("arn%3Aaws%3Abedrock%3Aus-east-1%3A111122223333%3Aapplication-inference-profile%2Fx"), prStreamBody, nil, 412, providedroutes.CodeModelNotAllowed},
		{"model outside the list", good, streamPath("us.anthropic.claude-sonnet-5-5"), prStreamBody, nil, 412, providedroutes.CodeModelNotAllowed},
		{"another operation", good, "/model/" + prTestModel + "/count-tokens", prStreamBody, nil, 404, providedroutes.CodeOperationNotAllowed},
		{"listing models", good, "/foundation-models", "", nil, 404, providedroutes.CodeOperationNotAllowed},
		{"missing output cap", good, streamPath(prTestModel), `{"messages":[]}`, nil, 400, providedroutes.CodeOutputCap},
		{"output cap above the model's maximum", good, streamPath(prTestModel), `{"max_tokens":99999999,"messages":[]}`, nil, 400, providedroutes.CodeOutputCap},
		{"unpriced model", good, streamPath("anthropic.claude-unpriced-x"), prStreamBody, nil, 412, providedroutes.CodeModelUnpriced},
	} {
		resp, body := e.call(c.tok, c.path, c.body, c.hdr)
		if resp.StatusCode != c.status || refusalCode(t, resp, body) != c.code {
			t.Errorf("%s: %d %s, want %d %s", c.name, resp.StatusCode, body, c.status, c.code)
		}
	}
	if e.up.n() != 0 || e.assumer.count() != 0 {
		t.Fatalf("a refused call reached upstream (%d) or STS (%d)", e.up.n(), e.assumer.count())
	}
	if rows, _ := e.st.ListInFlightProvidedRouteCalls(ctx, 0); len(rows) != 0 {
		t.Fatalf("a refused call reserved: %+v", rows)
	}
	var n int
	_ = e.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM provided_route_call`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d call rows after refusals", n)
	}
}

// D7's revocation gate, and D3's "no budget, no use": the next call after each change is refused.
func TestProvidedRouteGatewayRevocation(t *testing.T) {
	e := newPREnv(t)
	ctx := context.Background()
	src := e.source()
	tok := e.token(src.Binding(), TokenTTL)

	// Budget zero: a visible 503 naming the budget, the amount used, the level and the reset.
	if err := e.st.SetProvidedRouteMember(ctx, e.tenant.ID, e.mid, providedroutes.SourceDeployment, false, ptrI64(0)); err != nil {
		t.Fatal(err)
	}
	resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, nil)
	if resp.StatusCode != 503 || refusalCode(t, resp, body) != "route_budget_exhausted" ||
		resp.Header.Get("X-Af-Route-Budget-Level") != store.ProvidedRouteRefusedMemberBudget ||
		resp.Header.Get("X-Af-Route-Budget-Reset") != "2026-12-01T00:00:00Z" || !strings.Contains(string(body), "2026-12-01") {
		t.Fatalf("budget zero: %d %v %s", resp.StatusCode, resp.Header, body)
	}

	// A budget below one reservation: refused while some remains, saying what the next call
	// could cost.
	reserve := reservationOf(t, prTestModel, 1000)
	if err := e.st.SetProvidedRouteMember(ctx, e.tenant.ID, e.mid, providedroutes.SourceDeployment, false, ptrI64(reserve-1)); err != nil {
		t.Fatal(err)
	}
	resp, body = e.call(tok, streamPath(prTestModel), prStreamBody, nil)
	if resp.StatusCode != 503 || refusalCode(t, resp, body) != "route_budget_exhausted" || !strings.Contains(string(body), "the next call could cost up to "+usd(reserve)) {
		t.Fatalf("budget edge: %d %s", resp.StatusCode, body)
	}
	if err := e.st.SetProvidedRouteMember(ctx, e.tenant.ID, e.mid, providedroutes.SourceDeployment, false, nil); err != nil {
		t.Fatal(err)
	}

	// The tenant's ceiling is the other level.
	if err := e.st.SetProvidedRouteGrant(ctx, e.tenant.ID, providedroutes.SourceDeployment, true, ptrI64(reserve-1)); err != nil {
		t.Fatal(err)
	}
	resp, _ = e.call(tok, streamPath(prTestModel), prStreamBody, nil)
	if resp.StatusCode != 503 || resp.Header.Get("X-Af-Route-Budget-Level") != store.ProvidedRouteRefusedTenantCeiling {
		t.Fatalf("ceiling: %d %v", resp.StatusCode, resp.Header)
	}

	// Members only, and this member not granted.
	if err := e.st.SetProvidedRouteGrant(ctx, e.tenant.ID, providedroutes.SourceDeployment, true, ptrI64(100_000_000)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetProvidedRouteMemberPolicy(ctx, e.tenant.ID, providedroutes.SourceDeployment, true, 100_000_000); err != nil {
		t.Fatal(err)
	}
	if resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, nil); refusalCode(t, resp, body) != "route_member_not_granted" {
		t.Fatalf("members only: %s", body)
	}
	if err := e.st.SetProvidedRouteMember(ctx, e.tenant.ID, e.mid, providedroutes.SourceDeployment, true, nil); err != nil {
		t.Fatal(err)
	}
	if resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, nil); resp.StatusCode != 200 {
		t.Fatalf("granted member: %d %s", resp.StatusCode, body)
	}

	// The super_admin's grant off.
	if err := e.st.SetProvidedRouteGrant(ctx, e.tenant.ID, providedroutes.SourceDeployment, false, nil); err != nil {
		t.Fatal(err)
	}
	if resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, nil); resp.StatusCode != 412 || refusalCode(t, resp, body) != "route_not_granted" {
		t.Fatalf("grant off: %d %s", resp.StatusCode, body)
	}
	if err := e.st.SetProvidedRouteGrant(ctx, e.tenant.ID, providedroutes.SourceDeployment, true, ptrI64(100_000_000)); err != nil {
		t.Fatal(err)
	}

	// A model added to the allow-list is callable at the next call: the policy version moves the
	// credential cache key, so the new session's policy names it.
	e.register(prTestRole, "us-east-1", true, prTestModel, prTestModel2)
	if resp, body := e.call(tok, streamPath(prTestModel2), `{"max_tokens":100,"messages":[]}`, nil); resp.StatusCode != 200 {
		t.Fatalf("added model: %d %s", resp.StatusCode, body)
	}
	last := e.assumer.ins[len(e.assumer.ins)-1]
	if !strings.Contains(last.Policy, "inference-profile/"+prTestModel2) {
		t.Fatalf("the new session's policy = %s", last.Policy)
	}

	// The operator re-points the role: the bound session is refused, never moved.
	e.register("arn:aws:iam::123456789012:role/af-model-route/other", "us-east-1", true, prTestModel)
	if resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, nil); resp.StatusCode != 412 || refusalCode(t, resp, body) != "route_connection_changed" {
		t.Fatalf("re-pointed: %d %s", resp.StatusCode, body)
	}
	// Switched off by the super_admin: deactivated, not deleted.
	e.register("arn:aws:iam::123456789012:role/af-model-route/other", "us-east-1", false, prTestModel)
	tok = e.token(e.source().Binding(), TokenTTL)
	if resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, nil); refusalCode(t, resp, body) != "route_source_inactive" {
		t.Fatalf("unconfigured: %s", body)
	}

	// The membership removed.
	if _, err := e.st.DB().ExecContext(ctx, `UPDATE membership SET status='removed' WHERE id=?`, e.mid); err != nil {
		t.Fatal(err)
	}
	if resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, nil); resp.StatusCode != 401 || refusalCode(t, resp, body) != "route_token_invalid" && refusalCode(t, resp, body) != "route_membership_inactive" {
		t.Fatalf("removed: %d %s", resp.StatusCode, body)
	}
}

// The per-member in-flight cap refuses while calls are running, with a code waiting clears.
func TestProvidedRouteGatewayInFlightCap(t *testing.T) {
	e := newPREnv(t)
	e.g.cfg.MaxInFlight = 1
	tok := e.token(e.source().Binding(), TokenTTL)
	e.up.setMode("slow")
	done := make(chan struct{})
	go func() { e.call(tok, streamPath(prTestModel), prStreamBody, nil); close(done) }()
	waitFor(t, "provided route", func() bool { return e.up.n() == 1 })
	resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, nil)
	if resp.StatusCode != 503 || refusalCode(t, resp, body) != "route_in_flight_cap" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	close(e.up.release)
	<-done
}

// The orphan sweep settles at the reservation what no CP is finishing, once past the deadline;
// a draining CP refuses new calls and settles what it cut.
func TestProvidedRouteGatewayOrphansAndDrain(t *testing.T) {
	e := newPREnv(t)
	ctx := context.Background()
	src := e.source()
	admit := func(id string, at time.Time) {
		adm, err := e.st.AdmitProvidedRouteCall(ctx, store.ProvidedRouteCall{CallID: id, MembershipID: e.mid, TenantID: e.tenant.ID,
			Source: providedroutes.SourceDeployment, Binding: src.Binding(), Model: prTestModel, API: providedroutes.OpInvokeStream,
			PriceVersion: routeprice.Version(), ReservedMicro: 1234, AdmittedAt: at.Format(time.RFC3339Nano), CPInstance: "dead-cp"},
			store.ProvidedRouteLimits{MemberBudgetMicro: 100_000_000})
		if err != nil || !adm.Admitted {
			t.Fatalf("admit %s: %+v %v", id, adm, err)
		}
	}
	admit("old", e.clock().Add(-e.g.cfg.Deadline-routeOrphanGrace-time.Second))
	admit("young", e.clock().Add(-time.Second))
	if n := e.g.Sweep(ctx); n != 1 {
		t.Fatalf("sweep settled %d, want 1", n)
	}
	if c := e.callRow("old"); c.State != providedroutes.StateSettled || *c.FinalMicro != 1234 {
		t.Fatalf("orphan = %+v", c)
	}
	if c := e.callRow("young"); c.State != providedroutes.StateInFlight {
		t.Fatalf("a young call was settled: %+v", c)
	}
	// A second sweep changes nothing.
	if n := e.g.Sweep(ctx); n != 0 {
		t.Fatalf("second sweep settled %d", n)
	}

	tok := e.token(src.Binding(), TokenTTL)
	e.up.setMode("slow")
	got := make(chan *http.Response, 1)
	go func() { resp, _ := e.call(tok, streamPath(prTestModel), prStreamBody, nil); got <- resp }()
	waitFor(t, "provided route", func() bool { return e.up.n() == 1 })
	var callID string
	e.g.callMu.Lock()
	for id := range e.g.calls {
		callID = id
	}
	e.g.callMu.Unlock()
	e.g.Drain()
	if c := e.callRow(callID); c.State != providedroutes.StateSettled || *c.FinalMicro != c.ReservedMicro {
		t.Fatalf("drained call = %+v", c)
	}
	resp, body := e.call(tok, streamPath(prTestModel), prStreamBody, nil)
	if resp.StatusCode != 503 || refusalCode(t, resp, body) != "route_draining" {
		t.Fatalf("after drain: %d %s", resp.StatusCode, body)
	}
	close(e.up.release)
	<-got
	// The cut call finishing later does not settle it again.
	waitFor(t, "provided route", func() bool { e.g.callMu.Lock(); defer e.g.callMu.Unlock(); return len(e.g.calls) == 0 })
	if c := e.callRow(callID); *c.FinalMicro != c.ReservedMicro {
		t.Fatalf("settled twice: %+v", c)
	}
}

// admitDrainingStore starts the drain while the admission row is being written: the window in
// which a Drain snapshot cannot see the call yet.
type admitDrainingStore struct {
	Store
	g *Gateway
}

func (s admitDrainingStore) AdmitProvidedRouteCall(ctx context.Context, c store.ProvidedRouteCall, lim store.ProvidedRouteLimits) (store.ProvidedRouteAdmission, error) {
	adm, err := s.Store.AdmitProvidedRouteCall(ctx, c, lim)
	s.g.draining.Store(true)
	return adm, err
}

// A call admitted while a drain starts is settled at once, never dispatched, and does not wait
// for the orphan sweep.
func TestProvidedRouteGatewayAdmittedDuringDrain(t *testing.T) {
	e := newPREnv(t)
	e.g.d.Store = admitDrainingStore{Store: e.st, g: e.g}
	resp, body := e.call(e.token(e.source().Binding(), TokenTTL), streamPath(prTestModel), prStreamBody, nil)
	if resp.StatusCode != 503 || refusalCode(t, resp, body) != "route_draining" {
		t.Fatalf("admitted during drain: %d %s", resp.StatusCode, body)
	}
	if e.up.n() != 0 || e.assumer.count() != 0 {
		t.Fatalf("a call admitted during the drain was dispatched (%d upstream, %d STS)", e.up.n(), e.assumer.count())
	}
	left, err := e.st.ListInFlightProvidedRouteCalls(context.Background(), 0)
	if err != nil || len(left) != 0 {
		t.Fatalf("left in flight: %+v %v", left, err)
	}
	if p := e.memberPeriod(); p.SpentMicro != 0 || p.ReservedMicro != 0 {
		t.Fatalf("an undispatched call cost the member: %+v", p)
	}
}

// The token endpoint mints on the Agent's issuing token after the same checks a call runs, and
// what it mints is accepted by the gateway.
func TestProvidedRouteTokenIssue(t *testing.T) {
	e := newPREnv(t)
	src := e.source()
	issue := func(auth, binding string) (*http.Response, []byte) {
		b, _ := json.Marshal(map[string]string{"session": "s1", "incarnation": "i1", "route": providedroutes.RouteDeployment, "binding": binding})
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/internal/route/token", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}
	issuer := MintIssueToken(e.signKey, e.mid)
	resp, body := issue(issuer, src.Binding())
	if resp.StatusCode != 200 {
		t.Fatalf("issue: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(body, &out)
	c, ok := VerifyToken(e.signKey, out.Token, e.clock())
	if !ok || c.MembershipID != e.mid || c.Session != "s1" || c.Incarnation != "i1" || c.Binding != src.Binding() ||
		!c.Expires.Equal(e.clock().Add(TokenTTL).Truncate(time.Second)) {
		t.Fatalf("minted claims = %+v %v", c, ok)
	}
	if resp, body := e.call(out.Token, streamPath(prTestModel), prStreamBody, nil); resp.StatusCode != 200 {
		t.Fatalf("minted token refused: %d %s", resp.StatusCode, body)
	}
	good := `{"session":"s1","incarnation":"i1","route":"` + providedroutes.RouteDeployment + `","binding":"` + src.Binding() + `"}`
	for _, raw := range []string{good + ` {"session":"s2"}`, good + `x`,
		`{"session":"s1","incarnation":"i1","route":"` + providedroutes.RouteDeployment + `","binding":"` + src.Binding() + `","model":"x"}`} {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/internal/route/token", strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+issuer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if refusalCode(t, resp, b) != "route_bad_request" {
			t.Errorf("%s: %d %s", raw, resp.StatusCode, b)
		}
	}
	if resp, _ := issue(out.Token, src.Binding()); resp.StatusCode != 401 {
		t.Fatalf("a route token bought another: %d", resp.StatusCode)
	}
	if resp, body := issue(issuer, src.ID+"#0000000000000000"); refusalCode(t, resp, body) != "route_connection_changed" {
		t.Fatalf("stale binding: %s", body)
	}
	if err := e.st.SetProvidedRouteGrant(context.Background(), e.tenant.ID, providedroutes.SourceDeployment, false, nil); err != nil {
		t.Fatal(err)
	}
	if resp, body := issue(issuer, src.Binding()); refusalCode(t, resp, body) != "route_not_granted" {
		t.Fatalf("grant off: %s", body)
	}
	// A bridge rotation ends the issuing token.
	e.epoch.Add(1)
	if resp, _ := issue(issuer, src.Binding()); resp.StatusCode != 401 {
		t.Fatalf("a rotated issuing token minted: %d", resp.StatusCode)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The switch is exactly "1".
func TestEnabled(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes"} {
		t.Setenv(EnvSwitch, v)
		if Enabled() {
			t.Fatalf("%q turned the switch on", v)
		}
	}
	t.Setenv(EnvSwitch, "1")
	if !Enabled() {
		t.Fatal("1 left the switch off")
	}
}

// The deployment source: one Region, a buildable closure, and edits that keep the id, bump the
// policy version for a list change and move the fingerprint for a role change.
func TestPutDeploymentSource(t *testing.T) {
	e := newPREnv(t)
	ctx := context.Background()
	before := e.source()
	src, err := e.g.PutDeploymentSource(ctx, SourceInput{RoleARN: prTestRole, Region: "us-east-1", Models: []string{prTestModel, prTestModel2}})
	if err != nil || src.ID != before.ID || src.Fingerprint != before.Fingerprint || src.PolicyVersion != before.PolicyVersion+1 || !src.Active {
		t.Fatalf("edited = %+v %v, before %+v", src, err, before)
	}
	for _, in := range []SourceInput{
		{RoleARN: prTestRole, Region: "us-east-1,us-west-2", Models: []string{prTestModel}},
		{RoleARN: prTestRole, Region: "us-east-1 us-west-2", Models: []string{prTestModel}},
		{RoleARN: prTestRole, Region: "", Models: []string{prTestModel}},
		{RoleARN: prTestRole, Region: "us-east-1"},
		{RoleARN: prTestRole, Region: "us-east-1", Models: []string{"arn:aws:bedrock:us-east-1:1:application-inference-profile/x"}},
		{RoleARN: prTestRole, Region: "us-east-1", Models: []string{"anthropic.claude-unpriced-x"}},
		{RoleARN: "not-an-arn", Region: "us-east-1", Models: []string{prTestModel}},
	} {
		var invalid *InvalidInputError
		if _, err := e.g.PutDeploymentSource(ctx, in); !errors.As(err, &invalid) {
			t.Errorf("%+v: %v, want an InvalidInputError", in, err)
		}
	}
	if got := e.source(); got.PolicyVersion != src.PolicyVersion || len(got.Regions) != 1 {
		t.Fatalf("a refused put changed the source: %+v", got)
	}
	moved, err := e.g.PutDeploymentSource(ctx, SourceInput{RoleARN: "arn:aws:iam::123456789012:role/af-model-route/other", Region: "us-east-1", Models: []string{prTestModel}})
	if err != nil || moved.ID != before.ID || moved.Fingerprint == before.Fingerprint || !moved.Active {
		t.Fatalf("re-pointed = %+v %v", moved, err)
	}
}
