package handlers_test

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
	"github.com/a-novel/service-genai/internal/lib"
)

func TestGrpcTierList(t *testing.T) {
	t.Parallel()

	// A Tier is described by its ceilings alone. The model and effort behind it change with the
	// provider, so a field naming either would become a contract callers could not rely on.
	ceilingFields := []string{"tier", "max_input_tokens", "max_output_tokens"}

	fields := (&genaiv0.TierCeilings{}).ProtoReflect().Descriptor().Fields()
	for index := range fields.Len() {
		require.Contains(t, ceilingFields, string(fields.Get(index).Name()))
	}

	testCases := []struct {
		name string

		tiers []core.TierCeilings

		expect *genaiv0.TierListResponse
	}{
		{
			name: "Success",

			tiers: []core.TierCeilings{
				{Tier: lib.TierFast, MaxInputTokens: 100, MaxOutputTokens: 10},
				{Tier: lib.TierBalanced, MaxInputTokens: 200, MaxOutputTokens: 20},
				{Tier: lib.TierDeep, MaxInputTokens: 300, MaxOutputTokens: 30},
			},

			expect: &genaiv0.TierListResponse{Tiers: []*genaiv0.TierCeilings{
				{Tier: genaiv0.Tier_TIER_FAST, MaxInputTokens: 100, MaxOutputTokens: 10},
				{Tier: genaiv0.Tier_TIER_BALANCED, MaxInputTokens: 200, MaxOutputTokens: 20},
				{Tier: genaiv0.Tier_TIER_DEEP, MaxInputTokens: 300, MaxOutputTokens: 30},
			}},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockGrpcTierListService(t)
			service.EXPECT().Exec(mock.Anything).Return(testCase.tiers)

			response, err := handlers.NewGrpcTierList(service).TierList(t.Context(), &genaiv0.TierListRequest{})
			require.NoError(t, err)
			require.Equal(t, testCase.expect, response)
		})
	}
}
