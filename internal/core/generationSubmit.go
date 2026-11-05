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
	"strings"
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

// ErrUsageCapReached is returned when the user already reached a limit the submit answers to. A
// [UsageCapError] wraps it, and its message names the limit.
var ErrUsageCapReached = errors.New("usage cap reached")

// A UsageCapError refuses a submit past one of the limits it answers to. It wraps [ErrUsageCapReached].
type UsageCapError struct {
	// Scope names the limits that refused: `purpose "<name>"`, or `global` for a submit without caps.
	Scope string
	// Limit names the limit that refused, such as `concurrency 2` or `input tokens over 5h`.
	Limit string
	// RetryAfter is how long until the refusing window frees usage: its oldest Usage Record then leaves
	// it. Zero when no known time frees it, as for a concurrency.
	RetryAfter time.Duration

	// Reason words the refusal after the sentinel's message, as admission writes it.
	Reason string
}

// Error names the scope and the limit that refused.
func (err *UsageCapError) Error() string {
	return ErrUsageCapReached.Error() + ": " + err.Reason
}

// Unwrap returns [ErrUsageCapReached].
func (err *UsageCapError) Unwrap() error {
	return ErrUsageCapReached
}

// GenerationSubmitDao records the generation.
type GenerationSubmitDao interface {
	Exec(ctx context.Context, request *dao.GenerationSubmitRequest) (*dao.GenerationSubmitResult, error)
}

// GenerationSubmitDaoAttemptList reads the attempts of a replayed generation.
type GenerationSubmitDaoAttemptList interface {
	Exec(ctx context.Context, request *dao.GenerationAttemptListRequest) ([]*dao.GenerationAttempt, error)
}

// GenerationSubmitDaoUsageSum adds up a user's usage over each window of a cap.
type GenerationSubmitDaoUsageSum interface {
	Exec(ctx context.Context, request *dao.GenerationUsageSumRequest) ([]*dao.GenerationUsageTotals, error)
}

// GenerationSubmitDaoCountLive counts a user's generations in flight.
type GenerationSubmitDaoCountLive interface {
	Exec(ctx context.Context, request *dao.GenerationCountLiveRequest) (*dao.GenerationLiveCount, error)
}

// GenerationSubmitServiceCheck sends the provider call of a newly recorded generation.
type GenerationSubmitServiceCheck interface {
	Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
}

// GenerationSubmitRequest holds the parameters for a [GenerationSubmit.Exec] call.
type GenerationSubmitRequest struct {
	// OwnerID is the user the generation acts for, supplied by a caller that already verified it.
	OwnerID uuid.UUID `validate:"required"`
	// Purpose is why the caller makes the call, up to 255 characters. It must be declared in the caps.
	Purpose string `validate:"required,notblank,max=255"`
	// UserTier selects the caps file's tier the user answers to, without Caps. Empty or unrecognized, it
	// selects the default.
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
	// Caps are the limits the caller sets for its user on the purpose. Set, they alone limit the
	// submit: the caller owns every limit, and UserTier is ignored. Nil answers to the caps file.
	Caps *GenerationCaps
}

// GenerationCaps limits one user's generations of a purpose, as the caller sets them.
type GenerationCaps struct {
	// Concurrency caps the generations of the purpose the user may have in flight at once, the
	// submitted one included. Nil sets no limit.
	Concurrency *int `validate:"omitnil,gte=0"`
	// Windows cap the tokens the user consumed over rolling windows, one window per duration. Empty sets
	// no limit.
	Windows []GenerationCapWindow `validate:"max=10,unique=Duration,dive"`
}

// GenerationCapWindow caps the tokens one user consumed over a rolling window.
type GenerationCapWindow struct {
	// Duration is how far back the window reaches, up to a year.
	Duration time.Duration `validate:"gt=0,lte=8760h"`
	// InputTokens caps the input tokens, cached ones included. Nil sets no limit.
	InputTokens *int64 `validate:"omitnil,gte=0"`
	// OutputTokens caps the output tokens, reasoning ones included. Nil sets no limit.
	OutputTokens *int64 `validate:"omitnil,gte=0"`
}

// GenerationSubmitResult reports the stored generation and how it got there.
type GenerationSubmitResult struct {
	// Generation is the recorded generation: the new one, or the existing one on a replay.
	Generation *Generation
	// Created is false on a replay, so a resending caller attaches to work already paid for.
	Created bool
}

