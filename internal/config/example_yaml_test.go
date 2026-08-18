package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeti-switch/vlui/internal/config"
)

// The example is documentation people copy, and KnownFields makes a stale key
// in it a startup failure on somebody's server. Loading it here is what keeps
// the file and the struct from drifting apart.
func TestExampleConfigLoads(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config.example.yml is missing: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.example.yml does not load: %v", err)
	}

	// Spot-check the values the file is really about, so a silently emptied
	// example cannot pass.
	if cfg.VictoriaLogs.URL == "" || cfg.Metrics.Listen == "" {
		t.Errorf("the example lost its substance: %+v", cfg)
	}
	// Shipping an example with authentication on would break every fresh
	// install, since the issuer in it does not exist.
	if cfg.Auth.Enabled {
		t.Error("the example must ship with auth disabled")
	}
}

// The same file is what the container image carries, and a container that
// listened on loopback would answer nothing from outside.
func TestDockerConfigListensOnAllInterfaces(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "packaging", "config.docker.yml"))
	if err != nil {
		t.Fatalf("packaging/config.docker.yml does not load: %v", err)
	}
	if !strings.HasPrefix(cfg.Listen, "0.0.0.0:") {
		t.Errorf("listen = %q; a container must not listen on loopback", cfg.Listen)
	}
	if !strings.HasPrefix(cfg.Metrics.Listen, "0.0.0.0:") {
		t.Errorf("metrics.listen = %q; a container must not listen on loopback", cfg.Metrics.Listen)
	}
}
