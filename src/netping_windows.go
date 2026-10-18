//go:build windows

package main

import (
	"encoding/binary"
	"net"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modIphlpapi         = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = modIphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = modIphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = modIphlpapi.NewProc("IcmpSendEcho")
)

// icmpPing sends one echo and returns the round trip in ms (-1: no answer).
func icmpPing(h uintptr, ip net.IP, timeout time.Duration) float64 {
	ip4 := ip.To4()
	if ip4 == nil {
		return -1
	}
	data := []byte("xKi4o-Cleaner-ping")
	reply := make([]byte, 256)
	n, _, _ := procIcmpSendEcho.Call(h, uintptr(binary.LittleEndian.Uint32(ip4)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0,
		uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(timeout.Milliseconds()))
	if n == 0 {
		return -1
	}
	// ICMP_ECHO_REPLY: Address, Status, RoundTripTime (all ULONG)
	if binary.LittleEndian.Uint32(reply[4:8]) != 0 {
		return -1
	}
	return float64(binary.LittleEndian.Uint32(reply[8:12]))
}

func gatewayIP() net.IP {
	size := uint32(16 << 10)
	for i := 0; i < 4; i++ {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		if err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_INCLUDE_GATEWAYS, 0, aa, &size); err != nil {
			continue
		}
		for a := aa; a != nil; a = a.Next {
			if a.OperStatus != windows.IfOperStatusUp {
				continue
			}
			for g := a.FirstGatewayAddress; g != nil; g = g.Next {
				if ip := g.Address.IP(); ip != nil && ip.To4() != nil && !ip.IsUnspecified() {
					return ip
				}
			}
		}
		return nil
	}
	return nil
}

// netPing: 12 pings to each target at once (about 3 seconds, up to 8 when
// nothing answers). Read only.
func netPing() []pingResult {
	targets := []struct{ id, host string }{{"router", ""}, {"cloudflare", "1.1.1.1"}, {"google", "8.8.8.8"}}
	if gw := gatewayIP(); gw != nil {
		targets[0].host = gw.String()
	}
	out := make([]pingResult, len(targets))
	var wg sync.WaitGroup
	for i, tg := range targets {
		wg.Add(1)
		go func(i int, id, host string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					out[i] = pingResult{ID: id, Host: host, Error: T("err.unexpected", r)}
				}
			}()
			if host == "" {
				out[i] = pingResult{ID: id, Error: T("np.noRouter")}
				return
			}
			h, _, err := procIcmpCreateFile.Call()
			if h == 0 || h == uintptr(windows.InvalidHandle) {
				out[i] = pingResult{ID: id, Host: host, Error: T("np.noIcmp", err)}
				return
			}
			defer procIcmpCloseHandle.Call(h) //nolint:errcheck
			ip := net.ParseIP(host)
			rtts := make([]float64, 0, 12)
			for k := 0; k < 12; k++ {
				start := time.Now()
				rtts = append(rtts, icmpPing(h, ip, 600*time.Millisecond))
				if d := 250*time.Millisecond - time.Since(start); d > 0 {
					time.Sleep(d)
				}
			}
			r := pingStats(rtts)
			r.ID, r.Host = id, host
			out[i] = r
		}(i, tg.id, tg.host)
	}
	wg.Wait()
	logEvent("network", "info", "", T("log.netPing"), map[string]any{"results": out})
	return out
}
