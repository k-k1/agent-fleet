package memoryx

// Agent memory versioning (docs/log/39 ★4 / adoption point 1 from the prior-art survey) —
// the secret scan that runs at export time.
//
// Decision #2 settled on "v1 downloads in the clear (no encryption)" and, in exchange, raised
// this scan to a v1 requirement. Protecting an exported file at rest (age and the like) is a
// different layer's problem, but "carrying credentials that piled up in memory out of the
// workspace without noticing" can happen on this path today, so it is stopped here.
//
// Policy:
//   - Detection is limited to gitleaks-grade high-signature regexps. Memory is prose markdown,
//     so entropy heuristics and loose generic patterns drown it in false positives until the
//     warnings get ignored (warning fatigue is a failed defence).
//   - A detection is never swallowed: the API blocks by default and only an explicit ack lets
//     it through. Only the owner can judge — a machine cannot make the final call on whether
//     this is a real key.
//   - Never return the secret itself, and never write it to a log. What goes back is the rule
//     name, the path, the line number and a masked hint of the first few characters. Returning
//     the raw value here would turn a mechanism meant as a defence into a way of spreading the
//     secret to new places (audit logs, browser history).
//   - A bundle carries the WHOLE history, so the scan walks every reachable blob rather than the
//     HEAD tree. A key written once and then deleted is absent from HEAD but present in the
//     bundle.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// memorySecretRule is one detection rule. Name goes to the Console verbatim and is not
// translated: keeping the same vocabulary gitleaks and friends use lets the owner check what
// was flagged against knowledge from outside this product.
type memorySecretRule struct {
	Name string
	Re   *regexp.Regexp
}

