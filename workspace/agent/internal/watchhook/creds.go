package watchhook

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
)

// GitCreds is the member's credential for host, from the same git credential helper a push uses,
// so an expiring token is renewed exactly as there. A pasted Bitbucket API token comes back from
// git under a static user name; the REST API wants email:token, which `workspace-agent bkt-cred`
// answers (the bkt wrapper does the same).
func GitCreds(ctx context.Context, host string) (Cred, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := credFill(ctx, "git", []string{"credential", "fill"}, "protocol=https\nhost="+host+"\n\n")
	if err != nil {
		return Cred{}, err
	}
	user, tok := field(out, "username"), field(out, "password")
	if tok == "" {
		return Cred{}, errors.New("no credential")
	}
	if host == "bitbucket.org" && user == "x-bitbucket-api-token-auth" {
		self, err := os.Executable()
		if err != nil {
			return Cred{}, err
		}
		b, err := credFill(ctx, self, []string{"bkt-cred"}, "")
		if err != nil {
			return Cred{}, err
		}
		email, btok := field(b, "username"), field(b, "password")
		if email == "" || btok == "" {
			return Cred{}, errors.New("no credential")
		}
		return Cred{User: email, Token: btok, Basic: true}, nil
	}
	return Cred{User: user, Token: tok}, nil
}

func credFill(ctx context.Context, bin string, args []string, stdin string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out // stderr is dropped: nothing of a credential helper belongs in a log
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func field(s, key string) string {
	for _, l := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	return ""
}

var (
	defaultOnce sync.Once
	defaultH    Handlers
)

// Default is the Handlers the Agent routes use, built on first use so the environment is read
// when a request arrives.
func Default() Handlers {
	defaultOnce.Do(func() {
		defaultH = Handlers{M: &Manager{CPBase: cpurl.Request(), CPToken: os.Getenv("AF_WATCH_TOKEN"), Creds: GitCreds}}
	})
	return defaultH
}
