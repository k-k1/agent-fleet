package memengine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalid marks a model answer that failed deterministic validation. The
// answer is dropped; callers never retry it with a bigger prompt.
var ErrInvalid = errors.New("memengine: invalid model output")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// decodeStrict decodes one JSON value into v, refusing unknown fields and
// trailing data, so a schema is exactly the fields it names.
func decodeStrict(text string, v any) error {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("not the expected JSON: %v", err)
	}
	if dec.More() {
		return invalid("trailing data after the JSON object")
	}
	return nil
}
