package events

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"
)

func decode(t *testing.T, b []byte) (ce.Event, error) {
	t.Helper()
	var e ce.Event
	if err := json.Unmarshal(b, &e); err != nil {
		return e, err
	}
	return e, e.Validate()
}

func TestNewRoundTrip(t *testing.T) {
	want := Released{Ecosystem: "npm", Name: "left-pad", Repo: "stevemao/left-pad", FromTag: "v1.2.0", ToTag: "v1.3.0"}
	e, err := New("id-1", SourceCLI, TypePackageReleased, Subject("npm", "left-pad"), want)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decode(t, b)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != "id-1" || got.Type() != TypePackageReleased || got.Subject() != "npm/left-pad" || got.SpecVersion() != "1.0" {
		t.Fatalf("context = %v", got.Context)
	}
	var data Released
	if err := got.DataAs(&data); err != nil {
		t.Fatal(err)
	}
	if data != want {
		t.Fatalf("data = %+v, want %+v", data, want)
	}
}

func TestDecodesObservedRawDelivery(t *testing.T) {
	b, err := os.ReadFile("testdata/raw-delivery.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := decode(t, b)
	if err != nil {
		t.Fatal(err)
	}
	var rel Released
	if err := e.DataAs(&rel); err != nil {
		t.Fatal(err)
	}
	if e.ID() != "4612ef93-550b-46e1-8bbf-0c2eba4b3706" || e.Type() != TypePackageReleased || e.Subject() != "npm/left-pad" || rel.Repo != "stevemao/left-pad" {
		t.Fatalf("event = %v, data = %+v", e, rel)
	}
}

func TestRejectsPayloadsThatAreNotOneCloudEvent(t *testing.T) {
	envelope, err := os.ReadFile("testdata/with-metadata-delivery.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/raw-delivery.json")
	if err != nil {
		t.Fatal(err)
	}
	batch := []byte("[" + strings.TrimSpace(string(raw)) + "]")
	for name, b := range map[string][]byte{"with-metadata envelope": envelope, "batch array": batch} {
		if _, err := decode(t, b); err == nil {
			t.Errorf("%s decoded as a CloudEvent", name)
		}
	}
}

func TestLogAttrs(t *testing.T) {
	e, err := New("id-2", SourceCLI, TypeReviewDecided, "npm/x", ReviewDecided{CallbackID: "cb"})
	if err != nil {
		t.Fatal(err)
	}
	attrs := fmt.Sprint(LogAttrs(e))
	for _, want := range []string{"ce_id id-2", "ce_type " + TypeReviewDecided, "ce_source " + SourceCLI, "ce_subject npm/x"} {
		if !strings.Contains(attrs, want) {
			t.Errorf("LogAttrs = %s, missing %q", attrs, want)
		}
	}
}
