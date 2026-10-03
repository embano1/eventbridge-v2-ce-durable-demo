package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridgev2"
	"github.com/aws/aws-sdk-go-v2/service/eventbridgev2/types"
	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/events"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/publish"
)

type call struct {
	e       ce.Event
	dedupID string
}

type recorder struct{ calls []call }

func (r *recorder) Publish(_ context.Context, e ce.Event, dedupID string) (publish.Result, error) {
	r.calls = append(r.calls, call{e, dedupID})
	return publish.Result{EventID: fmt.Sprintf("eb-%d", len(r.calls)), Deduplicated: len(r.calls) > 1}, nil
}

func with(r *recorder) newPublisherFunc {
	return func(context.Context, string) (publisher, error) { return r, nil }
}

func TestReleaseDupPublishesTheSameEventTwice(t *testing.T) {
	r := &recorder{}
	var out bytes.Buffer
	err := run(context.Background(), []string{"release", "--bus-arn", "arn:bus", "--dup", "npm/left-pad", "stevemao/left-pad", "v1.2.0", "v1.3.0"}, with(r), &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || r.calls[0].e.ID() != r.calls[1].e.ID() || r.calls[0].dedupID != "" {
		t.Fatalf("calls = %+v", r.calls)
	}
	e := r.calls[0].e
	var data events.Released
	if err := e.DataAs(&data); err != nil {
		t.Fatal(err)
	}
	if e.Type() != events.TypePackageReleased || e.Subject() != "npm/left-pad" || data.Repo != "stevemao/left-pad" || data.ToTag != "v1.3.0" {
		t.Fatalf("event = %v, data = %+v", e, data)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"PUBLISHED"`) || !strings.Contains(lines[1], `"DEDUPLICATED"`) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestReleaseReusesAGivenID(t *testing.T) {
	r := &recorder{}
	args := []string{"release", "--bus-arn", "arn:bus", "--id", "fixed-1", "npm/left-pad", "stevemao/left-pad", "v1.2.0", "v1.3.0"}
	if err := run(context.Background(), args, with(r), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0].e.ID() != "fixed-1" {
		t.Fatalf("calls = %+v", r.calls)
	}
}

type rawAPI struct{ data [][]byte }

func (a *rawAPI) PutRawEvents(_ context.Context, in *eventbridgev2.PutRawEventsInput, _ ...func(*eventbridgev2.Options)) (*eventbridgev2.PutRawEventsOutput, error) {
	for _, e := range in.Entries {
		a.data = append(a.data, e.Data)
	}
	return &eventbridgev2.PutRawEventsOutput{Entries: []types.PutRawEventsResultEntry{{EventId: aws.String("eb")}}}, nil
}

func TestReleaseWithTheSameIDPublishesIdenticalBytes(t *testing.T) {
	api := &rawAPI{}
	newPub := func(_ context.Context, busARN string) (publisher, error) {
		return &publish.Publisher{API: api, BusARN: busARN}, nil
	}
	args := []string{"release", "--bus-arn", "arn:bus", "--id", "fixed-1", "npm/left-pad", "stevemao/left-pad", "v1.2.0", "v1.3.0"}
	for range 2 {
		if err := run(context.Background(), args, newPub, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(api.data) != 2 || !bytes.Equal(api.data[0], api.data[1]) {
		t.Fatalf("payloads differ:\n%s\n%s", api.data[0], api.data[1])
	}
	if bytes.Contains(api.data[0], []byte(`"time"`)) {
		t.Fatalf("payload carries a time attribute: %s", api.data[0])
	}
}

func TestReviewDedupsOnDecidedCallbackID(t *testing.T) {
	r := &recorder{}
	args := []string{"review", "--bus-arn", "arn:bus", "--package", "npm/left-pad", "--approve", "--reason", "looked fine", "--reviewer", "alice", "cb-1"}
	if err := run(context.Background(), args, with(r), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0].dedupID != "cb-1-decided" {
		t.Fatalf("calls = %+v", r.calls)
	}
	var d events.ReviewDecided
	if err := r.calls[0].e.DataAs(&d); err != nil {
		t.Fatal(err)
	}
	if d != (events.ReviewDecided{CallbackID: "cb-1", Decision: events.DecisionApprove, Reason: "looked fine", Reviewer: "alice"}) {
		t.Fatalf("data = %+v", d)
	}
	if r.calls[0].e.Type() != events.TypeReviewDecided || r.calls[0].e.Subject() != "npm/left-pad" {
		t.Fatalf("event = %v", r.calls[0].e)
	}
}

func TestBadArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"no command":         {},
		"unknown command":    {"deploy"},
		"package format":     {"release", "--bus-arn", "a", "left-pad", "o/r", "v1", "v2"},
		"missing positional": {"release", "--bus-arn", "a", "npm/left-pad", "o/r", "v1"},
		"both decisions":     {"review", "--bus-arn", "a", "--package", "npm/x", "--approve", "--reject", "cb"},
		"no decision":        {"review", "--bus-arn", "a", "--package", "npm/x", "cb"},
		"no package":         {"review", "--bus-arn", "a", "--approve", "cb"},
	} {
		t.Run(name, func(t *testing.T) {
			r := &recorder{}
			if err := run(context.Background(), args, with(r), &bytes.Buffer{}); err == nil {
				t.Fatal("expected an error")
			}
			if len(r.calls) != 0 {
				t.Fatalf("published %d events", len(r.calls))
			}
		})
	}
}

// A flag.Parse failure must name the offending flag, not fall through to the
// generic usage string, so the caller can tell "--bogus" from "missing args".
func TestFlagParseErrorNamesTheBadFlag(t *testing.T) {
	for name, args := range map[string][]string{
		"release": {"release", "--bogus", "npm/left-pad", "o/r", "v1", "v2"},
		"review":  {"review", "--bogus", "--package", "npm/x", "--approve", "cb"},
	} {
		t.Run(name, func(t *testing.T) {
			r := &recorder{}
			err := run(context.Background(), args, with(r), &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "bogus") {
				t.Fatalf("error = %q, want it to name the bad flag", err.Error())
			}
		})
	}
}

func TestBusARNResolution(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "infra"), 0o755); err != nil {
		t.Fatal(err)
	}
	outputs := `{"EbV2DemoStack":{"BusArn":"arn:aws:events:eu-central-1:111122223333:event-busv2/package-review/x"}}`
	if err := os.WriteFile(filepath.Join(dir, "infra", "cdk-outputs.json"), []byte(outputs), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("DEMO_BUS_ARN", "")
	if got, err := resolveBusARN(""); err != nil || !strings.HasSuffix(got, "package-review/x") {
		t.Fatalf("outputs: got %q, %v", got, err)
	}
	t.Setenv("DEMO_BUS_ARN", "arn:from-env")
	if got, _ := resolveBusARN(""); got != "arn:from-env" {
		t.Fatalf("env not preferred: %q", got)
	}
	if got, _ := resolveBusARN("arn:flag"); got != "arn:flag" {
		t.Fatalf("flag not preferred: %q", got)
	}
}

func TestNewPublisherTakesTheRegionFromTheBusARN(t *testing.T) {
	t.Setenv("AWS_REGION", "eu-central-1")
	p, err := newPublisher(context.Background(), "arn:aws:events:us-west-2:111122223333:event-busv2/package-review/x")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*publish.Publisher).API.(*eventbridgev2.Client).Options().Region; got != "us-west-2" {
		t.Errorf("region = %q, want us-west-2", got)
	}
	if _, err := newPublisher(context.Background(), "not-an-arn"); err == nil {
		t.Error("want an error for a malformed bus ARN")
	}
}
