package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
	"github.com/a-novel/service-genai/internal/handlers/protogen"
)

// watchStream records what the handler sent, standing in for a connected caller.
//
// The context field is how gRPC's own ServerStream exposes cancellation, so a stand-in for one has
// to carry it too.
//
//nolint:containedctx
type watchStream struct {
	grpc.ServerStream

	ctx  context.Context
	sent []*protogen.GenerationWatchResponse
}

func (stream *watchStream) Context() context.Context { return stream.ctx }

func (stream *watchStream) Send(response *protogen.GenerationWatchResponse) error {
	stream.sent = append(stream.sent, response)

	return nil
}

func (stream *watchStream) SetHeader(metadata.MD) error  { return nil }
func (stream *watchStream) SendHeader(metadata.MD) error { return nil }
func (stream *watchStream) SetTrailer(metadata.MD)       {}

// settled returns a terminal generation, which is what ends the stream.
func settledGeneration() *dao.Generation {
	generation := testGeneration()
	generation.Status = dao.GenerationStatusSucceeded
	generation.UpdatedAt = generation.UpdatedAt.Add(time.Minute)
	settledAt := generation.UpdatedAt
	generation.SettledAt = &settledAt

	return generation
}

func TestGrpcGenerationWatch(t *testing.T) {
	t.Parallel()

	type serviceMock struct {
		reads []*dao.Generation
		err   error
	}

	testCases := []struct {
		name string

		request *protogen.GenerationWatchRequest

		serviceMock *serviceMock

		// expectSent is how many snapshots reach the caller. One on subscribe, one per change, and
		// nothing for a tick where the generation did not move.
		expectSent   int
		expectStatus codes.Code
	}{
		{
			// Already terminal on subscribe: one snapshot, then the stream ends rather than idling.
			name: "Success/AlreadySettled",

			request:     &protogen.GenerationWatchRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{reads: []*dao.Generation{settledGeneration()}},

			expectSent: 1,
		},
		{
			name: "Success/StreamsUntilTerminal",

			request: &protogen.GenerationWatchRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{reads: []*dao.Generation{
				testGeneration(), settledGeneration(),
			}},

			expectSent: 2,
		},
		{
			// A tick where nothing moved sends nothing. Re-sending an unchanged generation would
			// make the stream a poll the caller pays for.
			name: "Success/SendsNothingForAnUnchangedTick",

			request: &protogen.GenerationWatchRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{reads: []*dao.Generation{
				testGeneration(), testGeneration(), settledGeneration(),
			}},

			expectSent: 2,
		},
		{
			// The watch reads through the same path as a get, so it cannot see what a read could
			// not.
			name: "Error/NotFound",

			request:     &protogen.GenerationWatchRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: core.ErrGenerationNotFound},

			expectStatus: codes.NotFound,
		},
		{
			name: "Error/InvalidOwnerID",

			request: &protogen.GenerationWatchRequest{Id: testGenerationID, OwnerId: "not-a-uuid"},

			expectStatus: codes.InvalidArgument,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockGrpcGenerationGetService(t)

			if testCase.serviceMock != nil {
				if testCase.serviceMock.err != nil {
					service.EXPECT().Exec(mock.Anything, mock.Anything).Return(nil, testCase.serviceMock.err)
				}

				for _, read := range testCase.serviceMock.reads {
					service.EXPECT().Exec(mock.Anything, mock.Anything).Return(read, nil).Once()
				}
			}

			stream := &watchStream{ctx: t.Context()}

			err := handlers.NewGrpcGenerationWatch(handlers.NewGrpcGenerationGet(service)).
				GenerationWatch(testCase.request, stream)

			if testCase.expectStatus != codes.OK {
				require.Equal(t, testCase.expectStatus, status.Code(err))

				return
			}

			require.NoError(t, err)
			require.Len(t, stream.sent, testCase.expectSent)

			// The last snapshot is always the terminal one, so a caller that reads only the final
			// message still has the outcome.
			last := stream.sent[len(stream.sent)-1]
			require.Equal(t, protogen.GenerationStatus_GENERATION_STATUS_SUCCEEDED, last.GetGeneration().GetStatus())

			service.AssertExpectations(t)
		})
	}
}

// Disconnecting mid-watch ends the stream rather than leaking the goroutine polling behind it.
func TestGrpcGenerationWatchStopsWhenTheCallerGoesAway(t *testing.T) {
	t.Parallel()

	service := handlersmocks.NewMockGrpcGenerationGetService(t)

	ctx, cancel := context.WithCancel(t.Context())

	service.EXPECT().
		Exec(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, *core.GenerationGetRequest) (*dao.Generation, error) {
			cancel()

			return testGeneration(), nil
		})

	stream := &watchStream{ctx: ctx}

	err := handlers.NewGrpcGenerationWatch(handlers.NewGrpcGenerationGet(service)).
		GenerationWatch(&protogen.GenerationWatchRequest{Id: testGenerationID, OwnerId: testOwnerID}, stream)
	require.ErrorIs(t, err, context.Canceled)

	// The subscribe snapshot still went out before the caller went away.
	require.Len(t, stream.sent, 1)
}
