package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mjones/temporal-word-game/internal/api"
	"github.com/mjones/temporal-word-game/internal/bootstrap"
	"github.com/mjones/temporal-word-game/internal/store"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	sessionJWTSecret := os.Getenv("SESSION_JWT_SECRET")
	if sessionJWTSecret == "" {
		return errors.New("SESSION_JWT_SECRET is required")
	}
	storeConfig, err := store.ConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	dataStore, err := store.Open(ctx, storeConfig)
	if err != nil {
		return err
	}
	if storeConfig.Endpoint != "" {
		// DynamoDB Local starts empty.
		if err := dataStore.CreateTables(ctx); err != nil {
			return err
		}
	}

	clientOptions := envconfig.MustLoadDefaultClientOptions()
	temporalClient, err := client.Dial(clientOptions)
	if err != nil {
		return err
	}
	defer temporalClient.Close()

	taskQueue := env("TEMPORAL_TASK_QUEUE", workflows.TaskQueue)
	temporalNamespace := clientOptions.Namespace
	if temporalNamespace == "" {
		temporalNamespace = "default"
	}
	if err := bootstrap.StartDefaultCampaign(ctx, temporalClient, taskQueue); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              env("HTTP_ADDRESS", ":8080"),
		Handler:           api.New(temporalClient, dataStore, taskQueue, temporalNamespace, sessionJWTSecret),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("web app listening on %s", server.Addr)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
