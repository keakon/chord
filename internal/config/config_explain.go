package config

import (
	"fmt"
	"slices"
)

// ExplainModelField describes the existing resolved snapshot without reading
// files, consulting the catalog, normalizing limits, or mutating the snapshot.
func ExplainModelField(rc *ResolvedConfig, provider, model string, field ExplainField) (ModelFieldExplanation, error) {
	d, err := explainDescriptor(field)
	if err != nil {
		return ModelFieldExplanation{}, err
	}
	if rc == nil || rc.Config == nil {
		return ModelFieldExplanation{}, fmt.Errorf("no resolved configuration")
	}
	_, mc, err := LookupConfiguredModel(rc.Config.Providers, provider, model)
	if err != nil {
		return ModelFieldExplanation{}, err
	}
	path := explainPath(provider, model, d)
	scalar, blockPresent := d.get(mc)
	r := ModelFieldExplanation{Model: CanonicalModelRef(provider, model, ""), Field: field, Value: scalar.value(), ValueState: ExplainValueUnknown, SourceStatus: ExplainSourceUntracked,
		Declarations: []ExplainDeclaration{}, Diagnostics: []Diagnostic{}, Notes: []ExplainNote{}}
	for _, diagnostic := range rc.Diagnostics {
		if relatedExplainDiagnostic(diagnostic, path) {
			r.Diagnostics = append(r.Diagnostics, diagnostic)
		}
	}
	evidence := rc.ExplainEvidence
	var raws []explainRawDeclaration
	var catalog *explainCatalogAssignment
	if evidence != nil {
		r.SourceStatus = ExplainSourceComplete
		for _, layer := range evidence.layers {
			if !layer.complete {
				r.SourceStatus = ExplainSourcePartial
				if layer.limitation != "" {
					r.note(ExplainNoteIncomplete, layer.file+": "+layer.limitation)
				}
			}
			for _, raw := range layer.ancestors {
				if pathAncestor(raw.source.PathSegments, path) {
					raws = append(raws, raw)
				}
			}
			if m := evidence.Providers[provider][model]; m != nil {
				for _, raw := range m.Fields[field] {
					if raw.source.File == layer.file && ExplainSourceKind(layer.layer) == raw.source.Kind {
						raws = append(raws, raw)
					}
				}
			}
		}
		if m := evidence.Providers[provider][model]; m != nil {
			if assignment, ok := m.Catalog[field]; ok {
				copy := assignment
				catalog = &copy
			}
		}
	}

	if evidence != nil && rc.Index == nil && catalog == nil {
		r.SourceStatus = ExplainSourcePartial
	}

	var selected Origin
	selectedOK := false
	var clearing BlockState
	if rc.Index != nil {
		if blocks, ok := rc.Index.ModelBlock(provider, model, d.block); ok {
			clearing = BlockClearing(blocks)
		}
		if d.block == "limit" {
			if mo, ok := rc.Index.Model(provider, model); ok {
				var origins []Origin
				switch field {
				case ExplainLimitContext:
					origins = mo.Limit.Context
				case ExplainLimitInput:
					origins = mo.Limit.Input
				case ExplainLimitOutput:
					origins = mo.Limit.Output
				}
				if len(origins) > 0 {
					selected = origins[len(origins)-1]
					selectedOK = true
				}
			}
		} else if origin, ok := rc.Index.ModelField(provider, model, string(field)); ok {
			selected = origin.Origin
			selectedOK = true
		}
	}
	barrier, activeNull := explainAncestorNull(raws, path)
	if selectedOK && barrier != nil && originLayerRank(selected.Layer) <= originLayerRank(OriginLayer(barrier.source.Kind)) {
		selectedOK = false
	}
	derived := field == ExplainLimitContext && mc.Limit.ContextDerived
	selectedRaw := -1
	for i, raw := range raws {
		if selectedOK && raw.source.File == selected.File && raw.source.Kind == ExplainSourceKind(selected.Layer) && slices.Equal(raw.source.PathSegments, path) && !raw.ignored && raw.scalar != nil {
			selectedRaw = i
		}
	}
	// The effective index chooses the layer, original evidence supplies location
	// and accepted scalar. A missing original declaration is never reconstructed
	// from sanitized line numbers.
	if selectedRaw >= 0 {
		raw := raws[selectedRaw]
		if *raw.scalar == scalar && blockPresent && !derived {
			r.ValueState = ExplainValueSet
			source := raw.source
			r.Source = &source
		} else if !derived {
			r.SourceStatus = ExplainSourcePartial
			r.note(ExplainNoteDifferent, "The resolved value differs from the selected declaration; its final source could not be proven.")
		}
	} else if selectedOK && selected.Layer != OriginLayerCatalog && evidence != nil {
		r.SourceStatus = ExplainSourcePartial
	}

	if catalog != nil && (d.block == "limit" || catalog.scalar.StringValue != "") {
		if catalog.scalar == scalar && blockPresent {
			source := catalog.source
			r.Source = &source
			r.ValueState = ExplainValueSet
		} else {
			r.SourceStatus = ExplainSourcePartial
			r.note(ExplainNoteDifferent, "The resolved value differs from the recorded catalog assignment.")
		}
	}

	if d.block != "limit" && !blockPresent && clearing.Cleared && activeNull != nil &&
		len(activeNull.source.PathSegments) == 5 && activeNull.source.File == clearing.Origin.File &&
		activeNull.source.Kind == ExplainSourceKind(clearing.Origin.Layer) {
		r.ValueState = ExplainValueCleared
		r.Value = nil
		source := activeNull.source
		r.Source = &source
	}

	leafNull := false
	for _, raw := range raws {
		if !raw.ignored && raw.kind == ExplainDeclarationNull && len(raw.source.PathSegments) == 6 &&
			(barrier == nil || originLayerRank(OriginLayer(raw.source.Kind)) > originLayerRank(OriginLayer(barrier.source.Kind))) &&
			(!selectedOK || originLayerRank(OriginLayer(raw.source.Kind)) >= originLayerRank(selected.Layer)) {
			leafNull = true
		}
	}
	if leafNull {
		r.SourceStatus = ExplainSourcePartial
		r.note(ExplainNoteLeafNull, "A leaf null was declared; the resolved value is shown, but its contribution is not tracked.")
		r.Source = nil
		r.ValueState = ExplainValueUnknown
		r.Value = scalar.value()
	}
	if activeNull != nil && r.ValueState == ExplainValueUnknown && !derived {
		r.SourceStatus = ExplainSourcePartial
		r.note(ExplainNoteAncestorNull, "An ancestor is null; lower-layer declarations cannot establish this value's source.")
	}
	if !derived && r.ValueState == ExplainValueUnknown && r.SourceStatus == ExplainSourceComplete {
		if scalar.IntValue == 0 && scalar.StringValue == "" {
			r.ValueState = ExplainValueUnset
			r.Value = nil
		} else {
			r.SourceStatus = ExplainSourcePartial
			r.note(ExplainNoteDifferent, "The resolved value has no matching recorded source.")
		}
	}

	if derived {
		r.ValueState = ExplainValueSet
		r.Value = scalar.value()
		r.Source = &ExplainSource{Kind: ExplainSourceDerived, PathSegments: path, Rule: "input_plus_output", Inputs: []ExplainInput{}}
		for _, dependency := range []ExplainField{ExplainLimitInput, ExplainLimitOutput} {
			input, _ := ExplainModelField(rc, provider, model, dependency)
			desc, _ := explainDescriptor(dependency)
			value, _ := desc.get(mc)
			r.Source.Inputs = append(r.Source.Inputs, ExplainInput{Field: dependency, Value: value.value(), SourceStatus: input.SourceStatus, Source: input.Source})
			if input.SourceStatus != ExplainSourceComplete && r.SourceStatus == ExplainSourceComplete {
				r.SourceStatus = ExplainSourcePartial
			}
		}
	}

	if catalog != nil {
		effect := ExplainEffectObserved
		if r.Source != nil && r.Source.Kind == ExplainSourceCatalog {
			effect = ExplainEffectSelected
		}
		r.Declarations = append(r.Declarations, ExplainDeclaration{Source: catalog.source, Kind: ExplainDeclarationScalar, Effect: effect, Value: catalog.scalar.value()})
	}
	var ignoredPaths [][]string
	lastFile := ""
	for i, raw := range raws {
		if raw.source.File != lastFile {
			ignoredPaths = nil
			lastFile = raw.source.File
		}
		hidden := false
		for _, p := range ignoredPaths {
			if pathAncestor(p, raw.source.PathSegments) {
				hidden = true
			}
		}
		if hidden {
			continue
		}
		// Ancestor mappings are only shape evidence; displaying them would bury
		// useful leaf declarations. Nulls and discarded ancestors remain visible.
		if len(raw.source.PathSegments) < 6 && !raw.ignored && raw.kind != ExplainDeclarationNull {
			showBlock := len(raw.source.PathSegments) == 5
			for _, leaf := range raws {
				if leaf.source.File == raw.source.File && leaf.source.Kind == raw.source.Kind && len(leaf.source.PathSegments) == 6 {
					showBlock = false
				}
			}
			if !showBlock {
				continue
			}
		}
		decl := ExplainDeclaration{Source: raw.source, Kind: raw.kind, Effect: ExplainEffectObserved}
		if raw.ignored {
			decl.Effect = ExplainEffectIgnored
			decl.Diagnostics = slices.Clone(raw.diagnostics)
			ignoredPaths = append(ignoredPaths, raw.source.PathSegments)
		} else if raw.kind == ExplainDeclarationNull && len(raw.source.PathSegments) < 6 {
			decl.Effect = ExplainEffectClearing
		} else if raw.scalar != nil {
			decl.Value = raw.scalar.value()
			overriddenByNull := barrier != nil && originLayerRank(OriginLayer(raw.source.Kind)) <= originLayerRank(OriginLayer(barrier.source.Kind))
			if i == selectedRaw && r.Source != nil && !derived && !leafNull && r.Source.Kind == raw.source.Kind {
				decl.Effect = ExplainEffectSelected
			} else if overriddenByNull || i != selectedRaw && (selectedRaw >= 0 || clearing.Cleared || clearing.LowerCleared) {
				decl.Effect = ExplainEffectOverridden
			}
		}
		r.Declarations = append(r.Declarations, decl)
	}
	// Related diagnostics use original positions only when a unique declaration
	// proves them; loader line numbers may refer to reserialized merge bytes.
	for i := range r.Diagnostics {
		diagnostic := &r.Diagnostics[i]
		var match *ExplainSource
		matches := 0
		for _, raw := range raws {
			if diagnostic.File == raw.source.File && slices.Equal(diagnostic.PathSegments, raw.source.PathSegments) {
				source := raw.source
				match = &source
				matches++
			}
		}
		if matches == 1 {
			diagnostic.Line = match.Line
			diagnostic.Col = match.Col
		}
	}
	if d.block != "limit" && blockPresent && catalog == nil && r.ValueState == ExplainValueUnset && (mc.Catalog == nil || !mc.Catalog.Disabled) {
		r.note(ExplainNoteCatalogBlock, "Catalog defaults fill this capability only when the entire block is absent; missing leaves in an explicit block are not filled.")
	}
	if field == ExplainLimitInput && mc.Limit.Input <= 0 {
		r.note(ExplainNoteInput, "No independent input limit is configured. Runtime input capacity depends on the context window and planned output budget.")
	}
	if field == ExplainLimitOutput {
		r.note(ExplainNoteOutput, "This is the static model output limit, not the request max_tokens value.")
	}
	if r.SourceStatus != ExplainSourceComplete {
		r.note(ExplainNoteIncomplete, "Some source evidence is unavailable; the value comes from the resolved disk configuration.")
	}
	r.chooseEditTarget(evidence, path, raws, activeNull)
	return r, nil
}

