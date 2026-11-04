package configs

import _ "embed"

// DefaultProjectsYAML contains the embedded default projects.yaml configuration.
//
//go:embed projects.yaml
var DefaultProjectsYAML []byte
