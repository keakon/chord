package agent

import (
	"strings"

	"github.com/keakon/chord/internal/config"
)

func splitProviderModelRef(ref string) (provider, model, variant string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", ""
	}
	i := strings.Index(ref, "/")
	if i < 0 {
		m, v := config.ParseModelRef(ref)
		return "", m, v
	}
	provider = ref[:i]
	m, v := config.ParseModelRef(ref[i+1:])
	return provider, m, v
}

func formatModelRefForNotification(displayRef, selectedRef, activeVariant string) string {
	displayRef = strings.TrimSpace(displayRef)
	selectedRef = strings.TrimSpace(selectedRef)
	activeVariant = strings.TrimSpace(activeVariant)
	if displayRef == "" {
		return ""
	}
	if _, _, v := splitProviderModelRef(displayRef); v != "" || activeVariant == "" {
		return displayRef
	}
	dp, dm, _ := splitProviderModelRef(displayRef)
	sp, sm, _ := splitProviderModelRef(selectedRef)
	if dp == "" || dm == "" || sp == "" || sm == "" {
		return displayRef
	}
	if dp != sp || dm != sm {
		return displayRef
	}
	return displayRef + "@" + activeVariant
}
