package composer

import (
	"fmt"
	"regexp"
	"strconv"
)

var imagePlaceholderRE = regexp.MustCompile(`\[image(\d+)\]`)

const InlineImagePlaceholderDisplay = "[image]"

func ImagePlaceholder(index int) string {
	if index <= 0 {
		index = 1
	}
	return fmt.Sprintf("[image%d]", index)
}

func InlineImagePlaceholderIndex(raw string) (int, bool) {
	m := imagePlaceholderRE.FindStringSubmatch(raw)
	if len(m) != 2 || m[0] != raw {
		return 0, false
	}
	idx, err := strconv.Atoi(m[1])
	if err != nil || idx < 1 {
		return 0, false
	}
	return idx, true
}
