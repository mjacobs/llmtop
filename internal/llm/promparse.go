package llm

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// PromSample is one sample line from a Prometheus text-format body.
type PromSample struct {
	Name   string
	Labels map[string]string // nil when the line has no label set
	Value  float64
}

// ParsePromText parses a Prometheus text-format body into name→value.
// Label sets are stripped (`foo{bar="baz"} 1` stores under "foo"). Duplicate
// names are summed, which is correct for counters and for request gauges
// split across vLLM engines; callers that need a mean (KV-cache %) should
// use ParsePromSamples.
func ParsePromText(body []byte) map[string]float64 {
	out := make(map[string]float64)
	for _, s := range ParsePromSamples(body) {
		out[s.Name] += s.Value
	}
	return out
}

// ParsePromSamples parses a Prometheus text-format body into its sample
// lines, keeping labels. vLLM labels every series (engine, model_name,
// sleep_state, …) and emits one series per engine under data parallelism, so
// callers aggregate over samples of a name; comment, blank, and malformed
// lines are skipped. A trailing timestamp field is ignored.
func ParsePromSamples(body []byte) []PromSample {
	var out []PromSample
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var s PromSample
		var rest string
		if open := strings.IndexByte(line, '{'); open >= 0 {
			close, labels, ok := parsePromLabels(line, open+1)
			if !ok {
				continue
			}
			s.Name = strings.TrimSpace(line[:open])
			s.Labels = labels
			rest = line[close+1:]
		} else if sp := strings.IndexAny(line, " \t"); sp >= 0 {
			s.Name = line[:sp]
			rest = line[sp:]
		} else {
			continue
		}
		fields := strings.Fields(rest)
		if s.Name == "" || len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		s.Value = v
		out = append(out, s)
	}
	return out
}

// parsePromLabels parses `name="value",…}` starting at line[i] (just after
// the opening brace). It returns the index of the closing brace.
func parsePromLabels(line string, i int) (int, map[string]string, bool) {
	labels := map[string]string{}
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == ',') {
			i++
		}
		if i >= len(line) {
			return 0, nil, false
		}
		if line[i] == '}' {
			return i, labels, true
		}
		eq := strings.IndexByte(line[i:], '=')
		if eq < 0 {
			return 0, nil, false
		}
		name := strings.TrimSpace(line[i : i+eq])
		i += eq + 1
		if i >= len(line) || line[i] != '"' {
			return 0, nil, false
		}
		i++
		var val strings.Builder
		for {
			if i >= len(line) {
				return 0, nil, false
			}
			c := line[i]
			switch c {
			case '\\':
				if i+1 >= len(line) {
					return 0, nil, false
				}
				switch line[i+1] {
				case 'n':
					val.WriteByte('\n')
				default:
					val.WriteByte(line[i+1])
				}
				i += 2
				continue
			case '"':
				i++
			default:
				val.WriteByte(c)
				i++
				continue
			}
			break
		}
		labels[name] = val.String()
	}
}

func promSum(samples []PromSample, name string) (float64, bool) {
	var sum float64
	found := false
	for _, s := range samples {
		if s.Name == name {
			sum += s.Value
			found = true
		}
	}
	return sum, found
}

func promMean(samples []PromSample, name string) (float64, bool) {
	var sum float64
	n := 0
	for _, s := range samples {
		if s.Name == name {
			sum += s.Value
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}
