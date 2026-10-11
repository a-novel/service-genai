package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

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
		"another":    connectorsmocks.NewMockAsync(t),
		"sync-only":  struct{}{},
	}
}

// testCaps declare the test purpose, capped over 5h for the default tier, and a global cap over 720h.
var testCaps = config.Caps{
	Purposes: map[string]config.TierCaps{
		testPurpose: {
			config.DefaultTier: {Windows: map[time.Duration]config.Limit{5 * time.Hour: {InputTokens: new(int64(1000))}}},
			"unlimited":        {Unlimited: true},
			"forbidden":        {Forbidden: true},
		},
		"studio.analysis": {config.DefaultTier: {Unlimited: true}},
	},
	Global: config.TierCaps{
		config.DefaultTier: {Windows: map[time.Duration]config.Limit{720 * time.Hour: {OutputTokens: new(int64(500))}}},
	},
	Concurrency: map[string]int{config.DefaultTier: 2, "unlimited": -1},
}

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
		// live is how many generations the user has pending or running, the created one included:
		// one when left zero. An unlimited tier is never counted.
		live    int64
		liveErr error
		// usage lists the window sums admission reads, in order. Only a created generation is admitted.
		usage []usageMock
		// expectRefusal settles the created generation as failed for a reached cap.
		expectRefusal bool
		settleErr     error
		// checkErr is what starting a created generation returns. A replay is never started here.
		checkErr   error
		attemptErr error

		expectCreated bool
		expectErr     error
	}{
		{
			name: "Success",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
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
			// Stored as written, so it reaches the provider as written.
			name: "Success/HTMLCharacters",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`{"input": "Tom & Jerry <3>"}`)
			}),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			usage:   underCaps,

			expectCreated: true,
		},
		{
			name: "Success/PayloadAtCeiling",

			request: submitRequest(payloadOfSize(core.PayloadSizeCeiling)),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// The purpose cap is lifted for this tier; the global one still applies.
			name: "Success/UnlimitedTier",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "unlimited" }),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			usage:   []usageMock{{window: 720 * time.Hour}},

			expectCreated: true,
		},
		{
			// The created generation is the second one running: exactly the limit.
			name: "Success/AtConcurrencyLimit",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:    2,
			usage:   underCaps,

			expectCreated: true,
		},
		{
			// Refused before any usage is read.
			name: "Error/ConcurrencyReached",

			request:       submitRequest(nil),
			daoMock:       &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			live:          3,
			expectRefusal: true,

			expectErr: core.ErrUsageCapReached,
		},
		{
			name: "Error/CountLive",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			liveErr: errFoo,

			expectErr: errFoo,
		},
		{
			// A tier the caps do not know answers to the default caps.
			name: "Success/UnrecognizedTier",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.UserTier = "premium" }),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			usage:   underCaps,

			expectCreated: true,
		},
		{
			name: "Error/PurposeCapReached",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
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
			usage: []usageMock{
				{purpose: true, window: 5 * time.Hour, used: dao.GenerationUsageTotals{InputTokens: 999}},
				{window: 720 * time.Hour, used: dao.GenerationUsageTotals{OutputTokens: 500}},
			},
			expectRefusal: true,

			expectErr: core.ErrUsageCapReached,
		},
		{
			// Refused before any usage is read.
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
			usage:   []usageMock{{purpose: true, window: 5 * time.Hour, err: errFoo}},

			expectErr: errFoo,
		},
		{
			// The refused generation stays pending, and the next check would start it: the caller
			// must not read this as a refusal.
			name: "Error/RefusalSettle",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			usage: []usageMock{
				{purpose: true, window: 5 * time.Hour, used: dao.GenerationUsageTotals{InputTokens: 1000}},
			},
			expectRefusal: true,
			settleErr:     errFoo,

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
			// PostgreSQL text cannot hold it.
			name: "Error/PurposeWithNUL",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Purpose = "studio\x00generation" }),

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
			name: "Error/PayloadNull",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`null`)
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
			// The generation is recorded, so a resend finds it; the caller learns the start failed.
			name: "Error/Start",

			request:  submitRequest(nil),
			daoMock:  &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			usage:    underCaps,
			checkErr: errFoo,

			expectErr: errFoo,
		},
		{
			name: "Error/Attempts",

			request:    submitRequest(nil),
			daoMock:    &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration()}},
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
			attemptDao := coremocks.NewMockGenerationSubmitAttemptListDao(t)
			usageDao := coremocks.NewMockGenerationSubmitUsageSumDao(t)
			liveDao := coremocks.NewMockGenerationSubmitCountLiveDao(t)
			settleDao := coremocks.NewMockGenerationSubmitSettleDao(t)
			check := coremocks.NewMockGenerationSubmitServiceCheck(t)

			admitted := !testCase.expectRefusal && testCase.liveErr == nil
			created := testCase.daoMock != nil && testCase.daoMock.resp != nil && testCase.daoMock.resp.Created

			if created && testCase.request.UserTier != "unlimited" {
				liveDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationCountLiveRequest{OwnerID: testCase.request.OwnerID}).
					Return(&dao.GenerationLiveCount{Count: max(testCase.live, 1)}, testCase.liveErr)
			}

			for _, usage := range testCase.usage {
				var purpose *string
				if usage.purpose {
					purpose = &testCase.request.Purpose
				}

				usageDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationUsageSumRequest{
						OwnerID: testCase.request.OwnerID, Purpose: purpose, Window: usage.window,
					}).
					Return(&usage.used, usage.err)

				admitted = admitted && usage.err == nil
			}

			if testCase.expectRefusal {
				settleDao.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationSettleRequest) bool {
						// Settled before any start, naming the cap, so no later check starts it.
						return request.ID == testGenerationID && request.Attempt == 0 &&
							request.StartRequestedAt == nil && request.ProviderCallID == nil &&
							request.Status == dao.GenerationStatusFailed &&
							strings.HasPrefix(*request.Error, "usage cap reached: ")
					})).
					Return(&dao.Generation{}, testCase.settleErr)
			}

			if testCase.daoMock != nil {
				submitDao.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationSubmitRequest) bool {
						// The payload is stored exactly as sent, and the digest is derived rather than
						// left empty.
						return request.Purpose == testCase.request.Purpose &&
							request.Provider == testCase.request.Provider &&
							string(request.Payload) == string(testCase.request.Payload) &&
							request.MaxAttempts == maxAttempts &&
							len(request.RequestKey) == 32 &&
							request.ID != uuid.Nil
					})).
					Return(testCase.daoMock.resp, testCase.daoMock.err)

				if created && admitted {
					check.EXPECT().
						Exec(mock.Anything, &core.GenerationCheckRequest{Generation: testCase.daoMock.resp.Generation}).
						Return(testCase.daoMock.resp.Generation, testCase.checkErr)
				}

				if testCase.daoMock.resp != nil && testCase.checkErr == nil && (!created || admitted) {
					attemptDao.EXPECT().
						Exec(mock.Anything, &dao.GenerationAttemptListRequest{GenerationID: testGenerationID}).
						Return([]*dao.GenerationAttempt{}, testCase.attemptErr)
				}
			}

			service, err := core.NewGenerationSubmit(
				core.GenerationSubmitConfig{MaxAttempts: maxAttempts, Retention: time.Hour, Caps: testCaps},
				testConnectors(t),
				core.GenerationSubmitDaos{
					Submit: submitDao, Attempts: attemptDao, Usage: usageDao, Live: liveDao, Settle: settleDao,
				},
				check,
			)
			require.NoError(t, err)

			result, err := service.Exec(t.Context(), testCase.request)
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
			settleDao.AssertExpectations(t)
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
			// A resend rebuilt from the caller's state may reorder keys or reformat; it is still the
			// same request.
			name: "ReformattedJSON",

			adjust: func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`{ "input" : "Continue the scene.", "model" : "a-model" }`)
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

	service, err := core.NewGenerationSubmit(
		core.GenerationSubmitConfig{MaxAttempts: 1, Retention: time.Hour, Caps: testCaps},
		testConnectors(t),
		core.GenerationSubmitDaos{Submit: submitDao},
		coremocks.NewMockGenerationSubmitServiceCheck(t),
	)
	if err != nil {
		panic(err)
	}

	_, err = service.Exec(t.Context(), request)
	if !errors.Is(err, errFoo) {
		panic(err)
	}

	return key
}

