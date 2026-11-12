package jirax

// `workspace-agent jira-attachment issue attachment add|list|download|delete KEY …` — the
// attachment commands of the jira wrapper (workspace/jira-auth-wrapper.sh), which hands over its
// own argv untouched. jira-cli 1.7.0 has no attachment command at all.
//
// The calls are made here, in the process that may hold the token, so the token never reaches the
// wrapper's argv or output. An upload is one POST per file and is never re-sent: a transport
// failure says the file may have been attached instead of inviting a re-run that would attach it
// twice.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// jiraTransferClient carries uploads and downloads, whose size jiraHTTPClient's 20 s cannot
// bound. The wrapper bounds the whole run (AF_JIRA_TIMEOUT); this is only the backstop for a
// helper started without it.
//
// Redirects are never followed: the Authorization header would go with them to any port or
// subdomain of the API host and over plain HTTP, and an upload's 302 would be re-sent as a GET
// whose 200 reads as success. A 3xx is reported as the failure it is.
var jiraTransferClient = &http.Client{
	Timeout:       30 * time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

const jiraAttachmentUsage = `usage:
  jira issue attachment add KEY FILE... [--name NAME]
  jira issue attachment list KEY
  jira issue attachment download KEY ID [-o PATH | -o -] [--force]
  jira issue attachment delete KEY ID`

// jiraAttachmentArgs is what the command line says.
type jiraAttachmentArgs struct {
	verb, key, id string
	files         []string
	name          string // add --name: the file name Jira shows (one file only)
	output        string // download -o: a path, or "-" for stdout
	force         bool   // download --force: replace an existing file
	help          bool
}

// parseJiraAttachmentArgs reads `issue|issues attachment|attachments VERB KEY …` with jira's global
// -c / -p (accepted, unused). An unknown flag is an error rather than a file name: `--nmae x.png`
// uploaded as a file called "--nmae" is not what anyone meant.
func parseJiraAttachmentArgs(args []string) (jiraAttachmentArgs, error) {
	var a jiraAttachmentArgs
	var pos []string
	valued := map[string]bool{"-c": true, "--config": true, "-p": true, "--project": true,
		"--name": true, "-o": true, "--output": true}
	set := func(name, val string) {
		switch name {
		case "--name":
			a.name = val
		case "-o", "--output":
			a.output = val
		}
	}
	setBool := func(name, val string) error {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("invalid argument %q for %q flag", val, name)
		}
		switch name {
		case "--force":
			a.force = b
		case "-h", "--help":
			a.help = b
		case "--debug":
			if b {
				return fmt.Errorf("--debug is not available for attachments (it would print the Authorization header)")
			}
		}
		return nil
	}
	boolean := map[string]bool{"--force": true, "-h": true, "--help": true, "--debug": true}
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case valued[s]:
			if i+1 >= len(args) {
				return a, fmt.Errorf("flag needs an argument: %s", s)
			}
			i++
			set(s, args[i])
		case boolean[s]:
			if err := setBool(s, "true"); err != nil {
				return a, err
			}
		case strings.HasPrefix(s, "--") && strings.Contains(s, "="):
			name, val, _ := strings.Cut(s, "=")
			switch {
			case valued[name]:
				set(name, val)
			case boolean[name]:
				if err := setBool(name, val); err != nil {
					return a, err
				}
			default:
				return a, fmt.Errorf("unknown flag: %s", name)
			}
		case len(s) > 2 && s[0] == '-' && s[1] != '-' && valued[s[:2]]:
			set(s[:2], strings.TrimPrefix(s[2:], "="))
		case strings.HasPrefix(s, "-h="):
			if err := setBool("-h", s[3:]); err != nil {
				return a, err
			}
		case strings.HasPrefix(s, "-") && s != "-":
			return a, fmt.Errorf("unknown flag: %s", s)
		default:
			pos = append(pos, s)
		}
	}
	if a.help {
		return a, nil
	}
	if len(pos) < 4 || (pos[0] != "issue" && pos[0] != "issues") || (pos[1] != "attachment" && pos[1] != "attachments") {
		return a, fmt.Errorf("%s", jiraAttachmentUsage)
	}
	a.verb, a.key = pos[2], pos[3]
	rest := pos[4:]
	switch a.verb {
	case "add":
		if len(rest) == 0 {
			return a, fmt.Errorf("attachment add needs at least one FILE\n%s", jiraAttachmentUsage)
		}
		a.files = rest
		if a.name != "" && len(a.files) != 1 {
			return a, fmt.Errorf("--name takes exactly one FILE")
		}
		if a.name != "" && (strings.ContainsAny(a.name, "/\\") || a.name == "." || a.name == "..") {
			return a, fmt.Errorf("--name %q is not a file name", a.name)
		}
	case "list":
		if len(rest) != 0 {
			return a, fmt.Errorf("%s", jiraAttachmentUsage)
		}
	case "download", "delete":
		if len(rest) != 1 {
			return a, fmt.Errorf("%s", jiraAttachmentUsage)
		}
		a.id = rest[0]
		if !jiraCommentIDRe.MatchString(a.id) {
			return a, fmt.Errorf("%q is not an attachment ID (digits; see `jira issue attachment list %s`)", a.id, a.key)
		}
	default:
		return a, fmt.Errorf("%s", jiraAttachmentUsage)
	}
	if a.verb != "add" && a.name != "" {
		return a, fmt.Errorf("unknown flag: --name")
	}
	if a.verb != "download" && (a.output != "" || a.force) {
		return a, fmt.Errorf("-o / --force are for attachment download only")
	}
	if !jiraCommentKeyRe.MatchString(a.key) {
		return a, fmt.Errorf("%q is not an issue key", a.key)
	}
	return a, nil
}

