package main

// Operation log.
//
// Every operation the app does for the user - a cleaning, a tweak, a startup
// change, deleting files, ending a process, a restore - is written to
// %LOCALAPPDATA%\xKi4oCleaner\logs\log.jsonl, one JSON object per line:
// when it started, each step with its result, every problem, and how it
// ended. The Logs page shows it, and a report of any operation can be
// copied in one click.
//
// The file is kept small: past 4 MB it is renamed to log.1.jsonl (the older
// one is dropped). An operation that has a start but no end in the file was
// cut off (the app was closed or crashed during it) and is shown so.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

type logEntry struct {
	ID    int64          `json:"id"`
	T     int64          `json:"t"`              // unix milliseconds
	Op    int64          `json:"op,omitempty"`   // the operation it belongs to
	Kind  string         `json:"kind"`           // clean, tweak, startup, disk, files, dupes, process, restore, memory, repair, app, ui
	Level string         `json:"level"`          // info, warn, error
	Subj  string         `json:"subj,omitempty"` // category, tweak, entry, path or process it is about
	Msg   string         `json:"msg"`
	Data  map[string]any `json:"data,omitempty"`
	Phase string         `json:"phase,omitempty"` // begin, end
	State string         `json:"state,omitempty"` // end: ok, warn, error
}

type opSummary struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Subj    string `json:"subj,omitempty"`
	Title   string `json:"title"`
	Start   int64  `json:"start"`
	End     int64  `json:"end,omitempty"`
	State   string `json:"state"` // running, ok, warn, error, cut
	Errors  int    `json:"errors"`
	Warns   int    `json:"warns"`
	Steps   int    `json:"steps"`
	Summary string `json:"summary,omitempty"`
}

const (
	logKeep     = 4000    // entries kept in memory
	logMaxBytes = 4 << 20 // the file is rotated past this size
)

type opLog struct {
	mu      sync.Mutex
	dir     string
	entries []logEntry
	ops     map[int64]*opSummary
	lastID  int64
	running map[int64]bool // operations of this run that have not ended
}

var olog = &opLog{ops: map[int64]*opSummary{}, running: map[int64]bool{}}

// logInit loads what earlier runs wrote. Operations without an end were cut
// off.
func logInit(dir string) {
	l := olog
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dir = dir
	l.entries = nil
	l.ops = map[int64]*opSummary{}
	l.running = map[int64]bool{}
	_ = os.MkdirAll(dir, 0o755)
	var all []logEntry
	for _, name := range []string{"log.1.jsonl", "log.jsonl"} {
		all = append(all, readLogFile(filepath.Join(dir, name))...)
	}
	if len(all) > logKeep {
		all = all[len(all)-logKeep:]
	}
	for _, e := range all {
		l.index(e)
		if e.ID > l.lastID {
			l.lastID = e.ID
		}
	}
	l.entries = all
	for _, s := range l.ops {
		if s.State == "running" {
			s.State = "cut"
		}
	}
}

func readLogFile(p string) []logEntry {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []logEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e logEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != 0 {
			out = append(out, e)
		}
	}
	return out
}

// index updates the operation summaries with one entry (lock held).
func (l *opLog) index(e logEntry) {
	if e.Op == 0 {
		return
	}
	s := l.ops[e.Op]
	if s == nil {
		s = &opSummary{ID: e.Op, Kind: e.Kind, Start: e.T, State: "running"}
		l.ops[e.Op] = s
	}
	switch e.Phase {
	case "begin":
		s.Title, s.Subj, s.Start = e.Msg, e.Subj, e.T
	case "end":
		s.End, s.State, s.Summary = e.T, e.State, e.Msg
	default:
		s.Steps++
	}
	switch e.Level {
	case "error":
		s.Errors++
	case "warn":
		s.Warns++
	}
}

func (l *opLog) nextID() int64 {
	id := time.Now().UnixMicro()
	if id <= l.lastID {
		id = l.lastID + 1
	}
	l.lastID = id
	return id
}

