package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRuntimeConfigRejectsDuplicateRegions(t *testing.T) {
	path := writeRuntimeConfigFile(t, `
providers:
  us-east-1:
    port: 8081
  us-east-1:
    port: 8082
`)

	_, err := loadRuntimeConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate region "us-east-1"`)
}

func TestLoadRuntimeConfigRejectsDuplicatePorts(t *testing.T) {
	path := writeRuntimeConfigFile(t, `
router:
  backendRPCTimeout: 5s
providers:
  us-east-1:
    port: 8081
  us-west-2:
    port: 8081
`)

	_, err := loadRuntimeConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicates providers.")
	assert.Contains(t, err.Error(), ".port (8081)")
}

func TestResolveProviderSettingsAppliesDefaultsAndOverrides(t *testing.T) {
	path := writeRuntimeConfigFile(t, `
router:
  backendRPCTimeout: 5s
providerDefaults:
  clusterName: dev
  nodeGroupAutoDiscovery:
    - asg:tag=k8s.io/cluster-autoscaler/dev
  skipNodesWithLocalStorage: true
  skipNodesWithSystemPods: false
providers:
  us-east-1:
    port: 8081
  us-west-2:
    port: 8082
    clusterName: dev-west
    nodeGroupAutoDiscovery:
      - asg:tag=environment/dev-west
    skipNodesWithSystemPods: true
`)

	cfg, err := loadRuntimeConfig(path)
	require.NoError(t, err)

	settings, err := cfg.ResolveProviderSettings(ProviderRuntimeSettings{
		Region:                    "us-west-2",
		GRPCAddress:               ":9000",
		ClusterName:               "fallback",
		NodeGroupAutoDiscovery:    []string{"asg:tag=fallback"},
		SkipNodesWithLocalStorage: false,
		SkipNodesWithSystemPods:   false,
	})
	require.NoError(t, err)

	assert.Equal(t, "us-west-2", settings.Region)
	assert.Equal(t, ":8082", settings.GRPCAddress)
	assert.Equal(t, "dev-west", settings.ClusterName)
	assert.Equal(t, []string{"asg:tag=environment/dev-west"}, settings.NodeGroupAutoDiscovery)
	assert.True(t, settings.SkipNodesWithLocalStorage)
	assert.True(t, settings.SkipNodesWithSystemPods)
}

func TestResolveRouterSettingsBuildsBackendsFromProviders(t *testing.T) {
	path := writeRuntimeConfigFile(t, `
router:
  grpcAddress: :18086
  httpAddress: :18080
  cacheTTL: 45s
  backendRPCTimeout: 6s
providers:
  us-east-1:
    port: 8081
    rpcTimeout: 7s
  us-west-2:
    port: 8082
`)

	cfg, err := loadRuntimeConfig(path)
	require.NoError(t, err)

	settings := cfg.ResolveRouterSettings(RouterRuntimeSettings{
		GRPCAddress:       ":8086",
		HTTPAddress:       ":8080",
		CacheTTL:          defaultCacheTTL,
		BackendRPCTimeout: defaultBackendRPCTimeout,
	})

	assert.Equal(t, ":18086", settings.GRPCAddress)
	assert.Equal(t, ":18080", settings.HTTPAddress)
	assert.Equal(t, 45*time.Second, settings.CacheTTL)
	assert.Equal(t, 6*time.Second, settings.BackendRPCTimeout)
	assert.Equal(t, []Backend{
		{Provider: "aws", Region: "us-east-1", Address: "127.0.0.1:8081", RPCTimeout: 7 * time.Second},
		{Provider: "aws", Region: "us-west-2", Address: "127.0.0.1:8082"},
	}, settings.Backends)
}

func TestResolveProviderSettingsRejectsUnknownRegion(t *testing.T) {
	cfg := &RuntimeConfig{
		Router: RouterRuntimeConfig{
			BackendRPCTimeout: yamlDuration{Duration: 5 * time.Second},
		},
		Providers: map[string]ProviderRef{
			"us-east-1": {Port: 8081},
		},
	}

	_, err := cfg.ResolveProviderSettings(ProviderRuntimeSettings{Region: "us-west-2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `does not contain region "us-west-2"`)
}

func TestLoadRuntimeConfigRejectsZeroBackendRPCTimeout(t *testing.T) {
	path := writeRuntimeConfigFile(t, `
router:
  backendRPCTimeout: 0s
providers:
  us-east-1:
    port: 8081
`)

	_, err := loadRuntimeConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "router.backendRPCTimeout must be greater than zero")
}

func TestLoadRuntimeConfigAllowsOmittedCacheTTL(t *testing.T) {
	path := writeRuntimeConfigFile(t, `
router:
  backendRPCTimeout: 5s
providers:
  us-east-1:
    port: 8081
`)

	_, err := loadRuntimeConfig(path)
	require.NoError(t, err)
}

func TestLoadRuntimeConfigRejectsZeroCacheTTL(t *testing.T) {
	path := writeRuntimeConfigFile(t, `
router:
  cacheTTL: 0s
  backendRPCTimeout: 5s
providers:
  us-east-1:
    port: 8081
`)

	_, err := loadRuntimeConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "router.cacheTTL must be greater than zero")
}

func TestLoadRuntimeConfigRejectsZeroProviderRPCTimeout(t *testing.T) {
	path := writeRuntimeConfigFile(t, `
router:
  backendRPCTimeout: 5s
providers:
  us-east-1:
    port: 8081
    rpcTimeout: 0s
`)

	_, err := loadRuntimeConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "providers.us-east-1.rpcTimeout must be greater than zero when set")
}

func writeRuntimeConfigFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "runtime-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}
