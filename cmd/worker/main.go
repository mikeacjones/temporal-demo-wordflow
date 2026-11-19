package main

import (
	"context"
	"log"
	"os"

	"github.com/mjones/temporal-word-game/internal/activities"
	"github.com/mjones/temporal-word-game/internal/bootstrap"
	"github.com/mjones/temporal-word-game/internal/store"
	"github.com/mjones/temporal-word-game/internal/workflows"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
	"go.temporal.io/sdk/worker"
)

func main() {
	ctx := context.Background()
	if os.Getenv(activities.OpenAIKeyEnvironment) == "" {
		log.Fatalf("%s is required: this worker generates the daily challenge and monthly pack", activities.OpenAIKeyEnvironment)
	}
	dataStore, err := openStore(ctx)
	if err != nil {
		log.Fatal(err)
	}

	temporalClient, err := client.Dial(envconfig.MustLoadDefaultClientOptions())
	if err != nil {
		log.Fatal(err)
	}
	defer temporalClient.Close()

	taskQueue := env("TEMPORAL_TASK_QUEUE", workflows.TaskQueue)
	if err := bootstrap.EnsureCampaignSchedules(ctx, temporalClient, taskQueue); err != nil {
		log.Fatal(err)
	}

	// On the demo platform the Worker Controller sets both; without them the
	// worker runs unversioned, as in local development.
	buildID := os.Getenv("TEMPORAL_WORKER_BUILD_ID")
	workerOptions := worker.Options{}
	if buildID != "" {
		workerOptions.DeploymentOptions = worker.DeploymentOptions{
			UseVersioning: true,
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: env("TEMPORAL_DEPLOYMENT_NAME", workflows.WorkerDeploymentName),
				BuildID:        buildID,
			},
		}
	}

	temporalWorker := worker.New(temporalClient, taskQueue, workerOptions)
	workflows.Register(temporalWorker, buildID != "")
	temporalWorker.RegisterActivity(&activities.Campaigns{Store: dataStore})
	temporalWorker.RegisterActivity(&activities.Levels{Store: dataStore})
	temporalWorker.RegisterActivity(&activities.CampaignGenerator{})
	if err := temporalWorker.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}

// openStore connects to the tables the environment names. DynamoDB Local
// starts empty, so the worker creates the tables there.
func openStore(ctx context.Context) (*store.Store, error) {
	config, err := store.ConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	dataStore, err := store.Open(ctx, config)
	if err != nil {
		return nil, err
	}
	if config.Endpoint != "" {
		if err := dataStore.CreateTables(ctx); err != nil {
			return nil, err
		}
	}
	return dataStore, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
