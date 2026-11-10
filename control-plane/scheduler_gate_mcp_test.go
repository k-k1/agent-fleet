package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The Agent's create_schedule / update_schedule advertise a `gate` argument and forward it to
// /internal/schedules verbatim. This reads the Agent's tool-list golden and holds that shape to
// scheduleGateDTO: every advertised property must decode into it (unknown fields refused) and
// the advertised set must be exactly the DTO's input fields, so renaming one side fails here.
const mcpToolListGoldenPath = "../workspace/agent/internal/mcpx/testdata/mcp_tools/list.json"

func TestMCPScheduleGateArgumentMatchesDTO(t *testing.T) {
	raw, err := os.ReadFile(mcpToolListGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Tools map[string]struct {
			InputSchema struct {
				Properties map[string]struct {
					Properties map[string]struct {
						Type string `json:"type"`
					} `json:"properties"`
				} `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}

	var want []string
	rt := reflect.TypeOf(scheduleGateDTO{})
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		switch name {
		case "fired_at", "last_eval_at", "last_verdict": // output only
		default:
			want = append(want, name)
		}
	}
	sort.Strings(want)

	seen := 0
	for key, tool := range golden.Tools {
		name := strings.SplitN(key, "#", 2)[0]
		if name != "create_schedule" && name != "update_schedule" {
			continue
		}
		seen++
		gate, ok := tool.InputSchema.Properties["gate"]
		if !ok {
			t.Errorf("%s does not advertise gate", key)
			continue
		}
		sample := map[string]any{}
		var got []string
		for prop, def := range gate.Properties {
			got = append(got, prop)
			switch def.Type {
			case "integer":
				sample[prop] = 7
			case "string":
				sample[prop] = "x"
			default:
				t.Errorf("%s gate.%s: unexpected type %q", key, prop, def.Type)
			}
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s gate properties = %v, want the DTO input fields %v", key, got, want)
		}
		b, _ := json.Marshal(sample)
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var dto scheduleGateDTO
		if err := dec.Decode(&dto); err != nil {
			t.Errorf("%s gate does not decode into scheduleGateDTO: %v", key, err)
		}
	}
	if seen == 0 {
		t.Fatal("no create_schedule / update_schedule in the golden: the check ran on nothing")
	}
}
