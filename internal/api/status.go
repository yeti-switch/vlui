package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/yeti-switch/vlui/internal/vl"
)

// handleStatus feeds the /status page: disk use, free space and compression,
// read from VictoriaLogs' own /metrics.
//
// Not scoped by tool, because it cannot be: the gauges are per storage tier and
// per data path, and say nothing about which logs fill them. Who may see them
// at all is status.allowed_groups.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !inGroups(r, s.cfg.Status.AllowedGroups) {
		s.log.Warn("status refused", "path", r.URL.Path)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "the status page is restricted to other groups",
		})
		return
	}

	out, err := s.vl.StorageStats(r.Context())
	if err != nil {
		// upstream() reads a 4xx as the user's query being wrong. There is no
		// query here: a 401, 403 or 404 on /metrics is a proxy in front of
		// VictoriaLogs that does not route it, which is the deployment's
		// problem and has to reach the log.
		var vlErr *vl.Error
		if errors.As(err, &vlErr) && vlErr.StatusCode < 500 {
			reqID := middleware.GetReqID(r.Context())
			s.log.Warn("victorialogs refused /metrics", "status", vlErr.StatusCode,
				"request_id", reqID, "err", vlErr.Body)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error":           vlErr.Error(),
				"upstream_status": vlErr.StatusCode,
				"request_id":      reqID,
			})
			return
		}
		s.upstream(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
