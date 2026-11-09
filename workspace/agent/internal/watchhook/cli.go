package watchhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const usage = `af-watch-hook — webhook pokes for private repositories (ADR 0133)

Usage:
  af-watch-hook add github|bitbucket <owner/name>   create a hook and install it at the forge
  af-watch-hook list                                 your hooks
  af-watch-hook rm <id>                              remove it at the forge and at Agent Fleet
  af-watch-hook --help

add uses your own GitHub / Bitbucket connection (Settings > Connections), which needs admin rights
on the repository. It creates the hook at the Control Plane, installs it at the forge and deletes
the Control Plane hook again if the install fails. The hook's secret is passed to the forge and
never shown or stored. Running add again for a repository that has a hook changes nothing.
Without a hook a private repository's watches stay on polling.
`

// Client reaches this workspace's Agent with the shell's AGENT_TOKEN.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// NewClient reads the Agent's address and token from the environment, like af-memory.
func NewClient() *Client {
	port := "7700"
	if _, p, err := net.SplitHostPort(os.Getenv("AGENT_ADDR")); err == nil && p != "" {
		port = p
	}
	return &Client{Base: "http://127.0.0.1:" + port, Token: os.Getenv("AGENT_TOKEN"), HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

// Run is `workspace-agent watch-hook <args>`.
func Run(args []string) { os.Exit(Main(NewClient(), args, os.Stdout, os.Stderr)) }

func (c *Client) do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("cannot reach the Agent on this workspace")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct{ Code, Message string }
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return errors.New(e.Error.Message)
		}
		return fmt.Errorf("the Agent answered HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

// Main runs one invocation: 0 done, 1 failed, 2 usage.
func Main(c *Client, args []string, out, errw io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(errw, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(out, usage)
		return 0
	case "list":
		if len(args) != 1 {
			break
		}
		var res struct{ Hooks []Hook }
		if err := c.do(http.MethodGet, "/watch/hooks", nil, &res); err != nil {
			return fail(errw, err)
		}
		if len(res.Hooks) == 0 {
			fmt.Fprintln(out, "no hooks")
		}
		for _, h := range res.Hooks {
			repo := h.Repo
			if repo == "" {
				repo = "(any)"
			}
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", h.ID, h.Source, repo, h.CreatedAt)
		}
		return 0
	case "add":
		if len(args) != 3 {
			break
		}
		var res AddResult
		if err := c.do(http.MethodPost, "/watch/hooks", map[string]string{"source": args[1], "repo": args[2]}, &res); err != nil {
			return fail(errw, err)
		}
		if res.Existing {
			fmt.Fprintf(out, "hook %s already exists for %s. %s\n", res.ID, res.Repo, res.Note)
			return 0
		}
		fmt.Fprintf(out, "hook %s installed at %s for %s (pull request, review, comment and check events). Watches on this repository's pull requests are now poked.\n", res.ID, res.Source, res.Repo)
		return 0
	case "rm":
		if len(args) != 2 {
			break
		}
		var res RemoveResult
		if err := c.do(http.MethodDelete, "/watch/hooks/"+url.PathEscape(args[1]), nil, &res); err != nil {
			return fail(errw, err)
		}
		fmt.Fprintf(out, "hook %s removed", res.Deleted)
		if res.ForgeRemoved {
			fmt.Fprint(out, " (also at the forge)")
		}
		fmt.Fprintln(out)
		if res.Warning != "" {
			fmt.Fprintln(errw, "af-watch-hook: warning:", res.Warning)
		}
		return 0
	default:
		fmt.Fprint(errw, usage)
		return 2
	}
	fmt.Fprint(errw, usage)
	return 2
}

func fail(errw io.Writer, err error) int {
	fmt.Fprintln(errw, "af-watch-hook:", strings.TrimSpace(err.Error()))
	return 1
}
