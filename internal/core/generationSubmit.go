package core

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/transaction"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

// ErrUsageCapReached is returned when the user already reached a limit the submit answers to. Its
// message names the limit.
var ErrUsageCapReached = errors.New("usage cap reached")

// GenerationSubmitDao records the generation.
type GenerationSubmitDao interface {
	Exec(ctx context.Context, request *dao.GenerationSubmitRequest) (*dao.GenerationSubmitResult, error)
}

// GenerationSubmitDaoAttemptList reads the attempts of a replayed generation.
type GenerationSubmitDaoAttemptList interface {
	Exec(ctx context.Context, request *dao.GenerationAttemptListRequest) ([]*dao.GenerationAttempt, error)
}

// GenerationSubmitDaoUsageSum adds up a user's usage over a window.
type GenerationSubmitDaoUsageSum interface {
	Exec(ctx context.Context, request *dao.GenerationUsageSumRequest) (*dao.GenerationUsageTotals, error)
}

// GenerationSubmitDaoCountLive counts a user's generations in flight.
type GenerationSubmitDaoCountLive interface {
	Exec(ctx context.Context, request *dao.GenerationCountLiveRequest) (*dao.GenerationLiveCount, error)
}

// GenerationSubmitDaoPayloadInsert stores the payload of a newly admitted generation.
type GenerationSubmitDaoPayloadInsert interface {
	Exec(ctx context.Context, request *dao.GenerationPayloadInsertRequest) (*dao.GenerationPayload, error)
}

// GenerationSubmitServiceCheck starts a newly recorded generation.
type GenerationSubmitServiceCheck interface {
	Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
}

// GenerationSubmitRequest holds the parameters for a [GenerationSubmit.Exec] call.
type GenerationSubmitRequest struct {
	// OwnerID is the user the generation acts for, supplied by a caller that already verified it.
	OwnerID uuid.UUID `validate:"required"`
	// Purpose is why the caller makes the call, up to 255 characters. It must be declared in the caps.
	Purpose string `validate:"required,notblank,max=255"`
	// UserTier selects the caps the user answers to. Empty or unrecognized, it selects the default.
	UserTier string `validate:"max=255"`
	// Provider names the service to run the call on. It must have a connector offering the Async
	// mode.
	Provider string `validate:"required,max=255"`
	// Payload is the provider's own request, built by the caller: a JSON object this service
	// forwards without reading.
	Payload json.RawMessage `validate:"required"`
	// Variant asks for another generation of a request that already succeeded.
	Variant uint32
	// Stream creates the provider call in streaming mode, so [GenerationStream] can read its events
	// while it runs. The provider must offer the Streaming mode.
	Stream bool
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
	dao              GenerationSubmitDao
	daoAttemptList   GenerationSubmitDaoAttemptList
	daoUsageSum      GenerationSubmitDaoUsageSum
	daoCountLive     GenerationSubmitDaoCountLive
	daoPayloadInsert GenerationSubmitDaoPayloadInsert
	serviceCheck     GenerationSubmitServiceCheck
	registry         connectors.Registry
	transactor       transaction.Transactor
	generations      config.Generations
	caps             config.Caps
}

