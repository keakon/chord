package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/keakon/chord/internal/httpheader"
)

const (
	StateNotSent   = "not_sent"
	StateRejected  = "rejected"
	StateUnknown   = "outcome_unknown"
	StateCompleted = "remote_completed"
	StateSaved     = "saved"
)

// Failure distinguishes safe credential rejection from uncertain execution.
type Failure struct {
	Provider  string
	Model     string
	State     string
	RetryKey  bool
	RequestID string
	Cause     error
	Details   FailureDetails
}

func (e *Failure) Error() string {
	text := fmt.Sprintf("image generation %s: %v", e.State, e.Cause)
	if e.State == StateCompleted {
		text = fmt.Sprintf("image service returned success; result validation could not complete: %v", e.Cause)
	}
	if e.Details.Category != "" {
		text += "; " + e.Details.String()
	}
	if e.State == StateUnknown || e.State == StateCompleted {
		text += "; do not replay generation"
	}
	return text
}
func (e *Failure) Unwrap() error { return e.Cause }

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("image API base_url must be an http(s) root without credentials, query or fragment")
	}
	return nil
}

func BuildRequest(ctx context.Context, t Target, r Request, key string) (*http.Request, *RequestDiagnostics, error) {
	fields := map[string]any{"model": t.Model, "prompt": r.Prompt}
	endpoint := t.BaseURL + "/generations"
	switch t.Preset {
	case PresetOpenAI:
		// Wire defaults are applied after pool selection, never as user constraints.
		fields["size"] = "auto"
		fields["n"] = 1
		for k, v := range map[string]string{"size": r.Size, "quality": r.Quality, "background": r.Background, "output_format": r.OutputFormat} {
			if v != "" {
				fields[k] = v
			}
		}
		if r.Background == "transparent" && r.OutputFormat == "" {
			fields["output_format"] = "png"
		}
	case PresetCompatible:
		fields["size"] = "1024x1024"
		fields["n"] = 1
		fields["response_format"] = "b64_json"
		if r.Size != "" {
			fields["size"] = r.Size
		}
	case PresetSeedream:
		fields["sequential_image_generation"] = "disabled"
		fields["response_format"] = "b64_json"
		fields["stream"] = false
		if r.Size != "" {
			fields["size"] = r.Size
		}
	case PresetXAI:
		fields["n"] = 1
		fields["response_format"] = "b64_json"
		if r.Size != "" {
			fields["resolution"] = r.Size
		}
		if r.AspectRatio != "" {
			fields["aspect_ratio"] = r.AspectRatio
		}
	case PresetQwen:
		fields["n"] = 1
		if r.Size != "" {
			fields["size"] = r.Size
		}
	case PresetGemini:
		endpoint = t.BaseURL + "/models/" + url.PathEscape(t.Model) + ":generateContent"
		parts := []map[string]any{{"text": r.Prompt}}
		for _, img := range r.References {
			parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": img.MIME, "data": base64.StdEncoding.EncodeToString(img.Data)}})
		}
		generation := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}, "candidateCount": 1}
		imageConfig := map[string]any{}
		if r.Size != "" {
			imageConfig["imageSize"] = r.Size
		}
		if r.AspectRatio != "" {
			imageConfig["aspectRatio"] = r.AspectRatio
		}
		if len(imageConfig) > 0 {
			generation["imageConfig"] = imageConfig
		}
		fields = map[string]any{"contents": []map[string]any{{"role": "user", "parts": parts}}, "generationConfig": generation}
	}
	secrets := diagnosticRedactions(key, r)
	diagnostics := newRequestDiagnostics(t, r, fields, secrets)
	var body io.Reader
	var contentLength int64
	contentType := "application/json"
	if t.Preset == PresetOpenAI && (r.Operation == Edit || len(r.References) > 0) {
		endpoint = t.BaseURL + "/edits"
		var buf bytes.Buffer
		var segments []io.Reader
		w := multipart.NewWriter(&buf)
		for k, v := range fields {
			if err := w.WriteField(k, fmt.Sprint(v)); err != nil {
				return nil, nil, fmt.Errorf("encode image edit field: %w", err)
			}
		}
		for i, img := range r.References {
			diagnostics.Images[i].Field = imageEditFormField
			ext := strings.TrimPrefix(img.MIME, "image/")
			header := textproto.MIMEHeader{
				"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": imageEditFormField, "filename": fmt.Sprintf("reference-%d.%s", i, ext)})},
				"Content-Type":        {img.MIME},
			}
			_, err := w.CreatePart(header)
			if err != nil {
				return nil, nil, fmt.Errorf("encode image edit: %w", err)
			}
			diagnostics.Images[i].PartMIME = header.Get("Content-Type")
			prefix := bytes.Clone(buf.Bytes())
			segments = append(segments, bytes.NewReader(prefix), bytes.NewReader(img.Data))
			contentLength += int64(len(prefix) + len(img.Data))
			buf.Reset()
		}
		if err := w.Close(); err != nil {
			return nil, nil, fmt.Errorf("finish image edit: %w", err)
		}
		segments = append(segments, bytes.NewReader(buf.Bytes()))
		contentLength += int64(buf.Len())
		body = io.MultiReader(segments...)
		contentType = w.FormDataContentType()
	} else {
		data, err := json.Marshal(fields)
		if err != nil {
			return nil, nil, fmt.Errorf("encode image request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, nil, fmt.Errorf("build image request: %w", err)
	}
	if contentLength != 0 {
		req.ContentLength = contentLength
	}
	// POST is not idempotent. In particular, do not install a replay body.
	req.GetBody = nil
	if t.UserAgent != "" {
		req.Header.Set("User-Agent", t.UserAgent)
	}
	req.Header.Set("Content-Type", contentType)
	if key != "" {
		if t.Preset == PresetGemini {
			req.Header.Set("x-goog-api-key", key)
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	diagnostics.Endpoint = boundedImageText(redactDiagnosticValues(req.URL.EscapedPath(), secrets), 256)
	diagnostics.ContentType = strings.Split(contentType, ";")[0]
	diagnostics.Bytes = req.ContentLength
	if t.Preset == PresetGemini {
		for i := range diagnostics.Images {
			diagnostics.Images[i].Field = "inlineData"
			diagnostics.Images[i].PartMIME = diagnostics.Images[i].MIME
		}
	}
	return req, diagnostics, nil
}

// Execute performs exactly one wire attempt. beforeSend is the durable barrier.
func Execute(ctx context.Context, client *http.Client, t Target, r Request, key string, beforeSend func() error) (result *Result, err error) {
	req, diagnostics, err := BuildRequest(ctx, t, r, key)
	if err != nil {
		return nil, &Failure{State: StateNotSent, Cause: err}
	}
	var response *ResponseDiagnostics
	secrets := diagnosticRedactions(key, r)
	defer func() {
		if failure, ok := errors.AsType[*Failure](err); ok {
			failure.Details.Request = diagnostics
			if failure.Details.Response == nil {
				failure.Details.Response = response
			}
			failure.RequestID = safeDiagnosticToken(failure.RequestID, secrets)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, &Failure{State: StateNotSent, Cause: err}
	}
	if err := beforeSend(); err != nil {
		return nil, &Failure{State: StateNotSent, Cause: err}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return nil, &Failure{State: StateUnknown, Cause: redactImageURLError(err)}
	}
	defer resp.Body.Close()
	id := resp.Header.Get("x-request-id")
	if id == "" {
		id = resp.Header.Get("request-id")
	}
	id = boundedImageText(id, 1024)
	data, err := ReadBounded(resp.Body, MaxResponseBytes)
	response = responseDiagnostics(resp.Header, len(data), secrets)
	if err != nil {
		return nil, &Failure{State: StateUnknown, RequestID: id, Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyImageHTTPFailure(resp.StatusCode, resp.Header, data, secrets)
	}
	result, err = ParseResponse(ctx, t, data)
	if err != nil {
		return nil, &Failure{State: StateCompleted, RequestID: id, Cause: err}
	}
	if result.RequestID == "" {
		result.RequestID = boundedImageText(id, 1024)
	}
	return result, nil
}

func decodeCandidate(ctx context.Context, encoded, mime string) (Image, error) {
	if base64.StdEncoding.DecodedLen(len(encoded)) > MaxImageBytes+2 {
		return Image{}, fmt.Errorf("base64 image exceeds byte budget")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Image{}, fmt.Errorf("decode image base64: %w", err)
	}
	return ValidateImage(ctx, data, mime)
}

func ParseResponse(ctx context.Context, t Target, data []byte) (*Result, error) {
	result := &Result{}
	if t.Preset == PresetGemini {
		var response struct {
			ResponseID string         `json:"responseId"`
			Usage      map[string]any `json:"usageMetadata"`
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text       string `json:"text"`
						Thought    bool   `json:"thought"`
						InlineData struct {
							MIME string `json:"mimeType"`
							Data string `json:"data"`
						} `json:"inlineData"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("decode Gemini image response: %w", err)
		}
		result.RequestID = response.ResponseID
		result.Usage = response.Usage
		for _, candidate := range response.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.Thought {
					continue
				}
				if part.Text != "" {
					result.Text = boundedImageText(result.Text+part.Text+"\n", 32000)
				}
				if part.InlineData.Data == "" {
					continue
				}
				if len(result.Images) >= MaxImages {
					return nil, fmt.Errorf("too many final images")
				}
				img, err := decodeCandidate(ctx, part.InlineData.Data, part.InlineData.MIME)
				if err != nil {
					return nil, err
				}
				result.Images = append(result.Images, Candidate{Image: img})
			}
		}
	} else {
		var response struct {
			Data []struct {
				B64     string `json:"b64_json"`
				URL     string `json:"url"`
				Revised string `json:"revised_prompt"`
			} `json:"data"`
			Usage     map[string]any `json:"usage"`
			RequestID string         `json:"request_id"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("decode Images response: %w", err)
		}
		if len(response.Data) > MaxImages {
			return nil, fmt.Errorf("too many returned images")
		}
		result.Usage = response.Usage
		result.RequestID = response.RequestID
		for _, entry := range response.Data {
			candidate := Candidate{URL: entry.URL, RevisedPrompt: entry.Revised}
			if entry.B64 != "" {
				img, err := decodeCandidate(ctx, entry.B64, "")
				if err != nil {
					return nil, err
				}
				candidate.Image = img
				candidate.URL = ""
			} else if entry.URL == "" {
				return nil, fmt.Errorf("image entry contains neither data nor URL")
			}
			result.Images = append(result.Images, candidate)
		}
	}
	if len(result.Images) == 0 {
		return nil, fmt.Errorf("provider returned no final images (text-only, thought-only or refusal response)")
	}
	if len(result.RequestID) > 1024 {
		result.RequestID = boundedImageText(result.RequestID, 1024)
		result.Warnings = append(result.Warnings, "Provider request ID was truncated to fit the metadata limit.")
	}
	if usage, err := json.Marshal(result.Usage); err != nil || len(usage) > 16<<10 {
		result.Usage = nil
		result.Warnings = append(result.Warnings, "Provider usage exceeded the metadata limit and was omitted; billing remains unknown.")
	}
	for i := range result.Images {
		img := &result.Images[i]
		if len(img.URL) > 8192 {
			return nil, fmt.Errorf("image download URL exceeds metadata limit")
		}
		if len(img.RevisedPrompt) > 32000 {
			img.RevisedPrompt = boundedImageText(img.RevisedPrompt, 32000)
			result.Warnings = append(result.Warnings, "Provider revised prompt was truncated to fit the metadata limit.")
		}
	}
	return result, nil
}

func boundedImageText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// url.Error includes signed query strings; retain the underlying error chain
// (including cancellation) without putting the URL in tool output or logs.
func redactImageURLError(err error) error {
	if e, ok := errors.AsType[*url.Error](err); ok {
		return e.Err
	}
	return err
}

// Download never forwards the generation request's credentials. Every redirect
// repeats the caller's network authorization before following it.
func Download(ctx context.Context, client *http.Client, rawURL string, authorize func(string) error) (Image, error) {
	var img Image
	err := RetryDelivery(ctx, "download", func() error {
		// Reserve time for further attempts and saving under the overall budget.
		attempt, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var err error
		img, err = downloadImage(attempt, client, rawURL, authorize)
		return err
	})
	return img, err
}

func downloadImage(ctx context.Context, client *http.Client, rawURL string, authorize func(string) error) (Image, error) {
	check := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid image download URL")
		}
		return authorize(raw)
	}
	if err := check(rawURL); err != nil {
		return Image{}, err
	}
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("image download exceeded redirect limit")
		}
		req.Header.Del("Authorization")
		req.Header.Del("x-goog-api-key")
		return check(req.URL.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Image{}, fmt.Errorf("build image download: %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return Image{}, fmt.Errorf("download image: %w", redactImageURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		delay, _ := httpheader.ParseRetryAfter(resp.Header.Get("Retry-After"))
		return Image{}, &downloadHTTPError{status: resp.StatusCode, retryAfter: delay}
	}
	data, err := ReadBounded(resp.Body, MaxImageBytes)
	if err != nil {
		return Image{}, err
	}
	return ValidateImage(ctx, data, "")
}

func Extension(mime string) string {
	if mime == "image/jpeg" {
		return ".jpg"
	}
	return "." + path.Base(mime)
}
