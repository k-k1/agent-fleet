// runtime_kubernetes_client.go — the kubernetes adapter's API client: the standard
// library's HTTP client against the in-cluster API server, with no client-go (ADR 0106
// decision 10). It knows how to authenticate, verify the server and turn a Status reply
// into an error; the kinds and the fields the adapter uses are in
// runtime_kubernetes_types.go.
package runtime

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// The in-cluster service account files. The token is a projected token the kubelet
// rotates, which is why kubeClient re-reads it instead of holding the boot-time value.
// Variables only so the factory test can point them at files of its own.
var (
	kubeSATokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	kubeSACAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// kubeTokenMaxAge bounds how long a token read from the file is reused. The kubelet
// refreshes the file at 80% of the token's lifetime (an hour by default) and the old
// token stays valid until it expires, so a minute is far inside the overlap; the 401
// retry covers a file replaced sooner than that.
const kubeTokenMaxAge = time.Minute

// kubeClient talks to one API server as one identity.
type kubeClient struct {
	base      string // https://host:port, no trailing slash
	tokenFile string
	hc        *http.Client
	now       func() time.Time

	mu     sync.Mutex
	token  string
	readAt time.Time
}

// newInClusterKubeClient builds the client from what Kubernetes gives every pod: the
// KUBERNETES_SERVICE_* variables, the service account's CA and its token file. Outside
// a pod it fails, on purpose: ADR 0106 decision 8 runs the CP inside the cluster, and a
// CP that silently talked to some other API server would be the wrong substrate.
func newInClusterKubeClient() (*kubeClient, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("kubernetes runtime: KUBERNETES_SERVICE_HOST/PORT are unset; the control plane must run inside the cluster")
	}
	ca, err := os.ReadFile(kubeSACAFile)
	if err != nil {
		return nil, fmt.Errorf("kubernetes runtime: read service account CA: %w", err)
	}
	return newKubeClient("https://"+net.JoinHostPort(host, port), ca, kubeSATokenFile)
}

// newKubeClient verifies the server against caPEM alone, never the system roots: the
// API server's certificate is issued by the cluster's own CA.
func newKubeClient(base string, caPEM []byte, tokenFile string) (*kubeClient, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("kubernetes runtime: no certificate in the CA bundle")
	}
	tr := &http.Transport{
		Proxy:               nil, // the API server is in-cluster; an egress proxy must not see the token
		TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	return &kubeClient{
		base:      strings.TrimRight(base, "/"),
		tokenFile: tokenFile,
		hc:        &http.Client{Transport: tr, Timeout: 30 * time.Second},
		now:       time.Now,
	}, nil
}

// bearer returns the token, re-reading the file when the cached copy is older than
// kubeTokenMaxAge or when fresh is set (the retry after a 401).
func (c *kubeClient) bearer(fresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !fresh && c.token != "" && c.now().Sub(c.readAt) < kubeTokenMaxAge {
		return c.token, nil
	}
	b, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return "", fmt.Errorf("kubernetes runtime: read service account token: %w", err)
	}
	c.token, c.readAt = strings.TrimSpace(string(b)), c.now()
	return c.token, nil
}

// kubeStatusError is a non-2xx answer, carrying the Status object's reason so callers
// can tell "not found" and "conflict" from everything else.
type kubeStatusError struct {
	Code    int
	Reason  string
	Message string
	Method  string
	Path    string
}

func (e *kubeStatusError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Code)
	}
	return fmt.Sprintf("kubernetes %s %s: %d %s: %s", e.Method, e.Path, e.Code, e.Reason, msg)
}

func kubeErrCode(err error) int {
	var se *kubeStatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

func isKubeNotFound(err error) bool { return kubeErrCode(err) == http.StatusNotFound }
func isKubeConflict(err error) bool { return kubeErrCode(err) == http.StatusConflict }

// Content types for the writes the adapter makes. JSON Patch is used where a write
// must replace a whole subtree (the pod template) and be conditional on what the
// adapter just read; a merge patch would merge maps such as nodeSelector key by key
// and keep a key the deployment had removed.
const (
	kubeJSON      = "application/json"
	kubeJSONPatch = "application/json-patch+json"
	// kubeMergePatch is for annotations only: a map whose keys the adapter owns, where a
	// merge is exactly what is meant, and metadata.resourceVersion in the body makes the
	// write conditional (409 on a mismatch).
	kubeMergePatch = "application/merge-patch+json"
)

// do sends one request. body is marshalled once so the 401 retry sends the same bytes;
// out, when not nil, receives the decoded 2xx body.
func (c *kubeClient) do(ctx context.Context, method, path string, query url.Values, contentType string, body any, out any) error {
	var payload []byte
	if body != nil {
		if raw, ok := body.([]byte); ok {
			payload = raw
		} else {
			b, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("kubernetes %s %s: encode: %w", method, path, err)
			}
			payload = b
		}
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		tok, err := c.bearer(attempt > 0)
		if err != nil {
			return err
		}
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", kubeJSON)
		if payload != nil {
			req.Header.Set("Content-Type", contentType)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return fmt.Errorf("kubernetes %s %s: %w", method, path, err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("kubernetes %s %s: read: %w", method, path, err)
		}
		// One retry with a token read afresh: the file may have been rotated after the
		// cached copy was read, and the old token has expired since.
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			se := &kubeStatusError{Code: resp.StatusCode, Method: method, Path: path}
			var st struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			}
			if json.Unmarshal(data, &st) == nil {
				se.Reason, se.Message = st.Reason, st.Message
			}
			return se
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("kubernetes %s %s: decode: %w", method, path, err)
			}
		}
		return nil
	}
}

func (c *kubeClient) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, "", nil, out)
}

func (c *kubeClient) list(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, "", nil, out)
}

func (c *kubeClient) create(ctx context.Context, path string, obj, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, kubeJSON, obj, out)
}

func (c *kubeClient) replace(ctx context.Context, path string, obj, out any) error {
	return c.do(ctx, http.MethodPut, path, nil, kubeJSON, obj, out)
}

func (c *kubeClient) jsonPatch(ctx context.Context, path string, ops []kubePatchOp, out any) error {
	return c.do(ctx, http.MethodPatch, path, nil, kubeJSONPatch, ops, out)
}

func (c *kubeClient) mergePatch(ctx context.Context, path string, patch, out any) error {
	return c.do(ctx, http.MethodPatch, path, nil, kubeMergePatch, patch, out)
}

// delete removes one object. An object already gone is success: every caller is a
// teardown step that a re-run must be able to repeat.
func (c *kubeClient) delete(ctx context.Context, path string) error {
	err := c.do(ctx, http.MethodDelete, path, nil, kubeJSON,
		map[string]any{"kind": "DeleteOptions", "apiVersion": "v1", "propagationPolicy": "Background"}, nil)
	if isKubeNotFound(err) {
		return nil
	}
	return err
}

// deleteIfUnchanged deletes an object only if it still has the UID and resource version
// the caller read; a changed object answers 409. Gone already is success.
func (c *kubeClient) deleteIfUnchanged(ctx context.Context, path, uid, resourceVersion string) error {
	err := c.do(ctx, http.MethodDelete, path, nil, kubeJSON, map[string]any{
		"kind": "DeleteOptions", "apiVersion": "v1", "propagationPolicy": "Background",
		"preconditions": map[string]string{"uid": uid, "resourceVersion": resourceVersion},
	}, nil)
	if isKubeNotFound(err) {
		return nil
	}
	return err
}

// kubePatchOp is one RFC 6902 operation.
type kubePatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}