// RunAttachment exits 0 on success, 1 on a failed call and 2 on a bad command line.
func RunAttachment(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), jiraCredStoreWait)
	s, err := secrets.LoadContext(ctx)
	cancel()
	var c *secrets.JiraCreds
	if err == nil && Connected(s.Jira) {
		cc := *s.Jira
		c = &cc
	}
	// The wrapper's timeout ends this process with TERM; cancelling the transfer instead lets a
	// download remove its temporary file rather than leave a partial copy beside the destination.
	sctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	rc := jiraAttachmentRun(sctx, c, args, os.Stdout, os.Stderr)
	stop()
	os.Exit(rc)
}

func jiraAttachmentRun(ctx context.Context, c *secrets.JiraCreds, args []string, stdout, stderr io.Writer) int {
	a, err := parseJiraAttachmentArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 2
	}
	if a.help {
		fmt.Fprintln(stdout, jiraAttachmentUsage)
		return 0
	}
	if a.verb == "add" {
		// A missing file is the caller's mistake and is told before anything is sent.
		for _, f := range a.files {
			fi, err := os.Stat(f)
			if err != nil {
				fmt.Fprintf(stderr, "jira: cannot read %s: %v\n", f, err)
				return 2
			}
			if !fi.Mode().IsRegular() {
				fmt.Fprintf(stderr, "jira: %s is not a regular file\n", f)
				return 2
			}
		}
	}
	if c == nil {
		fmt.Fprintln(stderr, "jira: no Jira connection (connect Jira in Settings)")
		return 1
	}
	if c.AuthKind == "oauth" && c.CloudID == "" {
		fmt.Fprintln(stderr, "jira: no Jira site selected for the connection")
		return 1
	}
	switch a.verb {
	case "add":
		return jiraAttachmentAdd(ctx, c, a, stdout, stderr)
	case "list":
		return jiraAttachmentList(c, a, stdout, stderr)
	case "download":
		return jiraAttachmentDownload(ctx, c, a, stdout, stderr)
	}
	return jiraAttachmentDelete(c, a, stdout, stderr)
}

// jiraAttachment is one entry of an issue's attachment field.
type jiraAttachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Created  string `json:"created"`
	Author   struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
}

