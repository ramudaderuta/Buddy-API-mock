package prompts

import _ "embed"

// ModelInstructions is the fixed behavior contract prepended by api-mock.
//
//go:embed model_instructions.md
var ModelInstructions string

// SkillDescription is the bundled WorkBuddy-compatible Skill tool description
// injected on upstream chat completion requests.
//
//go:embed skill_description.txt
var SkillDescription string
