package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

// TestDoneReportCSVCodeBlockWrapsLongLines verifies that CSV code blocks in
// Done tool reports wrap long lines instead of overflowing the card boundary.
func TestDoneReportCSVCodeBlockWrapsLongLines(t *testing.T) {
	ApplyTheme(DefaultTheme())

	// Simulate a CSV line with many columns that exceeds maxTextWidth (120)
	longCSVLine := `6,东阿阿胶(复方阿胶浆)的销售分析,resolved,"东阿阿胶(复方阿胶浆) → 集团名称=东阿阿胶, 厂家名称=东阿阿胶股份有限公司, 商品名=东阿阿胶, 品名=复方阿胶浆, 通用名=复方阿胶, 零售分类1=滋补保健类, 零售分类2=补气补血, 零售分类3=气血双补",`

	report := "## CSV 格式示例\n\n```csv\nidx,question,expected_status,entities,enums\n" + longCSVLine + "\n```"

	width := 80 // Typical terminal width
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     "done",
		DoneReport:   report,
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
	}

	lines := block.Render(width, "")
	joined := strings.Join(lines, "\n")

	// Verify the card renders without errors
	if len(lines) == 0 {
		t.Fatal("expected non-empty render output")
	}

	// Calculate actual card metrics to check width bounds
	metrics := newDoneToolCardMetrics(width)
	style := metrics.blockStyle
	maxLineWidth := style.GetMarginLeft() + style.GetPaddingLeft() + metrics.cardWidth + style.GetPaddingRight() + style.GetMarginRight()
	if railANSISeq("tool", false) != "" {
		maxLineWidth++ // rail adds one column
	}

	// Check that no line exceeds the card width (allowing for ANSI sequences)
	for i, line := range lines {
		displayWidth := tuiStringWidth(line)
		if displayWidth > maxLineWidth {
			t.Errorf("line %d exceeds card width: got %d, max %d\nline: %q", i, displayWidth, maxLineWidth, line)
		}
	}

	// Verify the CSV content is present (wrapped, not truncated)
	if !strings.Contains(joined, "东阿阿胶") {
		t.Error("CSV content should be present in rendered output")
	}
	if !strings.Contains(joined, "resolved") {
		t.Error("CSV status should be present in rendered output")
	}

	// Count how many lines the CSV block spans - if wrapping works, it should be multiple lines
	csvBlockLines := 0
	inCSVBlock := false
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "CSV") {
			inCSVBlock = true
		}
		if inCSVBlock && strings.TrimSpace(plain) != "" && !strings.Contains(plain, "TOOL CALL") {
			csvBlockLines++
		}
	}

	if csvBlockLines < 3 {
		t.Errorf("expected CSV block to span multiple lines due to wrapping, got %d lines", csvBlockLines)
	}
}

// TestDoneReportGenericCodeBlockWrapsLongLines tests wrapping for non-CSV code blocks
func TestDoneReportGenericCodeBlockWrapsLongLines(t *testing.T) {
	ApplyTheme(DefaultTheme())

	// A very long shell command line
	longCommand := "aws s3 sync s3://my-bucket/path/to/very/long/directory/structure/with/many/subdirectories/ /local/destination/path/that/is/also/very/long/and/exceeds/terminal/width --exclude '*.tmp' --include '*.json' --region us-west-2"

	report := "## Command executed\n\n```bash\n" + longCommand + "\n```"

	width := 80
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     "done",
		DoneReport:   report,
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
	}

	lines := block.Render(width, "")

	// Calculate max card width
	metrics := newDoneToolCardMetrics(width)
	style := metrics.blockStyle
	maxLineWidth := style.GetMarginLeft() + style.GetPaddingLeft() + metrics.cardWidth + style.GetPaddingRight() + style.GetMarginRight()
	if railANSISeq("tool", false) != "" {
		maxLineWidth++
	}

	// Verify no line exceeds card width
	for i, line := range lines {
		displayWidth := tuiStringWidth(line)
		if displayWidth > maxLineWidth {
			t.Errorf("line %d exceeds card width: got %d, max %d", i, displayWidth, maxLineWidth)
		}
	}

	// Verify the command content is present (wrapped, not truncated)
	joined := strings.Join(lines, "\n")
	plain := stripANSI(joined)
	if !strings.Contains(plain, "aws s3 sync") {
		t.Error("command content should be present")
	}
	if !strings.Contains(plain, "us-west-2") {
		t.Errorf("command end should be present (not truncated)\nGot:\n%s", plain)
	}
}
