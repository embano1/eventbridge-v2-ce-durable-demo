// Package classifier scores how suspicious a release diff looks, from 0 (benign) to 1 (malicious).
package classifier

import (
	"context"
	"net/http"
	"time"
)

type Result struct {
	Score float64 `json:"score"`
	Model string  `json:"model"`
}

type Classifier interface {
	Classify(ctx context.Context, diff string) (Result, error)
}

type Band string

const (
	Clean      Band = "clean"
	Uncertain  Band = "uncertain"
	Suspicious Band = "suspicious"

	SuspiciousAt = 0.8
	CleanAt      = 0.2
)

func BandOf(score float64) Band {
	switch {
	case score >= SuspiciousAt:
		return Suspicious
	case score <= CleanAt:
		return Clean
	}
	return Uncertain
}

// NoKey is the value the stack stores in the secret until someone sets a real Jev key.
const NoKey = "none"

func New(apiKey, baseURL string) Classifier {
	if apiKey == "" || apiKey == NoKey {
		return Heuristic{}
	}
	if baseURL == "" {
		baseURL = DefaultJevURL
	}
	return &Jev{BaseURL: baseURL, APIKey: apiKey, Model: DefaultJevModel, HTTP: &http.Client{Timeout: 30 * time.Second}}
}
