package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type lspDisplayArgs struct {
	Operation   string `json:"operation"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Character   int    `json:"character"`
	IncludeDecl *bool  `json:"include_declaration,omitempty"`
}

type lspDisplayLocation struct {
	Path      string
	Line      int
	Character int
}

func parseLspDisplayArgs(content string) (lspDisplayArgs, bool) {
	var args lspDisplayArgs
	if err := json.Unmarshal([]byte(content), &args); err != nil {
		return lspDisplayArgs{}, false
	}
	return args, true
}

func lspOperationLabel(operation string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "definition":
		return "go to definition"
	case "references":
		return "find references"
	case "implementation":
		return "find implementations"
	default:
		return strings.TrimSpace(operation)
	}
}

func lspLocationLabel(path string, line, character int) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if line <= 0 {
		return path
	}
	if character <= 0 {
		return fmt.Sprintf("%s:%d", path, line)
	}
	return fmt.Sprintf("%s:%d:%d", path, line, character)
}

func (b *Block) lspToolHeaderParts() (mainPart, grayPart string) {
	if b == nil {
		return "", ""
	}
	args, ok := parseLspDisplayArgs(b.Content)
	if !ok {
		return "", ""
	}
	mainPart = lspOperationLabel(args.Operation)
	grayPart = lspLocationLabel(b.displayToolPath(args.Path), args.Line, args.Character)
	if strings.EqualFold(strings.TrimSpace(args.Operation), "references") && args.IncludeDecl != nil && !*args.IncludeDecl {
		if grayPart != "" {
			grayPart += " "
		}
		grayPart += "(excluding declaration)"
	}
	return mainPart, grayPart
}

func parseLspDisplayLocation(line string) (lspDisplayLocation, bool) {
	line = strings.TrimSpace(line)
	characterSep := strings.LastIndexByte(line, ':')
	if characterSep <= 0 || characterSep == len(line)-1 {
		return lspDisplayLocation{}, false
	}
	lineSep := strings.LastIndexByte(line[:characterSep], ':')
	if lineSep <= 0 || lineSep == characterSep-1 {
		return lspDisplayLocation{}, false
	}
	lineNumber, err := strconv.Atoi(strings.TrimSpace(line[lineSep+1 : characterSep]))
	if err != nil || lineNumber <= 0 {
		return lspDisplayLocation{}, false
	}
	character, err := strconv.Atoi(strings.TrimSpace(line[characterSep+1:]))
	if err != nil || character <= 0 {
		return lspDisplayLocation{}, false
	}
	path := strings.TrimSpace(line[:lineSep])
	if path == "" {
		return lspDisplayLocation{}, false
	}
	return lspDisplayLocation{Path: path, Line: lineNumber, Character: character}, true
}

func lspNoLocationsSummary(result string) string {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case "no definition found.":
		return "No definition found"
	case "no references found.":
		return "No references found"
	case "no implementations found.":
		return "No implementations found"
	default:
		return ""
	}
}

func lspResultSummary(content, result string) string {
	if summary := lspNoLocationsSummary(result); summary != "" {
		return summary
	}
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return ""
	}
	locations := 0
	files := make(map[string]struct{})
	for line := range strings.SplitSeq(strings.TrimRight(trimmed, "\n"), "\n") {
		location, ok := parseLspDisplayLocation(line)
		if !ok {
			return ""
		}
		locations++
		files[location.Path] = struct{}{}
	}
	if locations == 0 {
		return ""
	}
	args, _ := parseLspDisplayArgs(content)
	noun := "locations"
	switch strings.ToLower(strings.TrimSpace(args.Operation)) {
	case "definition":
		noun = "definitions"
	case "references":
		noun = "references"
	case "implementation":
		noun = "implementations"
	}
	if locations == 1 {
		noun = strings.TrimSuffix(noun, "s")
	}
	summary := fmt.Sprintf("%d %s", locations, noun)
	if len(files) > 1 {
		summary += fmt.Sprintf(" · %d files", len(files))
	}
	return summary
}

func (b *Block) lspDisplayResultContent(result string) string {
	if b == nil || lspNoLocationsSummary(result) != "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(result, "\r\n", "\n"), "\n")
	for i, line := range lines {
		location, ok := parseLspDisplayLocation(line)
		if !ok {
			continue
		}
		lines[i] = lspLocationLabel(b.displayToolPath(location.Path), location.Line, location.Character)
	}
	return strings.Join(lines, "\n")
}
