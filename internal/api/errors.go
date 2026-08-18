package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/yeti-switch/vlui/internal/vl"
)

// badRequest is for what the caller got wrong. Its message is always shown.
func (s *Server) badRequest(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Debug("bad request", "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

// upstream turns a VictoriaLogs failure into a response.
//
// The upstream message is passed through verbatim, which is a deliberate
// departure from the usual "5xx says nothing" rule. A LogsQL syntax error is
// the single most common failure in this application, and VictoriaLogs names
// the offending token; so does a query that hit -search.maxQueryDuration. Any
// wording of our own would be strictly less useful, and none of it is
// information the caller could not obtain by querying VictoriaLogs directly —
// which, being signed in here, they are already entitled to do.
func (s *Server) upstream(w http.ResponseWriter, r *http.Request, err error) {
	reqID := middleware.GetReqID(r.Context())

	// The caller closed the tab or hit Cancel. Nothing to report and nobody to
	// report it to: the connection is already gone.
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return
	}

	if errors.Is(err, context.DeadlineExceeded) {
		s.log.Warn("victorialogs timed out", "path", r.URL.Path, "request_id", reqID)
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{
			"error":      "VictoriaLogs did not answer within victorialogs.timeout",
			"request_id": reqID,
		})
		return
	}

	var vlErr *vl.Error
	if errors.As(err, &vlErr) {
		// A 4xx from VictoriaLogs is the user's query being wrong, so it is the
		// user's error here too. Anything else is the upstream being unwell,
		// which is a 502 no matter what it called itself.
		code := http.StatusBadGateway
		if vlErr.StatusCode >= 400 && vlErr.StatusCode < 500 {
			code = http.StatusBadRequest
		} else {
			s.log.Warn("victorialogs error", "path", r.URL.Path, "status", vlErr.StatusCode,
				"request_id", reqID, "err", vlErr.Body)
		}
		writeJSON(w, code, map[string]any{
			"error":           vlErr.Error(),
			"upstream_status": vlErr.StatusCode,
			"request_id":      reqID,
		})
		return
	}

	// Transport failures: VictoriaLogs is down, DNS is wrong, TLS is refused.
	s.log.Error("victorialogs unreachable", "path", r.URL.Path, "request_id", reqID, "err", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{
		"error":      err.Error(),
		"request_id": reqID,
	})
}
