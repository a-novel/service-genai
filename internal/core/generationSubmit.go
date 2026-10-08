package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
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
	// GenerationSubmitUsageListDao reads what a replayed generation consumed.
	GenerationSubmitUsageListDao interface {
		Exec(ctx context.Context, request *dao.GenerationUsageListRequest) ([]*dao.GenerationUsage, error)
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
	// Purpose is what the caller attributes this spend to, up to 255 characters. Free-form: the
	// vocabulary belongs to the caller, and this service only groups by it.
	Purpose string `validate:"required,notblank,max=255"`
	// Tier is the level of model capability wanted.
	Tier lib.Tier `validate:"required,oneof=fast balanced deep"`
	// Instructions are the trusted channel.
	Instructions string `validate:"required,notblank"`
	// Input is the untrusted channel: any JSON value.
	Input json.RawMessage `validate:"required"`
	// OutputSchema is the JSON Schema the output conforms to: a JSON object.
	OutputSchema json.RawMessage `validate:"required"`
	// Variant asks for another generation of a request that already succeeded.
	Variant uint32
}

// GenerationSubmitResult reports the stored generation and how it got there.
type GenerationSubmitResult struct {
	Generation *Generation
	// Created is false on a replay, so a resending caller attaches to work already paid for.
	Created bool
}

// A GenerationSubmit records a generation and starts its provider call.
//
// The generation is identified by a key derived from the request and the owner, so a caller that
// lost track of it recovers by resending the same request. A replay returns the recorded generation
// as it stands, without checking it: the caller polls it next, and the poll checks it. A failed or
// cancelled generation does not count, so resending its request runs it again.
type GenerationSubmit struct {
	config   GenerationSubmitConfig
	dao      GenerationSubmitDao
	usageDao GenerationSubmitUsageListDao
	check    GenerationSubmitServiceCheck
}

func NewGenerationSubmit(
	config GenerationSubmitConfig,
	submitDao GenerationSubmitDao,
	usageDao GenerationSubmitUsageListDao,
	check GenerationSubmitServiceCheck,
) (*GenerationSubmit, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	return &GenerationSubmit{config: config, dao: submitDao, usageDao: usageDao, check: check}, nil
}

func (service *GenerationSubmit) Exec(
	ctx context.Context, request *GenerationSubmitRequest,
) (*GenerationSubmitResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSubmit")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.owner_id", request.OwnerID.String()),
		attribute.String("generation.purpose", request.Purpose),
		attribute.String("generation.tier", string(request.Tier)),
		attribute.Int("generation.request_bytes", requestBytes(request)),
	)

	err := validateSubmit(request)
	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	stored, err := encodeJSON(&generationRequest{
		Tier:         request.Tier,
		Instructions: request.Instructions,
		Input:        request.Input,
		OutputSchema: request.OutputSchema,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("encode request: %w", err))
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
		RequestKey:  key,
		Request:     stored,
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

	usage, err := service.usageDao.Exec(ctx, &dao.GenerationUsageListRequest{GenerationID: generation.ID})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("list usage: %w", err))
	}

	return &GenerationSubmitResult{
		Generation: newGeneration(generation, usage),
		Created:    result.Created,
	}, nil
}

func requestBytes(request *GenerationSubmitRequest) int {
	return len(request.Instructions) + len(request.Input) + len(request.OutputSchema)
}

func validateSubmit(request *GenerationSubmitRequest) error {
	err := validate.Struct(request)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	if size := requestBytes(request); size > RequestSizeCeiling {
		return fmt.Errorf(
			"%w: request contains %d bytes, limit is %d", ErrInvalidRequest, size, RequestSizeCeiling,
		)
	}

	// A NUL byte cannot be stored in a PostgreSQL text column.
	if strings.ContainsRune(request.Purpose, 0) {
		return fmt.Errorf("%w: purpose contains a NUL character", ErrInvalidRequest)
	}

	// I-JSON (RFC 7493) refuses duplicate names, invalid UTF-8 and unpaired surrogates: canonicalizing
	// any of them would give two different requests one key.
	if !request.Input.IsValid() {
		return fmt.Errorf("%w: input is not valid I-JSON (RFC 7493)", ErrInvalidRequest)
	}

	// The schema's content is the provider's to judge; its shape is not. A strict schema's root is
	// always an object.
	if !request.OutputSchema.IsValid() || request.OutputSchema.Kind() != '{' {
		return fmt.Errorf("%w: output schema is not a valid I-JSON object (RFC 7493)", ErrInvalidRequest)
	}

	return nil
}

// requestKeyTag versions what the request key covers. Changing the construction changes the tag, so
// a key of one version never matches a key of another.
const requestKeyTag = "a-novel/genai/request-key/v1"

// requestKey identifies a request among an owner's generations. The same content always yields the
// same key, which is what lets a caller recover by resending: it holds no key of its own.
//
// Each field is length-prefixed, so no two different requests can concatenate to the same bytes, and
// JSON is canonicalized, so formatting or key order cannot split one request into two. The key never
// leaves the generation row: it fingerprints user content, and is purged with it.
func requestKey(request *GenerationSubmitRequest) ([]byte, error) {
	input, err := canonicalJSON(request.Input)
	if err != nil {
		return nil, fmt.Errorf("canonicalize input: %w", err)
	}

	schema, err := canonicalJSON(request.OutputSchema)
	if err != nil {
		return nil, fmt.Errorf("canonicalize output schema: %w", err)
	}

	digest := sha256.New()

	for _, field := range [][]byte{
		[]byte(requestKeyTag),
		request.OwnerID[:],
		[]byte(request.Purpose),
		[]byte(request.Tier),
		[]byte(request.Instructions),
		input,
		schema,
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

// encodeJSON encodes without escaping <, > and &. json.Marshal would escape them, inside a caller's
// raw input too, and the model would then read \u0026 where the user wrote &.
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
