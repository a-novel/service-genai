// Command sweep checks the generations nobody checked within the interval, each time a scheduler
// posts to /sweep, and every interval when SWEEP_TICKER is set. It runs as its own private service,
// which only the scheduler may call; cmd/grpc serves the generation API.
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
	"go.opentelemetry.io/otel/trace"

	"github.com/a-novel-kit/golib/downtime"
	"github.com/a-novel-kit/golib/httpf"
	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/sentryf"
	"github.com/a-novel-kit/golib/worker"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/config/env"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/handlers"
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

	lo.Must0(sentryf.Init(cfg.Sentry))

	defer sentryf.Flush(config.SentryFlushTimeout)

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
	daoBeginStart := dao.NewPgGenerationBeginStart()
	daoGet := dao.NewPgGenerationGet()
	daoRecordProviderCall := dao.NewPgGenerationRecordProviderCall()
	daoReleaseStart := dao.NewPgGenerationReleaseStart()
	daoRequeue := dao.NewPgGenerationRequeue()
	daoSettle := dao.NewPgGenerationSettle()
	daoSweep := dao.NewPgGenerationSweep()
	daoUsageInsert := dao.NewPgGenerationUsageInsert()

	// =================================================================================================================
	// SERVICES
	// =================================================================================================================

	registry := lo.Must(connectors.NewRegistry(connectors.OpenAIConfig(cfg.OpenAI)))

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
		postgres.NewTransactor(nil),
		cfg.Logger,
		cfg.Generations,
	)
	serviceSweep := core.NewGenerationSweep(daoSweep, serviceCheck, cfg.Sweep)

	// =================================================================================================================
	// HANDLERS
	// =================================================================================================================

	handlerPing := handlers.NewRestPing()
	handlerHealth := handlers.NewRestHealth()
	handlerSweep := handlers.NewRestGenerationSweep(serviceSweep, cfg.Logger)

	// =================================================================================================================
	// ROUTER
	// =================================================================================================================

	router := httpf.NewRouter(cfg.Rest, cfg.Otel, cfg.RestLogger, cfg.App.DowntimeStart)

	router.Get("/ping", handlerPing.ServeHTTP)
	router.Get("/healthcheck", handlerHealth.ServeHTTP)
	router.With(httpf.EndOnShutdown(ctx)).Post("/sweep", handlerSweep.ServeHTTP)

	// =================================================================================================================
	// RUN
	// =================================================================================================================

	var ticker sync.WaitGroup

	if cfg.Sweep.Ticker {
		ticker.Go(func() {
			worker.Poll(ctx, cfg.Logger, "sweep", cfg.Sweep.Interval, 0, sweepRun(serviceSweep, cfg.App.DowntimeStart))
		})
	}

	log.Println("Starting sweep server on :" + strconv.Itoa(cfg.Rest.Port))

	// The sweep is billed per request and no consumer waits on it, so each answer waits for its
	// telemetry to leave while the request still holds a CPU.
	err := httpf.ListenAndServe(ctx, cfg.Rest, httpf.SendTelemetry(config.SweepTelemetryTimeout)(router))

	stop()

	// The ticker's last run waits for the checks it started, so they end before the database closes.
	ticker.Wait()

	if err != nil {
		panic(err)
	}
}

// sweepRun returns the run the ticker repeats. No handler answers for it, so it reports its own
// failure and panic to Sentry. From a planned downtime's start, it leaves the database alone.
//
// A run drains the backlog batch after batch, so it asks [worker.Poll] for no immediate rerun.
func sweepRun(service *core.GenerationSweep, downtimeStart *time.Time) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		defer otel.RecoverPanic(ctx, trace.SpanFromContext(ctx))

		if downtime.Started(downtimeStart, time.Now()) {
			return false, nil
		}

		_, err := service.Exec(ctx, &core.GenerationSweepRequest{})
		if err != nil {
			sentryf.Capture(ctx, "sweep", err)
		}

		return false, err
	}
}

func closeDatabase(database io.Closer) {
	err := database.Close()
	if err != nil {
		log.Println("Close Postgres: " + err.Error())
	}
}