// memorySecretRules lists high-signature rules only. Judge an addition by whether it could
// turn up by accident in the prose a memory is made of.
var memorySecretRules = []memorySecretRule{
	{"aws-access-key-id", regexp.MustCompile(`\b(?:A3T[A-Z0-9]|AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`)},
	{"aws-secret-access-key", regexp.MustCompile(`(?i)aws[^\n]{0,24}?secret[^\n]{0,24}?[:=]\s*["'` + "`" + `]?([A-Za-z0-9/+=]{40})`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{"github-fine-grained-pat", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,}\b`)},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}\b`)},
	{"slack-webhook", regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Za-z0-9_/\-]{20,}`)},
	{"anthropic-api-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{24,}`)},
	{"openai-api-key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{32,}\b`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"stripe-secret-key", regexp.MustCompile(`\b(?:sk|rk)_live_[0-9A-Za-z]{20,}\b`)},
	{"npm-token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{"pypi-token", regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_\-]{40,}`)},
	{"private-key-block", regexp.MustCompile(`-----BEGIN (?:RSA |DSA |EC |OPENSSH |PGP )?PRIVATE KEY(?: BLOCK)?-----`)},
	{"json-web-token", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
	{"url-basic-auth", regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s/:@]+:[^\s/@"']{8,}@[^\s/"']+`)},
	// Generic assignment form. Quotes and a 16-plus-character non-blank value are required so
	// prose like "token: usage count" does not match.
	{"generic-secret-assignment", regexp.MustCompile(`(?i)\b(?:api[_\-]?key|secret[_\-]?key|access[_\-]?token|auth[_\-]?token|client[_\-]?secret|password|passwd)\b\s*[:=]\s*["'` + "`" + `]([^"'` + "`" + `\s]{16,})["'` + "`" + `]`)},
}

// memorySecretPlaceholders drops values that are plainly examples or redactions. A real key
// almost never contains one of these, while runbooks and design notes are full of them.
var memorySecretPlaceholders = []string{
	"example", "your-", "your_", "yourkey", "xxxx", "aaaa", "0000", "1234567890abcdef",
	"changeme", "redacted", "placeholder", "dummy", "sample", "<", ">", "...", "***", "…",
}

// memorySecretFinding is one detection. It never carries the raw secret (Hint is masked).
type memorySecretFinding struct {
	Path    string `json:"path"`              // path inside the repo
	Line    int    `json:"line"`              // 1-based
	Rule    string `json:"rule"`              // Name from memorySecretRules
	Hint    string `json:"hint"`              // masked excerpt, first few characters only
	History bool   `json:"history,omitempty"` // not in the current tree, only in history
}

const (
	memorySecretMaxFindings   = 200       // beyond this we only count (the UI cannot read them either)
	memorySecretMaxPerFile    = 20        //
	memorySecretMaxBlobBytes  = 4 << 20   // scan limit per blob
	memorySecretMaxScanBytes  = 128 << 20 // scan limit overall (runaway guard)
	memorySecretHintPrefixLen = 4
)

// memoryMaskSecret masks a matched string, keeping only the first few characters — enough for
// the owner to tell WHICH key it is, too short to be useful as a value.
func memoryMaskSecret(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= memorySecretHintPrefixLen {
		return "…"
	}
	return s[:memorySecretHintPrefixLen] + "…(" + strconv.Itoa(len(s)) + ")"
}

// memoryLooksPlaceholder reports whether the value looks like an example or a redaction.
func memoryLooksPlaceholder(s string) bool {
	low := strings.ToLower(s)
	for _, p := range memorySecretPlaceholders {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// memoryScanContent scans one file's contents. Binary (anything containing a NUL) is skipped:
// a memory is markdown, and scanning binary produces nothing but noise.
func memoryScanContent(path string, b []byte) []memorySecretFinding {
	if bytes.IndexByte(b, 0) >= 0 {
		return nil
	}
	var out []memorySecretFinding
	seen := map[string]bool{}
	for i, line := range strings.Split(string(b), "\n") {
		if len(line) > 8192 {
			line = line[:8192] // absurdly long line (minified and the like): look at the head only
		}
		for _, rule := range memorySecretRules {
			m := rule.Re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			// The capture group when there is one, otherwise the whole match, is the candidate.
			val := m[0]
			if len(m) > 1 && m[1] != "" {
				val = m[1]
			}
			if memoryLooksPlaceholder(val) {
				continue
			}
			key := rule.Name + "\x00" + strconv.Itoa(i)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, memorySecretFinding{Path: path, Line: i + 1, Rule: rule.Name, Hint: memoryMaskSecret(val)})
			if len(out) >= memorySecretMaxPerFile {
				return out
			}
		}
	}
	return out
}

// memoryObjRef is one object to scan (a blob candidate).
type memoryObjRef struct {
	SHA  string
	Path string
}

// memoryScanRevTree scans a single point-in-time tree, for the tar export: a tar carries only
// the latest state.
func memoryScanRevTree(rev string) ([]memorySecretFinding, error) {
	out, err := memoryGitRun("ls-tree", "-r", rev)
	if err != nil {
		return nil, err
	}
	var refs []memoryObjRef
	for _, line := range strings.Split(out, "\n") {
		meta, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 3 || f[1] != "blob" {
			continue
		}
		refs = append(refs, memoryObjRef{SHA: f[2], Path: p})
	}
	return memoryScanObjects(refs, nil)
}

// memoryScanAllReachable scans every reachable blob, for the bundle export. A bundle carries the
// whole history, so a key written once and then deleted leaves with it — looking at HEAD alone
// is not enough.
func memoryScanAllReachable() ([]memorySecretFinding, error) {
	out, err := memoryGitRun("rev-list", "--objects", "--all")
	if err != nil {
		return nil, err
	}
	var refs []memoryObjRef
	for _, line := range strings.Split(out, "\n") {
		sha, p, _ := strings.Cut(strings.TrimSpace(line), " ")
		if len(sha) < 40 || p == "" {
			continue // a commit (no path), or a blank line
		}
		refs = append(refs, memoryObjRef{SHA: sha, Path: p})
	}
	// Anything still present in the current tree WITH THAT EXACT CONTENT is not "history only".
	// The test is the blob sha, not the path: the same path can still exist while holding the
	// post-deletion version, which is a different blob, and going by path would miss "the key
	// you deleted is still in the bundle".
	head := map[string]bool{}
	if memoryHasCommits() {
		if lt, lerr := memoryGitRun("ls-tree", "-r", memoryBranch); lerr == nil {
			for _, line := range strings.Split(lt, "\n") {
				meta, _, ok := strings.Cut(line, "\t")
				if !ok {
					continue
				}
				if f := strings.Fields(meta); len(f) >= 3 && f[1] == "blob" {
					head[f[2]] = true
				}
			}
		}
	}
	return memoryScanObjects(refs, head)
}

// memoryScanObjects reads the blobs in one batch through `git cat-file --batch` and scans them.
// Spawning git per object would mean hundreds of forks, so everything streams through a single
// process. When headBlobs is non-nil, findings from a blob missing there are marked History.
func memoryScanObjects(refs []memoryObjRef, headBlobs map[string]bool) ([]memorySecretFinding, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	cmd := memoryGit("cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		w := bufio.NewWriter(stdin)
		for _, r := range refs {
			_, _ = w.WriteString(r.SHA + "\n")
		}
		_ = w.Flush()
		_ = stdin.Close()
	}()

	var findings []memorySecretFinding
	br := bufio.NewReaderSize(stdout, 64<<10)
	var scanned int64
	for i := 0; i < len(refs); i++ {
		header, herr := br.ReadString('\n')
		if herr != nil {
			break
		}
		fields := strings.Fields(strings.TrimSpace(header))
		if len(fields) < 3 {
			continue // "<sha> missing" — it just drops out of the scan
		}
		size, perr := strconv.ParseInt(fields[2], 10, 64)
		if perr != nil {
			break // out of sync with the stream: stop here and return the partial result
		}
		body := make([]byte, 0)
		if fields[1] == "blob" && size <= memorySecretMaxBlobBytes && scanned < memorySecretMaxScanBytes {
			body = make([]byte, size)
			if _, rerr := io.ReadFull(br, body); rerr != nil {
				break
			}
			scanned += size
		} else if _, derr := io.CopyN(io.Discard, br, size); derr != nil {
			break
		}
		if _, derr := br.Discard(1); derr != nil { // the newline after the object
			break
		}
		if len(body) == 0 || len(findings) >= memorySecretMaxFindings {
			continue
		}
		for _, f := range memoryScanContent(refs[i].Path, body) {
			if headBlobs != nil && !headBlobs[refs[i].SHA] {
				f.History = true
			}
			findings = append(findings, f)
			if len(findings) >= memorySecretMaxFindings {
				break
			}
		}
	}
	_ = stdout.Close()
	_ = cmd.Wait()
	return memoryDedupeFindings(findings), nil
}

// memoryDedupeFindings folds identical (path, line, rule) triples into one. Scanning history
// reports the same line once per revision, and without this the list is unreadable.
func memoryDedupeFindings(in []memorySecretFinding) []memorySecretFinding {
	out := make([]memorySecretFinding, 0, len(in))
	seen := map[string]int{}
	for _, f := range in {
		key := fmt.Sprintf("%s\x00%d\x00%s", f.Path, f.Line, f.Rule)
		if i, ok := seen[key]; ok {
			// Present in the current tree too wins: it is then not "history only".
			if !f.History {
				out[i].History = false
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, f)
	}
	return out
}
