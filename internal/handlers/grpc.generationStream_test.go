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

	sent []*genaiv0.GenerationStreamResponse
}

func (stream *eventStream) Context() context.Context {
	return context.Background()
}

func (stream *eventStream) Send(response *genaiv0.GenerationStreamResponse) error {
	stream.sent = append(stream.sent, response)

	return nil
}

func TestGrpcGenerationStream(t *testing.T) {
	t.Parallel()

	delta := json.RawMessage(`{"type":"response.output_text.delta","sequence_number":3}`)

	// events yields the given events, then the error when there is one.
	events := func(err error, sent ...*core.GenerationStreamEvent) iter.Seq2[*core.GenerationStreamEvent, error] {
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

	type serviceMock struct {
		resp *core.GenerationStreamResult
		err  error
	}

	testCases := []struct {
		name string

		request *genaiv0.GenerationStreamRequest

		serviceMock *serviceMock

		expect       []*genaiv0.GenerationStreamResponse
		expectStatus codes.Code
	}{
		{
			name: "Success",

			request: &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID, StartingAfter: new(int64(2))},
			serviceMock: &serviceMock{resp: &core.GenerationStreamResult{
				Events: events(nil, &core.GenerationStreamEvent{Sequence: 3, Data: delta}),
			}},

			expect: []*genaiv0.GenerationStreamResponse{{SequenceNumber: 3, Event: delta}},
		},
		{
			// The provider dropped the stream: the caller resumes after the last event it received.
			name: "Error/Interrupted",

			request: &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{resp: &core.GenerationStreamResult{
				Events: events(core.ErrProviderRetryable, &core.GenerationStreamEvent{Sequence: 3, Data: delta}),
			}},

			expect:       []*genaiv0.GenerationStreamResponse{{SequenceNumber: 3, Event: delta}},
			expectStatus: codes.Unavailable,
		},
		{
			// Past the stream window, the caller polls for the result.
			name: "Error/StreamUnavailable",

			request: &genaiv0.GenerationStreamRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{resp: &core.GenerationStreamResult{
				Events: events(core.ErrStreamUnavailable),
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

			stream := &eventStream{}

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
