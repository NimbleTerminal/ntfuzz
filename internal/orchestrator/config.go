package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"nimbleterminal.dev/ntfuzz/configs"
)

// Config defines the YAML configuration for the ntfuzz orchestrator.
type Config struct {
	CorpusDir       string          `yaml:"corpus_dir"`
	WorkDir         string          `yaml:"work_dir"`
	DefaultFuzzTime string          `yaml:"default_fuzztime"`
	Projects        []ProjectConfig `yaml:"projects"`
}

// ProjectConfig defines a single repository/module to fuzz.
type ProjectConfig struct {
	Name      string `yaml:"name"`
	Repo      string `yaml:"repo"`
	Branch    string `yaml:"branch,omitempty"`
	FuzzTime  string `yaml:"fuzztime,omitempty"`
	CorpusDir string `yaml:"corpus_dir,omitempty"`
}

// EffectiveFuzzTime returns the configured fuzz duration for a project,
// falling back to the top-level default or 10s.
func (p ProjectConfig) EffectiveFuzzTime(defaultRaw string) time.Duration {
	for _, candidate := range []string{p.FuzzTime, defaultRaw} {
		if candidate == "" {
			continue
		}
		if d, err := time.ParseDuration(candidate); err == nil && d > 0 {
			return d
		}
	}
	return 10 * time.Second
}

// EffectiveCorpusDir returns the persistent corpus directory for this project.
func (p ProjectConfig) EffectiveCorpusDir(rootCorpusDir string) string {
	if p.CorpusDir != "" {
		return p.CorpusDir
	}
	if rootCorpusDir == "" {
		rootCorpusDir = filepath.Join(".ntfuzz", "corpus")
	}
	return filepath.Join(rootCorpusDir, p.Name)
}

// LoadConfig loads configuration from path, or from the embedded default config if path is empty
// or does not exist on disk.
func LoadConfig(path string) (Config, error) {
	var data []byte
	var err error

	if path != "" {
		data, err = os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) && path == "configs/projects.yaml" {
				data = configs.DefaultProjectsYAML
			} else {
				return Config{}, fmt.Errorf("read config %q: %w", path, err)
			}
		}
	} else {
		data = configs.DefaultProjectsYAML
	}

	return ParseConfig(data)
}

// ParseConfig unmarshals YAML bytes into a validated Config.
func ParseConfig(data []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config yaml: %w", err)
	}
	if cfg.CorpusDir == "" {
		cfg.CorpusDir = filepath.Join(".ntfuzz", "corpus")
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = filepath.Join(".ntfuzz", "repos")
	}
	if strings.TrimSpace(cfg.DefaultFuzzTime) == "" {
		cfg.DefaultFuzzTime = "10s"
	} else if d, err := time.ParseDuration(cfg.DefaultFuzzTime); err != nil || d <= 0 {
		return Config{}, fmt.Errorf("invalid default_fuzztime %q: must be a positive Go duration (e.g. \"10s\")", cfg.DefaultFuzzTime)
	}

	for i, p := range cfg.Projects {
		if strings.TrimSpace(p.Name) == "" {
			return Config{}, fmt.Errorf("project[%d] is missing name", i)
		}
		if strings.TrimSpace(p.Repo) == "" {
			return Config{}, fmt.Errorf("project %q is missing repo", p.Name)
		}
		if strings.TrimSpace(p.FuzzTime) != "" {
			if d, err := time.ParseDuration(p.FuzzTime); err != nil || d <= 0 {
				return Config{}, fmt.Errorf("project %q has invalid fuzztime %q: must be a positive Go duration (e.g. \"10s\")", p.Name, p.FuzzTime)
			}
		}
	}
	return cfg, nil
}
