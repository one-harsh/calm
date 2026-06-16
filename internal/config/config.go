// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package config loads CALM's YAML config. Fail-fast on missing file,
// unknown keys, or invalid namespace declarations — no silent degraded mode.
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
	maxNamespaceLength        = 64
	minRatePerSecond          = 1
	maxRatePerSecond          = 100000
	minFeedbackTTLMinutes     = 1
	maxFeedbackTTLMinutes     = 1440
	defaultFeedbackTTLMinutes = 60
)

var namespaceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Config struct {
	Service       ServiceConfig       `mapstructure:"service"`
	Server        ServerConfig        `mapstructure:"server"`
	Sessions      SessionsConfig      `mapstructure:"sessions"`
	Storage       StorageConfig       `mapstructure:"storage"`
	Observability ObservabilityConfig `mapstructure:"observability"`
	Namespaces    []NamespaceConfig   `mapstructure:"namespaces"`
}

type ObservabilityConfig struct {
	Logging LoggingConfig `mapstructure:"logging"`
	OTel    OTelConfig    `mapstructure:"otel"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type OTelConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

type ServiceConfig struct {
	ServiceName string `mapstructure:"service_name"`
	Version     string `mapstructure:"version"`
	Environment string `mapstructure:"environment"`
	Region      string `mapstructure:"region"`
}

type ServerConfig struct {
	Address                  string        `mapstructure:"address"`
	RequestTimeout           time.Duration `mapstructure:"request_timeout"`
	GracefulShutdownWait     time.Duration `mapstructure:"graceful_shutdown_wait"`
	MaxIngestPayloadKB       int           `mapstructure:"max_ingest_payload_kb"`
	RateLimitPerSecond       int           `mapstructure:"rate_limit_per_second"`
	RateLimitGlobalPerSecond int           `mapstructure:"rate_limit_global_per_second"`
	RateLimitPerIPPerSecond  int           `mapstructure:"rate_limit_per_ip_per_second"`
	TrustProxyHeaders        bool          `mapstructure:"trust_proxy_headers"`
	TLS                      TLSConfig     `mapstructure:"tls"`
}

// TLSConfig is opt-in: when Enabled, CALM terminates TLS itself (server-auth
// only). Default off — transport security is edge-terminated. CertFile/KeyFile
// resolve through the secret dialect to PEM contents (see internal/secrets).
type TLSConfig struct {
	Enabled  bool           `mapstructure:"enabled"`
	CertFile secrets.Secret `mapstructure:"cert_file"`
	KeyFile  secrets.Secret `mapstructure:"key_file"`
}

type SessionsConfig struct {
	DefaultTTLMinutes    int           `mapstructure:"default_ttl_minutes"`
	MaxTTLMinutes        int           `mapstructure:"max_ttl_minutes"`
	SnapshotMaxBudgetKB  int           `mapstructure:"snapshot_max_budget_kb"`
	TTLScannerIntervalMS int           `mapstructure:"ttl_scanner_interval_ms"`
	TTLScannerJitterMS   int           `mapstructure:"ttl_scanner_jitter_ms"`
	CacheSize            int           `mapstructure:"cache_size"`
	IdempotencyKeyTTL    time.Duration `mapstructure:"idempotency_key_ttl"`
	IdempotencyKeySize   int           `mapstructure:"idempotency_key_size"`
}

type StorageConfig struct {
	DSN string `mapstructure:"dsn"`
	// MigrateOnStartup off in multi-replica deploys to avoid concurrent migrator races.
	MigrateOnStartup bool `mapstructure:"migrate_on_startup"`
	// Zero on any pool field leaves the database/sql driver default.
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
}

type NamespaceConfig struct {
	Name                     string         `mapstructure:"name"`
	APIKey                   secrets.Secret `mapstructure:"api_key"`
	RatePerSecond            int            `mapstructure:"rate_per_second"`
	RequireClientCredentials bool           `mapstructure:"require_client_credentials"`
	FeedbackTTLMinutes       int            `mapstructure:"feedback_ttl_minutes"`
}

// Load reads the YAML at path, applies CALM_-prefixed env overrides, validates.
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
	v.SetDefault("observability.logging.level", "info")
	v.SetDefault("observability.logging.format", "json")

	v.SetDefault("server.address", ":8080")
	v.SetDefault("server.request_timeout", 2*time.Second)
	v.SetDefault("server.graceful_shutdown_wait", 10*time.Second)
	v.SetDefault("server.max_ingest_payload_kb", 1024)
	v.SetDefault("server.rate_limit_per_second", 100)
	v.SetDefault("server.rate_limit_global_per_second", 0)
	v.SetDefault("server.rate_limit_per_ip_per_second", 100)
	v.SetDefault("server.trust_proxy_headers", false)
	// Registered (zero-value defaults) so the keys are env-overridable
	// (CALM_SERVER_TLS_*); empty cert/key + enabled=false ⇒ TLS off.
	v.SetDefault("server.tls.enabled", false)
	v.SetDefault("server.tls.cert_file", "")
	v.SetDefault("server.tls.key_file", "")

	v.SetDefault("sessions.default_ttl_minutes", 120)
	v.SetDefault("sessions.max_ttl_minutes", 10_080)
	v.SetDefault("sessions.snapshot_max_budget_kb", 8)
	v.SetDefault("sessions.ttl_scanner_interval_ms", 60_000)
	v.SetDefault("sessions.ttl_scanner_jitter_ms", 10_000)
	v.SetDefault("sessions.cache_size", 10_000)
	v.SetDefault("sessions.idempotency_key_ttl", time.Hour)
	v.SetDefault("sessions.idempotency_key_size", 10_000)

	v.SetDefault("storage.migrate_on_startup", true)
	v.SetDefault("storage.max_open_conns", 25)
	v.SetDefault("storage.max_idle_conns", 25)
	v.SetDefault("storage.conn_max_lifetime", 30*time.Minute)
}

func validate(cfg *Config) error {
	if len(cfg.Namespaces) == 0 {
		return errors.New("at least one namespace is required (namespaces: [])")
	}
	if cfg.Storage.DSN == "" {
		return errors.New("storage.dsn is required")
	}
	if cfg.Storage.MaxOpenConns < 0 {
		return fmt.Errorf("storage.max_open_conns must be >= 0 (0 means driver default); got %d", cfg.Storage.MaxOpenConns)
	}
	if cfg.Storage.MaxIdleConns < 0 {
		return fmt.Errorf("storage.max_idle_conns must be >= 0 (0 means driver default); got %d", cfg.Storage.MaxIdleConns)
	}
	if cfg.Storage.ConnMaxLifetime < 0 {
		return fmt.Errorf("storage.conn_max_lifetime must be >= 0 (0 means no lifetime cap); got %v", cfg.Storage.ConnMaxLifetime)
	}
	if cfg.Storage.MaxOpenConns > 0 && cfg.Storage.MaxIdleConns > cfg.Storage.MaxOpenConns {
		return fmt.Errorf("storage.max_idle_conns (%d) must be <= storage.max_open_conns (%d) — database/sql would silently clamp it",
			cfg.Storage.MaxIdleConns, cfg.Storage.MaxOpenConns)
	}
	if err := validateRate("server.rate_limit_per_second", cfg.Server.RateLimitPerSecond); err != nil {
		return err
	}
	if err := validateRate("server.rate_limit_global_per_second", cfg.Server.RateLimitGlobalPerSecond); err != nil {
		return err
	}
	if err := validateRate("server.rate_limit_per_ip_per_second", cfg.Server.RateLimitPerIPPerSecond); err != nil {
		return err
	}
	if cfg.Server.TLS.Enabled {
		if cfg.Server.TLS.CertFile == "" || cfg.Server.TLS.KeyFile == "" {
			return errors.New("server.tls: cert_file and key_file are required when enabled")
		}
		cert := string(cfg.Server.TLS.CertFile)
		if !strings.HasPrefix(cert, "[") || !strings.HasSuffix(cert, "]") {
			return fmt.Errorf("server.tls.cert_file must be a bracketed secret reference [scheme:payload]; got %q", cert)
		}
		key := string(cfg.Server.TLS.KeyFile)
		if !strings.HasPrefix(key, "[") || !strings.HasSuffix(key, "]") {
			return fmt.Errorf("server.tls.key_file must be a bracketed secret reference [scheme:payload]; got %q", key)
		}
	}
	if cfg.Sessions.CacheSize < 0 {
		return fmt.Errorf("sessions.cache_size must be >= 0 (0 disables cache); got %d", cfg.Sessions.CacheSize)
	}
	if cfg.Sessions.DefaultTTLMinutes <= 0 || cfg.Sessions.DefaultTTLMinutes > 10_080 {
		return fmt.Errorf("sessions.default_ttl_minutes must be in [1, 10080]; got %d", cfg.Sessions.DefaultTTLMinutes)
	}
	if cfg.Sessions.MaxTTLMinutes <= 0 || cfg.Sessions.MaxTTLMinutes > 10_080 {
		return fmt.Errorf("sessions.max_ttl_minutes must be in [1, 10080]; got %d", cfg.Sessions.MaxTTLMinutes)
	}
	if cfg.Sessions.DefaultTTLMinutes > cfg.Sessions.MaxTTLMinutes {
		return fmt.Errorf("sessions.default_ttl_minutes (%d) must be <= sessions.max_ttl_minutes (%d) — otherwise the absent-TTL fallback would exceed the operator ceiling",
			cfg.Sessions.DefaultTTLMinutes, cfg.Sessions.MaxTTLMinutes)
	}
	if cfg.Sessions.TTLScannerIntervalMS < 0 {
		return fmt.Errorf("sessions.ttl_scanner_interval_ms must be >= 0 (0 disables scanner); got %d", cfg.Sessions.TTLScannerIntervalMS)
	}
	if cfg.Sessions.TTLScannerJitterMS < 0 {
		return fmt.Errorf("sessions.ttl_scanner_jitter_ms must be >= 0; got %d", cfg.Sessions.TTLScannerJitterMS)
	}
	// Jitter >= interval would compute a delay <= 0 → busy loop.
	if cfg.Sessions.TTLScannerIntervalMS > 0 && cfg.Sessions.TTLScannerJitterMS >= cfg.Sessions.TTLScannerIntervalMS {
		return fmt.Errorf("sessions.ttl_scanner_jitter_ms (%d) must be < sessions.ttl_scanner_interval_ms (%d)",
			cfg.Sessions.TTLScannerJitterMS, cfg.Sessions.TTLScannerIntervalMS)
	}
	if cfg.Sessions.IdempotencyKeyTTL <= 0 {
		return fmt.Errorf("sessions.idempotency_key_ttl must be > 0; got %v", cfg.Sessions.IdempotencyKeyTTL)
	}
	if cfg.Sessions.IdempotencyKeySize < 0 {
		return fmt.Errorf("sessions.idempotency_key_size must be >= 0 (0 disables dedup); got %d", cfg.Sessions.IdempotencyKeySize)
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

		if ns.FeedbackTTLMinutes < 0 {
			return fmt.Errorf("namespaces[%d] (%s): feedback_ttl_minutes must be >= 0 (0 means use the %d-minute default)", i, ns.Name, defaultFeedbackTTLMinutes)
		}
		if ns.FeedbackTTLMinutes > 0 {
			if ns.FeedbackTTLMinutes < minFeedbackTTLMinutes {
				return fmt.Errorf("namespaces[%d] (%s): feedback_ttl_minutes %d below minimum (%d)", i, ns.Name, ns.FeedbackTTLMinutes, minFeedbackTTLMinutes)
			}
			if ns.FeedbackTTLMinutes > maxFeedbackTTLMinutes {
				return fmt.Errorf("namespaces[%d] (%s): feedback_ttl_minutes %d above maximum (%d)", i, ns.Name, ns.FeedbackTTLMinutes, maxFeedbackTTLMinutes)
			}
		}
	}
	return nil
}

func validateRate(field string, value int) error {
	if value < 0 {
		return fmt.Errorf("%s must be >= 0 (0 disables this tier); got %d", field, value)
	}
	if value > 0 && value > maxRatePerSecond {
		return fmt.Errorf("%s %d above maximum (%d)", field, value, maxRatePerSecond)
	}
	return nil
}
