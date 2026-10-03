package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridgev2"
	"github.com/aws/aws-sdk-go-v2/service/eventbridgev2/types"
	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/events"
)

type fakeAPI struct {
	in  *eventbridgev2.PutRawEventsInput
	out *eventbridgev2.PutRawEventsOutput
	err error
}

func (f *fakeAPI) PutRawEvents(_ context.Context, in *eventbridgev2.PutRawEventsInput, _ ...func(*eventbridgev2.Options)) (*eventbridgev2.PutRawEventsOutput, error) {
	f.in = in
	return f.out, f.err
}

func result(code types.SuccessCode) *eventbridgev2.PutRawEventsOutput {
	return &eventbridgev2.PutRawEventsOutput{Entries: []types.PutRawEventsResultEntry{{EventId: aws.String("eb-1"), SuccessCode: code}}}
}

func TestPublishMapsCloudEventToEntry(t *testing.T) {
	api := &fakeAPI{out: result(types.SuccessCodePublished)}
	e, err := events.New("ce-1", events.SourceCLI, events.TypePackageReleased, "npm/left-pad", events.Released{Name: "left-pad"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Publisher{API: api, BusARN: "arn:bus"}).Publish(context.Background(), e, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.EventID != "eb-1" || res.Deduplicated {
		t.Fatalf("result = %+v", res)
	}
	if aws.ToString(api.in.EventBusArn) != "arn:bus" || len(api.in.Entries) != 1 {
		t.Fatalf("input = %+v", api.in)
	}
	entry := api.in.Entries[0]
	sm := entry.SystemMetadata
	if aws.ToString(sm.ContentType) != "application/json" || aws.ToString(sm.DeduplicationId) != "ce-1" || aws.ToString(sm.EventGroupId) != "npm/left-pad" {
		t.Fatalf("system metadata = %+v", sm)
	}
	var back ce.Event
	if err := json.Unmarshal(entry.Data, &back); err != nil || back.ID() != "ce-1" {
		t.Fatalf("data does not round-trip: %v, %v", back, err)
	}
}

func TestPublishUsesExplicitDedupID(t *testing.T) {
	api := &fakeAPI{out: result(types.SuccessCodePublished)}
	e, err := events.New("ce-2", events.SourceCLI, events.TypeReviewDecided, "npm/left-pad", events.ReviewDecided{CallbackID: "cb-9"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Publisher{API: api, BusARN: "arn:bus"}).Publish(context.Background(), e, "cb-9"); err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(api.in.Entries[0].SystemMetadata.DeduplicationId); got != "cb-9" {
		t.Fatalf("dedup id = %q", got)
	}
}

func TestPublishReportsDeduplicated(t *testing.T) {
	e, err := events.New("ce-3", events.SourceCLI, events.TypePackageReleased, "npm/x", events.Released{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Publisher{API: &fakeAPI{out: result(types.SuccessCodeDeduplicated)}, BusARN: "arn:bus"}).Publish(context.Background(), e, "")
	if err != nil || !res.Deduplicated {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestPublishSurfacesEntryErrors(t *testing.T) {
	out := &eventbridgev2.PutRawEventsOutput{Entries: []types.PutRawEventsResultEntry{{ErrorCode: aws.String("ValidationError"), ErrorMessage: aws.String("Unsupported contentType")}}}
	e, err := events.New("ce-4", events.SourceCLI, events.TypePackageReleased, "npm/x", events.Released{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Publisher{API: &fakeAPI{out: out}, BusARN: "arn:bus"}).Publish(context.Background(), e, "")
	if err == nil || !strings.Contains(err.Error(), "ValidationError") {
		t.Fatalf("err = %v", err)
	}
}

func TestPublishHashesLongDedupID(t *testing.T) {
	api := &fakeAPI{out: result(types.SuccessCodePublished)}
	e, err := events.New("ce-6", events.SourceCLI, events.TypeReviewRequested, "npm/left-pad", events.ReviewRequested{})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 370)
	if _, err := (&Publisher{API: api, BusARN: "arn:bus"}).Publish(context.Background(), e, long); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(long))
	want := hex.EncodeToString(sum[:])
	got := aws.ToString(api.in.Entries[0].SystemMetadata.DeduplicationId)
	if len(got) != 64 || got != want {
		t.Fatalf("dedup id = %q, want %q", got, want)
	}
}

func TestPublishKeepsShortDedupIDUnchanged(t *testing.T) {
	api := &fakeAPI{out: result(types.SuccessCodePublished)}
	e, err := events.New("ce-7", events.SourceCLI, events.TypeReviewRequested, "npm/left-pad", events.ReviewRequested{})
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("b", 128)
	if _, err := (&Publisher{API: api, BusARN: "arn:bus"}).Publish(context.Background(), e, id); err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(api.in.Entries[0].SystemMetadata.DeduplicationId); got != id {
		t.Fatalf("dedup id = %q, want unchanged %q", got, id)
	}
}

func TestPublishHashesDifferOnDecidedSuffix(t *testing.T) {
	requested := strings.Repeat("c", 370)
	decided := requested + "-decided"

	apiA := &fakeAPI{out: result(types.SuccessCodePublished)}
	eA, err := events.New("ce-8", events.SourceCLI, events.TypeReviewRequested, "npm/left-pad", events.ReviewRequested{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Publisher{API: apiA, BusARN: "arn:bus"}).Publish(context.Background(), eA, requested); err != nil {
		t.Fatal(err)
	}

	apiB := &fakeAPI{out: result(types.SuccessCodePublished)}
	eB, err := events.New("ce-9", events.SourceCLI, events.TypeReviewDecided, "npm/left-pad", events.ReviewDecided{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Publisher{API: apiB, BusARN: "arn:bus"}).Publish(context.Background(), eB, decided); err != nil {
		t.Fatal(err)
	}

	gotA := aws.ToString(apiA.in.Entries[0].SystemMetadata.DeduplicationId)
	gotB := aws.ToString(apiB.in.Entries[0].SystemMetadata.DeduplicationId)
	if gotA == gotB {
		t.Fatalf("dedup ids collided: %q", gotA)
	}
}

func TestPublishWithoutSubjectHasNoGroup(t *testing.T) {
	api := &fakeAPI{out: result(types.SuccessCodePublished)}
	e, err := events.New("ce-5", events.SourceCLI, events.TypePackageReleased, "", events.Released{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Publisher{API: api, BusARN: "arn:bus"}).Publish(context.Background(), e, ""); err != nil {
		t.Fatal(err)
	}
	if api.in.Entries[0].SystemMetadata.EventGroupId != nil {
		t.Fatal("EventGroupId set without a subject")
	}
}
