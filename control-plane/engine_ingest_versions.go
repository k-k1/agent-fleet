package main

// Model versions for the repository/model selected in the ingest catalogue.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type engineVersionsReq struct {
	Source   string `json:"source"`
	Ref      string `json:"ref"`
	ModelRef string `json:"model_ref"`
}

type engineVersion struct {
	Ref         string `json:"ref"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	// Bytes is the version's own model file, when the listing carries one. 0 means the listing
	// did not say — which the panel draws as "size unknown" and never as "nothing to download".
	// The authority on a size is still the resolve, which reads the version document itself.
	Bytes int64 `json:"bytes,omitempty"`
}

type engineVersionsAnswer struct {
	Versions []engineVersion `json:"versions"`
	// ModelRef is the model these versions belong to, as the CP resolved it. Sent because the
	// caller may not have had it: a registered row records the VERSION id and nothing else
	// (`civitai:<id>`), and the model id is a different number — the panel needs it to open the
	// ingest form on a chosen version.
	ModelRef string `json:"model_ref,omitempty"`
}

type engineHFVersionDoc struct {
	CreatedAt    string `json:"createdAt"`
	LastModified string `json:"lastModified"`
}

type engineCivitaiVersionsDoc struct {
	ID            int `json:"id"`
	ModelVersions []struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		PublishedAt string `json:"publishedAt"`
		UpdatedAt   string `json:"updatedAt"`
		// The same shape the by-id version document answers (engine_ingest.go), read here for
		// the size alone. 🔴 The list document is documented nowhere and carries fewer fields
		// than the by-id one (engine_ingest_search.go says so, measured); a version whose files
		// it leaves out answers 0, and the panel says so rather than guessing.
		Files []struct {
			Name   string  `json:"name"`
			SizeKB float64 `json:"sizeKB"`
			Type   string  `json:"type"`
		} `json:"files"`
	} `json:"modelVersions"`
}

// versionsIngest returns the selectable revisions before the files in one revision are listed.
// It reads metadata only and uses the same ingest-admin authorization as search/files/resolve.
func (a engineAdminAPI) versionsIngest(w http.ResponseWriter, r *http.Request, _ engineIngestGrant) {
	e := a.reg.get(strings.TrimSpace(r.PathValue("key")))
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no such engine"})
		return
	}
	var b engineVersionsReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	answer, aerr := engineIngestVersions(r.Context(), b, engineIngestKindFor(e))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

func engineIngestVersions(ctx context.Context, req engineVersionsReq, kind string) (engineVersionsAnswer, *apiError) {
	source := strings.TrimSpace(req.Source)
	switch source {
	case "hf":
		return engineHFVersions(ctx, req)
	case "civitai":
		if aerr := engineSourceAllowedForKind(kind, engineIngestSource{Civitai: &engineIngestCivitai{}}); aerr != nil {
			return engineVersionsAnswer{}, aerr
		}
		return engineCivitaiVersions(ctx, req)
	default:
		return engineVersionsAnswer{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"source has to be hf or civitai"}
	}
}

func engineSourceAllowedForKind(kind string, source engineIngestSource) *apiError {
	if kind == "gguf" && source.Civitai != nil {
		return &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"Civitai cannot be used with an LLM engine"}
	}
	return nil
}

func engineHFVersions(ctx context.Context, req engineVersionsReq) (engineVersionsAnswer, *apiError) {
	ref := strings.Trim(strings.TrimSpace(req.Ref), "/")
	modelRef := strings.Trim(strings.TrimSpace(req.ModelRef), "/")
	if modelRef == "" {
		modelRef = ref
	}
	if ref != "" && ref != modelRef {
		return engineVersionsAnswer{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"the Hugging Face ref and model_ref must name the same repository"}
	}
	path, ok := engineHFRepoPath(modelRef)
	if !ok {
		return engineVersionsAnswer{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"Hugging Face needs a repository"}
	}
	values := url.Values{}
	values.Add("expand[]", "createdAt")
	values.Add("expand[]", "lastModified")
	var doc engineHFVersionDoc
	if aerr := engineIngestGetJSON(ctx, engineIngestBase+"/api/models/"+path+"?"+values.Encode(), &doc); aerr != nil {
		return engineVersionsAnswer{}, aerr
	}
	return engineVersionsAnswer{ModelRef: modelRef, Versions: []engineVersion{{
		Ref: "main", Name: "main",
		PublishedAt: strings.TrimSpace(doc.CreatedAt), UpdatedAt: strings.TrimSpace(doc.LastModified),
	}}}, nil
}

func engineHFRepoPath(repo string) (string, bool) {
	parts := strings.Split(repo, "/")
	if len(parts) < 1 || len(parts) > 2 {
		return "", false
	}
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/"), true
}

func engineCivitaiVersions(ctx context.Context, req engineVersionsReq) (engineVersionsAnswer, *apiError) {
	selected := 0
	if ref := strings.TrimSpace(req.Ref); ref != "" {
		parsed, err := strconv.Atoi(ref)
		if err != nil || parsed <= 0 {
			return engineVersionsAnswer{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
				"Civitai ref must be a numeric version id"}
		}
		selected = parsed
	}
	modelID, aerr := engineCivitaiModelOf(ctx, strings.TrimSpace(req.ModelRef), selected)
	if aerr != nil {
		return engineVersionsAnswer{}, aerr
	}
	var doc engineCivitaiVersionsDoc
	if aerr := engineIngestGetJSON(ctx,
		engineCivitaiBase+"/api/v1/models/"+strconv.Itoa(modelID), &doc); aerr != nil {
		return engineVersionsAnswer{}, aerr
	}
	if doc.ID > 0 && doc.ID != modelID {
		return engineVersionsAnswer{}, &apiError{http.StatusBadGateway, errCodeIngestSourceError,
			"Civitai answered for a different model"}
	}
	versions := make([]engineVersion, 0, len(doc.ModelVersions))
	selectedFound := selected == 0
	for _, v := range doc.ModelVersions {
		if v.ID <= 0 {
			continue
		}
		ref := strconv.Itoa(v.ID)
		name := strings.TrimSpace(v.Name)
		if name == "" {
			name = ref
		}
		bytes := int64(0)
		for _, f := range v.Files {
			// The weights, not the config files and the preview images beside them — the same
			// rule the resolve applies when nobody named a file.
			if strings.EqualFold(f.Type, "Model") && f.SizeKB > 0 {
				bytes = int64(f.SizeKB * 1024)
				break
			}
		}
		versions = append(versions, engineVersion{
			Ref: ref, Name: name, Bytes: bytes,
			PublishedAt: strings.TrimSpace(v.PublishedAt), UpdatedAt: strings.TrimSpace(v.UpdatedAt),
		})
		selectedFound = selectedFound || v.ID == selected
	}
	if !selectedFound {
		return engineVersionsAnswer{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"the Civitai ref does not belong to model_ref"}
	}
	return engineVersionsAnswer{Versions: versions, ModelRef: strconv.Itoa(modelID)}, nil
}

// engineCivitaiModelOf settles which MODEL is being listed.
//
// 🔴 A caller that has only a version id is the normal case now, not an edge: a registered row
// records `civitai:<version>` and nothing else (engineSourceURL says why — the model id and its
// slug are a different number and a different string), so "the other versions of this model" can
// only start from the version. The version document knows, and it is the one the ingest already
// reads, so this costs one extra request and no second way of reading a Civitai page.
func engineCivitaiModelOf(ctx context.Context, modelRef string, versionID int) (int, *apiError) {
	if modelRef != "" {
		id, err := strconv.Atoi(modelRef)
		if err != nil || id <= 0 {
			return 0, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
				"Civitai needs a numeric model_ref"}
		}
		return id, nil
	}
	if versionID <= 0 {
		return 0, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"Civitai needs a numeric model_ref"}
	}
	doc, aerr := engineReadCivitai(ctx, versionID)
	if aerr != nil {
		return 0, aerr
	}
	if doc.ModelID <= 0 {
		return 0, &apiError{http.StatusBadGateway, errCodeIngestSourceError,
			"Civitai did not say which model this version belongs to"}
	}
	return doc.ModelID, nil
}
