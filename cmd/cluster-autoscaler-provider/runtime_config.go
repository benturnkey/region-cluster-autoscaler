package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const routerBackendHost = "127.0.0.1"

type yamlDuration struct {
	time.Duration
	Set bool
}

func (d *yamlDuration) UnmarshalYAML(node *yaml.Node) error {
	d.Set = true

	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("must be a duration string")
	}

	value := strings.TrimSpace(node.Value)
	if value == "" {
		d.Duration = 0
		return nil
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value, err)
	}

	d.Duration = parsed
	return nil
}

type RuntimeConfig struct {
	Router           RouterRuntimeConfig    `yaml:"router"`
	ProviderDefaults ProviderRuntimeOptions `yaml:"providerDefaults"`
	Providers        map[string]ProviderRef `yaml:"providers"`
}

type RouterRuntimeConfig struct {
	GRPCAddress       string       `yaml:"grpcAddress"`
	HTTPAddress       string       `yaml:"httpAddress"`
	CacheTTL          yamlDuration `yaml:"cacheTTL"`
	BackendRPCTimeout yamlDuration `yaml:"backendRPCTimeout"`
}

type ProviderRuntimeOptions struct {
	ClusterName               string   `yaml:"clusterName"`
	NodeGroupAutoDiscovery    []string `yaml:"nodeGroupAutoDiscovery"`
	SkipNodesWithLocalStorage *bool    `yaml:"skipNodesWithLocalStorage"`
	SkipNodesWithSystemPods   *bool    `yaml:"skipNodesWithSystemPods"`
}

type ProviderRef struct {
	Port                   int          `yaml:"port"`
	RPCTimeout             yamlDuration `yaml:"rpcTimeout"`
	ProviderRuntimeOptions `yaml:",inline"`
}

type ProviderRuntimeSettings struct {
	Region                    string
	GRPCAddress               string
	ClusterName               string
	NodeGroupAutoDiscovery    []string
	SkipNodesWithLocalStorage bool
	SkipNodesWithSystemPods   bool
}

type RouterRuntimeSettings struct {
	GRPCAddress       string
	HTTPAddress       string
	CacheTTL          time.Duration
	BackendRPCTimeout time.Duration
	Backends          []Backend
}

func loadRuntimeConfig(path string) (*RuntimeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read runtime config %q: %w", path, err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse runtime config %q: %w", path, err)
	}

	if err := validateRuntimeConfigNode(&root); err != nil {
		return nil, fmt.Errorf("validate runtime config %q: %w", path, err)
	}

	var cfg RuntimeConfig
	if err := root.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode runtime config %q: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid runtime config %q: %w", path, err)
	}

	return &cfg, nil
}

func validateRuntimeConfigNode(root *yaml.Node) error {
	if len(root.Content) == 0 {
		return fmt.Errorf("document is empty")
	}

	document := root.Content[0]
	if document.Kind != yaml.MappingNode {
		return fmt.Errorf("top-level document must be a mapping")
	}

	return validateUniqueProviderRegions(document)
}

func validateUniqueProviderRegions(document *yaml.Node) error {
	providersNode := mappingValue(document, "providers")
	if providersNode == nil {
		return nil
	}
	if providersNode.Kind != yaml.MappingNode {
		return fmt.Errorf("providers must be a mapping")
	}

	seen := make(map[string]struct{}, len(providersNode.Content)/2)
	for i := 0; i < len(providersNode.Content); i += 2 {
		keyNode := providersNode.Content[i]
		region := strings.TrimSpace(keyNode.Value)
		if _, ok := seen[region]; ok {
			return fmt.Errorf("providers contains duplicate region %q", region)
		}
		seen[region] = struct{}{}
	}

	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func (c *RuntimeConfig) Validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("providers must contain at least one region")
	}

	if c.Router.CacheTTL.Set && c.Router.CacheTTL.Duration <= 0 {
		return fmt.Errorf("router.cacheTTL must be greater than zero")
	}
	if c.Router.BackendRPCTimeout.Set && c.Router.BackendRPCTimeout.Duration <= 0 {
		return fmt.Errorf("router.backendRPCTimeout must be greater than zero")
	}

	seenPorts := make(map[int]string, len(c.Providers))
	for region, provider := range c.Providers {
		trimmedRegion := strings.TrimSpace(region)
		if trimmedRegion == "" {
			return fmt.Errorf("providers contains an empty region key")
		}
		if trimmedRegion != region {
			return fmt.Errorf("providers contains region key %q with surrounding whitespace", region)
		}
		if provider.Port <= 0 || provider.Port > 65535 {
			return fmt.Errorf("providers.%s.port must be between 1 and 65535", region)
		}
		if previous, ok := seenPorts[provider.Port]; ok {
			return fmt.Errorf("providers.%s.port duplicates providers.%s.port (%d)", region, previous, provider.Port)
		}
		seenPorts[provider.Port] = region
		if provider.RPCTimeout.Set && provider.RPCTimeout.Duration < 0 {
			return fmt.Errorf("providers.%s.rpcTimeout must not be negative", region)
		}
		if provider.RPCTimeout.Set && provider.RPCTimeout.Duration == 0 {
			return fmt.Errorf("providers.%s.rpcTimeout must be greater than zero when set", region)
		}
		if err := validateNodeGroupAutoDiscovery(fmt.Sprintf("providers.%s", region), provider.NodeGroupAutoDiscovery); err != nil {
			return err
		}
	}

	return validateNodeGroupAutoDiscovery("providerDefaults", c.ProviderDefaults.NodeGroupAutoDiscovery)
}

