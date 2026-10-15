//go:build windows

package main

// Process Manager: the running processes with their CPU, memory and disk
// use, read the same way Task Manager does (one NtQuerySystemInformation
// call per refresh). Ending a process asks it to close first; a forced end
// needs a second, explicit click. Windows itself, security software, this
// app, services and other users' processes can never be ended from here.

import (
	"encoding/binary"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetSystemTimes            = kernel32.NewProc("GetSystemTimes")
	procGetActiveProcessorCount   = kernel32.NewProc("GetActiveProcessorCount")
	procNtQueryInformationProcess = ntdll.NewProc("NtQueryInformationProcess")
	procGetWindow                 = user32.NewProc("GetWindow")
	procGetWindowTextLengthW      = user32.NewProc("GetWindowTextLengthW")
)

const systemProcessInformation = 5

// rawProc is one entry of SYSTEM_PROCESS_INFORMATION (64-bit layout).
type rawProc struct {
	pid, ppid      uint32
	name           string
	threads        uint32
	handles        uint32
	session        uint32
	created        int64 // FILETIME (100 ns since 1601)
	cpu            int64 // user + kernel time, 100 ns
	privateWS      uint64
	workingSet     uint64
	privateBytes   uint64
	readBytes      uint64
	writeBytes     uint64
	otherBytes     uint64
	priorityBase   int32
	pagefileUsage  uint64
	peakWorkingSet uint64
}

// parseProcInfo walks the buffer filled by NtQuerySystemInformation.
func parseProcInfo(b []byte) []rawProc {
	var out []rawProc
	le := binary.LittleEndian
	for off := 0; off+256 <= len(b); {
		e := b[off:]
		next := le.Uint32(e[0:])
		p := rawProc{
			threads:        le.Uint32(e[4:]),
			privateWS:      le.Uint64(e[8:]),
			created:        int64(le.Uint64(e[32:])),
			cpu:            int64(le.Uint64(e[40:])) + int64(le.Uint64(e[48:])),
			priorityBase:   int32(le.Uint32(e[72:])),
			pid:            uint32(le.Uint64(e[80:])),
			ppid:           uint32(le.Uint64(e[88:])),
			handles:        le.Uint32(e[96:]),
			session:        le.Uint32(e[100:]),
			peakWorkingSet: le.Uint64(e[136:]),
			workingSet:     le.Uint64(e[144:]),
			pagefileUsage:  le.Uint64(e[184:]),
			privateBytes:   le.Uint64(e[200:]),
			readBytes:      le.Uint64(e[232:]),
			writeBytes:     le.Uint64(e[240:]),
			otherBytes:     le.Uint64(e[248:]),
		}
		// ImageName: UNICODE_STRING {Length, MaximumLength, Buffer}
		if n := int(le.Uint16(e[56:])); n > 0 {
			ptr := uintptr(le.Uint64(e[64:]))
			base := uintptr(unsafe.Pointer(&b[0]))
			// the name lives inside the same buffer; never read outside it
			if ptr >= base && ptr+uintptr(n) <= base+uintptr(len(b)) {
				p.name = windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&b[ptr-base])), n/2))
			}
		}
		if p.pid == 0 && p.name == "" {
			p.name = "System Idle Process"
		}
		out = append(out, p)
		if next == 0 {
			break
		}
		off += int(next)
	}
	return out
}

func queryProcesses() ([]rawProc, error) {
	size := 512 << 10
	for i := 0; i < 8; i++ {
		buf := make([]byte, size)
		var need uint32
		st, _, _ := procNtQuerySystemInformation.Call(systemProcessInformation, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&need)))
		if st == 0 {
			return parseProcInfo(buf), nil
		}
		if st != 0xC0000004 { // STATUS_INFO_LENGTH_MISMATCH
			return nil, errors.New("NtQuerySystemInformation failed")
		}
		if int(need) > size {
			size = int(need) + 64<<10
		} else {
			size *= 2
		}
	}
	return nil, errors.New("process list too large")
}

