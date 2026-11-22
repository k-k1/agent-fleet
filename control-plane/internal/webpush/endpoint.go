package webpush

import (
	"errors"
	"net/url"
	"strings"
)

// DefaultEndpointHosts are the push services browsers use. A subscription's endpoint is
// member-supplied and the Control Plane POSTs to it, so without a host check any member could aim
// the Control Plane at an internal address.
var DefaultEndpointHosts = []string{
	"fcm.googleapis.com",
	"updates.push.services.mozilla.com",
	".push.services.mozilla.com",
	".push.apple.com",
	".notify.windows.com",
}

// CheckEndpoint accepts an https URL whose host is allowed. An allowed entry that starts with a
// dot matches any subdomain of it; any other entry matches that host only.
func CheckEndpoint(raw string, allowed []string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("push endpoint must be an https URL")
	}
	if p := u.Port(); p != "" && p != "443" {
		return errors.New("push endpoint must use the default https port")
	}
	host := strings.ToLower(u.Hostname())
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, ".") {
			if strings.HasSuffix(host, a) {
				return nil
			}
		} else if host == a {
			return nil
		}
	}
	return errors.New("push endpoint host is not an allowed push service")
}
