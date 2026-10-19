package main

// PC report: one self-contained HTML page with what matters about this PC
// (system, drives and their health, free space, network, programs that
// start with Windows, browser extensions, the last benchmark) and a short
// list of things worth a look. It only reads; it is saved where the user
// can find it (the Desktop) and opened in the browser.

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
)

type repRow struct {
	K, V string
	Tone string // "", good, warn, bad
}

type repSection struct {
	Title string
	Icon  string // a small inline glyph
	Rows  []repRow
	Table *repTable
	Note  string
}

type repTable struct {
	Head []string
	Rows [][]string
	Tone []string // per row
}

type repFinding struct {
	Tone, Text string // warn, bad, good
}

type reportData struct {
	Title, PC, User, Made, App string
	Score                      int // 0-100 when known
	Findings                   []repFinding
	Sections                   []repSection
	Lang                       string
}

func esc(s string) string { return html.EscapeString(s) }

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func fmtUptime(s int64, days, hours, mins string) string {
	d, h, m := s/86400, s%86400/3600, s%3600/60
	switch {
	case d > 0:
		return fmt.Sprintf("%d %s %d %s", d, days, h, hours)
	case h > 0:
		return fmt.Sprintf("%d %s %d %s", h, hours, m, mins)
	}
	return fmt.Sprintf("%d %s", m, mins)
}

