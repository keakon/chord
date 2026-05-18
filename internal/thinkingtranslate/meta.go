package thinkingtranslate

import "time"

type DecisionMeta struct {
	TargetLang   string
	Reason       string // e.g. "latin_ratio", "lang_mismatch", "disabled"
	DetectedLang string
	Confidence   float64
	LatinRatio   float64
	Chunks       int
	Duration     time.Duration
}
