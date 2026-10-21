// Command rest receives the providers' notifications that their calls ended, and checks the
// generations they name. It runs as its own public service; cmd/grpc serves the generation API.
package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/samber/lo"

	"github.com/a-novel-kit/golib/downtime"
	"github.com/a-novel-kit/golib/httpf"
	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"

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

	if env.GcloudProjectID == "" {
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
	daoGetByCall := dao.NewPgGenerationGetByCall()
	daoRecordProviderCall := dao.NewPgGenerationRecordProviderCall()
	daoReleaseStart := dao.NewPgGenerationReleaseStart()
	daoRequeue := dao.NewPgGenerationRequeue()
	daoSettle := dao.NewPgGenerationSettle()
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
	serviceNotify := core.NewGenerationNotify(daoGetByCall, serviceCheck, registry)

	// =================================================================================================================
	// HANDLERS
	// =================================================================================================================

	handlerPing := handlers.NewRestPing()
	handlerHealth := handlers.NewRestHealth()
	handlerNotify := handlers.NewRestGenerationNotify(serviceNotify, cfg.Logger)

	// =================================================================================================================
	// ROUTER
	// =================================================================================================================

	router := httpf.NewRouter(cfg.Rest, cfg.Otel, cfg.RestLogger, cfg.App.DowntimeStart)

	router.Get("/ping", handlerPing.ServeHTTP)
	router.Get("/healthcheck", handlerHealth.ServeHTTP)
	router.Post("/webhooks/{provider}", handlerNotify.ServeHTTP)

	// =================================================================================================================
	// RUN
	// =================================================================================================================

	log.Println("Starting REST server on :" + strconv.Itoa(cfg.Rest.Port))

	err := httpf.ListenAndServe(ctx, cfg.Rest, router)

	stop()

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
