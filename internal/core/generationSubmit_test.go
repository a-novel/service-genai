package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/a-novel-kit/golib/transaction/transactiontest"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
	connectorsmocks "github.com/a-novel/service-genai/internal/lib/connectors/mocks"
)

// submitRequest is a valid submission; a case adjusts it.
func submitRequest(adjust func(request *core.GenerationSubmitRequest)) *core.GenerationSubmitRequest {
	request := &core.GenerationSubmitRequest{
		OwnerID:  testOwner,
		Purpose:  testPurpose,
		Provider: testProvider,
		Payload:  json.RawMessage(`{"model": "a-model", "input": "Continue the scene."}`),
	}

	if adjust != nil {
		adjust(request)
	}

	return request
}

// payloadOfSize makes the payload the given number of bytes.
func payloadOfSize(size int) func(request *core.GenerationSubmitRequest) {
	return func(request *core.GenerationSubmitRequest) {
		const envelope = len(`{"input":""}`)

		request.Payload = json.RawMessage(`{"input":"` + strings.Repeat("x", size-envelope) + `"}`)
	}
}

// testConnectors serve the test provider in the Async mode, and another provider without it.
func testConnectors(t *testing.T) connectors.Registry {
	t.Helper()

	return connectors.Registry{
		testProvider: connectorsmocks.NewMockAsync(t),
		testStreamer: asyncStreaming{connectorsmocks.NewMockAsync(t), connectorsmocks.NewMockStreaming(t)},
		"another":    connectorsmocks.NewMockAsync(t),
		"sync-only":  struct{}{},
	}
}

// testCaps declare the test purpose, limited to 2 live generations and capped over 5h for the default
// tier, and a global limit of 3 live generations and cap over 720h.
var testCaps = config.Caps{
	Purposes: map[string]config.TierCaps{
		testPurpose: {
			config.DefaultTier: {
				Concurrency: new(2),
				Windows:     map[time.Duration]config.Limit{5 * time.Hour: {InputTokens: new(int64(1000))}},
			},
			"unthrottled": {
				Concurrency: new(-1),
				Windows:     map[time.Duration]config.Limit{5 * time.Hour: {InputTokens: new(int64(1000))}},
			},
			"unlimited": {Concurrency: new(-1)},
			"forbidden": {Forbidden: true},
		},
		"studio.analysis": {config.DefaultTier: {Concurrency: new(-1)}},
	},
	Global: config.TierCaps{
		config.DefaultTier: {
			Concurrency: new(3),
			Windows:     map[time.Duration]config.Limit{720 * time.Hour: {OutputTokens: new(int64(500))}},
		},
	},
}

// liveMock is one count of live generations admission reads, and what it finds. The count includes
// the generation the submit just created.
type liveMock struct {
	purpose bool
	count   int64
	err     error
}

// alone is the admission of a default-tier user whose only live generation is the one just created.
var alone = []liveMock{{purpose: true, count: 1}, {count: 1}}

// usageMock is one window sum admission reads, and what it finds.
type usageMock struct {
	purpose bool
	window  time.Duration
	used    dao.GenerationUsageTotals
	err     error
}

// underCaps is the admission of a default-tier user with room left under every cap.
var underCaps = []usageMock{{purpose: true, window: 5 * time.Hour}, {window: 720 * time.Hour}}

