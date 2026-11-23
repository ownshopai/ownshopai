//go:build bundle

package main

import _ "embed"

//go:embed bundled/windows-arm64/wacli.exe
var bundledWacli []byte

//go:embed bundled/windows-arm64/llama-server.exe
var bundledLlama []byte

func init() { bundled = map[string][]byte{"wacli": bundledWacli, "llama-server": bundledLlama} }
