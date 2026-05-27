package internal

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type AgentConfig struct {
	ServerURL string `yaml:"server_url"`
	APIKey    string `yaml:"api_key"`
}

func LoadAgentConfig(path string) (AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("read config: %w", err)
	}
	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return AgentConfig{}, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ServerURL == "" || cfg.APIKey == "" {
		return AgentConfig{}, fmt.Errorf("server_url and api_key required")
	}
	return cfg, nil
}
