package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// eventStream is the transport double a stream handler sends to.
type eventStream struct {
	grpc.ServerStream

	// left cancels the stream's context, as a caller closing the stream does.
	left bool
	sent []*genaiv0.GenerationStreamResponse
}

func (stream *eventStream) Context() context.Context {
	if !stream.left {
		return context.Background()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx
}

func (stream *eventStream) Send(response *genaiv0.GenerationStreamResponse) error {
	stream.sent = append(stream.sent, response)

	return nil
}

// eventsOf yields the given events, then the error when there is one, as the service streams them.
func eventsOf(err error, sent ...*core.GenerationStreamEvent) iter.Seq2[*core.GenerationStreamEvent, error] {
	return func(yield func(*core.GenerationStreamEvent, error) bool) {
		for _, event := range sent {
			if !yield(event, nil) {
				return
			}
		}

		if err != nil {
			yield(nil, err)
		}
	}
}

func TestGrpcGenerationStream(t *testing.T) {
	t.Parallel()

	delta := json.RawMessage(`{"type":"response.output_text.delta","sequence_number":3}`)

	type serviceMock struct {
		resp *core.GenerationStreamResult
		err  error
	}

	testCases := []struct {
		name string

		request *genaiv0.GenerationStreamRequest

		serviceMock *serviceMock
		callerLeft  bool

		expect       []*genaiv0.GenerationStreamResponse
		expectStatus codes.Code
	}{
		{
			name: "Success",

			request: &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID, StartingAfter: new(int64(2))},
			serviceMock: &serviceMock{resp: &core.GenerationStreamResult{
				Events: eventsOf(nil, &core.GenerationStreamEvent{Sequence: 3, Data: delta}),
			}},

			expect: []*genaiv0.GenerationStreamResponse{{SequenceNumber: 3, Event: delta}},
		},
		{
			// The provider dropped the stream: the caller resumes after the last event it received.
			name: "Error/Interrupted",

			request: &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{resp: &core.GenerationStreamResult{
				Events: eventsOf(core.ErrProviderRetryable, &core.GenerationStreamEvent{Sequence: 3, Data: delta}),
			}},

			expect:       []*genaiv0.GenerationStreamResponse{{SequenceNumber: 3, Event: delta}},
			expectStatus: codes.Unavailable,
		},
		{
			// Leaving cuts the provider's stream too, which is no server fault.
			name: "Error/CallerLeft",

			request: &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{resp: &core.GenerationStreamResult{
				Events: eventsOf(fmt.Errorf("%w: %w", core.ErrProviderRetryable, context.Canceled)),
			}},
			callerLeft: true,

			expectStatus: codes.Canceled,
		},
		{
			// Past the stream window, the caller polls for the result.
			name: "Error/StreamUnavailable",

			request: &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{resp: &core.GenerationStreamResult{
				Events: eventsOf(core.ErrStreamUnavailable),
			}},

			expectStatus: codes.FailedPrecondition,
		},
		{
			name: "Error/NotStreamed",

			request:     &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: core.ErrGenerationNotStreamed},

			expectStatus: codes.FailedPrecondition,
		},
		{
			// Another owner's generation reports exactly this, so an id cannot be probed for existence.
			name: "Error/NotFound",

			request:     &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: core.ErrGenerationNotFound},

			expectStatus: codes.NotFound,
		},
		{
			name: "Error/InvalidGenerationID",

			request: &genaiv0.GenerationStreamRequest{Id: "not-a-uuid", OwnerId: testOwnerID},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/InvalidOwnerID",

			request: &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: "not-a-uuid"},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/InvalidRequest",

			request:     &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: core.ErrInvalidRequest},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/ModeUnsupported",

			request:     &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: fmt.Errorf("provider %q: %w", "openai", core.ErrModeUnsupported)},

			expectStatus: codes.Unimplemented,
		},
		{
			name: "Error/Internal",

			request:     &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: errFoo},

			expectStatus: codes.Internal,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockGrpcGenerationStreamService(t)

			if testCase.serviceMock != nil {
				service.EXPECT().
					Exec(mock.Anything, &core.GenerationStreamRequest{
						ID:            uuid.MustParse(testCase.request.GetId()),
						OwnerID:       uuid.MustParse(testCase.request.GetOwnerId()),
						StartingAfter: testCase.request.StartingAfter,
					}).
					Return(testCase.serviceMock.resp, testCase.serviceMock.err)
			}

			stream := &eventStream{left: testCase.callerLeft}

			err := handlers.NewGrpcGenerationStream(service).GenerationStream(testCase.request, stream)
			resSt, ok := status.FromError(err)
			require.True(t, ok, resSt.Code().String())
			require.Equal(
				t,
				testCase.expectStatus, resSt.Code(),
				"expected status code %s, got %s (%v)", testCase.expectStatus, resSt.Code(), err,
			)
			require.Equal(t, testCase.expect, stream.sent)

			service.AssertExpectations(t)
		})
	}
}
