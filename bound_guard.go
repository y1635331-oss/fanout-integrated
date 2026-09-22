package main

import "strings"

// Keep stale bindings as blackholes until rebind succeeds, rather than allowing
// an absent outbound to fall through to a panel's direct route.
func preserveBoundOutbounds(cfg map[string]any, outs []any) []any {
	present := map[string]bool{}
	for _, o := range outs {
		if m, ok := o.(map[string]any); ok {
			tag, _ := m["tag"].(string)
			present[tag] = true
		}
	}
	routing, _ := cfg["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	for _, rule := range rules {
		m, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := m["outboundTag"].(string)
		if strings.HasPrefix(tag, xuiTagPrefix) && !present[tag] {
			outs = append(outs, map[string]any{"tag": tag, "protocol": "blackhole"})
			present[tag] = true
		}
	}
	return outs
}
