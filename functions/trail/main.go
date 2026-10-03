// Command trail logs every event a subscriber delivers, which gives the demo its event history.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/events"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/logging"
)

func main() {
	lambda.Start(handler(logging.New(os.Stdout, "trail")))
}

func handler(log *slog.Logger) func(context.Context, ce.Event) error {
	return func(_ context.Context, e ce.Event) error {
		attrs := events.LogAttrs(e)
		if id := callbackID(e); id != "" {
			attrs = append(attrs, "callback_id", id)
		}
		log.Info("event", append(attrs, "data", json.RawMessage(e.Data()))...)
		return nil
	}
}

func callbackID(e ce.Event) string {
	if e.Type() != events.TypeReviewRequested && e.Type() != events.TypeReviewDecided {
		return ""
	}
	var d struct {
		CallbackID string `json:"callbackId"`
	}
	if err := e.DataAs(&d); err != nil {
		return ""
	}
	return d.CallbackID
}
