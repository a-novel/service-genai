package config

import (
	_ "embed"

	"github.com/goccy/go-yaml"

	"github.com/a-novel-kit/golib/config"
)

//go:embed caps.config.yaml
var defaultCapsFile []byte

// CapsDefault is the usage policy built into the image, so every change to it is reviewed. It is
// decoded strictly: a misspelled field fails startup rather than lifting a limit.
var CapsDefault = config.MustUnmarshal[Caps](func(data []byte, value any) error {
	return yaml.UnmarshalWithOptions(data, value, yaml.DisallowUnknownField())
}, defaultCapsFile)
