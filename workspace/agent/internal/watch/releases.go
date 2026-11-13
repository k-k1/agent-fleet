package watch

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The GitHub releases and tags sources of ADR 0133 §9 ("a release tag published"). Each is its
// own source, as github_actions is, so the engine checks the event types of a ref's own kind: a
// releases watch cannot be asked for github.tag.created and then wait for it forever. Reads go
// through the GitHub source (member's token, ETag, the shared share of the hourly limit).
//
// Two subjects:
//
//	releases  github:o/r/releases[-pre]/after/<floor id>-<floor unix>[/match/<hex glob>]
//	tags      github:o/r/tags/after/<floor unix>[/match/<hex glob>]
//
// "New" is decided from a floor read from GitHub when the watch is made, never from this host's
// clock. A release is new when it is published after the floor's (published_at, id) pair; a draft
// that was created long ago and published later is new, so the id alone is not the rule. A tag
// carries no date and GitHub lists tags in name order, so the floor is the set of tag names that
// existed (see tagCursor). The pattern is part of the subject because the subject is all Poll
// has; it is hex so the glob characters stay inside the header token set.

// Event types of the releases and tags sources.
const (
	EventReleasePublished = "github.release.published"
	EventTagCreated       = "github.tag.created"
)

// Source names.
const (
	ReleasesKind = "github_releases"
	TagsKind     = "github_tags"
)

const (
	releasesIdleAfter = 5 * time.Minute // a 304 is free; a release is not a minutes-race
	releasesPerPage   = 30              // newest releases only: the floor and new ones are at the top
	tagsPerPage       = 100
	// tagsMaxPages bounds how far the tag list is followed (300 tags). A tag listed past it is
	// never seen, new or not; the tool description says so.
	tagsMaxPages = 3
	// globMax bounds the pattern. Printable ASCII only, and only the characters a git tag name
	// uses in practice plus the two wildcards.
	globMax = 64
	// tagNameMax bounds a tag name that may be told.
	tagNameMax = 100
	// releaseNotesMax bounds the release body kept in a pending event.
	releaseNotesMax = 300
)

var (
	relRepo      = `[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`
	relGlobRE    = regexp.MustCompile(`^[A-Za-z0-9._/+*?-]{1,` + strconv.Itoa(globMax) + `}$`)
	relTagNameRE = regexp.MustCompile(`^[A-Za-z0-9._/+-]{1,` + strconv.Itoa(tagNameMax) + `}$`)

	relURLRE  = regexp.MustCompile(`^https://github\.com/(` + relRepo + `)/(releases|tags)/?(?:[?#].*)?$`)
	relRefRE  = regexp.MustCompile(`^(?:(` + relRepo + `):)?(releases|tags)(\+pre)?(@.*)?$`)
	relSubjRE = regexp.MustCompile(`^github:(` + relRepo + `)/releases(-pre)?/after/([0-9]{1,18})-([0-9]{1,12})(?:/match/([0-9a-f]{2,` + strconv.Itoa(2*globMax) + `}))?$`)
	tagSubjRE = regexp.MustCompile(`^github:(` + relRepo + `)/tags/after/([0-9]{1,12})(?:/match/([0-9a-f]{2,` + strconv.Itoa(2*globMax) + `}))?$`)
)

// releasesRef is a parsed reference.
type releasesRef struct {
	repo string
	tags bool // tags, else releases
	pre  bool // releases only: pre-releases count too
	glob string
}

const releasesRefHelp = "give o/r:releases for new releases or o/r:tags for new tags, optionally narrowed to a tag name pattern " +
	"(o/r:releases@v2.*), or o/r:releases+pre to count pre-releases too; the releases URL https://github.com/o/r/releases works as o/r:releases"

// LooksLikeReleasesRef reports whether ref is in a releases or tags form, so the watch handler can
// send it to the right source when the caller left the source at its github default. Pull
// request and Actions references never match.
func LooksLikeReleasesRef(ref string) bool {
	_, ok := matchReleasesRef(strings.TrimSpace(ref), "x/x")
	return ok
}

// ReleasesKindOf is the source a releases or tags ref belongs to, "" for any other ref.
func ReleasesKindOf(ref string) string {
	r, ok := matchReleasesRef(strings.TrimSpace(ref), "x/x")
	switch {
	case !ok:
		return ""
	case r.tags:
		return TagsKind
	}
	return ReleasesKind
}

