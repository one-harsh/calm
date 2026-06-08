// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"fmt"
	"os"

	logging "github.com/one-harsh/context-logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func InitTracePropagation(otelEnabled bool) {
	if otelEnabled {
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		return
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
}

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
	KeySessionID               = "session.id"
	KeyNamespace               = "namespace"
	KeyClient                  = "client"
	KeySource                  = "source"
	KeyMatchLayer              = "match_layer"
	KeyEndpoint                = "endpoint"
	KeyEventType               = "event_type"
	KeyFormatHint              = "format_hint"
	KeyCloseReason             = "close_reason"
	KeyRateLimitTier           = "ratelimit.tier"
	KeyAuditInitiator          = "audit_initiator"
	KeyCascadedSources         = "session.delete.cascaded_sources"
	KeyCascadedChunks          = "session.delete.cascaded_chunks"
	KeyCascadedEvents          = "session.delete.cascaded_events"
	KeyCascadedLabels          = "session.delete.cascaded_labels"
	KeyCorrelationID           = "correlation_id"
	KeyFeedbackOutcome         = "feedback_outcome"
	KeyReferencedCorrelationID = "feedback.referenced_correlation_id"
)

var (
	MatchLayerPrimary = logging.StringField(KeyMatchLayer, "primary")
	MatchLayerTrigram = logging.StringField(KeyMatchLayer, "trigram")

	CloseReasonTTLExpired = logging.StringField(KeyCloseReason, "ttl_expired")
	CloseReasonExplicit   = logging.StringField(KeyCloseReason, "explicit")

	RateLimitTierIP        = logging.StringField(KeyRateLimitTier, "ip")
	RateLimitTierGlobal    = logging.StringField(KeyRateLimitTier, "global")
	RateLimitTierNamespace = logging.StringField(KeyRateLimitTier, "namespace")

	AuditInitiatorAPI    = logging.StringField(KeyAuditInitiator, "api")
	AuditInitiatorSystem = logging.StringField(KeyAuditInitiator, "system")
)

// Safe to log everywhere — non-secret.
func SessionID(value int64) logging.LoggingField {
	return logging.Int64Field(KeySessionID, value)
}

func Namespace(value string) logging.LoggingField {
	return logging.StringField(KeyNamespace, value)
}

func Client(value string) logging.LoggingField {
	return logging.StringField(KeyClient, value)
}

func Source(value string) logging.LoggingField {
	return logging.StringField(KeySource, value)
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

func CascadedSources(value int) logging.LoggingField {
	return logging.IntField(KeyCascadedSources, value)
}

func CascadedChunks(value int) logging.LoggingField {
	return logging.IntField(KeyCascadedChunks, value)
}

func CascadedEvents(value int) logging.LoggingField {
	return logging.IntField(KeyCascadedEvents, value)
}

func CascadedLabels(value int) logging.LoggingField {
	return logging.IntField(KeyCascadedLabels, value)
}

func CorrelationID(value string) logging.LoggingField {
	return logging.StringField(KeyCorrelationID, value)
}

func FeedbackOutcome(value string) logging.LoggingField {
	return logging.StringField(KeyFeedbackOutcome, value)
}

func ReferencedCorrelationID(value string) logging.LoggingField {
	return logging.StringField(KeyReferencedCorrelationID, value)
}
