package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/servicetoken"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/handlers"
)

func TestNewServer(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		serviceToken   servicetoken.Config
		issuerOptional bool

		expectErr error
	}{
		{
			name: "Success",

			serviceToken: servicetoken.Config{
				Issuer:   "https://issuer.test",
				Audience: "genai",
				Callers:  []string{"system:serviceaccount:agora:caller"},
			},
		},
		{
			// A local stack opts out, and the server starts without the gate.
			name: "Success/IssuerOptional",

			issuerOptional: true,
		},
		{
			// A deployment that forgets the issuer stops at startup rather than admit every caller.
			name: "Error/IssuerRequired",

			expectErr: handlers.ErrServiceTokenIssuerRequired,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.AppPresetDefault
			cfg.ServiceToken = testCase.serviceToken
			cfg.ServiceIssuerOptional = testCase.issuerOptional

			server, err := newServer(t.Context(), cfg, grpcf.NewCallLimit(0))
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, server)

				return
			}

			require.NotNil(t, server)
			server.Stop()
		})
	}
}
