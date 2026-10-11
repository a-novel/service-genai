package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
)

// Dependencies of [GenerationSubmit].
type (
	// GenerationSubmitDao records the generation.
	GenerationSubmitDao interface {
		Exec(ctx context.Context, request *dao.GenerationSubmitRequest) (*dao.GenerationSubmitResult, error)
	}
	// GenerationSubmitAttemptListDao reads the attempts of a replayed generation.
	GenerationSubmitAttemptListDao interface {
		Exec(ctx context.Context, request *dao.GenerationAttemptListRequest) ([]*dao.GenerationAttempt, error)
	}
	// GenerationSubmitServiceCheck starts a newly recorded generation.
	GenerationSubmitServiceCheck interface {
		Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
	}
)

// GenerationSubmitConfig is what a [GenerationSubmit] needs to run.
type GenerationSubmitConfig struct {
	// MaxAttempts caps the provider calls a generation gets when a call fails retryably.
	MaxAttempts int16 `validate:"required,min=1,max=10"`
}

// GenerationSubmitRequest holds the parameters for a [GenerationSubmit.Exec] call.
type GenerationSubmitRequest struct {
	// OwnerID is the user the generation acts for, supplied by a caller that already verified it.
	OwnerID uuid.UUID `validate:"required"`
	// Purpose is why the caller makes the call, up to 255 characters, in the caller's own vocabulary.
	Purpose string `validate:"required,notblank,max=255"`
	// Provider names the service to run the call on. It must have an adapter offering the Async
	// mode.
	Provider string `validate:"required"`
	// Payload is the provider's own request, built by the caller: a JSON object this service
	// forwards without reading.
	Payload json.RawMessage `validate:"required"`
	// Variant asks for another generation of a request that already succeeded.
	Variant uint32
}

// GenerationSubmitResult reports the stored generation and how it got there.
type GenerationSubmitResult struct {
	Generation *Generation
	// Created is false on a replay, so a resending caller attaches to work already paid for.
	Created bool
}

// A GenerationSubmit records a generation and starts its provider call in the Async mode.
//
// The generation is identified by a key derived from the request and the owner, so a caller that
// lost track of it recovers by resending the same request. A replay returns the recorded generation
// as it stands, without checking it: the caller polls it next, and the poll checks it. A failed or
// cancelled generation does not count, so resending its request runs it again.
type GenerationSubmit struct {
	config     GenerationSubmitConfig
	adapters   lib.Adapters
	dao        GenerationSubmitDao
	attemptDao GenerationSubmitAttemptListDao
	check      GenerationSubmitServiceCheck
}

func NewGenerationSubmit(
	config GenerationSubmitConfig,
	adapters lib.Adapters,
	submitDao GenerationSubmitDao,
	attemptDao GenerationSubmitAttemptListDao,
	check GenerationSubmitServiceCheck,
) (*GenerationSubmit, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	return &GenerationSubmit{
		config: config, adapters: adapters, dao: submitDao, attemptDao: attemptDao, check: check,
	}, nil
}

func (service *GenerationSubmit) Exec(
	ctx context.Context, request *GenerationSubmitRequest,
) (*GenerationSubmitResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSubmit")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.owner_id", request.OwnerID.String()),
		attribute.String("generation.purpose", request.Purpose),
		attribute.String("generation.provider", request.Provider),
		attribute.Int("generation.payload_bytes", len(request.Payload)),
	)

	err := service.validateRequest(request)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	key, err := requestKey(request)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("derive request key: %w", err))
	}

	result, err := service.dao.Exec(ctx, &dao.GenerationSubmitRequest{
		// Minted here so the created and replayed cases can be told apart without a second
		// round-trip. uuidv7 keeps the table's index locality under insert churn.
		ID:          uuid.Must(uuid.NewV7()),
		OwnerID:     request.OwnerID,
		Purpose:     request.Purpose,
		Provider:    request.Provider,
		RequestKey:  key,
		Payload:     request.Payload,
		MaxAttempts: service.config.MaxAttempts,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("submit generation: %w", err))
	}

	span.SetAttributes(attribute.String("generation.id", result.Generation.ID.String()))

	generation := result.Generation

	if result.Created {
		generation, err = service.check.Exec(ctx, &GenerationCheckRequest{Generation: generation})
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("start generation: %w", err))
		}
	}

	attempts, err := service.attemptDao.Exec(ctx, &dao.GenerationAttemptListRequest{GenerationID: generation.ID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("list attempts: %w", err))
	}

	return &GenerationSubmitResult{
		Generation: newGeneration(generation, attempts),
		Created:    result.Created,
	}, nil
}

