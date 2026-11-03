package jirax

// `workspace-agent confluence-page <sub> …` — the page core of the `confluence` wrapper
// (workspace/confluence-auth-wrapper.sh), over Confluence's v2 REST API.
//
// Why it exists: confluence-cli 2.31 reads, creates and updates pages through the v1
// /wiki/rest/api/content and /space endpoints, which Atlassian has removed on current Cloud sites
// (410 Gone), and the v2 replacements need granular scopes the CLI never asked for. So the
// subcommands that depend on those endpoints are answered here, with the CLI's own command line
// and output shape, and everything that still works (search, comments, attachments, …) stays
// with confluence-cli.
//
// Handled: read, info, create, create-child, update, edit, space-lookup. Not handled, and still
// the CLI's v1 calls (expected to answer 410 until upstream moves them): move, delete, children,
// copy-tree, find, spaces, versions, properties/front matter, folders.
//
// Rules this file keeps:
//   - the token is read from the store in this process and only ever goes into an Authorization
//     header: never argv, never output, never an error text;
//   - a write is sent once. A transport failure after the request left is reported as "outcome
//     unknown, check the page" rather than re-sent (a second create would be a second page);
//   - an update sends version+1 of what it just read, so a concurrent edit is a 409 reported as a
//     conflict instead of being overwritten;
//   - file and flag text must be valid UTF-8, or the command refuses instead of storing garbage.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Exit codes beyond 0/1: 2 is a usage error (as cobra's), 64 says "no Atlassian connection, run
// the real CLI" to the wrapper, which then prints its usual hint.
const (
	confluencePageNoConnection = 64
)

// confluencePageScopeList is what the v2 page endpoints need (each operation's OAuth2 security in
// Atlassian's openapi-v2.v3.json): GET /pages/{id} read:page, POST /pages and PUT /pages/{id}
// write:page, GET /spaces and /spaces/{id} read:space.
var confluencePageScopeList = []string{"read:page:confluence", "write:page:confluence", "read:space:confluence"}

const confluencePageReconnect = "this Atlassian authorization predates the Confluence v2 page scopes " +
	"(read:page:confluence, write:page:confluence, read:space:confluence), which page read/create/update need. " +
	"The tenant administrator adds them to the Atlassian app, then reconnect Jira in Settings. " +
	"(confluence search and comments keep working meanwhile.)"

// RunConfluencePage is `workspace-agent confluence-page <sub> args…`.
func RunConfluencePage(args []string) {
	os.Exit(confluencePageMain(args, os.Stdin, os.Stdout, os.Stderr))
}

func confluencePageMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin
	sub, rest := splitConfluenceArgs(args)
	p, err := parseConfluencePage(sub, rest)
	if err != nil {
		fmt.Fprintln(stderr, "confluence:", err)
		return 2
	}
	// Local inputs are read and converted before the network is touched, so a bad file fails
	// the same way with or without a connection.
	if err := p.loadBody(); err != nil {
		fmt.Fprintln(stderr, "confluence:", err)
		return 1
	}
	var note bytes.Buffer
	c, ok := freshConnection(&note)
	if !ok {
		if s := note.String(); strings.HasPrefix(s, "unusable=") {
			fmt.Fprintf(stderr, "confluence: the Atlassian connection cannot be used for Confluence [%s]. Reconnect Jira in Settings.\n",
				strings.TrimSpace(strings.TrimPrefix(s, "unusable=")))
			return 1
		}
		return confluencePageNoConnection
	}
	if c.AuthKind == "oauth" {
		jiraBackfillScopes(&c)
		switch ConfluenceScopes(&c) {
		case "missing":
			fmt.Fprintln(stderr, "confluence: this Atlassian authorization has no Confluence scopes. Reconnect Jira in Settings after the tenant administrator adds them to the Atlassian app.")
			return 1
		case "outdated":
			fmt.Fprintln(stderr, "confluence: "+confluencePageReconnect)
			return 1
		}
	}
	cl := &confluenceV2{c: &c}
	if err := cl.run(p, stdout); err != nil {
		fmt.Fprintln(stderr, "confluence:", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------------------------
// command line

type confluencePageCmd struct {
	sub    string
	pos    []string
	format string // read: output; create/update: input
	file   string
	cont   *string
	title  *string
	typ    string
	output string
	json   bool

	body    string // storage, once loadBody ran
	hasBody bool
}

var pageFlagSpecs = map[string]map[string]string{
	// flag -> canonical name; a name with a trailing "!" is boolean.
	"read":         {"-f": "format", "--format": "format", "--json": "json!"},
	"info":         {"-f": "format", "--format": "format", "--json": "json!"},
	"create":       {"-f": "file", "--file": "file", "-c": "content", "--content": "content", "--format": "format", "--type": "type", "--json": "json!"},
	"create-child": {"-f": "file", "--file": "file", "-c": "content", "--content": "content", "--format": "format", "--type": "type", "--json": "json!"},
	"update":       {"-t": "title", "--title": "title", "-f": "file", "--file": "file", "-c": "content", "--content": "content", "--format": "format", "--json": "json!"},
	"edit":         {"-o": "output", "--output": "output", "--json": "json!"},
	"space-lookup": {"--json": "json!"},
}

var pagePositional = map[string]int{"read": 1, "info": 1, "create": 2, "create-child": 2, "update": 1, "edit": 1, "space-lookup": 1}

// ConfluencePageSubs are the subcommands the wrapper hands to this file.
var ConfluencePageSubs = []string{"read", "info", "create", "create-child", "update", "edit", "space-lookup"}

// splitConfluenceArgs finds the subcommand (first word that is not an option, as the wrapper
// does) and returns everything else in order. Options before it are kept in rest.
func splitConfluenceArgs(args []string) (string, []string) {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			rest := append(append([]string{}, args[:i]...), args[i+1:]...)
			return a, rest
		}
	}
	return "", args
}

