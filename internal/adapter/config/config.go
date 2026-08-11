// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"

	"github.com/one-harsh/calm/internal/secrets"
)

const AdapterConfigFileName = "adapter.yaml"

type Config struct {
	Calm CalmConfig `mapstructure:"calm"`
	Log  LogConfig  `mapstructure:"log"`
}

type CalmConfig struct {
	URL               string         `mapstructure:"url"`
	APIKey            secrets.Secret `mapstructure:"api_key"`
	Client            string         `mapstructure:"client"`
	SessionTTLMinutes int            `mapstructure:"session_ttl_minutes"`
	GCSampleRate      int            `mapstructure:"gc_sample_rate"`
	// KeepSession preserves correlation rows until inactivity-TTL reclamation.
	KeepSession bool `mapstructure:"keep_session"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
	File   string `mapstructure:"file"` // empty = stderr
}

func Load(path, root string) (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("CALM_ADAPTER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	setDefaults(v)

	file := path
	if file == "" && root != "" {
		if candidate := filepath.Join(root, AdapterConfigFileName); fileExists(candidate) {
			file = candidate
		}
	}
	if file != "" {
		v.SetConfigFile(file)
		if err := v.ReadInConfig(); err != nil {
			return Config{}, fmt.Errorf("read config %s: %w", file, err)
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
	v.SetDefault("calm.session_ttl_minutes", 10_080)
	v.SetDefault("calm.gc_sample_rate", 20)
	v.SetDefault("calm.keep_session", false)
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("log.file", "")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func validate(cfg Config) error {
	if cfg.Calm.URL == "" {
		return errors.New("calm.url is required")
	}
	if cfg.Calm.SessionTTLMinutes <= 0 {
		return fmt.Errorf("calm.session_ttl_minutes must be > 0; got %d", cfg.Calm.SessionTTLMinutes)
	}
	if cfg.Calm.GCSampleRate < 0 {
		return fmt.Errorf("calm.gc_sample_rate must be >= 0 (0 disables reclamation sampling); got %d", cfg.Calm.GCSampleRate)
	}
	return nil
}
