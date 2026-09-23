package imagegen

// The agent's side of a studio (ADR 0100 decisions 3 and 5): what get_image_studio answers
// (GET …/{id}?view=agent&session=) and the persona the pane sends as the first turn.
//
// Context is pulled, never pushed: the persona tells the agent to call get_image_studio first
// on every message, and this answer is everything it needs to reply — the draft, the locks,
// what changed since its last call, the model's facts and the knowledge summary. It rides in
// the conversation on every turn, which is why it is capped.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// studioAgentViewMax is get_image_studio's size cap. A guess (ADR 0100 未解決 4), to be
// corrected against real sessions.
const studioAgentViewMax = 8 * 1024

// studioSinceMax is how many changes since the last call are spelled out; the rest is a count.
const studioSinceMax = 5

type studioAgentView struct {
	Studio     string           `json:"studio"`
	Title      string           `json:"title,omitempty"`
	Draft      ImageStudioDraft `json:"draft"`
	Locks      []string         `json:"locks"`
	UserFields []string         `json:"user_only_fields"`
	NeedsMask  bool             `json:"needs_mask,omitempty"`
	AgentTrial bool             `json:"agent_trial"`
	Since      studioSince      `json:"since"`
	// Model is the chosen model's facts; Models is what the user can choose from when no model
	// is chosen yet — the material for suggest_model.
	Model     *studioModelFacts      `json:"model,omitempty"`
	Models    []studioModelChoice    `json:"models,omitempty"`
	Knowledge []studioKnowledgeBrief `json:"knowledge,omitempty"`
	Versions  []studioVersionBrief   `json:"versions,omitempty"`
	Truncated bool                   `json:"truncated,omitempty"`
	Note      string                 `json:"note"`
}

type studioSince struct {
	First bool         `json:"first_call,omitempty"`
	Items []studioNews `json:"items"`
	More  int          `json:"more,omitempty"`
}

