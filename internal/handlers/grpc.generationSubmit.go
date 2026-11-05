package handlers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// GrpcMaxReceiveBytes admits a submit whose payload is at [core.PayloadSizeCeiling], with room for
// its envelope, so an oversized payload reaches core validation rather than gRPC's receive limit.
const GrpcMaxReceiveBytes = core.PayloadSizeCeiling + 64<<10

// GrpcGenerationSubmitService is the service dependency of [GrpcGenerationSubmit].
type GrpcGenerationSubmitService interface {
	Exec(ctx context.Context, request *core.GenerationSubmitRequest) (*core.GenerationSubmitResult, error)
}

// GrpcGenerationSubmit is the gRPC handler for the GenerationSubmit RPC.
type GrpcGenerationSubmit struct {
	genaiv0.UnimplementedGenerationSubmitServiceServer

	service GrpcGenerationSubmitService
}

// NewGrpcGenerationSubmit returns a new GrpcGenerationSubmit handler backed by the given service.
func NewGrpcGenerationSubmit(service GrpcGenerationSubmitService) *GrpcGenerationSubmit {
	return &GrpcGenerationSubmit{service: service}
}

// GenerationSubmit records a generation, or finds the one the same request already recorded, and
// returns it.
func (handler *GrpcGenerationSubmit) GenerationSubmit(
	ctx context.Context, request *genaiv0.GenerationSubmitRequest,
) (*genaiv0.GenerationSubmitResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.GenerationSubmit")
	defer span.End()

	ownerID, err := uuid.Parse(request.GetOwnerId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid owner id")
	}

	serviceRequest := &core.GenerationSubmitRequest{
		OwnerID:  ownerID,
		Purpose:  request.GetPurpose(),
		UserTier: request.GetUserTier(),
		Provider: request.GetProvider(),
		Payload:  request.GetPayload(),
		Variant:  request.GetVariant(),
		Stream:   request.GetStream(),
		Caps:     newCoreGenerationCaps(request.GetCaps()),
	}

	result, err := handler.service.Exec(ctx, serviceRequest)

	// The reason names the caller's own fields and limits, so it tells the caller what to fix.
	if errors.Is(err, core.ErrInvalidRequest) {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// The message names the cap, so the caller can tell its user which limit was reached. The details
	// say it in fields a caller reads without parsing the message.
	var refusal *core.UsageCapError
	if errors.As(err, &refusal) {
		return nil, newGrpcUsageCapStatus(refusal)
	}

	if errors.Is(err, core.ErrUsageCapReached) {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}

	if errors.Is(err, core.ErrModeUnsupported) {
		return nil, status.Error(codes.Unimplemented, "the provider does not support the requested mode")
	}

	if err != nil {
		return nil, grpcf.Fault(codes.Internal, "internal error", otel.ReportError(span, err))
	}

	return &genaiv0.GenerationSubmitResponse{
		Generation: NewGrpcGeneration(result.Generation),
		Created:    result.Created,
	}, nil
}

// newCoreGenerationCaps converts the caps a caller sent to their core form. Absent caps stay nil, so
// the submit answers to the caps file. Core validation refuses a window without a duration.
func newCoreGenerationCaps(caps *genaiv0.GenerationCaps) *core.GenerationCaps {
	if caps == nil {
		return nil
	}

	converted := &core.GenerationCaps{Windows: make([]core.GenerationCapWindow, len(caps.GetWindows()))}

	if caps.Concurrency != nil {
		converted.Concurrency = lo.ToPtr(int(caps.GetConcurrency()))
	}

	for index, window := range caps.GetWindows() {
		converted.Windows[index] = core.GenerationCapWindow{
			Duration:     window.GetDuration().AsDuration(),
			InputTokens:  window.InputTokens,
			OutputTokens: window.OutputTokens,
		}
	}

	return converted
}

// newGrpcUsageCapStatus answers a refusal with RESOURCE_EXHAUSTED and its message, a
// google.rpc.QuotaFailure naming the scope and the limit that refused, and a google.rpc.RetryInfo when
// the refusing window frees usage at a known time.
func newGrpcUsageCapStatus(refusal *core.UsageCapError) error {
	details := []protoadapt.MessageV1{&errdetails.QuotaFailure{
		Violations: []*errdetails.QuotaFailure_Violation{{Subject: refusal.Scope, Description: refusal.Limit}},
	}}

	if refusal.RetryAfter > 0 {
		details = append(details, &errdetails.RetryInfo{RetryDelay: durationpb.New(refusal.RetryAfter)})
	}

	refused := status.New(codes.ResourceExhausted, refusal.Error())

	// The details only fail to marshal on a bug: the caller still reads the limit in the message.
	detailed, err := refused.WithDetails(details...)
	if err != nil {
		return refused.Err()
	}

	return detailed.Err()
}
