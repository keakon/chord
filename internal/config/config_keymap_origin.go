package config

import "github.com/keakon/chord/internal/keybinding"

func (rc *ResolvedConfig) addKeymapLayer(layer keymapLayer) {
	if rc.Index == nil {
		rc.Index = &SourceIndex{}
	}
	if rc.Index.KeyMap == nil {
		rc.Index.KeyMap = make(map[keybinding.ActionID][]Origin)
	}
	for id, origin := range layer.Origins {
		rc.Index.KeyMap[id] = append(rc.Index.KeyMap[id], origin)
	}
}

func keymapEffectiveOrigin(index *SourceIndex, id keybinding.ActionID) (Origin, bool) {
	if index != nil {
		origins := index.KeyMap[id]
		if len(origins) > 0 {
			return origins[len(origins)-1], true
		}
	}
	return Origin{}, false
}
