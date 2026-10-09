package message

import "slices"

// MergeHostedCalls pairs continuation results with their original invocation.
// The returned slice owns its entries; raw payloads remain immutable.
func MergeHostedCalls(dst, src []HostedCall) []HostedCall {
	dst = slices.Clone(dst)
	byID := make(map[string]int, len(dst))
	for i, c := range dst {
		if c.ID != "" {
			byID[c.ID] = i
		}
	}
	for _, c := range src {
		if i, ok := byID[c.ID]; ok && c.ID != "" {
			if len(c.Result) > 0 || c.Error != "" {
				old := &dst[i]
				if c.Name == "" {
					c.Name = old.Name
				}
				if len(c.Input) == 0 {
					c.Input = old.Input
				}
				if c.Kind == "" || old.Kind == "server_tool_use" {
					c.Kind = old.Kind
				}
				*old = c
			}
		} else {
			dst = append(dst, c)
			if c.ID != "" {
				byID[c.ID] = len(dst) - 1
			}
		}
	}
	return dst
}
