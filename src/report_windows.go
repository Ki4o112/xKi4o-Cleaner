//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// reportMake reads everything (in parallel, read only), writes the report to
// the Desktop and returns where it is.
func reportMake() map[string]any {
	op := beginOp("report", "", T("log.report"), nil)
	var (
		wg     sync.WaitGroup
		si     sysInfo
		dh     dhView
		drives []driveRow
		ns     netState
		su     []startupEntry
		ex     extList
	)
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }() // one part failing must not stop the report
			f()
		}()
	}
	run(func() { si = systemInfo(false) })
	run(func() { dh = diskHealthList(false) })
	run(func() { drives = getDrives() })
	run(func() { ns = getNetState() })
	run(func() { su = listStartupQuick() })
	run(func() { ex = extScan() })
	wg.Wait()
	var bh []benchResult
	if h := readBenchHistory(benchHistPath()); len(h) > 0 {
		bh = h
	}
	d := buildReport(si, dh, drives, ns, su, ex, bh)
	html := renderReport(d)
	dir := knownFolder(windows.FOLDERID_Desktop, filepath.Join(P.profile, "Desktop"))
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		dir = P.profile
	}
	path := filepath.Join(dir, reportFileName(pcName(), time.Now()))
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		op.fail("", err.Error(), nil)
		op.end("", nil)
		return map[string]any{"error": err.Error()}
	}
	op.end(T("log.reportDone", path), map[string]any{"path": path, "findings": len(d.Findings)})
	return map[string]any{"path": path, "score": d.Score, "findings": len(d.Findings)}
}

