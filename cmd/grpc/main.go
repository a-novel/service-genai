// Command grpc serves the generation API, internal only, and its calls in flight to an autoscaler.
// cmd/sweep checks the generations nobody polled, and cmd/migrations applies the database schema.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/samber/lo"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/a-novel-kit/golib/downtime"
	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/httpf"
	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/config/env"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/handlers"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

func main() {
	cfg := config.AppPresetDefault
	lo.Must0(cfg.Validate())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	otel.SetAppName(cfg.App.Name)

	lo.Must0(otel.Init(cfg.Otel))
	defer cfg.Otel.Flush()

	if env.OTLPEndpoint == "" {
		log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))
	}

	// A server started during a planned downtime refuses work, so it leaves the database alone.
	if !downtime.Started(cfg.App.DowntimeStart, time.Now()) {
		ctx = lo.Must(postgres.NewContext(ctx, cfg.Postgres))

		database := lo.Must(cfg.Postgres.DB(ctx))
		defer closeDatabase(database)
	}

	// =================================================================================================================
	// DAO
	// =================================================================================================================

	daoAttemptInsert := dao.NewPgGenerationAttemptInsert()
	daoAttemptList := dao.NewPgGenerationAttemptList()
	daoCountLive := dao.NewPgGenerationCountLive()
	daoGet := dao.NewPgGenerationGet()
	daoQueueDepth := dao.NewPgGenerationQueueDepth()
	daoRecordProviderCall := dao.NewPgGenerationRecordProviderCall()
	daoRequestCancel := dao.NewPgGenerationRequestCancel()
	daoSettle := dao.NewPgGenerationSettle()
	daoSubmit := dao.NewPgGenerationSubmit()
	daoUsageInsert := dao.NewPgGenerationUsageInsert()
	daoUsageSum := dao.NewPgGenerationUsageSum()

	// =================================================================================================================
	// SERVICES
	// =================================================================================================================

	registry := lo.Must(connectors.NewRegistry(connectors.OpenAIConfig(cfg.OpenAI)))

	transactor := postgres.NewTransactor(nil)

	serviceCheck := core.NewGenerationCheck(
		daoRecordProviderCall,
		daoSettle,
		daoAttemptInsert,
		daoUsageInsert,
		daoGet,
		registry,
		transactor,
		cfg.Logger,
		cfg.Generations,
	)
	serviceSubmit := core.NewGenerationSubmit(
		daoSubmit,
		daoAttemptList,
		daoUsageSum,
		daoCountLive,
		serviceCheck,
		registry,
		transactor,
		cfg.Purposes,
	)
	serviceGet := core.NewGenerationGet(daoGet, daoAttemptList, serviceCheck)
	serviceCancel := core.NewGenerationCancel(daoRequestCancel, daoGet, daoAttemptList, serviceCheck, cfg.Generations)
	serviceStream := core.NewGenerationStream(daoGet, registry)
	serviceQueueDepth := core.NewGenerationQueueDepth(daoQueueDepth)
	serviceUsageSummary := core.NewUsageSummary(daoCountLive, daoUsageSum, cfg.Purposes)

	// =================================================================================================================
	// HANDLERS
	// =================================================================================================================

	handlerStatus := handlers.NewGrpcStatus(serviceQueueDepth)
	handlerSubmit := handlers.NewGrpcGenerationSubmit(serviceSubmit)
	handlerGet := handlers.NewGrpcGenerationGet(serviceGet)
	handlerCancel := handlers.NewGrpcGenerationCancel(serviceCancel)
	handlerStream := handlers.NewGrpcGenerationStream(serviceStream)
	handlerUsageSummary := handlers.NewGrpcUsageSummary(serviceUsageSummary)

	// =================================================================================================================
	// SERVER
	// =================================================================================================================

	callLimit := grpcf.NewCallLimit(cfg.Grpc.MaxConcurrentCalls)
	server := lo.Must(newServer(ctx, cfg, callLimit))

	healthcheck := grpcf.RegisterEchoServers(server)
	healthcheck.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	go func() {
		<-ctx.Done()
		healthcheck.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	}()

	genaiv0.RegisterStatusServiceServer(server, handlerStatus)
	genaiv0.RegisterGenerationSubmitServiceServer(server, handlerSubmit)
	genaiv0.RegisterGenerationGetServiceServer(server, handlerGet)
	genaiv0.RegisterGenerationCancelServiceServer(server, handlerCancel)
	genaiv0.RegisterGenerationStreamServiceServer(server, handlerStream)
	genaiv0.RegisterUsageSummaryServiceServer(server, handlerUsageSummary)

	reflection.Register(server)

	// =================================================================================================================
	// RUN
	// =================================================================================================================

	log.Println("Starting gRPC server on :" + strconv.Itoa(cfg.Grpc.Port))
	log.Println("Serving the calls in flight on :" + strconv.Itoa(cfg.Metrics.Port))

	// Either server stops the other when it ends, so the process never serves calls it cannot report.
	metrics := make(chan error, 1)

	go func() {
		metrics <- httpf.ListenAndServe(ctx, cfg.Metrics, callLimit)

		stop()
	}()

	err := grpcf.ListenAndServe(ctx, cfg.Grpc, server)

	stop()

	err = errors.Join(err, <-metrics)
	if err != nil {
		panic(err)
	}
}

// newServer builds the gRPC server under callLimit. Each call carries the database of ctx, and passes
// the service-token gate first. The gate refuses an empty issuer unless cfg opts a local stack out, so
// the server never starts open by mistake.
func newServer(ctx context.Context, cfg config.App, callLimit *grpcf.CallLimit) (*grpc.Server, error) {
	serviceToken, err := handlers.GrpcServiceToken(
		cfg.ServiceToken, cfg.ServiceIssuerOptional, httpf.NewPoolClient(httpf.PoolOptions{}),
	)
	if err != nil {
		return nil, fmt.Errorf("service token gate: %w", err)
	}

	ctxInterceptor := func(rpCtx context.Context) context.Context {
		return postgres.TransferContext(ctx, rpCtx)
	}

	options := slices.Concat(handlers.GrpcServerOptions(callLimit), serviceToken)

	return grpcf.NewServer(ctx, cfg.Otel, cfg.GrpcLogger, cfg.App.DowntimeStart, ctxInterceptor, options...), nil
}

func closeDatabase(database io.Closer) {
	err := database.Close()
	if err != nil {
		log.Println("Close Postgres: " + err.Error())
	}
}