func parseConfluencePage(sub string, args []string) (*confluencePageCmd, error) {
	spec, ok := pageFlagSpecs[sub]
	if !ok {
		return nil, fmt.Errorf("%q is not a page command handled here", sub)
	}
	p := &confluencePageCmd{sub: sub, typ: "page"}
	var content, title string
	var hasContent, hasTitle bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.pos = append(p.pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			p.pos = append(p.pos, a)
			continue
		}
		name, val, hasVal := a, "", false
		if strings.HasPrefix(a, "--") {
			if k := strings.IndexByte(a, '='); k > 0 {
				name, val, hasVal = a[:k], a[k+1:], true
			}
		} else if len(a) > 2 {
			name, val, hasVal = a[:2], a[2:], true
		}
		canon, known := spec[name]
		if !known {
			return nil, fmt.Errorf("option %s is not supported by the v2 page shim for '%s' (use 'confluence api' for anything else)", name, sub)
		}
		if strings.HasSuffix(canon, "!") {
			if hasVal {
				return nil, fmt.Errorf("option %s takes no value", name)
			}
			p.json = true
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("option %s argument missing", name)
			}
			i++
			val = args[i]
		}
		if !utf8.ValidString(val) {
			return nil, fmt.Errorf("option %s is not valid UTF-8; refusing to store it", name)
		}
		switch canon {
		case "format":
			p.format = val
		case "file":
			p.file = val
		case "content":
			content, hasContent = val, true
		case "title":
			title, hasTitle = val, true
		case "type":
			p.typ = val
		case "output":
			p.output = val
		}
	}
	if hasContent {
		p.cont = &content
	}
	if hasTitle {
		p.title = &title
	}
	if len(p.pos) != pagePositional[sub] {
		return nil, fmt.Errorf("'%s' takes %d argument(s), got %d", sub, pagePositional[sub], len(p.pos))
	}
	for _, s := range p.pos {
		if !utf8.ValidString(s) {
			return nil, fmt.Errorf("an argument is not valid UTF-8; refusing to use it")
		}
	}
	if p.typ != "page" {
		return nil, fmt.Errorf("--type %s is not supported by the v2 page shim (pages only)", p.typ)
	}
	switch sub {
	case "read":
		if p.format == "" {
			p.format = "text"
		}
		switch p.format {
		case "text", "markdown", "storage", "html":
		default:
			return nil, fmt.Errorf("unknown read format %q (text, markdown, storage, html)", p.format)
		}
	case "create", "create-child", "update":
		if p.format == "" {
			p.format = "storage"
		}
		switch p.format {
		case "storage", "html", "markdown", "auto":
		default:
			return nil, fmt.Errorf("unknown content format %q (auto, storage, html, markdown)", p.format)
		}
		if sub == "update" && p.title == nil && p.file == "" && p.cont == nil {
			return nil, fmt.Errorf("at least one of --title, --file, or --content must be provided")
		}
		if sub != "update" && p.file == "" && (p.cont == nil || *p.cont == "") {
			return nil, fmt.Errorf("either --file or --content option is required")
		}
		if p.title != nil && strings.TrimSpace(*p.title) == "" {
			return nil, fmt.Errorf("--title must not be empty")
		}
		if sub != "update" && strings.TrimSpace(p.pos[0]) == "" {
			return nil, fmt.Errorf("title must not be empty")
		}
	}
	return p, nil
}

