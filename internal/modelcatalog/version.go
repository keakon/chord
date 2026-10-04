package modelcatalog

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ValidateVersion enforces the upstream release format YYYY-MM-DD.N, where N
// is a positive canonical decimal revision within that calendar day.
func ValidateVersion(version string) error {
	date, revision, ok := strings.Cut(version, ".")
	if !ok {
		return fmt.Errorf("catalog version %q must use YYYY-MM-DD.N", version)
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return fmt.Errorf("catalog version %q must use a calendar day in YYYY-MM-DD.N", version)
	}
	n, err := strconv.Atoi(revision)
	if err != nil || n < 1 || strconv.Itoa(n) != revision {
		return fmt.Errorf("catalog version %q must end in a positive decimal revision without leading zeros", version)
	}
	return nil
}
