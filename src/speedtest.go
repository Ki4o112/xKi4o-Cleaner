package main

// Internet speed test: download and upload speed and latency, measured
// against Cloudflare's public speed test server (speed.cloudflare.com, the
// same one its own web page uses). Several connections run at once, like
// a real download, so a fast line can be filled. Nothing is changed.

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const speedBase = "https://speed.cloudflare.com"

type speedResult struct {
	Down   float64 `json:"down"`   // Mbit/s
	Up     float64 `json:"up"`     // Mbit/s
	Ping   float64 `json:"ping"`   // ms, the median
	Jitter float64 `json:"jitter"` // ms
	// the ping while the line is full (bufferbloat): during the download and the upload
	PingDown float64 `json:"pingDown,omitempty"`
	PingUp   float64 `json:"pingUp,omitempty"`
	Grade    string  `json:"grade,omitempty"` // A..D, as the usual bufferbloat tests grade it
	Server   string  `json:"server,omitempty"`
	Error    string  `json:"error,omitempty"`
	Stop     bool    `json:"stopped,omitempty"`
	At       int64   `json:"at"`
}

var speed struct {
	busy   atomic.Bool
	mu     sync.Mutex
	cancel context.CancelFunc
}

var speedProgress = func(phase string, mbps float64, pct int) {}

type countReader struct {
	n    *atomic.Int64
	left int64
}

func (c *countReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > c.left {
		n = int(c.left)
	}
	for i := 0; i < n; i++ {
		p[i] = byte(i * 7)
	}
	c.left -= int64(n)
	c.n.Add(int64(n))
	return n, nil
}

// runSpeed measures latency, then download, then upload, against base.
func runSpeed(ctx context.Context, client *http.Client, base string, phase time.Duration) speedResult {
	res := speedResult{At: time.Now().Unix()}
	// latency: the time to an empty answer, a few times
	var lat []float64
	for i := 0; i < 8 && ctx.Err() == nil; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/__down?bytes=0", nil)
		t := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			if i == 0 {
				res.Error = err.Error()
				return res
			}
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if i == 0 { // the first one also opens the connection
			if c := resp.Header.Get("cf-meta-colo"); c != "" {
				res.Server = c
			}
			continue
		}
		lat = append(lat, float64(time.Since(t).Microseconds())/1000)
		speedProgress("ping", 0, i*100/8)
	}
	if len(lat) > 0 {
		sort.Float64s(lat)
		res.Ping = lat[len(lat)/2]
		var j float64
		for i := 1; i < len(lat); i++ {
			j += math.Abs(lat[i] - lat[i-1])
		}
		if len(lat) > 1 {
			res.Jitter = j / float64(len(lat)-1)
		}
	}
	var loaded []float64 // the pings of the phase running now
	var lmu sync.Mutex
	measure := func(name string, streams int, one func(ctx context.Context, n *atomic.Int64) error) float64 {
		var n atomic.Int64
		pctx, cancel := context.WithTimeout(ctx, phase)
		defer cancel()
		lmu.Lock()
		loaded = nil
		lmu.Unlock()
		pingDone := make(chan struct{})
		go func() { // a small request now and then, behind the full line
			defer close(pingDone)
			tk := time.NewTicker(300 * time.Millisecond)
			defer tk.Stop()
			t0 := time.Now()
			for {
				select {
				case <-pctx.Done():
					return
				case <-tk.C:
				}
				if time.Since(t0) < time.Second {
					continue
				}
				req, _ := http.NewRequestWithContext(pctx, http.MethodGet, base+"/__down?bytes=0", nil)
				s := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if pctx.Err() == nil {
					lmu.Lock()
					loaded = append(loaded, float64(time.Since(s).Microseconds())/1000)
					lmu.Unlock()
				}
			}
		}()
		defer func() { <-pingDone }()
		var wg sync.WaitGroup
		var firstErr atomic.Value
		for i := 0; i < streams; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for pctx.Err() == nil {
					if err := one(pctx, &n); err != nil && pctx.Err() == nil {
						firstErr.CompareAndSwap(nil, err)
						return
					}
				}
			}()
		}
		start := time.Now()
		// the first second is not counted: connections are still speeding up
		var base int64
		var baseAt time.Time
		tick := time.NewTicker(250 * time.Millisecond)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		var mbps float64
	loop:
		for {
			select {
			case <-done:
				break loop
			case <-tick.C:
				el := time.Since(start)
				if baseAt.IsZero() && el >= time.Second {
					base, baseAt = n.Load(), time.Now()
				}
				if !baseAt.IsZero() {
					if s := time.Since(baseAt).Seconds(); s > 0 {
						mbps = float64(n.Load()-base) * 8 / 1e6 / s
					}
				} else if s := el.Seconds(); s > 0 {
					mbps = float64(n.Load()) * 8 / 1e6 / s
				}
				speedProgress(name, mbps, int(min(99, el*100/phase)))
			}
		}
		tick.Stop()
		if mbps == 0 {
			if e, ok := firstErr.Load().(error); ok && res.Error == "" {
				res.Error = e.Error()
			}
		}
		return mbps
	}
	res.Down = measure("down", 6, func(c context.Context, n *atomic.Int64) error {
		req, _ := http.NewRequestWithContext(c, http.MethodGet, base+"/__down?bytes=50000000", nil)
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New(resp.Status)
		}
		buf := make([]byte, 64<<10)
		for {
			k, err := resp.Body.Read(buf)
			n.Add(int64(k))
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	})
	res.PingDown = median(loaded)
	if ctx.Err() == nil {
		res.Up = measure("up", 4, func(c context.Context, n *atomic.Int64) error {
			const size = 10_000_000
			req, _ := http.NewRequestWithContext(c, http.MethodPost, base+"/__up", &countReader{n: n, left: size})
			req.ContentLength = size
			req.Header.Set("Content-Type", "application/octet-stream")
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil
		})
	}
	res.PingUp = median(loaded)
	res.Grade = bloatGrade(res.Ping, res.PingDown, res.PingUp)
	res.Stop = ctx.Err() != nil
	if res.Down > 0 || res.Up > 0 {
		res.Error = ""
	}
	return res
}

func speedClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 16
	tr.DisableCompression = true // the bytes on the wire are what is measured
	return &http.Client{Transport: tr, Timeout: 0}
}

// speedTest runs the test (one at a time).
func speedTest() speedResult {
	if !speed.busy.CompareAndSwap(false, true) {
		return speedResult{Error: "busy"}
	}
	defer speed.busy.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	speed.mu.Lock()
	speed.cancel = cancel
	speed.mu.Unlock()
	defer cancel()
	return runSpeed(ctx, speedClient(), speedBase, 8*time.Second)
}

func speedStop() bool {
	speed.mu.Lock()
	defer speed.mu.Unlock()
	if speed.cancel != nil {
		speed.cancel()
	}
	return true
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2]
}

// bloatGrade: how much the ping grows when the line is full (the worse of
// download and upload): A under 30 ms, B under 60, C under 200, D above.
func bloatGrade(idle, down, up float64) string {
	if idle <= 0 || down <= 0 && up <= 0 {
		return ""
	}
	worst := math.Max(down, up) - idle
	switch {
	case worst < 30:
		return "A"
	case worst < 60:
		return "B"
	case worst < 200:
		return "C"
	}
	return "D"
}
