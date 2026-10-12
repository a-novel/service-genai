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
		Generations: config.Generations{CheckInterval: 2 * time.Second, MaxAttempts: 2, Retention: time.Hour},
		Sweep:       config.Sweep{Interval: time.Minute, BatchSize: 50},
		Caps:        config.CapsDefault,
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
			name: "Error/NoCheckInterval",

			adjust: func(app *config.App) { app.Generations.CheckInterval = 0 },

			expectErr: config.ErrInvalidConfig,
		},
		{
			name: "Error/MaxAttemptsAboveCeiling",

			adjust: func(app *config.App) { app.Generations.MaxAttempts = 11 },

			expectErr: config.ErrInvalidConfig,
		},
		{
			// The purge would end an unknown outcome's hold on its owner's slot early.
			name: "Error/RetentionBelowHold",

			adjust: func(app *config.App) { app.Generations.Retention = 10 * time.Minute },

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
