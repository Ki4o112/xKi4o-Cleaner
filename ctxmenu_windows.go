//go:build windows

package main

import (
	"errors"
	"os"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const blockedKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Shell Extensions\Blocked`

var procSHLoadIndirectString = windows.NewLazySystemDLL("shlwapi.dll").NewProc("SHLoadIndirectString")

// indirect resolves "@C:\x\app.dll,-101" names; others come back as they are.
func indirect(s string) string {
	if !strings.HasPrefix(s, "@") {
		return s
	}
	in, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return ""
	}
	buf := make([]uint16, 512)
	if r, _, _ := procSHLoadIndirectString.Call(uintptr(unsafe.Pointer(in)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0); r != 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

func rootKey(root string) registry.Key {
	if root == "hkcu" {
		return registry.CURRENT_USER
	}
	return registry.LOCAL_MACHINE
}

func classesPath(path string) string { return `Software\Classes\` + path }

func readStr(k registry.Key, name string) string {
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return v
}

// clsidServer: the DLL of a COM class, as the user's or the machine's
// registration says.
func clsidServer(clsid string) (dll, name string) {
	for _, root := range []string{"hkcu", "hklm"} {
		k, err := registry.OpenKey(rootKey(root), classesPath(`CLSID\`+clsid), registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		name = readStr(k, "")
		k.Close()
		if s, err := registry.OpenKey(rootKey(root), classesPath(`CLSID\`+clsid+`\InprocServer32`), registry.QUERY_VALUE); err == nil {
			dll = readStr(s, "")
			s.Close()
		} else if s, err := registry.OpenKey(rootKey(root), classesPath(`CLSID\`+clsid+`\LocalServer32`), registry.QUERY_VALUE); err == nil {
			dll, _ = commandParts(readStr(s, ""))
			s.Close()
		}
		if dll != "" { // a user's key may only carry a name: the machine's has the file
			return expandEnv(strings.Trim(dll, `"`)), name
		}
	}
	return "", name
}

func blockedCLSIDs() map[string]bool {
	out := map[string]bool{}
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, blockedKey, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		names, _ := k.ReadValueNames(0)
		for _, n := range names {
			out[strings.ToUpper(n)] = true
		}
		k.Close()
	}
	return out
}