// reportOpen opens a report in the user's browser (as the user, not as
// administrator). Only this app's own report files are opened.
func reportOpen(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if !strings.HasPrefix(base, "xki4o-report-") || !strings.HasSuffix(base, ".html") || !filepath.IsAbs(path) {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	ex := filepath.Join(P.windir, "explorer.exe")
	return startAsUser(ex, `"`+ex+`" "`+path+`"`) == nil
}

func reportShow(path string) bool {
	if !strings.HasPrefix(strings.ToLower(filepath.Base(path)), "xki4o-report-") {
		return false
	}
	return showInFolder(path)
}

func buildReport(si sysInfo, dh dhView, drives []driveRow, ns netState, su []startupEntry, ex extList, bh []benchResult) reportData {
	yes, no := T("rep.yes"), T("rep.no")
	d := reportData{Title: T("rep.title"), PC: pcName(), User: userDisplayName(), App: "xKi4o Cleaner " + appVersion,
		Made: time.Now().Format("2006-01-02 15:04"), Lang: lang.Load().(string)}
	var f []repFinding
	add := func(t, s string) { f = append(f, repFinding{t, s}) }

	// system
	sys := repSection{Title: T("rep.s.system")}
	o := si.OS
	if o.Name != "" {
		sys.Rows = append(sys.Rows, repRow{K: T("rep.os"), V: strings.TrimSpace(o.Name + " " + o.Ver + " (" + o.Build + ")")})
	}
	if o.Uptime > 0 {
		r := repRow{K: T("rep.uptime"), V: fmtUptime(o.Uptime, T("rep.d"), T("rep.h"), T("rep.m"))}
		if o.Uptime > 7*86400 {
			r.Tone = "warn"
			add("warn", T("rep.f.uptime", o.Uptime/86400))
		}
		sys.Rows = append(sys.Rows, r)
	}
	for _, c := range si.CPU {
		sys.Rows = append(sys.Rows, repRow{K: T("rep.cpu"), V: fmt.Sprintf("%s · %d/%d", c.Name, c.Cores, c.Threads)})
	}
	var ram int64
	for _, m := range si.RAM {
		ram += m.Size
	}
	if ram > 0 {
		v := fmtBytes(ram << 20)
		if len(si.RAM) > 0 && si.RAM[0].Type != "" {
			v += fmt.Sprintf(" · %s %d MHz · %d×", si.RAM[0].Type, si.RAM[0].Cfg, len(si.RAM))
		}
		sys.Rows = append(sys.Rows, repRow{K: T("rep.ram"), V: v})
	}
	for _, g := range si.GPU {
		if g.Virtual {
			continue
		}
		v := g.Name
		if g.Driver != "" {
			v += " · " + g.Driver
		}
		sys.Rows = append(sys.Rows, repRow{K: T("rep.gpu"), V: v})
	}
	if b := si.Board; b.Model != "" {
		sys.Rows = append(sys.Rows, repRow{K: T("rep.board"), V: strings.TrimSpace(b.Maker + " " + b.Model)})
		if b.BIOS != "" {
			sys.Rows = append(sys.Rows, repRow{K: "BIOS", V: strings.TrimSpace(b.BIOS + " " + b.BIOSDate)})
		}
	}
	if bt := si.Battery; bt != nil && bt.Health > 0 {
		r := repRow{K: T("rep.battery"), V: fmt.Sprintf("%d%%", bt.Health)}
		if bt.Health < 70 {
			r.Tone = "warn"
			add("warn", T("rep.f.battery", bt.Health))
		}
		sys.Rows = append(sys.Rows, r)
	}
	d.Sections = append(d.Sections, sys)

	// security
	sec := repSection{Title: T("rep.s.security")}
	s := si.Security
	sb := repRow{K: "Secure Boot", V: map[string]string{"on": yes, "off": no}[s.SecureBoot]}
	switch s.SecureBoot {
	case "off":
		sb.Tone = "warn"
		add("warn", T("rep.f.secureboot"))
	case "on":
		sb.Tone = "good"
	}
	if sb.V != "" {
		sec.Rows = append(sec.Rows, sb)
	}
	tpm := repRow{K: "TPM", V: s.TPM, Tone: "good"}
	if s.TPM == "" && si.OS.Name != "" {
		tpm.V, tpm.Tone = no, "warn"
		add("warn", T("rep.f.tpm"))
	}
	if tpm.V != "" {
		sec.Rows = append(sec.Rows, tpm)
	}
	av := repRow{K: T("rep.av"), V: strings.Join(s.AV, ", "), Tone: "good"}
	if len(s.AV) == 0 && si.OS.Name != "" {
		av.V, av.Tone = T("rep.none"), "bad"
		add("bad", T("rep.f.av"))
	}
	sec.Rows = append(sec.Rows, av)
	if s.Firmware != "" {
		sec.Rows = append(sec.Rows, repRow{K: T("rep.firmware"), V: s.Firmware})
	}
	d.Sections = append(d.Sections, sec)

	// drives: free space and health
	free := repSection{Title: T("rep.s.space"), Table: &repTable{Head: []string{T("rep.drive"), T("rep.free"), T("rep.total"), T("rep.type")}}}
	for _, r := range drives {
		if r.Total <= 0 {
			continue
		}
		pct := float64(r.Free) / float64(r.Total) * 100
		t := ""
		switch {
		case pct < 10:
			t = "bad"
			add("bad", T("rep.f.space", r.Letter, pct))
		case pct < 20:
			t = "warn"
			add("warn", T("rep.f.space", r.Letter, pct))
		}
		free.Table.Rows = append(free.Table.Rows, []string{r.Letter + ":" + map[bool]string{true: " (Windows)", false: ""}[r.System],
			fmt.Sprintf("%s (%.0f%%)", fmtBytes(r.Free), pct), fmtBytes(r.Total), r.Media})
		free.Table.Tone = append(free.Table.Tone, t)
	}
	d.Sections = append(d.Sections, free)

	hl := repSection{Title: T("rep.s.health"), Table: &repTable{Head: []string{T("rep.disk"), T("rep.state"), T("rep.temp"), T("rep.wear"), T("rep.hours")}}}
	for _, x := range dh.Disks {
		st := map[string]string{"good": T("rep.ok"), "warn": T("rep.check"), "bad": T("rep.bad"), "unknown": "—"}[x.Verdict]
		t := ""
		switch x.Verdict {
		case "bad":
			t = "bad"
			add("bad", T("rep.f.diskBad", x.Name))
		case "warn":
			t = "warn"
			add("warn", T("rep.f.diskWarn", x.Name))
		}
		temp, wear, hrs := "—", "—", "—"
		if x.Temp != nil {
			temp = fmt.Sprintf("%d °C", *x.Temp)
			if *x.Temp >= 60 {
				add("warn", T("rep.f.hot", x.Name, *x.Temp))
			}
		}
		if x.Wear != nil {
			wear = fmt.Sprintf("%d%%", *x.Wear)
		}
		if x.Hours != nil {
			hrs = fmt.Sprint(*x.Hours)
		}
		name := x.Name
		if len(x.Letters) > 0 {
			name += " (" + strings.Join(x.Letters, ", ") + ")"
		}
		hl.Table.Rows = append(hl.Table.Rows, []string{name + " · " + x.Media + " " + x.Bus + " · " + fmtBytes(x.Size), st, temp, wear, hrs})
		hl.Table.Tone = append(hl.Table.Tone, t)
	}
	d.Sections = append(d.Sections, hl)

	// network
	net := repSection{Title: T("rep.s.net")}
	for _, a := range ns.Adapters {
		v := a.Desc
		if a.Speed > 0 {
			v += fmt.Sprintf(" · %d Mbit/s", a.Speed/1_000_000)
		}
		if len(a.DNS) > 0 {
			v += " · DNS " + strings.Join(a.DNS, ", ")
		}
		net.Rows = append(net.Rows, repRow{K: a.Name, V: v})
	}
	if len(net.Rows) > 0 {
		d.Sections = append(d.Sections, net)
	}

	// programs that start with Windows (read only: nothing is changed)
	start := repSection{Title: T("rep.s.startup"), Table: &repTable{Head: []string{T("rep.name"), T("rep.state")}}, Note: T("rep.startupNote")}
	on := 0
	for _, e := range su {
		state := T("rep.off")
		if e.Enabled {
			state = T("rep.on")
			on++
		}
		start.Table.Rows = append(start.Table.Rows, []string{e.Name, state})
		start.Table.Tone = append(start.Table.Tone, "")
	}
	if on > 15 {
		add("warn", T("rep.f.startup", on))
	}
	d.Sections = append(d.Sections, start)

	// browser extensions
	if len(ex.Exts) > 0 {
		bx := repSection{Title: T("rep.s.ext"), Table: &repTable{Head: []string{T("rep.name"), T("rep.browser"), T("rep.risk")}}}
		var risky []string
		for _, x := range ex.Exts {
			bx.Table.Rows = append(bx.Table.Rows, []string{x.Name + " " + x.Version, x.Browser, T("rep.risk." + x.Risk)})
			t := map[string]string{"high": "bad", "medium": "warn"}[x.Risk]
			bx.Table.Tone = append(bx.Table.Tone, t)
			if x.Risk == "high" && x.Enabled {
				risky = append(risky, x.Name)
			}
		}
		if len(risky) > 0 {
			if len(risky) > 4 {
				risky = append(risky[:4], "…")
			}
			add("warn", T("rep.f.ext", len(risky), strings.Join(risky, ", ")))
		}
		d.Sections = append(d.Sections, bx)
	}

	// the last benchmark
	if len(bh) > 0 {
		b := bh[0]
		bs := repSection{Title: T("rep.s.bench")}
		bs.Rows = append(bs.Rows, repRow{K: T("rep.bench.score"), V: fmt.Sprint(b.Score)},
			repRow{K: T("rep.bench.cpu1"), V: fmt.Sprint(b.SCPU1)}, repRow{K: T("rep.bench.cpuN"), V: fmt.Sprint(b.SCPUN)},
			repRow{K: T("rep.bench.ram"), V: fmt.Sprintf("%d · %.1f GB/s · %.0f ns", b.SRAM, b.RAMBW, b.RAMLat)})
		if b.Disk != nil && b.Disk.SeqRead > 0 {
			bs.Rows = append(bs.Rows, repRow{K: T("rep.bench.disk") + " " + b.Disk.Drive,
				V: fmt.Sprintf("%d · ↓%.0f ↑%.0f MB/s", b.SDisk, b.Disk.SeqRead, b.Disk.SeqWrit)})
		}
		bs.Note = T("rep.bench.when", b.At.Format("2006-01-02 15:04"))
		d.Sections = append(d.Sections, bs)
	}

	if len(f) == 0 {
		f = append(f, repFinding{"good", T("rep.f.none")})
	}
	sortFindings(f)
	d.Findings = f
	d.Score = reportScore(f)
	return d
}
