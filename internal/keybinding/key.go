package keybinding

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

var modifierNames = [...]string{"ctrl", "alt", "shift", "meta", "hyper", "super"}

var namedKeys = func() map[string]struct{} {
	names := strings.Fields(`enter tab backspace esc space up down left right begin find insert delete select
pgup pgdown home end equal mul plus comma minus period div sep
capslock scrolllock numlock printscreen pause menu
mediaplay mediapause mediaplaypause mediareverse mediastop mediafastforward
mediarewind medianext mediaprev mediarecord lowervol raisevol mute
leftshift leftalt leftctrl leftsuper lefthyper leftmeta
rightshift rightalt rightctrl rightsuper righthyper rightmeta
isolevel3shift isolevel5shift`)
	result := make(map[string]struct{}, len(names)+63)
	for _, name := range names {
		result[name] = struct{}{}
	}
	for i := 1; i <= 63; i++ {
		result["f"+strconv.Itoa(i)] = struct{}{}
	}
	return result
}()

// NormalizeKey validates one terminal keystroke, preserving character case and
// Unicode spelling. Modifier order follows the terminal's Keystroke output.
func NormalizeKey(raw string) (string, error) {
	var modifiers [len(modifierNames)]bool
	base := raw
	for {
		consumed := false
		for i, name := range modifierNames {
			if strings.HasPrefix(base, name+"+") {
				if modifiers[i] {
					return "", fmt.Errorf("duplicate modifier %q", name)
				}
				modifiers[i] = true
				base = base[len(name)+1:]
				consumed = true
				break
			}
		}
		if !consumed {
			break
		}
	}
	if base == "" {
		return "", fmt.Errorf("missing key")
	}
	if base == " " {
		base = "space"
	}
	if _, named := namedKeys[base]; !named {
		if !utf8.ValidString(base) || uniseg.GraphemeClusterCount(base) != 1 {
			return "", fmt.Errorf("expected a named key or one printable grapheme")
		}
		printable := false
		for _, r := range base {
			if unicode.IsControl(r) || (!unicode.IsPrint(r) && r != '\u200d') {
				return "", fmt.Errorf("key contains a non-printable character")
			}
			printable = printable || unicode.IsPrint(r)
		}
		if !printable {
			return "", fmt.Errorf("key must contain a printable character")
		}
	}
	var result strings.Builder
	for i, present := range modifiers {
		if present {
			result.WriteString(modifierNames[i])
			result.WriteByte('+')
		}
	}
	result.WriteString(base)
	return result.String(), nil
}
