// Command inspector is the durable package-review Lambda function.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-durable-execution-sdk-go/durable"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridgev2"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/classifier"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/diff"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/logging"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/publish"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/workflow"
)

func main() {
	log := logging.New(os.Stdout, "inspector")
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Error("load aws config", "error", err)
		os.Exit(1)
	}
	sm := secretsmanager.NewFromConfig(cfg)
	key, err := secret(ctx, sm, os.Getenv("JEV_SECRET_ARN"))
	if err != nil {
		log.Error("read jev key", "error", err)
		os.Exit(1)
	}
	token, err := secret(ctx, sm, os.Getenv("GITHUB_TOKEN_SECRET_ARN"))
	if err != nil {
		log.Error("read github token", "error", err)
		os.Exit(1)
	}
	if token == noSecret {
		token = ""
	}
	cls := classifier.New(key, os.Getenv("JEV_BASE_URL"))
	log.Info("starting", "classifier", fmt.Sprintf("%T", cls), "github_authenticated", token != "")

	durable.Start(workflow.NewHandler(workflow.Deps{
		Diffs:         &diff.GitHub{Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}},
		Classifier:    cls,
		Publisher:     &publish.Publisher{API: eventbridgev2.NewFromConfig(cfg), BusARN: os.Getenv("BUS_ARN")},
		ReviewTimeout: 24 * time.Hour,
		Retry:         durable.ExponentialBackoff(),
	}), durable.WithLogHandler(logging.Handler(os.Stdout, "inspector")))
}

// noSecret is the placeholder the stack stores in both secrets until someone sets a real value.
const noSecret = classifier.NoKey

// secret runs once per cold start, outside any step, so the value never lands in a checkpoint.
func secret(ctx context.Context, sm *secretsmanager.Client, arn string) (string, error) {
	if arn == "" {
		return "", nil
	}
	out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(arn)})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.SecretString), nil
}
