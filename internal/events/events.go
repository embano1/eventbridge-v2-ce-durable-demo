// Package events defines the demo's CloudEvents.
package events

import (
	"fmt"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
)

const (
	TypePackageReleased = "dev.example.package.released"
	TypeReviewRequested = "dev.example.review.requested"
	TypeReviewDecided   = "dev.example.review.decided"
	TypePackageVerdict  = "dev.example.package.verdict"

	SourceCLI      = "urn:demo:cli"
	SourceWorkflow = "urn:demo:inspector"

	// ContentType is what publishers declare to the bus. The bus rejects
	// application/cloudevents+json, so structured CloudEvents travel as application/json.
	ContentType = ce.ApplicationJSON
)

const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"

	VerdictApproved = "approved"
	VerdictBlocked  = "blocked"

	DecidedByClassifier = "classifier"
	DecidedByReviewer   = "reviewer"
	DecidedByTimeout    = "timeout"
	DecidedByError      = "error"
)

type Released struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Repo      string `json:"repo"`
	FromTag   string `json:"fromTag"`
	ToTag     string `json:"toTag"`
}

type ReviewRequested struct {
	CallbackID string  `json:"callbackId"`
	Package    string  `json:"package"`
	Score      float64 `json:"score"`
	DiffURL    string  `json:"diffUrl"`
	Reason     string  `json:"reason"`
}

type ReviewDecided struct {
	CallbackID string `json:"callbackId"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
	Reviewer   string `json:"reviewer"`
}

type Verdict struct {
	Verdict   string  `json:"verdict"`
	Score     float64 `json:"score"`
	DecidedBy string  `json:"decidedBy"`
	Reason    string  `json:"reason"`
}

func Subject(ecosystem, name string) string { return ecosystem + "/" + name }

func New(id, source, typ, subject string, data any) (ce.Event, error) {
	return build(id, source, typ, subject, data, time.Now().UTC())
}

// NewUntimed leaves out the optional time attribute, so the same arguments always encode to the same
// bytes. Lambda rejects a reused DurableExecutionName whose payload differs from the first one.
func NewUntimed(id, source, typ, subject string, data any) (ce.Event, error) {
	return build(id, source, typ, subject, data, time.Time{})
}

func build(id, source, typ, subject string, data any, t time.Time) (ce.Event, error) {
	e := ce.New()
	e.SetID(id)
	e.SetSource(source)
	e.SetType(typ)
	e.SetSubject(subject)
	if !t.IsZero() {
		e.SetTime(t)
	}
	if err := e.SetData(ce.ApplicationJSON, data); err != nil {
		return e, fmt.Errorf("set data: %w", err)
	}
	return e, e.Validate()
}

func LogAttrs(e ce.Event) []any {
	return []any{"ce_id", e.ID(), "ce_type", e.Type(), "ce_source", e.Source(), "ce_subject", e.Subject()}
}