// studioNews is one thing that happened since the agent last read the studio.
type studioNews struct {
	// Kind is "edit", "rewind", "press", "failed" or "result".
	Kind     string   `json:"kind"`
	At       string   `json:"at,omitempty"`
	By       string   `json:"by,omitempty"`
	Version  string   `json:"version,omitempty"`
	Changes  []string `json:"changes,omitempty"`
	RewindTo int      `json:"rewind_to,omitempty"`
	Path     string   `json:"path,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type studioModelFacts struct {
	Provider    string   `json:"provider"`
	ID          string   `json:"id"`
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Family      string   `json:"family,omitempty"`
	Knobs       []string `json:"reads,omitempty"`
	Ops         []string `json:"ops,omitempty"`
	Sizes       []string `json:"sizes,omitempty"`
	// Defaults are what runs for a field the draft leaves empty.
	Defaults  *EngineParams `json:"defaults,omitempty"`
	Negative  string        `json:"row_negative,omitempty"`
	MaxInputs int           `json:"max_inputs,omitempty"`
	familyAdvice
	Loras []studioLoraFact `json:"loras,omitempty"`
	// Missing says the draft names a model the provider no longer offers.
	Missing bool `json:"missing,omitempty"`
}

type studioLoraFact struct {
	Name         string   `json:"name"`
	TrainedWords []string `json:"trigger_words,omitempty"`
	Weight       float64  `json:"weight,omitempty"`
}

type studioModelChoice struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	Family string `json:"family,omitempty"`
}

type studioKnowledgeBrief struct {
	Scope     string `json:"scope"`
	Key       string `json:"key"`
	Summary   string `json:"summary"`
	Truncated bool   `json:"summary_truncated,omitempty"`
}

type studioVersionBrief struct {
	Version string `json:"version"`
	Mode    string `json:"mode"`
	By      string `json:"by,omitempty"`
	At      string `json:"at"`
	// State is the press_result's, or "pending" while there is none.
	State  string `json:"state"`
	Prompt string `json:"prompt,omitempty"`
}

func handleStudioAgentView(w http.ResponseWriter, r *http.Request, id string) {
	self := r.URL.Query().Get("session")
	unlock := lockStudio(id)
	defer unlock()
	rec, err := loadStudio(id)
	if err != nil {
		writeStudioErr(w, err)
		return
	}
	if self == "" || self != rec.Session {
		httpx.WriteErr(w, http.StatusConflict, "studio_not_bound",
			"this session is not the one bound to the studio; the user can bind it again from the studio pane")
		return
	}
	entries := readStudioLog(id)
	history := readHistory()
	seen, had := rec.Seen[self]
	view := studioAgentView{
		Studio: rec.ID, Title: rec.Title, Draft: rec.Draft, Locks: append([]string{}, rec.Locks...),
		NeedsMask: studioNeedsMask(rec.Draft), AgentTrial: rec.AgentTrial,
		Since:    studioSinceFor(id, self, entries, history, seen, had),
		Versions: studioVersions(entries, 5),
		Note: "Change the draft with set_image_draft. Generating is the user's button; " +
			"run_image_trial makes one trial picture when allowed. Pictures are files: open one only when you need to look.",
	}
	for _, k := range studioDraftKeys {
		if !slices.Contains(ImageStudioAgentFields, k) {
			view.UserFields = append(view.UserFields, k)
		}
	}
	view.Model, view.Models = studioModelFactsFor(r.Context(), rec.Draft)
	if view.Model != nil {
		if k, err := readKnowledge(KnowledgeFamily, view.Model.Family); err == nil && view.Model.Family != "" && k.Summary != "" {
			view.Knowledge = append(view.Knowledge, studioKnowledgeBrief{Scope: k.Scope, Key: k.Key, Summary: k.Summary, Truncated: k.SummaryTruncated})
		}
	}
	if rec.Draft.Model != "" {
		if k, err := readKnowledge(KnowledgeModel, rec.Draft.Model); err == nil && k.Summary != "" {
			view.Knowledge = append(view.Knowledge, studioKnowledgeBrief{Scope: k.Scope, Key: k.Key, Summary: k.Summary, Truncated: k.SummaryTruncated})
		}
	}
	body := fitStudioAgentView(&view)

	// The read ledger moves only after the answer is built, and without touching UpdatedAt: a
	// read is not an edit, and moving the version would make the pane's next save look stale.
	next := studioSeen{At: studioNow().UTC().Format(time.RFC3339), History: len(history)}
	if n := len(entries); n > 0 {
		next.Seq = entries[n-1].Seq
	}
	if rec.Seen == nil {
		rec.Seen = map[string]studioSeen{}
	}
	rec.Seen[self] = next
	_ = saveStudio(rec)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// studioSinceFor is decision 5's "since last call": the member's edits and presses, rewinds,
// another session's edits, failures, and the pictures that arrived — not this session's own
// edits, which it already knows. The first call reports nothing but the fact that it is first:
// the draft itself is the whole state, and a history of how it got there is not news.
func studioSinceFor(id, self string, entries []DraftLogEntry, history []HistoryItem, seen studioSeen, had bool) studioSince {
	if !had {
		return studioSince{First: true, Items: []studioNews{}}
	}
	var results, log []studioNews
	for _, e := range entries {
		if e.Seq <= seen.Seq {
			continue
		}
		if e.Author == studioAuthorAgent && e.Session == self {
			continue
		}
		switch e.Kind {
		case DraftLogEdit, DraftLogRewind:
			n := studioNews{Kind: e.Kind, At: e.At, By: studioAuthorLabel(e), RewindTo: e.RewindTo}
			for _, c := range e.Changes {
				n.Changes = append(n.Changes, studioChangeText(c))
			}
			log = append(log, n)
		case DraftLogPress:
			log = append(log, studioNews{Kind: "press", At: e.At, By: studioAuthorLabel(e), Version: e.Version})
		case DraftLogPressResult:
			if e.State == pressFailed || e.State == pressLost {
				results = append(results, studioNews{Kind: "failed", At: e.At, Version: e.Version, Error: e.Error})
			}
		}
	}
	for _, j := range jobs.List().Jobs {
		if j.Studio == id && j.State == string(JobFailed) && j.FinishedAt >= seen.At {
			results = append(results, studioNews{Kind: "failed", At: j.FinishedAt, Version: j.Version, Error: j.Error})
		}
	}
	if seen.History < len(history) {
		for _, it := range history[seen.History:] {
			if it.Studio == id {
				results = append(results, studioNews{Kind: "result", At: it.CreatedAt, Version: it.Version, Path: it.Path})
			}
		}
	}
	// Results first: a picture that came back is what the member is most likely to talk about.
	all := slices.Concat(results, log)
	out := studioSince{Items: []studioNews{}}
	if len(all) > studioSinceMax {
		out.More = len(all) - studioSinceMax
		// The newest of each kind survive: results keep their tail, the log its tail.
		keepResults := min(len(results), studioSinceMax)
		keepLog := studioSinceMax - keepResults
		all = slices.Concat(results[len(results)-keepResults:], log[len(log)-keepLog:])
	}
	out.Items = append(out.Items, all...)
	return out
}

func studioAuthorLabel(e DraftLogEntry) string {
	switch e.Author {
	case studioAuthorAgent:
		return "agent " + e.Session
	case studioAuthorRewind:
		return "user (rewind)"
	}
	return "user"
}

func studioChangeText(c DraftChange) string {
	show := func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return "(empty)"
		}
		s := string(raw)
		if len(s) > 120 {
			s = truncateRunes(s, 120) + "…"
		}
		return s
	}
	return c.Field + ": " + show(c.Before) + " → " + show(c.After)
}

func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// studioVersions summarises the newest n presses, oldest first.
func studioVersions(entries []DraftLogEntry, n int) []studioVersionBrief {
	state := map[string]string{}
	for _, e := range entries {
		if e.Kind == DraftLogPressResult {
			state[e.Version] = e.State
		}
	}
	var out []studioVersionBrief
	for _, e := range entries {
		if e.Kind != DraftLogPress {
			continue
		}
		v := studioVersionBrief{Version: e.Version, Mode: e.Mode, By: studioAuthorLabel(e), At: e.At, State: state[e.Version]}
		if v.State == "" {
			v.State = "pending"
		}
		if e.Draft != nil {
			v.Prompt = truncateRunes(e.Draft.Prompt, 160)
		}
		out = append(out, v)
	}
	return out[max(len(out)-n, 0):]
}

// studioModelFactsFor is the chosen model's facts from the provider's own catalogue — the same
// family row the status route draws the pane's card from (decision 7). With no model chosen it
// answers the choices instead.
func studioModelFactsFor(ctx context.Context, d ImageStudioDraft) (*studioModelFacts, []studioModelChoice) {
	var prov Provider
	for _, p := range Providers() {
		if p.ID() == d.Provider {
			prov = p
		}
	}
	if prov == nil {
		return nil, nil
	}
	s, ok := studioOf(ctx, prov)
	if !ok {
		if d.Model == "" {
			return nil, nil
		}
		return &studioModelFacts{Provider: prov.ID(), ID: d.Model}, nil
	}
	if d.Model == "" {
		var out []studioModelChoice
		for _, m := range s.Models {
			out = append(out, studioModelChoice{ID: m.ID, Label: m.Label, Family: m.Family})
		}
		return nil, out
	}
	for _, m := range s.Models {
		if m.ID != d.Model {
			continue
		}
		params := m.Params
		f := &studioModelFacts{
			Provider: prov.ID(), ID: m.ID, Label: m.Label, Description: m.Description, Family: m.Family,
			Knobs: m.Knobs, Sizes: m.Sizes, Defaults: &params, Negative: m.Negative, MaxInputs: m.MaxInputs,
			familyAdvice: familyAdviceFor(m.Family),
		}
		for _, op := range m.Ops {
			f.Ops = append(f.Ops, string(op))
		}
		for _, l := range s.Loras {
			if l.BaseModel == "" || l.BaseModel == m.Family {
				f.Loras = append(f.Loras, studioLoraFact{Name: l.Name, TrainedWords: l.TrainedWords, Weight: l.Weight})
			}
		}
		return f, nil
	}
	return &studioModelFacts{Provider: prov.ID(), ID: d.Model, Missing: true}, nil
}

// fitStudioAgentView marshals the view under studioAgentViewMax, giving up the least useful
// parts first: the model choices and LoRAs beyond a handful, the older versions, the knowledge
// summaries' tails, the spelled-out changes, and last the prompt texts themselves.
func fitStudioAgentView(v *studioAgentView) []byte {
	enc := func() []byte { b, _ := json.Marshal(v); return b }
	b := enc()
	steps := []func(){
		func() {
			if len(v.Models) > 10 {
				v.Models = v.Models[:10]
			}
			if v.Model != nil && len(v.Model.Loras) > 5 {
				v.Model.Loras = v.Model.Loras[:5]
			}
		},
		func() { v.Versions = v.Versions[max(len(v.Versions)-2, 0):] },
		func() {
			for i := range v.Knowledge {
				if len(v.Knowledge[i].Summary) > 300 {
					v.Knowledge[i].Summary, v.Knowledge[i].Truncated = truncateRunes(v.Knowledge[i].Summary, 300), true
				}
			}
		},
		func() {
			for i := range v.Since.Items {
				if len(v.Since.Items[i].Changes) > 1 {
					v.Since.Items[i].Changes = append(v.Since.Items[i].Changes[:1], fmt.Sprintf("… %d more", len(v.Since.Items[i].Changes)-1))
				}
			}
			for i := range v.Versions {
				v.Versions[i].Prompt = truncateRunes(v.Versions[i].Prompt, 40)
			}
			if v.Model != nil {
				v.Model.Description, v.Model.Loras = truncateRunes(v.Model.Description, 200), nil
			}
		},
	}
	for _, step := range steps {
		if len(b) <= studioAgentViewMax {
			return b
		}
		step()
		v.Truncated = true
		b = enc()
	}
	// Last: the draft's own long texts, cut to what still fits.
	for len(b) > studioAgentViewMax {
		over := len(b) - studioAgentViewMax
		switch {
		case len(v.Draft.Prompt) >= len(v.Draft.NegativePrompt) && v.Draft.Prompt != "":
			v.Draft.Prompt = truncateRunes(v.Draft.Prompt, max(len(v.Draft.Prompt)-over-16, 0)) + "…"
		case v.Draft.NegativePrompt != "":
			v.Draft.NegativePrompt = truncateRunes(v.Draft.NegativePrompt, max(len(v.Draft.NegativePrompt)-over-16, 0)) + "…"
		default:
			v.Knowledge, v.Since.Items, v.Versions, v.Models = nil, nil, nil, nil
			return enc()
		}
		b = enc()
	}
	return b
}

// HandleStudioPersona answers GET /imagegen/studios/{id}/persona: the first turn the pane sends
// to a newly bound session (decision 5), in the member's language. It states the role and the
// one habit everything else rests on — read the studio first — and leaves the facts to that call.
func HandleStudioPersona(w http.ResponseWriter, r *http.Request) {
	rec, err := loadStudio(r.PathValue("id"))
	if err != nil {
		writeStudioErr(w, err)
		return
	}
	lang := studioLocale()
	httpx.WriteJSON(w, http.StatusOK, ImageStudioPersona{Prompt: studioPersona(lang, rec.Title), Lang: lang})
}

func studioPersona(lang, title string) string {
	if lang == "en" {
		name := "an image studio"
		if title != "" {
			name = "the image studio \"" + title + "\""
		}
		return "In this session you work with the user on the prompt draft of " + name + " in Agent Fleet.\n" +
			"- On every message from the user, call get_image_studio FIRST, before answering: it has the draft, the locked fields, " +
			"and what changed since your last call (the user's edits, rewinds, new pictures).\n" +
			"- Change the draft only with set_image_draft. Locked fields and the user's fields (model, seed, jobs, count, out_dir, label, mask) " +
			"are not yours; propose a model with suggest_model.\n" +
			"- You do not generate. The user presses the generate button. If trials are allowed, run_image_trial makes one quick picture.\n" +
			"- Read files in the repository only when the user asks you to.\n" +
			"- Do not write files unless the user asks you to.\n" +
			"- When the user judges a result or asks you to remember something, record it with add_image_knowledge.\n" +
			"Answer in English. Start by calling get_image_studio, then greet the user in one or two sentences."
	}
	name := "画像生成スタジオ"
	if title != "" {
		name = "画像生成スタジオ「" + title + "」"
	}
	return "このセッションでは、Agent Fleet の" + name + "で、利用者と一緒にプロンプトの下書きを作ります。\n" +
		"- 利用者の発言を受けたら、答える前にまず get_image_studio を呼んでください。下書き・錠の欄・前回からの変化" +
		"（利用者の編集・巻き戻し・新しい絵）が分かります。\n" +
		"- 下書きの変更は set_image_draft だけで行います。錠の欄と利用者の欄（model・seed・jobs・count・out_dir・label・mask）は" +
		"変えられません。モデルは suggest_model で提案してください。\n" +
		"- 生成はしません。生成ボタンは利用者が押します。試走が許可されていれば run_image_trial で 1 枚だけ試せます。\n" +
		"- リポジトリ内の資料は、頼まれたときに読んでください。\n" +
		"- ファイルは、頼まれない限り書かないでください。\n" +
		"- 利用者が結果の良し悪しを言ったときや「覚えて」と言われたときは add_image_knowledge で記録してください。\n" +
		"日本語で答えてください。まず get_image_studio を呼び、1〜2 文で挨拶してください。"
}
