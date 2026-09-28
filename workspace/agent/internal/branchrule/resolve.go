package branchrule

import (
	"regexp"
	"sort"
	"strings"
)

// Warning is advisory. Code is stable for the Console to translate; Message is the English
// fallback.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Item is the work item a name is rendered for.
type Item struct {
	Provider string   `json:"provider,omitempty"`
	Key      string   `json:"key"`
	Title    string   `json:"title,omitempty"`
	Type     string   `json:"type,omitempty"`
	Labels   []string `json:"labels,omitempty"`
}

// KindView is one resolved kind: what the rename chips offer.
type KindView struct {
	Kind   string `json:"kind"`
	Prefix string `json:"prefix"`
	Base   string `json:"base"`
}

// resolver answers field lookups over the matching rules of each layer, strongest first.
type resolver struct {
	layers  []Layer
	rules   [][]Rule
	kindSet map[string]bool
}

func newResolver(layers []Layer, id string) *resolver {
	r := &resolver{layers: layers, kindSet: map[string]bool{}}
	for _, l := range layers {
		rs := matching(l, id)
		r.rules = append(r.rules, rs)
		if l.Repository {
			for _, x := range rs {
				for _, k := range x.Declares {
					r.kindSet[k] = true
				}
			}
		}
	}
	return r
}

func (r *resolver) label(li int, rule Rule, field string) string {
	return r.layers[li].Name + ": " + rule.label(field)
}

// first walks layers, then the layer's rules most specific first, and returns the first
// value set (decision 2).
func (r *resolver) first(field string, get func(Rule) *string) (string, string, bool) {
	for li, rs := range r.rules {
		for _, x := range rs {
			if v := get(x); v != nil {
				return *v, r.label(li, x, field), true
			}
		}
	}
	return "", "", false
}

func (r *resolver) name() (string, string) {
	v, src, _ := r.first("name", func(x Rule) *string { return x.Name })
	return v, src
}

func (r *resolver) prefix(kind string) (string, string) {
	field := "types." + kind + ".prefix"
	v, src, _ := r.first(field, func(x Rule) *string { return x.Types[kind].Prefix })
	return v, src
}

// base is decision 5 without the typed value: per layer, the kind's base before the rule's
// base, then the built-in `head`.
func (r *resolver) base(kind string) (string, string) {
	for li, rs := range r.rules {
		field := "types." + kind + ".base"
		for _, x := range rs {
			if v := x.Types[kind].Base; v != nil {
				return *v, r.label(li, x, field)
			}
		}
		for _, x := range rs {
			if x.Base != nil {
				return *x.Base, r.label(li, x, "base")
			}
		}
	}
	return "head", "builtin"
}

// kinds is the kind set when the repository declares one, else the whole vocabulary.
func (r *resolver) kinds() []string {
	if len(r.kindSet) == 0 {
		return Kinds
	}
	var out []string
	for _, k := range Kinds {
		if r.kindSet[k] {
			out = append(out, k)
		}
	}
	return out
}

// kindFor picks the kind (decision 4): an explicit kind, else the tracker type then the
// labels, each looked up in `from` in field order and then in the built-in map; `feature`
// when nothing maps. The repository kind set is applied last.
func (r *resolver) kindFor(item *Item, explicit string) (string, string, []Warning) {
	var warns []Warning
	kind, src := "", ""
	if explicit != "" {
		if ValidKind(explicit) {
			kind, src = explicit, "request"
		} else {
			warns = append(warns, Warning{"unknown_kind", "kind " + quote(explicit) + " is not in the vocabulary; ignored"})
		}
	}
	if kind == "" && item != nil {
		kind, src = r.kindFromItem(item)
	}
	if kind == "" {
		kind, src = "feature", "builtin"
	}
	if len(r.kindSet) > 0 && !r.kindSet[kind] && kind != "feature" {
		src = "repository kind set (" + kind + " is not declared)"
		kind = "feature"
	}
	return kind, src, warns
}

func (r *resolver) kindFromItem(item *Item) (string, string) {
	// Each kind's `from` is a field of its own, supplied by the first rule that sets it;
	// the kinds are then tried in the order of their supplying rules.
	type fromOf struct {
		kind   string
		values []string
		rank   int
		src    string
	}
	var froms []fromOf
	for vi, k := range Kinds {
		rank := 0
	found:
		for li, rs := range r.rules {
			for _, x := range rs {
				rank++
				if v := x.Types[k].From; len(v) > 0 {
					froms = append(froms, fromOf{k, v, rank*len(Kinds) + vi, r.label(li, x, "types."+k+".from")})
					break found
				}
			}
		}
	}
	sort.SliceStable(froms, func(i, j int) bool { return froms[i].rank < froms[j].rank })
	var values []string
	if t := strings.TrimSpace(item.Type); t != "" {
		values = append(values, t)
	}
	for _, l := range item.Labels {
		if l = strings.TrimSpace(l); l != "" {
			values = append(values, l)
		}
	}
	for _, v := range values {
		for _, f := range froms {
			for _, fv := range f.values {
				if strings.EqualFold(fv, v) {
					return f.kind, f.src + " (" + v + ")"
				}
			}
		}
		for _, b := range builtinFrom {
			if strings.EqualFold(b.value, v) {
				return b.kind, "builtin (" + v + ")"
			}
		}
	}
	return "", ""
}

// Effective is the rule after merging, without a work item.
type Effective struct {
	Name    string            `json:"name"`
	Base    string            `json:"base"`
	Kinds   []KindView        `json:"kinds"`
	Sources map[string]string `json:"-"`
}

