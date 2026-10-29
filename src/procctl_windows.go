//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	procSetProcessInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetProcessInformation")
	procGetProcessInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessInformation")
	procNtSuspendProcess      = ntdll.NewProc("NtSuspendProcess")
	procNtResumeProcess       = ntdll.NewProc("NtResumeProcess")
)

const (
	processPowerThrottling = 4 // ProcessPowerThrottling
	throttleExecSpeed      = 1 // PROCESS_POWER_THROTTLING_EXECUTION_SPEED
	processSuspendResume   = 0x0800
)

type powerThrottling struct{ Version, ControlMask, StateMask uint32 }

var procPaused struct {
	sync.Mutex
	set map[uint64]bool // procKey(pid, start)
}

// procCheck: the process is still the one the page showed and the app may
// change it (the same rule as for closing it).
func procCheck(t procTarget) (rawProc, string) {
	raw, err := queryProcesses()
	if err != nil {
		return rawProc{}, err.Error()
	}
	self := windows.GetCurrentProcessId()
	var mySession uint32
	_ = windows.ProcessIdToSessionId(self, &mySession)
	for _, p := range raw {
		if p.pid != t.PID {
			continue
		}
		if startID(p.created) != t.Start {
			break
		}
		own := descendants(raw, self)
		pm.Lock()
		if !pm.sidSet {
			pm.mySID, pm.sidSet = processUserSID(self), true
		}
		inf := lookupProc(t.PID)
		pm.Unlock()
		if _, can, why := classifyProc(p, inf, self, own[t.PID], mySession, windowPIDs()[t.PID]); !can {
			return p, why
		}
		return p, ""
	}
	return rawProc{}, T("pm.gone")
}

// procIsGame: the process is a game (by the same signs as game mode).
func procIsGame(p rawProc) bool {
	path := pathFromList(p.pid)
	return gameVerdict(strings.ToLower(p.name), path, windowsKnowsGame(path), false)
}

func procEach(targets []procTarget, access uint32, f func(h windows.Handle, p rawProc) error) procCtlResult {
	var r procCtlResult
	for _, t := range targets {
		p, why := procCheck(t)
		if why == "" && procIsGame(p) {
			why = T("pm.err.game")
		}
		if why != "" {
			r.Failed++
			r.Error = why
			continue
		}
		h, err := windows.OpenProcess(access, false, t.PID)
		if err != nil {
			r.Failed++
			r.Error = T("pm.denied")
			continue
		}
		if err := f(h, p); err != nil {
			r.Failed++
			r.Error = T("pm.denied")
		} else {
			r.Done++
		}
		windows.CloseHandle(h)
	}
	return r
}

// procPriority gives the processes a priority now; remember also asks
// Windows to start the program with it every time.
func procPriority(targets []procTarget, level string, remember bool) procCtlResult {
	class, ok := prioLevels[level]
	if !ok {
		return procCtlResult{Error: T("pm.err.level")}
	}
	op := beginOp("process", level, T("log.pmPrio", T("pm.p."+level)), map[string]any{"count": len(targets), "remember": remember})
	if len(targets) > 0 {
		if p, why := procCheck(targets[0]); why == "" && procIsGame(p) {
			// a game is never opened: Windows gives the priority when it starts next
			r := procCtlResult{Done: len(targets)}
			if err := prioRemember(p.name, level); err != nil {
				r = procCtlResult{Failed: len(targets), Error: err.Error()}
			}
			opEndCtl(op, r)
			return r
		}
	}
	exe := ""
	r := procEach(targets, windows.PROCESS_SET_INFORMATION|windows.PROCESS_QUERY_LIMITED_INFORMATION, func(h windows.Handle, p rawProc) error {
		exe = p.name
		return windows.SetPriorityClass(h, class)
	})
	if remember && exe != "" && r.Done > 0 {
		if err := prioRemember(exe, level); err != nil {
			r.Error = err.Error()
		}
	}
	opEndCtl(op, r)
	return r
}

// procEco: Efficiency mode, as Task Manager does it — Windows runs the
// process slowly on the efficient cores, at the lowest priority.
func procEco(targets []procTarget, on bool) procCtlResult {
	op := beginOp("process", "", T(map[bool]string{true: "log.pmEcoOn", false: "log.pmEcoOff"}[on]), map[string]any{"count": len(targets)})
	r := procEach(targets, windows.PROCESS_SET_INFORMATION|windows.PROCESS_QUERY_LIMITED_INFORMATION, func(h windows.Handle, p rawProc) error {
		st := powerThrottling{Version: 1}
		prio := uint32(0x20)
		if on {
			st.ControlMask, st.StateMask, prio = throttleExecSpeed, throttleExecSpeed, 0x40
		}
		if r, _, err := procSetProcessInformation.Call(uintptr(h), processPowerThrottling, uintptr(unsafe.Pointer(&st)), unsafe.Sizeof(st)); r == 0 {
			return err
		}
		return windows.SetPriorityClass(h, prio)
	})
	opEndCtl(op, r)
	return r
}

