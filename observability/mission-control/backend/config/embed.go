// Package mcconfig embeds the default thresholds file.
package mcconfig

import _ "embed"

// DefaultThresholds is config/thresholds.yaml as built into the binary.
//
//go:embed thresholds.yaml
var DefaultThresholds []byte
