// Package api holds the gateway's copy of the public OpenAPI contract and the
// models generated from it. Both are produced by `make gen` from
// contracts/openapi/dt.v1.yaml; edit that file, never these.
package api

import _ "embed"

// Spec is the OpenAPI document the gateway validates requests against.
//
//go:embed openapi.yaml
var Spec []byte