func processorCount() int {
	if r, _, _ := procGetActiveProcessorCount.Call(0xFFFF); r > 0 {
		return int(r)
	}
	var si struct {
		_     [32]byte
		count uint32
		_     [12]byte
	}
	_, _, _ = procGetSystemInfo.Call(uintptr(unsafe.Pointer(&si)))
	if si.count > 0 {
		return int(si.count)
	}
	return 1
}

// systemTimes returns idle and busy (kernel + user - idle) CPU time.
func systemTimes() (idle, total int64, ok bool) {
	var i, k, u windows.Filetime
	r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	if r == 0 {
		return 0, 0, false
	}
	ft := func(f windows.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return ft(i), ft(k) + ft(u), true // kernel time includes idle time
}

// cpuMeter turns two readings of the system times into a usage percentage.
type cpuMeter struct {
	idle, total int64
	have        bool
}

func (m *cpuMeter) read() float64 {
	idle, total, ok := systemTimes()
	if !ok {
		return 0
	}
	defer func() { m.idle, m.total, m.have = idle, total, true }()
	if !m.have || total <= m.total {
		return 0
	}
	busy := float64((total-m.total)-(idle-m.idle)) / float64(total-m.total) * 100
	return clamp(busy, 0, 100)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------- the list

type procRow struct {
	PID      uint32  `json:"pid"`
	PPID     uint32  `json:"ppid"`
	Name     string  `json:"name"`
	Desc     string  `json:"desc,omitempty"`
	Company  string  `json:"company,omitempty"`
	Path     string  `json:"path,omitempty"`
	CPU      float64 `json:"cpu"`  // % of the whole computer
	Mem      uint64  `json:"mem"`  // private working set (what Task Manager shows)
	WS       uint64  `json:"ws"`   // whole working set
	Commit   uint64  `json:"priv"` // private bytes (commit)
	IO       uint64  `json:"io"`   // disk and other I/O, bytes per second
	Threads  uint32  `json:"threads"`
	Handles  uint32  `json:"handles"`
	Session  uint32  `json:"session"`
	Started  int64   `json:"started,omitempty"` // unix ms
	Start    int64   `json:"start"`             // start time in unix microseconds: identifies the process with its PID (exact as a JavaScript number)
	Kind     string  `json:"kind"`              // app, background, windows, service, security, self
	Window   bool    `json:"window,omitempty"`
	Closable bool    `json:"closable"`
	Why      string  `json:"why,omitempty"`
}

type procView struct {
	Rows     []procRow `json:"rows"`
	CPU      float64   `json:"cpu"`
	Cores    int       `json:"cores"`
	MemTotal uint64    `json:"memTotal"`
	MemUsed  uint64    `json:"memUsed"`
	MemLoad  uint32    `json:"memLoad"`
	Threads  uint64    `json:"threads"`
	Handles  uint64    `json:"handles"`
	Error    string    `json:"error,omitempty"`
}

type procPrev struct {
	start, cpu int64
	io         uint64
}

type procInfo struct {
	path, desc, company string
	critical            bool
	ownUser, known      bool
}

var pm = struct {
	sync.Mutex
	prev   map[uint32]procPrev
	at     time.Time
	meter  cpuMeter
	info   map[uint64]procInfo // by pid<<32 ^ start
	vers   map[string][2]string
	mySID  string
	sidSet bool
}{prev: map[uint32]procPrev{}, info: map[uint64]procInfo{}, vers: map[string][2]string{}}

func procKey(pid uint32, start int64) uint64 { return uint64(pid)<<32 ^ uint64(start) }

// startID turns a process start time (FILETIME) into unix microseconds. A
// FILETIME is larger than 2^53 and would change on its way through the
// page (JavaScript numbers); microseconds since 1970 stay exact.
func startID(created int64) int64 {
	if created <= 116444736000000000 {
		return created / 10
	}
	return (created - 116444736000000000) / 10
}

// procList returns all processes with their use since the previous call.
func procList() procView {
	pm.Lock()
	defer pm.Unlock()
	var v procView
	raw, err := queryProcesses()
	if err != nil {
		v.Error = err.Error()
		return v
	}
	now := time.Now()
	elapsed := now.Sub(pm.at)
	first := pm.at.IsZero() || elapsed <= 0
	pm.at = now
	v.Cores = processorCount()
	v.CPU = pm.meter.read()
	ms := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		v.MemTotal, v.MemUsed, v.MemLoad = ms.totalPhys, ms.totalPhys-ms.availPhys, ms.memoryLoad
	}
	if !pm.sidSet {
		pm.mySID, pm.sidSet = processUserSID(windows.GetCurrentProcessId()), true
	}
	self := windows.GetCurrentProcessId()
	var mySession uint32
	_ = windows.ProcessIdToSessionId(self, &mySession)
	own := descendants(raw, self)
	windowed := windowPIDs()
	next := make(map[uint32]procPrev, len(raw))
	seen := map[uint64]bool{}
	for _, p := range raw {
		io := p.readBytes + p.writeBytes + p.otherBytes
		r := procRow{PID: p.pid, PPID: p.ppid, Name: p.name, Threads: p.threads, Handles: p.handles, Session: p.session,
			Mem: p.privateWS, WS: p.workingSet, Commit: p.privateBytes, Start: startID(p.created), Window: windowed[p.pid]}
		if r.Mem == 0 { // older Windows and Wine do not fill it
			r.Mem = p.workingSet
		}
		if r.Commit == 0 {
			r.Commit = p.pagefileUsage // the same number on Windows; Wine fills only this one
		}
		if p.created > 0 {
			r.Started = (p.created - 116444736000000000) / 10000
		}
		if prev, ok := pm.prev[p.pid]; ok && prev.start == p.created && !first {
			secs := elapsed.Seconds()
			r.CPU = clamp(float64(p.cpu-prev.cpu)/(elapsed.Seconds()*1e7*float64(v.Cores))*100, 0, 100)
			if io >= prev.io && secs > 0 {
				r.IO = uint64(float64(io-prev.io) / secs)
			}
		}
		next[p.pid] = procPrev{p.created, p.cpu, io}
		if p.pid == 0 {
			r.Kind, r.Why = "windows", T("blk.windows")
			v.Rows = append(v.Rows, r)
			continue
		}
		key := procKey(p.pid, p.created)
		seen[key] = true
		inf, ok := pm.info[key]
		if !ok {
			inf = lookupProc(p.pid)
			pm.info[key] = inf
		}
		r.Path, r.Desc, r.Company = inf.path, inf.desc, inf.company
		v.Threads += uint64(p.threads)
		v.Handles += uint64(p.handles)
		r.Kind, r.Closable, r.Why = classifyProc(p, inf, self, own[p.pid], mySession, windowed[p.pid])
		v.Rows = append(v.Rows, r)
	}
	pm.prev = next
	for k := range pm.info { // forget processes that ended
		if !seen[k] {
			delete(pm.info, k)
		}
	}
	sort.Slice(v.Rows, func(i, j int) bool { return v.Rows[i].PID < v.Rows[j].PID })
	return v
}

// classifyProc decides what kind of process it is and whether this app may
// end it.
func classifyProc(p rawProc, inf procInfo, self uint32, ours bool, mySession uint32, window bool) (kind string, closable bool, why string) {
	n := strings.ToLower(p.name)
	switch {
	case p.pid == self || ours:
		return "self", false, T("pm.self")
	case securityProcs[n]:
		return "security", false, T("blk.security")
	case windowsProcs[n] || p.pid == 4 || inf.critical:
		return "windows", false, T("blk.windows")
	case p.session == 0:
		return "service", false, T("blk.service")
	case p.session != mySession || (inf.known && !inf.ownUser):
		return "background", false, T("blk.session")
	}
	kind = "background"
	if window {
		kind = "app"
	}
	if inf.path != "" && underWindowsDir(inf.path) && !window {
		return "windows", false, T("blk.windows") // a background part of Windows
	}
	return kind, true, ""
}

func underWindowsDir(p string) bool {
	root := strings.ToLower(windowsDir()) + `\`
	return strings.HasPrefix(strings.ToLower(p), root)
}

func windowsDir() string {
	if d, err := windows.GetWindowsDirectory(); err == nil {
		return d
	}
	return `C:\Windows`
}

// descendants: this app's own child processes (WebView2) and theirs.
func descendants(raw []rawProc, root uint32) map[uint32]bool {
	kids := map[uint32][]rawProc{}
	for _, p := range raw {
		kids[p.ppid] = append(kids[p.ppid], p)
	}
	byPID := map[uint32]rawProc{}
	for _, p := range raw {
		byPID[p.pid] = p
	}
	out := map[uint32]bool{}
	var walk func(pid uint32, created int64)
	walk = func(pid uint32, created int64) {
		for _, k := range kids[pid] {
			// a child is younger than its parent (guards against a reused PID)
			if out[k.pid] || k.pid == pid || k.created < created {
				continue
			}
			out[k.pid] = true
			walk(k.pid, k.created)
		}
	}
	if me, ok := byPID[root]; ok {
		walk(root, me.created)
	}
	return out
}

func lookupProc(pid uint32) procInfo {
	var inf procInfo
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return inf
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
		inf.path = windows.UTF16ToString(buf[:n])
	}
	var crit uint32
	if st, _, _ := procNtQueryInformationProcess.Call(uintptr(h), 29, uintptr(unsafe.Pointer(&crit)), 4, 0); st == 0 { // ProcessBreakOnTermination
		inf.critical = crit != 0
	}
	if sid := tokenSID(h); sid != "" {
		inf.known = true
		inf.ownUser = sid == pm.mySID
	}
	if inf.path != "" {
		v, ok := pm.vers[strings.ToLower(inf.path)]
		if !ok {
			m := fileVersionStrings(inf.path, "FileDescription", "CompanyName")
			v = [2]string{m["FileDescription"], m["CompanyName"]}
			if len(pm.vers) > 2000 {
				pm.vers = map[string][2]string{}
			}
			pm.vers[strings.ToLower(inf.path)] = v
		}
		inf.desc, inf.company = v[0], v[1]
	}
	return inf
}

func tokenSID(h windows.Handle) string {
	var tok windows.Token
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok) != nil {
		return ""
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return ""
	}
	return u.User.Sid.String()
}

func processUserSID(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	return tokenSID(h)
}

var (
	winMu   sync.Mutex
	winPIDs map[uint32]bool
)

var appWindowCB = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
	if !windows.IsWindowVisible(hwnd) {
		return 1
	}
	if owner, _, _ := procGetWindow.Call(uintptr(hwnd), 4); owner != 0 { // GW_OWNER: a dialog of another window
		return 1
	}
	if n, _, _ := procGetWindowTextLengthW.Call(uintptr(hwnd)); n == 0 {
		return 1
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err == nil {
		winPIDs[pid] = true
	}
	return 1
})

// windowPIDs: processes that show a normal window (an "app" in Task Manager).
func windowPIDs() map[uint32]bool {
	winMu.Lock()
	defer winMu.Unlock()
	winPIDs = map[uint32]bool{}
	_ = windows.EnumWindows(appWindowCB, nil)
	out := winPIDs
	winPIDs = nil
	return out
}

// ---------------------------------------------------------------- ending

type procTarget struct {
	PID   uint32 `json:"pid"`
	Start int64  `json:"start"`
}

type procEndResult struct {
	PID    uint32 `json:"pid"`
	Name   string `json:"name"`
	Ended  bool   `json:"ended"`
	Error  string `json:"error,omitempty"`
	Forced bool   `json:"forced,omitempty"`
}

// procEnd ends processes. Without force it only asks them to close (like
// clicking X) and waits; force ends them at once (unsaved work is lost - the
// page asks first). Every target is checked again right before.
func procEnd(targets []procTarget, force bool) []procEndResult {
	title := T("log.pmEnd")
	if force {
		title = T("log.pmForce")
	}
	op := beginOp("process", "", title, map[string]any{"count": len(targets), "force": force})
	raw, err := queryProcesses()
	if err != nil {
		op.fail("", err.Error(), nil)
		op.end("", nil)
		return nil
	}
	byPID := map[uint32]rawProc{}
	for _, p := range raw {
		byPID[p.pid] = p
	}
	self := windows.GetCurrentProcessId()
	var mySession uint32
	_ = windows.ProcessIdToSessionId(self, &mySession)
	own := descendants(raw, self)
	windowed := windowPIDs()
	pm.Lock()
	if !pm.sidSet {
		pm.mySID, pm.sidSet = processUserSID(self), true
	}
	pm.Unlock()

	var res []procEndResult
	ok := map[uint32]string{}
	for _, t := range targets {
		p, found := byPID[t.PID]
		r := procEndResult{PID: t.PID, Name: p.name, Forced: force}
		switch {
		case !found || startID(p.created) != t.Start:
			r.Ended, r.Error = true, T("pm.gone") // already closed (or the PID now belongs to another process)
			r.Name = ""
		default:
			pm.Lock()
			inf := lookupProc(t.PID)
			pm.Unlock()
			if _, can, why := classifyProc(p, inf, self, own[t.PID], mySession, windowed[t.PID]); !can {
				r.Error = why
			} else {
				ok[t.PID] = strings.ToLower(p.name)
			}
		}
		res = append(res, r)
	}
	if len(ok) > 0 {
		left := endPIDs(ok, byPID, force, 6*time.Second)
		for i := range res {
			pid := res[i].PID
			if _, asked := ok[pid]; !asked {
				continue
			}
			if left[pid] {
				if force {
					res[i].Error = T("pm.denied")
				} else if windowed[pid] {
					res[i].Error = T("pm.stillOpen")
				} else {
					res[i].Error = T("pm.noWindow")
				}
			} else {
				res[i].Ended = true
			}
		}
	}
	n := 0
	for _, r := range res {
		if r.Ended && r.Error == "" {
			n++
			op.info(r.Name, T("pm.ended"), map[string]any{"pid": r.PID})
		} else if r.Error != "" && !r.Ended {
			op.warnf(r.Name, r.Error, map[string]any{"pid": r.PID})
		}
	}
	op.end(T("pm.sum", n, len(targets)), nil)
	return res
}

// endPIDs asks the processes to close (or, with force, ends them) and
// returns the ones still running afterwards.
func endPIDs(pids map[uint32]string, before map[uint32]rawProc, force bool, wait time.Duration) map[uint32]bool {
	if force {
		for pid := range pids {
			if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid); err == nil {
				_ = windows.TerminateProcess(h, 1)
				_, _ = windows.WaitForSingleObject(h, 3000)
				windows.CloseHandle(h)
			}
		}
	} else {
		postClose(pids)
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) && len(stillRunning(pids, before)) > 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	return stillRunning(pids, before)
}

// stillRunning: which of the PIDs still run as the same process.
func stillRunning(pids map[uint32]string, before map[uint32]rawProc) map[uint32]bool {
	out := map[uint32]bool{}
	raw, err := queryProcesses()
	if err != nil {
		for p := range pids {
			out[p] = true
		}
		return out
	}
	for _, p := range raw {
		if _, ok := pids[p.pid]; ok && before[p.pid].created == p.created {
			out[p.pid] = true
		}
	}
	return out
}

// procOpenLocation shows the program file of a process in Explorer.
func procOpenLocation(pid uint32) bool {
	p := processPath(pid)
	if p == "" {
		return false
	}
	return showInFolder(p)
}

// ---------------------------------------------------------------- vitals

var vit = struct {
	sync.Mutex
	meter cpuMeter
}{}

type vitals struct {
	CPU      float64 `json:"cpu"`
	MemLoad  uint32  `json:"mem"`
	MemUsed  uint64  `json:"memUsed"`
	MemTotal uint64  `json:"memTotal"`
}

// sysVitals is the small live readout in the sidebar.
func sysVitals() vitals {
	vit.Lock()
	defer vit.Unlock()
	v := vitals{CPU: vit.meter.read()}
	ms := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		v.MemLoad, v.MemUsed, v.MemTotal = ms.memoryLoad, ms.totalPhys-ms.availPhys, ms.totalPhys
	}
	return v
}