// loadBody reads --file / --content and converts it to storage format.
func (p *confluencePageCmd) loadBody() error {
	var raw string
	switch {
	case p.file != "":
		b, err := os.ReadFile(p.file)
		if err != nil {
			return fmt.Errorf("file not found: %s", p.file)
		}
		if !utf8.Valid(b) {
			return fmt.Errorf("%s is not valid UTF-8; refusing to store it (convert it first)", p.file)
		}
		raw = strings.TrimPrefix(string(b), "\ufeff")
	case p.cont != nil && *p.cont != "":
		raw = *p.cont
	default:
		return nil
	}
	f := p.format
	if f == "auto" {
		f = "markdown"
		if t := strings.TrimSpace(raw); strings.HasPrefix(t, "<") {
			f = "storage"
		}
	}
	switch f {
	case "markdown":
		s, err := MarkdownToStorage(raw)
		if err != nil {
			return fmt.Errorf("markdown conversion failed: %v", err)
		}
		p.body = s
	default: // storage; html is passed through as the XHTML subset storage accepts
		p.body = raw
	}
	p.hasBody = true
	return nil
}

// ---------------------------------------------------------------------------------------------
// client

type confluenceV2 struct{ c *secrets.JiraCreds }

func (a *confluenceV2) api() string {
	if a.c.AuthKind == "oauth" {
		return CloudAPIBase + "/ex/confluence/" + a.c.CloudID + "/wiki/api/v2"
	}
	return strings.TrimRight(a.c.Site, "/") + "/wiki/api/v2"
}

func (a *confluenceV2) webURL(webui string) string {
	return strings.TrimRight(a.c.Site, "/") + "/wiki" + webui
}

type v2Page struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Title    string `json:"title"`
	SpaceID  string `json:"spaceId"`
	ParentID string `json:"parentId"`
	Version  struct {
		Number int `json:"number"`
	} `json:"version"`
	Body struct {
		Storage *struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

type v2Space struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
}

var pageIDRe = regexp.MustCompile(`^[0-9]+$`)
var pageURLRe = regexp.MustCompile(`/pages/([0-9]+)|[?&]pageId=([0-9]+)`)

// pageID accepts a numeric id or a page URL, as the CLI does.
func pageID(s string) (string, error) {
	if pageIDRe.MatchString(s) {
		return s, nil
	}
	if m := pageURLRe.FindStringSubmatch(s); m != nil {
		if m[1] != "" {
			return m[1], nil
		}
		return m[2], nil
	}
	return "", fmt.Errorf("%q is not a page id or page URL", s)
}

// get is a read: re-sent by Request on transient failures, and once more after a renewal on 401.
func (a *confluenceV2) get(path string) ([]byte, int, error) {
	if err := EnsureFresh(a.c); err != nil {
		return nil, 0, fmt.Errorf("could not renew the Atlassian token")
	}
	body, status, err := Request(a.c, "GET", a.api()+path, nil)
	if err == nil && status == http.StatusUnauthorized && a.c.AuthKind == "oauth" {
		if RefreshNow(a.c) == nil {
			body, status, err = Request(a.c, "GET", a.api()+path, nil)
		}
	}
	if err != nil {
		return nil, 0, fmt.Errorf("%s", strings.Replace(err.Error(), "Jira", "Confluence", 1))
	}
	return body, status, nil
}

// send is a write: exactly one attempt (plus one after a 401, which proves nothing was applied).
// outcomeUnknown is true when the request may have been processed but no answer came back.
func (a *confluenceV2) send(method, path string, payload any) (body []byte, status int, outcomeUnknown bool, err error) {
	raw, merr := json.Marshal(payload)
	if merr != nil {
		return nil, 0, false, merr
	}
	if err := EnsureFresh(a.c); err != nil {
		return nil, 0, false, fmt.Errorf("could not renew the Atlassian token")
	}
	u := a.api() + path
	body, status, _, oerr := jiraOnce(a.c, method, u, raw, false)
	if oerr == nil && status == http.StatusUnauthorized && a.c.AuthKind == "oauth" {
		if RefreshNow(a.c) == nil {
			body, status, _, oerr = jiraOnce(a.c, method, u, raw, false)
		}
	}
	if oerr != nil {
		return nil, 0, jiraMaybeApplied(oerr), fmt.Errorf("could not reach Confluence (%s)", jiraTransportClass(oerr))
	}
	return body, status, false, nil
}

