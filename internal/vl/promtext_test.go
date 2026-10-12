package vl

import (
	"strings"
	"testing"
)

func keepAll(string) bool { return true }

func TestParseSamplesDecodesGoEscapes(t *testing.T) {
	in := `vl_free_disk_space_bytes{path="/data\tlogs/café \"x\""} 5` + "\n"
	got, err := parseSamples(strings.NewReader(in), keepAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].labels["path"] != "/data\tlogs/café \"x\"" {
		t.Errorf("got %+v", got)
	}
}

func TestParseSamplesAcceptsTabs(t *testing.T) {
	got, err := parseSamples(strings.NewReader("vl_partitions\t7\n"), keepAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].name != "vl_partitions" || got[0].value != 7 {
		t.Errorf("got %+v", got)
	}
}

// One line past the buffer is skipped; what surrounds it is still read.
func TestParseSamplesSkipsAnOverlongLine(t *testing.T) {
	in := "vl_partitions 7\n" +
		`flag{name="huge", value="` + strings.Repeat("x", 3*maxMetricsLine) + "\"} 1\n" +
		"vl_storage_parts{type=\"storage/big\"} 4"
	got, err := parseSamples(strings.NewReader(in), keepAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].name != "vl_partitions" || got[1].name != "vl_storage_parts" {
		t.Errorf("got %+v", got)
	}
}

func TestSummariseIgnoresAPathlessGauge(t *testing.T) {
	samples, _ := parseSamples(strings.NewReader(
		"vl_partitions 1\nvl_storage_is_read_only 0\nvl_total_disk_space_bytes{path=\"/d\"} 10\n"), keepAll)
	got, ok := summarise(samples)
	if !ok {
		t.Fatal("no storage gauges found")
	}
	if len(got.Paths) != 1 || got.Paths[0].Path != "/d" {
		t.Errorf("paths = %+v, want only /d", got.Paths)
	}
}
