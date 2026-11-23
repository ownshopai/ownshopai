//go:build bundle

package main

import _ "embed"

//go:embed bundled/darwin-arm64/wacli
var bundledWacli []byte

//go:embed bundled/darwin-arm64/llama-server
var bundledLlama []byte

func init() { bundled = map[string][]byte{"wacli": bundledWacli, "llama-server": bundledLlama} }
