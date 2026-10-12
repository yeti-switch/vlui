package vl

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// sample is one line of the Prometheus text exposition format.
type sample struct {
	name   string
	labels map[string]string
	value  float64
}

// maxMetricsLine is the longest line parseSamples reads. Every gauge it wants
// is far shorter; a longer line is somebody else's and is skipped whole.
const maxMetricsLine = 64 << 10

// parseSamples reads the text exposition format and keeps the samples keep
// accepts by name. It is a small parser rather than prometheus/common's expfmt:
// all the status page reads is a dozen gauges, which needs no types, no
// histograms and no extra direct dependency.
//
// A line it cannot read — malformed, or longer than maxMetricsLine — is skipped
// rather than failing the whole page: the page is about a handful of series,
// and one exotic line elsewhere in the output should not cost it all of them.
func parseSamples(r io.Reader, keep func(name string) bool) ([]sample, error) {
	var out []sample
	br := bufio.NewReaderSize(r, maxMetricsLine)
	for {
		raw, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			if err == io.EOF {
				return out, nil
			}
			if err != nil {
				return out, err
			}
			continue
		}

		if s, ok := parseLine(string(raw), keep); ok {
			out = append(out, s)
		}

		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

func parseLine(line string, keep func(name string) bool) (sample, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return sample{}, false
	}
	// The format separates tokens with spaces or tabs.
	end := strings.IndexAny(line, "{ \t")
	if end <= 0 || !keep(line[:end]) {
		return sample{}, false
	}

	s := sample{name: line[:end], labels: map[string]string{}}
	rest := line[end:]

	if strings.HasPrefix(rest, "{") {
		var err error
		rest, err = parseLabels(rest[1:], s.labels)
		if err != nil {
			return sample{}, false
		}
	}

	// The value, then optionally a timestamp, which is not needed here.
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return sample{}, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return sample{}, false
	}
	s.value = v
	return s, true
}

// parseLabels reads `a="x",b="y"}` into dst and returns what follows the
// closing brace.
func parseLabels(in string, dst map[string]string) (string, error) {
	for {
		in = strings.TrimLeft(in, " \t,")
		if strings.HasPrefix(in, "}") {
			return in[1:], nil
		}
		eq := strings.IndexByte(in, '=')
		if eq <= 0 {
			return "", fmt.Errorf("malformed label set")
		}
		name := strings.TrimSpace(in[:eq])
		in = strings.TrimLeft(in[eq+1:], " \t")
		if !strings.HasPrefix(in, `"`) {
			return "", fmt.Errorf("malformed label set")
		}

		// Find the closing quote, stepping over escaped characters.
		i := 1
		for ; i < len(in) && in[i] != '"'; i++ {
			if in[i] == '\\' {
				i++
			}
		}
		if i >= len(in) {
			return "", fmt.Errorf("unterminated label value")
		}
		dst[name] = unescapeLabel(in[:i+1])
		in = in[i+1:]
	}
}

// unescapeLabel decodes a quoted label value. The format itself defines only
// \\, \" and \n, but VictoriaLogs writes a data path with Go's %q, which can
// also emit \t, \xNN and \uNNNN — and a path is whatever the operator named it.
// Go's own unquoting covers both; the fallback is for anything it refuses.
func unescapeLabel(quoted string) string {
	if v, err := strconv.Unquote(quoted); err == nil {
		return v
	}
	body := quoted[1 : len(quoted)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			i++
			if body[i] == 'n' {
				b.WriteByte('\n')
				continue
			}
		}
		b.WriteByte(body[i])
	}
	return b.String()
}
