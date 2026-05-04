// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"fmt"
	"os"

	logging "github.com/one-harsh/context-logging"
)

func NewLogger(serviceName, version, environment, region, level, format string) (*logging.Logger, error) {
	logger, err := logging.New(logging.Config{
		Level:       level,
		Format:      format,
		Output:      os.Stdout,
		Service:     serviceName,
		Version:     version,
		Environment: environment,
		Region:      region,
	})
	if err != nil {
		return nil, fmt.Errorf("init logger: %w", err)
	}
	return logger, nil
}

const (
	KeySessionID  = "session_id"
	KeyNamespace  = "namespace"
	KeySource     = "source"
	KeyMatchLayer = "match_layer"
	KeyEndpoint   = "endpoint"
	KeyEventType  = "event_type"
	KeyFormatHint = "format_hint"
)

func SessionID(value string) logging.LoggingField {
	return logging.StringField(KeySessionID, value)
}

func Namespace(value string) logging.LoggingField {
	return logging.StringField(KeyNamespace, value)
}

func Source(value string) logging.LoggingField {
	return logging.StringField(KeySource, value)
}

func MatchLayer(value string) logging.LoggingField {
	return logging.StringField(KeyMatchLayer, value)
}

func Endpoint(value string) logging.LoggingField {
	return logging.StringField(KeyEndpoint, value)
}

func EventType(value string) logging.LoggingField {
	return logging.StringField(KeyEventType, value)
}

func FormatHint(value string) logging.LoggingField {
	return logging.StringField(KeyFormatHint, value)
}