func (r *ModelFieldExplanation) note(code ExplainNoteCode, message string) {
	r.Notes = append(r.Notes, ExplainNote{Code: code, Message: message})
}

func (r *ModelFieldExplanation) chooseEditTarget(e *ConfigExplainEvidence, path []string, raws []explainRawDeclaration, activeNull *explainRawDeclaration) {
	if e == nil {
		return
	}
	file := e.globalPath
	if e.projectExists {
		file = e.projectPath
	}
	target := &ExplainEditTarget{File: file, PathSegments: slices.Clone(path), Action: ExplainEditAddOverride}
	source := r.Source
	if activeNull != nil {
		source = &activeNull.source
	}
	if source != nil && (source.Kind == ExplainSourceGlobal || source.Kind == ExplainSourceProject) {
		target.File = source.File
		for _, raw := range raws {
			if raw.source.File != source.File || !slices.Equal(raw.source.PathSegments, source.PathSegments) || raw.ignored {
				continue
			}
			if raw.editable && raw.source.Line > 0 {
				target.Action = ExplainEditReplace
				target.PathSegments = slices.Clone(source.PathSegments)
				target.Line = source.Line
				target.Col = source.Col
				if raw.kind == ExplainDeclarationNull {
					r.note(ExplainNoteClear, "Replace the null ancestor with a mapping containing the requested field path.")
				}
			} else {
				r.note(ExplainNoteInherited, "This declaration comes through a shared YAML alias or merge. Add an explicit override under the target model.")
			}
			break
		}
	}
	if target.File != "" {
		r.EditTarget = target
	}
}
