package imagegen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestImagePresetsDeliverPrompt(t *testing.T) {
	const prompt = "A small tree.\nSoft light and a \"blue\" background."
	data := samplePNG(t)
	img, err := ValidateImage(t.Context(), data, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		preset, model, operation string
	}{
		{PresetOpenAI, "gpt-image-1", Generate},
		{PresetCompatible, "sample-image", Generate},
		{PresetGemini, "gemini-3-pro-image-preview", Generate},
		{PresetSeedream, "doubao-seedream-4-0-250828", Generate},
		{PresetXAI, "grok-imagine-image", Generate},
		{PresetQwen, "qwen-image-3.0", Generate},
		{PresetOpenAI, "gpt-image-1", Edit},
		{PresetGemini, "gemini-3-pro-image-preview", Edit},
	} {
		t.Run(tc.preset+"/"+tc.operation, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost {
					t.Error("image request must use POST")
				}
				var gotPrompt string
				if tc.preset == PresetOpenAI && tc.operation == Edit {
					if r.URL.Path != "/edits" {
						t.Errorf("wrong edit endpoint: %s", r.URL.Path)
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					defer r.MultipartForm.RemoveAll()
					gotPrompt = r.FormValue("prompt")
				} else {
					var body struct {
						Model    string `json:"model"`
						Prompt   string `json:"prompt"`
						Contents []struct {
							Parts []struct {
								Text string `json:"text"`
							} `json:"parts"`
						} `json:"contents"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					gotPrompt = body.Prompt
					if tc.preset == PresetGemini {
						if r.URL.Path != "/models/"+tc.model+":generateContent" || len(body.Contents) != 1 || len(body.Contents[0].Parts) == 0 {
							t.Error("wrong Gemini request structure")
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						gotPrompt = body.Contents[0].Parts[0].Text
					} else if r.URL.Path != "/generations" || body.Model != tc.model {
						t.Error("wrong image endpoint or model")
					}
				}
				if gotPrompt != prompt {
					t.Errorf("delivered prompt = %q, want %q", gotPrompt, prompt)
				}
				encoded := base64.StdEncoding.EncodeToString(data)
				response := `{"data":[{"b64_json":"` + encoded + `"}]}`
				if tc.preset == PresetGemini {
					response = `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + encoded + `"}}]}}]}`
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(response)); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			target, err := ResolveTarget(tc.preset, tc.model, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			request := Request{Operation: tc.operation, Prompt: prompt}
			if tc.operation == Edit {
				request.ReferenceImages = []string{"sample.png"}
				request.References = []Image{img}
			}
			if err := target.Validate(&request); err != nil {
				t.Fatal(err)
			}
			result, err := Execute(t.Context(), server.Client(), target, request, "sample-key", func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || len(result.Images) != 1 || !bytes.Equal(result.Images[0].Data, data) {
				t.Fatal("image request did not complete in one call")
			}
		})
	}
}
