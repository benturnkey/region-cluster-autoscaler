package main

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	content := `
cacheTTL: 10s
backends:
  - provider: aws
    region: us-east-1
    port: 8081
  - provider: aws
    region: us-west-2
    port: 8082
`
	tmpfile, err := os.CreateTemp("", "config.yaml")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, os.Remove(tmpfile.Name()))
	}()

	_, err = tmpfile.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	cfg, err := LoadConfig(tmpfile.Name())
	require.NoError(t, err)

	assert.Equal(t, 10*time.Second, time.Duration(cfg.CacheTTL))
	assert.Equal(t, 5*time.Second, time.Duration(cfg.BackendRPCTimeout))
	assert.Equal(t, 2, len(cfg.Backends))
	assert.Equal(t, "us-east-1", cfg.Backends[0].Region)
	assert.Equal(t, 8081, cfg.Backends[0].Port)
}

func TestGetBackendForRegion(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{
			{Region: "us-east-1", Port: 8081},
		},
	}

	backend, err := cfg.GetBackendForRegion("us-east-1")
	require.NoError(t, err)
	assert.Equal(t, 8081, backend.Port)

	_, err = cfg.GetBackendForRegion("unknown")
	assert.Error(t, err)
}

func TestConfigValidateRejectsDuplicateRegions(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{
			{Provider: "aws", Region: "us-east-1", Port: 8081},
			{Provider: "aws", Region: "us-east-1", Port: 8082},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `duplicates region "us-east-1"`)
}

func TestConfigValidateRejectsDuplicatePorts(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{
			{Provider: "aws", Region: "us-east-1", Port: 8081},
			{Provider: "aws", Region: "us-west-2", Port: 8081},
		},
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicates port 8081")
}

func TestConfigValidateRejectsMissingBackends(t *testing.T) {
	cfg := &Config{}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one backend")
}

func TestConfigSummary(t *testing.T) {
	cfg := &Config{
		Backends: []BackendConfig{
			{Provider: "aws", Region: "us-east-1", Port: 8081},
			{Provider: "aws", Region: "us-west-2", Port: 8082},
		},
	}

	assert.Equal(t, "us-east-1:8081, us-west-2:8082", cfg.Summary())
}
