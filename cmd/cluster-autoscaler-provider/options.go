package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCacheTTL          = 15 * time.Second
	defaultBackendRPCTimeout = 5 * time.Second
)

type multiStringFlag struct {
	values  []string
	envVars []string
}

func (f *multiStringFlag) String() string {
	return "[" + strings.Join(f.Values(), " ") + "]"
}

func (f *multiStringFlag) Set(value string) error {
	f.values = append(f.values, value)
	return nil
}

func (f *multiStringFlag) Values() []string {
	if len(f.values) > 0 {
		return append([]string(nil), f.values...)
	}
	for _, envVar := range f.envVars {
		values := splitListEnv(envVar)
		if len(values) > 0 {
			return values
		}
	}
	return nil
}

func registerMultiStringFlag(name string, envVars []string, usage string) *multiStringFlag {
	value := &multiStringFlag{envVars: envVars}
	flag.Var(value, name, usage)
	return value
}

type RouterOptions struct {
	CacheTTL          time.Duration
	BackendRPCTimeout time.Duration
	Backends          []Backend
}

type Backend struct {
	Provider   string
	Region     string
	Address    string
	RPCTimeout time.Duration
}

func formatBackendSummary(backends []Backend) string {
	parts := make([]string, 0, len(backends))
	for _, backend := range backends {
		parts = append(parts, backend.Region+"="+backend.Address)
	}
	return strings.Join(parts, ", ")
}

func splitListEnv(name string) []string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return nil
	}

	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == '\n'
	})
	items := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		items = append(items, field)
	}
	return items
}

func stringEnvDefault(name string, fallback string) string {
	return firstStringEnvDefault([]string{name}, fallback)
}

func firstStringEnvDefault(names []string, fallback string) string {
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return fallback
}

func durationEnvDefault(name string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: invalid duration %q for environment variable %s: %v\n", value, name, err)
		os.Exit(1)
	}
	return duration
}

func boolEnvDefault(name string, fallback bool) bool {
	return firstBoolEnvDefault([]string{name}, fallback)
}

func firstBoolEnvDefault(names []string, fallback bool) bool {
	for _, name := range names {
		value, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}

		parsed, err := strconv.ParseBool(value)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: invalid boolean %q for environment variable %s: %v\n", value, name, err)
			os.Exit(1)
		}
		return parsed
	}

	return fallback
}
