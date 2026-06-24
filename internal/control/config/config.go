// Package config loads the panel's own configuration (listen address, data
// dir, engine binary paths, role). Secrets like jwt_secret / panel_path /
// admin credentials are generated on first run and stored in the DB, not here.
package config

import (
	"os"
	"path/filepath"
)

// Role values for the single-binary multi-mode design.
const (
	RoleStandalone = "standalone" // Lite: control + node in one process
	RoleControl    = "control"    // Platform (v2): control plane only
	RoleNode       = "node"       // Platform (v2): node agent only
)

// Config is the panel runtime configuration.
type Config struct {
	Role          string
	Listen        string // panel HTTP listen address
	DataDir       string // base dir for sqlite, certs, configs
	SingboxBin    string
	XrayBin       string
	SingboxConfig string
	XrayConfig    string
	LogLevel      string
}

// Default returns a config populated with sensible defaults. Flags/env override
// these in main.
func Default() Config {
	dataDir := "/etc/edgenest"
	// In dev / non-root environments, fall back to a local data dir.
	if _, err := os.Stat("/etc"); err != nil || os.Getuid() != 0 {
		if wd, e := os.Getwd(); e == nil {
			dataDir = filepath.Join(wd, ".edgenest-data")
		}
	}
	return Config{
		Role: RoleStandalone,
		// Default to the dual-stack wildcard so a fresh binary started without
		// an explicit --listen flag accepts both v4 and v6 clients out of the
		// box. Linux net.Listen("tcp", "[::]:port") sets IPV6_V6ONLY=0 so the
		// same socket accepts v4-mapped peers. On a kernel where install.sh
		// disabled v6 (the v4-only branch), this addr fails and main.go's
		// startup fallback retries with 0.0.0.0 so the panel still comes up.
		Listen:  "[::]:2087",
		DataDir: dataDir,
		SingboxBin:    "/usr/local/bin/sing-box",
		XrayBin:       "/usr/local/bin/xray",
		SingboxConfig: filepath.Join(dataDir, "sing-box.json"),
		XrayConfig:    filepath.Join(dataDir, "xray.json"),
		LogLevel:      "info",
	}
}

// DBPath returns the sqlite file path under the data dir.
func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, "edgenest.db")
}

// EnsureDataDir creates the data directory if missing.
func (c Config) EnsureDataDir() error {
	return os.MkdirAll(c.DataDir, 0o750)
}
