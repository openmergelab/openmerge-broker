package discord

import (
	"regexp"
	"strings"
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9-]`)

// SanitiseChannelName makes a string safe for use as a Discord channel name.
// Discord requires: lowercase, no spaces, max 100 chars, alphanumeric + hyphens.
func SanitiseChannelName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = nonAlphanumeric.ReplaceAllString(s, "")
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}