func TestGenerationSubmit(t *testing.T) {
	t.Parallel()

	const maxAttempts = 2

	type daoMock struct {
		resp *dao.GenerationSubmitResult
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationSubmitRequest

		daoMock *daoMock
		// live lists the counts of live generations admission reads. Only a created generation is
		// admitted.
		live []liveMock
		// usage lists the window sums admission reads, in order. Only a created generation is admitted.
		usage []usageMock
		// expectRefusal refuses the created generation for a reached limit: its transaction rolls it
		// back, so nothing ever starts it.
		expectRefusal bool
		// checkErr is what starting a created generation returns. A replay is never started here.
		// payloadErr is what storing an admitted generation's payload returns.
		payloadErr error
		checkErr   error
		attemptErr error

		expectCreated bool
		expectErr     error
	}{
		{
			name: "Success",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    alone,
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// A resend attaches to the recorded generation; whoever polls it next checks it.
			name: "Success/Replayed",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration()}},
		},
		{
			name: "Success/PayloadAtCeiling",

			request: submitRequest(payloadOfSize(core.PayloadSizeCeiling)),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    alone,
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// The purpose's limits are lifted for this tier; the global ones still apply.
			name: "Success/UnlimitedTier",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "unlimited" }),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    []liveMock{{count: 1}},
			usage:   []usageMock{{window: 720 * time.Hour}},

			expectCreated: true,
		},
		{
			// -1 lifts the purpose's concurrency alone: its windows and the global limits still apply.
			name: "Success/ConcurrencyLifted",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "unthrottled" }),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    []liveMock{{count: 1}},
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// The created generation is the second of its purpose and the third overall: exactly both
			// limits.
			name: "Success/AtConcurrencyLimit",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    []liveMock{{purpose: true, count: 2}, {count: 3}},
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// Refused before any usage is read.
			name: "Error/PurposeConcurrencyReached",

			request:       submitRequest(nil),
			daoMock:       &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:          []liveMock{{purpose: true, count: 3}},
			expectRefusal: true,

			expectErr: core.ErrUsageCapReached,
		},
		{
			// Room left in the purpose, but not across every purpose.
			name: "Error/GlobalConcurrencyReached",

			request:       submitRequest(nil),
			daoMock:       &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:          []liveMock{{purpose: true, count: 1}, {count: 4}},
			usage:         []usageMock{{purpose: true, window: 5 * time.Hour}},
			expectRefusal: true,

			expectErr: core.ErrUsageCapReached,
		},
		{
			name: "Error/CountLive",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    []liveMock{{purpose: true, err: errFoo}},

			expectErr: errFoo,
		},
		{
			name: "Error/PurposeCapReached",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    []liveMock{{purpose: true, count: 1}},
			usage: []usageMock{
				{purpose: true, window: 5 * time.Hour, used: dao.GenerationUsageTotals{InputTokens: 1000}},
			},
			expectRefusal: true,

			expectErr: core.ErrUsageCapReached,
		},
		{
			name: "Error/GlobalCapReached",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    alone,
			usage: []usageMock{
				{purpose: true, window: 5 * time.Hour, used: dao.GenerationUsageTotals{InputTokens: 999}},
				{window: 720 * time.Hour, used: dao.GenerationUsageTotals{OutputTokens: 500}},
			},
			expectRefusal: true,

			expectErr: core.ErrUsageCapReached,
		},
		{
			// Refused before anything is counted.
			name: "Error/ForbiddenTier",

			request:       submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "forbidden" }),
			daoMock:       &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			expectRefusal: true,

			expectErr: core.ErrUsageCapReached,
		},
		{
			name: "Error/UndeclaredPurpose",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Purpose = "studio.unknown" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/UsageSum",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    []liveMock{{purpose: true, count: 1}},
			usage:   []usageMock{{purpose: true, window: 5 * time.Hour, err: errFoo}},

			expectErr: errFoo,
		},
		{
			name: "Error/NoOwner",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.OwnerID = uuid.Nil }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NoPurpose",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Purpose = " " }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NoProvider",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Provider = "" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/UnknownProvider",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Provider = "gpt-5" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// The request is valid; this service cannot run it in the Async mode.
			name: "Error/ModeUnsupported",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Provider = "sync-only" }),

			expectErr: connectors.ErrModeUnsupported,
		},
		{
			// Streaming needs a provider that offers the Streaming mode, not only the Async one.
			name: "Error/StreamModeUnsupported",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Stream = true }),

			expectErr: connectors.ErrModeUnsupported,
		},
		{
			name: "Success/Streamed",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Provider = testStreamer
				request.Stream = true
			}),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    alone,
			usage:   underCaps,

			expectCreated: true,
		},
		{
			name: "Error/PayloadNotJSON",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`not json`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// A provider request is always an object; anything else is a caller bug.
			name: "Error/PayloadNotAnObject",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`["model", "a-model"]`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// Canonicalizing would keep one of the values, so two different requests would share a key.
			name: "Error/PayloadDuplicateNames",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`{"input": "a door", "input": "a window"}`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/PayloadUnpairedSurrogate",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`{"input": "\ud800"}`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// Refused here rather than at the transport, so the caller gets an error it can act on.
			name: "Error/PayloadTooLarge",

			request: submitRequest(payloadOfSize(core.PayloadSizeCeiling + 1)),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// Recording and admission roll back together, so nothing starts a generation with no payload.
			name: "Error/StorePayload",

			request:    submitRequest(nil),
			daoMock:    &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:       alone,
			usage:      underCaps,
			payloadErr: errFoo,

			expectErr: errFoo,
		},
		{
			// The generation is admitted and recorded, so the next poll or the sweep starts it: the caller
			// gets it to follow.
			name: "Success/StartFailed",

			request:  submitRequest(nil),
			daoMock:  &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:     alone,
			usage:    underCaps,
			checkErr: errFoo,

			expectCreated: true,
		},
		{
			name: "Error/Attempts",

			request: submitRequest(nil),
			daoMock: &daoMock{
				resp: &dao.GenerationSubmitResult{Generation: settledGeneration(dao.GenerationStatusSucceeded)},
			},
			attemptErr: errFoo,

			expectErr: errFoo,
		},
		{
			name: "Error/Internal",

			request: submitRequest(nil),
			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			submitDao := coremocks.NewMockGenerationSubmitDao(t)
			attemptDao := coremocks.NewMockGenerationSubmitDaoAttemptList(t)
			usageDao := coremocks.NewMockGenerationSubmitDaoUsageSum(t)
			liveDao := coremocks.NewMockGenerationSubmitDaoCountLive(t)
			payloadDao := coremocks.NewMockGenerationSubmitDaoPayloadInsert(t)
			check := coremocks.NewMockGenerationSubmitServiceCheck(t)

			admitted := !testCase.expectRefusal
			created := testCase.daoMock != nil && testCase.daoMock.resp != nil && testCase.daoMock.resp.Created

			// A purpose scope reads the request's purpose; the global one reads every purpose.
			scope := func(purpose bool) *string {
				if purpose {
					return &testCase.request.Purpose
				}

				return nil
			}

			for _, live := range testCase.live {
				// Only the purpose scope counts a generation whose call may still run untracked.
				var unknownHold time.Duration
				if live.purpose {
					unknownHold = 15 * time.Minute
				}

				liveDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationCountLiveRequest{
						OwnerID: testCase.request.OwnerID, Purpose: scope(live.purpose), UnknownOutcomeWindow: unknownHold,
					}).
					Return(&dao.GenerationLiveCount{Count: live.count}, live.err)

				admitted = admitted && live.err == nil
			}

			for _, usage := range testCase.usage {
				usageDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationUsageSumRequest{
						OwnerID: testCase.request.OwnerID, Purpose: scope(usage.purpose), Window: usage.window,
					}).
					Return(&usage.used, usage.err)

				admitted = admitted && usage.err == nil
			}

			if testCase.daoMock != nil {
				submitDao.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationSubmitRequest) bool {
						// The digest is derived rather than left empty.
						return request.Purpose == testCase.request.Purpose &&
							request.Provider == testCase.request.Provider &&
							request.Streamed == testCase.request.Stream &&
							request.MaxAttempts == maxAttempts &&
							len(request.RequestKey) == 32 &&
							request.ID != uuid.Nil &&
							// A later check links back to the submitting request through it.
							request.Traceparent != nil &&
							strings.Contains(*request.Traceparent, testCaller.TraceID().String())
					})).
					Return(testCase.daoMock.resp, testCase.daoMock.err)

				if created && admitted {
					// Stored as sent, once admitted: a replay or a refusal never sends it over.
					payloadDao.EXPECT().
						Exec(mock.Anything, &dao.GenerationPayloadInsertRequest{
							GenerationID: testGenerationID, Payload: testCase.request.Payload,
						}).
						Return(&dao.GenerationPayload{GenerationID: testGenerationID}, testCase.payloadErr)
				}

				if created && admitted && testCase.payloadErr == nil {
					check.EXPECT().
						Exec(mock.Anything, &core.GenerationCheckRequest{Generation: testCase.daoMock.resp.Generation}).
						Return(lo.Ternary(testCase.checkErr == nil, testCase.daoMock.resp.Generation, nil), testCase.checkErr)
				}

				// Only a generation with an answered attempt is listed; the fixtures' pending ones have none.
				if testCase.daoMock.resp != nil && testCase.payloadErr == nil &&
					(!created || admitted) && testCase.daoMock.resp.Generation.SettledAt != nil {
					attemptDao.EXPECT().
						Exec(mock.Anything, &dao.GenerationAttemptListRequest{GenerationID: testGenerationID}).
						Return([]*dao.GenerationAttempt{}, testCase.attemptErr)
				}
			}

			service := core.NewGenerationSubmit(
				submitDao, attemptDao, usageDao, liveDao, payloadDao, check,
				testConnectors(t),
				transactiontest.NewTransactor(),
				config.Generations{MaxAttempts: maxAttempts},
				testCaps,
			)

			result, err := service.Exec(trace.ContextWithSpanContext(t.Context(), testCaller), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testGenerationID, result.Generation.ID)
				require.Equal(t, testCase.expectCreated, result.Created)
			}

			submitDao.AssertExpectations(t)
			attemptDao.AssertExpectations(t)
			usageDao.AssertExpectations(t)
			liveDao.AssertExpectations(t)
			payloadDao.AssertExpectations(t)
			check.AssertExpectations(t)
		})
	}

	// The key is the request's identity: equal requests share it, and every field the caller sends
	// changes it. A field left out would let two different requests return one generation.
	keyCases := []struct {
		name string

		adjust func(request *core.GenerationSubmitRequest)

		expectSame bool
	}{
		{name: "Same", expectSame: true},
		{
			// A resend rebuilt from the caller's state may reorder the top-level members, reformat, or
			// escape a character another way; it is still the same request.
			name: "ReformattedJSON",

			adjust: func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`{ "input" : "Continue the sc\u0065ne.", "model" : "a-model" }`)
			},

			expectSame: true,
		},
		{name: "Owner", adjust: func(request *core.GenerationSubmitRequest) { request.OwnerID = uuid.New() }},
		{name: "Purpose", adjust: func(request *core.GenerationSubmitRequest) { request.Purpose = "studio.analysis" }},
		{name: "Provider", adjust: func(request *core.GenerationSubmitRequest) { request.Provider = "another" }},
		{name: "Payload", adjust: func(request *core.GenerationSubmitRequest) {
			request.Payload = json.RawMessage(`{"model": "a-model", "input": "Stop."}`)
		}},
		{name: "Variant", adjust: func(request *core.GenerationSubmitRequest) { request.Variant = 1 }},
	}

	baseKey := submittedKey(t, submitRequest(nil))

	// A provider can read meaning into nested order, such as a schema's property order, which sets the
	// order of the output's fields: reordered, it is another request.
	t.Run("RequestKey/NestedOrder", func(t *testing.T) {
		t.Parallel()

		withSchema := func(properties string) *core.GenerationSubmitRequest {
			return submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`{"model": "a-model", "text": {"properties": ` + properties + `}}`)
			})
		}

		require.NotEqual(t,
			submittedKey(t, withSchema(`{"reasoning": {}, "answer": {}}`)),
			submittedKey(t, withSchema(`{"answer": {}, "reasoning": {}}`)),
		)
	})

	// Polling and streaming the same payload are two requests.
	t.Run("RequestKey/Stream", func(t *testing.T) {
		t.Parallel()

		polled := submittedKey(t, submitRequest(func(request *core.GenerationSubmitRequest) {
			request.Provider = testStreamer
		}))
		streamedKey := submittedKey(t, submitRequest(func(request *core.GenerationSubmitRequest) {
			request.Provider = testStreamer
			request.Stream = true
		}))

		require.NotEqual(t, polled, streamedKey)
	})

	for _, keyCase := range keyCases {
		t.Run("RequestKey/"+keyCase.name, func(t *testing.T) {
			t.Parallel()

			key := submittedKey(t, submitRequest(keyCase.adjust))

			if keyCase.expectSame {
				require.Equal(t, baseKey, key)
			} else {
				require.NotEqual(t, baseKey, key)
			}
		})
	}
}

// submittedKey returns the request key a submission hands to the data access.
func submittedKey(t *testing.T, request *core.GenerationSubmitRequest) []byte {
	t.Helper()

	var key []byte

	submitDao := coremocks.NewMockGenerationSubmitDao(t)
	submitDao.EXPECT().
		Exec(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, submitted *dao.GenerationSubmitRequest) (*dao.GenerationSubmitResult, error) {
			key = submitted.RequestKey

			return nil, errFoo
		})

	service := core.NewGenerationSubmit(
		submitDao,
		coremocks.NewMockGenerationSubmitDaoAttemptList(t),
		coremocks.NewMockGenerationSubmitDaoUsageSum(t),
		coremocks.NewMockGenerationSubmitDaoCountLive(t),
		coremocks.NewMockGenerationSubmitDaoPayloadInsert(t),
		coremocks.NewMockGenerationSubmitServiceCheck(t),
		testConnectors(t),
		transactiontest.NewTransactor(),
		config.Generations{MaxAttempts: 1},
		testCaps,
	)

	_, err := service.Exec(t.Context(), request)
	if !errors.Is(err, errFoo) {
		panic(err)
	}

	return key
}
