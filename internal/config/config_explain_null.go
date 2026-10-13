package config

// explainAncestorNull identifies the highest-priority clear boundary and whether
// a higher layer rebuilt that ancestor. The sparse leaf index does not discard
// declarations below model/provider or limit nulls, so it cannot alone establish
// an effective source, even when a lower scalar equals the resolved zero value.
func explainAncestorNull(raws []explainRawDeclaration, path []string) (barrier, active *explainRawDeclaration) {
	for i := range raws {
		raw := &raws[i]
		if raw.ignored || raw.kind != ExplainDeclarationNull || len(raw.source.PathSegments) >= len(path) {
			continue
		}
		if barrier == nil || originLayerRank(OriginLayer(raw.source.Kind)) > originLayerRank(OriginLayer(barrier.source.Kind)) {
			barrier = raw
		}
	}
	if barrier == nil {
		return nil, nil
	}
	for _, raw := range raws {
		if !raw.ignored && originLayerRank(OriginLayer(raw.source.Kind)) > originLayerRank(OriginLayer(barrier.source.Kind)) &&
			pathAncestor(barrier.source.PathSegments, raw.source.PathSegments) {
			return barrier, nil
		}
	}
	return barrier, barrier
}
