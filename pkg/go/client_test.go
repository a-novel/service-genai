package servicegenai_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	golibproto "github.com/a-novel-kit/golib/grpcf/proto/gen"

	"github.com/a-novel/service-genai/internal/config/env"
	servicegenai "github.com/a-novel/service-genai/pkg/go"
)

// newClient dials the service this test runs against. It is a live container, not a mock: what is
// checked here is that the published contract works end to end.
func newClient(t *testing.T) servicegenai.Client {
	t.Helper()

	client, err := servicegenai.NewClient(env.GrpcUrl, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}

	t.Cleanup(client.Close)

	return client
}

// submitRequest is a valid submission under the given key.
func submitRequest(owner, key string) *servicegenai.GenerationSubmitRequest {
	return &servicegenai.GenerationSubmitRequest{
		OwnerId: owner, Purpose: "studio.generation", IdempotencyKey: key,
		Tier: servicegenai.TierBalanced, Instructions: "Continue the scene.",
		Input:        []byte(`{"scene": "a door"}`),
		OutputSchema: []byte(`{"type": "object", "properties": {}, "required": [], "additionalProperties": false}`),
	}
}

// submit records a generation for the owner, returning it.
func submit(t *testing.T, client servicegenai.Client, owner string) *servicegenai.Generation {
	t.Helper()

	response, err := client.GenerationSubmit(t.Context(), submitRequest(owner, uuid.Must(uuid.NewV7()).String()))
	if err != nil {
		panic(err)
	}

	return response.GetGeneration()
}

func TestClient(t *testing.T) {
	t.Parallel()

	client := newClient(t)

	_, err := client.UnaryEcho(t.Context(), &golibproto.UnaryEchoRequest{})
	require.NoError(t, err)

	response, err := client.Status(t.Context(), &servicegenai.StatusRequest{})
	require.NoError(t, err)
	require.NotNil(t, response.GetPostgres())
	require.NotNil(t, response.GetQueue())
}

// The submit contract against a running service: a fresh key creates, the same key replays onto the
// same generation, and a different request under that key is refused.
func TestClientGenerationSubmit(t *testing.T) {
	t.Parallel()

	client := newClient(t)

	owner := uuid.Must(uuid.NewV7()).String()
	key := uuid.Must(uuid.NewV7()).String()

	created, err := client.GenerationSubmit(t.Context(), submitRequest(owner, key))
	require.NoError(t, err)
	require.True(t, created.GetCreated())

	// A retry attaches to the work already in flight rather than paying for a second run.
	replayed, err := client.GenerationSubmit(t.Context(), submitRequest(owner, key))
	require.NoError(t, err)
	require.False(t, replayed.GetCreated())
	require.Equal(t, created.GetGeneration().GetId(), replayed.GetGeneration().GetId())

	// The same key with different content is a caller bug, not a replay.
	different := submitRequest(owner, key)
	different.Instructions = "Something else."
	_, err = client.GenerationSubmit(t.Context(), different)
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// A request without a Tier names no model to run.
	untiered := submitRequest(owner, uuid.Must(uuid.NewV7()).String())
	untiered.Tier = servicegenai.Tier(0)
	_, err = client.GenerationSubmit(t.Context(), untiered)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// The ownership predicate over the wire. Another owner's generation must be indistinguishable from
// one that does not exist, or an identifier can be probed.
func TestClientGenerationGet(t *testing.T) {
	t.Parallel()

	client := newClient(t)

	owner := uuid.Must(uuid.NewV7()).String()
	generation := submit(t, client, owner)

	read, err := client.GenerationGet(t.Context(), &servicegenai.GenerationGetRequest{
		Id: generation.GetId(), OwnerId: owner,
	})
	require.NoError(t, err)
	require.Equal(t, generation.GetId(), read.GetGeneration().GetId())

	_, err = client.GenerationGet(t.Context(), &servicegenai.GenerationGetRequest{
		Id: generation.GetId(), OwnerId: uuid.Must(uuid.NewV7()).String(),
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// A generation whose start never reached the provider is cancelled at once: there is no call to stop.
func TestClientGenerationCancel(t *testing.T) {
	t.Parallel()

	client := newClient(t)

	owner := uuid.Must(uuid.NewV7()).String()
	generation := submit(t, client, owner)

	cancelled, err := client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
		Id: generation.GetId(), OwnerId: owner,
	})
	require.NoError(t, err)
	require.Equal(t, servicegenai.GenerationStatusCancelled, cancelled.GetGeneration().GetStatus())

	// Another owner cannot stop it, and is told the same thing as if it did not exist.
	_, err = client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
		Id: generation.GetId(), OwnerId: uuid.Must(uuid.NewV7()).String(),
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}
