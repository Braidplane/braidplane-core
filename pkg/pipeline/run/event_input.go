package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const MaxEventInputBytes = 1024 * 1024

// EventInput is a validated, runtime-only JSON payload supplied after external verification.
type EventInput struct {
	key     slug.Slug
	payload *pipepayload.Payload
	summary EventInputSummary
}

// EventInputSummary is the only event-input data safe for run history.
type EventInputSummary struct {
	Keys            []string                     `json:"keys"`
	Representation  model.ContractRepresentation `json:"representation"`
	SizeBytes       int64                        `json:"size_bytes"`
	DigestAlgorithm string                       `json:"digest_algorithm"`
	Digest          string                       `json:"digest"`
}

// NewVerifiedJSONEventInput accepts bytes only after the caller verifies the external delivery.
func NewVerifiedJSONEventInput(key slug.Slug, document json.RawMessage) (*EventInput, error) {
	if _, err := slug.New(key.String()); err != nil {
		return nil, xerror.InvalidInput(xerror.Event).Detailf("event input key: %w", err)
	}
	if len(document) == 0 {
		return nil, xerror.InvalidRequired(xerror.Event).Detail("event input JSON is required")
	}
	if len(document) > MaxEventInputBytes {
		return nil, xerror.InvalidInput(xerror.Event).Detailf(
			"event input exceeds %d byte limit",
			MaxEventInputBytes,
		)
	}
	if !json.Valid(document) {
		return nil, xerror.InvalidInput(xerror.Event).Detail("event input must be valid JSON")
	}

	document = append(json.RawMessage(nil), document...)
	digest := sha256.Sum256(document)
	return &EventInput{
		key: key,
		payload: &pipepayload.Payload{
			Representation:     model.ContractRepresentationJSON,
			RootShape:          eventInputRootShape(document),
			VerificationStatus: pipepayload.VerificationStatusVerified,
			Provenance:         pipepayload.ProvenanceRunInput,
			Document:           document,
		},
		summary: EventInputSummary{
			Keys:            []string{key.String()},
			Representation:  model.ContractRepresentationJSON,
			SizeBytes:       int64(len(document)),
			DigestAlgorithm: "sha256",
			Digest:          hex.EncodeToString(digest[:]),
		},
	}, nil
}

func (input *EventInput) Key() slug.Slug {
	if input == nil {
		return ""
	}
	return input.key
}

func (input *EventInput) Payload() *pipepayload.Payload {
	if input == nil {
		return nil
	}
	return input.payload.Clone()
}

func (input *EventInput) Summary() *EventInputSummary {
	if input == nil {
		return nil
	}
	summary := input.summary
	summary.Keys = append([]string(nil), summary.Keys...)
	return &summary
}

func eventInputRootShape(document []byte) model.ContractRootShape {
	switch bytes.TrimSpace(document)[0] {
	case '{':
		return model.ContractRootShapeObject
	case '[':
		return model.ContractRootShapeArray
	default:
		return model.ContractRootShapeUnknown
	}
}
