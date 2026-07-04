package llm

import "testing"

const promFixture = `# HELP llamacpp:prompt_tokens_total Number of prompt tokens processed.
# TYPE llamacpp:prompt_tokens_total counter
llamacpp:prompt_tokens_total 16121
# HELP llamacpp:tokens_predicted_total Number of generation tokens processed.
# TYPE llamacpp:tokens_predicted_total counter
llamacpp:tokens_predicted_total 247195
# HELP llamacpp:prompt_tokens_seconds Average prompt throughput in tokens/s.
# TYPE llamacpp:prompt_tokens_seconds gauge
llamacpp:prompt_tokens_seconds 2754.78
# HELP llamacpp:predicted_tokens_seconds Average generation throughput in tokens/s.
# TYPE llamacpp:predicted_tokens_seconds gauge
llamacpp:predicted_tokens_seconds 367.57
# HELP llamacpp:requests_processing Number of requests processing.
# TYPE llamacpp:requests_processing gauge
llamacpp:requests_processing 0
# HELP llamacpp:requests_deferred Number of requests deferred.
# TYPE llamacpp:requests_deferred gauge
llamacpp:requests_deferred 0

garbage line without value
llamacpp:broken abc
`

func TestParsePromText(t *testing.T) {
	m := ParsePromText([]byte(promFixture))
	want := map[string]float64{
		"llamacpp:prompt_tokens_total":      16121,
		"llamacpp:tokens_predicted_total":   247195,
		"llamacpp:prompt_tokens_seconds":    2754.78,
		"llamacpp:predicted_tokens_seconds": 367.57,
		"llamacpp:requests_processing":      0,
		"llamacpp:requests_deferred":        0,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("m[%q] = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["llamacpp:broken"]; ok {
		t.Error("malformed value line must be skipped")
	}
	if _, ok := m["garbage"]; ok {
		t.Error("garbage line must be skipped")
	}
}
