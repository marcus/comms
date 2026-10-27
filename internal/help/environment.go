package help

import _ "embed"

// EnvironmentReference is shared by installed help and the README reference.
//
//go:embed environment.md
var EnvironmentReference string
