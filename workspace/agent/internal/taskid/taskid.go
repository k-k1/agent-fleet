// Package taskid reads the ECS task this Agent runs in.
package taskid

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// The ECS task this Agent runs in, reported on /healthz as task_id so the Control Plane can
// tell which task answered a probe that went through the shared Service Connect alias
// (ADR 0121, 2026-11-12 note on #3993).
//
// Only the task id (the last segment of the task ARN) is exposed, never the ARN: the ARN
// carries the AWS account id, and /healthz is the one route that answers without the bearer
// token to anything that can reach port 7700 (the VPC, through Service Connect). A task id
// alone names no account and grants nothing.

const (
	taskMetadataTimeout  = 2 * time.Second
	taskMetadataAttempts = 3
	taskMetadataRetry    = time.Second
	taskMetadataMaxBody  = 1 << 20
	taskIDMaxLen         = 128
)

// value holds the id once it is known; empty means unknown (outside ECS, or the
// metadata endpoint has not answered), and /healthz then omits the field.
var value atomic.Value

// Current returns the task id, or "" when it is not known.
func Current() string {
	s, _ := value.Load().(string)
	return s
}

// Start reads the task id in the background. It never blocks start-up and
// never fails it: a lookup that gives up leaves the field absent, which the CP reads as
// "identity not proven".
func Start() {
	base := os.Getenv("ECS_CONTAINER_METADATA_URI_V4")
	if base == "" {
		return // not on ECS
	}
	go func() {
		for i := 0; i < taskMetadataAttempts; i++ {
			if i > 0 {
				time.Sleep(taskMetadataRetry)
			}
			if id := fetchTaskID(context.Background(), base); id != "" {
				value.Store(id)
				return
			}
		}
		log.Printf("task identity: ECS task metadata gave no task id; /healthz omits it")
	}()
}

// fetchTaskID asks <base>/task for the TaskARN and returns its id segment, or "" on any
// failure or a value that does not look like an id.
func fetchTaskID(ctx context.Context, base string) string {
	ctx, cancel := context.WithTimeout(ctx, taskMetadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/task", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var meta struct {
		TaskARN string `json:"TaskARN"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, taskMetadataMaxBody)).Decode(&meta); err != nil {
		return ""
	}
	return taskIDFromARN(meta.TaskARN)
}

// taskIDFromARN returns the segment after the last "/" of arn:aws:ecs:<region>:<acct>:task/<cluster>/<id>,
// or "" when it is not made of the characters an id has.
func taskIDFromARN(arn string) string {
	i := strings.LastIndex(arn, "/")
	if i < 0 {
		return ""
	}
	id := arn[i+1:]
	if id == "" || len(id) > taskIDMaxLen {
		return ""
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return ""
		}
	}
	return id
}

// SetForTest replaces the id; tests in other packages use it to drive /healthz.
func SetForTest(id string) { value.Store(id) }
