package main

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatBackendSummary(t *testing.T) {
	assert.Equal(t, "us-east-1=127.0.0.1:8081, us-west-2=127.0.0.1:8082", formatBackendSummary([]Backend{
		{Provider: "aws", Region: "us-east-1", Address: "127.0.0.1:8081"},
		{Provider: "aws", Region: "us-west-2", Address: "127.0.0.1:8082"},
	}))
}

func TestDurationEnvDefault(t *testing.T) {
	t.Setenv("CLUSTER_AUTOSCALER_PROVIDER_CACHE_TTL", "30s")
	assert.Equal(t, 30*time.Second, durationEnvDefault("CLUSTER_AUTOSCALER_PROVIDER_CACHE_TTL", defaultCacheTTL))
}

func TestDurationEnvDefaultExitsForInvalidValue(t *testing.T) {
	if os.Getenv("TEST_DURATION_ENV_DEFAULT_INVALID") == "1" {
		_ = durationEnvDefault("CLUSTER_AUTOSCALER_PROVIDER_CACHE_TTL", defaultCacheTTL)
		t.Fatal("expected process to exit")
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestDurationEnvDefaultExitsForInvalidValue")
	cmd.Env = append(os.Environ(),
		"TEST_DURATION_ENV_DEFAULT_INVALID=1",
		"CLUSTER_AUTOSCALER_PROVIDER_CACHE_TTL=not-a-duration",
	)

	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Contains(t, string(output), `FATAL: invalid duration "not-a-duration" for environment variable CLUSTER_AUTOSCALER_PROVIDER_CACHE_TTL`)
}

func TestFirstStringEnvDefaultIgnoresEmptyValues(t *testing.T) {
	t.Setenv("PRIMARY_STRING", "   ")
	t.Setenv("SECONDARY_STRING", "router")

	assert.Equal(t, "router", firstStringEnvDefault([]string{"PRIMARY_STRING", "SECONDARY_STRING"}, "provider"))
}

func TestFirstBoolEnvDefault(t *testing.T) {
	t.Setenv("PRIMARY_BOOL", "")
	t.Setenv("SECONDARY_BOOL", "true")

	assert.True(t, firstBoolEnvDefault([]string{"PRIMARY_BOOL", "SECONDARY_BOOL"}, false))
}
