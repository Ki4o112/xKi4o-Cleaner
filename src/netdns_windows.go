//go:build windows

package main

import (
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The network page: which DNS servers the PC uses, switching them for the
// connections in use (and back), a speed test and flushing the DNS cache.

type netAdapter struct {
	Index    int      `json:"index"`
	GUID     string   `json:"guid"`
	Name     string   `json:"name"`
	Desc     string   `json:"desc"`
	WiFi     bool     `json:"wifi"`
	Speed    int64    `json:"speed"`
	DNS      []string `json:"dns"`    // in use now
	Static   bool     `json:"static"` // set by hand, not from the router
	Provider string   `json:"provider"`
	Gateway  bool     `json:"gateway"`
}

type netState struct {
	Adapters  []netAdapter  `json:"adapters"`
	Providers []dnsProvider `json:"providers"`
	Current   string        `json:"current"` // a provider id, "auto", "custom", "mixed" or "" (no connection)
	Backup    bool          `json:"backup"`  // what was there before can be put back
}

type dnsSetResult struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	Warn  string   `json:"warn,omitempty"`
	State netState `json:"state"`
}

const (
	tcpip4Key = `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces\`
	tcpip6Key = `SYSTEM\CurrentControlSet\Services\Tcpip6\Parameters\Interfaces\`
)

var dnsMu sync.Mutex // one change at a time

// staticDNS: the servers set by hand for one adapter and family ("" when
// they come from the router).
func staticDNS(base, guid string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, base+guid, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("NameServer")
	return strings.Join(splitServers(v), ",")
}

// listAdapters: the connections that are up (wired and Wi-Fi).
func listAdapters() []netAdapter {
	size := uint32(16 << 10)
	var aa *windows.IpAdapterAddresses
	for i := 0; i < 4; i++ {
		buf := make([]byte, size)
		aa = (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST, 0, aa, &size)
		if err == nil {
			break
		}
		aa = nil
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			return []netAdapter{}
		}
	}
	out := []netAdapter{}
	for a := aa; a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp || a.IfType != windows.IF_TYPE_ETHERNET_CSMACD && a.IfType != windows.IF_TYPE_IEEE80211 {
			continue
		}
		n := netAdapter{
			Index:   int(a.IfIndex),
			GUID:    windows.BytePtrToString(a.AdapterName),
			Name:    windows.UTF16PtrToString(a.FriendlyName),
			Desc:    windows.UTF16PtrToString(a.Description),
			WiFi:    a.IfType == windows.IF_TYPE_IEEE80211,
			Speed:   int64(a.ReceiveLinkSpeed),
			Gateway: a.FirstGatewayAddress != nil,
			DNS:     []string{},
		}
		if n.Index == 0 {
			n.Index = int(a.Ipv6IfIndex)
		}
		for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
			ip := d.Address.IP()
			if ip == nil || isSiteLocalDNS(ip) {
				continue
			}
			n.DNS = append(n.DNS, ip.String())
		}
		n.Static = staticDNS(tcpip4Key, n.GUID) != "" || staticDNS(tcpip6Key, n.GUID) != ""
		if n.Static {
			n.Provider = providerOf(n.DNS)
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Gateway && !out[j].Gateway })
	return out
}

// fec0:0:0:ffff::1-3: the placeholders Windows lists when IPv6 has no DNS.
func isSiteLocalDNS(ip net.IP) bool {
	return ip.To4() == nil && len(ip) == net.IPv6len && ip[0] == 0xfe && ip[1]&0xc0 == 0xc0
}

// inUse: the adapters a DNS change is for — the ones with a way out (a
// gateway); when none has one, all that are up.
func inUse(all []netAdapter) []netAdapter {
	var gw []netAdapter
	for _, a := range all {
		if a.Gateway {
			gw = append(gw, a)
		}
	}
	if len(gw) > 0 {
		return gw
	}
	return all
}

func getNetState() netState {
	st := netState{Adapters: listAdapters(), Providers: dnsProviders}
	use := inUse(st.Adapters)
	for i, a := range use {
		cur := "auto"
		if a.Static {
			cur = a.Provider
			if cur == "" {
				cur = "custom"
			}
		}
		if i == 0 {
			st.Current = cur
		} else if st.Current != cur {
			st.Current = "mixed"
		}
	}
	b, _ := getSettings()["dnsBackup"].(map[string]any)
	st.Backup = len(b) > 0
	return st
}

// netsh changes one family of one adapter; the index works where a name
// in another language might not.
func netshDNS(family string, index int, servers []string) error {
	idx := strconv.Itoa(index)
	if len(servers) == 0 {
		return runTool(sys32("netsh.exe"), "interface", family, "set", "dnsservers", "name="+idx, "source=dhcp")
	}
	if err := runTool(sys32("netsh.exe"), "interface", family, "set", "dnsservers", "name="+idx, "source=static", "address="+servers[0], "register=primary", "validate=no"); err != nil {
		return err
	}
	for i, s := range servers[1:] {
		if err := runTool(sys32("netsh.exe"), "interface", family, "add", "dnsservers", "name="+idx, "address="+s, "index="+strconv.Itoa(i+2), "validate=no"); err != nil {
			return err
		}
	}
	return nil
}

// applyDNS sets one adapter: IPv4 must work; IPv6 is best effort (it may
// be switched off on that connection).
func applyDNS(a netAdapter, v4, v6 []string) (warn string, err error) {
	if err := netshDNS("ipv4", a.Index, v4); err != nil {
		return "", err
	}
	if e := netshDNS("ipv6", a.Index, v6); e != nil {
		warn = e.Error()
	}
	return warn, nil
}

func flushDNSCache() bool {
	_, code, err := runQuiet(30*time.Second, sys32("ipconfig.exe"), "/flushdns")
	return err == nil && code == 0
}

// dnsSet switches the connections in use to a provider, or back to what
// the router gives ("auto"). What was there before is kept the first time,
// so "Restore" brings it back.
func dnsSet(id string) dnsSetResult {
	dnsMu.Lock()
	defer dnsMu.Unlock()
	var v4, v6 []string
	name := T("dns.auto")
	if id != "auto" {
		p, ok := providerByID(id)
		if !ok {
			return dnsSetResult{Error: T("dns.err.unknown"), State: getNetState()}
		}
		v4, v6, name = p.V4, p.V6, p.Name
	}
	use := inUse(listAdapters())
	if len(use) == 0 {
		return dnsSetResult{Error: T("dns.err.noconn"), State: getNetState()}
	}
	op := beginOp("network", "", T("log.dns", name), map[string]any{"servers": append(append([]string{}, v4...), v6...)})
	saved := updateSettings(func(m map[string]any) {
		backup, _ := m["dnsBackup"].(map[string]any)
		if backup == nil {
			backup = map[string]any{}
		}
		for _, a := range use {
			if _, ok := backup[a.GUID]; !ok {
				backup[a.GUID] = map[string]any{"name": a.Name, "v4": staticDNS(tcpip4Key, a.GUID), "v6": staticDNS(tcpip6Key, a.GUID)}
			}
		}
		m["dnsBackup"] = backup
	})
	if !saved {
		op.fail("", T("dns.err.backup"), nil)
		op.end(T("dns.err.backup"), nil)
		return dnsSetResult{Error: T("dns.err.backup"), State: getNetState()}
	}
	res := dnsSetResult{OK: true}
	var warns []string
	for _, a := range use {
		w, err := applyDNS(a, v4, v6)
		if err != nil {
			res.OK = false
			res.Error = a.Name + ": " + err.Error()
			op.fail(a.Name, err.Error(), nil)
			continue
		}
		if w != "" && len(v6) > 0 {
			warns = append(warns, a.Name)
			op.warnf(a.Name, T("dns.w.v6", w), nil)
		}
		op.info(a.Name, T("dns.done", name), nil)
	}
	if id == "auto" && res.OK {
		// back on the router's servers: a connection that had them before
		// has nothing to put back (one that had its own servers keeps them)
		updateSettings(func(m map[string]any) {
			backup, _ := m["dnsBackup"].(map[string]any)
			for g, v := range backup {
				if b, _ := v.(map[string]any); str(b["v4"]) == "" && str(b["v6"]) == "" {
					delete(backup, g)
				}
			}
			if len(backup) == 0 {
				delete(m, "dnsBackup")
			}
		})
	}
	if len(warns) > 0 {
		res.Warn = T("dns.w.v6s", strings.Join(warns, ", "))
	}
	flushDNSCache()
	op.end(T("dns.done", name), nil)
	res.State = getNetState()
	return res
}

// dnsRestore puts back the servers each connection had before the first
// change.
func dnsRestore() dnsSetResult {
	dnsMu.Lock()
	defer dnsMu.Unlock()
	backup, _ := getSettings()["dnsBackup"].(map[string]any)
	if len(backup) == 0 {
		return dnsSetResult{OK: true, State: getNetState()}
	}
	byGUID := map[string]netAdapter{}
	for _, a := range listAdapters() {
		byGUID[a.GUID] = a
	}
	op := beginOp("network", "", T("log.dnsRestore"), nil)
	res := dnsSetResult{OK: true}
	var done []string
	for guid, v := range backup {
		b, _ := v.(map[string]any)
		a, ok := byGUID[guid]
		if !ok {
			// not connected now: its old setting stays in the backup
			op.warnf(str(b["name"]), T("dns.w.notnow"), nil)
			continue
		}
		v4, v6 := splitServers(str(b["v4"])), splitServers(str(b["v6"]))
		if _, err := applyDNS(a, v4, v6); err != nil {
			res.OK = false
			res.Error = a.Name + ": " + err.Error()
			op.fail(a.Name, err.Error(), nil)
			continue
		}
		done = append(done, guid)
		op.info(a.Name, T("dns.restored"), map[string]any{"v4": v4, "v6": v6})
	}
	updateSettings(func(m map[string]any) {
		cur, _ := m["dnsBackup"].(map[string]any)
		for _, g := range done {
			delete(cur, g)
		}
		if len(cur) == 0 {
			delete(m, "dnsBackup")
		}
	})
	flushDNSCache()
	op.end(T("dns.restored"), nil)
	res.State = getNetState()
	return res
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// dnsTest: the speed test, with the servers this PC uses now.
func dnsTest() []dnsResult {
	var own []string
	for _, a := range inUse(listAdapters()) {
		own = append(own, a.DNS...)
	}
	return dnsBench(udpExchange, own, 1500*time.Millisecond)
}

func dnsFlush() bool {
	ok := flushDNSCache()
	lvl := "info"
	if !ok {
		lvl = "warn"
	}
	logEvent("network", lvl, "", T("log.dnsFlush"), nil)
	return ok
}
