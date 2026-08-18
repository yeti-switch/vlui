package vl_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeti-switch/vlui/internal/vl"
)

func TestEveryRequestCarriesTheTenantAndCredentials(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"values":[]}`))
	}))
	defer srv.Close()

	c := vl.New(vl.Options{
		URL: srv.URL, Timeout: 5 * time.Second,
		Username: "u", Password: "p",
		AccountID: 12, ProjectID: 34,
	}, nil)

	if _, err := c.FieldNames(context.Background(), vl.ValuesParams{Query: "*"}); err != nil {
		t.Fatal(err)
	}

	if got.Header.Get("AccountID") != "12" || got.Header.Get("ProjectID") != "34" {
		t.Errorf("tenant headers = %q/%q", got.Header.Get("AccountID"), got.Header.Get("ProjectID"))
	}
	user, pass, ok := got.BasicAuth()
	if !ok || user != "u" || pass != "p" {
		t.Errorf("basic auth = %q/%q (ok=%v)", user, pass, ok)
	}
	// POST: a LogsQL query is easily longer than a proxy will accept in a URL.
	if got.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.Method)
	}
}

// The tenant is sent even when it is the default, so a misconfigured proxy
// cannot silently redirect the read somewhere else.
func TestTheDefaultTenantIsStillExplicit(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"values":[]}`))
	}))
	defer srv.Close()

	c := vl.New(vl.Options{URL: srv.URL, Timeout: 5 * time.Second}, nil)
	if _, err := c.FieldNames(context.Background(), vl.ValuesParams{Query: "*"}); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("AccountID") != "0" || got.Header.Get("ProjectID") != "0" {
		t.Error("the default tenant must still be sent explicitly")
	}
}

func TestErrorCarriesTheUpstreamMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `cannot parse query: unexpected token "|~"`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := vl.New(vl.Options{URL: srv.URL, Timeout: 5 * time.Second}, nil)
	_, err := c.Query(context.Background(), vl.QueryParams{Query: "bad |~ query"})

	var vlErr *vl.Error
	if !errors.As(err, &vlErr) {
		t.Fatalf("err = %v, want a *vl.Error", err)
	}
	if vlErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", vlErr.StatusCode)
	}
	// The message names the offending token; anything of our own would be
	// strictly less useful.
	if !strings.Contains(vlErr.Error(), "unexpected token") {
		t.Errorf("error = %q", vlErr.Error())
	}
}

func TestQueryStreamsRatherThanBuffers(t *testing.T) {
	// The handler sends one line, then blocks. A client that buffered the body
	// would return nothing until the handler gave up.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"_msg":"first"}` + "\n"))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := vl.New(vl.Options{URL: srv.URL, Timeout: 5 * time.Second}, nil)
	body, err := c.Query(context.Background(), vl.QueryParams{Query: "*", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()

	buf := make([]byte, 64)
	n, err := body.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "first") {
		t.Errorf("read %q before the handler finished, want the first line", buf[:n])
	}
}

// The observer is what feeds the exporter. It must fire for failures too, or
// vl_requests_total would only ever count successes.
func TestObserverSeesBothOutcomes(t *testing.T) {
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"values":[]}`))
	}))
	defer srv.Close()

	var seen []string
	c := vl.New(vl.Options{URL: srv.URL, Timeout: 5 * time.Second}, func(endpoint, status string, d time.Duration) {
		seen = append(seen, endpoint+"="+status)
	})

	ctx := context.Background()
	_, _ = c.FieldNames(ctx, vl.ValuesParams{Query: "*"})
	fail = true
	_, _ = c.FieldNames(ctx, vl.ValuesParams{Query: "*"})

	want := []string{
		vl.EndpointFieldNames + "=ok",
		vl.EndpointFieldNames + "=500",
	}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Errorf("observed %v, want %v", seen, want)
	}
}

func TestPingIsTheHealthEndpoint(t *testing.T) {
	var path, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, method = r.URL.Path, r.Method
		_, _ = io.WriteString(w, "OK")
	}))
	defer srv.Close()

	c := vl.New(vl.Options{URL: srv.URL, Timeout: 5 * time.Second}, nil)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if path != vl.EndpointHealth || method != http.MethodGet {
		t.Errorf("ping hit %s %s, want GET %s", method, path, vl.EndpointHealth)
	}
}
