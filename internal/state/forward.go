package state

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

// There is one immutable in-flight envelope and one replaceable pending
// envelope. Retry IDs and bytes remain stable across all HTTP failures. After
// recovery we replace any backlog with a fresh full snapshot immediately.
func (t *Tracker) forward(ctx context.Context) {
	for {
		var data []byte
		select {
		case <-ctx.Done():
			return
		case data = <-t.pending:
		}
		retried := false
		backoff := 100 * time.Millisecond
		for {
			if ctx.Err() != nil {
				return
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.options.ParentURL, bytes.NewReader(data))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if t.options.ParentToken != "" {
				req.Header.Set("Authorization", "Bearer "+t.options.ParentToken)
			}
			resp, err := t.client.Do(req)
			ok := false
			if err == nil {
				ok = resp.StatusCode >= 200 && resp.StatusCode < 300
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
			}
			if ok {
				if retried {
					t.mu.Lock()
					t.publishLocked(time.Now())
					t.mu.Unlock()
				}
				break
			}
			retried = true
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}
