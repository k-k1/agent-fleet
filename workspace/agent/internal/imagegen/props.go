package imagegen

// What a picture was made from, recovered after the fact (ADR 0081 decision 3).
//
// Two sources, in this order, and the answer says which one it came from:
//
//   - the sidecar `<name>.json` this Agent writes next to every picture the job queue makes.
//     Format-independent (it works for webp and jpeg, and needs no PNG rewrite), invisible to
//     the gallery, and the only one of the two that holds the label, the job and the warnings.
//   - the PNG's own `prompt` text chunk, which ComfyUI's SaveImage embeds unless the box runs
//     with --disable-metadata. It is the API graph rather than the request, and it is the ONLY
//     way to read the pictures made before the sidecar existed — measured on a live deployment,
//     251 files walked chunk by chunk: every comfy-route PNG carries it (1,550-1,716 bytes) and
//     no vendor-route one does.
//
// Neither is a guess: a file with no sidecar and no chunk answers `source: "none"` rather than a
// half-filled record. The vendor routes (codex, agy) are that case by construction, and saying
// so is the point — blanks would read as "the seed was 0".
//
// 🔴 Never read from the thumbnail. `fs/download?thumb=` is a JPEG (or PNG) re-encode and
// neither encoder writes a text chunk; a source under thumbMinSourceBytes is served as the
// original instead, so a chunk that does come back from that route is an accident of the
// small-file exception and never a contract.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// ImageProps is one picture's resolved request — what the sidecar holds on disk, and what the
// props route answers with whichever source it came from. One record, one reader: the shared
// lightbox's properties bar is the single surface the gallery, the mirror's file card and the
// generation pane all meet at, so a second shape here would be a second thing to keep in step.
type ImageProps struct {
	// Source is "sidecar" | "png" | "none" and rides only on the ROUTE's answer — the file on
	// disk does not need to tell the reader that it is itself.
	Source string `json:"source,omitempty"`
	// Provider and Model name what made the picture; Family is the workflow template, which is
	// what decides which of the knobs below meant anything at all.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Family   string `json:"family,omitempty"`
	Op       string `json:"op,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	// Negative is the prompt AS COMPOSED — the catalogue row's, the caller's and the
	// deployment's, joined the way the sampler saw them (comfyNegativeFor). Showing the
	// caller's own half alone would misreport what was excluded.
	Negative string `json:"negative,omitempty"`
	// Seed is this picture's own, not the request's base: a batch of four is four seeds.
	Seed *int64 `json:"seed,omitempty"`
	Size string `json:"size,omitempty"`
	// Params is what actually ran, after recipe ← catalogue row ← request.
	Params *EngineParams `json:"params,omitempty"`
	// FullSteps is what the BATCH would run at, on a trial whose steps were reduced (ADR 0081
	// decision 11). It is on the record rather than only in the form so that the picture says
	// both what made it and what its keeper would be made with.
	FullSteps int       `json:"full_steps,omitempty"`
	Loras     []LoraRef `json:"loras,omitempty"`
	// Strength is how much of the input picture an edit changed, on the ops that read it.
	Strength *float64 `json:"strength,omitempty"`
	// Inputs are the reference pictures an edit or inpaint started from, as the paths they were
	// named by.
	Inputs []string `json:"inputs,omitempty"`
	Mask   string   `json:"mask,omitempty"`
	// Job, Group and Label are the queue's own (ADR 0081 decision 2), absent on a picture made
	// through the blocking MCP route and on anything read out of a PNG chunk.
	Job   string `json:"job,omitempty"`
	Group string `json:"group,omitempty"`
	Label string `json:"label,omitempty"`
	Trial bool   `json:"trial,omitempty"`
	// ElapsedMS is what this picture cost in wall-clock time, which is the number the estimate
	// on the next one is built from.
	ElapsedMS int64    `json:"elapsed_ms,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	// Agent is the build that wrote the graph. The templates change between releases, and the
	// sidecar outlives the binary — "which Agent made this" is provenance nothing else carries.
	Agent     string `json:"agent,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// sidecarExt is the suffix a picture's record is filed under. It is appended to the WHOLE file
// name rather than replacing the extension, so `image-1-1.png.json` cannot collide with a
// `image-1-1.json` somebody else wrote, and the picture's own name is recoverable from it.
const sidecarExt = ".json"

func sidecarPathFor(imagePath string) string { return imagePath + sidecarExt }

// writeSidecar puts the record next to the picture. A failure is returned rather than logged:
// the record is what the properties bar, "open in image generation" and any later X/Y grid all
// read, so a picture without one is a picture whose settings are lost.
func writeSidecar(imagePath string, props ImageProps) error {
	b, err := json.MarshalIndent(props, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sidecarPathFor(imagePath), append(b, '\n'), 0o600)
}

func readSidecar(imagePath string) (ImageProps, bool) {
	b, err := os.ReadFile(sidecarPathFor(imagePath))
	if err != nil {
		return ImageProps{}, false
	}
	var out ImageProps
	if json.Unmarshal(b, &out) != nil {
		return ImageProps{}, false
	}
	return out, true
}

// --- the route ------------------------------------------------------------------------------

// BrowsePath resolves a browse-root-relative path for READING, and BrowseWritablePath the same
// for a folder that is about to be written into. Both are filled in by the Agent's main package
// at startup, which is where the browse root, the denylist and the symlink re-check live
// (fs.go); this package cannot import main, and re-implementing the gate here would be a second
// copy of a security decision.
//
// nil means no path may be named at all: this package refuses rather than falling back to a
// weaker check of its own.
var (
	BrowsePath         func(p string) (full, rel string, ok bool)
	BrowseWritablePath func(p string) (full, rel string, ok bool)
)

// HandleProps answers GET /imagegen/props?path=<browse-root-relative>.
//
// Cached by the file's own mtime, with Last-Modified and a 304 on If-Modified-Since, for the
// same reason the thumbnail route is: the lightbox asks for this every time it opens, and the
// answer for a given picture never changes unless the picture does.
func HandleProps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	if strings.TrimSpace(q) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "path is required")
		return
	}
	if BrowsePath == nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "no_browse_root", "this Agent cannot resolve browse paths")
		return
	}
	full, _, ok := BrowsePath(q)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_path", "invalid path")
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such picture: "+q)
		return
	}
	mod := info.ModTime().UTC().Truncate(time.Second)
	if since, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !mod.After(since) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Last-Modified", mod.Format(http.TimeFormat))
	httpx.WriteJSON(w, http.StatusOK, readImageProps(full))
}

// readImageProps is the two-source rule of decision 3, in its stated order.
func readImageProps(full string) ImageProps {
	if props, ok := readSidecar(full); ok {
		props.Source = "sidecar"
		return props
	}
	if props, ok := readPNGProps(full); ok {
		props.Source = "png"
		return props
	}
	return ImageProps{Source: "none"}
}

// --- the PNG chunk --------------------------------------------------------------------------

// pngMaxTextBytes bounds one text chunk. The measured graphs are 1,550-1,716 bytes; a megabyte
// is room for a graph an order of magnitude larger and still a refusal rather than an
// out-of-memory on a file that claims a 4 GB chunk.
const pngMaxTextBytes = 1 << 20

// readPNGProps reads the `prompt` text chunk ComfyUI's SaveImage embeds and maps the API graph
// back into the sidecar's shape.
//
// It stops at the first IDAT and decodes no pixels. That is not an optimisation: the metadata
// chunks are all before the image data by the format's own rule, and reading further would mean
// pulling a 3 MB picture through memory for four numbers, for every card a folder shows.
func readPNGProps(path string) (ImageProps, bool) {
	graph, ok := readPNGTextChunk(path, "prompt")
	if !ok {
		return ImageProps{}, false
	}
	var nodes map[string]struct {
		ClassType string         `json:"class_type"`
		Inputs    map[string]any `json:"inputs"`
	}
	if json.Unmarshal([]byte(graph), &nodes) != nil || len(nodes) == 0 {
		return ImageProps{}, false
	}
	g := comfyReadGraph{}
	for id, n := range nodes {
		g[id] = comfyReadNode{Class: n.ClassType, Inputs: n.Inputs}
	}
	props := g.props()
	props.Provider = ProviderComfy
	if seed := props.Seed; seed != nil {
		// A batch PNG carries the graph's BASE seed with batch_size next to it, so the picture's
		// own seed is base + its index — which lives only in the `-<n>` the store put in the file
		// name. Measured on this container's own generated/ (ADR 0081 decision 3).
		if n := imageIndexInName(filepath.Base(path)); n > 1 {
			s := *seed + int64(n-1)
			props.Seed = &s
		}
	}
	return props, true
}

// imageIndexInName reads the 1-based batch index out of `image-<unixnano>-<n>.<ext>`. 0 when the
// name is not one this store wrote, in which case nothing may be added to the base seed.
func imageIndexInName(name string) int {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	i := strings.LastIndex(base, "-")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(base[i+1:])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// readPNGTextChunk walks the chunk list for a tEXt or an uncompressed iTXt with this keyword.
// zTXt and a compressed iTXt are deliberately not decoded: ComfyUI writes neither (PngInfo's
// add_text produces tEXt for latin-1 text), and a zlib reader here would be code that has never
// met a real input.
func readPNGTextChunk(path, keyword string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	var sig [8]byte
	if _, err := io.ReadFull(f, sig[:]); err != nil || string(sig[:]) != "\x89PNG\r\n\x1a\n" {
		return "", false
	}
	var head [8]byte
	for {
		if _, err := io.ReadFull(f, head[:]); err != nil {
			return "", false
		}
		size := binary.BigEndian.Uint32(head[:4])
		kind := string(head[4:8])
		if kind == "IDAT" || kind == "IEND" {
			return "", false // the pixels start here; every text chunk is behind us
		}
		if (kind != "tEXt" && kind != "iTXt") || size > pngMaxTextBytes {
			// +4 for the CRC, which is skipped along with the data.
			if _, err := f.Seek(int64(size)+4, io.SeekCurrent); err != nil {
				return "", false
			}
			continue
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(f, data); err != nil {
			return "", false
		}
		if _, err := f.Seek(4, io.SeekCurrent); err != nil {
			return "", false
		}
		if text, ok := pngTextValue(kind, data, keyword); ok {
			return text, true
		}
	}
}

// pngTextValue splits one text chunk's payload. tEXt is `keyword\0text`; iTXt adds a compression
// flag, a compression method, a language tag and a translated keyword between the two, and is
// read only when the flag says the text is not compressed.
func pngTextValue(kind string, data []byte, keyword string) (string, bool) {
	i := bytes.IndexByte(data, 0)
	if i < 0 || string(data[:i]) != keyword {
		return "", false
	}
	rest := data[i+1:]
	if kind == "tEXt" {
		return string(rest), true
	}
	if len(rest) < 2 || rest[0] != 0 {
		return "", false // compressed: not something this reader claims to handle
	}
	rest = rest[2:]
	for n := 0; n < 2; n++ { // the language tag and the translated keyword, both NUL-terminated
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			return "", false
		}
		rest = rest[j+1:]
	}
	return string(rest), true
}

// --- mapping the graph back ------------------------------------------------------------------

type comfyReadNode struct {
	Class  string
	Inputs map[string]any
}

type comfyReadGraph map[string]comfyReadNode

// node answers by the Agent's OWN node id first and by class type second (ADR 0081 decision 3).
// The ids are words this package chose (`ckpt`, `pos`, `neg`, `ks`, `lat`, `save`) and every
// graph this Agent ever sent carries them, so they are exact; the class-type walk is the
// fallback for a graph somebody else's ComfyUI wrote, where an id is a number with no meaning.
func (g comfyReadGraph) node(id string, classes ...string) (string, comfyReadNode, bool) {
	if n, ok := g[id]; ok {
		for _, c := range classes {
			if n.Class == c {
				return id, n, true
			}
		}
		if len(classes) == 0 {
			return id, n, true
		}
	}
	// Deterministic: the ids are walked in order, because a map range would answer differently
	// on two reads of the same file.
	for _, key := range sortedKeys(g) {
		for _, c := range classes {
			if g[key].Class == c {
				return key, g[key], true
			}
		}
	}
	return "", comfyReadNode{}, false
}

func sortedKeys(g comfyReadGraph) []string {
	out := make([]string, 0, len(g))
	for k := range g {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ { // insertion sort: these graphs are a dozen nodes
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// props reads one API graph into the record. Every field is optional: this is a graph that may
// have been written by another ComfyUI, and a missing node means "not known", never zero.
func (g comfyReadGraph) props() ImageProps {
	var out ImageProps
	params := EngineParams{}

	// The sampler, in the two shapes the five templates use: KSampler carries everything, while
	// the custom-sampler path spreads it over KSamplerSelect, a scheduler node and RandomNoise.
	_, sampler, hasSampler := g.node("ks", "KSampler")
	if hasSampler {
		out.Seed = intPtrOf(sampler.Inputs["seed"])
		params.Steps = intOf(sampler.Inputs["steps"])
		params.CFG = floatOf(sampler.Inputs["cfg"])
		params.Sampler = stringOf(sampler.Inputs["sampler_name"])
		params.Scheduler = stringOf(sampler.Inputs["scheduler"])
	} else {
		if _, n, ok := g.node("noise", "RandomNoise"); ok {
			out.Seed = intPtrOf(n.Inputs["noise_seed"])
		}
		if _, n, ok := g.node("sampler", "KSamplerSelect"); ok {
			params.Sampler = stringOf(n.Inputs["sampler_name"])
		}
		if _, n, ok := g.node("scheduler", "BasicScheduler"); ok {
			params.Steps = intOf(n.Inputs["steps"])
			params.Scheduler = stringOf(n.Inputs["scheduler"])
		} else if _, n, ok := g.node("sigmas", "Flux2Scheduler"); ok {
			params.Steps = intOf(n.Inputs["steps"])
		}
		_, sampler, hasSampler = g.node("sca", "SamplerCustomAdvanced")
	}
	if params != (EngineParams{}) {
		p := params
		out.Params = &p
	}

	// The positive prompt is the CLIPTextEncode WIRED to the sampler, not "the first one in the
	// graph": klein's second text encode is a zeroed-out copy of the same conditioning, and
	// flux1's runs through FluxGuidance on the way, so a graph read by position would report the
	// negative prompt as the positive one on two of the five families.
	if hasSampler {
		out.Prompt = g.textBehind(sampler, "positive", "conditioning", "guider")
		out.Negative = g.textBehind(sampler, "negative")
	}

	if _, n, ok := g.node("ckpt", "CheckpointLoaderSimple"); ok {
		out.Model = stringOf(n.Inputs["ckpt_name"])
	} else if _, n, ok := g.node("unet", "UNETLoader"); ok {
		out.Model = stringOf(n.Inputs["unet_name"])
	}
	if _, n, ok := g.node("lat", "EmptyLatentImage", "EmptySD3LatentImage", "EmptyFlux2LatentImage"); ok {
		if w, h := intOf(n.Inputs["width"]), intOf(n.Inputs["height"]); w > 0 && h > 0 {
			out.Size = fmt.Sprintf("%dx%d", w, h)
		}
	}
	if _, n, ok := g.node("save", "SaveImage"); ok {
		out.Family = comfyFamilyFromPrefix(stringOf(n.Inputs["filename_prefix"]))
	}
	for _, id := range sortedKeys(g) {
		if g[id].Class != "LoraLoader" {
			continue
		}
		out.Loras = append(out.Loras, LoraRef{
			Name:   stringOf(g[id].Inputs["lora_name"]),
			Weight: floatOf(g[id].Inputs["strength_model"]),
		})
	}
	if _, n, ok := g.node("img", "LoadImage"); ok {
		out.Inputs = []string{stringOf(n.Inputs["image"])}
		out.Op = string(OpEdit)
	}
	if _, n, ok := g.node("mask", "LoadImageMask"); ok {
		out.Mask = stringOf(n.Inputs["image"])
		out.Op = string(OpInpaint)
	}
	if out.Op == "" {
		out.Op = string(OpGenerate)
	}
	return out
}

// textBehind follows one of the sampler's conditioning inputs back to the CLIPTextEncode that
// feeds it, through the guidance and guider nodes that sit in between on the FLUX families.
// Bounded, because a graph from elsewhere may be a cycle and this is parsing somebody else's
// file.
func (g comfyReadGraph) textBehind(from comfyReadNode, inputs ...string) string {
	for _, in := range inputs {
		id, ok := linkTarget(from.Inputs[in])
		for hop := 0; ok && hop < 6; hop++ {
			n, exists := g[id]
			if !exists {
				break
			}
			if n.Class == "CLIPTextEncode" {
				return stringOf(n.Inputs["text"])
			}
			// The one hop that matters on each family: FluxGuidance's `conditioning`,
			// BasicGuider's / CFGGuider's `conditioning` or `positive`.
			next := ""
			for _, key := range []string{"conditioning", "positive", "negative"} {
				if t, ok2 := linkTarget(n.Inputs[key]); ok2 {
					next = t
					break
				}
			}
			if next == "" {
				break
			}
			id = next
		}
	}
	return ""
}

// linkTarget reads a graph edge `[node id, slot]`.
func linkTarget(v any) (string, bool) {
	pair, ok := v.([]any)
	if !ok || len(pair) == 0 {
		return "", false
	}
	id, ok := pair[0].(string)
	return id, ok && id != ""
}

// comfyFamilyFromPrefix reads the family off SaveImage's filename_prefix, which every template
// sets to `af-<family>` — so the family is READ rather than inferred from the node shapes.
// Empty for a prefix that is not one of the five, which is what a graph this Agent did not write
// looks like.
func comfyFamilyFromPrefix(prefix string) string {
	name := strings.TrimPrefix(strings.TrimSpace(prefix), "af-")
	if name == prefix {
		return ""
	}
	for _, f := range comfyFamilies {
		if string(f) == name || comfyFamilyPrefixName(f) == name {
			return string(f)
		}
	}
	return ""
}

// comfyFamilyPrefixName is the short word a template puts in its filename_prefix, which is the
// family's own spelling except for flux2-klein, whose prefix is `af-klein`.
func comfyFamilyPrefixName(f comfyFamily) string {
	if f == ComfyFamilyFlux2Klein {
		return "klein"
	}
	return string(f)
}

func stringOf(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func floatOf(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func intOf(v any) int { return int(floatOf(v)) }

// intPtrOf answers nil for "the graph does not carry this", which for a seed is a different fact
// from seed 0 — and 0 is a perfectly good seed.
func intPtrOf(v any) *int64 {
	if v == nil {
		return nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	n := int64(f)
	return &n
}

// errNoBrowseRoot is the refusal when the Agent's path gate was never installed. It is a
// separate error rather than a bare false so that "this path is outside the browse root" and
// "this Agent cannot check" do not reach the caller as the same sentence.
var errNoBrowseRoot = errors.New("this Agent cannot resolve browse paths")

// resolveOutDir turns a caller's browse-root-relative folder into an absolute one, through the
// SAME gate the upload route uses (fs.go's safeWritableBrowsePath: inside the browse root, not
// under fsDeny, never an absolute path). Created on first use, because a folder named for work
// that has not happened yet cannot be expected to exist.
func resolveOutDir(outDir string) (full, rel string, err error) {
	outDir = strings.TrimSpace(outDir)
	if outDir == "" {
		return "", "", nil
	}
	if BrowseWritablePath == nil {
		return "", "", errNoBrowseRoot
	}
	full, rel, ok := BrowseWritablePath(outDir)
	if !ok {
		return "", "", fmt.Errorf("out_dir %q is not a folder this workspace may write to", outDir)
	}
	if err := os.MkdirAll(full, 0o700); err != nil {
		return "", "", err
	}
	return full, rel, nil
}
