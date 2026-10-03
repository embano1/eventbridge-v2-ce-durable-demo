package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-durable-execution-sdk-go/durable"
	"github.com/aws/aws-durable-execution-sdk-go/durable/durabletest"
	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/classifier"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/diff"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/events"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/logging"
	"github.com/embano1/eventbridge-v2-ce-durable-demo/internal/publish"
)

type fakeDiffs struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeDiffs) Compare(_ context.Context, repo, from, to string) (diff.Diff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return diff.Diff{}, f.err
	}
	return diff.Diff{Text: "+console.log('hi')\n", URL: "https://github.com/" + repo + "/compare/" + from + "..." + to}, nil
}

func (f *fakeDiffs) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeClassifier struct {
	mu    sync.Mutex
	calls int
	score float64
	err   error
}

func (f *fakeClassifier) Classify(context.Context, string) (classifier.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return classifier.Result{}, f.err
	}
	return classifier.Result{Score: f.score, Model: "fake"}, nil
}

func (f *fakeClassifier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakePublisher struct {
	mu        sync.Mutex
	published []ce.Event
}

func (f *fakePublisher) Publish(_ context.Context, e ce.Event, _ string) (publish.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, e)
	return publish.Result{EventID: "eb-" + e.ID()}, nil
}

func (f *fakePublisher) ofType(typ string) []ce.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ce.Event
	for _, e := range f.published {
		if e.Type() == typ {
			out = append(out, e)
		}
	}
	return out
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func released(t *testing.T) ce.Event {
	t.Helper()
	e, err := events.New("rel-1", events.SourceCLI, events.TypePackageReleased, "npm/left-pad",
		events.Released{Ecosystem: "npm", Name: "left-pad", Repo: "stevemao/left-pad", FromTag: "v1.2.0", ToTag: "v1.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func newRunner(c *fakeClassifier, d *fakeDiffs, p *fakePublisher, opts ...durable.HandlerOption) *durabletest.LocalRunner[ce.Event, events.Verdict] {
	return durabletest.NewLocalRunner(NewHandler(Deps{
		Diffs:         d,
		Classifier:    c,
		Publisher:     p,
		ReviewTimeout: time.Hour,
		Retry:         durable.MustNewRetryStrategy(durable.RetryConfig{MaxAttempts: 2, InitialDelay: time.Second, MaxDelay: 2 * time.Second, BackoffRate: 2}),
	}), opts...)
}

func verdictOf(t *testing.T, res *durabletest.TestResult) events.Verdict {
	t.Helper()
	if res.Status != durabletest.Succeeded {
		t.Fatalf("status = %s, error = %+v", res.Status, res.Error)
	}
	v, err := durabletest.ResultAs[events.Verdict](res)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func assertVerdictEvent(t *testing.T, p *fakePublisher, id string, want events.Verdict) {
	t.Helper()
	got := p.ofType(events.TypePackageVerdict)
	if len(got) != 1 || got[0].ID() != id+"-verdict" || got[0].Subject() != "npm/left-pad" {
		t.Fatalf("verdict events = %v", got)
	}
	var v events.Verdict
	if err := got[0].DataAs(&v); err != nil {
		t.Fatal(err)
	}
	if v != want {
		t.Fatalf("published verdict = %+v, want %+v", v, want)
	}
}

func TestClassifierDecides(t *testing.T) {
	for name, tc := range map[string]struct {
		score   float64
		verdict string
	}{
		"clean":      {0.05, events.VerdictApproved},
		"suspicious": {0.93, events.VerdictBlocked},
	} {
		t.Run(name, func(t *testing.T) {
			c, d, p := &fakeClassifier{score: tc.score}, &fakeDiffs{}, &fakePublisher{}
			v := verdictOf(t, newRunner(c, d, p).RunUntilComplete(t, released(t)))
			if v.Verdict != tc.verdict || v.DecidedBy != events.DecidedByClassifier || v.Score != tc.score {
				t.Fatalf("verdict = %+v", v)
			}
			assertVerdictEvent(t, p, "rel-1", v)
			if len(p.ofType(events.TypeReviewRequested)) != 0 {
				t.Fatal("asked for a review")
			}
		})
	}
}

func TestObservedDeliveryRuns(t *testing.T) {
	b, err := os.ReadFile("../events/testdata/raw-delivery.json")
	if err != nil {
		t.Fatal(err)
	}
	var in ce.Event
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	c, d, p := &fakeClassifier{score: 0.05}, &fakeDiffs{}, &fakePublisher{}
	v := verdictOf(t, newRunner(c, d, p).RunUntilComplete(t, in))
	assertVerdictEvent(t, p, in.ID(), v)
}

func TestUncertainScoreWaitsForReviewer(t *testing.T) {
	c, d, p := &fakeClassifier{score: 0.5}, &fakeDiffs{}, &fakePublisher{}
	logs := &syncBuffer{}
	runner := newRunner(c, d, p, durable.WithLogHandler(logging.Handler(logs, "inspector")))
	input := released(t)

	res := runner.RunUntilComplete(t, input)
	if res.Status != durabletest.Pending {
		t.Fatalf("status = %s, want PENDING", res.Status)
	}
	cbs := runner.OpenCallbacks()
	if len(cbs) != 1 {
		t.Fatalf("open callbacks = %+v", cbs)
	}
	reqs := p.ofType(events.TypeReviewRequested)
	if len(reqs) != 1 || reqs[0].ID() != cbs[0].CallbackID {
		t.Fatalf("review requests = %v, callback = %s", reqs, cbs[0].CallbackID)
	}
	var req events.ReviewRequested
	if err := reqs[0].DataAs(&req); err != nil {
		t.Fatal(err)
	}
	if req.CallbackID != cbs[0].CallbackID || req.Package != "npm/left-pad" || req.Score != 0.5 || req.DiffURL == "" {
		t.Fatalf("review request = %+v", req)
	}

	decision := events.ReviewDecided{CallbackID: cbs[0].CallbackID, Decision: events.DecisionApprove, Reason: "looked fine", Reviewer: "alice"}
	if err := runner.SendCallbackSuccess(cbs[0].CallbackID, decision); err != nil {
		t.Fatal(err)
	}
	v := verdictOf(t, runner.RunUntilComplete(t, input))
	if v.Verdict != events.VerdictApproved || v.DecidedBy != events.DecidedByReviewer || !strings.Contains(v.Reason, "alice") {
		t.Fatalf("verdict = %+v", v)
	}
	assertVerdictEvent(t, p, "rel-1", v)
	if c.callCount() != 1 || d.callCount() != 1 {
		t.Fatalf("replay re-ran steps: classifier %d, diffs %d", c.callCount(), d.callCount())
	}
	if !strings.Contains(logs.String(), `"message":"verdict"`) || !strings.Contains(logs.String(), `"reason":"alice: looked fine"`) {
		t.Fatalf("verdict line with reason missing after resume; logs:\n%s", logs.String())
	}
}

func TestReviewerRejects(t *testing.T) {
	c, d, p := &fakeClassifier{score: 0.5}, &fakeDiffs{}, &fakePublisher{}
	runner := newRunner(c, d, p)
	input := released(t)
	runner.RunUntilComplete(t, input)
	cb := runner.OpenCallbacks()[0].CallbackID
	if err := runner.SendCallbackSuccess(cb, events.ReviewDecided{CallbackID: cb, Decision: events.DecisionReject, Reviewer: "bob"}); err != nil {
		t.Fatal(err)
	}
	v := verdictOf(t, runner.RunUntilComplete(t, input))
	if v.Verdict != events.VerdictBlocked || v.DecidedBy != events.DecidedByReviewer {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestReviewTimeoutBlocks(t *testing.T) {
	c, d, p := &fakeClassifier{score: 0.5}, &fakeDiffs{}, &fakePublisher{}
	runner := newRunner(c, d, p)
	input := released(t)
	runner.RunUntilComplete(t, input)
	if err := runner.TimeoutCallback(runner.OpenCallbacks()[0].CallbackID); err != nil {
		t.Fatal(err)
	}
	v := verdictOf(t, runner.RunUntilComplete(t, input))
	if v.Verdict != events.VerdictBlocked || v.DecidedBy != events.DecidedByTimeout {
		t.Fatalf("verdict = %+v", v)
	}
	assertVerdictEvent(t, p, "rel-1", v)
}

func TestClassifierFailureEscalates(t *testing.T) {
	c, d, p := &fakeClassifier{err: errors.New("jev down")}, &fakeDiffs{}, &fakePublisher{}
	logs := &syncBuffer{}
	runner := newRunner(c, d, p, durable.WithLogHandler(logging.Handler(logs, "inspector")))
	res := runner.RunUntilComplete(t, released(t))
	if res.Status != durabletest.Pending || len(runner.OpenCallbacks()) != 1 {
		t.Fatalf("status = %s, callbacks = %+v", res.Status, runner.OpenCallbacks())
	}
	if c.callCount() != 2 {
		t.Fatalf("classifier calls = %d, want 2 (one retry)", c.callCount())
	}
	var req events.ReviewRequested
	if err := p.ofType(events.TypeReviewRequested)[0].DataAs(&req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.Reason, "classifier unavailable") || !strings.Contains(req.Reason, "jev down") {
		t.Fatalf("reason = %q", req.Reason)
	}
	if out := logs.String(); strings.Contains(out, "execution suspended") || !strings.Contains(out, "classifier unavailable") {
		t.Fatalf("want the classifier failure logged and no suspension handled as a failure; logs:\n%s", out)
	}
}

func TestTransientFetchFailureRetriesThenBlocks(t *testing.T) {
	c, d, p := &fakeClassifier{}, &fakeDiffs{err: errors.New("github 502")}, &fakePublisher{}
	v := verdictOf(t, newRunner(c, d, p).RunUntilComplete(t, released(t)))
	if v.Verdict != events.VerdictBlocked || v.DecidedBy != events.DecidedByError || !strings.Contains(v.Reason, "github 502") {
		t.Fatalf("verdict = %+v", v)
	}
	if d.callCount() != 2 || c.callCount() != 0 {
		t.Fatalf("diff calls = %d, want 2 (one retry); classifier calls = %d", d.callCount(), c.callCount())
	}
	assertVerdictEvent(t, p, "rel-1", v)
}

func TestMissingRepoBlocksWithoutClassifying(t *testing.T) {
	c, d, p := &fakeClassifier{}, &fakeDiffs{err: fmt.Errorf("%w: 404", diff.ErrNotFound)}, &fakePublisher{}
	v := verdictOf(t, newRunner(c, d, p).RunUntilComplete(t, released(t)))
	if v.Verdict != events.VerdictBlocked || v.DecidedBy != events.DecidedByError || c.callCount() != 0 || d.callCount() != 1 {
		t.Fatalf("verdict = %+v, classifier calls = %d, diff calls = %d", v, c.callCount(), d.callCount())
	}
	assertVerdictEvent(t, p, "rel-1", v)
}

func TestRejectsOtherEventTypes(t *testing.T) {
	e, err := events.New("x-1", events.SourceCLI, events.TypeReviewDecided, "npm/left-pad", events.ReviewDecided{})
	if err != nil {
		t.Fatal(err)
	}
	res := newRunner(&fakeClassifier{}, &fakeDiffs{}, &fakePublisher{}).RunUntilComplete(t, e)
	if res.Status != durabletest.Failed {
		t.Fatalf("status = %s, want FAILED", res.Status)
	}
}
