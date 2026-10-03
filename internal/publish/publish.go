// Package publish sends CloudEvents to an EventBridge v2 bus with PutRawEvents.
package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridgev2"
	"github.com/aws/aws-sdk-go-v2/service/eventbridgev2/types"
	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/events"
)

// maxDedupIDLength is the bus's limit on systemMetadata.deduplicationId.
const maxDedupIDLength = 128

// hashDedupID keeps a long id deterministic and under the bus's limit by
// substituting its SHA-256 hex digest.
func hashDedupID(id string) string {
	if len(id) <= maxDedupIDLength {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

type API interface {
	PutRawEvents(ctx context.Context, in *eventbridgev2.PutRawEventsInput, optFns ...func(*eventbridgev2.Options)) (*eventbridgev2.PutRawEventsOutput, error)
}

type Publisher struct {
	API    API
	BusARN string
}

type Result struct {
	EventID      string `json:"eventId"`
	Deduplicated bool   `json:"deduplicated"`
}

// Publish defaults dedupID to the CloudEvent id, so a retry within the bus's 5-minute window
// comes back DEDUPLICATED instead of creating a second event.
func (p *Publisher) Publish(ctx context.Context, e ce.Event, dedupID string) (Result, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return Result{}, fmt.Errorf("encode cloudevent: %w", err)
	}
	if dedupID == "" {
		dedupID = e.ID()
	}
	dedupID = hashDedupID(dedupID)
	sm := &types.PutRawEventsSystemMetadata{ContentType: aws.String(events.ContentType), DeduplicationId: aws.String(dedupID)}
	if s := e.Subject(); s != "" {
		sm.EventGroupId = aws.String(s)
	}
	out, err := p.API.PutRawEvents(ctx, &eventbridgev2.PutRawEventsInput{
		EventBusArn: aws.String(p.BusARN),
		Entries:     []types.PutRawEventsRequestEntry{{Data: data, SystemMetadata: sm}},
	})
	if err != nil {
		return Result{}, fmt.Errorf("put raw events: %w", err)
	}
	if len(out.Entries) != 1 {
		return Result{}, fmt.Errorf("put raw events: got %d results for 1 entry", len(out.Entries))
	}
	r := out.Entries[0]
	if r.ErrorCode != nil {
		return Result{}, fmt.Errorf("put raw events: %s: %s", aws.ToString(r.ErrorCode), aws.ToString(r.ErrorMessage))
	}
	return Result{EventID: aws.ToString(r.EventId), Deduplicated: r.SuccessCode == types.SuccessCodeDeduplicated}, nil
}
