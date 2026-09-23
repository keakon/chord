package thinkingtranslate

type DetectFunc func(text string) (lang string, confidence float64)
