package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
)

func TestCeilingDefault(t *testing.T) {
	t.Parallel()

	// The ceiling built into the image reaches the service as written in its file.
	require.Equal(t, config.Cap{
		Concurrency: new(8),
		Windows: map[time.Duration]config.Limit{
			720 * time.Hour: {InputTokens: new(int64(500_000_000)), OutputTokens: new(int64(50_000_000))},
		},
	}, config.CeilingDefault)
}
