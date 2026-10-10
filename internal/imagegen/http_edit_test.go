package imagegen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEditUploadsReferencesWithTheirImageMIME(t *testing.T) {
	pngData := samplePNG(t)
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil); err != nil {
		t.Fatal(err)
	}
	var references []Image
	for _, data := range [][]byte{pngData, jpegData.Bytes()} {
		img, err := ValidateImage(t.Context(), data, "")
		if err != nil {
			t.Fatal(err)
		}
		references = append(references, img)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/edits" {
			t.Error("wrong edit endpoint")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer r.MultipartForm.RemoveAll()
		files := r.MultipartForm.File[imageEditFormField]
		if len(files) != len(references) {
			t.Error("missing reference files")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for i, file := range files {
			if file.Header.Get("Content-Type") != references[i].MIME {
				t.Errorf("reference %d Content-Type=%q, want %q", i, file.Header.Get("Content-Type"), references[i].MIME)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f, err := file.Open()
			if err != nil {
				t.Error(err)
				return
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil || !bytes.Equal(data, references[i].Data) {
				t.Error("reference bytes changed")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(pngData)}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	target, err := ResolveTarget(PresetOpenAI, "gpt-image-1", server.URL+"/v1/images")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(t.Context(), server.Client(), target, Request{Operation: Edit, Prompt: "Change the square color", References: references}, "sample-key", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(result.Images) != 1 || !bytes.Equal(result.Images[0].Data, pngData) {
		t.Fatal("edit did not complete in one request")
	}
}