// Effect merges the layers for the repository id.
func Effect(layers []Layer, id string) Effective {
	r := newResolver(layers, id)
	e := Effective{Sources: map[string]string{}}
	e.Name, e.Sources["name"] = r.name()
	e.Base, e.Sources["base"] = r.base("feature")
	for _, k := range r.kinds() {
		p, ps := r.prefix(k)
		b, bs := r.base(k)
		e.Kinds = append(e.Kinds, KindView{Kind: k, Prefix: p, Base: b})
		e.Sources["kinds."+k+".prefix"] = ps
		e.Sources["kinds."+k+".base"] = bs
	}
	return e
}

// Request is the input of Name.
type Request struct {
	Item *Item
	Kind string
	Slug string
}

// NameResult is what POST …/branch-name answers, before the base is checked against git.
type NameResult struct {
	Name      string
	NameEmpty bool
	Base      string
	Kind      string
	Warnings  []Warning
	Sources   map[string]string
}

// Name renders the branch name and picks the base for req.
func Name(layers []Layer, id string, req Request) NameResult {
	r := newResolver(layers, id)
	res := NameResult{Sources: map[string]string{}}
	kind, ksrc, warns := r.kindFor(req.Item, strings.TrimSpace(req.Kind))
	res.Kind, res.Warnings = kind, warns
	res.Sources["kind"] = ksrc
	tmpl, tsrc := r.name()
	res.Sources["name"] = tsrc
	prefix, psrc := r.prefix(kind)
	res.Sources["prefix"] = psrc
	res.Base, res.Sources["base"] = r.base(kind)

	vals := placeholders(req.Item)
	vals["type"] = kind
	vals["prefix"] = prefix
	if s := strings.TrimSpace(req.Slug); s != "" {
		vals["slug"] = TitleSlug(s)
	}
	raw, unknown := render(tmpl, vals)
	for _, u := range unknown {
		res.Warnings = append(res.Warnings, Warning{"unknown_placeholder", "placeholder {" + u + "} is not known; rendered empty"})
	}
	// Checked on the raw rendering: sanitising drops the empty last segment and would turn
	// `feature/` into a bare `feature` that no longer looks like "only the prefix".
	if stripSep(raw) == stripSep(prefix) {
		if vals["slug"] == "" {
			res.NameEmpty = true
			return res
		}
		raw = prefix + vals["slug"]
	}
	res.Name = Sanitize(raw)
	res.NameEmpty = res.Name == ""
	return res
}

var jiraKeyRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)-(\d+)$`)
var keyCharsRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// placeholders fills {ref} {num} {key} {project} {slug} from the item (decision 4). With no
// item they render empty.
func placeholders(item *Item) map[string]string {
	v := map[string]string{"ref": "", "num": "", "key": "", "project": "", "slug": ""}
	if item == nil {
		return v
	}
	key := strings.TrimSpace(item.Key)
	if i := strings.LastIndex(key, "#"); i >= 0 {
		// GitHub / Bitbucket `owner/repo#N`: the working copy already says which repo it is.
		ref := key[i+1:]
		v["ref"], v["num"], v["key"] = ref, ref, "issue-"+ref
	} else {
		v["ref"], v["key"] = key, key
		if m := jiraKeyRe.FindStringSubmatch(key); m != nil {
			v["project"], v["num"] = m[1], m[2]
		}
	}
	for _, k := range []string{"ref", "num", "key", "project"} {
		v[k] = keyCharsRe.ReplaceAllString(v[k], "-")
	}
	v["slug"] = TitleSlug(item.Title)
	return v
}

var placeholderRe = regexp.MustCompile(`\{([A-Za-z_]+)\}`)

func render(tmpl string, vals map[string]string) (string, []string) {
	var unknown []string
	out := placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		k := m[1 : len(m)-1]
		if v, ok := vals[k]; ok {
			return v
		}
		unknown = append(unknown, k)
		return ""
	})
	return strings.TrimSpace(out), unknown
}

func stripSep(s string) string {
	return strings.Trim(s, "/-_. ")
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// TitleSlug is the Console's titleSlug: ASCII only, so a Japanese title gives "".
func TitleSlug(title string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	return s
}

var branchCharsRe = regexp.MustCompile(`[^A-Za-z0-9._/-]+`)
var dotsRe = regexp.MustCompile(`\.\.+`)

// Sanitize is the Console's sanitizeBranch: only [A-Za-z0-9._/-] survives, empty segments
// collapse, and a separator left by an empty placeholder is dropped.
func Sanitize(raw string) string {
	var segs []string
	for _, seg := range strings.Split(branchCharsRe.ReplaceAllString(raw, "-"), "/") {
		seg = strings.TrimLeft(strings.TrimRight(seg, "-."), "-.")
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	out := dotsRe.ReplaceAllString(strings.Join(segs, "/"), ".")
	if strings.HasSuffix(strings.ToLower(out), ".lock") {
		out = out[:len(out)-len(".lock")]
	}
	return out
}

// CheckPrefix warns when name starts with none of the resolved prefixes (decision 8).
// `temp/` is the Agent's own deferred name and is exempt; a kind with an empty prefix
// accepts every name.
func CheckPrefix(name string, kinds []KindView) []Warning {
	if strings.HasPrefix(name, "temp/") {
		return nil
	}
	var ps []string
	for _, k := range kinds {
		if k.Prefix == "" {
			return nil
		}
		if strings.HasPrefix(name, k.Prefix) {
			return nil
		}
		ps = append(ps, k.Prefix)
	}
	if len(ps) == 0 {
		return nil
	}
	return []Warning{{"prefix_mismatch", "the name starts with none of the resolved prefixes (" + strings.Join(ps, " ") + ")"}}
}

func quote(s string) string { return "\"" + s + "\"" }
