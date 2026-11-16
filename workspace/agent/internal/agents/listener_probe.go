package agents

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ListenerMayBeUp reports whether a daemon may be listening at addr (http://, ws:// or
// unix://). Only a definite "nothing there" answers false: the connection is refused, or the
// unix socket does not exist. A connection that succeeds, a timeout, an address that cannot be
// parsed and every other error answer true, because the caller uses it to decide that nothing
// runs.
func ListenerMayBeUp(addr string) bool {
	network, target := "tcp", ""
	if path, ok := strings.CutPrefix(addr, "unix://"); ok {
		network, target = "unix", path
	} else {
		u, err := url.Parse(addr)
		if err != nil || u.Host == "" {
			return true
		}
		target = u.Host
	}
	conn, err := net.DialTimeout(network, target, 2*time.Second)
	if err == nil {
		conn.Close()
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || (network == "unix" && errors.Is(err, syscall.ENOENT)) {
		return false
	}
	return true
}
