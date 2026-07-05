// Package config loads Setu's YAML configuration and builds a gateway
// from it. The schema is intentionally close to LiteLLM's proxy config
// (model_list, router_settings) so migration is a copy-paste.
package config

import (
	"fmt"
	"os"
	"strings"

	"time"

	"gopkg.in/yaml.v3"

	"github.com/arbazkhan971/setu/cache"
	"github.com/arbazkhan971/setu/gateway"
	"github.com/arbazkhan971/setu/policy"
	"github.com/arbazkhan971/setu/pricing"
	"github.com/arbazkhan971/setu/provider"
)

// ModelEntry maps a client-facing model name to a provider deployment.
type ModelEntry struct {
	ModelName string         `yaml:"model_name"`
	Provider  string         `yaml:"provider"`
	Params    map[string]any `yaml:"params"`
}

// RouterSettings configures routing behavior.
type RouterSettings struct {
	Fallbacks  []map[string][]string `yaml:"fallbacks"`
	MaxRetries int                   `yaml:"max_retries"`
}

// ServerSettings configures the HTTP server.
type ServerSettings struct {
	MasterKey string `yaml:"master_key"`
	Port      int    `yaml:"port"`
	Metrics   bool   `yaml:"metrics"`
}

// CacheSettings configures the response cache.
type CacheSettings struct {
	Enabled    bool `yaml:"enabled"`
	TTL        int  `yaml:"ttl"`         // seconds; <=0 means no expiry
	MaxEntries int  `yaml:"max_entries"` // <=0 means unbounded
}

// VirtualKey is a scoped API key clients present instead of the master key.
type VirtualKey struct {
	Key       string   `yaml:"key"`
	Name      string   `yaml:"name"`
	Models    []string `yaml:"models"`
	MaxBudget float64  `yaml:"max_budget"`
	RPM       int      `yaml:"rpm"`
	TPM       int      `yaml:"tpm"`
}

// Config is the top-level configuration document.
type Config struct {
	ModelList      []ModelEntry   `yaml:"model_list"`
	RouterSettings RouterSettings `yaml:"router_settings"`
	Server         ServerSettings `yaml:"server"`
	VirtualKeys    []VirtualKey   `yaml:"virtual_keys"`
	Cache          CacheSettings  `yaml:"cache"`
}

// Load reads and parses a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(c.ModelList) == 0 {
		return nil, fmt.Errorf("%s: model_list is empty", path)
	}
	return &c, nil
}

// resolveEnv expands "os.environ/VAR" references (LiteLLM-compatible) and
// returns plain values unchanged.
func resolveEnv(v string) string {
	if strings.HasPrefix(v, "os.environ/") {
		return os.Getenv(strings.TrimPrefix(v, "os.environ/"))
	}
	return v
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return resolveEnv(v)
	}
	return ""
}

// weightOf reads an optional load-balancing weight from a params block,
// defaulting to 1. YAML decodes integers as int and floats as float64.
func weightOf(m map[string]any) int {
	switch v := m["weight"].(type) {
	case int:
		if v > 0 {
			return v
		}
	case int64:
		if v > 0 {
			return int(v)
		}
	case float64:
		if v > 0 {
			return int(v)
		}
	}
	return 1
}

// BuildGateway constructs providers and assembles the gateway.
func (c *Config) BuildGateway() (*gateway.Gateway, error) {
	var deps []*gateway.Deployment
	for _, m := range c.ModelList {
		if m.ModelName == "" || m.Provider == "" {
			return nil, fmt.Errorf("model entry missing model_name or provider")
		}
		opts := provider.Options{
			APIKey:  str(m.Params, "api_key"),
			BaseURL: str(m.Params, "base_url"),
			Model:   str(m.Params, "model"),
			Params:  m.Params,
		}
		p, err := provider.New(m.Provider, opts)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", m.ModelName, err)
		}
		deps = append(deps, &gateway.Deployment{ModelName: m.ModelName, Provider: p, Weight: weightOf(m.Params)})
	}

	fallbacks := map[string][]string{}
	for _, entry := range c.RouterSettings.Fallbacks {
		for k, v := range entry {
			fallbacks[k] = v
		}
	}

	maxRetries := 2
	if c.RouterSettings.MaxRetries > 0 {
		maxRetries = c.RouterSettings.MaxRetries
	}

	return gateway.New(deps,
		gateway.WithFallbacks(fallbacks),
		gateway.WithMaxRetries(maxRetries),
	), nil
}

// MasterKey returns the resolved server master key.
func (c *Config) MasterKey() string { return resolveEnv(c.Server.MasterKey) }

// BuildEnforcer builds the virtual-key policy enforcer, or nil if no virtual
// keys are configured. Key secrets support os.environ/VAR resolution.
func (c *Config) BuildEnforcer() *policy.Enforcer {
	if len(c.VirtualKeys) == 0 {
		return nil
	}
	keys := make([]*policy.Key, 0, len(c.VirtualKeys))
	for i, vk := range c.VirtualKeys {
		name := vk.Name
		if name == "" {
			name = fmt.Sprintf("key-%d", i+1)
		}
		keys = append(keys, &policy.Key{
			Secret:    resolveEnv(vk.Key),
			Name:      name,
			Models:    vk.Models,
			MaxBudget: vk.MaxBudget,
			RPM:       vk.RPM,
			TPM:       vk.TPM,
		})
	}
	return policy.New(keys, pricing.Default())
}

// BuildCache builds the response cache, or nil if caching is disabled.
func (c *Config) BuildCache() *cache.Cache {
	if !c.Cache.Enabled {
		return nil
	}
	ttl := time.Duration(c.Cache.TTL) * time.Second
	return cache.New(c.Cache.MaxEntries, ttl)
}
