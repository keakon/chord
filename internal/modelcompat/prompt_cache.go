package modelcompat

import (
	"strconv"
	"strings"
)

// SupportsResponsesCacheBreakpoints reports the model generations whose
// Responses contract accepts explicit input-text cache breakpoints. Unknown
// aliases cannot establish support; callers must also check the wire family.
func SupportsResponsesCacheBreakpoints(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if slash := strings.LastIndexByte(modelID, '/'); slash >= 0 {
		modelID = modelID[slash+1:]
	}
	version, ok := strings.CutPrefix(modelID, "gpt-")
	if !ok {
		return false
	}
	version, _, _ = strings.Cut(version, "-")
	majorText, minorText, hasMinor := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return false
	}
	minor := 0
	if hasMinor {
		minor, err = strconv.Atoi(minorText)
		if err != nil || minor < 0 {
			return false
		}
	}
	return major >= 6 || major == 5 && minor >= 6
}
