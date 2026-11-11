// Package skills embeds the imported CTF skills from their source directory.
package skills

import "embed"

// Resources keeps the source skills available in standalone HotMouse binaries.
//
//go:embed README.md */SKILL.md
var Resources embed.FS
