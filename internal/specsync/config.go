package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	Spec             string              `json:"spec"`
	GoPackageDir     string              `json:"goPackageDir"`
	Renames          map[string]string   `json:"renames"`
	Operations       map[string]string   `json:"operations"`
	IgnoreOperations map[string]string   `json:"ignoreOperations"`
	SkipTypes        map[string]string   `json:"skipTypes"`
	IgnoreSchemas    map[string]string   `json:"ignoreSchemas"`
	UntypedEnums     map[string]string   `json:"untypedEnums"`
	FormatPhrases    map[string][]string `json:"formatPhrases"`
	Accepted         []Accepted          `json:"accepted"`
}

type Accepted struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Spec   string `json:"spec,omitempty"`
	Go     string `json:"go,omitempty"`
	Reason string `json:"reason"`
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if cfg.Spec == "" {
		return Config{}, fmt.Errorf("config %q: spec must not be empty", path)
	}
	if cfg.GoPackageDir == "" {
		return Config{}, fmt.Errorf("config %q: goPackageDir must not be empty", path)
	}
	return cfg, nil
}
