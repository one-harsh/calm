// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"

	"github.com/one-harsh/calm/internal/secrets"
)

type Config struct {
	Calm CalmConfig `mapstructure:"calm"`
	Log  LogConfig  `mapstructure:"log"`
}

type CalmConfig struct {
	URL               string         `mapstructure:"url"`
	APIKey            secrets.Secret `mapstructure:"api_key"`
	Client            string         `mapstructure:"client"`
	SessionTTLMinutes int            `mapstructure:"session_ttl_minutes"`
	// Anchors source-label path normalization, not a sandbox: commands keep full
	// local shell access. Empty → main falls back to os.Getwd() at startup.
	WorkspaceRoot string `mapstructure:"workspace_root"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
	File   string `mapstructure:"file"` // empty = stderr
}

func Load(path string) (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("CALM_ADAPTER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	setDefaults(v)

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return Config{}, fmt.Errorf("read config %s: %w", path, err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg, func(c *mapstructure.DecoderConfig) { c.ErrorUnused = true }); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("calm.url", "http://localhost:8080")
	v.SetDefault("calm.api_key", "")
	v.SetDefault("calm.client", "calm-adapter")
	v.SetDefault("calm.session_ttl_minutes", 120)
	v.SetDefault("calm.workspace_root", "")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("log.file", "")
}

func validate(cfg Config) error {
	if cfg.Calm.URL == "" {
		return errors.New("calm.url is required")
	}
	if cfg.Calm.SessionTTLMinutes <= 0 {
		return fmt.Errorf("calm.session_ttl_minutes must be > 0; got %d", cfg.Calm.SessionTTLMinutes)
	}
	return nil
}
