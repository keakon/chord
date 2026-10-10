package imagegen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/keakon/golog/log"
)

const deliveryAttempts = 3

const deliveryContextFailureFormat = "%s delivery failed: %w; context ended: %w"

type downloadHTTPError struct {
	status     int
	retryAfter time.Duration
}

func (e *downloadHTTPError) Error() string { return fmt.Sprintf("image download HTTP %d", e.status) }

// RetryDelivery retries idempotent image delivery only. Paid generation must
// never use it. The caller's deadline and cancellation bound every wait.
func RetryDelivery(ctx context.Context, stage string, action func() error) error {
	return retryDelivery(ctx, stage, 250*time.Millisecond, action)
}

func retryDelivery(ctx context.Context, stage string, delay time.Duration, action func() error) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := action()
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf(deliveryContextFailureFormat, stage, err, ctxErr)
		}
		if attempt == deliveryAttempts || !retryableDeliveryError(err) {
			return err
		}
		wait := delay << (attempt - 1)
		if httpErr, ok := errors.AsType[*downloadHTTPError](err); ok {
			wait = max(wait, httpErr.retryAfter)
		}
		// Do not retry earlier than a server's advice or wait beyond this call.
		if deadline, ok := ctx.Deadline(); ok && wait >= time.Until(deadline) {
			return err
		}
		if wait > 30*time.Second {
			return err
		}
		log.Warnf("image delivery retry stage=%v attempt=%v wait_ms=%v", stage, attempt, wait.Milliseconds())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf(deliveryContextFailureFormat, stage, err, ctx.Err())
		case <-timer.C:
		}
	}
}

func retryableDeliveryError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if httpErr, ok := errors.AsType[*downloadHTTPError](err); ok {
		return httpErr.status == 408 || httpErr.status == 429 || httpErr.status == 500 || httpErr.status == 502 || httpErr.status == 503 || httpErr.status == 504
	}
	if network, ok := errors.AsType[net.Error](err); ok && network.Timeout() {
		return true
	}
	if dns, ok := errors.AsType[*net.DNSError](err); ok && dns.IsTemporary {
		return true
	}
	for _, transient := range []error{io.EOF, io.ErrUnexpectedEOF, io.ErrShortWrite, syscall.EINTR, syscall.EAGAIN, syscall.EBUSY, syscall.ETIMEDOUT, syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE, syscall.ENETUNREACH, syscall.EHOSTUNREACH} {
		if errors.Is(err, transient) {
			return true
		}
	}
	return false
}
