// Package memengine is the model-facing core of cultivated memory (ADR 0130,
// decisions 6 and 8): two bounded model calls with fixed JSON schemas, the
// deterministic validation every answer must pass, and the model as a plug-in.
//
// The module is deliberately separate from the rest of the repository and
// imports nothing from it (independence_test.go fails on any import of an
// agent-fleet package). It takes no SDK and no API key: a model is a one-shot
// command that reads the prompt on stdin and writes JSON on stdout, configured
// by argv alone.
//
// An answer that fails validation is dropped, never retried with a bigger
// prompt: a retry would hide the failure rate the offline evaluation measures.
package memengine
