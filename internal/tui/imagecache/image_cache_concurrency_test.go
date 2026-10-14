package imagecache

import (
	"bytes"
	"image"
	"image/jpeg"
	"sync"
	"testing"
	"time"
)

func TestImageViewerPreparationDoesNotBlockOtherInlineImages(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	for n := range 7 {
		part := Source{Data: append(append([]byte{}, data...), byte(n)), MIME: "image/jpeg"}
		if _, err := imageRuntimeCache.Lookup(part, false); err != nil {
			t.Fatal(err)
		}
	}
	entered, release, done, loaded := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan error, 1)
	original := imageCacheDecode
	var once sync.Once
	imageCacheDecode = func(data []byte) (image.Image, string, error) {
		close(entered)
		<-release
		return original(data)
	}
	defer func() {
		once.Do(func() { close(release) })
		<-done
		imageCacheDecode = original
	}()
	go func() {
		defer close(done)
		_, err := cacheTransport(Source{Data: data, MIME: "image/jpeg"})
		loaded <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("viewer did not reach decode")
	}
	rendered := make(chan error, 1)
	go func() {
		part := Source{Data: append(append([]byte{}, data...), 255), MIME: "image/jpeg"}
		entry, err := imageRuntimeCache.Lookup(part, false)
		if err == nil {
			_, _, err = entry.Config(part)
		}
		rendered <- err
	}()
	select {
	case err := <-rendered:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("inline layout waited for unrelated viewer decoding")
		once.Do(func() { close(release) })
		if err := <-rendered; err != nil {
			t.Error(err)
		}
	}
	once.Do(func() { close(release) })
	if err := <-loaded; err != nil {
		t.Error(err)
	}
}

func TestImageCacheResidentBytesTrackPublishedPayloads(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	part := Source{MIME: "image/png", Data: makeTestPNG(t)}
	entry, err := imageRuntimeCache.Lookup(part, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Raw(part); err != nil {
		t.Fatal(err)
	}
	if got := entry.residentBytes.Load(); got != int64(cap(part.Data)) {
		t.Fatalf("Raw bytes = %d, want %d", got, cap(part.Data))
	}
	for range 2 {
		encoded, _, err := entry.Base64PNG(part)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := entry.residentBytes.Load(), int64(cap(part.Data)+len(encoded)); got != want {
			t.Fatalf("resident bytes = %d, want %d", got, want)
		}
	}
}
