// Command grpc serves the generation API. It is the service's only network
// entrypoint; cmd/migrations applies the database schema.
//
// The service is internal: callers are other services, never a browser, so
// there is no HTTP surface to serve alongside it.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/samber/lo"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/logging"
	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/worker"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/config/env"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/handlers"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
	"github.com/a-novel/service-genai/internal/lib"
)

func main() {
	cfg := config.AppPresetDefault

	processCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ctx := processCtx

	otel.SetAppName(cfg.App.Name)

	lo.Must0(otel.Init(cfg.Otel))
	defer cfg.Otel.Flush()

	if env.GcloudProjectId == "" {
		log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))
	}

	ctx = lo.Must(postgres.NewContext(ctx, cfg.Postgres))

	database := lo.Must(cfg.Postgres.DB(ctx))
	defer closeDatabase(database)

	// =================================================================================================================
	// DAO
	// =================================================================================================================

	daoClaim := dao.NewGenerationClaim()
	daoRecordProviderCall := dao.NewGenerationRecordProviderCall()
	daoSettle := dao.NewGenerationSettle()
	daoObserveLater := dao.NewGenerationObserveLater()
	daoRequeue := dao.NewGenerationRequeue()
	daoUsageInsert := dao.NewGenerationUsageInsert()
	daoReap := dao.NewGenerationReap()
	daoSubmit := dao.NewGenerationSubmit()
	daoGet := dao.NewGenerationGet()
	daoRequestCancel := dao.NewGenerationRequestCancel()
	daoQueueDepth := dao.NewGenerationQueueDepth()
	daoUsageQuery := dao.NewGenerationUsageQuery()

	// =================================================================================================================
	// SERVICES
	// =================================================================================================================

	provider := lib.NewOpenAI(providerOptions(cfg.Provider)...)

	generationWorker := lo.Must(core.NewWorker(
		core.WorkerConfig{
			ID:           cfg.Worker.ID,
			Lease:        cfg.Worker.Lease,
			BatchSize:    cfg.Worker.BatchSize,
			PollInterval: cfg.Worker.PollInterval,
			Retention:    cfg.Retention,
		},
		cfg.Log,
		provider,
		postgres.NewTransactor(nil),
		core.WorkerDaos{
			Claim:        daoClaim,
			Record:       daoRecordProviderCall,
			Settle:       daoSettle,
			ObserveLater: daoObserveLater,
			Requeue:      daoRequeue,
			Usage:        daoUsageInsert,
		},
	))

	serviceSubmit := core.NewGenerationSubmit(daoSubmit)
	serviceGet := core.NewGenerationGet(daoGet)
	serviceCancel := core.NewGenerationCancel(daoRequestCancel)
	serviceQueueDepth := core.NewQueueDepth(daoQueueDepth)
	serviceUsageQuery := core.NewUsageQuery(daoUsageQuery)

	reaper := lo.Must(core.NewReaper(
		core.ReaperConfig{
			Grace:     cfg.Reaper.Grace,
			BatchSize: cfg.Reaper.BatchSize,
			Retention: cfg.Retention,
		},
		daoReap,
	))

	// =================================================================================================================
	// HANDLERS
	// =================================================================================================================

	handlerStatus := handlers.NewGrpcStatus(serviceQueueDepth)
	handlerSubmit := handlers.NewGrpcGenerationSubmit(serviceSubmit)
	handlerGet := handlers.NewGrpcGenerationGet(serviceGet)
	handlerCancel := handlers.NewGrpcGenerationCancel(serviceCancel)
	handlerWatch := handlers.NewGrpcGenerationWatch(handlerGet)
	handlerUsageQuery := handlers.NewGrpcUsageQuery(serviceUsageQuery)

	// =================================================================================================================
	// SERVER
	// =================================================================================================================

	ctxInterceptor := func(rpCtx context.Context) context.Context {
		return postgres.TransferContext(ctx, rpCtx)
	}

	server := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		cfg.Otel.RpcInterceptor(),
		grpc.ChainUnaryInterceptor(
			grpcf.BaseContextUnaryInterceptor(ctxInterceptor),
			cfg.Logger.UnaryInterceptor(),
			cfg.Logger.PanicUnaryInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			grpcf.BaseContextStreamInterceptor(ctxInterceptor),
			cfg.Logger.StreamInterceptor(),
			cfg.Logger.PanicStreamInterceptor(),
		),
	)

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
	genaiv0.RegisterGenerationWatchServiceServer(server, handlerWatch)
	genaiv0.RegisterUsageQueryServiceServer(server, handlerUsageQuery)

	reflection.Register(server)

	// =================================================================================================================
	// RUN
	// =================================================================================================================

	// The worker and the reaper run in this process. Neither needs a network hop, and an always-on
	// container is already paid for.
	//
	// They take the boot context, not a request one: a request context dies at the server's own
	// timeout, and a generation outlives that by design. The stagger keeps two loops sharing an
	// interval from waking together.
	var loops sync.WaitGroup

	loops.Add(1)
	go func() {
		defer loops.Done()

		runLoop(ctx, cfg.Log, "generation-worker", cfg.Worker.Interval, 0, generationWorker.RunOnce)
	}()

	loops.Add(1)
	go func() {
		defer loops.Done()

		runLoop(ctx, cfg.Log, "generation-reaper", cfg.Reaper.Interval, time.Second, reaper.RunOnce)
	}()

	log.Println("Starting gRPC server on :" + strconv.Itoa(cfg.Grpc.Port))

	serveErr := grpcf.Serve(ctx, server, fmt.Sprintf("0.0.0.0:%d", cfg.Grpc.Port), cfg.Grpc.Shutdown)

	// A serving failure must stop the process-owned loops too. Waiting here keeps their spans and
	// database calls ahead of the deferred resource close and telemetry flush.
	stop()
	loops.Wait()

	if serveErr != nil {
		panic(serveErr)
	}
}

func closeDatabase(database io.Closer) {
	err := database.Close()
	if err != nil {
		log.Println("Close Postgres: " + err.Error())
	}
}

// runLoop drives a background loop under panic recovery.
//
// The OpenTelemetry SDK does not recover panics, and a panic escaping a goroutine takes the whole
// process with it — including the gRPC server, which had nothing to do with the fault.
func runLoop(
	ctx context.Context,
	logger logging.Log,
	name string,
	interval, stagger time.Duration,
	fn func(context.Context) (bool, error),
) {
	ctx, span := otel.Tracer().Start(ctx, "cmd.runLoop")
	defer span.End()
	defer otel.RecoverPanic(ctx, span)

	span.SetAttributes(attribute.String("loop.name", name))

	worker.Poll(ctx, logger, name, interval, stagger, fn)
}

// providerOptions builds the client options from configuration. An empty base URL leaves the
// client's own default, so only a deployment pointing at another endpoint sets one.
func providerOptions(provider config.Provider) []option.RequestOption {
	opts := []option.RequestOption{option.WithAPIKey(provider.APIKey)}

	if provider.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(provider.BaseURL))
	}

	return opts
}
