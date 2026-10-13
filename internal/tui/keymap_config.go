package tui

import "github.com/keakon/chord/internal/keybinding"

type keyMapSetter struct {
	id    keybinding.ActionID
	field func(*KeyMap) *[]string
}

var keyMapSetters = []keyMapSetter{
	{keybinding.ActionInsertEscape, func(km *KeyMap) *[]string { return &km.InsertEscape }},
	{keybinding.ActionInsertSubmit, func(km *KeyMap) *[]string { return &km.InsertSubmit }},
	{keybinding.ActionInsertSubmitAfterCompletion, func(km *KeyMap) *[]string { return &km.InsertSubmitAfterCompletion }},
	{keybinding.ActionInsertNewline, func(km *KeyMap) *[]string { return &km.InsertNewline }},
	{keybinding.ActionInsertHistorySearch, func(km *KeyMap) *[]string { return &km.InsertHistorySearch }},
	{keybinding.ActionInsertHistoryUp, func(km *KeyMap) *[]string { return &km.InsertHistoryUp }},
	{keybinding.ActionInsertHistoryDown, func(km *KeyMap) *[]string { return &km.InsertHistoryDown }},
	{keybinding.ActionInsertAttachClipboard, func(km *KeyMap) *[]string { return &km.InsertAttachClipboard }},
	{keybinding.ActionInsertAttachFile, func(km *KeyMap) *[]string { return &km.InsertAttachFile }},
	{keybinding.ActionInsertClearInput, func(km *KeyMap) *[]string { return &km.InsertClearInput }},
	{keybinding.ActionInsertUndo, func(km *KeyMap) *[]string { return &km.InsertUndo }},
	{keybinding.ActionInsertPageUp, func(km *KeyMap) *[]string { return &km.InsertPageUp }},
	{keybinding.ActionInsertPageDown, func(km *KeyMap) *[]string { return &km.InsertPageDown }},
	{keybinding.ActionEnterInsert, func(km *KeyMap) *[]string { return &km.EnterInsert }},
	{keybinding.ActionQuit, func(km *KeyMap) *[]string { return &km.Quit }},
	{keybinding.ActionHelpToggle, func(km *KeyMap) *[]string { return &km.HelpToggle }},
	{keybinding.ActionScrollDown, func(km *KeyMap) *[]string { return &km.ScrollDown }},
	{keybinding.ActionScrollUp, func(km *KeyMap) *[]string { return &km.ScrollUp }},
	{keybinding.ActionFullPageDown, func(km *KeyMap) *[]string { return &km.FullPageDown }},
	{keybinding.ActionFullPageUp, func(km *KeyMap) *[]string { return &km.FullPageUp }},
	{keybinding.ActionScrollToBottom, func(km *KeyMap) *[]string { return &km.ScrollToBottom }},
	{keybinding.ActionScrollToTopSeq, func(km *KeyMap) *[]string { return &km.ScrollToTopSeq }},
	{keybinding.ActionNextBlock, func(km *KeyMap) *[]string { return &km.NextBlock }},
	{keybinding.ActionPrevBlock, func(km *KeyMap) *[]string { return &km.PrevBlock }},
	{keybinding.ActionNextUserBlock, func(km *KeyMap) *[]string { return &km.NextUserBlock }},
	{keybinding.ActionPrevUserBlock, func(km *KeyMap) *[]string { return &km.PrevUserBlock }},
	{keybinding.ActionNextAssistantBlock, func(km *KeyMap) *[]string { return &km.NextAssistantBlock }},
	{keybinding.ActionPrevAssistantBlock, func(km *KeyMap) *[]string { return &km.PrevAssistantBlock }},
	{keybinding.ActionNextSameTypeBlock, func(km *KeyMap) *[]string { return &km.NextSameTypeBlock }},
	{keybinding.ActionPrevSameTypeBlock, func(km *KeyMap) *[]string { return &km.PrevSameTypeBlock }},
	{keybinding.ActionToggleCollapse, func(km *KeyMap) *[]string { return &km.ToggleCollapse }},
	{keybinding.ActionForkSession, func(km *KeyMap) *[]string { return &km.ForkSession }},
	{keybinding.ActionDirectory, func(km *KeyMap) *[]string { return &km.Directory }},
	{keybinding.ActionUsageStats, func(km *KeyMap) *[]string { return &km.UsageStats }},
	{keybinding.ActionErrorPanel, func(km *KeyMap) *[]string { return &km.ErrorPanel }},
	{keybinding.ActionBackgroundJobs, func(km *KeyMap) *[]string { return &km.BackgroundJobs }},
	{keybinding.ActionSearchStart, func(km *KeyMap) *[]string { return &km.SearchStart }},
	{keybinding.ActionSearchNext, func(km *KeyMap) *[]string { return &km.SearchNext }},
	{keybinding.ActionSearchPrev, func(km *KeyMap) *[]string { return &km.SearchPrev }},
	{keybinding.ActionSwitchAgent, func(km *KeyMap) *[]string { return &km.SwitchAgent }},
	{keybinding.ActionSwitchRole, func(km *KeyMap) *[]string { return &km.SwitchRole }},
	{keybinding.ActionStop, func(km *KeyMap) *[]string { return &km.Stop }},
	{keybinding.ActionSwitchModel, func(km *KeyMap) *[]string { return &km.SwitchModel }},
	{keybinding.ActionServiceTier, func(km *KeyMap) *[]string { return &km.ServiceTier }},
	{keybinding.ActionDiagnostics, func(km *KeyMap) *[]string { return &km.Diagnostics }},
	{keybinding.ActionYolo, func(km *KeyMap) *[]string { return &km.Yolo }},
	{keybinding.ActionMCP, func(km *KeyMap) *[]string { return &km.MCP }},
}

// KeyMapFromConfig resolves overrides against the shared action catalog. Empty
// lists disable actions; absent or invalid declarations retain their defaults.
// Returned bindings never share mutable slices with the input configuration.
func KeyMapFromConfig(m map[string][]string) KeyMap {
	var km KeyMap
	resolved := keybinding.Resolve(m)
	for _, setter := range keyMapSetters {
		*setter.field(&km) = resolved[setter.id]
	}
	return km
}
