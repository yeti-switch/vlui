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

// A trimmed /metrics from a single-node VictoriaLogs, with the lines around the
// storage gauges kept so the parser has to skip them.
const metricsSample = `# HELP vl_data_size_bytes Data size
# TYPE vl_data_size_bytes gauge
vl_data_size_bytes{type="storage"} 1000
vl_data_size_bytes{type="indexdb"} 50
vl_compressed_data_size_bytes{type="storage/inmemory"} 10
vl_compressed_data_size_bytes{type="storage/small"} 90
vl_compressed_data_size_bytes{type="storage/big"} 900
vl_uncompressed_data_size_bytes{type="storage/big"} 9000
vl_uncompressed_data_size_bytes{type="storage/inmemory"} 50
vl_uncompressed_data_size_bytes{type="storage/small"} 950
vl_storage_rows{type="storage/big"} 3e+06
vl_storage_rows{type="storage/small"} 1000
vl_storage_parts{type="storage/big"} 4
vl_partitions 7
vl_free_disk_space_bytes{path="/vlogs \"data\""} 2000
vl_total_disk_space_bytes{path="/vlogs \"data\""} 5000
vl_max_disk_space_usage_bytes{path="/vlogs \"data\""} 4000
vl_storage_is_read_only{path="/vlogs \"data\""} 0
vl_http_requests_total{path="/select/logsql/query"} 12
flag{name="retentionPeriod", value="7d", is_set="true"} 1
flag{name="httpListenAddr", value=":9428", is_set="false"} 0
go_goroutines 42
`

func TestStorageStatsSummarisesMetrics(t *testing.T) {
	var path, method, account string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, method, account = r.URL.Path, r.Method, r.Header.Get("AccountID")
		_, _ = io.WriteString(w, metricsSample)
	}))
	defer srv.Close()

	c := vl.New(vl.Options{URL: srv.URL, Timeout: 5 * time.Second, AccountID: 3}, nil)
	got, err := c.StorageStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != vl.EndpointMetrics || method != http.MethodGet || account != "3" {
		t.Errorf("hit %s %s with AccountID %q, want GET %s with 3", method, path, account, vl.EndpointMetrics)
	}

	if got.DataBytes != 1000 || got.IndexdbBytes != 50 {
		t.Errorf("data/indexdb = %d/%d, want 1000/50", got.DataBytes, got.IndexdbBytes)
	}
	if got.CompressedBytes != 1000 || got.UncompressedBytes != 10000 {
		t.Errorf("compressed/uncompressed = %d/%d, want 1000/10000", got.CompressedBytes, got.UncompressedBytes)
	}
	if got.Rows != 3001000 || got.Parts != 4 || got.Partitions != 7 {
		t.Errorf("rows/parts/partitions = %d/%d/%d, want 3001000/4/7", got.Rows, got.Parts, got.Partitions)
	}
	if got.Retention != "7d" {
		t.Errorf("retention = %q, want 7d", got.Retention)
	}

	var names []string
	for _, tier := range got.Tiers {
		names = append(names, tier.Name)
	}
	if strings.Join(names, ",") != "inmemory,small,big" {
		t.Errorf("tiers = %v, want inmemory,small,big", names)
	}

	want := vl.DiskPath{Path: `/vlogs "data"`, FreeBytes: 2000, TotalBytes: 5000, MaxUsageBytes: 4000}
	if len(got.Paths) != 1 || got.Paths[0] != want {
		t.Errorf("paths = %+v, want [%+v]", got.Paths, want)
	}
}

func TestStorageStatsRefusesAnAnswerWithoutStorageGauges(t *testing.T) {
	// What a vlselect answers: metrics, but none about storage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "vl_http_requests_total{path=\"/select/logsql/query\"} 12\ngo_goroutines 42\n")
	}))
	defer srv.Close()

	c := vl.New(vl.Options{URL: srv.URL, Timeout: 5 * time.Second}, nil)
	_, err := c.StorageStats(context.Background())
	if err == nil || !strings.Contains(err.Error(), "single-node") {
		t.Fatalf("err = %v, want one that names the single-node requirement", err)
	}
}