// NewGenerationSubmit returns a new GenerationSubmit service.
func NewGenerationSubmit(
	dao GenerationSubmitDao,
	daoAttemptList GenerationSubmitDaoAttemptList,
	daoUsageSum GenerationSubmitDaoUsageSum,
	daoCountLive GenerationSubmitDaoCountLive,
	daoPayloadInsert GenerationSubmitDaoPayloadInsert,
	serviceCheck GenerationSubmitServiceCheck,
	registry connectors.Registry,
	transactor transaction.Transactor,
	generations config.Generations,
	caps config.Caps,
) *GenerationSubmit {
	return &GenerationSubmit{
		dao:              dao,
		daoAttemptList:   daoAttemptList,
		daoUsageSum:      daoUsageSum,
		daoCountLive:     daoCountLive,
		daoPayloadInsert: daoPayloadInsert,
		serviceCheck:     serviceCheck,
		registry:         registry,
		transactor:       transactor,
		generations:      generations,
		caps:             caps,
	}
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

	// A check may run the generation later, outside this request; it links back through this.
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)

	var (
		result  *dao.GenerationSubmitResult
		refusal error
	)

	// Recording and admission commit together, so a generation is never recorded unless admitted:
	// nothing can start one that was refused, or left behind by a failure.
	err = service.transactor.WithinTx(ctx, func(ctx context.Context) error {
		var err error

		result, err = service.dao.Exec(ctx, &dao.GenerationSubmitRequest{
			// Minted here so the created and replayed cases can be told apart without a second
			// round-trip. uuidv7 keeps the table's index locality under insert churn.
			ID:          uuid.Must(uuid.NewV7()),
			OwnerID:     request.OwnerID,
			Purpose:     request.Purpose,
			Provider:    request.Provider,
			RequestKey:  key,
			MaxAttempts: service.generations.MaxAttempts,
			Traceparent: lo.EmptyableToPtr(carrier.Get("traceparent")),
			Streamed:    request.Stream,
		})
		if err != nil {
			return fmt.Errorf("submit generation: %w", err)
		}

		if !result.Created {
			return nil
		}

		refusal = service.admit(ctx, request)
		if refusal != nil {
			return refusal
		}

		// Stored only once admitted, so neither a replay nor a refusal sends the payload over.
		_, err = service.daoPayloadInsert.Exec(ctx, &dao.GenerationPayloadInsertRequest{
			GenerationID: result.Generation.ID, Payload: request.Payload,
		})
		if err != nil {
			return fmt.Errorf("store payload: %w", err)
		}

		return nil
	})
	// The caller reads a refusal's message to tell its user which limit was reached: it goes out as
	// admission wrote it, without the transaction's wrapping.
	if errors.Is(refusal, ErrUsageCapReached) {
		return nil, otel.ReportError(span, refusal)
	}

	if err != nil {
		return nil, otel.ReportError(span, err)
	}

	span.SetAttributes(attribute.String("generation.id", result.Generation.ID.String()))

	generation := result.Generation

	// The generation is admitted and recorded, so a start that failed, such as on a lock a migration
	// holds, is redone by the next poll or the sweep: the caller gets the generation to follow either
	// way. The check's own span holds the failure.
	if result.Created {
		checked, checkErr := service.serviceCheck.Exec(ctx, &GenerationCheckRequest{Generation: generation})
		if checkErr == nil {
			generation = checked
		}
	}

	var attempts []*dao.GenerationAttempt

	if mayHaveAttempts(generation) {
		attempts, err = service.daoAttemptList.Exec(ctx, &dao.GenerationAttemptListRequest{GenerationID: generation.ID})
		if err != nil {
			return nil, otel.ReportError(span, fmt.Errorf("list attempts: %w", err))
		}
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
		return errors.Join(err, ErrInvalidRequest)
	}

	if _, declared := service.caps.Purposes[request.Purpose]; !declared {
		return fmt.Errorf("%w: purpose %q is not declared in the caps", ErrInvalidRequest, request.Purpose)
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

	_, err = service.registry.Async(request.Provider)
	if err == nil && request.Stream {
		_, err = service.registry.Streaming(request.Provider)
	}

	if errors.Is(err, connectors.ErrProviderUnknown) {
		return fmt.Errorf("%w: %w: %q", ErrInvalidRequest, err, request.Provider)
	}

	if err != nil {
		return fmt.Errorf("provider %q: %w", request.Provider, err)
	}

	return nil
}

