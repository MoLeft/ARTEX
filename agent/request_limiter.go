package agent

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"
)

// RequestLimiter counts HTTP attempts in rolling second/minute windows. Keep it
// outside provider caches so rebuilding a provider does not reset its quota.
// Its zero value is ready for use; limits are set by NewProviderWithLimiter.
type RequestLimiter struct {
	mu          sync.Mutex
	windows     [2]requestWindow
	sent        []time.Time
	changed     chan struct{}
	maxInFlight int
	inFlight    int
}

type requestWindow struct {
	period time.Duration
	limit  float64
}

func (l *RequestLimiter) configure(perSecond, perMinute float64, maxInFlight int) error {
	if maxInFlight < 0 {
		return fmt.Errorf("LLM max concurrency must be non-negative")
	}
	var windows [2]requestWindow
	for i, rate := range [2]float64{perSecond, perMinute} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) {
			return fmt.Errorf("LLM request rate must be finite")
		}
		if rate <= 0 {
			continue
		}
		period := [2]time.Duration{time.Second, time.Minute}[i]
		// Whole HTTP attempts cannot exceed a fractional window capacity.
		limit := math.Floor(rate)
		if rate < 1 {
			// Preserve fractional rates such as 0.5/s as one attempt per 2s.
			d := math.Ceil(float64(period) / rate)
			if d >= float64(math.MaxInt64) {
				return fmt.Errorf("LLM request rate is too small")
			}
			period, limit = time.Duration(d), 1
		}
		windows[i] = requestWindow{period: period, limit: limit}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.windows = windows
	l.maxInFlight = maxInFlight
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
	return nil
}

// wait counts a request only when it may actually proceed. Waiters recheck both
// windows under the same lock, so concurrency and cancellation cannot overbook
// capacity. Requests in (now-period, now] count toward each rolling window.
func (l *RequestLimiter) wait(ctx context.Context) error {
	return l.waitSlot(ctx, false)
}

func (l *RequestLimiter) waitSlot(ctx context.Context, slot bool) error {
	if l == nil {
		return ctx.Err()
	}
	for {
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return err
		}
		now := time.Now()
		// Retain at least a minute across limit changes, including while limits
		// are disabled. Fractional rates may need a longer history.
		retention := time.Minute
		for _, w := range l.windows {
			if w.period > retention {
				retention = w.period
			}
		}
		first := 0
		// Keep the last attempt even when older than retention: a subsequent
		// switch to a fractional rate may need to space requests further apart.
		for first+1 < len(l.sent) && !l.sent[first].After(now.Add(-retention)) {
			first++
		}
		l.sent = l.sent[first:]
		var delay time.Duration
		for _, w := range l.windows {
			if w.limit == 0 {
				continue
			}
			start := len(l.sent)
			for start > 0 && l.sent[start-1].After(now.Add(-w.period)) {
				start--
			}
			count := len(l.sent) - start
			if float64(count) >= w.limit {
				// If a limit was lowered, enough old requests must expire to
				// leave room for this attempt, not just the oldest request.
				d := l.sent[len(l.sent)-int(w.limit)].Add(w.period).Sub(now)
				if d > delay {
					delay = d
				}
			}
		}
		full := slot && l.maxInFlight > 0 && l.inFlight >= l.maxInFlight
		if delay <= 0 && !full {
			l.sent = append(l.sent, now)
			if slot {
				l.inFlight++
			}
			l.mu.Unlock()
			return nil
		}
		changed := l.changed
		l.mu.Unlock()
		var timer *time.Timer
		var timerC <-chan time.Time
		if delay > 0 {
			timer = time.NewTimer(delay)
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return ctx.Err()
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
		case <-timerC:
		}
	}
}

func (l *RequestLimiter) release() {
	l.mu.Lock()
	l.inFlight--
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
	l.mu.Unlock()
}

type rateLimitedTransport struct {
	base    http.RoundTripper
	limiter *RequestLimiter
}

func (t rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.limiter.waitSlot(req.Context(), true); err != nil {
		return nil, err
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		t.limiter.release()
		return resp, err
	}
	body := &limitedBody{ReadCloser: resp.Body, releaseSlot: t.limiter.release, done: make(chan struct{})}
	resp.Body = body
	go func() {
		select {
		case <-req.Context().Done():
			body.release()
		case <-body.done:
		}
	}()
	return resp, nil
}

type limitedBody struct {
	io.ReadCloser
	releaseSlot func()
	once        sync.Once
	done        chan struct{}
}

func (b *limitedBody) release() {
	b.once.Do(func() { b.releaseSlot(); close(b.done) })
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.release()
	}
	return n, err
}

func (b *limitedBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}
