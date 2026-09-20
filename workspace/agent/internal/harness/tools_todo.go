package harness

// tools_todo.go is the todo_write builtin (docs/log/99 §4.6: "無くても動く"). It
// only keeps Runtime.Todos current; a higher layer reads that back for
// TranscriptData.Tasks (out of this package's scope, per types.go's segment
// split). Like ask_user, it never touches the filesystem or a process, so it is
// not gated by approval.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

var validTodoStatus = map[string]bool{"pending": true, "in_progress": true, "completed": true}

type todoWriteArgs struct {
	Todos []TodoItem `json:"todos"`
}

var todoWriteToolDef = ToolDef{
	Name:        "todo_write",
	Description: "Replace the session's to-do list with the given items.",
	Parameters: json.RawMessage(`{
		"type":"object",
		"properties":{
			"todos":{
				"type":"array",
				"items":{
					"type":"object",
					"properties":{
						"content":{"type":"string"},
						"status":{"type":"string","enum":["pending","in_progress","completed"]}
					},
					"required":["content","status"]
				}
			}
		},
		"required":["todos"]
	}`),
}

func runTodoWrite(_ context.Context, rt *Runtime, argsJSON string) (string, error) {
	var a todoWriteArgs
	if err := decodeArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	for i, item := range a.Todos {
		if strings.TrimSpace(item.Content) == "" {
			return "", fmt.Errorf("todos[%d].content must not be empty", i)
		}
		if !validTodoStatus[item.Status] {
			return "", fmt.Errorf("todos[%d].status %q is not one of pending/in_progress/completed", i, item.Status)
		}
	}
	rt.Todos = a.Todos
	return fmt.Sprintf("recorded %d todo(s)", len(rt.Todos)), nil
}
