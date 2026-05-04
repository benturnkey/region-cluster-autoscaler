package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

type Config struct {
	CacheTTL          Duration        `json:"cacheTTL"`
	BackendRPCTimeout Duration        `json:"backendRPCTimeout"`
	Backends          []BackendConfig `json:"backends"`
}

type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := yaml.Unmarshal(b, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
}

type BackendConfig struct {
	Provider string `json:"provider"`
	Region   string `json:"region"`
	Port     int    `json:"port"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Set defaults
	if config.CacheTTL == 0 {
		config.CacheTTL = Duration(15 * time.Second)
	}
	if config.BackendRPCTimeout == 0 {
		config.BackendRPCTimeout = Duration(5 * time.Second)
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	return &config, nil
}

func (c *Config) GetBackendForRegion(region string) (*BackendConfig, error) {
	for i := range c.Backends {
		if c.Backends[i].Region == region {
			return &c.Backends[i], nil
		}
	}
	return nil, fmt.Errorf("no backend found for region %q", region)
}

func (c *Config) Validate() error {
	if len(c.Backends) == 0 {
		return fmt.Errorf("config must define at least one backend")
	}

	seenRegions := make(map[string]int, len(c.Backends))
	seenPorts := make(map[int]int, len(c.Backends))
	for i, backend := range c.Backends {
		if strings.TrimSpace(backend.Provider) == "" {
			return fmt.Errorf("backend[%d] provider must not be empty", i)
		}
		if strings.TrimSpace(backend.Region) == "" {
			return fmt.Errorf("backend[%d] region must not be empty", i)
		}
		if backend.Port <= 0 || backend.Port > 65535 {
			return fmt.Errorf("backend[%d] port %d is out of range", i, backend.Port)
		}

		if previous, exists := seenRegions[backend.Region]; exists {
			return fmt.Errorf("backend[%d] duplicates region %q from backend[%d]", i, backend.Region, previous)
		}
		seenRegions[backend.Region] = i

		if previous, exists := seenPorts[backend.Port]; exists {
			return fmt.Errorf("backend[%d] duplicates port %d from backend[%d]", i, backend.Port, previous)
		}
		seenPorts[backend.Port] = i
	}

	return nil
}

func (c *Config) Summary() string {
	parts := make([]string, 0, len(c.Backends))
	for _, backend := range c.Backends {
		parts = append(parts, fmt.Sprintf("%s:%d", backend.Region, backend.Port))
	}
	return strings.Join(parts, ", ")
}