// procPause stops every thread of the processes (pause) or lets them run
// again. A paused program does not answer until it is resumed.
func procPause(targets []procTarget, pause bool) procCtlResult {
	op := beginOp("process", "", T(map[bool]string{true: "log.pmPause", false: "log.pmResume"}[pause]), map[string]any{"count": len(targets)})
	r := procEach(targets, processSuspendResume|windows.PROCESS_QUERY_LIMITED_INFORMATION, func(h windows.Handle, p rawProc) error {
		call := procNtResumeProcess
		if pause {
			call = procNtSuspendProcess
		}
		if st, _, _ := call.Call(uintptr(h)); st != 0 {
			return windows.NTStatus(st)
		}
		procPaused.Lock()
		if procPaused.set == nil {
			procPaused.set = map[uint64]bool{}
		}
		k := procKey(p.pid, startID(p.created))
		if pause {
			procPaused.set[k] = true
		} else {
			delete(procPaused.set, k)
		}
		procPaused.Unlock()
		return nil
	})
	opEndCtl(op, r)
	return r
}

func isPaused(pid uint32, start int64) bool {
	procPaused.Lock()
	defer procPaused.Unlock()
	return procPaused.set[procKey(pid, start)]
}

// resumeAllPaused lets every process the app paused run again (the app ends).
func resumeAllPaused() {
	raw, err := queryProcesses()
	if err != nil {
		return
	}
	for _, p := range raw {
		if !isPaused(p.pid, startID(p.created)) {
			continue
		}
		if h, err := windows.OpenProcess(processSuspendResume, false, p.pid); err == nil {
			_, _, _ = procNtResumeProcess.Call(uintptr(h))
			windows.CloseHandle(h)
		}
	}
}

func opEndCtl(op *opHandle, r procCtlResult) {
	if r.Failed > 0 {
		op.warnf("", r.Error, nil)
	}
	op.end(T("pm.sum", r.Done, r.Done+r.Failed), nil)
}

// procCtlState: what the process has now, for the page's menu.
func procCtlState(t procTarget) procCtlInfo {
	p, why := procCheck(t)
	v := procCtlInfo{Can: why == "", Why: why}
	if p.pid == 0 {
		return v
	}
	v.Paused = isPaused(p.pid, startID(p.created))
	v.Rule = prioRuleOf(p.name)
	if v.Game = procIsGame(p); v.Game {
		v.Prio = v.Rule
		return v // not opened
	}
	if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, t.PID); err == nil {
		if c, err := windows.GetPriorityClass(h); err == nil {
			v.Prio = prioName(c)
		}
		st := powerThrottling{Version: 1}
		if r, _, _ := procGetProcessInformation.Call(uintptr(h), processPowerThrottling, uintptr(unsafe.Pointer(&st)), unsafe.Sizeof(st)); r != 0 {
			v.Eco = st.ControlMask&throttleExecSpeed != 0 && st.StateMask&throttleExecSpeed != 0
		}
		windows.CloseHandle(h)
	}
	return v
}

// ---- remembered priorities (Windows' own per-program setting)

var prioRulesMu sync.Mutex

func prioRulesPath() string { return dataDirPath("priority-rules.json") }

func prioRulesRead() []prioRule {
	var l []prioRule
	if b, err := os.ReadFile(prioRulesPath()); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	return l
}

func prioRuleOf(exe string) string {
	for _, r := range prioRulesRead() {
		if strings.EqualFold(r.Exe, exe) {
			return r.Level
		}
	}
	return ""
}

func prioRemember(exe, level string) error {
	if !app.admin {
		return errors.New(T("blocked.admin"))
	}
	prioRulesMu.Lock()
	defer prioRulesMu.Unlock()
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, ifeoKey+`\`+exe+`\PerfOptions`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	err = k.SetDWordValue("CpuPriorityClass", ifeoPrio[level])
	k.Close()
	if err != nil {
		return err
	}
	b, _ := json.Marshal(setRule(prioRulesRead(), exe, level))
	return os.WriteFile(prioRulesPath(), b, 0o644)
}

// prioForget takes back a remembered priority (and the keys the app made).
func prioForget(exe string) bool {
	if !app.admin {
		return false
	}
	prioRulesMu.Lock()
	defer prioRulesMu.Unlock()
	path := ifeoKey + `\` + exe + `\PerfOptions`
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.SET_VALUE|registry.QUERY_VALUE); err == nil {
		_ = k.DeleteValue("CpuPriorityClass")
		n, _ := k.Stat()
		k.Close()
		if n != nil && n.ValueCount == 0 && n.SubKeyCount == 0 {
			_ = registry.DeleteKey(registry.LOCAL_MACHINE, path)
		}
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, ifeoKey+`\`+exe, registry.QUERY_VALUE); err == nil {
		n, _ := k.Stat()
		k.Close()
		if n != nil && n.ValueCount == 0 && n.SubKeyCount == 0 {
			_ = registry.DeleteKey(registry.LOCAL_MACHINE, ifeoKey+`\`+exe)
		}
	}
	b, _ := json.Marshal(setRule(prioRulesRead(), exe, ""))
	return os.WriteFile(prioRulesPath(), b, 0o644) == nil
}

// prioRules: the remembered priorities, as Windows has them now.
func prioRules() []prioRule {
	out := []prioRule{}
	for _, r := range prioRulesRead() {
		if k, err := registry.OpenKey(registry.LOCAL_MACHINE, ifeoKey+`\`+r.Exe+`\PerfOptions`, registry.QUERY_VALUE); err == nil {
			if v, _, err := k.GetIntegerValue("CpuPriorityClass"); err == nil {
				r.Level = ifeoPrioName(uint32(v))
			}
			k.Close()
		}
		out = append(out, r)
	}
	return out
}
