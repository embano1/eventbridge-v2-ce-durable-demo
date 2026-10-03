package diff

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path != "/repos/stevemao/left-pad/compare/v1.2.0...v1.3.0":
			http.NotFound(w, r)
		case r.Header.Get("Accept") != "application/vnd.github.diff":
			http.Error(w, "accept", http.StatusBadRequest)
		case r.Header.Get("Authorization") != "Bearer t0k":
			http.Error(w, "auth", http.StatusUnauthorized)
		default:
			fmt.Fprint(w, "diff --git a/index.js b/index.js\n+hello\n")
		}
	}))
	defer srv.Close()
	d, err := (&GitHub{BaseURL: srv.URL, Token: "t0k"}).Compare(t.Context(), "stevemao/left-pad", "v1.2.0", "v1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Text, "+hello") || d.Truncated || d.URL != "https://github.com/stevemao/left-pad/compare/v1.2.0...v1.3.0" {
		t.Fatalf("diff = %+v", d)
	}
}

func TestCompareNotFoundIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := (&GitHub{BaseURL: srv.URL}).Compare(t.Context(), "o/r", "v1", "v2")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestCompareServerErrorIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusBadGateway) }))
	defer srv.Close()
	_, err := (&GitHub{BaseURL: srv.URL}).Compare(t.Context(), "o/r", "v1", "v2")
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidRepo) {
		t.Fatalf("err = %v", err)
	}
}

func TestCompareTruncatesLargeDiffs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, strings.Repeat("a", 100)) }))
	defer srv.Close()
	d, err := (&GitHub{BaseURL: srv.URL, MaxBytes: 10}).Compare(t.Context(), "o/r", "v1", "v2")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Text) != 10 || !d.Truncated {
		t.Fatalf("len = %d, truncated = %v", len(d.Text), d.Truncated)
	}
}

// A malformed repo or ref must be rejected before any HTTP request is built:
// both are ErrInvalidRepo, never ErrNotFound (that's reserved for GitHub 404/422).
func TestCompareRejectsHostileInputBeforeRequesting(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL}
	for _, in := range [][3]string{
		{"../../orgs/x", "v1", "v2"},
		{"a/b/c", "v1", "v2"},
		{"a", "v1", "v2"},
		{"o/r", "../x", "v2"},
		{"o/r", "v1", "v2?x=1"},
		{"o/r", "", "v2"},
	} {
		if _, err := g.Compare(t.Context(), in[0], in[1], in[2]); !errors.Is(err, ErrInvalidRepo) {
			t.Errorf("Compare%v err = %v, want ErrInvalidRepo", in, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("server hit %d times", hits.Load())
	}
}
