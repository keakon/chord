//go:build linux

package power

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/keakon/golog/log"
)

type linuxBackend struct{ reporter errorReporter }

// NewBackend constructs a backend without connecting to either bus.
func NewBackend() Backend { return &linuxBackend{} }

func (b *linuxBackend) Acquire(ctx context.Context) (Lease, error) {
	var providers []*linuxProvider
	var failures []error
	var categories []string
	for _, spec := range []struct{ name, address string }{{logindName, systemBusAddress()}, {gnomeName, sessionBusAddress()}} {
		if ctx.Err() != nil {
			break
		}
		provider := "logind"
		if spec.name == gnomeName {
			provider = "gnome"
		}
		p, err := acquireLinuxProvider(ctx, spec.name, spec.address)
		if err != nil {
			b.reporter.report(provider, "acquire", linuxCategorizedError{err})
			failures = append(failures, err)
			categories = append(categories, provider+":"+(linuxCategorizedError{err}).PowerErrorCategory())
			if errors.Is(err, ErrCleanupFailed) {
				break
			}
		} else {
			b.reporter.recovered(provider)
			providers = append(providers, p)
		}
	}
	var failure error
	if len(failures) > 0 {
		failure = linuxAcquireError{error: errors.Join(failures...), category: strings.Join(categories, ";")}
	}
	if ctx.Err() != nil || errors.Is(failure, ErrCleanupFailed) {
		for _, p := range providers {
			failure = errors.Join(failure, p.close())
		}
		return nil, errors.Join(ctx.Err(), failure)
	}
	if len(providers) == 0 {
		return nil, failure
	}
	for _, p := range providers {
		if err := p.healthy(); err != nil {
			failure = errors.Join(failure, err)
			for _, p := range providers {
				failure = errors.Join(failure, p.close())
			}
			return nil, failure
		}
	}
	if err := ctx.Err(); err != nil {
		for _, p := range providers {
			err = errors.Join(err, p.close())
		}
		return nil, err
	}
	if len(failures) > 0 {
		log.Debugf("power: partial Linux sleep prevention coverage: %v", failure)
	}
	for _, p := range providers {
		if p.name == logindName {
			log.Debug("power: acquired logind idle/block API lease")
		} else {
			log.Debug("power: acquired GNOME suspend API lease")
		}
	}
	l := &linuxLease{providers: providers, done: make(chan struct{}), stop: make(chan struct{}), monitorDone: make(chan struct{}), closeDone: make(chan struct{})}
	go l.monitor()
	return l, nil
}

type linuxLease struct {
	providers                          []*linuxProvider
	done, stop, monitorDone, closeDone chan struct{}
	mu                                 sync.Mutex
	err                                error
	doneOnce, closeOnce                sync.Once
	closeErr                           error
}

func (l *linuxLease) Done() <-chan struct{} { return l.done }
func (l *linuxLease) Err() error            { l.mu.Lock(); defer l.mu.Unlock(); return l.err }
func (l *linuxLease) finish(err error) {
	l.doneOnce.Do(func() { l.mu.Lock(); l.err = err; l.mu.Unlock(); close(l.done) })
}
func (l *linuxLease) monitor() {
	defer close(l.monitorDone)
	var disconnected [2]<-chan struct{}
	var ownerLost [2]<-chan struct{}
	for i, p := range l.providers {
		disconnected[i] = p.conn.Context().Done()
		ownerLost[i] = p.watch.lost
	}
	var err error
	select {
	case <-l.stop:
		return
	case <-disconnected[0]:
		err = fmt.Errorf("%s bus disconnected", l.providers[0].name)
	case <-ownerLost[0]:
		err = fmt.Errorf("%s owner changed", l.providers[0].name)
	case <-disconnected[1]:
		err = fmt.Errorf("%s bus disconnected", l.providers[1].name)
	case <-ownerLost[1]:
		err = fmt.Errorf("%s owner changed", l.providers[1].name)
	}
	select {
	case <-l.stop:
		return
	default:
		l.finish(err)
	}
}
func (l *linuxLease) Close() error {
	l.closeOnce.Do(func() {
		close(l.stop)
		for _, p := range l.providers {
			l.closeErr = errors.Join(l.closeErr, p.close())
		}
		<-l.monitorDone
		l.finish(l.closeErr)
		close(l.closeDone)
	})
	<-l.closeDone
	return l.closeErr
}
