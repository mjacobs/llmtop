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

func TestParsePromTextLabeled(t *testing.T) {
	body := []byte(`# HELP vllm:prompt_tokens_total Number of prefill tokens processed.
vllm:prompt_tokens_total{engine="0",model_name="qwen"} 10.0
vllm:prompt_tokens_total{engine="1",model_name="qwen"} 15.0
vllm:kv_cache_usage_perc{engine="0",model_name="qwen"} 0.25
vllm:kv_cache_usage_perc{engine="1",model_name="qwen"} 0.75
vllm:engine_sleep_state{engine="0",sleep_state="awake"} 1.0
`)
	m := ParsePromText(body)
	if m["vllm:prompt_tokens_total"] != 25 {
		t.Errorf("summed prompt tokens = %v, want 25", m["vllm:prompt_tokens_total"])
	}
	samples := ParsePromSamples(body)
	if v, ok := promMean(samples, "vllm:kv_cache_usage_perc"); !ok || v != 0.5 {
		t.Errorf("mean kv = %v ok=%v, want 0.5", v, ok)
	}
	found := false
	for _, s := range samples {
		if s.Name == "vllm:engine_sleep_state" && s.Labels["sleep_state"] == "awake" && s.Value == 1 {
			found = true
		}
	}
	if !found {
		t.Error("sleep_state label must be preserved")
	}
}
