package imagegen

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestEditFailureRetainsWireDiagnostics(t *testing.T) {
	data := samplePNG(t)
	img, err := ValidateImage(t.Context(), data, "")
	if err != nil {
		t.Fatal(err)
	}
	key, prompt := "sample-credential", "Change the square color"
	message := "Invalid file 'image[0]': unsupported image type. Supported formats: 'image/png', 'image/jpeg', 'image/webp'."
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/images/edits" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("size") != "auto" || r.FormValue("n") != "1" {
			t.Error("missing wire defaults")
		}
		files := r.MultipartForm.File[imageEditFormField]
		if len(files) != 1 {
			t.Error("missing image field")
			w.WriteHeader(500)
			return
		}
		file, err := files[0].Open()
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		received, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(received, data) {
			t.Error("reference bytes changed")
		}
		if files[0].Header.Get("Content-Type") != "image/png" {
			t.Error("upload MIME does not match reference image")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("x-request-id", "request-1")
		w.Header().Set("cf-ray", "1234abcd-TEST")
		w.WriteHeader(http.StatusBadRequest)
		if err := json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "unsupported_file_mimetype", "type": "invalid_request_error", "param": "image[0]", "message": message + " Payload: " + base64.StdEncoding.EncodeToString(data) + " Credential: " + key + " Prompt: " + prompt}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	target, _ := ResolveTarget(PresetOpenAI, "gpt-image-1", server.URL+"/v1/images")
	_, err = Execute(t.Context(), server.Client(), target, Request{Operation: Edit, Prompt: prompt, References: []Image{img}}, key, func() error { return nil })
	failure, ok := errors.AsType[*Failure](err)
	if !ok {
		t.Fatalf("failure=%v", err)
	}
	d := failure.Details
	if calls.Load() != 1 || failure.State != StateRejected || failure.RetryKey || failure.RequestID != "request-1" || d.Code != "unsupported_file_mimetype" || d.Param != "image[0]" || !strings.Contains(d.Message, message) {
		t.Fatalf("failure=%+v details=%+v", failure, d)
	}
	if d.Request == nil || d.Request.Endpoint != "/v1/images/edits" || d.Request.Operation != Edit || d.Request.Preset != PresetOpenAI || d.Request.ContentType != "multipart/form-data" || d.Request.Bytes <= int64(len(data)) || d.Request.Parameters["size"] != "auto" || d.Request.Parameters["n"] != "1" || len(d.Request.Images) != 1 {
		t.Fatalf("request=%+v", d.Request)
	}
	ref := d.Request.Images[0]
	if ref.Field != imageEditFormField || ref.MIME != "image/png" || ref.PartMIME != "image/png" || ref.Bytes != len(data) || ref.Width != img.Width || ref.Height != img.Height || ref.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatalf("reference=%+v", ref)
	}
	if d.Response == nil || d.Response.ContentType != "application/json" || d.Response.Bytes == 0 || d.Response.CFRay != "1234abcd-TEST" {
		t.Fatalf("response=%+v", d.Response)
	}
	saved, _ := json.Marshal(d)
	for _, secret := range []string{key, prompt, server.URL, base64.StdEncoding.EncodeToString(data)} {
		if strings.Contains(string(saved)+failure.Error(), secret) {
			t.Fatalf("diagnostics leaked %q", secret)
		}
	}
}

func TestImageErrorMessageRedactionAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		secrets     []string
		absent      []string
		present     string
	}{
		{"quoted values", `Invalid size 'private-input'; expected 'auto'.`, nil, []string{"private-input"}, "expected 'auto'"},
		{"credentials and URL", `Failed with Authorization: Bearer sample-token URL https://example.invalid/image?signature=private-signature data:image/png;base64,private-data`, nil, []string{"sample-token", "example.invalid", "private-signature", "private-data"}, "Failed"},
		{"known content", `Input: private-credential; private prompt text`, []string{"private-credential", "private prompt text"}, []string{"private-credential", "private prompt text"}, "Input"},
		{"escaped content", `Input: private\nprompt`, []string{"private\nprompt"}, []string{"private", "prompt"}, "Input"},
		{"large blob", "Invalid image " + strings.Repeat("a", 1000), nil, []string{strings.Repeat("a", 129)}, "Invalid image"},
		{"controls", "Invalid\nimage\x1b\x00", nil, []string{"\n", "\x1b", "\x00"}, "Invalid image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := safeImageErrorMessage(tc.input, tc.secrets)
			if !strings.Contains(got, tc.present) {
				t.Fatalf("message=%q", got)
			}
			for _, secret := range tc.absent {
				if strings.Contains(got, secret) {
					t.Fatalf("leaked %q in %q", secret, got)
				}
			}
		})
	}
	got := safeImageErrorMessage(strings.Repeat("sample ", 400), nil)
	if len(got) > 512 || !utf8.ValidString(got) {
		t.Fatalf("unbounded message len=%d", len(got))
	}
	if got = safeImageErrorMessage(strings.Repeat("sample ", 2000), nil); got != "provider error message exceeds diagnostic limit" {
		t.Fatalf("oversized message=%q", got)
	}
}

func TestGatewayFailureDiagnosticsDoNotRetainHTMLOrEnableReplay(t *testing.T) {
	headers := http.Header{"Content-Type": []string{"text/html"}, "Cf-Ray": []string{"abcd-TEST"}}
	failure := classifyImageHTTPFailure(524, headers, []byte("<html>private gateway body</html>"), nil)
	if failure.State != StateUnknown || failure.RetryKey || failure.Details.Message != "" || failure.Details.Response.ContentType != "text/html" || failure.Details.Response.CFRay != "abcd-TEST" {
		t.Fatalf("failure=%+v", failure)
	}
	body, _ := json.Marshal(failure.Details)
	if strings.Contains(string(body), "private gateway body") {
		t.Fatal("HTML body retained")
	}
	// A message that suggests retrying is diagnostic text, not execution evidence.
	failure = classifyImageHTTPFailure(503, http.Header{}, []byte(`{"error":{"code":"custom_failure","message":"retry with another key"}}`), nil)
	if failure.Details.Code != "custom_failure" || failure.State != StateUnknown || failure.RetryKey {
		t.Fatalf("unexpected retry classification=%+v", failure)
	}
}
