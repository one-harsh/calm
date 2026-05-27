// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package config loads the CALM service configuration from a YAML file at
// the path given by CALM_CONFIG_FILE. Env vars prefixed CALM_ override file
// values via Viper's key replacer (e.g., CALM_SERVER_ADDRESS overrides
// server.address). The loader fails fast on missing file, unknown keys, or
// invalid namespace declarations — no silent degraded mode.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"

	"github.com/one-harsh/calm/internal/secrets"
)

const (
	maxNamespaceLength = 64
	minRatePerSecond   = 1
	maxRatePerSecond   = 100000
)

var namespaceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Config struct {
	Service    ServiceConfig     `mapstructure:"service"`
	Server     ServerConfig      `mapstructure:"server"`
	Sessions   SessionsConfig    `mapstructure:"sessions"`
	Storage    StorageConfig     `mapstructure:"storage"`
	Namespaces []NamespaceConfig `mapstructure:"namespaces"`
}

type ServiceConfig struct {
	ServiceName string `mapstructure:"service_name"`
	Version     string `mapstructure:"version"`
	Environment string `mapstructure:"environment"`
	Region      string `mapstructure:"region"`
	LogLevel    string `mapstructure:"log_level"`
	LogFormat   string `mapstructure:"log_format"`
}

type ServerConfig struct {
	Address              string        `mapstructure:"address"`
	RequestTimeout       time.Duration `mapstructure:"request_timeout"`
	GracefulShutdownWait time.Duration `mapstructure:"graceful_shutdown_wait"`
	MaxIngestPayloadKB   int           `mapstructure:"max_ingest_payload_kb"`
	RateLimitPerSecond   int           `mapstructure:"rate_limit_per_second"`
}

type SessionsConfig struct {
	DefaultTTLMinutes    int `mapstructure:"default_ttl_minutes"`
	SnapshotMaxBudgetKB  int `mapstructure:"snapshot_max_budget_kb"`
	TTLScannerIntervalMS int `mapstructure:"ttl_scanner_interval_ms"`
	TTLScannerJitterMS   int `mapstructure:"ttl_scanner_jitter_ms"`
	CacheSize            int `mapstructure:"cache_size"`
}

// StorageConfig carries Postgres connection details and the migration
// strategy. MigrateOnStartup=true is the v1 default (single-replica deploys);
// production multi-replica installs flip it off and run migrations as a
// separate job to avoid concurrent migrator races.
type StorageConfig struct {
	DSN              string `mapstructure:"dsn"`
	MigrateOnStartup bool   `mapstructure:"migrate_on_startup"`
}

type NamespaceConfig struct {
	Name          string         `mapstructure:"name"`
	APIKey        secrets.Secret `mapstructure:"api_key"`
	RatePerSecond int            `mapstructure:"rate_per_second"`
}

