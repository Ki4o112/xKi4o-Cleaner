package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// finderJob runs one search at a time (large files or duplicates) and keeps
// the last result of each kind, which deletions are checked against.
type finderJob struct {
	mu        sync.Mutex
	running   bool
	kind      string
	stop      atomic.Bool
	st        *findStats
	large     []foundFile
	largeOpts findOpts
	largeAt   int64
	largeTook int64
	dupes     []dupGroup
	dupesOpts findOpts
	dupesAt   int64
	dupesTook int64
	scanned   map[string]findProgress // totals of the last search of each kind
}

var fjob = &finderJob{scanned: map[string]findProgress{}}

// start begins a search in the background. emit receives progress about
// four times a second and a final "find-done".
func (j *finderJob) start(kind string, o findOpts, fs finderFS, emit func(string, any)) bool {
	if kind != "large" && kind != "dupes" {
		return false
	}
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return false
	}
	j.running, j.kind = true, kind
	j.stop.Store(false)
	st := &findStats{}
	st.phase.Store("walk")
	j.st = st
	j.mu.Unlock()

	go func() {
		t0 := time.Now()
		done := make(chan struct{})
		go func() {
			tk := time.NewTicker(250 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-done:
					return
				case <-tk.C:
					emit("find-progress", map[string]any{"kind": kind, "p": st.progress()})
				}
			}
		}()
		var large []foundFile
		var dupes []dupGroup
		ok := true
		func() {
			defer func() {
				if r := recover(); r != nil {
					ok = false
					logEvent("files", "error", "", "finder: "+anyText(r), nil)
				}
			}()
			if kind == "large" {
				large = findLarge(o, fs, &j.stop, st)
			} else if o.Similar {
				dupes = findSimilar(o, fs, &j.stop, st)
			} else {
				dupes = findDupes(o, fs, &j.stop, st)
			}
		}()
		close(done)
		stopped := j.stop.Load()
		took := time.Since(t0).Milliseconds()
		j.mu.Lock()
		if ok && !stopped {
			if kind == "large" {
				j.large, j.largeOpts, j.largeAt, j.largeTook = large, o, time.Now().UnixMilli(), took
			} else {
				j.dupes, j.dupesOpts, j.dupesAt, j.dupesTook = dupes, o, time.Now().UnixMilli(), took
			}
			j.scanned[kind] = st.progress()
		}
		j.running = false
		j.mu.Unlock()
		n := len(large) + len(dupes)
		emit("find-done", map[string]any{"kind": kind, "ok": ok, "stopped": stopped, "count": n, "took": took, "p": st.progress()})
	}()
	return true
}

func (j *finderJob) cancel() bool {
	j.stop.Store(true)
	return true
}

type findResult struct {
	Kind    string       `json:"kind"`
	Running bool         `json:"running"`
	Large   []foundFile  `json:"large,omitempty"`
	Dupes   []dupGroup   `json:"dupes,omitempty"`
	Opts    findOpts     `json:"opts"`
	At      int64        `json:"at"`
	Took    int64        `json:"took"`
	Wasted  int64        `json:"wasted,omitempty"`
	Total   int64        `json:"total,omitempty"` // large files: their size together
	Scanned findProgress `json:"scanned"`
}

func (j *finderJob) result(kind string) findResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := findResult{Kind: kind, Running: j.running && j.kind == kind, Scanned: j.scanned[kind]}
	if kind == "large" {
		r.Large, r.Opts, r.At, r.Took = j.large, j.largeOpts, j.largeAt, j.largeTook
		for _, f := range j.large {
			r.Total += f.Size
		}
	} else {
		r.Dupes, r.Opts, r.At, r.Took = j.dupes, j.dupesOpts, j.dupesAt, j.dupesTook
		for _, g := range j.dupes {
			r.Wasted += g.Wasted
		}
	}
	return r
}

// delete moves the chosen files of the last result to the Recycle Bin.
// Errors are text keys (translated by the caller).
func (j *finderJob) delete(kind string, paths []string, fs finderFS) []delResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		var out []delResult
		for _, p := range paths {
			out = append(out, delResult{Path: p, Error: "da.err.busy"})
		}
		return out
	}
	var ok []foundFile
	var refused []delResult
	if kind == "dupes" {
		ok, refused = planDupDelete(j.dupes, paths, fs)
	} else {
		ok, refused = planFileDelete(j.large, paths, fs)
	}
	res := append(refused, recycleFiles(ok, fs)...)
	gone := map[string]bool{}
	for _, r := range res {
		if r.OK {
			gone[normPath(r.Path)] = true
		}
	}
	if kind == "dupes" {
		j.dupes = removeFromGroups(j.dupes, gone)
	} else {
		var keep []foundFile
		for _, f := range j.large {
			if !gone[normPath(f.Path)] {
				keep = append(keep, f)
			}
		}
		j.large = keep
	}
	return res
}