// failure words a non-2xx answer in one line, with Confluence's own message when it gave one.
func confluenceFailure(what string, status int, body []byte) error {
	detail := confluenceErrorText(body)
	switch status {
	case http.StatusUnauthorized:
		if strings.Contains(strings.ToLower(detail+string(body)), "scope") {
			return fmt.Errorf("cannot %s (401): %s", what, confluencePageReconnect)
		}
		return fmt.Errorf("cannot %s (401): Confluence rejected the credentials (reconnect Jira in Settings)", what)
	case http.StatusForbidden:
		return fmt.Errorf("cannot %s (403): the account lacks the permission%s", what, suffix(detail))
	case http.StatusNotFound:
		return fmt.Errorf("cannot %s (404): not found, or not visible to this account%s", what, suffix(detail))
	case http.StatusConflict:
		return fmt.Errorf("cannot %s (409): the page was changed by someone else since it was read (version conflict). Read it again and redo the edit; nothing was overwritten%s", what, suffix(detail))
	case http.StatusGone:
		return fmt.Errorf("cannot %s (410): Atlassian has removed this endpoint%s", what, suffix(detail))
	}
	return fmt.Errorf("cannot %s (%d)%s", what, status, suffix(detail))
}

func suffix(d string) string {
	if d == "" {
		return ""
	}
	return ": " + d
}

