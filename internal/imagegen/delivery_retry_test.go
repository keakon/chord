package imagegen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDeliveryRetryClassifiesAndBoundsAttempts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		attempts int
	}{
		{"connection", fmt.Errorf("read: %w", syscall.ECONNRESET), 3},
		{"short response", io.ErrUnexpectedEOF, 3},
		{"short write", io.ErrShortWrite, 3},
		{"temporary write", &fs.PathError{Op: "write", Path: "image.png", Err: syscall.EINTR}, 3},
		{"busy file", syscall.EBUSY, 3},
		{"permission", fs.ErrPermission, 1},
		{"disk full", syscall.ENOSPC, 1},
		{"existing file", fs.ErrExist, 1},
		{"expired url", &downloadHTTPError{status: 403}, 1},
		{"server error", &downloadHTTPError{status: 503}, 3},
		{"invalid image", fmt.Errorf("invalid image"), 1},
		{"long retry advice", &downloadHTTPError{status: 429, retryAfter: time.Minute}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := retryDelivery(t.Context(), "test", 0, func() error { calls++; return tc.err })
			if !errors.Is(err, tc.err) || calls != tc.attempts {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
	count := 0
	if err := retryDelivery(t.Context(), "test", 0, func() error {
		count++
		if count < 3 {
			return syscall.EINTR
		}
		return nil
	}); err != nil || count != 3 {
		t.Fatalf("temporary error did not succeed: calls=%d error=%v", count, err)
	}
}

func TestDownloadRetriesInterruptedBody(t *testing.T) {
	data := samplePNG(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data[:len(data)/2])
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	img, err := Download(t.Context(), server.Client(), server.URL, func(string) error { return nil })
	if err != nil || calls.Load() != 2 || len(img.Data) != len(data) {
		t.Fatalf("requests=%d bytes=%d error=%v", calls.Load(), len(img.Data), err)
	}
}

func TestDeliveryRetryHonorsCancellationAndBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- retryDelivery(ctx, "test", time.Second, func() error {
			close(started)
			return syscall.EINTR
		})
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	calls := 0
	err := retryDelivery(ctx, "test", 0, func() error {
		calls++
		return &downloadHTTPError{status: 429, retryAfter: 2 * time.Second}
	})
	if err == nil || calls != 1 {
		t.Fatal("retry exceeded remaining budget")
	}
}

func TestDownloadRetriesSameURLAndRechecksAuthorization(t *testing.T) {
	data := samplePNG(t)
	for _, denyRetry := range []bool{false, true} {
		var calls atomic.Int32
		checks := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			_, _ = w.Write(data)
		}))
		img, err := Download(t.Context(), server.Client(), server.URL+"/image?signature=private", func(string) error {
			checks++
			if denyRetry && checks > 1 {
				return fs.ErrPermission
			}
			return nil
		})
		server.Close()
		if checks != 2 || (!denyRetry && (err != nil || len(img.Data) == 0 || calls.Load() != 2)) || (denyRetry && (!errors.Is(err, fs.ErrPermission) || calls.Load() != 1)) {
			t.Fatalf("deny=%t requests=%d checks=%d error=%v", denyRetry, calls.Load(), checks, err)
		}
		if err != nil && strings.Contains(err.Error(), "private") {
			t.Fatal("signed URL leaked")
		}
	}
}

func TestDeliveryCancellationPreservesActionFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	attempts := 0
	err := retryDelivery(ctx, "save original", time.Millisecond, func() error {
		attempts++
		cancel()
		return fmt.Errorf("write image: %w", syscall.ENOSPC)
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, syscall.ENOSPC) || attempts != 1 {
		t.Fatalf("delivery lost cancellation or action error: attempts=%d err=%v", attempts, err)
	}
}
