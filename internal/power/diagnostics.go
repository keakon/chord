package power

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/keakon/golog/log"
)

// errorReporter retains only the latest category per fixed provider, never a
// history of arbitrary error strings. Backends may be called concurrently.
type errorReporter struct {
	mu   sync.Mutex
	last [3]errorReport
}
type errorReport struct {
	category string
	at       time.Time
	count    int
}

func errorCategory(err error) string {
	if categorized, ok := errors.AsType[interface {
		error
		PowerErrorCategory() string
	}](err); ok {
		return categorized.PowerErrorCategory()
	}
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return fmt.Sprintf("errno:%d", errno)
	}
	if errors.Is(err, ErrUnsupported) {
		return "unsupported"
	}
	if errors.Is(err, ErrCleanupFailed) {
		return "cleanup"
	}
	for errors.Unwrap(err) != nil {
		err = errors.Unwrap(err)
	}
	return fmt.Sprintf("%T:%v", err, err)
}
func providerIndex(provider string) int {
	switch provider {
	case "logind":
		return 1
	case "gnome":
		return 2
	default:
		return 0
	}
}
func (r *errorReporter) report(provider, operation string, err error) {
	category := operation + ":" + errorCategory(err)
	r.mu.Lock()
	entry := &r.last[providerIndex(provider)]
	entry.count++
	emit := entry.category != category || time.Since(entry.at) >= 5*time.Minute
	count := entry.count
	if emit {
		*entry = errorReport{category: category, at: time.Now()}
	}
	r.mu.Unlock()
	if emit {
		log.Warnf("power: provider=%s operation=%s failures=%d error=%v", provider, operation, count, err)
	}
}
func (r *errorReporter) recovered(provider string) {
	r.mu.Lock()
	entry := &r.last[providerIndex(provider)]
	recovered := entry.category != ""
	*entry = errorReport{}
	r.mu.Unlock()
	if recovered {
		log.Debugf("power: provider=%s recovered", provider)
	}
}
