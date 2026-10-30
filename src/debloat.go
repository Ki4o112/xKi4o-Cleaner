package main

// The Debloat page: everything Win11Debloat (github.com/Raphire/Win11Debloat,
// MIT licence, see debloat/LICENSE-Win11Debloat) does, run by the app
// itself. debloat/import.py turns Win11Debloat's files into
// debloat/w11d.json: each change as plain registry operations, the apps it
// removes and its presets, with the Bulgarian texts. A change counts as
// applied when every one of its operations matches the registry. Free of
// Windows calls.

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

//go:embed debloat/w11d.json
var w11dJSON []byte

// start2.bin with no pinned apps (Win11Debloat's Assets/Start/start2.bin)
//
//go:embed debloat/start2.bin
var emptyStartBin []byte

type regOp struct {
	Op   string `json:"op"` // set, delval, delkey, mkkey
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type,omitempty"` // dword, string, binary, expand, multi, qword
	Num  uint64 `json:"num,omitempty"`
	Str  string `json:"str,omitempty"`
	Bin  []byte `json:"-"`
	BinJ []int  `json:"bin,omitempty"`
}

type dbFeature struct {
	ID     string    `json:"id"`
	Cat    string    `json:"cat"`
	Label  string    `json:"label"`
	Tip    string    `json:"tip"`
	BG     [2]string `json:"bg"`
	Prio   int       `json:"prio"`
	Min    int       `json:"min,omitempty"`
	Max    int       `json:"max,omitempty"`
	Reboot bool      `json:"reboot,omitempty"`
	Oneway bool      `json:"oneway,omitempty"` // cannot be taken back here
	Apply  []regOp   `json:"apply,omitempty"`
	Undo   []regOp   `json:"undo,omitempty"`
	Custom string    `json:"custom,omitempty"` // widgets, sandbox, wsl, clearstart
	Apps   []string  `json:"apps,omitempty"`   // apps removed with it
	Tasks  bool      `json:"tasks,omitempty"`  // telemetry scheduled tasks too
}

type dbGroupValue struct {
	Label string   `json:"label"`
	BG    string   `json:"bg"`
	IDs   []string `json:"ids"`
}

type dbGroup struct {
	ID     string         `json:"id"`
	Cat    string         `json:"cat"`
	Label  string         `json:"label"`
	Tip    string         `json:"tip"`
	BG     []string       `json:"bg"`
	Prio   int            `json:"prio"`
	Values []dbGroupValue `json:"values"`
}

type dbApp struct {
	IDs    []string `json:"ids"`
	Name   string   `json:"name"`
	Desc   string   `json:"desc"`
	BG     []string `json:"bg"` // name (or ""), description
	Rec    string   `json:"rec"`
	Method string   `json:"method"` // Appx, WinGet
	Def    bool     `json:"def"`
}

type dbData struct {
	Source     string `json:"source"`
	Categories []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		BG    string `json:"bg"`
	} `json:"categories"`
	Features []dbFeature `json:"features"`
	Groups   []dbGroup   `json:"groups"`
	Apps     []dbApp     `json:"apps"`
	Presets  []struct {
		Name string   `json:"name"`
		IDs  []string `json:"ids"`
	} `json:"presets"`
	Defaults []string `json:"defaults"`
}

var w11d struct {
	once sync.Once
	d    dbData
	byID map[string]*dbFeature
}

func debloatData() *dbData {
	w11d.once.Do(func() {
		if err := json.Unmarshal(w11dJSON, &w11d.d); err != nil {
			panic("debloat/w11d.json: " + err.Error())
		}
		w11d.byID = map[string]*dbFeature{}
		for i := range w11d.d.Features {
			f := &w11d.d.Features[i]
			for _, ops := range [][]regOp{f.Apply, f.Undo} {
				for j := range ops {
					ops[j].Bin = make([]byte, len(ops[j].BinJ))
					for k, b := range ops[j].BinJ {
						ops[j].Bin[k] = byte(b)
					}
				}
			}
			w11d.byID[f.ID] = f
		}
	})
	return &w11d.d
}

func dbFeatureByID(id string) *dbFeature {
	debloatData()
	return w11d.byID[id]
}

// fits: the change is meant for this Windows build.
func (f *dbFeature) fits(build int) bool {
	return (f.Min == 0 || build >= f.Min) && (f.Max == 0 || build <= f.Max)
}

func buildNum(s string) int {
	n, _ := strconv.Atoi(strings.SplitN(s, ".", 2)[0])
	return n
}

// regView: what the state check needs from the registry.
type regView interface {
	value(key, name string) (typ string, num uint64, str string, bin []byte, ok bool)
	keyExists(key string) bool
}

func opMatches(op regOp, r regView) bool {
	switch op.Op {
	case "mkkey":
		return r.keyExists(op.Key)
	case "delkey":
		return !r.keyExists(op.Key)
	case "delval":
		_, _, _, _, ok := r.value(op.Key, op.Name)
		return !ok
	case "set":
		typ, num, str, bin, ok := r.value(op.Key, op.Name)
		if !ok {
			return false
		}
		switch op.Type {
		case "dword", "qword":
			return (typ == "dword" || typ == "qword") && num == op.Num
		case "string", "expand":
			return (typ == "string" || typ == "expand") && str == op.Str
		default:
			return string(bin) == string(op.Bin)
		}
	}
	return false
}

// regApplied: every operation of the change is in the registry.
func regApplied(ops []regOp, r regView) bool {
	if len(ops) == 0 {
		return false
	}
	for _, op := range ops {
		if !opMatches(op, r) {
			return false
		}
	}
	return true
}

// appMatches: an installed package name is the app's (Win11Debloat looks
// for the id anywhere in the name: some ids are only part of it).
func appMatches(id, pkg string) bool {
	return strings.Contains(strings.ToLower(pkg), strings.ToLower(id))
}
