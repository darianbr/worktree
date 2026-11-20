package wtree

import (
	"fmt"
	"regexp"
	"strings"
)

var ticketPattern = regexp.MustCompile(`(?i)[a-z][a-z0-9]*-[0-9]+`)
var nonLabel = regexp.MustCompile(`[^a-z0-9]+`)
var validLabel = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9-]*[a-z0-9])?$`)

// ResolveSlug chooses an explicit DNS label or derives one from a Git branch.
func ResolveSlug(explicit, branch string) (string, error) {
	var slug string
	if explicit != "" {
		slug = strings.ToLower(explicit)
	} else if ticket := ticketPattern.FindString(branch); ticket != "" {
		slug = strings.ToLower(ticket)
	} else {
		slug = strings.Trim(nonLabel.ReplaceAllString(strings.ToLower(branch), "-"), "-")
		if len(slug) > 63 {
			slug = strings.TrimRight(slug[:63], "-")
		}
	}
	if len(slug) == 0 || len(slug) > 63 || !validLabel.MatchString(slug) {
		return "", fmt.Errorf("invalid slug %q: use --slug with a DNS label", slug)
	}
	return slug, nil
}
