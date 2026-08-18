package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/yeti-switch/vlui/internal/vl"
)

// keepalive is how often a comment line is sent down an idle tail. Quiet logs
// are normal, and every proxy between here and the browser has an idle timeout
// that would otherwise close the stream and make the pane look broken.
const keepalive = 15 * time.Second

// defaultTailBackfill is how much history a tail starts with. Without it the
// pane sits empty until something new is ingested, which reads as a bug rather
// than as a quiet system.
const defaultTailBackfill = 5 * time.Minute

// handleTail is live tailing, delivered as Server-Sent Events.
//
// SSE rather than WebSocket: this is one-way, text, and already framed by
// newlines. EventSource reconnects on its own, works through any HTTP proxy,
// and needs no protocol upgrade in nginx.
func (s *Server) handleTail(w http.ResponseWriter, r *http.Request) {
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

	// Bounded: a tab left open over a weekend otherwise holds an upstream
	// stream open for the weekend. When it expires the browser reconnects by
	// itself, so nobody sees the seam.
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.VictoriaLogs.TailMaxDuration)
	defer cancel()

	body, err := s.vl.Tail(ctx, vl.TailParams{
		Query:           query,
		Filters:         filters(tool),
		StartOffset:     optDuration(r, "start_offset", defaultTailBackfill),
		Offset:          optDuration(r, "offset", 0),
		RefreshInterval: optDuration(r, "refresh_interval", 0),
	})
	if err != nil {
		s.upstream(w, r, err)
		return
	}
	defer body.Close()

	defer s.m.TailStarted()()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	// Writes on a tail are rare and small, so each one is flushed: a log line
	// held in a buffer for a few hundred milliseconds defeats the point.
	_ = rc.Flush()

	lines, readErr := readLines(ctx, body)

	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()

	rows := 0
	for {
		select {
		case <-ctx.Done():
			// Either the browser went away, or the tail hit its ceiling. Both
			// end the same way: stop, and let EventSource come back.
			s.m.AddRows(rows)
			return

		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				s.m.AddRows(rows)
				return
			}
			_ = rc.Flush()

		case line, ok := <-lines:
			if !ok {
				s.m.AddRows(rows)
				if err := <-readErr; err != nil && !isDisconnect(ctx, err) {
					s.log.Warn("tail stream failed", "err", err)
					writeEvent(w, "error", mustJSON(map[string]string{"error": err.Error()}))
					_ = rc.Flush()
				}
				return
			}

			if !writeEvent(w, "", line) {
				s.m.AddRows(rows)
				return
			}
			_ = rc.Flush()
			rows++
			ticker.Reset(keepalive)
		}
	}
}

// readLines pumps the upstream NDJSON into a channel, so the handler can also
// watch its ticker and its context. Reading inline would block on a quiet log
// stream and never send a keepalive.
func readLines(ctx context.Context, body io.Reader) (<-chan []byte, <-chan error) {
	lines := make(chan []byte)
	errc := make(chan error, 1)

	go func() {
		defer close(lines)
		br := bufio.NewReaderSize(body, 64<<10)
		for {
			line, err := br.ReadBytes('\n')
			line = bytes.TrimRight(line, "\r\n")
			if len(line) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					errc <- ctx.Err()
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					errc <- nil
					return
				}
				errc <- err
				return
			}
		}
	}()

	return lines, errc
}

// writeEvent emits one SSE event, reporting whether the client is still there.
func writeEvent(w http.ResponseWriter, event string, data []byte) bool {
	if event != "" {
		if _, err := io.WriteString(w, "event: "+event+"\n"); err != nil {
			return false
		}
	}
	// One JSON object per line, so the data field never contains a newline and
	// the frame stays a single "data:" line.
	if _, err := w.Write(append(append([]byte("data: "), data...), '\n', '\n')); err != nil {
		return false
	}
	return true
}

// isDisconnect reports whether the error is just the tail being ended by us or
// by the browser, rather than something worth logging.
func isDisconnect(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":"encode"}`)
	}
	return b
}
