// Command grpc serves the generation API. It is the service's only network
// entrypoint; cmd/migrations applies the database schema.
//
// The service is internal: callers are other services, never a browser, so
// there is no HTTP surface to serve alongside it.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/samber/lo"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
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
	"github.com/a-novel/service-genai/internal/handlers/protogen"
	"github.com/a-novel/service-genai/internal/lib"
)

func main() {
	cfg := config.AppPresetDefault
	ctx := context.Background()

	otel.SetAppName(cfg.App.Name)

	lo.Must0(otel.Init(cfg.Otel))
	defer cfg.Otel.Flush()

	if env.GcloudProjectId == "" {
		log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))
	}

	ctx = lo.Must(postgres.NewContext(ctx, config.PostgresPresetDefault))

	// =================================================================================================================
	// DAO
	// =================================================================================================================

	daoClaim := dao.NewGenerationClaim()
	daoRecordProviderCall := dao.NewGenerationRecordProviderCall()
	daoSettle := dao.NewGenerationSettle()
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
		provider,
		postgres.NewTransactor(nil),
		core.WorkerDaos{
			Claim:   daoClaim,
			Record:  daoRecordProviderCall,
			Settle:  daoSettle,
			Requeue: daoRequeue,
			Usage:   daoUsageInsert,
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

	listenerConfig := new(net.ListenConfig)
	listener := lo.Must(listenerConfig.Listen(ctx, "tcp", fmt.Sprintf("0.0.0.0:%d", cfg.Grpc.Port)))
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

	grpcf.SetEchoServersContext(ctx, server, cfg.Grpc.Ping)

	protogen.RegisterStatusServiceServer(server, handlerStatus)
	protogen.RegisterGenerationSubmitServiceServer(server, handlerSubmit)
	protogen.RegisterGenerationGetServiceServer(server, handlerGet)
	protogen.RegisterGenerationCancelServiceServer(server, handlerCancel)
	protogen.RegisterGenerationWatchServiceServer(server, handlerWatch)
	protogen.RegisterUsageQueryServiceServer(server, handlerUsageQuery)

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
	go runLoop(ctx, cfg.Log, "generation-worker", cfg.Worker.Interval, 0, generationWorker.RunOnce)
	go runLoop(ctx, cfg.Log, "generation-reaper", cfg.Reaper.Interval, time.Second, reaper.RunOnce)

	log.Println("Starting gRPC server on :" + strconv.Itoa(cfg.Grpc.Port))

	go func() {
		err := server.Serve(listener)
		if err != nil {
			panic(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down gRPC server...")
	server.GracefulStop()
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
