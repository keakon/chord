package tui

import "github.com/keakon/chord/internal/tui/composer"

func validComposerTokens(in *composer.Input) bool {
	text := []rune(in.DisplayValue())
	end := 0
	for _, token := range in.InlinePastes() {
		if token.Start < end || token.End < token.Start || token.End > len(text) || string(text[token.Start:token.End]) != token.DisplayText {
			return false
		}
		end = token.End
	}
	return true
}
