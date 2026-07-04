// Package client feeds the TUI with Snapshot updates from local
// collectors, remote agents, and the --demo generator.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mjacobs/llmtop/internal/collector"
	"github.com/mjacobs/llmtop/internal/model"
)

type Update struct {
	Host     string
	Snapshot *model.Snapshot
	Err      error
}

func send(ctx context.Context, ch chan<- Update, u Update) bool {
	select {
	case ch <- u:
		return true
	case <-ctx.Done():
		return false
	}
}

func StartLocal(ctx context.Context, name string, col *collector.Collector, interval time.Duration, ch chan<- Update) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			snap := col.Collect(ctx)
			if !send(ctx, ch, Update{Host: name, Snapshot: &snap}) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

const (
	backoffMin = time.Second
	backoffMax = 30 * time.Second
)

func StartRemote(ctx context.Context, name, addr, token string, interval time.Duration, ch chan<- Update) {
	base := addr
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	base = strings.TrimRight(base, "/")
	go func() {
		hc := &http.Client{Timeout: 5 * time.Second}
		backoff := backoffMin
		lastSeen := int64(0)
		needBackfill := true
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			var failed error
			if needBackfill {
				snaps, err := fetchHistory(ctx, hc, base, token, lastSeen)
				if err != nil {
					failed = err
				} else {
					for i := range snaps {
						if !send(ctx, ch, Update{Host: name, Snapshot: &snaps[i]}) {
							return
						}
						lastSeen = snaps[i].Timestamp.UnixMilli()
					}
					needBackfill = false
				}
			} else {
				snap, err := fetchSnapshot(ctx, hc, base, token)
				if err != nil {
					failed = err
					needBackfill = true // refill the gap after reconnect
				} else {
					// lastSeen only ever advances (monotonic guard): the
					// live poll can return the same snapshot backfill
					// already delivered (no new sample yet), which must
					// still be sent so the TUI keeps refreshing, but must
					// not rewind the since= cursor used on reconnect.
					if ts := snap.Timestamp.UnixMilli(); ts > lastSeen {
						lastSeen = ts
					}
					if !send(ctx, ch, Update{Host: name, Snapshot: snap}) {
						return
					}
				}
			}
			if failed != nil {
				if !send(ctx, ch, Update{Host: name, Err: failed}) {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, backoffMax)
				continue
			}
			backoff = backoffMin
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func getJSON(ctx context.Context, hc *http.Client, url, token string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return json.Unmarshal(body, v)
}

func fetchHistory(ctx context.Context, hc *http.Client, base, token string, since int64) ([]model.Snapshot, error) {
	var snaps []model.Snapshot
	err := getJSON(ctx, hc, fmt.Sprintf("%s/api/v1/history?since=%d", base, since), token, &snaps)
	return snaps, err
}

func fetchSnapshot(ctx context.Context, hc *http.Client, base, token string) (*model.Snapshot, error) {
	var snap model.Snapshot
	if err := getJSON(ctx, hc, base+"/api/v1/snapshot", token, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}