func subKeys(root, path string) []string {
	k, err := registry.OpenKey(rootKey(root), classesPath(path), registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer k.Close()
	n, _ := k.ReadSubKeyNames(0)
	return n
}

var ctxLast struct {
	sync.Mutex
	items map[string]ctxItem
}

// ctxMenuList: the entries other programs put in the right-click menu.
func ctxMenuList() []ctxItem {
	windir := os.Getenv("SystemRoot")
	if windir == "" {
		windir = `C:\Windows`
	}
	blocked := blockedCLSIDs()
	var all []ctxItem
	for _, root := range []string{"hkcu", "hklm"} {
		for _, c := range ctxClasses {
			// verbs: shell\<verb>\command
			for _, verb := range subKeys(root, c.class+`\shell`) {
				path := c.class + `\shell\` + verb
				k, err := registry.OpenKey(rootKey(root), classesPath(path), registry.QUERY_VALUE)
				if err != nil {
					continue
				}
				name := indirect(readStr(k, "MUIVerb"))
				if name == "" {
					name = indirect(readStr(k, ""))
				}
				_, _, errOff := k.GetStringValue("LegacyDisable")
				_, _, errHidden := k.GetValue("ProgrammaticAccessOnly", nil)
				_, _, errShift := k.GetValue("Extended", nil)
				icon := readStr(k, "Icon")
				_, _, errSub := k.GetValue("SubCommands", nil)
				k.Close()
				if errHidden == nil {
					continue
				}
				prog := ""
				if ck, err := registry.OpenKey(rootKey(root), classesPath(path+`\command`), registry.QUERY_VALUE); err == nil {
					prog = menuProgram(readStr(ck, ""), os.Getenv)
					if prog == "" || windowsOwn(prog, windir) {
						if de := readStr(ck, "DelegateExecute"); isGUID(de) {
							prog, _ = clsidServer(de)
						}
					}
					ck.Close()
				}
				if prog == "" && errSub == nil {
					prog = iconFile(icon, os.Getenv) // a submenu: its picture tells whose it is
				}
				if windowsOwn(prog, windir) {
					continue
				}
				if name == "" {
					name = verb
				}
				all = append(all, ctxItem{Kind: "verb", Name: menuName(name), Where: c.where, Program: prog, Icon: iconFile(icon, os.Getenv),
					Missing: !fileExistsAny(prog), Shift: errShift == nil, Off: errOff == nil, locs: []ctxLoc{{root, path}}})
			}
			// shell extensions: shellex\ContextMenuHandlers\<name> = {CLSID}
			for _, h := range subKeys(root, c.class+`\shellex\ContextMenuHandlers`) {
				path := c.class + `\shellex\ContextMenuHandlers\` + h
				clsid := h
				if !isGUID(clsid) {
					k, err := registry.OpenKey(rootKey(root), classesPath(path), registry.QUERY_VALUE)
					if err != nil {
						continue
					}
					clsid = strings.TrimSpace(readStr(k, ""))
					k.Close()
				}
				if !isGUID(clsid) {
					continue
				}
				dll, cname := clsidServer(clsid)
				if windowsOwn(dll, windir) {
					continue
				}
				name := strings.TrimSpace(h)
				if isGUID(name) || name == "" {
					name = indirect(cname)
				}
				if name == "" {
					name = winBase(dll)
				}
				all = append(all, ctxItem{Kind: "ext", Name: name, Where: c.where, Program: dll, CLSID: strings.ToUpper(clsid),
					Missing: !fileExistsAny(dll), Off: blocked[strings.ToUpper(clsid)], locs: []ctxLoc{{root, path}}})
			}
		}
	}
	items := mergeCtx(all)
	ctxLast.Lock()
	ctxLast.items = map[string]ctxItem{}
	for _, it := range items {
		ctxLast.items[it.ID] = it
	}
	ctxLast.Unlock()
	return items
}

type ctxSetResult struct {
	Done     int       `json:"done"`
	Failed   []string  `json:"failed"`
	Explorer bool      `json:"explorer"` // an extension changed: Explorer must restart to show it
	Items    []ctxItem `json:"items"`
}

// ctxMenuSet hides (off) or shows the chosen entries again.
func ctxMenuSet(ids []string, off bool) ctxSetResult {
	res := ctxSetResult{Failed: []string{}}
	ctxLast.Lock()
	var todo []ctxItem
	for _, id := range ids {
		if it, ok := ctxLast.items[id]; ok {
			todo = append(todo, it)
		}
	}
	ctxLast.Unlock()
	if len(todo) == 0 {
		res.Items = ctxMenuList()
		return res
	}
	title := T("log.ctxShow")
	if off {
		title = T("log.ctxHide")
	}
	op := beginOp("menu", "", title, map[string]any{"count": len(todo)})
	for _, it := range todo {
		var err error
		if it.Kind == "ext" {
			err = setBlocked(it.CLSID, it.Name, off)
			res.Explorer = true
		} else {
			for _, l := range it.locs {
				if e := setLegacyDisable(l, off); e != nil {
					err = e
				}
			}
		}
		if err != nil {
			res.Failed = append(res.Failed, it.Name)
			op.fail(it.Name, err.Error(), map[string]any{"program": it.Program})
			continue
		}
		res.Done++
		op.info(it.Name, title, map[string]any{"program": it.Program, "kind": it.Kind})
	}
	op.end(T("log.ctxSum", res.Done, len(todo)), nil)
	res.Items = ctxMenuList()
	return res
}

func setLegacyDisable(l ctxLoc, off bool) error {
	k, err := registry.OpenKey(rootKey(l.Root), classesPath(l.Path), registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if off {
		return k.SetStringValue("LegacyDisable", "")
	}
	if err := k.DeleteValue("LegacyDisable"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// setBlocked puts a shell extension on Windows' blocked list (machine
// wide) or takes it off both lists.
func setBlocked(clsid, name string, off bool) error {
	if off {
		k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, blockedKey, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		return k.SetStringValue(clsid, name)
	}
	var last error
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, blockedKey, registry.SET_VALUE|registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		names, _ := k.ReadValueNames(0)
		for _, n := range names {
			if strings.EqualFold(n, clsid) {
				if err := k.DeleteValue(n); err != nil {
					last = err
				}
			}
		}
		k.Close()
	}
	return last
}
