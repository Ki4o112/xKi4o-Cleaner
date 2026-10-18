//go:build windows

package main

// History of tweak changes, undo, and profiles.
//
// Every change is written to %LOCALAPPDATA%\xKi4oCleaner\tweaks-history.json
// with the time, the result and a snapshot of the state just before it. The
// file survives restarts, so a change can still be undone later.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type histEntry struct {
	ID       int64        `json:"id"`
	At       int64        `json:"at"` // unix seconds
	Tweak    string       `json:"tweak"`
	Action   string       `json:"action"` // apply, revert, undo
	Source   string       `json:"source,omitempty"`
	OK       bool         `json:"ok"`
	State    string       `json:"state,omitempty"` // verified state afterwards
	Error    string       `json:"error,omitempty"`
	Reboot   bool         `json:"reboot,omitempty"`
	Boot     int64        `json:"boot,omitempty"`     // when Windows had started, to see later whether it restarted since
	Defaults bool         `json:"defaults,omitempty"` // turned off with the Windows defaults (no saved original)
	Undone   bool         `json:"undone,omitempty"`
	UndoOf   int64        `json:"undoOf,omitempty"`
	Before   *tweakBackup `json:"before,omitempty"`   // state right before this change
	Original *tweakBackup `json:"original,omitempty"` // the saved original at that moment
}

const maxHistory = 400

var (
	histMu sync.Mutex
	hist   []*histEntry
	histOK bool
)

func historyPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "xKi4oCleaner", "tweaks-history.json")
}

func loadHistory() {
	if histOK {
		return
	}
	histOK = true
	hist = nil
	if b, err := os.ReadFile(historyPath()); err == nil {
		_ = json.Unmarshal(b, &hist)
	}
}

func saveHistory() error {
	if len(hist) > maxHistory {
		hist = hist[len(hist)-maxHistory:]
	}
	b, err := json.MarshalIndent(hist, "", " ")
	if err != nil {
		return err
	}
	p := historyPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func addHistory(e *histEntry) {
	histMu.Lock()
	defer histMu.Unlock()
	loadHistory()
	now := time.Now()
	// microseconds: unique and still exact as a JavaScript number
	e.ID, e.At, e.Boot = now.UnixMicro(), now.Unix(), bootTime()
	if n := len(hist); n > 0 && hist[n-1].ID >= e.ID {
		e.ID = hist[n-1].ID + 1
	}
	hist = append(hist, e)
	_ = saveHistory()
}

// lastHistory is the latest entry of a tweak.
func lastHistory(id string) *histEntry {
	histMu.Lock()
	defer histMu.Unlock()
	loadHistory()
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Tweak == id {
			e := *hist[i]
			return &e
		}
	}
	return nil
}

// bootTime is when Windows last started (unix seconds).
func bootTime() int64 {
	return time.Now().Add(-windows.DurationSinceBoot()).Unix()
}

func rebootedSince(boot int64) bool {
	d := bootTime() - boot
	return d > 90 || d < -90
}

// ---------------------------------------------------------------- page bindings

type histItem struct {
	ID       int64  `json:"id"`
	At       int64  `json:"at"`
	Tweak    string `json:"tweak"`
	Action   string `json:"action"`
	Source   string `json:"source,omitempty"`
	OK       bool   `json:"ok"`
	State    string `json:"state,omitempty"`
	Error    string `json:"error,omitempty"`
	Reboot   bool   `json:"reboot,omitempty"`
	Pending  bool   `json:"pending,omitempty"` // still waits for a restart
	Defaults bool   `json:"defaults,omitempty"`
	Undone   bool   `json:"undone,omitempty"`
	CanUndo  bool   `json:"canUndo,omitempty"`
}

type historyView struct {
	Items []histItem `json:"items"`
	Undo  *histItem  `json:"undo,omitempty"` // what "Undo" would undo now
}

func undoable(e *histEntry) bool {
	if !e.OK || e.Undone || (e.Action != "apply" && e.Action != "revert") {
		return false
	}
	t := findTweak(e.Tweak)
	if t == nil || t.oneWay || t.legacy {
		return false
	}
	return !t.exact() || e.Before != nil
}

func toItem(e *histEntry) histItem {
	return histItem{ID: e.ID, At: e.At, Tweak: e.Tweak, Action: e.Action, Source: e.Source, OK: e.OK, State: e.State,
		Error: e.Error, Reboot: e.Reboot, Pending: e.OK && e.Reboot && !rebootedSince(e.Boot), Defaults: e.Defaults, Undone: e.Undone}
}

// tweakHistory returns the history, newest first.
func tweakHistory() historyView {
	histMu.Lock()
	defer histMu.Unlock()
	loadHistory()
	var v historyView
	for i := len(hist) - 1; i >= 0; i-- {
		it := toItem(hist[i])
		if v.Undo == nil && undoable(hist[i]) {
			it.CanUndo = true
			c := it
			v.Undo = &c
		}
		v.Items = append(v.Items, it)
	}
	return v
}

