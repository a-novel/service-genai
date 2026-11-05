package config

import (
	_ "embed"

	"github.com/goccy/go-yaml"

	"github.com/a-novel-kit/golib/config"
)

//go:embed purposes.config.yaml
var defaultPurposesFile []byte

// PurposesDefault lists the purposes built into the image, so every change to them is reviewed. A file
// that is not a list of names fails startup.
var PurposesDefault = config.MustUnmarshal[[]string](yaml.Unmarshal, defaultPurposesFile)
