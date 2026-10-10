package imagegen

import (
	"crypto/sha256"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const imageEditFormField = "image[]"

// RequestDiagnostics describes the encoded request without credentials or content.
type RequestDiagnostics struct {
	Operation   string                 `json:"operation"`
	Preset      string                 `json:"preset"`
	Endpoint    string                 `json:"endpoint"`
	ContentType string                 `json:"content_type"`
	Bytes       int64                  `json:"bytes"`
	Parameters  map[string]string      `json:"parameters,omitempty"`
	Images      []ReferenceDiagnostics `json:"images,omitempty"`
}

type ReferenceDiagnostics struct {
	Field    string `json:"field"`
	MIME     string `json:"mime_type"`
	PartMIME string `json:"part_mime_type,omitempty"`
	Bytes    int    `json:"bytes"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	SHA256   string `json:"sha256"`
}

type ResponseDiagnostics struct {
	ContentType string `json:"content_type,omitempty"`
	Bytes       int    `json:"bytes"`
	CFRay       string `json:"cf_ray,omitempty"`
}

func newRequestDiagnostics(t Target, r Request, fields map[string]any, secrets []string) *RequestDiagnostics {
	d := &RequestDiagnostics{Operation: r.Operation, Preset: t.Preset}
	if d.Operation == "" {
		d.Operation = Generate
	}
	for _, k := range []string{"n", "size", "quality", "background", "output_format", "response_format", "resolution", "aspect_ratio", "sequential_image_generation", "stream"} {
		if v, ok := fields[k]; ok {
			if d.Parameters == nil {
				d.Parameters = make(map[string]string)
			}
			d.Parameters[k] = boundedImageText(safeImageErrorMessage(fmt.Sprint(v), secrets), 64)
		}
	}
	if t.Preset == PresetGemini {
		if g, ok := fields["generationConfig"].(map[string]any); ok {
			if c, ok := g["imageConfig"].(map[string]any); ok {
				for k, v := range c {
					if d.Parameters == nil {
						d.Parameters = make(map[string]string)
					}
					d.Parameters[k] = boundedImageText(safeImageErrorMessage(fmt.Sprint(v), secrets), 64)
				}
			}
		}
	}
	for _, img := range r.References {
		d.Images = append(d.Images, ReferenceDiagnostics{MIME: img.MIME, Bytes: len(img.Data), Width: img.Width, Height: img.Height, SHA256: fmt.Sprintf("%x", sha256.Sum256(img.Data))})
	}
	return d
}

var (
	diagnosticMediaType   = regexp.MustCompile(`^[a-zA-Z0-9.+-]{1,64}/[a-zA-Z0-9.+-]{1,64}$`)
	diagnosticToken       = regexp.MustCompile(`^[A-Za-z0-9_.:\[\]-]{1,128}$`)
	diagnosticURL         = regexp.MustCompile(`(?i)(?:https?://|file://|artifact:|data:)\S+`)
	diagnosticCredential  = regexp.MustCompile(`(?i)\b(?:authorization|api[_ -]?key|token|secret|signature|password)\s*[:=]\s*(?:Bearer\s+)?(?:"[^"]*"|'[^']*'|\S+)`)
	diagnosticBearer      = regexp.MustCompile(`(?i)\bBearer\s+\S+`)
	diagnosticSecretToken = regexp.MustCompile(`\b(?:sk-|sess-|eyJ)[A-Za-z0-9_.-]+`)
	diagnosticBlob        = regexp.MustCompile(`[A-Za-z0-9+/=_-]{64,}`)
	diagnosticQuoted      = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`)
	diagnosticPrompt      = regexp.MustCompile(`(?i)\b(?:prompt|request body)\s*[:=].*`)
	diagnosticParam       = regexp.MustCompile(`^(?:image|images|mask)(?:\[\d{0,3}\]|\.\d{1,3})?(?:\.(?:image_url|file_id))?$`)
)

func diagnosticRedactions(key string, r Request) []string {
	return append([]string{key, r.Prompt, r.OutputPath}, r.ReferenceImages...)
}

func redactDiagnosticValues(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
			escaped := strconv.Quote(secret)
			value = strings.ReplaceAll(value, escaped[1:len(escaped)-1], "[redacted]")
		}
	}
	return value
}

func safeDiagnosticToken(value string, secrets []string) string {
	if !diagnosticToken.MatchString(value) || redactDiagnosticValues(value, secrets) != value || diagnosticSecretToken.MatchString(value) {
		return ""
	}
	return value
}

func safeDiagnosticLiteral(value string) bool {
	if diagnosticParam.MatchString(value) {
		return true
	}
	switch value {
	case "model", "prompt", "size", "n", "quality", "background", "output_format", "response_format", "input_fidelity", "image_url", "file_id", "image/png", "image/jpeg", "image/webp", "application/octet-stream", "auto", "opaque", "transparent", "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

func safeImageErrorMessage(value string, secrets []string) string {
	// Remove complete secrets before truncation so a cut cannot expose their prefix.
	value = redactDiagnosticValues(value, secrets)
	if len(value) > 8192 {
		return "provider error message exceeds diagnostic limit"
	}
	value = diagnosticURL.ReplaceAllString(value, "[redacted]")
	value = diagnosticCredential.ReplaceAllString(value, "[redacted]")
	value = diagnosticBearer.ReplaceAllString(value, "[redacted]")
	value = diagnosticSecretToken.ReplaceAllString(value, "[redacted]")
	value = diagnosticBlob.ReplaceAllString(value, "[redacted]")
	value = diagnosticQuoted.ReplaceAllStringFunc(value, func(quoted string) string {
		if safeDiagnosticLiteral(quoted[1 : len(quoted)-1]) {
			return quoted
		}
		return "[redacted]"
	})
	value = diagnosticPrompt.ReplaceAllString(value, "prompt=[redacted]")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return boundedImageText(strings.Join(strings.Fields(value), " "), 512)
}

func responseDiagnostics(headers http.Header, bytes int, secrets []string) *ResponseDiagnostics {
	contentType, _, _ := mime.ParseMediaType(headers.Get("Content-Type"))
	if !diagnosticMediaType.MatchString(contentType) || redactDiagnosticValues(contentType, secrets) != contentType {
		contentType = ""
	}
	return &ResponseDiagnostics{ContentType: contentType, Bytes: bytes, CFRay: safeDiagnosticToken(headers.Get("cf-ray"), secrets)}
}
