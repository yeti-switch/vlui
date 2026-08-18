package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/yeti-switch/vlui/internal/vl"
)

// ErrorField is the key of the sentinel line that reports a failure which
// happened after the response had already begun.
//
// Once the first log line is on the wire the status code is spent, so a stream
// that dies halfway can only say so in band. The alternative — buffering the
// whole result to keep the option of a clean 500 — would throw away the
// streaming that makes a large query usable at all.
//
// A real log entry carrying this exact field would be indistinguishable, which
// is why the name is one nothing emits by accident.
const ErrorField = "_vlui_error"

// flushInterval is how often the buffer is pushed to the browser during a long
// query. Often enough that rows appear as they are found; rarely enough that a
// fast query is not a syscall per line.
const flushInterval = 100 * time.Millisecond

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	// The tool comes first: its filter is resolved and applied here rather than
	// accepted from the caller (see internal/api/tools.go), and it also decides
	// whether an empty query is an error.
	tool, err := s.resolveTool(r)
	if err != nil {
		s.toolFailed(w, r, err)
		return
	}
	query, err := queryFor(r, tool)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	rng, err := s.timeRange(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	limit := s.limit(r)

	// The whole query, not just its headers: a stream held open forever is the
	// failure mode this bounds.
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.VictoriaLogs.Timeout)
	defer cancel()

	defer s.m.QueryStarted()()

	body, err := s.vl.Query(ctx, vl.QueryParams{Query: query, Range: rng, Limit: limit, Filters: filters(tool)})
	if err != nil {
		s.upstream(w, r, err)
		return
	}
	// Closing the body is what stops the query at the far end, so it must
	// happen on every path — including the browser hitting Cancel.
	defer body.Close()

	// NDJSON, forwarded as it arrives, so the table fills while the query is
	// still running.
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// nginx buffers proxied responses by default, which would hold every row
	// back until the query finished and undo all of the above.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rows, bytes, err := s.pump(w, body, limit)
	s.m.AddRows(rows)
	s.m.AddBytes(bytes)

	if err != nil {
		s.streamFailed(w, r, err, rows)
	}
}

// pump forwards NDJSON lines, counting them and stopping at limit.
//
// The limit is enforced here as well as upstream. VictoriaLogs honours the
// limit arg, but this process is what feeds a browser, and "how many rows can
// reach the tab" should not depend on the far end agreeing about a query shape.
func (s *Server) pump(w http.ResponseWriter, body io.Reader, limit int) (rows int, written int64, err error) {
	rc := http.NewResponseController(w)
	br := bufio.NewReaderSize(body, 64<<10)

	lastFlush := time.Now()

	for rows < limit {
		// ReadBytes, not Scanner: a single log entry can be a megabyte of stack
		// trace, and Scanner's token limit would turn that into a truncated
		// stream rather than a big line.
		line, readErr := br.ReadBytes('\n')

		if len(line) > 0 {
			n, writeErr := w.Write(line)
			written += int64(n)
			if writeErr != nil {
				// The browser is gone. Not an error to report — there is
				// nobody left to report it to.
				return rows, written, nil
			}
			if line[len(line)-1] == '\n' {
				rows++
			}

			if time.Since(lastFlush) >= flushInterval {
				_ = rc.Flush()
				lastFlush = time.Now()
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			_ = rc.Flush()
			return rows, written, readErr
		}
	}

	_ = rc.Flush()
	return rows, written, nil
}

// streamFailed appends the sentinel line described at ErrorField.
func (s *Server) streamFailed(w http.ResponseWriter, r *http.Request, err error, rows int) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return // the caller cancelled; the connection is already closed
	}

	msg := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "VictoriaLogs did not finish within victorialogs.timeout; showing partial results"
	}
	s.log.Warn("query stream failed", "rows", rows, "err", err)

	line, mErr := json.Marshal(map[string]string{ErrorField: msg})
	if mErr != nil {
		return
	}
	_, _ = w.Write(append(line, '\n'))
	_ = http.NewResponseController(w).Flush()
}