// undoTweak undoes one history entry (the latest undoable one when id is 0):
// the state from right before that change is put back exactly.
func undoTweak(id int64) (res tweakResult) {
	histMu.Lock()
	loadHistory()
	// only the latest change that can be undone, so changes are undone in
	// the reverse order they were made
	var e *histEntry
	for i := len(hist) - 1; i >= 0; i-- {
		if undoable(hist[i]) {
			e = hist[i]
			break
		}
	}
	histMu.Unlock()
	if e == nil || (id != 0 && e.ID != id) {
		res.Error = T("tw.err.noundo")
		return
	}
	t := findTweak(e.Tweak)
	if !t.exact() {
		// an uninstall/reinstall: the opposite operation is the undo
		r := setTweak(t.id, e.Action == "revert", tweakOpts{Source: "undo"})
		if r.OK {
			markUndone(e.ID)
		}
		return r
	}
	tweakMu.Lock()
	defer tweakMu.Unlock()
	app.working.Store(true)
	defer app.working.Store(false)
	entry := &histEntry{Tweak: t.id, Action: "undo", UndoOf: e.ID, Original: getBackup(t.id)}
	defer func() {
		if r := recover(); r != nil {
			res.OK, res.Error = false, T("err.unexpected", r)
		}
		st := fullStateNoHist(t, newProbe())
		entry.OK, entry.Error, entry.State, entry.Reboot = res.OK, res.Error, st.State, res.OK && t.reboot
		addHistory(entry)
		res.State = fullState(t, newProbe())
	}()
	if !app.admin {
		res.Error = T("blocked.admin")
		return
	}
	cur, err := takeSnap(t)
	if err != nil {
		res.Error = T("tw.err.snap", err.Error())
		return
	}
	entry.Before = cur
	if err := putSnap(t, e.Before); err != nil {
		res.Error = T("tw.err.restore", err.Error())
		return
	}
	if d := diffSnap(t, e.Before); len(d) > 0 {
		res.Error = T("tw.err.restore", strings.Join(d, "; "))
		return
	}
	// the saved original goes back to what it was before that change
	putBackup(t.id, e.Original)
	markUndone(e.ID)
	res.OK, res.Explorer, res.Reboot = true, t.explorer, t.reboot
	return
}

func markUndone(id int64) {
	histMu.Lock()
	defer histMu.Unlock()
	loadHistory()
	for _, h := range hist {
		if h.ID == id {
			h.Undone = true
		}
	}
	_ = saveHistory()
}

// appliedTweaks lists the tweaks changed from this app whose original state
// is saved: what "Undo all" would restore.
func appliedTweaks() []string {
	var out []string
	for _, t := range tweaks {
		if getBackup(t.id) != nil {
			out = append(out, t.id)
		}
	}
	return out
}

type undoAllResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// undoAllTweaks restores the saved original of every listed tweak, one by
// one, and reports each result.
func undoAllTweaks(ids []string) []undoAllResult {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []undoAllResult
	for _, id := range appliedTweaks() {
		if !want[id] {
			continue
		}
		r := setTweak(id, false, tweakOpts{Source: "undo-all"})
		out = append(out, undoAllResult{ID: id, OK: r.OK, Error: r.Error})
		emit("tweak-state", r.State)
	}
	return out
}

// ---------------------------------------------------------------- profiles

type tweakProfile struct {
	App     string          `json:"app"`
	Kind    string          `json:"kind"`
	Version int             `json:"version"`
	Created string          `json:"created"`
	Windows string          `json:"windows,omitempty"`
	Tweaks  map[string]bool `json:"tweaks"`
}

const profileKind = "xki4o-tweak-profile"

// profileTweak: tweaks that a profile can carry (two-way and not a list of
// apps to remove).
func profileTweak(t *tweak) bool { return !t.oneWay && !t.legacy }

type profileResult struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path,omitempty"`
	Count int    `json:"count,omitempty"`
	Error string `json:"error,omitempty"`
}

// exportTweakProfile saves the current on/off state of the tweaks to a file
// the user picks.
func exportTweakProfile() profileResult {
	path, ok := saveFileDialog(T("prof.saveTitle"), "xKi4o-tweaks.json")
	if !ok {
		return profileResult{}
	}
	p := newProbe()
	prof := tweakProfile{App: appName + " " + appVersion, Kind: profileKind, Version: 1,
		Created: time.Now().Format(time.RFC3339), Windows: osName(), Tweaks: map[string]bool{}}
	for _, t := range tweaks {
		if !profileTweak(t) {
			continue
		}
		switch t.state(p).State {
		case "on":
			prof.Tweaks[t.id] = true
		case "off":
			prof.Tweaks[t.id] = false
		}
	}
	b, _ := json.MarshalIndent(prof, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return profileResult{Error: err.Error()}
	}
	return profileResult{OK: true, Path: path, Count: len(prof.Tweaks)}
}

type profileStep struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

