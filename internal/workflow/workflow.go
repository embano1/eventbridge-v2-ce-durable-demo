// Package workflow is the durable package review: fetch the release diff, classify it, and hand
// uncertain results to a human reviewer.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-durable-execution-sdk-go/durable"
	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/classifier"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/diff"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/events"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/publish"
)

type Publisher interface {
	Publish(ctx context.Context, e ce.Event, dedupID string) (publish.Result, error)
}

type DiffFetcher interface {
	Compare(ctx context.Context, repo, from, to string) (diff.Diff, error)
}

type Deps struct {
	Diffs         DiffFetcher
	Classifier    classifier.Classifier
	Publisher     Publisher
	ReviewTimeout time.Duration
	Retry         durable.RetryStrategy
}

// fetchResult carries permanent fetch failures as data. A step error replayed from its checkpoint
// keeps only the recorded type and message, so errors.Is(err, diff.ErrNotFound) no longer matches.
type fetchResult struct {
	Diff    diff.Diff `json:"diff"`
	Problem string    `json:"problem,omitempty"`
}

func NewHandler(d Deps) durable.Handler[ce.Event, events.Verdict] {
	return func(ctx durable.Context, in ce.Event) (events.Verdict, error) {
		if in.Type() != events.TypePackageReleased {
			return events.Verdict{}, fmt.Errorf("unexpected event type %q", in.Type())
		}
		var rel events.Released
		if err := in.DataAs(&rel); err != nil {
			return events.Verdict{}, fmt.Errorf("decode %s data: %w", in.Type(), err)
		}
		log := ctx.Logger().With(events.LogAttrs(in)...)
		log.Info("reviewing release", "repo", rel.Repo, "from", rel.FromTag, "to", rel.ToTag)

		v, err := decide(ctx, d, in, rel)
		if err != nil {
			return events.Verdict{}, err
		}
		return v, emitVerdict(ctx, d, in, v)
	}
}

// failed reports whether err is a terminal *durable.OperationError. Anything else, including the
// suspension signal and checkpoint, serdes or context errors, must be returned unchanged.
func failed(err error) bool {
	var opErr *durable.OperationError
	return errors.As(err, &opErr)
}

func decide(ctx durable.Context, d Deps, in ce.Event, rel events.Released) (events.Verdict, error) {
	fetched, err := durable.Step(ctx, "fetch-diff", func(sc durable.StepContext) (fetchResult, error) {
		dd, err := d.Diffs.Compare(sc, rel.Repo, rel.FromTag, rel.ToTag)
		if errors.Is(err, diff.ErrNotFound) || errors.Is(err, diff.ErrInvalidRepo) {
			return fetchResult{Problem: err.Error()}, nil
		}
		if err != nil {
			return fetchResult{}, err
		}
		return fetchResult{Diff: dd}, nil
	}, durable.WithRetry(d.Retry))
	switch {
	case err != nil && !failed(err):
		return events.Verdict{}, err
	case err != nil:
		return verdict(events.VerdictBlocked, events.DecidedByError, 0, "could not fetch the diff: "+err.Error()), nil
	case fetched.Problem != "":
		return verdict(events.VerdictBlocked, events.DecidedByError, 0, fetched.Problem), nil
	}

	res, err := durable.Step(ctx, "classify", func(sc durable.StepContext) (classifier.Result, error) {
		return d.Classifier.Classify(sc, fetched.Diff.Text)
	}, durable.WithRetry(d.Retry))
	if err != nil && !failed(err) {
		return events.Verdict{}, err
	}
	if err != nil {
		return askHuman(ctx, d, in, fetched.Diff, 0, "classifier unavailable: "+err.Error())
	}
	reason := fmt.Sprintf("%s scored the diff %.2f", res.Model, res.Score)
	switch classifier.BandOf(res.Score) {
	case classifier.Suspicious:
		return verdict(events.VerdictBlocked, events.DecidedByClassifier, res.Score, reason), nil
	case classifier.Clean:
		return verdict(events.VerdictApproved, events.DecidedByClassifier, res.Score, reason), nil
	}
	return askHuman(ctx, d, in, fetched.Diff, res.Score, reason+", which is too close to call")
}

func askHuman(ctx durable.Context, d Deps, in ce.Event, dd diff.Diff, score float64, reason string) (events.Verdict, error) {
	decision, err := durable.WaitForCallback[events.ReviewDecided](ctx, "human-review",
		func(sc durable.StepContext, callbackID string) error {
			req, err := events.New(callbackID, events.SourceWorkflow, events.TypeReviewRequested, in.Subject(),
				events.ReviewRequested{CallbackID: callbackID, Package: in.Subject(), Score: score, DiffURL: dd.URL, Reason: reason})
			if err != nil {
				return err
			}
			// The reason is logged here, not before the wait: the handler logger drops lines on resume.
			sc.Logger().Info("requesting human review", append(events.LogAttrs(in), "callback_id", callbackID, "reason", reason)...)
			_, err = d.Publisher.Publish(sc, req, "")
			return err
		},
		durable.WithCallbackTimeout(d.ReviewTimeout),
		durable.WithSubmitterRetry(d.Retry),
	)
	var timeout *durable.CallbackTimeoutError
	switch {
	case errors.As(err, &timeout):
		return verdict(events.VerdictBlocked, events.DecidedByTimeout, score, "no review decision within "+d.ReviewTimeout.String()), nil
	case err != nil && !failed(err):
		return events.Verdict{}, err
	case err != nil:
		return verdict(events.VerdictBlocked, events.DecidedByError, score, "review failed: "+err.Error()), nil
	case decision.Decision == events.DecisionApprove:
		return verdict(events.VerdictApproved, events.DecidedByReviewer, score, decision.Reviewer+": "+decision.Reason), nil
	}
	return verdict(events.VerdictBlocked, events.DecidedByReviewer, score, decision.Reviewer+": "+decision.Reason), nil
}

// emitVerdict derives the event id from the release id, so a retried step is deduplicated by the bus.
func emitVerdict(ctx durable.Context, d Deps, in ce.Event, v events.Verdict) error {
	_, err := durable.Step(ctx, "emit-verdict", func(sc durable.StepContext) (publish.Result, error) {
		e, err := events.New(in.ID()+"-verdict", events.SourceWorkflow, events.TypePackageVerdict, in.Subject(), v)
		if err != nil {
			return publish.Result{}, err
		}
		res, err := d.Publisher.Publish(sc, e, "")
		if err != nil {
			return publish.Result{}, err
		}
		// Logged inside the step: after the last durable operation, a resumed invocation drops log lines.
		sc.Logger().Info("verdict", append(events.LogAttrs(in), "verdict", v.Verdict, "decided_by", v.DecidedBy, "score", v.Score, "reason", v.Reason)...)
		return res, nil
	}, durable.WithRetry(d.Retry))
	return err
}

func verdict(v, decidedBy string, score float64, reason string) events.Verdict {
	return events.Verdict{Verdict: v, Score: score, DecidedBy: decidedBy, Reason: reason}
}
