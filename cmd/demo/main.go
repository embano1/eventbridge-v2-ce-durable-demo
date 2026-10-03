// Command demo publishes the demo's CloudEvents: package releases and review decisions.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridgev2"
	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/google/uuid"

	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/events"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/publish"
)

const usage = `usage (flags before arguments):
  demo release [--bus-arn A] [--dup] [--id ID] <ecosystem/name> <owner/repo> <fromTag> <toTag>
  demo review  [--bus-arn A] --package <ecosystem/name> (--approve | --reject) [--reason R] [--reviewer U] <callbackId>`

type publisher interface {
	Publish(ctx context.Context, e ce.Event, dedupID string) (publish.Result, error)
}

type newPublisherFunc func(ctx context.Context, busARN string) (publisher, error)

func main() {
	if err := run(context.Background(), os.Args[1:], newPublisher, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// newPublisher takes the region from the bus ARN, so the CLI reaches the bus whatever AWS_REGION says.
func newPublisher(ctx context.Context, busARN string) (publisher, error) {
	a, err := arn.Parse(busARN)
	if err != nil {
		return nil, fmt.Errorf("parse bus arn: %w", err)
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(a.Region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &publish.Publisher{API: eventbridgev2.NewFromConfig(cfg), BusARN: busARN}, nil
}

func run(ctx context.Context, args []string, newPub newPublisherFunc, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "release":
		return release(ctx, args[1:], newPub, stdout)
	case "review":
		return review(ctx, args[1:], newPub, stdout)
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], usage)
}

func newFlagSet(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	busARN := fs.String("bus-arn", "", "event bus ARN (default: $DEMO_BUS_ARN, then infra/cdk-outputs.json)")
	return fs, busARN
}

func release(ctx context.Context, args []string, newPub newPublisherFunc, stdout io.Writer) error {
	fs, busARN := newFlagSet("release")
	dup := fs.Bool("dup", false, "publish the same event twice to show ingestion dedup")
	id := fs.String("id", "", "reuse a CloudEvent id, e.g. to repeat a release after the 5-minute dedup window")
	// A parse error (e.g. an unknown flag) must name the flag, not fall through to the generic usage string.
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	if fs.NArg() != 4 {
		return errors.New(usage)
	}
	eco, name, ok := strings.Cut(fs.Arg(0), "/")
	if !ok || eco == "" || name == "" {
		return fmt.Errorf("package must be <ecosystem>/<name>, got %q", fs.Arg(0))
	}
	if *id == "" {
		*id = uuid.NewString()
	}
	e, err := events.NewUntimed(*id, events.SourceCLI, events.TypePackageReleased, events.Subject(eco, name),
		events.Released{Ecosystem: eco, Name: name, Repo: fs.Arg(1), FromTag: fs.Arg(2), ToTag: fs.Arg(3)})
	if err != nil {
		return err
	}
	pub, err := connect(ctx, *busARN, newPub)
	if err != nil {
		return err
	}
	times := 1
	if *dup {
		times = 2
	}
	for range times {
		if err := publishAndPrint(ctx, pub, e, "", stdout); err != nil {
			return err
		}
	}
	return nil
}

func review(ctx context.Context, args []string, newPub newPublisherFunc, stdout io.Writer) error {
	fs, busARN := newFlagSet("review")
	pkg := fs.String("package", "", "package subject from the review.requested event, e.g. npm/left-pad")
	approve := fs.Bool("approve", false, "approve the release")
	reject := fs.Bool("reject", false, "reject the release")
	reason := fs.String("reason", "", "why")
	reviewer := fs.String("reviewer", "demo-reviewer", "who decided")
	// A parse error (e.g. an unknown flag) must name the flag, not fall through to the generic usage string.
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	if fs.NArg() != 1 {
		return errors.New(usage)
	}
	if *pkg == "" || *approve == *reject {
		return fmt.Errorf("need --package and exactly one of --approve or --reject\n%s", usage)
	}
	decision := events.DecisionReject
	if *approve {
		decision = events.DecisionApprove
	}
	callbackID := fs.Arg(0)
	e, err := events.NewUntimed(uuid.NewString(), events.SourceCLI, events.TypeReviewDecided, *pkg,
		events.ReviewDecided{CallbackID: callbackID, Decision: decision, Reason: *reason, Reviewer: *reviewer})
	if err != nil {
		return err
	}
	pub, err := connect(ctx, *busARN, newPub)
	if err != nil {
		return err
	}
	// review.requested already dedups on the bare callback id in the same event group.
	return publishAndPrint(ctx, pub, e, callbackID+"-decided", stdout)
}

func connect(ctx context.Context, busARN string, newPub newPublisherFunc) (publisher, error) {
	arn, err := resolveBusARN(busARN)
	if err != nil {
		return nil, err
	}
	return newPub(ctx, arn)
}

func publishAndPrint(ctx context.Context, pub publisher, e ce.Event, dedupID string, stdout io.Writer) error {
	r, err := pub.Publish(ctx, e, dedupID)
	if err != nil {
		return err
	}
	result := "PUBLISHED"
	if r.Deduplicated {
		result = "DEDUPLICATED"
	}
	return json.NewEncoder(stdout).Encode(map[string]string{
		"ce_id": e.ID(), "ce_type": e.Type(), "ce_subject": e.Subject(), "event_id": r.EventID, "result": result,
	})
}

func resolveBusARN(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("DEMO_BUS_ARN"); v != "" {
		return v, nil
	}
	b, err := os.ReadFile(filepath.Join("infra", "cdk-outputs.json"))
	if err != nil {
		return "", errors.New("no bus ARN: pass --bus-arn, set DEMO_BUS_ARN, or run `make deploy`")
	}
	var outputs map[string]map[string]string
	if err := json.Unmarshal(b, &outputs); err != nil {
		return "", fmt.Errorf("read infra/cdk-outputs.json: %w", err)
	}
	for _, stack := range outputs {
		if arn := stack["BusArn"]; arn != "" {
			return arn, nil
		}
	}
	return "", errors.New("infra/cdk-outputs.json has no BusArn output")
}
