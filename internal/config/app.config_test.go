package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
)

func TestApp(t *testing.T) {
	t.Parallel()

	valid := config.App{
		Generations: config.Generations{Retention: time.Hour},
		Sweep:       config.Sweep{Interval: time.Minute, BatchSize: 50, Concurrency: 8},
		Caps:        config.CapsDefault,
		Ceiling:     config.CeilingDefault,
	}

	testCases := []struct {
		name string

		adjust func(app *config.App)

		expectErr error
	}{
		{
			name: "Success",
		},
		{
			// A ceiling that allows nothing refuses every submit with caps: a deliberate policy.
			name: "Success/CeilingForbidden",

			adjust: func(app *config.App) { app.Ceiling = config.Cap{Forbidden: true} },
		},
		{
			// The purge would end an unknown outcome's hold on its owner's slot early.
			name: "Error/RetentionBelowHold",

			adjust: func(app *config.App) { app.Generations.Retention = 10 * time.Minute },

			expectErr: config.ErrInvalidConfig,
		},
		{
			// A sweep that runs no check at once never checks anything.
			name: "Error/NoSweepConcurrency",

			adjust: func(app *config.App) { app.Sweep.Concurrency = 0 },

			expectErr: config.ErrInvalidConfig,
		},
		{
			// A finished result stays retrievable for about ten minutes; a slower sweep would lose it.
			name: "Error/SweepIntervalAboveCeiling",

			adjust: func(app *config.App) { app.Sweep.Interval = 10 * time.Minute },

			expectErr: config.ErrInvalidConfig,
		},
		{
			name: "Error/SweepBatchSizeAboveCeiling",

			adjust: func(app *config.App) { app.Sweep.BatchSize = 101 },

			expectErr: config.ErrInvalidConfig,
		},
		{
			name: "Error/Caps",

			adjust: func(app *config.App) { app.Caps = config.Caps{} },

			expectErr: config.ErrInvalidConfig,
		},
		{
			// A user under caps of their own would run as many generations at once as they like.
			name: "Error/CeilingWithoutConcurrency",

			adjust: func(app *config.App) {
				app.Ceiling = config.Cap{Windows: map[time.Duration]config.Limit{time.Hour: {InputTokens: new(int64(1))}}}
			},

			expectErr: config.ErrInvalidConfig,
		},
		{
			name: "Error/CeilingLimitsNothing",

			adjust: func(app *config.App) {
				app.Ceiling = config.Cap{Concurrency: new(1), Windows: map[time.Duration]config.Limit{time.Hour: {}}}
			},

			expectErr: config.ErrInvalidConfig,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			app := valid
			if testCase.adjust != nil {
				testCase.adjust(&app)
			}

			require.ErrorIs(t, app.Validate(), testCase.expectErr)
		})
	}
}