func matchReleasesRef(ref, defaultRepo string) (releasesRef, bool) {
	if m := relURLRE.FindStringSubmatch(ref); m != nil {
		return releasesRef{repo: m[1], tags: m[2] == "tags"}, true
	}
	if m := relRefRE.FindStringSubmatch(ref); m != nil {
		tags := m[2] == "tags"
		if tags && m[3] != "" {
			return releasesRef{}, false
		}
		if m[4] == "@" { // a pattern was announced and left empty
			return releasesRef{}, false
		}
		return releasesRef{repo: orDefault(m[1], defaultRepo), tags: tags, pre: m[3] != "", glob: strings.TrimPrefix(m[4], "@")}, true
	}
	return releasesRef{}, false
}

func parseReleasesRef(ref, kind, defaultRepo string) (releasesRef, error) {
	ref = strings.TrimSpace(ref)
	r, ok := matchReleasesRef(ref, defaultRepo)
	if !ok {
		if m := relRefRE.FindStringSubmatch(ref); m != nil && m[2] == "tags" && m[3] != "" {
			return releasesRef{}, &ErrRefused{"+pre applies to releases only; a tag has no pre-release state"}
		}
		return releasesRef{}, &ErrRefused{releasesRefHelp}
	}
	if r.tags != (kind == TagsKind) {
		return releasesRef{}, &ErrRefused{"that reference belongs to source " + map[bool]string{true: TagsKind, false: ReleasesKind}[r.tags]}
	}
	if r.repo == "" {
		return releasesRef{}, &ErrRefused{"this form needs a GitHub repository in this session's working copy; give o/r:" + map[bool]string{true: "tags", false: "releases"}[r.tags]}
	}
	if !validRepo(r.repo) {
		return releasesRef{}, &ErrRefused{releasesRefHelp}
	}
	r.repo = strings.ToLower(r.repo)
	if r.glob != "" && !relGlobRE.MatchString(r.glob) {
		return releasesRef{}, &ErrRefused{fmt.Sprintf("the tag pattern must be 1 to %d printable ASCII characters: letters, digits and . _ - / + with * (any run) and ? (one character)", globMax)}
	}
	return r, nil
}

