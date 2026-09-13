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
}

type engineVersionsAnswer struct {
	Versions []engineVersion `json:"versions"`
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
	versions, aerr := engineIngestVersions(r.Context(), b, engineIngestKindFor(e))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, engineVersionsAnswer{Versions: versions})
}

func engineIngestVersions(ctx context.Context, req engineVersionsReq, kind string) ([]engineVersion, *apiError) {
	source := strings.TrimSpace(req.Source)
	switch source {
	case "hf":
		return engineHFVersions(ctx, req)
	case "civitai":
		if aerr := engineSourceAllowedForKind(kind, engineIngestSource{Civitai: &engineIngestCivitai{}}); aerr != nil {
			return nil, aerr
		}
		return engineCivitaiVersions(ctx, req)
	default:
		return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
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

func engineHFVersions(ctx context.Context, req engineVersionsReq) ([]engineVersion, *apiError) {
	ref := strings.Trim(strings.TrimSpace(req.Ref), "/")
	modelRef := strings.Trim(strings.TrimSpace(req.ModelRef), "/")
	if modelRef == "" {
		modelRef = ref
	}
	if ref != "" && ref != modelRef {
		return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"the Hugging Face ref and model_ref must name the same repository"}
	}
	path, ok := engineHFRepoPath(modelRef)
	if !ok {
		return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"Hugging Face needs a repository"}
	}
	values := url.Values{}
	values.Add("expand[]", "createdAt")
	values.Add("expand[]", "lastModified")
	var doc engineHFVersionDoc
	if aerr := engineIngestGetJSON(ctx, engineIngestBase+"/api/models/"+path+"?"+values.Encode(), &doc); aerr != nil {
		return nil, aerr
	}
	return []engineVersion{{
		Ref: "main", Name: "main",
		PublishedAt: strings.TrimSpace(doc.CreatedAt), UpdatedAt: strings.TrimSpace(doc.LastModified),
	}}, nil
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

func engineCivitaiVersions(ctx context.Context, req engineVersionsReq) ([]engineVersion, *apiError) {
	modelID, err := strconv.Atoi(strings.TrimSpace(req.ModelRef))
	if err != nil || modelID <= 0 {
		return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"Civitai needs a numeric model_ref"}
	}
	selected := 0
	if ref := strings.TrimSpace(req.Ref); ref != "" {
		selected, err = strconv.Atoi(ref)
		if err != nil || selected <= 0 {
			return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
				"Civitai ref must be a numeric version id"}
		}
	}
	var doc engineCivitaiVersionsDoc
	if aerr := engineIngestGetJSON(ctx,
		engineCivitaiBase+"/api/v1/models/"+strconv.Itoa(modelID), &doc); aerr != nil {
		return nil, aerr
	}
	if doc.ID > 0 && doc.ID != modelID {
		return nil, &apiError{http.StatusBadGateway, errCodeIngestSourceError,
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
		versions = append(versions, engineVersion{
			Ref: ref, Name: name,
			PublishedAt: strings.TrimSpace(v.PublishedAt), UpdatedAt: strings.TrimSpace(v.UpdatedAt),
		})
		selectedFound = selectedFound || v.ID == selected
	}
	if !selectedFound {
		return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"the Civitai ref does not belong to model_ref"}
	}
	return versions, nil
}
