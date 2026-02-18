package validation

import (
	"fmt"
	"regexp"
	"strings"
)

var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9._\- ]{1,50}$`)

// ValidateName checks that a name (agent, room, or team) contains only safe characters.
func ValidateName(name string) error {
	if name == "" {
		return nil // empty means default
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: only [a-zA-Z0-9._- ] allowed, max 50 chars", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid name %q: '..' not allowed", name)
	}
	return nil
}
