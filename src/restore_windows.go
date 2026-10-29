//go:build windows

package main

// Restore Center: everything this app can put back, in one place - restore
// points of Windows, the saved original state of each tweak, startup
// changes, and the files the app moved to the Recycle Bin.

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type rcTweak struct {
	ID string `json:"id"`
	At int64  `json:"at"` // when its original state was saved (unix seconds)
}

type rcOverview struct {
	Tweaks  []rcTweak       `json:"tweaks"`
	Startup []*startupLog   `json:"startup"`
	Files   []recycledEntry `json:"files"`
	Undo    *histItem       `json:"undo,omitempty"`
	Admin   bool            `json:"admin"`
}

func restoreOverview() rcOverview {
	v := rcOverview{Admin: app.admin}
	for _, id := range appliedTweaks() {
		var at int64
		if b := getBackup(id); b != nil {
			at = b.At
		}
		v.Tweaks = append(v.Tweaks, rcTweak{ID: id, At: at})
	}
	sort.Slice(v.Tweaks, func(i, j int) bool { return v.Tweaks[i].At > v.Tweaks[j].At })
	// only the latest change of each entry can be put back (an older one
	// would undo the changes made after it)
	seen := map[string]bool{}
	for _, e := range startupLogList() {
		if seen[e.Key] {
			continue
		}
		seen[e.Key] = true
		if e.OK && e.Backup != nil && !e.Restored && e.Action != "restore" {
			v.Startup = append(v.Startup, e)
		}
	}
	v.Files = recycledList(recycleBinOf)
	if h := tweakHistory(); h.Undo != nil {
		v.Undo = h.Undo
	}
	return v
}

const rpListScript = `try { $r=@(Get-ComputerRestorePoint -ErrorAction Stop | ForEach-Object { [pscustomobject]@{seq=[int]$_.SequenceNumber; desc=[string]$_.Description; type=[int]$_.RestorePointType; time=[Management.ManagementDateTimeConverter]::ToDateTime($_.CreationTime).ToUniversalTime().ToString('o')} }); ConvertTo-Json -InputObject $r -Compress -Depth 2 } catch { 'ERR:' + $_.Exception.Message }`

// restorePointList lists Windows' restore points, newest first.
func restorePointList() rpList {
	if !app.admin {
		return rpList{Error: T("blocked.admin")}
	}
	out, _, err := runPS(time.Minute, rpListScript)
	out = strings.TrimSpace(out)
	if err != nil {
		return rpList{Error: err.Error()}
	}
	if strings.HasPrefix(out, "ERR:") {
		return rpList{Error: strings.TrimSpace(out[4:])}
	}
	var l rpList
	if out != "" && json.Unmarshal([]byte(out), &l.Points) != nil {
		return rpList{Error: T("rc.err.read")}
	}
	sort.Slice(l.Points, func(i, j int) bool { return l.Points[i].Seq > l.Points[j].Seq })
	return l
}

var procSRRemoveRestorePoint = srclient.NewProc("SRRemoveRestorePoint")

// srRemove deletes one restore point of Windows by its sequence number.
func srRemove(seq int) error {
	if err := procSRRemoveRestorePoint.Find(); err != nil {
		return err
	}
	if r, _, _ := procSRRemoveRestorePoint.Call(uintptr(uint32(seq))); r != 0 {
		return windows.Errno(r)
	}
	return nil
}

// deleteRestorePoints is the Restore Center's red button: it removes
// Windows' restore points (all of them, or all but the newest) so they do
// not pile up. Only on the user's explicit choice; nothing else is touched.
func deleteRestorePoints(keepNewest bool) rpDelResult {
	if !app.admin {
		return rpDelResult{Error: T("blocked.admin")}
	}
	op := beginOp("restore", "", T("log.rpDel"), map[string]any{"keepNewest": keepNewest})
	l := restorePointList()
	if l.Error != "" {
		op.fail("", l.Error, nil)
		op.end("", nil)
		return rpDelResult{Error: l.Error}
	}
	app.working.Store(true)
	defer app.working.Store(false)
	res := removeRestorePoints(l.Points, keepNewest, srRemove, op)
	op.end(T("log.rpDelSum", res.Deleted, res.Total), map[string]any{"deleted": res.Deleted, "failed": res.Failed, "kept": res.Kept})
	return res
}

