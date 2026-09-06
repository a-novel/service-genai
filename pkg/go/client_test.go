package servicegenai_test

import (
	"context"
	"testing"
	"time"

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

// submit records a generation for the owner, returning it.
func submit(t *testing.T, client servicegenai.Client, owner string) *servicegenai.Generation {
	t.Helper()

	response, err := client.GenerationSubmit(t.Context(), &servicegenai.GenerationSubmitRequest{
		OwnerId: owner, Purpose: "studio.generation",
		IdempotencyKey: uuid.Must(uuid.NewV7()).String(),
		Request:        []byte(`{"model": "a-model"}`),
	})
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
	request := []byte(`{"model": "a-model", "input": "write"}`)

	created, err := client.GenerationSubmit(t.Context(), &servicegenai.GenerationSubmitRequest{
		OwnerId: owner, Purpose: "studio.generation", IdempotencyKey: key,
		Request: request, MaxAttempts: 1,
	})
	require.NoError(t, err)
	require.True(t, created.GetCreated())

	// A retry attaches to the work already in flight rather than paying for a second run.
	replayed, err := client.GenerationSubmit(t.Context(), &servicegenai.GenerationSubmitRequest{
		OwnerId: owner, Purpose: "studio.generation", IdempotencyKey: key,
		Request: request, MaxAttempts: 1,
	})
	require.NoError(t, err)
	require.False(t, replayed.GetCreated())
	require.Equal(t, created.GetGeneration().GetId(), replayed.GetGeneration().GetId())

	// The same key with different content is a caller bug, not a replay.
	_, err = client.GenerationSubmit(t.Context(), &servicegenai.GenerationSubmitRequest{
		OwnerId: owner, Purpose: "studio.generation", IdempotencyKey: key,
		Request: []byte(`{"model": "a-model", "input": "something else"}`), MaxAttempts: 1,
	})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// An unkeyed submission of a priced call is refused rather than defaulted.
	_, err = client.GenerationSubmit(t.Context(), &servicegenai.GenerationSubmitRequest{
		OwnerId: owner, Purpose: "studio.generation", Request: request,
	})
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

// Cancelling records the request and leaves the status alone: the worker settles it once the
// provider operation has actually stopped, so what was spent before the stop is still recorded.
func TestClientGenerationCancel(t *testing.T) {
	t.Parallel()

	client := newClient(t)

	owner := uuid.Must(uuid.NewV7()).String()
	generation := submit(t, client, owner)

	cancelled, err := client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
		Id: generation.GetId(), OwnerId: owner,
	})
	require.NoError(t, err)
	require.NotEqual(t, servicegenai.GenerationStatusCancelled, cancelled.GetGeneration().GetStatus())

	// Another owner cannot stop it, and is told the same thing as if it did not exist.
	_, err = client.GenerationCancel(t.Context(), &servicegenai.GenerationCancelRequest{
		Id: generation.GetId(), OwnerId: uuid.Must(uuid.NewV7()).String(),
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// The stream answers from durable state, so a subscribe yields the generation as it stands even
// when nothing has changed since it was submitted.
func TestClientGenerationWatch(t *testing.T) {
	t.Parallel()

	client := newClient(t)

	owner := uuid.Must(uuid.NewV7()).String()
	generation := submit(t, client, owner)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	stream, err := client.GenerationWatch(ctx, &servicegenai.GenerationWatchRequest{
		Id: generation.GetId(), OwnerId: owner,
	})
	require.NoError(t, err)

	first, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, generation.GetId(), first.GetGeneration().GetId())

	// Another owner's watch is refused on the first receive, the same as a read.
	otherStream, err := client.GenerationWatch(t.Context(), &servicegenai.GenerationWatchRequest{
		Id: generation.GetId(), OwnerId: uuid.Must(uuid.NewV7()).String(),
	})
	require.NoError(t, err)

	_, err = otherStream.Recv()
	require.Equal(t, codes.NotFound, status.Code(err))
}

// The usage surface over the wire. A generation that has not run yet consumed nothing, so an owner
// with only fresh submissions reports an empty set with a zero total — not an error, and not a nil
// total a caller would have to guard.
func TestClientUsageQuery(t *testing.T) {
	t.Parallel()

	client := newClient(t)

	owner := uuid.Must(uuid.NewV7()).String()
	submit(t, client, owner)

	now := time.Now()

	response, err := client.UsageQuery(t.Context(), &servicegenai.UsageQueryRequest{
		OwnerId: owner,
		From:    now.Add(-time.Hour).Format(time.RFC3339),
		To:      now.Add(time.Hour).Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Empty(t, response.GetGroups())
	require.NotNil(t, response.GetTotal())
	require.Zero(t, response.GetTotal().GetAttempts())

	// The window is required: this record is never purged, so an unbounded scan grows without limit.
	_, err = client.UsageQuery(t.Context(), &servicegenai.UsageQueryRequest{OwnerId: owner})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
