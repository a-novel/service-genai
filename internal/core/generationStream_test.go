package core_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
	connectorsmocks "github.com/a-novel/service-genai/internal/lib/connectors/mocks"
)

func TestGenerationStream(t *testing.T) {
	t.Parallel()

	delta := json.RawMessage(`{"type":"response.output_text.delta","sequence_number":3}`)
	completed := json.RawMessage(`{"type":"response.completed","sequence_number":4}`)

	type daoMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationStreamRequest

		daoMock *daoMock
		// events is what the provider streams; nil expects no stream.
		events    []*connectors.StreamEvent
		streamErr error

		expect    []*core.GenerationStreamEvent
		expectErr error
	}{
		{
			name: "Success",

			request: &core.GenerationStreamRequest{ID: testGenerationID, OwnerID: testOwner, StartingAfter: new(int64(2))},
			daoMock: &daoMock{resp: streamed(withProvider(runningGeneration(1), testStreamer))},
			events: []*connectors.StreamEvent{
				{Sequence: 3, Data: delta}, {Sequence: 4, Data: completed, Terminal: true},
			},

			expect: []*core.GenerationStreamEvent{{Sequence: 3, Data: delta}, {Sequence: 4, Data: completed}},
		},
		{
			// The provider stops streaming it mid-way: the caller polls for the result.
			name: "Error/StreamUnavailable",

			request:   &core.GenerationStreamRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:   &daoMock{resp: streamed(withProvider(runningGeneration(1), testStreamer))},
			events:    []*connectors.StreamEvent{{Sequence: 3, Data: delta}},
			streamErr: connectors.ErrStreamUnavailable,

			expect:    []*core.GenerationStreamEvent{{Sequence: 3, Data: delta}},
			expectErr: core.ErrStreamUnavailable,
		},
		{
			name: "Error/NotStreamed",

			request: &core.GenerationStreamRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{resp: withProvider(runningGeneration(1), testStreamer)},

			expectErr: core.ErrGenerationNotStreamed,
		},
		{
			// A streamed start records its call once the provider has begun it.
			name: "Error/NoCallYet",

			request: &core.GenerationStreamRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{resp: streamed(withProvider(pendingGeneration(), testStreamer))},

			expectErr: core.ErrStreamUnavailable,
		},
		{
			name: "Error/ModeUnsupported",

			request: &core.GenerationStreamRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{resp: streamed(runningGeneration(1))},

			expectErr: connectors.ErrModeUnsupported,
		},
		{
			// Another owner's generation reports this too, so an id cannot be probed for existence.
			name: "Error/NotFound",

			request: &core.GenerationStreamRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{err: dao.ErrGenerationGetNotFound},

			expectErr: core.ErrGenerationNotFound,
		},
		{
			name: "Error/NegativeCursor",

			request: &core.GenerationStreamRequest{ID: testGenerationID, OwnerID: testOwner, StartingAfter: new(int64(-1))},

			expectErr: core.ErrInvalidRequest,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			getDao := coremocks.NewMockGenerationStreamDao(t)
			streaming := connectorsmocks.NewMockStreaming(t)
			registry := connectors.Registry{
				testProvider: connectorsmocks.NewMockAsync(t),
				testStreamer: asyncStreaming{connectorsmocks.NewMockAsync(t), streaming},
			}

			if testCase.daoMock != nil {
				getDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{ID: testCase.request.ID, OwnerID: testCase.request.OwnerID}).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			if testCase.events != nil {
				streaming.EXPECT().
					Stream(mock.Anything, &connectors.StreamRequest{ID: testCallID, StartingAfter: testCase.request.StartingAfter}).
					Return(streamOf(testCase.events, testCase.streamErr))
			}

			result, err := core.NewGenerationStream(getDao, registry).Exec(t.Context(), testCase.request)

			var received []*core.GenerationStreamEvent

			if err == nil {
				for event, eventErr := range result.Events {
					if eventErr != nil {
						err = eventErr

						break
					}

					received = append(received, event)
				}
			}

			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expect, received)

			getDao.AssertExpectations(t)
			streaming.AssertExpectations(t)
		})
	}
}