// admit refuses a newly created generation whose user already reached a limit it answers to: its
// purpose's, then the global one, each counting live generations before summing usage. Nothing is
// reserved, so a call admitted under a cap runs to the end past it; the concurrency limits bound how
// many can.
//
// A generation whose call may still run untracked counts toward its purpose's concurrency only, so
// an uncertain call never holds back the user's other purposes.
func (service *GenerationSubmit) admit(ctx context.Context, request *GenerationSubmitRequest) error {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSubmit(admit)")
	defer span.End()

	scopes := []struct {
		name        string
		purpose     *string
		capOfTier   config.Cap
		unknownHold time.Duration
	}{
		{
			name:        fmt.Sprintf("purpose %q", request.Purpose),
			purpose:     &request.Purpose,
			capOfTier:   service.caps.Purposes[request.Purpose].For(request.UserTier),
			unknownHold: config.UnknownOutcomeHold,
		},
		{name: "global", capOfTier: service.caps.Global.For(request.UserTier)},
	}

	for _, scope := range scopes {
		if scope.capOfTier.Forbidden {
			return otel.ReportError(span, fmt.Errorf("%w: %s allows no usage", ErrUsageCapReached, scope.name))
		}

		if limit := scope.capOfTier.Concurrency; limit != nil && *limit >= 0 {
			live, err := service.daoCountLive.Exec(ctx, &dao.GenerationCountLiveRequest{
				OwnerID: request.OwnerID, Purpose: scope.purpose, UnknownOutcomeWindow: scope.unknownHold,
			})
			if err != nil {
				return otel.ReportError(span, fmt.Errorf("count live generations: %w", err))
			}

			// The count includes the generation this submit just created.
			if live.Count > int64(*limit) {
				return otel.ReportError(span, fmt.Errorf(
					"%w: %s concurrency: %d generations already in flight",
					ErrUsageCapReached, scope.name, live.Count-1,
				))
			}
		}

		for _, window := range slices.Sorted(maps.Keys(scope.capOfTier.Windows)) {
			limit := scope.capOfTier.Windows[window]

			used, err := service.daoUsageSum.Exec(ctx, &dao.GenerationUsageSumRequest{
				OwnerID: request.OwnerID, Purpose: scope.purpose, Window: window,
			})
			if err != nil {
				return otel.ReportError(span, fmt.Errorf("sum usage: %w", err))
			}

			if reached(limit.InputTokens, used.InputTokens) || reached(limit.OutputTokens, used.OutputTokens) {
				return otel.ReportError(span, fmt.Errorf("%w: %s over %s", ErrUsageCapReached, scope.name, window))
			}
		}
	}

	return nil
}

func reached(limit *int64, used int64) bool {
	return limit != nil && used >= *limit
}

// requestKeyTag versions what the request key covers. Changing the construction changes the tag, so
// a key of one version never matches a key of another. A resend then no longer finds a generation
// submitted under the previous tag: a release that changes it pays again for those still running.
const requestKeyTag = "a-novel/genai/request-key/v4"

// asyncMode and streamedMode name the execution mode in the request key, so the same payload
// submitted in another mode is another request.
const (
	asyncMode    = "async"
	streamedMode = "async-streamed"
)

// requestKey identifies a request among an owner's generations. The same content always yields the
// same key, which is what lets a caller recover by resending: it holds no key of its own.
//
// Each field is length-prefixed, so no two different requests can concatenate to the same bytes, and
// the payload is canonicalized as it is sent, so formatting cannot split one request into two. The
// key never leaves the generation row: it fingerprints user content, and is purged with it.
func requestKey(request *GenerationSubmitRequest) ([]byte, error) {
	payload, err := canonicalPayload(request.Payload)
	if err != nil {
		return nil, fmt.Errorf("canonicalize payload: %w", err)
	}

	digest := sha256.New()

	for _, field := range [][]byte{
		[]byte(requestKeyTag),
		request.OwnerID[:],
		[]byte(request.Purpose),
		[]byte(request.Provider),
		[]byte(lo.Ternary(request.Stream, streamedMode, asyncMode)),
		payload,
		binary.BigEndian.AppendUint32(nil, request.Variant),
	} {
		digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(field))))
		digest.Write(field)
	}

	return digest.Sum(nil), nil
}

// canonicalPayload writes a payload as the provider reads it: its top-level members sorted, as the
// connector sends them, and nested members in the caller's order, which a provider can read meaning
// into, such as a schema's property order. Insignificant whitespace goes and strings are written one
// way; numbers keep their literal form.
func canonicalPayload(payload json.RawMessage) ([]byte, error) {
	var members map[string]jsontext.Value

	err := json.Unmarshal(payload, &members)
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}

	for name, value := range members {
		err = value.Format()
		if err != nil {
			return nil, fmt.Errorf("format %q: %w", name, err)
		}

		members[name] = value
	}

	canonical, err := json.Marshal(members)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}

	return canonical, nil
}
