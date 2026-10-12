package vl

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
)

// StorageStats is what the status page shows: how much disk VictoriaLogs uses,
// how much it has left, and how well it compresses. All of it is read from
// VictoriaLogs' own /metrics, the only place it is published.
//
// That makes it single-node only. In a cluster vlselect answers /metrics too,
// but about itself: the storage gauges live on each vlstorage node, which this
// process does not talk to.
//
// Instance-wide, too: the gauges are per storage tier and per data path, never
// per tenant or per stream.
type StorageStats struct {
	// On-disk bytes, from vl_data_size_bytes: the log data, and the index of
	// stream fields that finds it.
	DataBytes    int64 `json:"data_bytes"`
	IndexdbBytes int64 `json:"indexdb_bytes"`

	// Sums over the tiers below.
	CompressedBytes   int64 `json:"compressed_bytes"`
	UncompressedBytes int64 `json:"uncompressed_bytes"`
	Rows              int64 `json:"rows"`
	Parts             int64 `json:"parts"`

	// Daily partitions: how many days of logs are on disk.
	Partitions int64 `json:"partitions"`

	// The -retentionPeriod flag as given, e.g. "7d". Empty when VictoriaLogs
	// does not export its flags.
	Retention string `json:"retention,omitempty"`

	Tiers []StorageTier `json:"tiers"`
	Paths []DiskPath    `json:"paths"`
}

// StorageTier is one of VictoriaLogs' storage levels: inmemory (recent data,
// still in RAM), small and big parts.
type StorageTier struct {
	Name              string `json:"name"`
	Rows              int64  `json:"rows"`
	Parts             int64  `json:"parts"`
	CompressedBytes   int64  `json:"compressed_bytes"`
	UncompressedBytes int64  `json:"uncompressed_bytes"`
}

// DiskPath is the filesystem one storage directory lives on.
type DiskPath struct {
	Path       string `json:"path"`
	FreeBytes  int64  `json:"free_bytes"`
	TotalBytes int64  `json:"total_bytes"`
	// The size past which retention deletes the oldest partitions. Zero when
	// no cap is configured or this VictoriaLogs does not report one.
	MaxUsageBytes int64 `json:"max_usage_bytes,omitempty"`
	// Set when free space fell under -storage.minFreeDiskSpaceBytes and
	// VictoriaLogs stopped accepting logs.
	ReadOnly bool `json:"read_only"`
}

// maxMetricsBody caps what is read from /metrics. A VictoriaLogs answers in a
// few hundred kilobytes; anything far past that is not one.
const maxMetricsBody = 16 << 20

// StorageStats reads and summarises /metrics.
func (c *Client) StorageStats(ctx context.Context) (StorageStats, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.Timeout)
	defer cancel()

	resp, err := c.get(ctx, EndpointMetrics)
	if err != nil {
		return StorageStats{}, err
	}
	defer resp.Body.Close()

	samples, err := parseSamples(io.LimitReader(resp.Body, maxMetricsBody), func(name string) bool {
		return strings.HasPrefix(name, "vl_") || name == "flag"
	})
	if err != nil {
		return StorageStats{}, fmt.Errorf("victorialogs %s: %w", EndpointMetrics, err)
	}

	out, ok := summarise(samples)
	if !ok {
		// Most likely a vlselect, or a proxy that answers /metrics for itself.
		return StorageStats{}, fmt.Errorf("victorialogs %s: no storage metrics in the answer — "+
			"victorialogs.url has to be a single-node VictoriaLogs; a cluster's vlselect does not export them",
			EndpointMetrics)
	}
	return out, nil
}

// summarise folds the samples into StorageStats. ok is false when none of the
// storage gauges were present at all.
func summarise(samples []sample) (StorageStats, bool) {
	var out StorageStats
	tiers := map[string]*StorageTier{}
	paths := map[string]*DiskPath{}
	found := false

	tier := func(s sample) *StorageTier {
		name, ok := strings.CutPrefix(s.labels["type"], "storage/")
		if !ok {
			return nil
		}
		t := tiers[name]
		if t == nil {
			t = &StorageTier{Name: name}
			tiers[name] = t
		}
		return t
	}
	// Nil for a sample with no path label: a filesystem nobody can name is a
	// row of zeros on the page, not information.
	path := func(s sample) *DiskPath {
		name := s.labels["path"]
		if name == "" {
			return nil
		}
		p := paths[name]
		if p == nil {
			p = &DiskPath{Path: name}
			paths[name] = p
		}
		return p
	}

	for _, s := range samples {
		v := int64(s.value)
		switch s.name {
		case "vl_data_size_bytes":
			found = true
			switch s.labels["type"] {
			case "storage":
				out.DataBytes = v
			case "indexdb":
				out.IndexdbBytes = v
			}
		case "vl_compressed_data_size_bytes":
			if t := tier(s); t != nil {
				found = true
				t.CompressedBytes = v
			}
		case "vl_uncompressed_data_size_bytes":
			if t := tier(s); t != nil {
				found = true
				t.UncompressedBytes = v
			}
		case "vl_storage_rows":
			if t := tier(s); t != nil {
				found = true
				t.Rows = v
			}
		case "vl_storage_parts":
			if t := tier(s); t != nil {
				found = true
				t.Parts = v
			}
		case "vl_partitions":
			found = true
			out.Partitions = v
		case "vl_free_disk_space_bytes":
			if p := path(s); p != nil {
				p.FreeBytes = v
			}
		case "vl_total_disk_space_bytes":
			if p := path(s); p != nil {
				p.TotalBytes = v
			}
		case "vl_max_disk_space_usage_bytes":
			if p := path(s); p != nil {
				p.MaxUsageBytes = v
			}
		case "vl_storage_is_read_only":
			if p := path(s); p != nil {
				p.ReadOnly = s.value != 0
			}
		case "flag":
			if s.labels["name"] == "retentionPeriod" {
				out.Retention = s.labels["value"]
			}
		}
	}

	for _, t := range tiers {
		out.CompressedBytes += t.CompressedBytes
		out.UncompressedBytes += t.UncompressedBytes
		out.Rows += t.Rows
		out.Parts += t.Parts
		out.Tiers = append(out.Tiers, *t)
	}
	// Youngest data first, the order it moves through them.
	order := map[string]int{"inmemory": 0, "small": 1, "big": 2}
	slices.SortFunc(out.Tiers, func(a, b StorageTier) int {
		oa, aok := order[a.Name]
		ob, bok := order[b.Name]
		switch {
		case aok && bok:
			return cmp.Compare(oa, ob)
		case aok:
			return -1
		case bok:
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})

	for _, p := range paths {
		out.Paths = append(out.Paths, *p)
	}
	slices.SortFunc(out.Paths, func(a, b DiskPath) int { return cmp.Compare(a.Path, b.Path) })

	// Never null in the JSON: the page iterates both.
	if out.Tiers == nil {
		out.Tiers = []StorageTier{}
	}
	if out.Paths == nil {
		out.Paths = []DiskPath{}
	}
	return out, found
}
