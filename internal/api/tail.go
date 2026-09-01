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

// defaultTailBackfill is how much history a tail starts with when the caller
// asks for none. Without it the pane sits empty until something new is
// ingested, which reads as a bug rather than as a quiet system.
const defaultTailBackfill = 5 * time.Minute

// backfillSettle is how long the handler waits, once the history has stopped
// arriving, before deciding the backfill is over.
//
// The transition is normally spotted by the first row timestamped after the
// tail began. On a quiet stream that row may never come, and the buffered
// history would sit here unsent — so silence ends the backfill too. Short,
// because it delays the first paint by this much on a stream with no live
// traffic; comfortably longer than a local read of a few thousand rows.
const backfillSettle = 750 * time.Millisecond

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

	/* The backfill is bounded by ROWS, here, rather than by how far back the
	 * caller asked to look.
	 *
	 * A tail that starts from an hour ago on a busy stream is millions of lines
	 * that the browser would receive, hold for a moment and throw away — it
	 * keeps the newest `limit` of them and nothing else. Bounding the duration
	 * instead would be the wrong knob: it makes "follow this, starting from
	 * what I was looking at" impossible to ask for on a busy system and
	 * pointlessly restrictive on a quiet one.
	 *
	 * So the history is buffered here, `limit` rows deep, and only that much is
	 * written out. VictoriaLogs still reads what was asked of it, but that
	 * traffic stays between it and this process, on the same network.
	 *
	 * Which rows are history: those timestamped before the tail began. That is
	 * exactly what start_offset asked upstream for, so the boundary is the one
	 * VictoriaLogs itself is working to.
	 */
	startedAt := time.Now()
	backfill := newRing(s.limit(r))

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

	// Ticks only while the history is still being buffered; stopped for good at
	// the transition, after which every line goes straight out.
	settle := time.NewTimer(backfillSettle)
	defer settle.Stop()

	rows := 0

	// send writes one line and keeps the bookkeeping in one place.
	send := func(line []byte) bool {
		if !writeEvent(w, "", line) {
			return false
		}
		rows++
		return true
	}

	// flush ends the backfill: out goes the tail of the history, newest `limit`
	// rows of it, in the order VictoriaLogs sent them.
	flush := func() bool {
		for _, line := range backfill.drain() {
			if !send(line) {
				return false
			}
		}
		_ = rc.Flush()
		settle.Stop()
		return true
	}

	for {
		select {
		case <-ctx.Done():
			// Either the browser went away, or the tail hit its ceiling. Both
			// end the same way: stop, and let EventSource come back.
			s.m.AddRows(rows)
			return

		case <-settle.C:
			// The history has stopped arriving and nothing live has come to
			// mark the boundary. Send what is held and start following.
			if !flush() {
				s.m.AddRows(rows)
				return
			}

		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				s.m.AddRows(rows)
				return
			}
			_ = rc.Flush()

		case line, ok := <-lines:
			if !ok {
				// The stream ended. Whatever history is held is still worth
				// sending — a tail of a quiet system can end this way.
				flush()
				s.m.AddRows(rows)
				if err := <-readErr; err != nil && !isDisconnect(ctx, err) {
					s.log.Warn("tail stream failed", "err", err)
					writeEvent(w, "error", mustJSON(map[string]string{"error": err.Error()}))
					_ = rc.Flush()
				}
				return
			}

			// History is held back until there is enough of it to know which
			// rows are the newest. A line whose timestamp is at or after the
			// moment this tail began is the first live one, and ends that.
			if backfill.holding() && isHistory(line, startedAt) {
				backfill.add(line)
				settle.Reset(backfillSettle)
				ticker.Reset(keepalive)
				continue
			}
			if backfill.holding() && !flush() {
				s.m.AddRows(rows)
				return
			}

			if !send(line) {
				s.m.AddRows(rows)
				return
			}
			_ = rc.Flush()
			ticker.Reset(keepalive)
		}
	}
}

/* isHistory reports whether a row was ingested before this tail began, which is
 * what makes it part of the backfill rather than of the follow.
 *
 * Only _time is parsed out of the line; the rest is passed through as the bytes
 * that arrived, so a row this process does not understand is still a row the
 * browser gets. A line with no readable _time counts as live: the failure that
 * shows too much is better than the one that silently holds rows back.
 */
func isHistory(line []byte, startedAt time.Time) bool {
	var row struct {
		Time string `json:"_time"`
	}
	if err := json.Unmarshal(line, &row); err != nil || row.Time == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, row.Time)
	if err != nil {
		return false
	}
	return t.Before(startedAt)
}

/* ring holds the last n lines of the backfill and nothing else.
 *
 * A ring rather than a growing slice because the backfill can be any size at
 * all — an hour of a busy stream is millions of lines — and only the newest n
 * of them will ever be written. Memory here is therefore the caller's limit,
 * not the caller's time range.
 */
type ring struct {
	lines []([]byte)
	n     int  // how many have been added in total
	open  bool // still holding, i.e. the backfill has not been flushed
}

func newRing(size int) *ring {
	if size < 1 {
		size = 1
	}
	return &ring{lines: make([][]byte, size), open: true}
}

func (r *ring) holding() bool { return r.open }

func (r *ring) add(line []byte) {
	r.lines[r.n%len(r.lines)] = line
	r.n++
}

// drain returns what is held, oldest first, and closes the ring: everything
// after this goes straight out.
func (r *ring) drain() [][]byte {
	r.open = false

	first := 0
	if r.n > len(r.lines) {
		first = r.n - len(r.lines)
	}
	out := make([][]byte, 0, r.n-first)
	for i := first; i < r.n; i++ {
		out = append(out, r.lines[i%len(r.lines)])
	}
	r.lines = nil
	return out
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