// A GenerationSubmit records a generation and sends its provider call in the Async mode.
//
// The generation is identified by a key derived from the request and the owner, so a caller that
// lost track of it recovers by resending the same request. A replay returns the recorded generation
// as it stands, without checking it: the caller polls it next, and the poll checks it. A failed or
// cancelled generation does not count, so resending its request runs it again.
//
// The submit holds the payload in memory and sends it itself, once the generation is recorded: the
// payload is never stored. A call fails on its first outcome, and the caller decides whether to
// resend.
type GenerationSubmit struct {
	dao            GenerationSubmitDao
	daoAttemptList GenerationSubmitDaoAttemptList
	daoUsageSum    GenerationSubmitDaoUsageSum
	daoCountLive   GenerationSubmitDaoCountLive
	serviceCheck   GenerationSubmitServiceCheck
	registry       connectors.Registry
	transactor     transaction.Transactor
	caps           config.Caps
}

// NewGenerationSubmit returns a new GenerationSubmit service.
func NewGenerationSubmit(
	dao GenerationSubmitDao,
	daoAttemptList GenerationSubmitDaoAttemptList,
	daoUsageSum GenerationSubmitDaoUsageSum,
	daoCountLive GenerationSubmitDaoCountLive,
	serviceCheck GenerationSubmitServiceCheck,
	registry connectors.Registry,
	transactor transaction.Transactor,
	caps config.Caps,
) *GenerationSubmit {
	return &GenerationSubmit{
		dao:            dao,
		daoAttemptList: daoAttemptList,
		daoUsageSum:    daoUsageSum,
		daoCountLive:   daoCountLive,
		serviceCheck:   serviceCheck,
		registry:       registry,
		transactor:     transactor,
		caps:           caps,
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

	submission := &dao.GenerationSubmitRequest{
		// Minted here so the created and replayed cases can be told apart without a second round-trip.
		// uuidv7 keeps the table's index locality under insert churn.
		ID:          uuid.Must(uuid.NewV7()),
		OwnerID:     request.OwnerID,
		Purpose:     request.Purpose,
		Provider:    request.Provider,
		RequestKey:  key,
		Traceparent: lo.EmptyableToPtr(carrier.Get("traceparent")),
		Streamed:    request.Stream,
	}

	var (
		result  *dao.GenerationSubmitResult
		refusal error
	)

	// Taken before the start intent is recorded, so it can only overstate the intent's age.
	intentAt := time.Now()

	// Recording and admission commit together, so a generation is never recorded unless admitted:
	// nothing sends one that was refused, or left behind by a failure.
	err = service.transactor.WithinTx(ctx, func(ctx context.Context) error {
		var err error

		result, err = service.dao.Exec(ctx, submission)
		if err != nil {
			return fmt.Errorf("submit generation: %w", err)
		}

		if !result.Created {
			return nil
		}

		refusal = service.admit(ctx, request)

		return refusal
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

	// The generation is admitted and recorded, so its call goes out now, outside the transaction. A
	// replay sends nothing: its generation had its own start. A check that failed leaves the start
	// intent for a later check to settle, so the caller gets the generation to follow either way. The
	// check's own span holds the failure.
	if result.Created {
		checked, checkErr := service.serviceCheck.Exec(ctx, &GenerationCheckRequest{
			Generation: generation, Start: &GenerationCheckStart{Payload: request.Payload, IntentAt: intentAt},
		})
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

	// Canonicalizing collapses duplicate names, invalid UTF-8 and unpaired surrogates, so two different
	// requests holding them could share a key. I-JSON (RFC 7493) refuses all three. A provider request
	// is always an object.
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

// admissionScope is one set of limits a submit answers to.
type admissionScope struct {
	// name says which limits refused: `purpose "<name>"` or `global`.
	name string
	// purpose restricts the scope to one purpose; nil covers every purpose.
	purpose *string
	limits  config.Cap
	// unknownHold is how long a generation failed with its outcome unknown still counts toward the
	// concurrency.
	unknownHold time.Duration
}

// admissionScopes lists the limits a submit answers to. With caps, the caller owns every limit: its
// caps alone limit the purpose. Without, the caps file's tier limits the purpose, then every purpose.
//
// A generation whose call may still run untracked counts toward its purpose's concurrency only, so
// an uncertain call never holds back the user's other purposes.
func (service *GenerationSubmit) admissionScopes(request *GenerationSubmitRequest) []admissionScope {
	purpose := admissionScope{
		name:        fmt.Sprintf("purpose %q", request.Purpose),
		purpose:     &request.Purpose,
		unknownHold: config.UnknownOutcomeHold,
	}

	if request.Caps == nil {
		purpose.limits = service.caps.Purposes[request.Purpose].For(request.UserTier)

		return []admissionScope{purpose, {name: "global", limits: service.caps.Global.For(request.UserTier)}}
	}

	purpose.limits = config.Cap{
		Concurrency: request.Caps.Concurrency,
		Windows:     make(map[time.Duration]config.Limit, len(request.Caps.Windows)),
	}

	for _, window := range request.Caps.Windows {
		purpose.limits.Windows[window.Duration] = config.Limit{
			InputTokens: window.InputTokens, OutputTokens: window.OutputTokens,
		}
	}

	return []admissionScope{purpose}
}

// admit refuses a newly created generation whose user already reached one of its limits. It checks
// each scope in turn, and counts live generations before it sums usage. Nothing is reserved: a call
// admitted under a cap runs to the end past it. The concurrency limits bound how many can.
func (service *GenerationSubmit) admit(ctx context.Context, request *GenerationSubmitRequest) error {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationSubmit(admit)")
	defer span.End()

	for _, scope := range service.admissionScopes(request) {
		if scope.limits.Forbidden {
			return otel.ReportError(span, &UsageCapError{
				Scope: scope.name, Limit: "no usage", Reason: scope.name + " allows no usage",
			})
		}

		if limit := scope.limits.Concurrency; limit != nil && *limit >= 0 {
			live, err := service.daoCountLive.Exec(ctx, &dao.GenerationCountLiveRequest{
				OwnerID: request.OwnerID, Purpose: scope.purpose, UnknownOutcomeWindow: scope.unknownHold,
			})
			if err != nil {
				return otel.ReportError(span, fmt.Errorf("count live generations: %w", err))
			}

			// The count includes the generation this submit just created.
			if live.Count > int64(*limit) {
				return otel.ReportError(span, &UsageCapError{
					Scope:  scope.name,
					Limit:  fmt.Sprintf("concurrency %d", *limit),
					Reason: fmt.Sprintf("%s concurrency: %d generations already in flight", scope.name, live.Count-1),
				})
			}
		}

		windows := slices.Sorted(maps.Keys(scope.limits.Windows))
		if len(windows) == 0 {
			continue
		}

		used, err := service.daoUsageSum.Exec(ctx, &dao.GenerationUsageSumRequest{
			OwnerID: request.OwnerID, Purpose: scope.purpose, Windows: windows,
		})
		if err != nil {
			return otel.ReportError(span, fmt.Errorf("sum usage: %w", err))
		}

		for index, window := range windows {
			refusal := windowRefusal(scope.name, window, scope.limits.Windows[window], used[index])
			if refusal != nil {
				return otel.ReportError(span, refusal)
			}
		}
	}

	return nil
}

// windowRefusal refuses a scope's window whose usage reached one of its limits, input first. It
// returns nil when the window has room left.
func windowRefusal(
	scope string, window time.Duration, limit config.Limit, used *dao.GenerationUsageTotals,
) *UsageCapError {
	var tokens string

	switch {
	case reached(limit.InputTokens, used.InputTokens):
		tokens = "input"
	case reached(limit.OutputTokens, used.OutputTokens):
		tokens = "output"
	default:
		return nil
	}

	refusal := &UsageCapError{
		Scope:  scope,
		Limit:  fmt.Sprintf("%s tokens over %s", tokens, formatWindow(window)),
		Reason: fmt.Sprintf("%s over %s", scope, window),
	}

	// The window frees usage once its oldest record leaves it.
	if used.OldestAt != nil {
		refusal.RetryAfter = max(time.Until(used.OldestAt.Add(window)), 0)
	}

	return refusal
}

// reached reports whether the usage met the limit. A nil limit is never reached.
func reached(limit *int64, used int64) bool {
	return limit != nil && used >= *limit
}

// formatWindow writes a window as the caps file does, such as 5h or 90m, without the zero minutes and
// seconds [time.Duration.String] adds.
func formatWindow(window time.Duration) string {
	written := window.String()

	if strings.HasSuffix(written, "m0s") {
		written = strings.TrimSuffix(written, "0s")
	}

	if strings.HasSuffix(written, "h0m") {
		written = strings.TrimSuffix(written, "0m")
	}

	return written
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
// Each field is length-prefixed, so no two different requests can concatenate to the same bytes. The
// payload is canonicalized as it is sent, so formatting cannot split one request into two. The key
// never leaves the generation row: it fingerprints user content, and is purged with it.
func requestKey(request *GenerationSubmitRequest) ([]byte, error) {
	payload, err := canonicalPayload(request.Payload)
	if err != nil {
		return nil, fmt.Errorf("canonicalize payload: %w", err)
	}

	fields := [][]byte{
		[]byte(requestKeyTag),
		request.OwnerID[:],
		[]byte(request.Purpose),
		[]byte(request.Provider),
		[]byte(lo.Ternary(request.Stream, streamedMode, asyncMode)),
		payload,
		binary.BigEndian.AppendUint32(nil, request.Variant),
	}

	digest := sha256.New()

	// One buffer holds each field's length prefix in turn.
	var length [8]byte

	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		digest.Write(length[:])
		digest.Write(field)
	}

	return digest.Sum(nil), nil
}

// canonicalPayload writes a payload as the provider reads it. Top-level members are sorted, as the
// connector sends them. Nested members keep the caller's order, since a provider can read meaning
// into it, such as a schema's property order. Insignificant whitespace goes, strings are written one
// way, and numbers keep their literal form.
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
