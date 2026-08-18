package api

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/yeti-switch/vlui/internal/auth"
	"github.com/yeti-switch/vlui/internal/config"
	"github.com/yeti-switch/vlui/internal/vl"
)

// Tools are the icons in the left rail, and each one's query is a constraint on
// everything the session asks for.
//
// Three decisions here are what make that constraint real, and all three are
// about the fact that this API is reachable with curl by anyone holding a
// session cookie:
//
//   - The filter is applied HERE, never composed in the browser. A prefix the
//     SPA glues onto the query box is worth nothing: the next request can leave
//     it off.
//
//   - It is sent as extra_filters rather than concatenated into the query.
//     VictoriaLogs propagates extra_filters into every subquery — `| join`,
//     `| union`, `:in(...)` — and its own documentation names that as the
//     mechanism for restricting queries to a subset of logs. A concatenated
//     prefix is escapable through a subquery, which is exactly the bypass
//     somebody would find.
//
//   - A missing `tool` parameter selects the FIRST tool, not "no filter". The
//     failure mode of the opposite default is a request that omits the
//     parameter and reads everything, which is precisely what an attacker would
//     try first.
//
// What this does NOT do on its own: stop a signed-in operator switching to a
// less restrictive tool. If the rail is meant to be a boundary rather than a
// convenience, the wide tools need allowed_groups — see config.Tool.
func (s *Server) resolveTool(r *http.Request) (*config.Tool, error) {
	if len(s.cfg.Tools) == 0 {
		return nil, nil
	}

	id := r.FormValue("tool")
	if id == "" {
		// Deliberately the first tool rather than an unfiltered read.
		return s.authorizeTool(r, &s.cfg.Tools[0])
	}

	for i := range s.cfg.Tools {
		if s.cfg.Tools[i].ID == id {
			return s.authorizeTool(r, &s.cfg.Tools[i])
		}
	}
	return nil, &toolError{code: http.StatusBadRequest, msg: fmt.Sprintf("unknown tool %q", id)}
}

func (s *Server) authorizeTool(r *http.Request, t *config.Tool) (*config.Tool, error) {
	if s.permitted(r, t) {
		return t, nil
	}
	// Named, not hidden behind a 404: the operator can see the tool is a thing
	// that exists, and the message tells whoever is on call what to grant.
	return nil, &toolError{
		code: http.StatusForbidden,
		msg:  fmt.Sprintf("tool %q is restricted to other groups", t.Tooltip),
	}
}

// permitted reports whether the caller may use this tool.
func (s *Server) permitted(r *http.Request, t *config.Tool) bool {
	if len(t.AllowedGroups) == 0 {
		return true
	}
	// Config refuses allowed_groups without auth.enabled, so a request reaching
	// here with no user is one the middleware would already have rejected.
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		return false
	}
	return slices.ContainsFunc(u.Groups, func(g string) bool {
		return slices.Contains(t.AllowedGroups, g)
	})
}

// filters is what every handler passes to the client.
func filters(t *config.Tool) vl.Filters {
	if t == nil || t.Query == "" {
		return nil
	}
	return vl.Filters{t.Query}
}

type toolError struct {
	code int
	msg  string
}

func (e *toolError) Error() string { return e.msg }

// toolFailed writes the response for a tool that could not be resolved.
func (s *Server) toolFailed(w http.ResponseWriter, r *http.Request, err error) {
	var te *toolError
	if ok := asToolError(err, &te); ok {
		s.log.Warn("tool refused", "path", r.URL.Path, "tool", r.FormValue("tool"), "err", te.msg)
		writeJSON(w, te.code, map[string]string{"error": te.msg})
		return
	}
	s.badRequest(w, r, err)
}

func asToolError(err error, target **toolError) bool {
	te, ok := err.(*toolError)
	if ok {
		*target = te
	}
	return ok
}
