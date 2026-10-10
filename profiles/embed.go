package profiles

import "embed"

// Files contains the reviewed pricing data shipped with the application.
// Updates are distributed by rebuilding the application/container image.
//
//go:embed providers/*.yaml grids/*.yaml dk/*.yaml
var Files embed.FS
