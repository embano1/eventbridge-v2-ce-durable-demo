package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNewUsesSDKFieldNames(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, "trail").Info("hello", "ce_id", "id-1")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not JSON: %s", buf.String())
	}
	for key, want := range map[string]any{"message": "hello", "level": "INFO", "component": "trail", "ce_id": "id-1"} {
		if line[key] != want {
			t.Errorf("%s = %v, want %v", key, line[key], want)
		}
	}
	if _, ok := line["timestamp"]; !ok {
		t.Errorf("missing timestamp: %s", buf.String())
	}
}
