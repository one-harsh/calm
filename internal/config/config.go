// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"strconv"
)

type Config struct {
	Address string

	ServiceName string
	Version     string
	Environment string
	Region      string
	LogLevel    string
	LogFormat   string

	StorageDSN string

	APIKeysFile          string
	DefaultTTLMinutes    int
	RateLimitPerSecond   int
	MaxIngestPayloadKB   int
	SnapshotMaxBudgetKB  int
	TTLScannerIntervalMS int
}

func Load() (*Config, error) {
	cfg := &Config{
		Address:              env("CALM_ADDR", ":8080"),
		ServiceName:          env("CALM_SERVICE_NAME", "calm"),
		Version:              env("CALM_VERSION", "dev"),
		Environment:          env("CALM_ENVIRONMENT", "local"),
		Region:               env("CALM_REGION", "local"),
		LogLevel:             env("CALM_LOG_LEVEL", "info"),
		LogFormat:            env("CALM_LOG_FORMAT", "json"),
		StorageDSN:           env("CALM_STORAGE_DSN", "postgres://postgres:postgres@localhost:5432/calm?sslmode=disable"),
		APIKeysFile:          env("CALM_API_KEYS_FILE", ""),
		DefaultTTLMinutes:    envInt("CALM_DEFAULT_TTL_MINUTES", 120),
		RateLimitPerSecond:   envInt("CALM_RATE_LIMIT_PER_SECOND", 100),
		MaxIngestPayloadKB:   envInt("CALM_MAX_INGEST_KB", 1024),
		SnapshotMaxBudgetKB:  envInt("CALM_SNAPSHOT_MAX_KB", 8),
		TTLScannerIntervalMS: envInt("CALM_TTL_SCAN_INTERVAL_MS", 60_000),
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