// renderReport makes the HTML page. Everything that comes from the PC is
// escaped; the page has no scripts and loads nothing from the internet.
func renderReport(d reportData) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s) }
	tone := func(t string) string {
		if t == "" {
			return ""
		}
		return ` class="` + esc(t) + `"`
	}
	w(`<!doctype html><html lang="` + esc(d.Lang) + `"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	w(`<title>` + esc(d.Title+" — "+d.PC) + `</title><style>` + reportCSS + `</style></head><body><main>`)
	w(`<header><div class="brand"><span class="logo">x</span><span>xKi4o Cleaner</span></div><h1>` + esc(d.Title) + `</h1>`)
	w(`<p class="meta">` + esc(d.PC))
	if d.User != "" {
		w(` · ` + esc(d.User))
	}
	w(` · ` + esc(d.Made) + ` · ` + esc(d.App) + `</p>`)
	if d.Score > 0 {
		t := "good"
		if d.Score < 60 {
			t = "bad"
		} else if d.Score < 85 {
			t = "warn"
		}
		w(`<div class="score ` + t + `"><b>` + fmt.Sprint(d.Score) + `</b><span>/100</span></div>`)
	}
	w(`</header>`)
	if len(d.Findings) > 0 {
		w(`<section class="find"><ul>`)
		for _, f := range d.Findings {
			w(`<li` + tone(f.Tone) + `>` + esc(f.Text) + `</li>`)
		}
		w(`</ul></section>`)
	}
	for _, s := range d.Sections {
		w(`<section><h2>` + esc(s.Title) + `</h2>`)
		if len(s.Rows) > 0 {
			w(`<dl>`)
			for _, r := range s.Rows {
				w(`<div><dt>` + esc(r.K) + `</dt><dd` + tone(r.Tone) + `>` + esc(r.V) + `</dd></div>`)
			}
			w(`</dl>`)
		}
		if t := s.Table; t != nil && len(t.Rows) > 0 {
			w(`<div class="tw"><table><thead><tr>`)
			for _, h := range t.Head {
				w(`<th>` + esc(h) + `</th>`)
			}
			w(`</tr></thead><tbody>`)
			for i, r := range t.Rows {
				tr := ""
				if i < len(t.Tone) {
					tr = t.Tone[i]
				}
				w(`<tr` + tone(tr) + `>`)
				for _, c := range r {
					w(`<td>` + esc(c) + `</td>`)
				}
				w(`</tr>`)
			}
			w(`</tbody></table></div>`)
		}
		if s.Note != "" {
			w(`<p class="note">` + esc(s.Note) + `</p>`)
		}
		w(`</section>`)
	}
	w(`<footer>xKi4o Cleaner · github.com/Ki4o112/xKi4o-Cleaner</footer></main></body></html>`)
	return b.String()
}

// reportScore: 100 minus the findings (a bad one counts more).
func reportScore(f []repFinding) int {
	s := 100
	for _, x := range f {
		switch x.Tone {
		case "bad":
			s -= 15
		case "warn":
			s -= 6
		}
	}
	return max(5, s)
}

func sortFindings(f []repFinding) {
	rank := map[string]int{"bad": 0, "warn": 1, "good": 2}
	sort.SliceStable(f, func(i, j int) bool { return rank[f[i].Tone] < rank[f[j].Tone] })
}

func reportFileName(pc string, at time.Time) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, pc)
	if clean == "" {
		clean = "PC"
	}
	return "xKi4o-report-" + clean + "-" + at.Format("2006-01-02_1504") + ".html"
}

const reportCSS = `:root{--bg:#0b0f19;--card:#121826;--line:#232c3f;--text:#e8edf7;--muted:#93a0b8;--good:#34d399;--warn:#fbbf24;--bad:#f87171;--acc:#5a8cff}
@media (prefers-color-scheme:light){:root{--bg:#f4f6fb;--card:#fff;--line:#e2e7f0;--text:#141a26;--muted:#5b6678;--good:#0c9364;--warn:#b7791f;--bad:#c53030;--acc:#2f6bff}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 "Segoe UI",system-ui,sans-serif}
main{max-width:980px;margin:0 auto;padding:32px 20px 48px}header{position:relative;margin-bottom:22px}
.brand{display:flex;gap:10px;align-items:center;font-weight:700;color:var(--muted)}.logo{display:inline-grid;place-items:center;width:28px;height:28px;border-radius:8px;background:var(--acc);color:#fff;font-weight:800}
h1{font-size:28px;margin:14px 0 4px;letter-spacing:-.02em}.meta{color:var(--muted);margin:0}
.score{position:absolute;right:0;top:0;padding:12px 18px;border-radius:16px;background:var(--card);border:1px solid var(--line);text-align:right}
.score b{font-size:34px}.score span{color:var(--muted)}.score.good b{color:var(--good)}.score.warn b{color:var(--warn)}.score.bad b{color:var(--bad)}
section{background:var(--card);border:1px solid var(--line);border-radius:18px;padding:18px 20px;margin:14px 0}
h2{font-size:16px;margin:0 0 12px}dl{display:grid;grid-template-columns:repeat(auto-fill,minmax(260px,1fr));gap:8px 24px;margin:0}
dl div{display:flex;justify-content:space-between;gap:12px;border-bottom:1px dashed var(--line);padding:5px 0}dt{color:var(--muted)}dd{margin:0;text-align:right;font-weight:600}
.good{color:var(--good)}.warn{color:var(--warn)}.bad{color:var(--bad)}
.find ul{margin:0;padding:0;list-style:none}.find li{padding:8px 12px;border-radius:10px;margin:4px 0;background:rgba(127,127,127,.08);color:var(--text)}
.find li.bad{border-left:3px solid var(--bad)}.find li.warn{border-left:3px solid var(--warn)}.find li.good{border-left:3px solid var(--good)}
.tw{overflow-x:auto}table{width:100%;border-collapse:collapse;font-size:13px}th{text-align:left;color:var(--muted);font-weight:600;border-bottom:1px solid var(--line);padding:6px 8px}
td{padding:6px 8px;border-bottom:1px solid var(--line)}tr.bad td:first-child{color:var(--bad)}tr.warn td:first-child{color:var(--warn)}
.note{color:var(--muted);font-size:12.5px;margin:10px 0 0}footer{color:var(--muted);text-align:center;font-size:12px;margin-top:24px}
@media (max-width:640px){.score{position:static;display:inline-block;margin-top:12px}}
@media print{body{background:#fff;color:#000}section{break-inside:avoid}}`