// confluenceErrorText picks the human message out of a v2 or v1 error body, capped and printable.
func confluenceErrorText(body []byte) string {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	s := e.Message
	for _, x := range e.Errors {
		if x.Title != "" {
			s += " " + x.Title
		}
		if x.Detail != "" {
			s += " " + x.Detail
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

func (a *confluenceV2) page(id string, withBody bool) (*v2Page, error) {
	q := ""
	if withBody {
		q = "?body-format=storage"
	}
	body, status, err := a.get("/pages/" + id + q)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, confluenceFailure("read page "+id, status, body)
	}
	var p v2Page
	if err := json.Unmarshal(body, &p); err != nil || p.ID == "" {
		return nil, fmt.Errorf("unexpected answer for page %s", id)
	}
	return &p, nil
}

func (a *confluenceV2) spaceByID(id string) (*v2Space, error) {
	body, status, err := a.get("/spaces/" + url.PathEscape(id))
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, confluenceFailure("read space "+id, status, body)
	}
	var s v2Space
	if err := json.Unmarshal(body, &s); err != nil || s.ID == "" {
		return nil, fmt.Errorf("unexpected answer for space %s", id)
	}
	return &s, nil
}

// spaceByKey is nil, nil for a key that does not exist.
func (a *confluenceV2) spaceByKey(key string) (*v2Space, error) {
	body, status, err := a.get("/spaces?limit=1&keys=" + url.QueryEscape(key))
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, confluenceFailure("look up space "+key, status, body)
	}
	var r struct {
		Results []v2Space `json:"results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("unexpected answer for space %s", key)
	}
	for _, s := range r.Results {
		if strings.EqualFold(s.Key, key) {
			return &s, nil
		}
	}
	return nil, nil
}

func emitJSON(w io.Writer, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(w, string(b))
}

func (a *confluenceV2) run(p *confluencePageCmd, w io.Writer) error {
	switch p.sub {
	case "space-lookup":
		s, err := a.spaceByKey(p.pos[0])
		if err != nil {
			return err
		}
		if s == nil {
			emitJSON(w, map[string]any{"found": false, "key": p.pos[0]})
			return nil
		}
		emitJSON(w, s)
		return nil
	case "create", "create-child":
		return a.create(p, w)
	}
	id, err := pageID(p.pos[0])
	if err != nil {
		return err
	}
	switch p.sub {
	case "read":
		pg, err := a.page(id, true)
		if err != nil {
			return err
		}
		if pg.Body.Storage == nil {
			return fmt.Errorf("page %s has no storage body", id)
		}
		v := pg.Body.Storage.Value
		switch p.format {
		case "markdown":
			v = StorageToMarkdown(v)
		case "text":
			v = StorageToText(v)
		}
		fmt.Fprintln(w, v)
	case "info":
		pg, err := a.page(id, false)
		if err != nil {
			return err
		}
		sp, err := a.spaceByID(pg.SpaceID)
		if err != nil {
			return err
		}
		if p.json {
			emitJSON(w, map[string]any{"id": pg.ID, "title": pg.Title, "type": "page", "status": pg.Status,
				"space": sp, "version": pg.Version.Number, "url": a.webURL(pg.Links.WebUI)})
			return nil
		}
		fmt.Fprintf(w, "Page Information:\nTitle: %s\nID: %s\nType: page\nStatus: %s\nSpace: %s (%s)\n", pg.Title, pg.ID, pg.Status, sp.Name, sp.Key)
	case "edit":
		pg, err := a.page(id, true)
		if err != nil {
			return err
		}
		if pg.Body.Storage == nil {
			return fmt.Errorf("page %s has no storage body", id)
		}
		sp, err := a.spaceByID(pg.SpaceID)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Page Information:\nTitle: %s\nID: %s\nVersion: %d\nSpace: %s (%s)\n\n", pg.Title, pg.ID, pg.Version.Number, sp.Name, sp.Key)
		if p.output != "" {
			if err := os.WriteFile(p.output, []byte(pg.Body.Storage.Value), 0o644); err != nil {
				return fmt.Errorf("could not write %s: %v", p.output, err)
			}
			fmt.Fprintf(w, "✅ Content saved to: %s\n💡 Edit the file and use \"confluence update\" to save changes\n", p.output)
		} else {
			fmt.Fprintf(w, "Page Content:\n%s\n", pg.Body.Storage.Value)
		}
	case "update":
		return a.update(p, id, w)
	}
	return nil
}

func (a *confluenceV2) create(p *confluencePageCmd, w io.Writer) error {
	title := p.pos[0]
	req := map[string]any{"status": "current", "title": title,
		"body": map[string]any{"representation": "storage", "value": p.body}}
	var sp *v2Space
	if p.sub == "create-child" {
		pid, err := pageID(p.pos[1])
		if err != nil {
			return err
		}
		parent, err := a.page(pid, false)
		if err != nil {
			return err
		}
		if sp, err = a.spaceByID(parent.SpaceID); err != nil {
			return err
		}
		req["spaceId"], req["parentId"] = parent.SpaceID, parent.ID
	} else {
		var err error
		if sp, err = a.spaceByKey(p.pos[1]); err != nil {
			return err
		}
		if sp == nil {
			return fmt.Errorf("space %q was not found, or is not visible to this account", p.pos[1])
		}
		req["spaceId"] = sp.ID
	}
	body, status, unknown, err := a.send("POST", "/pages", req)
	if err != nil {
		if unknown {
			return fmt.Errorf("%v. The page may or may not have been created; search for the title before running create again", err)
		}
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return confluenceFailure("create the page", status, body)
	}
	var pg v2Page
	if json.Unmarshal(body, &pg) != nil || pg.ID == "" {
		return fmt.Errorf("the page was probably created, but the answer was unreadable; search for the title before running create again")
	}
	u := a.webURL(pg.Links.WebUI)
	if p.json {
		emitJSON(w, map[string]any{"id": pg.ID, "title": pg.Title, "type": "page", "spaceKey": sp.Key, "url": u})
		return nil
	}
	fmt.Fprintf(w, "✅ Page created successfully!\nTitle: %s\nID: %s\nSpace: %s (%s)\nURL: %s\n", pg.Title, pg.ID, sp.Name, sp.Key, u)
	return nil
}

func (a *confluenceV2) update(p *confluencePageCmd, id string, w io.Writer) error {
	cur, err := a.page(id, true)
	if err != nil {
		return err
	}
	value := p.body
	if !p.hasBody {
		if cur.Body.Storage == nil {
			return fmt.Errorf("page %s has no storage body to keep; pass --content or --file", id)
		}
		value = cur.Body.Storage.Value
	}
	title := cur.Title
	if p.title != nil {
		title = *p.title
	}
	req := map[string]any{"id": cur.ID, "status": "current", "title": title,
		"body":    map[string]any{"representation": "storage", "value": value},
		"version": map[string]any{"number": cur.Version.Number + 1}}
	body, status, unknown, err := a.send("PUT", "/pages/"+id, req)
	if err != nil {
		if unknown {
			return fmt.Errorf("%v. The update may or may not have been applied; read the page before trying again", err)
		}
		return err
	}
	if status != http.StatusOK {
		return confluenceFailure("update page "+id, status, body)
	}
	var pg v2Page
	if json.Unmarshal(body, &pg) != nil || pg.ID == "" {
		return fmt.Errorf("the page was probably updated, but the answer was unreadable; read it before trying again")
	}
	u := a.webURL(pg.Links.WebUI)
	if p.json {
		emitJSON(w, map[string]any{"id": pg.ID, "title": pg.Title, "version": pg.Version.Number, "url": u})
		return nil
	}
	fmt.Fprintf(w, "✅ Page updated successfully!\nTitle: %s\nID: %s\nVersion: %s\nURL: %s\n", pg.Title, pg.ID, strconv.Itoa(pg.Version.Number), u)
	return nil
}
