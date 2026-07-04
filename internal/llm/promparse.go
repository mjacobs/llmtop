package llm

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// ParsePromText parses a Prometheus text-format body into name→value.
// llama.cpp emits no labels, so "name value" lines are all we handle;
// comment, blank, and malformed lines are skipped.
func ParsePromText(body []byte) map[string]float64 {
	out := make(map[string]float64)
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		out[fields[0]] = v
	}
	return out
}
