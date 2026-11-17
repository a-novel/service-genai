// Command sweep checks every unsettled generation every SWEEP_INTERVAL. Its server only answers the
// liveness and health probes; cmd/grpc serves the generation API.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/samber/lo"

	"github.com/a-novel-kit/golib/downtime"
	"github.com/a-novel-kit/golib/httpf"
	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/worker"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/dao"
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

	// A server started during a planned downtime refuses work, so it leaves the database alone.
	if !downtime.Started(cfg.App.DowntimeStart, time.Now()) {
		ctx = lo.Must(postgres.NewContext(ctx, cfg.Postgres))

		defer cfg.Postgres.Close()
	}

	// =================================================================================================================
	// DAO
	// =================================================================================================================

	daoAttemptInsert := dao.NewPgGenerationAttemptInsert()
	daoGet := dao.NewPgGenerationGet()
	daoRecordProviderCall := dao.NewPgGenerationRecordProviderCall()
	daoSettle := dao.NewPgGenerationSettle()
	daoSweep := dao.NewPgGenerationSweep()
	daoSweepLock := dao.NewPgGenerationSweepLock()
	daoSweepUnlock := dao.NewPgGenerationSweepUnlock()
	daoUsageInsert := dao.NewPgGenerationUsageInsert()

	// =================================================================================================================
	// SERVICES
	// =================================================================================================================

	registry := lo.Must(connectors.NewRegistry(connectors.OpenAIConfig(cfg.OpenAI)))

	serviceCheck := core.NewGenerationCheck(
		daoRecordProviderCall,
		daoSettle,
		daoAttemptInsert,
		daoUsageInsert,
		daoGet,
		registry,
		postgres.NewTransactor(nil),
		cfg.Logger,
		cfg.Generations,
	)
	serviceSweep := core.NewGenerationSweep(daoSweep, daoSweepLock, daoSweepUnlock, serviceCheck, cfg.Sweep)

	// =================================================================================================================
	// HANDLERS
	// =================================================================================================================

	// =================================================================================================================
	// ROUTER
	// =================================================================================================================

	router := httpf.NewMux(cfg.Rest, cfg.RestLogger, cfg.App.DowntimeStart)

	router.Get("/ping", httpf.Ping)
	router.Get("/healthcheck", httpf.Health(map[string]func(context.Context) error{"client:postgres": postgres.Health}))

	// =================================================================================================================
	// RUN
	// =================================================================================================================

	var ticker sync.WaitGroup

	ticker.Go(func() {
		worker.Poll(ctx, cfg.Logger, "sweep", cfg.Sweep.Interval, 0, sweepRun(serviceSweep, cfg.App.DowntimeStart))
	})

	log.Println("Starting sweep server on :" + strconv.Itoa(cfg.Rest.Port))

	err := httpf.ListenAndServe(ctx, cfg.Rest, router)

	stop()

	// The ticker's last run waits for the checks it started, so they end before the database closes.
	ticker.Wait()

	if err != nil {
		panic(err)
	}
}

// sweepRun returns the run the ticker repeats. [worker.Poll] logs the run's failures and panics,
// and records them on the run's span. From a planned downtime's start, the run leaves the database
// alone.
//
// A run drains the backlog batch after batch, so it asks [worker.Poll] for no immediate rerun.
func sweepRun(service *core.GenerationSweep, downtimeStart *time.Time) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		if downtime.Started(downtimeStart, time.Now()) {
			return false, nil
		}

		_, err := service.Exec(ctx, &core.GenerationSweepRequest{})

		return false, err
	}
}