func validateNodeGroupAutoDiscovery(path string, values []string) error {
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s.nodeGroupAutoDiscovery[%d] must not be empty", path, i)
		}
	}
	return nil
}

func (c *RuntimeConfig) ResolveRouterSettings(defaults RouterRuntimeSettings) RouterRuntimeSettings {
	settings := defaults

	if c.Router.GRPCAddress != "" {
		settings.GRPCAddress = c.Router.GRPCAddress
	}
	if c.Router.HTTPAddress != "" {
		settings.HTTPAddress = c.Router.HTTPAddress
	}
	if c.Router.CacheTTL.Duration > 0 {
		settings.CacheTTL = c.Router.CacheTTL.Duration
	}
	if c.Router.BackendRPCTimeout.Duration > 0 {
		settings.BackendRPCTimeout = c.Router.BackendRPCTimeout.Duration
	}

	regions := make([]string, 0, len(c.Providers))
	for region := range c.Providers {
		regions = append(regions, region)
	}
	sort.Strings(regions)

	backends := make([]Backend, 0, len(c.Providers))
	for _, region := range regions {
		provider := c.Providers[region]
		backends = append(backends, Backend{
			Provider:   "aws",
			Region:     region,
			Address:    fmt.Sprintf("%s:%d", routerBackendHost, provider.Port),
			RPCTimeout: provider.RPCTimeout.Duration,
		})
	}
	settings.Backends = backends
	return settings
}

func (c *RuntimeConfig) ResolveProviderSettings(defaults ProviderRuntimeSettings) (ProviderRuntimeSettings, error) {
	provider, ok := c.Providers[defaults.Region]
	if !ok {
		return ProviderRuntimeSettings{}, fmt.Errorf("providers does not contain region %q", defaults.Region)
	}

	settings := defaults
	settings.GRPCAddress = fmt.Sprintf(":%d", provider.Port)
	settings.ClusterName = firstNonEmpty(provider.ClusterName, c.ProviderDefaults.ClusterName, defaults.ClusterName)
	settings.NodeGroupAutoDiscovery = mergeStringList(defaults.NodeGroupAutoDiscovery, c.ProviderDefaults.NodeGroupAutoDiscovery, provider.NodeGroupAutoDiscovery)
	settings.SkipNodesWithLocalStorage = mergeBool(defaults.SkipNodesWithLocalStorage, c.ProviderDefaults.SkipNodesWithLocalStorage, provider.SkipNodesWithLocalStorage)
	settings.SkipNodesWithSystemPods = mergeBool(defaults.SkipNodesWithSystemPods, c.ProviderDefaults.SkipNodesWithSystemPods, provider.SkipNodesWithSystemPods)

	return settings, nil
}

func mergeStringList(fallback []string, defaults []string, override []string) []string {
	switch {
	case override != nil:
		return append([]string(nil), override...)
	case defaults != nil:
		return append([]string(nil), defaults...)
	default:
		return append([]string(nil), fallback...)
	}
}

func mergeBool(fallback bool, defaults *bool, override *bool) bool {
	switch {
	case override != nil:
		return *override
	case defaults != nil:
		return *defaults
	default:
		return fallback
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