// Load reads the YAML config at path, applies env overrides (CALM_-prefixed,
// `.`/`-` in keys map to `_` in env names), unmarshals into Config, and
// validates. Returns wrapped error on any failure; callers should surface
// these as service-startup failures (no degraded mode).
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("CALM_CONFIG_FILE is required")
	}

	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvPrefix("CALM")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	setDefaults(v)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg,
		func(c *mapstructure.DecoderConfig) { c.ErrorUnused = true }); err != nil {
		return nil, fmt.Errorf("unmarshal config %s: %w", path, err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("service.service_name", "calm")
	v.SetDefault("service.version", "dev")
	v.SetDefault("service.environment", "local")
	v.SetDefault("service.region", "local")
	v.SetDefault("service.log_level", "info")
	v.SetDefault("service.log_format", "json")

	v.SetDefault("server.address", ":8080")
	v.SetDefault("server.request_timeout", 2*time.Second)
	v.SetDefault("server.graceful_shutdown_wait", 10*time.Second)
	v.SetDefault("server.max_ingest_payload_kb", 1024)
	v.SetDefault("server.rate_limit_per_second", 100)

	v.SetDefault("sessions.default_ttl_minutes", 120)
	v.SetDefault("sessions.snapshot_max_budget_kb", 8)
	v.SetDefault("sessions.ttl_scanner_interval_ms", 60_000)
	v.SetDefault("sessions.ttl_scanner_jitter_ms", 10_000)
	v.SetDefault("sessions.cache_size", 10_000)

	v.SetDefault("storage.migrate_on_startup", true)
}

func validate(cfg *Config) error {
	if len(cfg.Namespaces) == 0 {
		return errors.New("at least one namespace is required (namespaces: [])")
	}
	if cfg.Storage.DSN == "" {
		return errors.New("storage.dsn is required")
	}
	if cfg.Sessions.CacheSize < 0 {
		return fmt.Errorf("sessions.cache_size must be >= 0 (0 disables cache); got %d", cfg.Sessions.CacheSize)
	}
	if cfg.Sessions.TTLScannerIntervalMS < 0 {
		return fmt.Errorf("sessions.ttl_scanner_interval_ms must be >= 0 (0 disables scanner); got %d", cfg.Sessions.TTLScannerIntervalMS)
	}
	if cfg.Sessions.TTLScannerJitterMS < 0 {
		return fmt.Errorf("sessions.ttl_scanner_jitter_ms must be >= 0; got %d", cfg.Sessions.TTLScannerJitterMS)
	}
	// Jitter must be strictly less than interval; otherwise a tick could
	// compute a delay <= 0 → busy loop. Only enforce when scanner is enabled.
	if cfg.Sessions.TTLScannerIntervalMS > 0 && cfg.Sessions.TTLScannerJitterMS >= cfg.Sessions.TTLScannerIntervalMS {
		return fmt.Errorf("sessions.ttl_scanner_jitter_ms (%d) must be < sessions.ttl_scanner_interval_ms (%d)",
			cfg.Sessions.TTLScannerJitterMS, cfg.Sessions.TTLScannerIntervalMS)
	}

	seenNames := map[string]int{}
	seenSecrets := map[string]int{}
	for i, ns := range cfg.Namespaces {
		if ns.Name == "" {
			return fmt.Errorf("namespaces[%d]: name is required", i)
		}
		if !namespaceNameRegex.MatchString(ns.Name) {
			return fmt.Errorf("namespaces[%d]: invalid name %q (allowed pattern: [a-zA-Z0-9_-], 1-%d chars)", i, ns.Name, maxNamespaceLength)
		}
		if prev, ok := seenNames[ns.Name]; ok {
			return fmt.Errorf("namespaces[%d]: duplicate name %q (also at index %d)", i, ns.Name, prev)
		}
		seenNames[ns.Name] = i

		if ns.APIKey == "" {
			return fmt.Errorf("namespaces[%d] (%s): api_key is required", i, ns.Name)
		}
		raw := string(ns.APIKey)
		if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
			return fmt.Errorf("namespaces[%d] (%s): api_key must be a bracketed secret reference [scheme:payload]; got %q", i, ns.Name, raw)
		}
		if prev, ok := seenSecrets[raw]; ok {
			return fmt.Errorf("namespaces[%d] (%s): duplicate api_key reference %q (also at index %d)", i, ns.Name, raw, prev)
		}
		seenSecrets[raw] = i

		if ns.RatePerSecond < 0 {
			return fmt.Errorf("namespaces[%d] (%s): rate_per_second must be >= 0 (0 means fall back to global)", i, ns.Name)
		}
		if ns.RatePerSecond > 0 {
			if ns.RatePerSecond < minRatePerSecond {
				return fmt.Errorf("namespaces[%d] (%s): rate_per_second %d below minimum (%d)", i, ns.Name, ns.RatePerSecond, minRatePerSecond)
			}
			if ns.RatePerSecond > maxRatePerSecond {
				return fmt.Errorf("namespaces[%d] (%s): rate_per_second %d above maximum (%d)", i, ns.Name, ns.RatePerSecond, maxRatePerSecond)
			}
		}
	}
	return nil
}
