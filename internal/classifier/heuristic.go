package classifier

import (
	"context"
	"math"
	"regexp"
	"strings"
)

type signal struct {
	re     *regexp.Regexp
	weight float64
}

var signals = []signal{
	{regexp.MustCompile(`"(pre|post)?install"\s*:`), 0.35},
	{regexp.MustCompile(`\beval\(|new Function\(`), 0.3},
	{regexp.MustCompile(`child_process|\bexecSync\(|\bspawn\(`), 0.25},
	{regexp.MustCompile(`atob\(|base64`), 0.2},
	{regexp.MustCompile(`process\.env|\.npmrc|\.aws/credentials|id_rsa`), 0.3},
	{regexp.MustCompile(`https?://\d{1,3}(\.\d{1,3}){3}`), 0.3},
}

// Heuristic is the offline fallback when no Jev key is configured: weighted regexes over added lines.
type Heuristic struct{}

func (Heuristic) Classify(_ context.Context, diff string) (Result, error) {
	var score float64
	for _, s := range signals {
		if matchesAddedLine(diff, s.re) {
			score += s.weight
		}
	}
	return Result{Score: math.Min(score, 1), Model: "heuristic"}, nil
}

func matchesAddedLine(diff string, re *regexp.Regexp) bool {
	for line := range strings.Lines(diff) {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") && re.MatchString(line) {
			return true
		}
	}
	return false
}
