// Command grpc serves the generation API. It is the service's only network
// entrypoint; cmd/migrations applies the database schema.
//
// The service is internal: callers are other services, never a browser, so
// there is no HTTP surface to serve alongside it.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/samber/lo"
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

var (
	errLoopExitedUnexpectedly = errors.New("background loop exited unexpectedly")
	errLoopPanicked           = errors.New("background loop panicked")
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

	daoGet := dao.NewGenerationGet()
	daoUsageList := dao.NewGenerationUsageList()

	// =================================================================================================================
	// SERVICES
	// =================================================================================================================

	provider := lo.Must(newProvider(cfg.Provider))

	serviceCheck := lo.Must(core.NewGenerationCheck(
		core.GenerationCheckConfig{
			Retention: cfg.Retention, ProviderName: cfg.Provider.Name, ProviderEpoch: cfg.Provider.Epoch,
		},
		cfg.Log,
		provider,
		postgres.NewTransactor(nil),
		core.GenerationCheckDaos{
			BeginStart:   dao.NewGenerationBeginStart(),
			ReleaseStart: dao.NewGenerationReleaseStart(),
			Record:       dao.NewGenerationRecordProviderCall(),
			Settle:       dao.NewGenerationSettle(),
			Requeue:      dao.NewGenerationRequeue(),
			Restart:      dao.NewGenerationRestart(),
			Usage:        dao.NewGenerationUsageInsert(),
			Get:          daoGet,
		},
	))

	serviceSubmit := lo.Must(core.NewGenerationSubmit(
		core.GenerationSubmitConfig{MaxAttempts: cfg.MaxAttempts},
		dao.NewGenerationSubmit(),
		daoUsageList,
		serviceCheck,
	))
	serviceGet := lo.Must(core.NewGenerationGet(
		core.GenerationGetConfig{CheckInterval: cfg.CheckInterval, ProviderEpoch: cfg.Provider.Epoch},
		daoGet,
		dao.NewGenerationElectCheck(),
		daoUsageList,
		serviceCheck,
	))
	serviceCancel := lo.Must(core.NewGenerationCancel(
		core.GenerationCancelConfig{Retention: cfg.Retention},
		dao.NewGenerationRequestCancel(),
		daoGet,
		daoUsageList,
		serviceCheck,
	))
	serviceQueueDepth := core.NewQueueDepth(dao.NewGenerationQueueDepth())
	serviceTierList := core.NewTierList(providerTiers(cfg.Provider))

	sweep := lo.Must(core.NewGenerationSweep(
		core.GenerationSweepConfig{
			Interval: cfg.Sweep.Interval, BatchSize: cfg.Sweep.BatchSize, ProviderEpoch: cfg.Provider.Epoch,
		},
		dao.NewGenerationSweep(),
		serviceCheck,
	))

	// =================================================================================================================
	// HANDLERS
	// =================================================================================================================

	handlerStatus := handlers.NewGrpcStatus(serviceQueueDepth)
	handlerSubmit := handlers.NewGrpcGenerationSubmit(serviceSubmit)
	handlerGet := handlers.NewGrpcGenerationGet(serviceGet)
	handlerCancel := handlers.NewGrpcGenerationCancel(serviceCancel)
	handlerTierList := handlers.NewGrpcTierList(serviceTierList)

	// =================================================================================================================
	// SERVER
	// =================================================================================================================

	ctxInterceptor := func(rpCtx context.Context) context.Context {
		return postgres.TransferContext(ctx, rpCtx)
	}

	server := grpc.NewServer(
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

	beginShutdown := sync.OnceFunc(func() {
		healthcheck.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		stop()
	})

	genaiv0.RegisterStatusServiceServer(server, handlerStatus)
	genaiv0.RegisterGenerationSubmitServiceServer(server, handlerSubmit)
	genaiv0.RegisterGenerationGetServiceServer(server, handlerGet)
	genaiv0.RegisterGenerationCancelServiceServer(server, handlerCancel)
	genaiv0.RegisterTierListServiceServer(server, handlerTierList)

	reflection.Register(server)

	// =================================================================================================================
	// RUN
	// =================================================================================================================

	// The sweep runs in this process and takes the boot context: a request context dies at the
	// server's own timeout. It only catches generations nobody polled, so one loop per replica is
	// enough.
	log.Println("Starting gRPC server on :" + strconv.Itoa(cfg.Grpc.Port))

	serveErr := runProcess(
		ctx,
		beginShutdown,
		func(ctx context.Context) error {
			return grpcf.Serve(ctx, server, fmt.Sprintf("0.0.0.0:%d", cfg.Grpc.Port), cfg.Grpc.Shutdown)
		},
		func(ctx context.Context) error {
			return runLoop(ctx, cfg.Log, "generation-sweep", cfg.Sweep.Interval, 0, sweep.RunOnce)
		},
	)
	if serveErr != nil {
		panic(serveErr)
	}
}

// runProcess keeps the server and its background loops under one owner. Process cancellation or
// the first component to stop makes the service unavailable and cancels its siblings; all
// components are joined before the caller closes the database and flushes telemetry.
func runProcess(
	ctx context.Context,
	beginShutdown func(),
	components ...func(context.Context) error,
) error {
	results := make(chan error, len(components))

	for _, component := range components {
		go func() {
			results <- component(ctx)
		}()
	}

	var err error

	completed := 0

	select {
	case err = <-results:
		completed = 1
	case <-ctx.Done():
	}

	beginShutdown()

	for completed < len(components) {
		err = errors.Join(err, <-results)
		completed++
	}

	return err
}

func closeDatabase(database io.Closer) {
	err := database.Close()
	if err != nil {
		log.Println("Close Postgres: " + err.Error())
	}
}

// runLoop drives a background loop until cancellation. A panic or premature return becomes a
// process error without including the panic payload, which may contain private provider data.
func runLoop(
	ctx context.Context,
	logger logging.Log,
	name string,
	interval, stagger time.Duration,
	fn func(context.Context) (bool, error),
) error {
	return observeLoop(ctx, name, func() {
		worker.Poll(ctx, logger, name, interval, stagger, fn)
	})
}

// observeLoop turns a loop boundary into a normal cancellation or a safe, named process failure.
func observeLoop(ctx context.Context, name string, loop func()) (err error) {
	ctx, span := otel.Tracer().Start(ctx, "cmd.runLoop")
	defer span.End()
	defer func() {
		if recover() != nil {
			err = otel.ReportError(span, fmt.Errorf("%s: %w", name, errLoopPanicked))
		}
	}()

	span.SetAttributes(attribute.String("loop.name", name))

	loop()

	if ctx.Err() != nil {
		return nil
	}

	return otel.ReportError(span, fmt.Errorf("%s: %w", name, errLoopExitedUnexpectedly))
}

// newProvider builds the adapter the server runs. The conformance check builds it the same way, so
// what the check proves is what the server runs.
func newProvider(provider config.Provider) (*lib.OpenAI, error) {
	return lib.NewOpenAI(lib.OpenAIConfig{
		BaseURL: provider.BaseURL, APIKey: provider.APIKey, Tiers: providerTiers(provider),
	})
}

// providerTiers converts the configured bindings to the adapter's. A name that is not a Tier is
// dropped; a Tier left unbound fails the adapter's construction.
func providerTiers(provider config.Provider) map[lib.Tier]lib.TierBinding {
	tiers := make(map[lib.Tier]lib.TierBinding, len(provider.Tiers))

	for name, binding := range provider.Tiers {
		tiers[lib.Tier(name)] = lib.TierBinding{
			Model:           binding.Model,
			ReasoningEffort: binding.ReasoningEffort,
			MaxInputTokens:  binding.MaxInputTokens,
			MaxOutputTokens: binding.MaxOutputTokens,
		}
	}

	return tiers
}
