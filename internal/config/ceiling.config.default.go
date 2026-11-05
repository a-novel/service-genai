package config

import (
	_ "embed"

	"github.com/goccy/go-yaml"

	"github.com/a-novel-kit/golib/config"
)

//go:embed ceiling.config.yaml
var defaultCeilingFile []byte

// CeilingDefault is the ceiling built into the image, so every change to it is reviewed. It is
// decoded strictly, as [CapsDefault] is: a misspelled field fails startup rather than lifting a limit.
var CeilingDefault = config.MustUnmarshal[Cap](func(data []byte, value any) error {
	return yaml.UnmarshalWithOptions(data, value, yaml.DisallowUnknownField())
}, defaultCeilingFile)
