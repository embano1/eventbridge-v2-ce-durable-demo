// Package diff fetches the unified diff between two tags of a GitHub repository.
package diff

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrNotFound = errors.New("github: repository or tag not found")
	// ErrInvalidRepo covers both a malformed repository string and a malformed ref (tag).
	ErrInvalidRepo = errors.New("github: repository must be <owner>/<name>")

	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	refPattern  = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
)

const defaultMaxBytes = 64 << 10

type Diff struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	URL       string `json:"url"`
}

type GitHub struct {
	BaseURL  string
	Token    string // optional; unauthenticated calls get 60 requests per hour
	MaxBytes int    // caps what lands in the durable checkpoint
	HTTP     *http.Client
}

func (g *GitHub) Compare(ctx context.Context, repo, from, to string) (Diff, error) {
	if !repoPattern.MatchString(repo) || strings.Contains(repo, "..") {
		return Diff{}, fmt.Errorf("%w: invalid repo %q", ErrInvalidRepo, repo)
	}
	for _, ref := range []string{from, to} {
		if !refPattern.MatchString(ref) || strings.Contains(ref, "..") {
			return Diff{}, fmt.Errorf("%w: invalid tag %q", ErrInvalidRepo, ref)
		}
	}
	base := g.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	u := fmt.Sprintf("%s/repos/%s/compare/%s...%s", strings.TrimRight(base, "/"), repo, url.PathEscape(from), url.PathEscape(to))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Diff{}, err
	}
	req.Header.Set("Accept", "application/vnd.github.diff")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	client := g.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Diff{}, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity:
		return Diff{}, fmt.Errorf("%w: %s %s...%s", ErrNotFound, repo, from, to)
	case resp.StatusCode != http.StatusOK:
		return Diff{}, fmt.Errorf("github: status %d", resp.StatusCode)
	}
	limit := g.MaxBytes
	if limit <= 0 {
		limit = defaultMaxBytes
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return Diff{}, fmt.Errorf("github: read diff: %w", err)
	}
	d := Diff{URL: fmt.Sprintf("https://github.com/%s/compare/%s...%s", repo, from, to)}
	if len(b) > limit {
		b, d.Truncated = b[:limit], true
	}
	d.Text = strings.ToValidUTF8(string(b), "")
	return d, nil
}