// trimOwnRestorePoints: after the app makes a restore point, only its newest
// three stay (unless switched off in Settings). Points Windows, drivers or
// other programs made are never touched.
func trimOwnRestorePoints() {
	if v, ok := getSettings()["rpKeep"].(bool); ok && !v {
		return
	}
	l := restorePointList()
	if l.Error != "" {
		return
	}
	plan := rpTrimPlan(l.Points, 3)
	if len(plan) == 0 {
		return
	}
	op := beginOp("restore", "", T("log.rpTrim"), map[string]any{"keep": 3, "remove": len(plan)})
	n := 0
	for _, p := range plan {
		if err := srRemove(p.Seq); err != nil {
			op.fail(rpLabel(p), err.Error(), map[string]any{"seq": p.Seq})
			continue
		}
		n++
		op.info(rpLabel(p), T("log.rpDeleted"), map[string]any{"seq": p.Seq})
	}
	op.end(T("log.rpTrimSum", n), map[string]any{"removed": n})
}

type rcResult struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// restoreFiles puts files the app recycled back where they were.
func restoreFiles(ids []int64) []rcResult {
	op := beginOp("restore", "", T("log.rcFiles"), map[string]any{"count": len(ids)})
	byID := map[int64]recycledEntry{}
	for _, e := range recycledList(recycleBinOf) {
		byID[e.ID] = e
	}
	var out []rcResult
	n := 0
	for _, id := range ids {
		r := rcResult{ID: id}
		err := restoreRecycled(id, recycleBinOf)
		path := byID[id].Path
		if err != nil {
			r.Error = trKey(err.Error())
			op.warnf(path, r.Error, nil)
		} else {
			r.OK = true
			n++
			op.info(path, T("log.restored"), nil)
			shellNotifyCreated(path)
		}
		out = append(out, r)
	}
	op.end(T("log.rcSum", n, len(ids)), nil)
	return out
}

func openRecycleBin() bool {
	shellOpen("shell:RecycleBinFolder")
	return true
}

var procSHChangeNotify = shell32.NewProc("SHChangeNotify")

// shellNotifyCreated tells Explorer that an item appeared again (open
// windows and the Recycle Bin view refresh).
func shellNotifyCreated(path string) {
	const shcneCreate, shcneUpdateDir, shcnfPathW = 0x2, 0x1000, 0x5
	if p, err := windows.UTF16PtrFromString(path); err == nil {
		_, _, _ = procSHChangeNotify.Call(shcneCreate, shcnfPathW, uintptr(unsafe.Pointer(p)), 0)
	}
	if b, err := windows.UTF16PtrFromString(recycleBinOf(path)); err == nil {
		_, _, _ = procSHChangeNotify.Call(shcneUpdateDir, shcnfPathW, uintptr(unsafe.Pointer(b)), 0)
	}
}

// restoreToPoint asks Windows to bring the system back to a restore point
// (the same as System Restore does) and restarts the PC, where Windows does
// the work. Personal files stay as they are. "" = started.
func restoreToPoint(seq int) string {
	if !app.admin {
		return T("blocked.admin")
	}
	var p *restorePoint
	l := restorePointList()
	for i := range l.Points {
		if l.Points[i].Seq == seq {
			p = &l.Points[i]
		}
	}
	if p == nil {
		return T("rc.err.noPoint")
	}
	op := beginOp("restore", p.Desc, T("log.rpRestore", p.Desc), map[string]any{"seq": seq, "time": p.Time})
	out, _, err := runPS(2*time.Minute, `$r = Invoke-CimMethod -Namespace root/default -ClassName SystemRestore -MethodName Restore -Arguments @{ SequenceNumber = [uint32]`+strconv.Itoa(seq)+` }; [int]$r.ReturnValue`)
	if code := strings.TrimSpace(out); err != nil || code != "0" {
		msg := T("rc.err.restore", code)
		op.fail("", msg, nil)
		op.end("", nil)
		return msg
	}
	op.end(T("log.rpRestart"), nil)
	if !restartPC() {
		return T("rc.err.restart")
	}
	return ""
}
