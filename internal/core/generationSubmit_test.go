package core_test

import (
	"bytes"
	"context"
	"encoding/hex"
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
			"windowed": {
				Windows: map[time.Duration]config.Limit{
					5 * time.Hour:   {InputTokens: new(int64(1000))},
					720 * time.Hour: {OutputTokens: new(int64(500))},
				},
			},
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

func TestGenerationSubmit(t *testing.T) {
	t.Parallel()

	purposeScope := `purpose "studio.generation"`
	// fourHoursAgo dates the oldest usage of a 5h window: it leaves the window in an hour.
	fourHoursAgo := time.Now().Add(-4 * time.Hour)

	// withCaps sends the caller's caps for its user.
	withCaps := func(caps *core.GenerationCaps) func(request *core.GenerationSubmitRequest) {
		return func(request *core.GenerationSubmitRequest) { request.Caps = caps }
	}

	type daoMock struct {
		resp *dao.GenerationSubmitResult
		err  error
	}

	// liveMock is one count of live generations admission reads, and what it finds. The count includes
	// the generation the submit just created.
	type liveMock struct {
		purpose bool
		count   int64
		err     error
	}

	// usageMock is the window sums of one scope admission reads, and what it finds: one total per
	// window, in the order of the windows.
	type usageMock struct {
		purpose bool
		windows []time.Duration
		used    []*dao.GenerationUsageTotals
		err     error
	}

	// alone is the admission of a default-tier user whose only live generation is the one just created.
	alone := []liveMock{{purpose: true, count: 1}, {count: 1}}

	// underCaps is the admission of a default-tier user with room left under every cap.
	underCaps := []usageMock{
		{purpose: true, windows: []time.Duration{5 * time.Hour}, used: []*dao.GenerationUsageTotals{{}}},
		{windows: []time.Duration{720 * time.Hour}, used: []*dao.GenerationUsageTotals{{}}},
	}

	testCases := []struct {
		name string

		request *core.GenerationSubmitRequest

		daoMock *daoMock
		// live lists the counts of live generations admission reads. Only a created generation is
		// admitted.
		live []liveMock
		// usage lists the scopes whose window sums admission reads, in order. Only a created generation
		// is admitted.
		usage []usageMock
		// expectRefusal refuses the created generation for a reached limit: its transaction rolls it
		// back, so nothing ever sends it. The refusal names the scope and the limit that refused, and
		// the time its window frees usage.
		expectRefusal    bool
		expectScope      string
		expectLimit      string
		expectRetryAfter time.Duration
		// expectMessage is the refusal's message a caller reads, when the case pins it.
		expectMessage string
		// checkErr is what sending a created generation's call returns. A replay is never sent here.
		checkErr   error
		attemptErr error

		expectCreated bool
		expectErr     error
	}{
		{
			name: "Success",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    alone,
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// A resend attaches to the recorded generation; whoever polls it next checks it.
			name: "Success/Replayed",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(time.Minute)}},
		},
		{
			name: "Success/PayloadAtCeiling",

			request: submitRequest(payloadOfSize(core.PayloadSizeCeiling)),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    alone,
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// The purpose's limits are lifted for this tier; the global ones still apply.
			name: "Success/UnlimitedTier",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "unlimited" }),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    []liveMock{{count: 1}},
			usage:   []usageMock{underCaps[1]},

			expectCreated: true,
		},
		{
			// -1 lifts the purpose's concurrency alone: its windows and the global limits still apply.
			name: "Success/ConcurrencyLifted",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "unthrottled" }),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    []liveMock{{count: 1}},
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// The created generation is the second of its purpose and the third overall: exactly both
			// limits.
			name: "Success/AtConcurrencyLimit",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    []liveMock{{purpose: true, count: 2}, {count: 3}},
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// Refused before any usage is read.
			name: "Error/PurposeConcurrencyReached",

			request:       submitRequest(nil),
			daoMock:       &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:          []liveMock{{purpose: true, count: 3}},
			expectRefusal: true,
			expectScope:   purposeScope,
			expectLimit:   "concurrency 2",
			// The message reads as admission words it.
			expectMessage: `usage cap reached: purpose "studio.generation" concurrency: 2 generations already in flight`,

			expectErr: core.ErrUsageCapReached,
		},
		{
			// Room left in the purpose, but not across every purpose.
			name: "Error/GlobalConcurrencyReached",

			request:       submitRequest(nil),
			daoMock:       &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:          []liveMock{{purpose: true, count: 1}, {count: 4}},
			usage:         []usageMock{underCaps[0]},
			expectRefusal: true,
			expectScope:   "global",
			expectLimit:   "concurrency 3",

			expectErr: core.ErrUsageCapReached,
		},
		{
			name: "Error/CountLive",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    []liveMock{{purpose: true, err: errFoo}},

			expectErr: errFoo,
		},
		{
			name: "Error/PurposeCapReached",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    []liveMock{{purpose: true, count: 1}},
			usage: []usageMock{{
				purpose: true,
				windows: []time.Duration{5 * time.Hour},
				used:    []*dao.GenerationUsageTotals{{InputTokens: 1000, OldestAt: &fourHoursAgo}},
			}},
			expectRefusal:    true,
			expectScope:      purposeScope,
			expectLimit:      "input tokens over 5h",
			expectRetryAfter: time.Hour,
			expectMessage:    `usage cap reached: purpose "studio.generation" over 5h0m0s`,

			expectErr: core.ErrUsageCapReached,
		},
		{
			name: "Error/GlobalCapReached",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    alone,
			usage: []usageMock{
				{
					purpose: true,
					windows: []time.Duration{5 * time.Hour},
					used:    []*dao.GenerationUsageTotals{{InputTokens: 999}},
				},
				{windows: []time.Duration{720 * time.Hour}, used: []*dao.GenerationUsageTotals{{OutputTokens: 500}}},
			},
			expectRefusal: true,
			expectScope:   "global",
			// No record dates the window's usage, so nothing tells when it frees.
			expectLimit: "output tokens over 720h",

			expectErr: core.ErrUsageCapReached,
		},
		{
			// One read sums every window of the purpose, shortest first.
			name: "Success/SeveralWindows",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "windowed" }),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    []liveMock{{count: 1}},
			usage: []usageMock{
				{
					purpose: true,
					windows: []time.Duration{5 * time.Hour, 720 * time.Hour},
					used:    []*dao.GenerationUsageTotals{{InputTokens: 999}, {OutputTokens: 499}},
				},
				underCaps[1],
			},

			expectCreated: true,
		},
		{
			// A window past the first can refuse the generation too.
			name: "Error/LaterWindowReached",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "windowed" }),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			usage: []usageMock{{
				purpose: true,
				windows: []time.Duration{5 * time.Hour, 720 * time.Hour},
				used:    []*dao.GenerationUsageTotals{{InputTokens: 999}, {OutputTokens: 500}},
			}},
			expectRefusal: true,
			expectScope:   purposeScope,
			expectLimit:   "output tokens over 720h",

			expectErr: core.ErrUsageCapReached,
		},
		{
			// Refused before anything is counted.
			name: "Error/ForbiddenTier",

			request:       submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "forbidden" }),
			daoMock:       &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			expectRefusal: true,
			expectScope:   purposeScope,
			expectLimit:   "no usage",

			expectErr: core.ErrUsageCapReached,
		},
		{
			// The caller owns every limit: its caps alone limit the purpose, nothing counts the user's
			// other purposes, and the caps file's tier is ignored, a forbidden one included.
			name: "Success/Caps",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.UserTier = "forbidden"
				request.Caps = &core.GenerationCaps{
					Concurrency: new(1),
					Windows:     []core.GenerationCapWindow{{Duration: time.Hour, InputTokens: new(int64(100))}},
				}
			}),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    []liveMock{{purpose: true, count: 1}},
			usage: []usageMock{
				{purpose: true, windows: []time.Duration{time.Hour}, used: []*dao.GenerationUsageTotals{{InputTokens: 99}}},
			},

			expectCreated: true,
		},
		{
			// Empty caps limit nothing: admission reads no count and no sum.
			name: "Success/EmptyCaps",

			request: submitRequest(withCaps(&core.GenerationCaps{})),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},

			expectCreated: true,
		},
		{
			// The count includes the generations whose call may still run, within the hold.
			name: "Error/CapsConcurrencyReached",

			request:       submitRequest(withCaps(&core.GenerationCaps{Concurrency: new(1)})),
			daoMock:       &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:          []liveMock{{purpose: true, count: 2}},
			expectRefusal: true,
			expectScope:   purposeScope,
			expectLimit:   "concurrency 1",
			expectMessage: `usage cap reached: purpose "studio.generation" concurrency: 1 generations already in flight`,

			expectErr: core.ErrUsageCapReached,
		},
		{
			// The refusal tells when the window frees usage: its oldest record then leaves it.
			name: "Error/CapsWindowReached",

			request: submitRequest(withCaps(&core.GenerationCaps{
				Windows: []core.GenerationCapWindow{{Duration: 5 * time.Hour, OutputTokens: new(int64(10))}},
			})),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			usage: []usageMock{{
				purpose: true,
				windows: []time.Duration{5 * time.Hour},
				used:    []*dao.GenerationUsageTotals{{OutputTokens: 10, OldestAt: &fourHoursAgo}},
			}},
			expectRefusal:    true,
			expectScope:      purposeScope,
			expectLimit:      "output tokens over 5h",
			expectRetryAfter: time.Hour,

			expectErr: core.ErrUsageCapReached,
		},
		{
			name: "Error/CapsNegativeConcurrency",

			request: submitRequest(withCaps(&core.GenerationCaps{Concurrency: new(-1)})),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/CapsTooManyWindows",

			request: submitRequest(withCaps(&core.GenerationCaps{Windows: func() []core.GenerationCapWindow {
				windows := make([]core.GenerationCapWindow, 11)
				for index := range windows {
					windows[index] = core.GenerationCapWindow{Duration: time.Duration(index+1) * time.Hour}
				}

				return windows
			}()})),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// Two windows of one duration would each claim the same usage.
			name: "Error/CapsRepeatedWindow",

			request: submitRequest(withCaps(&core.GenerationCaps{Windows: []core.GenerationCapWindow{
				{Duration: time.Hour, InputTokens: new(int64(1))}, {Duration: 60 * time.Minute, OutputTokens: new(int64(1))},
			}})),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/CapsEmptyWindow",

			request: submitRequest(withCaps(&core.GenerationCaps{Windows: []core.GenerationCapWindow{{}}})),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/CapsWindowPastAYear",

			request: submitRequest(withCaps(&core.GenerationCaps{Windows: []core.GenerationCapWindow{
				{Duration: 8761 * time.Hour, InputTokens: new(int64(1))},
			}})),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/CapsNegativeLimit",

			request: submitRequest(withCaps(&core.GenerationCaps{Windows: []core.GenerationCapWindow{
				{Duration: time.Hour, InputTokens: new(int64(-1))},
			}})),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/UndeclaredPurpose",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Purpose = "studio.unknown" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/UsageSum",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
			live:    []liveMock{{purpose: true, count: 1}},
			usage:   []usageMock{{purpose: true, windows: []time.Duration{5 * time.Hour}, err: errFoo}},

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
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
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
			// The generation is admitted and recorded, so a later check settles its start intent: the
			// caller gets it to follow.
			name: "Success/StartFailed",

			request:  submitRequest(nil),
			daoMock:  &daoMock{resp: &dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}},
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
			check := coremocks.NewMockGenerationSubmitServiceCheck(t)

			admitted := !testCase.expectRefusal
			created := testCase.daoMock != nil && testCase.daoMock.resp != nil && testCase.daoMock.resp.Created

			// A purpose scope reads the request's purpose; the global one reads every purpose.
			purpose := &testCase.request.Purpose

			for _, live := range testCase.live {
				liveRequest := &dao.GenerationCountLiveRequest{
					OwnerID: testCase.request.OwnerID,
					Purpose: lo.Ternary(live.purpose, purpose, nil),
					// Only the purpose scope counts a generation whose call may still run untracked.
					UnknownOutcomeWindow: lo.Ternary(live.purpose, 15*time.Minute, 0),
				}

				liveDao.EXPECT().
					Exec(mock.Anything, liveRequest).
					Return(&dao.GenerationLiveCount{Count: live.count}, live.err)

				admitted = admitted && live.err == nil
			}

			for _, usage := range testCase.usage {
				usageDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationUsageSumRequest{
						OwnerID: testCase.request.OwnerID, Purpose: lo.Ternary(usage.purpose, purpose, nil), Windows: usage.windows,
					}).
					Return(usage.used, usage.err)

				admitted = admitted && usage.err == nil
			}

			if testCase.daoMock != nil {
				submitDao.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationSubmitRequest) bool {
						// The digest is derived rather than left empty.
						return request.Purpose == testCase.request.Purpose &&
							request.Provider == testCase.request.Provider &&
							request.Streamed == testCase.request.Stream &&
							len(request.RequestKey) == 32 &&
							request.ID != uuid.Nil &&
							// A later check links back to the submitting request through it.
							request.Traceparent != nil &&
							strings.Contains(*request.Traceparent, testCaller.TraceID().String())
					})).
					Return(testCase.daoMock.resp, testCase.daoMock.err)

				if created && admitted {
					// The submit sends the payload it holds, as sent: a replay or a refusal never sends it.
					check.EXPECT().
						Exec(mock.Anything, mock.MatchedBy(func(request *core.GenerationCheckRequest) bool {
							return request.Generation == testCase.daoMock.resp.Generation && request.Start != nil &&
								bytes.Equal(request.Start.Payload, testCase.request.Payload) &&
								!request.Start.IntentAt.IsZero()
						})).
						Return(lo.Ternary(testCase.checkErr == nil, testCase.daoMock.resp.Generation, nil), testCase.checkErr)
				}

				// Only a generation with an answered attempt is listed; the fixtures' pending ones have none.
				if testCase.daoMock.resp != nil && (!created || admitted) && testCase.daoMock.resp.Generation.SettledAt != nil {
					attemptDao.EXPECT().
						Exec(mock.Anything, &dao.GenerationAttemptListRequest{GenerationID: testGenerationID}).
						Return([]*dao.GenerationAttempt{}, testCase.attemptErr)
				}
			}

			service := core.NewGenerationSubmit(
				submitDao, attemptDao, usageDao, liveDao, check,
				testConnectors(t),
				transactiontest.NewTransactor(),
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

			if testCase.expectRefusal {
				var refusal *core.UsageCapError

				require.ErrorAs(t, err, &refusal)
				require.Equal(t, testCase.expectScope, refusal.Scope)
				require.Equal(t, testCase.expectLimit, refusal.Limit)
				require.InDelta(t, testCase.expectRetryAfter.Seconds(), refusal.RetryAfter.Seconds(), 5)

				if testCase.expectMessage != "" {
					require.EqualError(t, err, testCase.expectMessage)
				}
			}

			mock.AssertExpectationsForObjects(t, submitDao, attemptDao, usageDao, liveDao, check)
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

	// A key is stored with its generation, so one computed another way for the same request misses it
	// and pays again: changing these bytes means changing the key's tag.
	t.Run("RequestKey/Stable", func(t *testing.T) {
		t.Parallel()

		// Members sorted at the top level only, the number as written, the escaped letter decoded and
		// the HTML characters escaped.
		nested := submittedKey(t, submitRequest(func(request *core.GenerationSubmitRequest) {
			request.Payload = json.RawMessage(`{"text": {"format": {"b": 1.50, "a": "<\u00e9&>"}}, "model": "m"}`)
		}))

		require.Equal(t, "5911295a5b8a1f65b689558f6611c183ec942364196ad02b466d86c4841cadf9", hex.EncodeToString(baseKey))
		require.Equal(t, "0fd7c5c113277aa9fe9a26d90a0936edf98ba287ea53f0da00d07b8b5ccfc1dc", hex.EncodeToString(nested))
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
		coremocks.NewMockGenerationSubmitServiceCheck(t),
		testConnectors(t),
		transactiontest.NewTransactor(),
		testCaps,
	)

	_, err := service.Exec(t.Context(), request)
	if !errors.Is(err, errFoo) {
		panic(err)
	}

	return key
}
