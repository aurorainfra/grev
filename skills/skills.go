// Package skills embeds the agent skill that ships with the grev tools, so
// `grev-settings skill install` works without the source tree or a package's
// share directory.
package skills

import "embed"

// FS holds the grev skill: grev/SKILL.md and any files beside it.
//
//go:embed grev
var FS embed.FS
