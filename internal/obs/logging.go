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
	return logger.WithCallerSkip(1), nil
}

const (
	// Request / correlation scope.
	KeyCorrelationID  = "correlation_id"
	KeyRequestType    = "request_type"
	KeyEndpoint       = "endpoint"
	KeyErrorBody      = "error_body"
	KeyAuditInitiator = "audit_initiator"

	// Isolation identity.
	KeyNamespace = "namespace"
	KeyClient    = "client"

	// Session scope.
	KeySessionID       = "session_id"
	KeyCloseReason     = "close_reason"
	KeyCascadedSources = "session.delete.cascaded_sources"
	KeyCascadedChunks  = "session.delete.cascaded_chunks"
	KeyCascadedEvents  = "session.delete.cascaded_events"
	KeyCascadedLabels  = "session.delete.cascaded_labels"

	// Source / content scope.
	KeySource          = "source"
	KeyFormatHint      = "format_hint"
	KeyFormatEffective = "format_effective"

	// Event scope.
	KeyEventType = "event_type"

	// Ingest scope.
	KeyIngestSectionsTotal   = "ingest.sections_total"
	KeyIngestSectionsIndexed = "ingest.sections_indexed"
	KeyIngestSourceCreated   = "ingest.source_created"

	// Search scope.
	KeyMatchLayer        = "match_layer"
	KeySearchQueries     = "search.queries"
	KeySearchHitsTotal   = "search.hits_total"
	KeySearchHitsPrimary = "search.hits_primary"
	KeySearchHitsTrigram = "search.hits_trigram"

	// Snapshot scope.
	KeySnapshotEventsIncluded = "snapshot.events_included"
	KeySnapshotEventsTotal    = "snapshot.events_total"
	KeySnapshotByteBudgetUsed = "snapshot.byte_budget_used"
	KeySnapshotBudgetExceeded = "snapshot.budget_exceeded"

	// Feedback scope.
	KeyFeedbackOutcome                 = "feedback_outcome"
	KeyFeedbackReferencedCorrelationID = "feedback_referenced_correlation_id"

	// Rate limiting.
	KeyRateLimitTier = "ratelimit_tier"
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

func FormatEffective(value string) logging.LoggingField {
	return logging.StringField(KeyFormatEffective, value)
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

func FeedbackReferencedCorrelationID(value string) logging.LoggingField {
	return logging.StringField(KeyFeedbackReferencedCorrelationID, value)
}

func RequestType(value string) logging.LoggingField {
	return logging.StringField(KeyRequestType, value)
}

func ErrorBody(value string) logging.LoggingField {
	return logging.StringField(KeyErrorBody, value)
}

func SearchQueries(value int) logging.LoggingField {
	return logging.IntField(KeySearchQueries, value)
}

func SearchHitsTotal(value int) logging.LoggingField {
	return logging.IntField(KeySearchHitsTotal, value)
}

func SearchHitsPrimary(value int) logging.LoggingField {
	return logging.IntField(KeySearchHitsPrimary, value)
}

func SearchHitsTrigram(value int) logging.LoggingField {
	return logging.IntField(KeySearchHitsTrigram, value)
}

func IngestSectionsTotal(value int) logging.LoggingField {
	return logging.IntField(KeyIngestSectionsTotal, value)
}

func IngestSectionsIndexed(value int) logging.LoggingField {
	return logging.IntField(KeyIngestSectionsIndexed, value)
}

func IngestSourceCreated(value bool) logging.LoggingField {
	return logging.BoolField(KeyIngestSourceCreated, value)
}

func SnapshotEventsIncluded(value int) logging.LoggingField {
	return logging.IntField(KeySnapshotEventsIncluded, value)
}

func SnapshotEventsTotal(value int) logging.LoggingField {
	return logging.IntField(KeySnapshotEventsTotal, value)
}

func SnapshotByteBudgetUsed(value int) logging.LoggingField {
	return logging.IntField(KeySnapshotByteBudgetUsed, value)
}

func SnapshotBudgetExceeded(value bool) logging.LoggingField {
	return logging.BoolField(KeySnapshotBudgetExceeded, value)
}
