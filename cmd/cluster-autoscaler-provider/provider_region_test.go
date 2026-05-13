package main

import "testing"

func TestRegionFromProviderID(t *testing.T) {
	t.Parallel()

	if got := regionFromProviderID("aws:///us-east-1a/i-123"); got != "us-east-1" {
		t.Fatalf("regionFromProviderID() = %q, want %q", got, "us-east-1")
	}
}

func TestRegionFromProviderIDRejectsInvalidPrefix(t *testing.T) {
	t.Parallel()

	if got := regionFromProviderID("gce:///us-east-1a/i-123"); got != "" {
		t.Fatalf("regionFromProviderID() = %q, want empty string", got)
	}
}