// add stores one entry (in memory and in the file).
func (l *opLog) add(e logEntry) logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.ID = l.nextID()
	if e.Phase == "begin" {
		e.Op = e.ID // the begin entry's own id is the operation id
		l.running[e.ID] = true
	}
	if e.T == 0 {
		e.T = time.Now().UnixMilli()
	}
	if e.Level == "" {
		e.Level = "info"
	}
	l.entries = append(l.entries, e)
	if len(l.entries) > logKeep+500 {
		l.entries = append([]logEntry(nil), l.entries[len(l.entries)-logKeep:]...)
		l.prune()
	}
	l.index(e)
	l.write(e)
	return e
}

// prune drops summaries whose entries are gone (lock held).
func (l *opLog) prune() {
	if len(l.entries) == 0 {
		return
	}
	oldest := l.entries[0].ID
	for id := range l.ops {
		if id < oldest && !l.running[id] {
			delete(l.ops, id)
		}
	}
}

func (l *opLog) write(e logEntry) {
	if l.dir == "" {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	p := filepath.Join(l.dir, "log.jsonl")
	if fi, err := os.Stat(p); err == nil && fi.Size() > logMaxBytes {
		old := filepath.Join(l.dir, "log.1.jsonl")
		_ = os.Remove(old)
		_ = os.Rename(p, old)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

// ---------------------------------------------------------------- operations

// opHandle is one running operation. A nil handle does nothing, so code can
// log without checking.
type opHandle struct {
	id   int64
	kind string
	mu   sync.Mutex
	errs int
	warn int
	done bool
}

func beginOp(kind, subj, title string, data map[string]any) *opHandle {
	e := olog.add(logEntry{Kind: kind, Subj: subj, Msg: title, Data: data, Phase: "begin"})
	return &opHandle{id: e.Op, kind: kind}
}

func (o *opHandle) log(level, subj, msg string, data map[string]any) {
	if o == nil {
		return
	}
	o.mu.Lock()
	switch level {
	case "error":
		o.errs++
	case "warn":
		o.warn++
	}
	o.mu.Unlock()
	olog.add(logEntry{Op: o.id, Kind: o.kind, Level: level, Subj: subj, Msg: msg, Data: data})
}

func (o *opHandle) info(subj, msg string, data map[string]any) { o.log("info", subj, msg, data) }
func (o *opHandle) warnf(subj, msg string, data map[string]any) {
	o.log("warn", subj, msg, data)
}
func (o *opHandle) fail(subj, msg string, data map[string]any) { o.log("error", subj, msg, data) }

// panicked records a recovered panic with its stack.
func (o *opHandle) panicked(r any) {
	o.fail("", fmt.Sprint(r), map[string]any{"stack": string(debug.Stack())})
}

// end closes the operation. Its state follows what was logged: error if
// anything failed, warn if anything was skipped or left, else ok.
func (o *opHandle) end(summary string, data map[string]any) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.done {
		o.mu.Unlock()
		return
	}
	o.done = true
	state := "ok"
	if o.errs > 0 {
		state = "error"
	} else if o.warn > 0 {
		state = "warn"
	}
	o.mu.Unlock()
	olog.add(logEntry{Op: o.id, Kind: o.kind, Level: "info", Msg: summary, Data: data, Phase: "end", State: state})
	olog.mu.Lock()
	delete(olog.running, o.id)
	var sum opSummary
	if s := olog.ops[o.id]; s != nil {
		sum = *s
	}
	olog.mu.Unlock()
	if onOpEnd != nil && sum.ID != 0 {
		onOpEnd(sum)
	}
}

// onOpEnd is told about every operation that ended (the page shows a toast
// with "Copy report" for failed ones).
var onOpEnd func(opSummary)

// logEvent records something that is not part of an operation.
func logEvent(kind, level, subj, msg string, data map[string]any) {
	olog.add(logEntry{Kind: kind, Level: level, Subj: subj, Msg: msg, Data: data})
}

// ---------------------------------------------------------------- page bindings

type logFilter struct {
	Kind  string `json:"kind"`
	State string `json:"state"` // "", "problems"
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type logOpsView struct {
	Ops    []opSummary `json:"ops"`
	Total  int         `json:"total"`
	Errors int         `json:"errors"` // operations that ended with errors
	Cut    int         `json:"cut"`
	Dir    string      `json:"dir"`
}

// logOps lists operations, newest first.
func logOps(f logFilter) logOpsView {
	l := olog
	l.mu.Lock()
	defer l.mu.Unlock()
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 300
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	all := make([]opSummary, 0, len(l.ops))
	for _, s := range l.ops {
		all = append(all, *s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	v := logOpsView{Dir: l.dir, Total: len(all)}
	for _, s := range all {
		switch s.State {
		case "error":
			v.Errors++
		case "cut":
			v.Cut++
		}
		if f.Kind != "" && s.Kind != f.Kind {
			continue
		}
		if f.State == "problems" && s.State != "error" && s.State != "warn" && s.State != "cut" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(s.Title+" "+s.Subj+" "+s.Summary), q) {
			continue
		}
		if len(v.Ops) < f.Limit {
			v.Ops = append(v.Ops, s)
		}
	}
	return v
}

// logEntries returns the entries of one operation (op 0: the latest
// entries that belong to no operation, newest last).
func logEntries(op int64) []logEntry {
	l := olog
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logEntry
	for _, e := range l.entries {
		if e.Op == op {
			out = append(out, e)
		}
	}
	if op == 0 && len(out) > 300 {
		out = out[len(out)-300:]
	}
	return out
}

// logRecentProblems returns the latest warnings and errors of any kind.
func logRecentProblems(n int) []logEntry {
	l := olog
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logEntry
	for i := len(l.entries) - 1; i >= 0 && len(out) < n; i-- {
		if e := l.entries[i]; e.Level != "info" {
			out = append(out, e)
		}
	}
	return out
}

// logClear empties the log (both files).
func logClear() bool {
	l := olog
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dir != "" {
		for _, n := range []string{"log.jsonl", "log.1.jsonl"} {
			if err := os.Remove(filepath.Join(l.dir, n)); err != nil && !os.IsNotExist(err) {
				return false
			}
		}
	}
	l.entries = nil
	for id := range l.ops {
		if !l.running[id] {
			delete(l.ops, id)
		}
	}
	return true
}

// logClient records an error of the page itself (a script error), so it
// shows up in reports too.
func logClient(level, msg, detail string) bool {
	if level != "error" && level != "warn" {
		level = "info"
	}
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if len(detail) > 8000 {
		detail = detail[:8000]
	}
	var data map[string]any
	if detail != "" {
		data = map[string]any{"detail": detail}
	}
	logEvent("ui", level, "", msg, data)
	return true
}

// maskProfile replaces the user's profile folder in a text with
// %USERPROFILE%, so a report can be shared without the account name.
func maskProfile(s, profile string) string {
	if profile == "" || len(profile) < 4 {
		return s
	}
	n := len(profile)
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+n <= len(s) && strings.EqualFold(s[i:i+n], profile) {
			b.WriteString("%USERPROFILE%")
			i += n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func anyText(v any) string { return fmt.Sprint(v) }

// sizeText formats a size for log lines (1 decimal from MB up).
func sizeText(b int64) string {
	u := []string{"B", "KB", "MB", "GB", "TB"}
	if lang.Load() == "bg" {
		u = []string{"Б", "КБ", "МБ", "ГБ", "ТБ"}
	}
	if b < 1024 {
		return fmt.Sprintf("%d %s", b, u[0])
	}
	v := float64(b)
	i := 0
	for v >= 1024 && i < len(u)-1 {
		v /= 1024
		i++
	}
	if i == 1 {
		return fmt.Sprintf("%.0f %s", v, u[i])
	}
	return fmt.Sprintf("%.1f %s", v, u[i])
}

// recoverLog keeps a panic in a background worker from closing the whole
// app: the panic is written to the log with its stack instead. Use it as
// "defer recoverLog(where)".
func recoverLog(where string) {
	if r := recover(); r != nil {
		logEvent("app", "error", where, fmt.Sprint(r), map[string]any{"stack": string(debug.Stack())})
	}
}
