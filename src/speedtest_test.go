package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpeedAgainstLocalServer(t *testing.T) {
	var up atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__down":
			n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
			w.Header().Set("cf-meta-colo", "SOF")
			buf := make([]byte, 32<<10)
			for n > 0 {
				k := min(n, len(buf))
				if _, err := w.Write(buf[:k]); err != nil {
					return
				}
				n -= k
			}
		case "/__up":
			k, _ := io.Copy(io.Discard, r.Body)
			up.Add(k)
		}
	}))
	defer srv.Close()
	r := runSpeed(context.Background(), srv.Client(), srv.URL, 1500*time.Millisecond)
	if r.Error != "" || r.Down <= 0 || r.Up <= 0 || r.Ping <= 0 || r.Server != "SOF" {
		t.Fatalf("%+v", r)
	}
	t.Logf("local: down %.0f up %.0f Mbit/s ping %.2f ms", r.Down, r.Up, r.Ping)
	// cancelled: stops at once and says so
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := runSpeed(ctx, srv.Client(), srv.URL, time.Second); r.Error == "" && !r.Stop {
		t.Fatalf("cancelled: %+v", r)
	}
	// no server: an error, not a hang
	srv.Close()
	if r := runSpeed(context.Background(), &http.Client{Timeout: 2 * time.Second}, srv.URL, time.Second); r.Error == "" {
		t.Fatal("expected an error")
	}
}

func TestReportRender(t *testing.T) {
	d := reportData{Title: "Report", PC: `PC<script>`, Made: "2026-10-19", App: "x", Lang: "en",
		Findings: []repFinding{{"warn", "a"}, {"bad", "b"}},
		Sections: []repSection{{Title: "S", Rows: []repRow{{K: "k", V: `<b>v</b>`, Tone: "good"}},
			Table: &repTable{Head: []string{"h"}, Rows: [][]string{{"r&1"}}, Tone: []string{"bad"}}}}}
	sortFindings(d.Findings)
	d.Score = reportScore(d.Findings)
	h := renderReport(d)
	for _, want := range []string{"PC&lt;script&gt;", "&lt;b&gt;v&lt;/b&gt;", "r&amp;1", `<tr class="bad">`, ">79<"} {
		if !contains(h, want) {
			t.Errorf("missing %q", want)
		}
	}
	if contains(h, "<script") || contains(h, "http://") || d.Findings[0].Tone != "bad" {
		t.Error("the report must have no scripts or outside links, worst first")
	}
	if n := reportFileName(`My PC/1`, time.Date(2026, 10, 19, 9, 5, 0, 0, time.UTC)); n != "xKi4o-report-My-PC-1-2026-10-19_0905.html" {
		t.Error(n)
	}
	if fmtBytes(1536<<20) != "1.5 GB" || fmtUptime(90061, "d", "h", "m") != "1 d 1 h" {
		t.Error(fmtBytes(1536<<20), fmtUptime(90061, "d", "h", "m"))
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