func TestNewGenerationSubmit(t *testing.T) {
	t.Parallel()

	someLimit := map[time.Duration]config.Limit{time.Hour: {InputTokens: new(int64(1))}}
	someConcurrency := map[string]int{config.DefaultTier: 1}
	someTiers := map[string]config.TierCaps{testPurpose: {config.DefaultTier: {Windows: someLimit}}}

	testCases := []struct {
		name string

		caps config.Caps

		expectErr error
	}{
		{
			// The caps built into the image must always pass, or the service cannot start.
			name: "Success/Embedded",

			caps: config.CapsDefault,
		},
		{
			name: "Error/PurposeWithoutDefault",

			caps: config.Caps{
				Purposes:    map[string]config.TierCaps{testPurpose: {"admin": {Unlimited: true}}},
				Global:      config.TierCaps{config.DefaultTier: {Windows: someLimit}},
				Concurrency: someConcurrency,
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/GlobalWithoutDefault",

			caps: config.Caps{
				Purposes:    map[string]config.TierCaps{testPurpose: {config.DefaultTier: {Windows: someLimit}}},
				Concurrency: someConcurrency,
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			// An empty map is neither unlimited nor forbidden: a typo, not a policy.
			name: "Error/NoWindow",

			caps: config.Caps{
				Purposes:    map[string]config.TierCaps{testPurpose: {config.DefaultTier: {}}},
				Global:      config.TierCaps{config.DefaultTier: {Windows: someLimit}},
				Concurrency: someConcurrency,
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/LimitsNothing",

			caps: config.Caps{
				Purposes: map[string]config.TierCaps{testPurpose: {config.DefaultTier: {
					Windows: map[time.Duration]config.Limit{time.Hour: {}},
				}}},
				Global:      config.TierCaps{config.DefaultTier: {Windows: someLimit}},
				Concurrency: someConcurrency,
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NegativeLimit",

			caps: config.Caps{
				Purposes: map[string]config.TierCaps{testPurpose: {config.DefaultTier: {
					Windows: map[time.Duration]config.Limit{time.Hour: {OutputTokens: new(int64(-1))}},
				}}},
				Global:      config.TierCaps{config.DefaultTier: {Windows: someLimit}},
				Concurrency: someConcurrency,
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/ConcurrencyWithoutDefault",

			caps: config.Caps{
				Purposes:    someTiers,
				Global:      config.TierCaps{config.DefaultTier: {Windows: someLimit}},
				Concurrency: map[string]int{"admin": -1},
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/ConcurrencyBelowUnlimited",

			caps: config.Caps{
				Purposes:    someTiers,
				Global:      config.TierCaps{config.DefaultTier: {Windows: someLimit}},
				Concurrency: map[string]int{config.DefaultTier: -2},
			},

			expectErr: core.ErrInvalidRequest,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := core.NewGenerationSubmit(
				core.GenerationSubmitConfig{MaxAttempts: 1, Retention: time.Hour, Caps: testCase.caps},
				testConnectors(t),
				core.GenerationSubmitDaos{},
				coremocks.NewMockGenerationSubmitServiceCheck(t),
			)
			require.ErrorIs(t, err, testCase.expectErr)
		})
	}
}
