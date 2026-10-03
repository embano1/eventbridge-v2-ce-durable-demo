package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	DefaultJevURL   = "https://api.typesafe.ai"
	DefaultJevModel = "jev-latest"
	questionKey     = "suspicious"
	instructions    = "This is a unified git diff between two releases of an open-source package. Does it introduce suspicious behavior: new or changed install scripts, obfuscated or encoded code, network calls to unexpected hosts, or reading credentials, tokens or environment variables?"

	// maxResponseBytes caps a well-formed 200 body so a misbehaving endpoint can't exhaust memory.
	maxResponseBytes = 1 << 20 // 1 MiB
)

var criteria = map[string]string{
	"true":  "The diff adds code that looks like malware or a supply-chain attack.",
	"false": "The diff looks like ordinary maintenance: features, fixes, docs, tests or refactoring.",
}

// Jev scores the diff with Typesafe's Jev model through the systemone API.
type Jev struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

type jevQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type jevRequest struct {
	State     string                 `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]jevQuestion `json:"questions"`
}

type jevResponse struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Noul *float64 `json:"noul"`
	} `json:"answers"`
}

func (j *Jev) Classify(ctx context.Context, diff string) (Result, error) {
	body, err := json.Marshal(jevRequest{
		State:     diff,
		Model:     j.Model,
		Questions: map[string]jevQuestion{questionKey: {Type: "noul", Instructions: instructions, Criteria: criteria}},
	})
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(j.BaseURL, "/")+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+j.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := j.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("jev: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Result{}, fmt.Errorf("jev: status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	var out jevResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return Result{}, fmt.Errorf("jev: decode: %w", err)
	}
	a, ok := out.Answers[questionKey]
	if !ok || a.Noul == nil {
		return Result{}, fmt.Errorf("jev: no answer for %q", questionKey)
	}
	// Fail closed: an out-of-range score would otherwise silently band as Clean or Suspicious.
	if *a.Noul < 0 || *a.Noul > 1 {
		return Result{}, fmt.Errorf("jev: score %v out of range [0,1]", *a.Noul)
	}
	return Result{Score: *a.Noul, Model: out.Model}, nil
}
