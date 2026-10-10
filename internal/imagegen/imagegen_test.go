package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDownloadRedactsSignedURLAndPreservesCancellation(t *testing.T) {
	err := redactImageURLError(&url.Error{Op: "Get", URL: "https://example.invalid/image?signature=secret", Err: context.Canceled})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret") {
		t.Fatal("signed URL leaked or cancellation chain lost", err)
	}
}

func TestResponseMetadataBounded(t *testing.T) {
	target, _ := ResolveTarget(PresetOpenAI, "gpt-image-1", "")
	data, err := json.Marshal(map[string]any{
		"data":       []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(samplePNG(t)), "revised_prompt": strings.Repeat("tree ", 10000)}},
		"usage":      map[string]any{"detail": strings.Repeat("x", 20000)},
		"request_id": strings.Repeat("r", 2000),
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseResponse(t.Context(), target, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.RequestID) > 1024 || len(r.Images[0].RevisedPrompt) > 32000 || r.Usage != nil || len(r.Warnings) != 3 {
		t.Fatal("provider metadata not bounded")
	}
}

func samplePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 128})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestTargetValidation(t *testing.T) {
	for _, tc := range []struct {
		preset, model string
		edit          bool
	}{
		{PresetOpenAI, "gpt-image-1", true}, {PresetCompatible, "sample-image", false}, {PresetGemini, "gemini-2.5-flash-image", true}, {PresetGemini, "gemini-3-pro-image-preview", true}, {PresetSeedream, "doubao-seedream-4-0-250828", false}, {PresetXAI, "grok-imagine-image", false}, {PresetQwen, "qwen-image-3.0", false},
	} {
		t.Run(tc.preset+tc.model, func(t *testing.T) {
			target, err := ResolveTarget(tc.preset, tc.model, "https://example.invalid/v1")
			if err != nil {
				t.Fatal(err)
			}
			if target.Edit != tc.edit {
				t.Fatal("wrong edit capability")
			}
			r := Request{Prompt: "A small tree", Operation: Generate}
			if err := target.Validate(&r); err != nil {
				t.Fatal(err)
			}
			r.Operation = Edit
			if err := target.Validate(&r); err == nil {
				t.Fatal("edit without references accepted")
			}
			r.ReferenceImages = []string{"sample.png"}
			if got := target.Validate(&r) == nil; got != tc.edit {
				t.Fatal("wrong reference capability")
			}
		})
	}
	for _, tc := range []struct{ preset, model, base string }{
		{"unknown", "sample", ""}, {PresetOpenAI, "unknown", ""}, {PresetGemini, "unknown", ""}, {PresetSeedream, "unknown", ""}, {PresetXAI, "unknown", ""}, {PresetQwen, "unknown", ""}, {PresetCompatible, "sample", ""}, {PresetCompatible, "sample", "ftp://example.invalid"}, {PresetCompatible, "sample", "https://key@example.invalid"}, {PresetCompatible, "sample", "https://example.invalid?q=1"}, {PresetCompatible, "a/b", "https://example.invalid"},
	} {
		if _, err := ResolveTarget(tc.preset, tc.model, tc.base); err == nil {
			t.Fatalf("invalid target accepted: %+v", tc)
		}
	}
	for _, preset := range []string{PresetOpenAI, PresetGemini, PresetSeedream, PresetXAI} {
		models := map[string]string{PresetOpenAI: "gpt-image-1", PresetGemini: "gemini-2.5-flash-image", PresetSeedream: "doubao-seedream-4-5-251128", PresetXAI: "grok-imagine-image-pro"}
		if _, err := ResolveTarget(preset, models[preset], ""); err != nil {
			t.Fatal(err)
		}
	}
	target, _ := ResolveTarget(PresetOpenAI, "gpt-image-1", "")
	for _, r := range []Request{{}, {Prompt: "tree", Operation: "mask"}, {Prompt: "tree", Size: "4K"}, {Prompt: "tree", Quality: "unknown"}, {Prompt: "tree", AspectRatio: "1:1"}, {Prompt: "tree", Background: "transparent", OutputFormat: "jpeg"}, {Prompt: "tree", OutputFormat: "gif"}, {Prompt: "tree", ReferenceImages: make([]string, 6)}} {
		if target.Validate(&r) == nil {
			t.Fatalf("invalid request accepted: %+v", r)
		}
	}
}

