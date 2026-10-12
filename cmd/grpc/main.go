// Command grpc serves the generation API, internal only, and runs the sweep. cmd/rest receives the
// providers' notifications, and cmd/migrations applies the database schema.
package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/samber/lo"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/a-novel-kit/golib/downtime"
	"github.com/a-novel-kit/golib/grpcf"
	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/worker"

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

	processCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ctx := processCtx

	otel.SetAppName(cfg.App.Name)

	lo.Must0(otel.Init(cfg.Otel))
	defer cfg.Otel.Flush()

	if env.GcloudProjectId == "" {
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
	daoBeginStart := dao.NewPgGenerationBeginStart()
	daoCountLive := dao.NewPgGenerationCountLive()
	daoElectCheck := dao.NewPgGenerationElectCheck()
	daoGet := dao.NewPgGenerationGet()
	daoPayloadInsert := dao.NewPgGenerationPayloadInsert()
	daoQueueDepth := dao.NewPgGenerationQueueDepth()
	daoRecordProviderCall := dao.NewPgGenerationRecordProviderCall()
	daoReleaseStart := dao.NewPgGenerationReleaseStart()
	daoRequestCancel := dao.NewPgGenerationRequestCancel()
	daoRequeue := dao.NewPgGenerationRequeue()
	daoSettle := dao.NewPgGenerationSettle()
	daoSubmit := dao.NewPgGenerationSubmit()
	daoSweep := dao.NewPgGenerationSweep()
	daoUsageInsert := dao.NewPgGenerationUsageInsert()
	daoUsageSum := dao.NewPgGenerationUsageSum()

	// =================================================================================================================
	// SERVICES
	// =================================================================================================================

	registry := lo.Must(connectors.NewRegistry(connectors.OpenAIConfig{
		BaseURL: cfg.OpenAI.BaseURL, APIKey: cfg.OpenAI.APIKey, WebhookSecret: cfg.OpenAI.WebhookSecret,
	}))

	transactor := postgres.NewTransactor(nil)

	serviceCheck := core.NewGenerationCheck(
		daoBeginStart,
		daoReleaseStart,
		daoRecordProviderCall,
		daoSettle,
		daoRequeue,
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
		daoPayloadInsert,
		serviceCheck,
		registry,
		transactor,
		cfg.Generations,
		cfg.Caps,
	)
	serviceGet := core.NewGenerationGet(daoGet, daoElectCheck, daoAttemptList, serviceCheck, cfg.Generations)
	serviceCancel := core.NewGenerationCancel(daoRequestCancel, daoGet, daoAttemptList, serviceCheck, cfg.Generations)
	serviceStream := core.NewGenerationStream(daoGet, registry)
	serviceQueueDepth := core.NewGenerationQueueDepth(daoQueueDepth)
	serviceSweep := core.NewGenerationSweep(daoSweep, serviceCheck, cfg.Sweep)

	// =================================================================================================================
	// HANDLERS
	// =================================================================================================================

	handlerStatus := handlers.NewGrpcStatus(serviceQueueDepth)
	handlerSubmit := handlers.NewGrpcGenerationSubmit(serviceSubmit)
	handlerGet := handlers.NewGrpcGenerationGet(serviceGet)
	handlerCancel := handlers.NewGrpcGenerationCancel(serviceCancel)
	handlerStream := handlers.NewGrpcGenerationStream(serviceStream)

	// =================================================================================================================
	// SERVER
	// =================================================================================================================

	ctxInterceptor := func(rpCtx context.Context) context.Context {
		return postgres.TransferContext(ctx, rpCtx)
	}

	// A stopping server waits for its handlers, so none still runs once the database closes.
	server := grpcf.NewServer(
		ctx, cfg.Otel, cfg.GrpcLogger, cfg.App.DowntimeStart, ctxInterceptor, grpc.WaitForHandlers(true),
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
	genaiv0.RegisterGenerationStreamServiceServer(server, handlerStream)

	reflection.Register(server)

	// =================================================================================================================
	// RUN
	// =================================================================================================================

	log.Println("Starting gRPC server on :" + strconv.Itoa(cfg.Grpc.Port))

	// The sweep runs in this process and takes the boot context: a request context dies at the
	// server's own timeout. It only catches generations nobody polled, so one loop per replica is
	// enough.
	var sweep sync.WaitGroup

	sweep.Go(func() {
		worker.Poll(
			ctx, cfg.Logger, "generation-sweep", cfg.Sweep.Interval, 0,
			outsideDowntime(cfg.App.DowntimeStart, sweepOnce(serviceSweep)),
		)
	})

	err := grpcf.ListenAndServe(ctx, cfg.Grpc, server)

	// The sweep stops with the server, even one that failed to start, and finishes its last checks
	// before the database closes.
	stop()
	sweep.Wait()

	if err != nil {
		panic(err)
	}
}

func closeDatabase(database io.Closer) {
	err := database.Close()
	if err != nil {
		log.Println("Close Postgres: " + err.Error())
	}
}

// outsideDowntime idles a loop iteration once a planned downtime has started, since the database
// it works on is unavailable then.
func outsideDowntime(
	start *time.Time, run func(context.Context) (bool, error),
) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		if downtime.Started(start, time.Now()) {
			return false, nil
		}

		return run(ctx)
	}
}

// sweepOnce runs one sweep pass in the shape worker.Poll drives, reporting whether it found work.
func sweepOnce(sweep *core.GenerationSweep) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		result, err := sweep.Exec(ctx, &core.GenerationSweepRequest{})
		if err != nil {
			return false, err
		}

		return result.Checked > 0, nil
	}
}
