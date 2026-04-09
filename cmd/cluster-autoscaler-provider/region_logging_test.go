package main

import (
	"strings"
	"testing"
)

func TestRegionLogHelpersPrefixMessages(t *testing.T) {
	gotInfo := captureKlogOutput(t, func() {
		logRegionInfo("us-east-1", "refresh_provider")
	})
	if !strings.Contains(gotInfo, `"regional operation"`) || !strings.Contains(gotInfo, `region="us-east-1"`) || !strings.Contains(gotInfo, `op="refresh_provider"`) {
		t.Fatalf("info log = %q, want structured region message", gotInfo)
	}

	gotError := captureKlogOutput(t, func() {
		logRegionError("us-west-2", "refresh_provider_failed", "error", errBoom)
	})
	if !strings.Contains(gotError, `"regional operation"`) || !strings.Contains(gotError, `region="us-west-2"`) || !strings.Contains(gotError, `op="refresh_provider_failed"`) || !strings.Contains(gotError, `error="boom"`) {
		t.Fatalf("error log = %q, want structured region message", gotError)
	}
}