func TestRequestPresetsAndMultipart(t *testing.T) {
	data := samplePNG(t)
	img, err := ValidateImage(t.Context(), data, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		preset, model string
		fields        map[string]any
		absent        []string
	}{
		{PresetOpenAI, "gpt-image-1", map[string]any{"n": float64(1), "output_format": "png", "background": "transparent"}, []string{"response_format"}},
		{PresetCompatible, "sample-image", map[string]any{"n": float64(1), "response_format": "b64_json"}, []string{"background", "quality"}},
		{PresetSeedream, "doubao-seedream-4-0-250828", map[string]any{"sequential_image_generation": "disabled", "response_format": "b64_json", "stream": false}, []string{"n", "quality"}},
		{PresetXAI, "grok-imagine-image", map[string]any{"n": float64(1), "resolution": "2k", "aspect_ratio": "16:9"}, []string{"size", "quality"}},
		{PresetQwen, "qwen-image-3.0", map[string]any{"n": float64(1), "size": "1024x1024"}, []string{"response_format", "quality"}},
	} {
		t.Run(tc.preset, func(t *testing.T) {
			target, _ := ResolveTarget(tc.preset, tc.model, "https://example.invalid/v1")
			r := Request{Prompt: "tree", Operation: Generate}
			if tc.preset == PresetOpenAI {
				r.Background = "transparent"
			}
			if tc.preset == PresetXAI {
				r.Size = "2k"
				r.AspectRatio = "16:9"
			}
			if tc.preset == PresetQwen {
				r.Size = "1024x1024"
			}
			req, _, err := BuildRequest(t.Context(), target, r, "test-key")
			if err != nil {
				t.Fatal(err)
			}
			if req.GetBody != nil || req.Header.Get("Authorization") != "Bearer test-key" {
				t.Fatal("unsafe request transport contract")
			}
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			for key, want := range tc.fields {
				if body[key] != want {
					t.Errorf("%s = %v, want %v", key, body[key], want)
				}
			}
			for _, key := range tc.absent {
				if _, ok := body[key]; ok {
					t.Errorf("unexpected field %s", key)
				}
			}
		})
	}
	target, _ := ResolveTarget(PresetOpenAI, "gpt-image-1", "")
	req, _, err := BuildRequest(t.Context(), target, Request{Prompt: "change color", Operation: Edit, References: []Image{img}, Quality: "high"}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(req.URL.Path, "/edits") {
		t.Fatal(req.URL)
	}
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	defer req.MultipartForm.RemoveAll()
	file, _, err := req.FormFile("image[]")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, _ := io.ReadAll(file)
	if !bytes.Equal(got, data) || req.FormValue("quality") != "high" {
		t.Fatal("edit did not preserve reference bytes")
	}
	target, _ = ResolveTarget(PresetGemini, "gemini-3-pro-image-preview", "")
	req, _, err = BuildRequest(t.Context(), target, Request{Prompt: "tree", References: []Image{img}, Size: "2K", AspectRatio: "4:3"}, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("x-goog-api-key") != "test-key" || req.Header.Get("Authorization") != "" {
		t.Fatal("wrong Gemini auth")
	}
	body, _ := io.ReadAll(req.Body)
	for _, want := range []string{"inlineData", "responseModalities", "imageSize", "aspectRatio"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatal(want)
		}
	}
	if bytes.Contains(body, []byte("googleSearch")) {
		t.Fatal("implicit grounding")
	}
}

func TestResponseFinalPartsAndLimits(t *testing.T) {
	pngData := samplePNG(t)
	encoded := base64.StdEncoding.EncodeToString(pngData)
	target := Target{Preset: PresetOpenAI}
	raw := []byte(`{"data":[{"b64_json":"` + encoded + `","revised_prompt":"tree"},{"url":"https://example.invalid/image"}],"usage":{"input_tokens":3}}`)
	result, err := ParseResponse(t.Context(), target, raw)
	if err != nil || len(result.Images) != 2 || !bytes.Equal(result.Images[0].Data, pngData) || result.Images[0].RevisedPrompt != "tree" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	target.Preset = PresetGemini
	raw = []byte(`{"responseId":"request-1","usageMetadata":{"candidatesTokenCount":4},"candidates":[{"content":{"parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"bad"}},{"text":"tree"},{"inlineData":{"mimeType":"image/png","data":"` + encoded + `"}}]}}]}`)
	result, err = ParseResponse(t.Context(), target, raw)
	if err != nil || len(result.Images) != 1 || result.RequestID != "request-1" || result.Text == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, raw := range []string{`{}`, `{"candidates":[{"content":{"parts":[{"text":"refused"}]}}]}`, `{"candidates":[{"content":{"parts":[{"thought":true,"inlineData":{"data":"` + encoded + `"}}]}}]}`, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"` + encoded + `"}}]}}]}`, `{bad`} {
		if _, err := ParseResponse(t.Context(), target, []byte(raw)); err == nil {
			t.Fatal("invalid final output accepted")
		}
	}
	target.Preset = PresetOpenAI
	for _, raw := range []string{`{}`, `{bad`, `{"data":[{}]}`, `{"data":[{"b64_json":"bad"}]}`, `{"data":[{},{},{},{},{},{}]}`} {
		if _, err := ParseResponse(t.Context(), target, []byte(raw)); err == nil {
			t.Fatal("invalid Images output accepted")
		}
	}
	if _, err := decodeCandidate(t.Context(), strings.Repeat("A", (MaxImageBytes/3+2)*4), ""); err == nil {
		t.Fatal("oversized base64 accepted")
	}
	if _, err := ReadBounded(strings.NewReader("1234"), 3); err == nil {
		t.Fatal("oversized stream accepted")
	}
	if _, err := ValidateImage(t.Context(), pngData[:len(pngData)-15], ""); err == nil {
		t.Fatal("truncated PNG accepted")
	}
	if _, err := ValidateImage(t.Context(), nil, ""); err == nil {
		t.Fatal("empty image accepted")
	}
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	img, err := ValidateImage(t.Context(), jpegBuf.Bytes(), "image/jpeg")
	if err != nil || img.Width != 2 || Extension(img.MIME) != ".jpg" {
		t.Fatal(err)
	}
	if Extension("image/png") != ".png" {
		t.Fatal("wrong extension")
	}
	// A valid IHDR with an excessive pixel count must fail before allocation
	// or full decoding, even though its compressed payload is tiny.
	hostile := bytes.Clone(pngData)
	binary.BigEndian.PutUint32(hostile[16:20], 6001)
	binary.BigEndian.PutUint32(hostile[20:24], 6001)
	binary.BigEndian.PutUint32(hostile[29:33], crc32.ChecksumIEEE(hostile[12:29]))
	if _, err := ValidateImage(t.Context(), hostile, ""); err == nil || !strings.Contains(err.Error(), "pixel budget") {
		t.Fatal("oversized image reached decoding", err)
	}
	webp, err := base64.StdEncoding.DecodeString("UklGRmAAAABXRUJQVlA4IFQAAAAQAgCdASoDAAQAAgA0JbACdC0Zga/2YmwAAP7qXtN+oPd/gvF9Cty3/8wcNgdPgCCf/GgvP/3+ueGP65nn+zl9/v8EVv+Co/QVopxwKRjxUy2YAAA=")
	if err != nil {
		t.Fatal(err)
	}
	img, err = ValidateImage(t.Context(), webp, "image/webp")
	if err != nil || img.Width != 3 || img.Height != 4 || !bytes.Equal(img.Data, webp) {
		t.Fatal("WebP original changed", err)
	}
}

func TestExecuteFailureClassificationAndBarrier(t *testing.T) {
	for _, tc := range []struct {
		status      int
		body, state string
		retry       bool
	}{
		{401, `{"error":{"code":"invalid_api_key"}}`, StateRejected, true}, {429, `{"error":{"code":"insufficient_quota"}}`, StateRejected, true}, {400, `{"error":{"type":"invalid_request_error"}}`, StateRejected, false}, {502, `{"error":{"code":"invalid_api_key"}}`, StateUnknown, false}, {401, `proxy denied`, StateUnknown, false}, {200, `{bad`, StateCompleted, false},
	} {
		t.Run(tc.state+tc.body, func(t *testing.T) {
			count := 0
			barrier := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				if !barrier {
					t.Error("request preceded durable barrier")
				}
				w.Header().Set("x-request-id", "request-1")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			target, _ := ResolveTarget(PresetCompatible, "sample-image", server.URL)
			_, err := Execute(t.Context(), server.Client(), target, Request{Prompt: "tree"}, "key", func() error { barrier = true; return nil })
			failure, ok := errors.AsType[*Failure](err)
			if !ok || failure.State != tc.state || failure.RetryKey != tc.retry || count != 1 || failure.RequestID != "request-1" {
				t.Fatalf("failure=%+v count=%d", failure, count)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("barrier failure dispatched request") }))
	defer server.Close()
	target, _ := ResolveTarget(PresetCompatible, "sample-image", server.URL)
	_, err := Execute(t.Context(), server.Client(), target, Request{Prompt: "tree"}, "key", func() error { return io.ErrClosedPipe })
	failure, _ := errors.AsType[*Failure](err)
	if failure.State != StateNotSent {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Execute(ctx, server.Client(), target, Request{Prompt: "tree"}, "key", func() error { return nil })
	failure, _ = errors.AsType[*Failure](err)
	if failure.State != StateNotSent {
		t.Fatal(err)
	}
}

func TestExecuteSuccessRedirectAndDownload(t *testing.T) {
	data := samplePNG(t)
	encoded := base64.StdEncoding.EncodeToString(data)
	postCount := 0
	getCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/generations":
			postCount++
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + encoded + `"}]}`))
		case "/redirect/generations":
			http.Redirect(w, r, "/generations", http.StatusTemporaryRedirect)
		case "/image":
			getCount++
			if r.Header.Get("Authorization") != "" || r.Header.Get("x-goog-api-key") != "" {
				t.Error("credentials leaked to download")
			}
			_, _ = w.Write(data)
		case "/download":
			http.Redirect(w, r, "/image", http.StatusFound)
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	target, _ := ResolveTarget(PresetCompatible, "sample-image", server.URL)
	result, err := Execute(t.Context(), server.Client(), target, Request{Prompt: "tree"}, "key", func() error { return nil })
	if err != nil || !bytes.Equal(result.Images[0].Data, data) || postCount != 1 {
		t.Fatal(err)
	}
	target.BaseURL = server.URL + "/redirect"
	_, err = Execute(t.Context(), server.Client(), target, Request{Prompt: "tree"}, "key", func() error { return nil })
	if err == nil || postCount != 1 {
		t.Fatal("paid POST redirect followed")
	}
	authorized := 0
	img, err := Download(t.Context(), server.Client(), server.URL+"/download", func(string) error { authorized++; return nil })
	if err != nil || !bytes.Equal(img.Data, data) || authorized != 2 || getCount != 1 {
		t.Fatal(err)
	}
	_, err = Download(t.Context(), server.Client(), server.URL+"/download", func(raw string) error {
		if strings.HasSuffix(raw, "/image") {
			return io.ErrClosedPipe
		}
		return nil
	})
	if err == nil || getCount != 1 {
		t.Fatal("redirect bypassed authorization")
	}
	for _, url := range []string{"file:///tmp/image", "http://key@example.invalid/image", server.URL + "/missing"} {
		if _, err := Download(t.Context(), server.Client(), url, func(string) error { return nil }); err == nil {
			t.Fatal("invalid download accepted")
		}
	}
}

func TestImageWireSizeDefaultsPreserveUserConstraints(t *testing.T) {
	for _, tc := range []struct{ preset, model, want string }{{PresetOpenAI, "gpt-image-1.5", "auto"}, {PresetCompatible, "sample-image", "1024x1024"}} {
		target, err := ResolveTarget(tc.preset, tc.model, "https://example.invalid/v1/images")
		if err != nil {
			t.Fatal(err)
		}
		for _, size := range []string{"", "1536x1024"} {
			request := Request{Prompt: "A tree", Operation: Generate, Size: size}
			if err = target.Validate(&request); err != nil {
				t.Fatal(err)
			}
			if request.Size != size {
				t.Fatal("validation imposed a routing constraint")
			}
			req, _, err := BuildRequest(t.Context(), target, request, "sample-key")
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			err = json.NewDecoder(req.Body).Decode(&fields)
			req.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := size
			if want == "" {
				want = tc.want
			}
			if fields["size"] != want || request.Size != size {
				t.Fatalf("fields=%v request=%+v", fields, request)
			}
		}
	}
}
