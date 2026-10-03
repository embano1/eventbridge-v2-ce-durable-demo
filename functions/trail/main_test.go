package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/events"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/logging"
)

func TestHandlerLogsTheObservedDelivery(t *testing.T) {
	b, err := os.ReadFile("../../internal/events/testdata/raw-delivery.json")
	if err != nil {
		t.Fatal(err)
	}
	var e ce.Event
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := handler(logging.New(&buf, "trail"))(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON line: %s", buf.String())
	}
	data, _ := line["data"].(map[string]any)
	if line["message"] != "event" || line["ce_id"] != e.ID() || line["ce_subject"] != "npm/left-pad" || data["repo"] != "stevemao/left-pad" || line["callback_id"] != nil {
		t.Fatalf("line = %v", line)
	}
}

func TestHandlerAddsCallbackIDForReviewEvents(t *testing.T) {
	for typ, data := range map[string]any{
		events.TypeReviewRequested: events.ReviewRequested{CallbackID: "cb-1", Package: "npm/dotenv"},
		events.TypeReviewDecided:   events.ReviewDecided{CallbackID: "cb-1", Decision: events.DecisionApprove},
	} {
		t.Run(typ, func(t *testing.T) {
			e, err := events.New("id-1", events.SourceWorkflow, typ, "npm/dotenv", data)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := handler(logging.New(&buf, "trail"))(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			var line map[string]any
			if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
				t.Fatalf("not one JSON line: %s", buf.String())
			}
			if line["callback_id"] != "cb-1" {
				t.Fatalf("line = %v", line)
			}
		})
	}
}
