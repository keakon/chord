package tui

import "strings"

type nextRequestModelRefProvider interface {
	NextRequestModelRef() string
}

func nextRequestModelRefForAgent(a any) string {
	if p, ok := a.(nextRequestModelRefProvider); ok {
		if ref := strings.TrimSpace(p.NextRequestModelRef()); ref != "" {
			return ref
		}
	}
	return ""
}
