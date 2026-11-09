package memengine

// Usage is the token usage a model command reported for one call. Fields the
// command did not report stay zero.
type Usage struct {
	InputTokens       int `json:"input_tokens"`
	CacheCreateTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens   int `json:"cache_read_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

// TotalInput counts every input token the call consumed, cached or not. The
// cache split is an accounting detail of the vendor; the monthly budget in
// ADR 0130 is in tokens the model read.
func (u Usage) TotalInput() int {
	return u.InputTokens + u.CacheCreateTokens + u.CacheReadTokens
}

// Add returns the field-wise sum.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:       u.InputTokens + o.InputTokens,
		CacheCreateTokens: u.CacheCreateTokens + o.CacheCreateTokens,
		CacheReadTokens:   u.CacheReadTokens + o.CacheReadTokens,
		OutputTokens:      u.OutputTokens + o.OutputTokens,
	}
}