// globMatch reports whether name matches the pattern, where * is any run (also empty, also across
// "/") and ? is one character. The pattern was checked by relGlobRE, so it holds no other
// metacharacter. An empty pattern matches everything.
func globMatch(pattern, name string) bool {
	if pattern == "" {
		return true
	}
	px, nx := 0, 0
	star, mark := -1, 0
	for nx < len(name) {
		switch {
		case px < len(pattern) && pattern[px] == '*':
			star, mark = px, nx
			px++
		case px < len(pattern) && (pattern[px] == '?' || pattern[px] == name[nx]):
			px++
			nx++
		case star >= 0:
			px = star + 1
			mark++
			nx = mark
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

func encodeGlob(g string) string {
	if g == "" {
		return ""
	}
	return "/match/" + hex.EncodeToString([]byte(g))
}

func decodeGlob(h string) string {
	b, _ := hex.DecodeString(h)
	return string(b)
}

func releasesSubject(repo string, pre bool, glob string, floorID, floorUnix int64) SubjectID {
	kind := "releases"
	if pre {
		kind += "-pre"
	}
	return SubjectID(fmt.Sprintf("github:%s/%s/after/%d-%d%s", repo, kind, floorID, floorUnix, encodeGlob(glob)))
}

func tagsSubject(repo, glob string, floorUnix int64) SubjectID {
	return SubjectID(fmt.Sprintf("github:%s/tags/after/%d%s", repo, floorUnix, encodeGlob(glob)))
}

func releasesPath(repo string) string {
	return fmt.Sprintf("/repos/%s/releases?per_page=%d", repo, releasesPerPage)
}

func tagsPath(repo string, page int) string {
	return fmt.Sprintf("/repos/%s/tags?per_page=%d&page=%d", repo, tagsPerPage, page)
}

const goneReleases = "the repository was deleted or can no longer be read with your GitHub connection"

// ghDate is GitHub's own clock from an answer's Date header, 0 when absent.
func ghDate(h http.Header) int64 {
	if d, err := http.ParseTime(h.Get("Date")); err == nil {
		return d.Unix()
	}
	return 0
}

// ---- releases ----

type ghRelease struct {
	ID          int64  `json:"id"`
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	HTMLURL     string `json:"html_url"`
}

// publishedUnix is the publication time, ok false for a draft or an unreadable time: a release
// that cannot be shown to be new is never new.
func (r ghRelease) publishedUnix() (int64, bool) {
	if r.Draft {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, r.PublishedAt)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}

// GitHubReleases watches new releases.
type GitHubReleases struct {
	GH *GitHub
}

// NewGitHubReleases returns the releases source over gh.
func NewGitHubReleases(gh *GitHub) *GitHubReleases { return &GitHubReleases{GH: gh} }

func (a *GitHubReleases) Kind() string    { return ReleasesKind }
func (a *GitHubReleases) Types() []string { return []string{EventReleasePublished} }

// releasesCursor keeps the list and the newest release already reported. A release is new only
// above both the floor and that mark, so a later edit (the newest turned back into a draft, its
// tag renamed out of the pattern) cannot make an older candidate the best again and tell it twice.
type releasesCursor struct {
	List   ghCached[[]ghRelease] `json:"list"`
	MarkAt int64                 `json:"mark_at,omitempty"`
	MarkID int64                 `json:"mark_id,omitempty"`
}

func (a *GitHubReleases) Resolve(ctx context.Context, ref, defaultRepo string) (Resolved, error) {
	r, err := parseReleasesRef(ref, ReleasesKind, defaultRepo)
	if err != nil {
		return Resolved{}, err
	}
	var list []ghRelease
	status, _, hdr, err := a.GH.getHdr(ctx, releasesPath(r.repo), "", &list)
	switch {
	case err != nil:
		return Resolved{}, err
	case status == http.StatusNotFound || status == http.StatusForbidden:
		return Resolved{}, &ErrRefused{fmt.Sprintf("%s is not a repository your GitHub connection can read", r.repo)}
	case status != http.StatusOK:
		return Resolved{}, fmt.Errorf("GitHub answered %d", status)
	}
	floorID, floorUnix := releaseFloor(list)
	if floorUnix == 0 {
		// Nothing published yet: GitHub's clock, to the minute (earlier is the safe side, there
		// is no release to be old).
		floorUnix = ghDate(hdr)
		floorUnix -= floorUnix % 60
	}
	subj := releasesSubject(r.repo, r.pre, r.glob, floorID, floorUnix)
	if !TokenOK(string(subj)) {
		return Resolved{}, &ErrRefused{"that reference is too long"}
	}
	return Resolved{Subject: subj, URL: SafeURL("https://github.com/" + r.repo + "/releases")}, nil
}

// releaseFloor is the newest (published_at, id) among the published releases listed, pre-releases
// included whatever the watch asks for: they are all history.
func releaseFloor(list []ghRelease) (id, unix int64) {
	for _, r := range list {
		if u, ok := r.publishedUnix(); ok && (u > unix || u == unix && r.ID > id) {
			id, unix = r.ID, u
		}
	}
	return id, unix
}

func (a *GitHubReleases) Poll(ctx context.Context, subject SubjectID, raw json.RawMessage, _ []string) ([]Event, json.RawMessage, Hint, error) {
	m := relSubjRE.FindStringSubmatch(string(subject))
	if m == nil {
		return nil, raw, Hint{}, fmt.Errorf("not a github_releases subject: %s", subject)
	}
	repo, pre, floorID, floorUnix, glob := m[1], m[2] != "", atoi64(m[3]), atoi64(m[4]), decodeGlob(m[5])
	var cur releasesCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	if err := actionsGet(a.GH, ctx, releasesPath(repo), &cur.List, goneReleases); err != nil {
		return nil, raw, Hint{}, err
	}
	var best *ghRelease
	var bestAt int64
	for i := range cur.List.Val {
		r := &cur.List.Val[i]
		u, ok := r.publishedUnix()
		if !ok || (u < floorUnix || u == floorUnix && r.ID <= floorID) || (u < cur.MarkAt || u == cur.MarkAt && r.ID <= cur.MarkID) {
			continue
		}
		if (r.Prerelease && !pre) || !relTagNameRE.MatchString(r.TagName) || !globMatch(glob, r.TagName) {
			continue
		}
		if best == nil || u > bestAt || u == bestAt && r.ID > best.ID {
			best, bestAt = r, u
		}
	}
	if best != nil {
		cur.MarkAt, cur.MarkID = bestAt, best.ID
	}
	out, _ := json.Marshal(cur)
	var evs []Event
	if best != nil {
		kind := "release"
		if best.Prerelease {
			kind = "pre-release"
		}
		data := []DataItem{{Text: "Release name: " + best.Name, URL: SafeURL(best.HTMLURL)}}
		if n := strings.TrimSpace(best.Body); n != "" {
			data = append(data, DataItem{Text: "Release notes: " + CleanText(n, releaseNotesMax)})
		}
		evs = []Event{{
			Source: ReleasesKind, Subject: subject, Type: EventReleasePublished, Version: strconv.FormatInt(best.ID, 10),
			Summary: fmt.Sprintf("New %s published in %s: tag %s.", kind, repo, best.TagName), Data: data, At: a.GH.clock().UTC(),
		}}
	}
	return evs, out, Hint{After: releasesIdleAfter}, nil
}

// ---- tags ----

type ghTag struct {
	Name string `json:"name"`
}

// GitHubTags watches new tags.
type GitHubTags struct {
	GH *GitHub
}

// NewGitHubTags returns the tags source over gh.
func NewGitHubTags(gh *GitHub) *GitHubTags { return &GitHubTags{GH: gh} }

func (a *GitHubTags) Kind() string    { return TagsKind }
func (a *GitHubTags) Types() []string { return []string{EventTagCreated} }

// tagsMaxKnown bounds the persisted known-name set and the generation counts. It is a variable
// only so a test can reach it. Reaching it ends the watch (goneTagState) instead of dropping
// names: a name forgotten while still listed would be told as new, and a generation count that
// stops growing would suppress a recreated tag.
var tagsMaxKnown = 2000

const goneTagState = "this repository's tag history grew past what a watch can track; watch its releases instead"

// tagCursor is what one Poll keeps for the next. Known is the set of tag names that are history:
// the ones that existed when the watch was made (seeded by Resolve) plus every one already
// reported. A tag is new when it is listed and not in Known. GitHub's list is not time-ordered
// and carries no dates, so neither a re-ordered list nor a page shift can make an old tag look new.
//
// A name leaves Known only after it was missing from two complete reads in a row (Absent holds
// the first), so a tag deleted and created again is new, while one tag that slipped between two
// pages during a read is not. Only a read that reached the end of the list (a page shorter than
// tagsPerPage) is complete; a longer list is read to tagsMaxPages and forgets nothing.
type tagCursor struct {
	Pages  map[string]ghCached[[]ghTag] `json:"pages,omitempty"`
	Known  []string                     `json:"known,omitempty"`
	Absent []string                     `json:"absent,omitempty"`
	// Gen counts how often a name was forgotten and came back. It is part of the version of a
	// created tag (the name, then name#2, ...), because the engine tells one version once and a
	// tag created again must be news even when its name was the last one told.
	Gen    map[string]int `json:"gen,omitempty"`
	Seeded bool           `json:"seeded,omitempty"`
}

type tagSeed struct {
	Known []string `json:"known"`
}

// readTags reads the tag list page by page with each page's own ETag. complete is true when the
// last page was short.
func readTags(ctx context.Context, g *GitHub, repo string, pages map[string]ghCached[[]ghTag], gone string) (names []string, complete bool, next map[string]ghCached[[]ghTag], err error) {
	next = map[string]ghCached[[]ghTag]{}
	for page := 1; page <= tagsMaxPages; page++ {
		path := tagsPath(repo, page)
		c := pages[path]
		if err := actionsGet(g, ctx, path, &c, gone); err != nil {
			return nil, false, nil, err
		}
		next[path] = c
		for _, t := range c.Val {
			names = append(names, t.Name)
		}
		if len(c.Val) < tagsPerPage {
			return names, true, next, nil
		}
	}
	return names, false, next, nil
}

func (a *GitHubTags) Resolve(ctx context.Context, ref, defaultRepo string) (Resolved, error) {
	r, err := parseReleasesRef(ref, TagsKind, defaultRepo)
	if err != nil {
		return Resolved{}, err
	}
	var first []ghTag
	status, _, hdr, err := a.GH.getHdr(ctx, tagsPath(r.repo, 1), "", &first)
	switch {
	case err != nil:
		return Resolved{}, err
	case status == http.StatusNotFound || status == http.StatusForbidden:
		return Resolved{}, &ErrRefused{fmt.Sprintf("%s is not a repository your GitHub connection can read", r.repo)}
	case status != http.StatusOK:
		return Resolved{}, fmt.Errorf("GitHub answered %d", status)
	}
	names := make([]string, 0, len(first))
	for _, t := range first {
		names = append(names, t.Name)
	}
	// The floor must be the whole list: a name that is merely past the page cap would be taken
	// for new the day a deletion moves it into view. A longer list is refused, never half-seeded.
	last := len(first)
	for page := 2; last == tagsPerPage && page <= tagsMaxPages+1; page++ {
		var more []ghTag
		st, _, _, err := a.GH.getHdr(ctx, tagsPath(r.repo, page), "", &more)
		if err != nil {
			return Resolved{}, err
		}
		if st != http.StatusOK {
			return Resolved{}, fmt.Errorf("GitHub answered %d for the tag list", st)
		}
		if page > tagsMaxPages {
			if len(more) > 0 {
				return Resolved{}, &ErrRefused{fmt.Sprintf("%s has more than %d tags; its tag list cannot be read in full, so a new tag could not be told from an old one. Watch its releases instead (o/r:releases)", r.repo, tagsMaxPages*tagsPerPage)}
			}
			break
		}
		last = len(more)
		for _, t := range more {
			names = append(names, t.Name)
		}
	}
	subj := tagsSubject(r.repo, r.glob, ghDate(hdr))
	if !TokenOK(string(subj)) {
		return Resolved{}, &ErrRefused{"that reference is too long"}
	}
	seed, _ := json.Marshal(tagSeed{Known: sortedNames(names)})
	return Resolved{Subject: subj, URL: SafeURL("https://github.com/" + r.repo + "/tags"), Seed: seed}, nil
}

// SeedCursor records the tags that existed when a watch was made. A subject another watch of the
// same second already reads keeps what it knows and adds these.
func (*GitHubTags) SeedCursor(raw, seed json.RawMessage) json.RawMessage {
	var cur tagCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	var s tagSeed
	if json.Unmarshal(seed, &s) != nil {
		return raw
	}
	cur.Known = sortedNames(append(cur.Known, s.Known...))
	cur.Seeded = true
	out, _ := json.Marshal(cur)
	return out
}

// sortedNames returns the names sorted and without duplicates.
func sortedNames(names []string) []string {
	slices.Sort(names)
	return slices.Compact(names)
}

func (a *GitHubTags) Poll(ctx context.Context, subject SubjectID, raw json.RawMessage, _ []string) ([]Event, json.RawMessage, Hint, error) {
	m := tagSubjRE.FindStringSubmatch(string(subject))
	if m == nil {
		return nil, raw, Hint{}, fmt.Errorf("not a github_tags subject: %s", subject)
	}
	repo, glob := m[1], decodeGlob(m[3])
	var cur tagCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	listed, complete, pages, err := readTags(ctx, a.GH, repo, cur.Pages, goneReleases)
	if err != nil {
		return nil, raw, Hint{}, err
	}
	cur.Pages = pages
	seen := make(map[string]bool, len(listed))
	for _, n := range listed {
		seen[n] = true
	}
	known := make(map[string]bool, len(cur.Known))
	for _, n := range cur.Known {
		known[n] = true
	}
	var fresh []string
	if !cur.Seeded {
		// No floor was recorded (state from before it, or a lost seed): what is there is history.
		cur.Seeded = true
	} else {
		for _, n := range listed {
			if !known[n] && relTagNameRE.MatchString(n) && globMatch(glob, n) {
				fresh = append(fresh, n)
			}
		}
	}
	// Every listed name is history from now on, whether or not it matched.
	for n := range seen {
		known[n] = true
	}
	var absent []string
	if complete {
		prev := make(map[string]bool, len(cur.Absent))
		for _, n := range cur.Absent {
			prev[n] = true
		}
		for n := range known {
			if seen[n] {
				continue
			}
			if prev[n] {
				delete(known, n)
				if cur.Gen == nil {
					cur.Gen = map[string]int{}
				}
				if _, has := cur.Gen[n]; !has && len(cur.Gen) >= tagsMaxKnown {
					return nil, raw, Hint{}, &ErrGone{goneTagState}
				}
				cur.Gen[n]++
			} else {
				absent = append(absent, n)
			}
		}
	}
	cur.Absent = sortedNames(absent)
	all := make([]string, 0, len(known))
	for n := range known {
		all = append(all, n)
	}
	if len(all) > tagsMaxKnown {
		return nil, raw, Hint{}, &ErrGone{goneTagState}
	}
	cur.Known = sortedNames(all)
	out, _ := json.Marshal(cur)

	var evs []Event
	if len(fresh) > 0 {
		sort.Strings(fresh)
		newest := fresh[len(fresh)-1] // names carry no date; the greatest is the best guess at the latest
		version := newest
		if g := cur.Gen[newest]; g > 0 {
			version = fmt.Sprintf("%s#%d", newest, g+1)
		}
		var data []DataItem
		for _, n := range fresh[:len(fresh)-1] {
			data = append(data, DataItem{Text: "Also created: " + n})
		}
		evs = []Event{{
			Source: TagsKind, Subject: subject, Type: EventTagCreated, Version: version,
			Summary: fmt.Sprintf("New tag created in %s: %s.", repo, newest), Data: data, At: a.GH.clock().UTC(),
		}}
	}
	return evs, out, Hint{After: releasesIdleAfter}, nil
}
