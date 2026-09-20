package fleetgraph

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
)

// EraseLineage removes every lineage.jsonl row naming this lane (ADR 0096 decision 6):
// called ONLY from an explicit person-initiated delete (DELETE /sessions/{name}?reclaim=1),
// never from the stopped-session TTL prune — that one leaves lineage alone by design, so a
// pruned parent's branch line still has a root to draw from.
//
// This is the one place this package does a read-modify-write instead of an append: a
// delete is rare and person-triggered, not a concurrent hot path, and rewriting a jsonl a
// person just asked to have erased is the only way to actually erase it. Activity lines
// naming this id are deliberately left alone (they rotate out within 30 days on their own,
// and rewriting 30 append-only files per delete would be the read-modify-write habit this
// package otherwise refuses — decision 6, docs/log/101 §101.3).
func EraseLineage(name string) error {
	if name == "" {
		return nil
	}
	path := lineagePath()
	lineageMu.Lock()
	defer lineageMu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var kept bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		raw := sc.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var probe struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &probe) == nil && probe.Name == name {
			continue // erased
		}
		kept.Write(raw)
		kept.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, kept.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
