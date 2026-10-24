//go:build windows

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// gamePing pings every region of one game 5 times, all at once (a few seconds).
func gamePing(game string) []gpResult {
	targets := gameTargets[game]
	if game == "cs2" {
		targets = sdrTargets()
	}
	out := make([]gpResult, len(targets))
	var wg sync.WaitGroup
	for i, tg := range targets {
		wg.Add(1)
		go func(i int, tg gpTarget) {
			defer wg.Done()
			defer recoverLog("game ping")
			out[i] = gpResult{Code: tg.Code, Name: tg.Name}
			out[i].Host = tg.Host
			ip := net.ParseIP(tg.Host)
			if ip == nil {
				ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", tg.Host)
				if err != nil || len(ips) == 0 {
					out[i].Error = T("gp.err.dns")
					return
				}
				ip = ips[0]
			}
			h, _, err := procIcmpCreateFile.Call()
			if h == 0 || h == uintptr(windows.InvalidHandle) {
				out[i].Error = T("np.noIcmp", err)
				return
			}
			defer procIcmpCloseHandle.Call(h) //nolint:errcheck
			rtts := make([]float64, 0, 5)
			for k := 0; k < 5; k++ {
				start := time.Now()
				rtts = append(rtts, icmpPing(h, ip, 800*time.Millisecond))
				if d := 200*time.Millisecond - time.Since(start); d > 0 {
					time.Sleep(d)
				}
			}
			out[i].pingResult = pingStats(rtts)
			out[i].Host = tg.Host
		}(i, tg)
	}
	wg.Wait()
	markBest(out)
	return out
}

func sdrTargets() []gpTarget {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sdrConfigURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "xKi4o-Cleaner/"+appVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return parseSDRConfig(b)
}
