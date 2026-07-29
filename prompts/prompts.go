package prompts

import _ "embed"

// ModelInstructions is the fixed behavior contract prepended by api-mock.
//
//go:embed model_instructions.md
var ModelInstructions string