type profilePlan struct {
	OK      bool          `json:"ok"`
	Path    string        `json:"path,omitempty"`
	Error   string        `json:"error,omitempty"`
	Steps   []profileStep `json:"steps"`
	Same    int           `json:"same"`    // already as in the profile
	Skipped []string      `json:"skipped"` // unknown here, not available or one-way
}

// importTweakProfile reads a profile the user picks and works out what would
// change. Nothing is changed here: the page shows the plan first.
func importTweakProfile() profilePlan {
	path, ok := openFileDialog(T("prof.openTitle"))
	if !ok {
		return profilePlan{}
	}
	return planProfileFile(path)
}

func planProfileFile(path string) (plan profilePlan) {
	plan.Path = path
	b, err := os.ReadFile(path)
	if err != nil {
		plan.Error = err.Error()
		return
	}
	if len(b) > 1<<20 {
		plan.Error = T("prof.bad")
		return
	}
	var prof tweakProfile
	if json.Unmarshal(b, &prof) != nil || prof.Kind != profileKind || prof.Tweaks == nil {
		plan.Error = T("prof.bad")
		return
	}
	p := newProbe()
	ids := make([]string, 0, len(prof.Tweaks))
	for id := range prof.Tweaks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		want := prof.Tweaks[id]
		t := findTweak(id)
		if t == nil || !profileTweak(t) {
			plan.Skipped = append(plan.Skipped, id)
			continue
		}
		st := t.state(p).State
		switch {
		case st == "na" || st == "pending":
			plan.Skipped = append(plan.Skipped, id)
		case (st == "on") == want:
			plan.Same++
		default:
			plan.Steps = append(plan.Steps, profileStep{ID: id, On: want})
		}
	}
	plan.OK = true
	return
}

// ---------------------------------------------------------------- file dialogs

var (
	comdlg32             = windows.NewLazySystemDLL("comdlg32.dll")
	procGetSaveFileNameW = comdlg32.NewProc("GetSaveFileNameW")
	procGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")
)

type openFileName struct {
	structSize    uint32
	owner         uintptr
	instance      uintptr
	filter        *uint16
	customFilter  *uint16
	maxCustFilter uint32
	filterIndex   uint32
	file          *uint16
	maxFile       uint32
	fileTitle     *uint16
	maxFileTitle  uint32
	initialDir    *uint16
	title         *uint16
	flags         uint32
	fileOffset    uint16
	fileExtension uint16
	defExt        *uint16
	custData      uintptr
	hook          uintptr
	templateName  *uint16
	reserved      uintptr
	reserved2     uint32
	flagsEx       uint32
}

const (
	ofnOverwritePrompt = 0x2
	ofnHideReadOnly    = 0x4
	ofnNoChangeDir     = 0x8
	ofnPathMustExist   = 0x800
	ofnFileMustExist   = 0x1000
	ofnExplorer        = 0x80000
)

// fileDialog shows the Save or Open dialog. kinds are pairs of a name and a
// pattern ("Text files", "*.txt"); ext is added to a name typed without one.
func fileDialog(save bool, title, name string, kinds []string, ext string) (string, bool) {
	var filter []uint16 // pairs of NUL-terminated strings, ended by an empty one
	for _, part := range append(kinds, T("prof.all"), "*.*") {
		u, _ := windows.UTF16FromString(part)
		filter = append(filter, u...)
	}
	filter = append(filter, 0)
	buf := make([]uint16, 1024)
	if u, err := windows.UTF16FromString(name); err == nil && len(u) < len(buf) {
		copy(buf, u)
	}
	defExt, _ := windows.UTF16PtrFromString(ext)
	t, _ := windows.UTF16PtrFromString(title)
	docs := filepath.Join(os.Getenv("USERPROFILE"), "Documents")
	dir, _ := windows.UTF16PtrFromString(docs)
	ofn := openFileName{owner: app.hwnd, filter: &filter[0], filterIndex: 1, file: &buf[0], maxFile: uint32(len(buf)),
		initialDir: dir, title: t, defExt: defExt, flags: ofnExplorer | ofnNoChangeDir | ofnPathMustExist | ofnHideReadOnly}
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	proc := procGetOpenFileNameW
	if save {
		ofn.flags |= ofnOverwritePrompt
		proc = procGetSaveFileNameW
	} else {
		ofn.flags |= ofnFileMustExist
	}
	ok := false
	withCOM(func() {
		r, _, _ := proc.Call(uintptr(unsafe.Pointer(&ofn)))
		ok = r != 0
	})
	runtime.KeepAlive(filter)
	runtime.KeepAlive(buf)
	if !ok {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

func saveFileDialog(title, name string) (string, bool) {
	return fileDialog(true, title, name, []string{T("prof.filter"), "*.json"}, "json")
}
func openFileDialog(title string) (string, bool) {
	return fileDialog(false, title, "", []string{T("prof.filter"), "*.json"}, "json")
}

// OPENFILENAMEW is 152 bytes on 64-bit Windows.
var _ = [1]struct{}{}[unsafe.Sizeof(openFileName{})-152]
