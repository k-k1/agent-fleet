package routegw

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
)

// signV4 signs req in place with AWS Signature Version 4 for service in region. req must have
// its Host and EscapedPath set to what is sent, and body is the exact payload. It signs every
// header on req (except Authorization), so the caller sets the headers it wants covered first.
//
// Written here rather than taken from the AWS SDK because the Agent carries no AWS dependency
// and this is the one operation that needs one; signV4's test pins it to the signatures AWS
// publishes for its own test suite.
func signV4(req *http.Request, body []byte, c awsx.RouteCredentials, service, region string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}

	names := []string{"host"}
	vals := map[string]string{"host": req.Host}
	for k, v := range req.Header {
		l := strings.ToLower(k)
		if l == "authorization" {
			continue
		}
		names = append(names, l)
		vals[l] = strings.Join(trimAll(v), ",")
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")

	sum := sha256.Sum256(body)
	canonical := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL.EscapedPath()),
		canonicalQuery(req.URL.Query()),
		canonHeaders.String(),
		signed,
		hex.EncodeToString(sum[:]),
	}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	csum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(csum[:])

	k := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA256(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

func trimAll(vs []string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return out
}

// canonicalPath is the request's wire path encoded once more, segment by segment: a service
// other than S3 signs the double-encoded path. "/" stays.
func canonicalPath(escaped string) string {
	if escaped == "" {
		return "/"
	}
	return encodeSigV4(escaped, false)
}

func canonicalQuery(q map[string][]string) string {
	type kv struct{ k, v string }
	var all []kv
	for k, vs := range q {
		for _, v := range vs {
			all = append(all, kv{encodeSigV4(k, true), encodeSigV4(v, true)})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].k != all[j].k {
			return all[i].k < all[j].k
		}
		return all[i].v < all[j].v
	})
	parts := make([]string, len(all))
	for i, p := range all {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

// encodeSigV4 percent-encodes everything but the unreserved set; slash only when encodeSlash.
func encodeSigV4(s string, encodeSlash bool) string {
	const hexd = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexd[c>>4])
			b.WriteByte(hexd[c&15])
		}
	}
	return b.String()
}