// validateRequest checks the envelope of a request, never its payload's content, which belongs to the
// caller and the provider.
func (service *GenerationSubmit) validateRequest(request *GenerationSubmitRequest) error {
	err := validate.Struct(request)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	// A NUL byte cannot be stored in a PostgreSQL text column.
	if strings.ContainsRune(request.Purpose, 0) {
		return fmt.Errorf("%w: purpose contains a NUL character", ErrInvalidRequest)
	}

	if size := len(request.Payload); size > PayloadSizeCeiling {
		return fmt.Errorf(
			"%w: payload contains %d bytes, limit is %d", ErrInvalidRequest, size, PayloadSizeCeiling,
		)
	}

	// I-JSON (RFC 7493) refuses duplicate names, invalid UTF-8 and unpaired surrogates: canonicalizing
	// any of them would give two different requests one key. A provider request is always an object.
	if !request.Payload.IsValid() || request.Payload.Kind() != '{' {
		return fmt.Errorf("%w: payload is not a valid I-JSON object (RFC 7493)", ErrInvalidRequest)
	}

	_, err = service.adapters.Async(request.Provider)
	if errors.Is(err, lib.ErrProviderUnknown) {
		return fmt.Errorf("%w: %w: %q", ErrInvalidRequest, err, request.Provider)
	}

	if err != nil {
		return fmt.Errorf("provider %q: %w", request.Provider, err)
	}

	return nil
}

// requestKeyTag versions what the request key covers. Changing the construction changes the tag, so
// a key of one version never matches a key of another.
const requestKeyTag = "a-novel/genai/request-key/v2"

// asyncMode names the Async mode in the request key, so the same payload submitted in another mode
// is another request.
const asyncMode = "async"

// requestKey identifies a request among an owner's generations. The same content always yields the
// same key, which is what lets a caller recover by resending: it holds no key of its own.
//
// Each field is length-prefixed, so no two different requests can concatenate to the same bytes, and
// the payload is canonicalized, so formatting or key order cannot split one request into two. The
// key never leaves the generation row: it fingerprints user content, and is purged with it.
func requestKey(request *GenerationSubmitRequest) ([]byte, error) {
	payload, err := canonicalJSON(request.Payload)
	if err != nil {
		return nil, fmt.Errorf("canonicalize payload: %w", err)
	}

	digest := sha256.New()

	for _, field := range [][]byte{
		[]byte(requestKeyTag),
		request.OwnerID[:],
		[]byte(request.Purpose),
		[]byte(request.Provider),
		[]byte(asyncMode),
		payload,
		binary.BigEndian.AppendUint32(nil, request.Variant),
	} {
		digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(field))))
		digest.Write(field)
	}

	return digest.Sum(nil), nil
}

// canonicalJSON re-encodes a JSON value with sorted object keys and no insignificant whitespace.
// Numbers keep their literal form.
func canonicalJSON(value json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()

	var decoded any

	err := decoder.Decode(&decoded)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	return encodeJSON(decoded)
}

// encodeJSON encodes without escaping <, > and &, so the canonical form keeps the caller's
// characters.
func encodeJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer

	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)

	err := encoder.Encode(value)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
