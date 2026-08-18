// Package vl is the VictoriaLogs client.
//
// It is deliberately thin. Log results are handed back as an open stream rather
// than a decoded slice: /select/logsql/query emits one JSON object per line as
// soon as each is found, and a result set can be arbitrarily large, so anything
// that decoded it whole would trade the streaming for memory and latency and
// give nothing back. The API layer forwards those lines to the browser as it
// reads them, and closing the client connection stops the query at the far end —
// VictoriaLogs frees the query as soon as it sees the reader go away.
package vl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	// URL is the VictoriaLogs base URL with no trailing slash and no path.
	URL string

	// Timeout bounds the small JSON endpoints (hits, facets, field names and
	// values, health). It deliberately does NOT bound Query or Tail: those are
	// streams whose lifetime belongs to the caller's context.
	Timeout time.Duration

	Username string
	Password string

	// AccountID / ProjectID address one tenant, sent as headers of the same
	// name on every request. Fixed for the process.
	AccountID int
	ProjectID int
}

// Observer is told about every completed upstream request. Nil is fine; it is
// how tests and a metrics-disabled process avoid a branch at each call site.
type Observer func(endpoint, status string, d time.Duration)

type Client struct {
	base    string
	hc      *http.Client
	opt     Options
	observe Observer
}

func New(opt Options, obs Observer) *Client {
	return &Client{
		base: strings.TrimRight(opt.URL, "/"),
		// No Client.Timeout: it applies to the whole exchange including the
		// body, so it would cut live tailing off mid-stream. Deadlines come
		// from the context instead, per call.
		hc:      &http.Client{},
		opt:     opt,
		observe: obs,
	}
}

// Error carries what VictoriaLogs said. Its body is the whole point: a LogsQL
// syntax error is the most common failure here, and the upstream message names
// the offending token. Anything that replaced it with "query failed" would make
// the tool unusable.
type Error struct {
	Endpoint   string
	StatusCode int
	Body       string
}

func (e *Error) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("victorialogs %s: HTTP %d", e.Endpoint, e.StatusCode)
	}
	return e.Body
}

// maxErrBody caps what is read from a failed response. VictoriaLogs answers
// errors in a line or two; a proxy in front of it may answer with a whole HTML
// page, and none of it needs to reach memory.
const maxErrBody = 8 << 10

// post issues a form-encoded POST. POST rather than GET throughout: a LogsQL
// query is easily longer than what a proxy will accept in a URL, and a query
// truncated by an intermediary fails in a way nobody can read.
//
// The returned response's Body is the caller's to close.
func (c *Client) post(ctx context.Context, endpoint string, form url.Values) (*http.Response, error) {
	started := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		c.report(endpoint, "error", started)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.applyAuth(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		c.report(endpoint, "error", started)
		return nil, fmt.Errorf("victorialogs %s: %w", endpoint, err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		resp.Body.Close()
		c.report(endpoint, strconv.Itoa(resp.StatusCode), started)
		return nil, &Error{
			Endpoint:   endpoint,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
		}
	}

	// Reported on the response headers, not on the last byte of the body: for
	// a stream that is the only moment that exists, and it is also what
	// VictoriaLogs' own VL-Request-Duration-Seconds measures.
	c.report(endpoint, "ok", started)
	return resp, nil
}

func (c *Client) applyAuth(req *http.Request) {
	if c.opt.Username != "" || c.opt.Password != "" {
		req.SetBasicAuth(c.opt.Username, c.opt.Password)
	}
	// Always sent, including the (0,0) default: being explicit means a
	// misconfigured vmauth cannot silently redirect the read to another tenant.
	req.Header.Set("AccountID", strconv.Itoa(c.opt.AccountID))
	req.Header.Set("ProjectID", strconv.Itoa(c.opt.ProjectID))
}

func (c *Client) report(endpoint, status string, started time.Time) {
	if c.observe != nil {
		c.observe(endpoint, status, time.Since(started))
	}
}

// getJSON runs one of the small endpoints and decodes the result. The timeout
// is the client's, since none of these stream.
func (c *Client) getJSON(ctx context.Context, endpoint string, form url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.opt.Timeout)
	defer cancel()

	resp, err := c.post(ctx, endpoint, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("victorialogs %s: decode response: %w", endpoint, err)
	}
	return nil
}
