package thinkingtranslate

import "testing"

func TestNormalizeForCompare(t *testing.T) {
	in := "\r\n  hello\r\nworld  \r\n"
	got := NormalizeForCompare(in)
	want := "hello\nworld"
	if got != want {
		t.Fatalf("normalizeForCompare() = %q, want %q", got, want)
	}
}

func TestExtractTranslationEnvelope(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "你好", want: "你好"},
		{name: "wrapped", in: "<TRANSLATION>\n你好\n</TRANSLATION>", want: "你好"},
		{name: "case insensitive delimiters", in: "<translation>你好</translation>", want: "你好"},
		{name: "extra text outside", in: "note\n<TRANSLATION>你好</TRANSLATION>\ndone", want: "你好"},
		{name: "partial open only", in: "<TRANSLATION>你好", want: "你好"},
		{name: "partial open before markdown", in: "<TRANSLATION>\n**评估代码路径**\n\n正文", want: "**评估代码路径**\n\n正文"},
		{name: "partial open not at start", in: "前缀 <TRANSLATION>你好", want: "前缀 <TRANSLATION>你好"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractTranslationEnvelope(tt.in); got != tt.want {
				t.Fatalf("ExtractTranslationEnvelope() = %q, want %q", got, tt.want)
			}
		})
	}
}
