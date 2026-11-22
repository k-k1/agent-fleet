package engine

import (
	"encoding/json"
	"testing"
)

// The far side's token exchange and catalogue are another deployment's wire. These vectors were
// taken from the types before they moved into this package, so a tag edited during a move fails
// here rather than as a 401 or an empty launch menu in production.
func TestRemoteWireVectors(t *testing.T) {
	b, err := json.Marshal(remoteTokenReq{Session: "s1", Key: "image"})
	if err != nil || string(b) != `{"session":"s1","key":"image"}` {
		t.Fatalf("token request = %s, %v", b, err)
	}
	var tok remoteTokenResp
	if err := json.Unmarshal([]byte(`{"token":"t","expires_at":"2030-01-02T03:04:05Z","base_url":"/engine/image/v1"}`), &tok); err != nil ||
		tok.Token != "t" || tok.ExpiresAt != "2030-01-02T03:04:05Z" || tok.BaseURL != "/engine/image/v1" {
		t.Fatalf("token answer = %+v, %v", tok, err)
	}
	var cat remoteCatalog
	raw := `{"engines":[{"key":"image","api":"images","provider":"comfy","base_url":"/b","negative_always":"x",
	  "model_rows":[{"id":"m","context_tokens":1,"max_output_tokens":2,"description":"d","sizes":["1x1"],
	    "base_model":"sdxl","negative":"n","selected":true,"default":true,"warm":true,
	    "files":[{"flag":"f","s3_key":"k/a.safetensors"}],
	    "trained_words":["w"],"license_name":"l","license_url":"u"}],
	  "loras":[{"id":"l1"}]}]}`
	if err := json.Unmarshal([]byte(raw), &cat); err != nil || len(cat.Engines) != 1 {
		t.Fatalf("catalogue = %+v, %v", cat, err)
	}
	row := cat.Engines[0]
	m := row.Models[0]
	if row.Key != "image" || row.API != "images" || row.Provider != "comfy" || row.NegativeAlways != "x" ||
		len(row.Loras) != 1 || row.Loras[0].ID != "l1" ||
		m.ID != "m" || m.ContextTokens != 1 || m.MaxOutputTokens != 2 || !m.Selected || !m.Default || !m.Warm ||
		m.Files[0].S3Key != "k/a.safetensors" || m.Files[0].Flag != "f" ||
		m.TrainedWords[0] != "w" || m.LicenseName != "l" || m.LicenseURL != "u" || m.BaseModel != "sdxl" || m.Negative != "n" {
		t.Fatalf("decoded row = %+v", row)
	}
}