// jiraIssueAttachments reads the issue's attachments. download and delete look the ID up here
// first, so an ID mistyped from another issue's list fails instead of acting on that issue.
func jiraIssueAttachments(c *secrets.JiraCreds, a jiraAttachmentArgs) ([]jiraAttachment, error) {
	raw, status, _, err := jiraCommentCall(c, "GET", APIBase(c)+"/rest/api/3/issue/"+a.key+"?fields=attachment", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", jiraAttachmentFailure(a, status, raw))
	}
	var out struct {
		Fields struct {
			Attachment []jiraAttachment `json:"attachment"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("cannot read the attachments of %s: %v", a.key, err)
	}
	return out.Fields.Attachment, nil
}

func jiraFindAttachment(c *secrets.JiraCreds, a jiraAttachmentArgs) (jiraAttachment, error) {
	list, err := jiraIssueAttachments(c, a)
	if err != nil {
		return jiraAttachment{}, err
	}
	for _, at := range list {
		if at.ID == a.id {
			return at, nil
		}
	}
	return jiraAttachment{}, fmt.Errorf("%s has no attachment %s (see `jira issue attachment list %s`)", a.key, a.id, a.key)
}

func jiraAttachmentList(c *secrets.JiraCreds, a jiraAttachmentArgs, stdout, stderr io.Writer) int {
	list, err := jiraIssueAttachments(c, a)
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintf(stderr, "%s has no attachments\n", a.key)
		return 0
	}
	fmt.Fprintln(stdout, "ID\tNAME\tSIZE\tCREATED\tAUTHOR")
	for _, at := range list {
		fmt.Fprintf(stdout, "%s\t%s\t%d\t%s\t%s\n", at.ID, oneLine(at.Filename), at.Size, at.Created, oneLine(at.Author.DisplayName))
	}
	return 0
}

// oneLine keeps a name from breaking the tab-separated listing.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

func jiraAttachmentAdd(ctx context.Context, c *secrets.JiraCreds, a jiraAttachmentArgs, stdout, stderr io.Writer) int {
	u := APIBase(c) + "/rest/api/3/issue/" + a.key + "/attachments"
	for i, f := range a.files {
		name := a.name
		if name == "" {
			name = filepath.Base(f)
		}
		raw, status, err := jiraUpload(ctx, c, u, f, name)
		if err != nil {
			fmt.Fprintf(stderr, "jira: %v; %s may have been attached - check `jira issue attachment list %s` before running it again\n", err, f, a.key)
			jiraAttachmentNotSent(a.files[i+1:], stderr)
			return 1
		}
		if status >= 500 {
			// A gateway can answer 5xx after Jira stored the file.
			fmt.Fprintf(stderr, "jira: %s: %s; it may have been attached - check `jira issue attachment list %s` before running it again\n", f, jiraAttachmentFailure(a, status, raw), a.key)
			jiraAttachmentNotSent(a.files[i+1:], stderr)
			return 1
		}
		if status != http.StatusOK && status != http.StatusCreated {
			fmt.Fprintf(stderr, "jira: %s: %s\n", f, jiraAttachmentFailure(a, status, raw))
			jiraAttachmentNotSent(a.files[i+1:], stderr)
			return 1
		}
		var got []jiraAttachment
		_ = json.Unmarshal(raw, &got)
		if len(got) == 1 {
			fmt.Fprintf(stdout, "Attached %q to %s (id %s, %d bytes)\n", got[0].Filename, a.key, got[0].ID, got[0].Size)
		} else {
			fmt.Fprintf(stdout, "Attached %q to %s\n", name, a.key)
		}
	}
	fmt.Fprintf(stdout, "%s/browse/%s\n", c.Site, a.key)
	return 0
}

func jiraAttachmentNotSent(rest []string, stderr io.Writer) {
	if len(rest) > 0 {
		fmt.Fprintf(stderr, "jira: not sent: %s\n", strings.Join(rest, ", "))
	}
}

// jiraUpload POSTs one file as multipart/form-data. The body is the multipart head, the file and
// the closing boundary, with an exact Content-Length: a chunked upload is refused with 411 by some
// proxies in front of Jira. A 401 on OAuth is answered by a renewal and one more attempt (Jira
// refused the request, so nothing was attached); any other failure is never re-sent.
func jiraUpload(ctx context.Context, c *secrets.JiraCreds, u, path, name string) ([]byte, int, error) {
	if err := EnsureFresh(c); err != nil {
		return nil, 0, err
	}
	raw, status, err := jiraUploadOnce(ctx, c, u, path, name)
	if err == nil && status == http.StatusUnauthorized && c.AuthKind == "oauth" {
		if rerr := RefreshNow(c); rerr == nil {
			raw, status, err = jiraUploadOnce(ctx, c, u, path, name)
		}
	}
	return raw, status, err
}

func jiraUploadOnce(ctx context.Context, c *secrets.JiraCreds, u, path, name string) ([]byte, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot read %s: %v", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("cannot read %s: %v", path, err)
	}
	head, tail, ctype := jiraMultipartFrame(name, jiraContentType(f, name))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("cannot read %s: %v", path, err)
	}
	body := io.MultiReader(bytes.NewReader(head), io.LimitReader(f, fi.Size()), bytes.NewReader(tail))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, 0, err
	}
	req.ContentLength = int64(len(head)) + fi.Size() + int64(len(tail))
	req.Header.Set("Authorization", jiraAuthHeader(c))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", ctype)
	// Jira refuses a multipart POST without it (XSRF check), with a 403 that reads like a permission problem.
	req.Header.Set("X-Atlassian-Token", "no-check")
	resp, err := jiraTransferClient.Do(req)
	if err != nil {
		return nil, 0, jiraTransportError(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return raw, resp.StatusCode, nil
}

// jiraMultipartFrame is the bytes before and after the file in a one-part form named "file", the
// field Jira reads, and the request's Content-Type.
func jiraMultipartFrame(name, partType string) (head, tail []byte, ctype string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	esc := strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "", "\n", "")
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+esc.Replace(name)+`"`)
	h.Set("Content-Type", partType)
	_, _ = w.CreatePart(h)
	head = append([]byte(nil), buf.Bytes()...)
	buf.Reset()
	_ = w.Close()
	return head, append([]byte(nil), buf.Bytes()...), w.FormDataContentType()
}

// jiraContentType is the part's type: from the extension, else sniffed from the first 512 bytes,
// so that Jira previews an image instead of offering it as a download.
func jiraContentType(f io.Reader, name string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	return http.DetectContentType(buf[:n])
}

func jiraAttachmentDownload(ctx context.Context, c *secrets.JiraCreds, a jiraAttachmentArgs, stdout, stderr io.Writer) int {
	at, err := jiraFindAttachment(c, a)
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	// Only an explicit -o - means stdout: an attachment may itself be named "-".
	toStdout := a.output == "-"
	dest := a.output
	if dest == "" {
		dest = jiraSafeFileName(at.Filename, at.ID)
	} else if !toStdout {
		if fi, err := os.Stat(dest); err == nil && fi.IsDir() {
			dest = filepath.Join(dest, jiraSafeFileName(at.Filename, at.ID))
		}
	}
	if !toStdout && !a.force {
		if _, err := os.Lstat(dest); err == nil {
			fmt.Fprintf(stderr, "jira: %s already exists (pass --force to replace it, or -o PATH)\n", dest)
			return 1
		}
	}
	if err := EnsureFresh(c); err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	// redirect=false: Jira streams the content itself instead of answering 303 to the media
	// host, so the download is one request to the host the credential is for.
	u := APIBase(c) + "/rest/api/3/attachment/content/" + a.id + "?redirect=false"
	resp, err := jiraDownloadOnce(ctx, c, u)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && c.AuthKind == "oauth" {
		resp.Body.Close()
		if rerr := RefreshNow(c); rerr == nil {
			resp, err = jiraDownloadOnce(ctx, c, u)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		fmt.Fprintln(stderr, "jira:", jiraAttachmentFailure(a, resp.StatusCode, raw))
		return 1
	}
	if toStdout {
		if _, err := io.Copy(stdout, resp.Body); err != nil {
			fmt.Fprintln(stderr, "jira: download cut off:", jiraTransportError(err))
			return 1
		}
		return 0
	}
	// Written beside the destination and published at the end, so a cut-off download never
	// leaves a truncated file under the real name; the deferred removal also covers a cancel.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".jira-attachment-*")
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	defer os.Remove(tmp.Name())
	n, cerr := io.Copy(tmp, resp.Body)
	if err := tmp.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		fmt.Fprintln(stderr, "jira: download cut off:", jiraTransportError(cerr))
		return 1
	}
	if a.force {
		err = os.Rename(tmp.Name(), dest)
	} else {
		// link(2) fails when the name exists, so a file that appeared during the transfer (another
		// session downloading the same attachment) is never replaced; the Lstat above only gives
		// the early answer.
		err = os.Link(tmp.Name(), dest)
		if os.IsExist(err) {
			fmt.Fprintf(stderr, "jira: %s appeared during the download; not replaced (pass --force, or -o PATH)\n", dest)
			return 1
		}
	}
	if err != nil {
		if !a.force {
			fmt.Fprintf(stderr, "jira: cannot publish %s without replacing anything (%v); pass --force to rename it into place\n", dest, err)
		} else {
			fmt.Fprintln(stderr, "jira:", err)
		}
		return 1
	}
	fmt.Fprintf(stdout, "Downloaded %q from %s to %s (%d bytes)\n", at.Filename, a.key, dest, n)
	return 0
}

func jiraDownloadOnce(ctx context.Context, c *secrets.JiraCreds, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", jiraAuthHeader(c))
	resp, err := jiraTransferClient.Do(req)
	if err != nil {
		return nil, jiraTransportError(err)
	}
	return resp, nil
}

// jiraSafeFileName is the attachment's name with any directory part dropped: the name comes from
// whoever uploaded it, and `../x` must not write outside the current directory.
func jiraSafeFileName(name, id string) string {
	b := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if b == "" || b == "." || b == ".." || b == "/" {
		return "attachment-" + id
	}
	return b
}

func jiraAttachmentDelete(c *secrets.JiraCreds, a jiraAttachmentArgs, stdout, stderr io.Writer) int {
	at, err := jiraFindAttachment(c, a)
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	raw, status, afterTransport, err := jiraCommentCall(c, "DELETE", APIBase(c)+"/rest/api/3/attachment/"+a.id, nil)
	if err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	if status == http.StatusNotFound && afterTransport {
		fmt.Fprintf(stdout, "Attachment %s on %s probably already deleted\n", a.id, a.key)
		fmt.Fprintln(stderr, "jira: an earlier attempt failed in transport and the retry got 404, so the delete most likely went through")
		return 0
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		fmt.Fprintln(stderr, "jira:", jiraAttachmentFailure(a, status, raw))
		return 1
	}
	fmt.Fprintf(stdout, "Attachment %s (%q) on %s deleted\n", a.id, at.Filename, a.key)
	return 0
}

// jiraAttachmentFailure words a refusal in one line, without the token or the request.
func jiraAttachmentFailure(a jiraAttachmentArgs, status int, raw []byte) string {
	what := map[string]string{
		"add":      "attach a file to " + a.key,
		"list":     "list the attachments of " + a.key,
		"download": fmt.Sprintf("download attachment %s of %s", a.id, a.key),
		"delete":   fmt.Sprintf("delete attachment %s of %s", a.id, a.key),
	}[a.verb]
	switch status {
	case http.StatusUnauthorized:
		return "Jira rejected the credentials (re-connect Jira)"
	case http.StatusForbidden:
		perm := "Browse Projects"
		switch a.verb {
		case "add":
			perm = "Create Attachments"
		case "delete":
			perm = "Delete Own Attachments / Delete All Attachments"
		}
		return fmt.Sprintf("Jira refused to %s (403): attachments may be turned off on the site, or the account may lack the %s permission; an OAuth connection needs write:jira-work", what, perm)
	case http.StatusNotFound:
		return fmt.Sprintf("cannot %s (404): there is no such issue or attachment, or it is not visible to this account", what)
	case http.StatusRequestEntityTooLarge:
		return fmt.Sprintf("cannot %s (413): the file is larger than the site's attachment size limit", what)
	}
	return fmt.Sprintf("cannot %s (%d): %s", what, status, ErrText(raw))
}
