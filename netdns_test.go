package main

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDNSQueryFormat(t *testing.T) {
	q, err := dnsQuery(0x1234, "www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := "\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x03www\x07example\x03com\x00\x00\x01\x00\x01"
	if string(q) != want {
		t.Errorf("query % x", q)
	}
	if _, err := dnsQuery(1, "a..b"); err == nil {
		t.Error("an empty label was accepted")
	}
	if _, err := dnsQuery(1, strings.Repeat("x", 64)+".com"); err == nil {
		t.Error("a 64-letter label was accepted")
	}
	reply := append([]byte{}, q...)
	reply[2] |= 0x80
	if !dnsAnswered(reply, 0x1234) || dnsAnswered(q, 0x1234) || dnsAnswered(reply, 0x1235) || dnsAnswered(reply[:5], 0x1234) {
		t.Error("dnsAnswered")
	}
}

func TestProviders(t *testing.T) {
	ids := map[string]bool{}
	for _, p := range dnsProviders {
		if ids[p.ID] || len(p.V4) != 2 || len(p.V6) != 2 || p.Note == "" {
			t.Errorf("provider %+v", p)
		}
		ids[p.ID] = true
		for _, a := range append(append([]string{}, p.V4...), p.V6...) {
			if net.ParseIP(a) == nil {
				t.Errorf("%s: %q is not an address", p.ID, a)
			}
		}
		for _, a := range p.V4 {
			if net.ParseIP(a).To4() == nil {
				t.Errorf("%s: %s is not IPv4", p.ID, a)
			}
		}
		for _, a := range p.V6 {
			if net.ParseIP(a).To4() != nil {
				t.Errorf("%s: %s is not IPv6", p.ID, a)
			}
		}
	}
	if providerOf([]string{"1.1.1.1", "1.0.0.1"}) != "cloudflare" || providerOf([]string{"8.8.8.8", "2001:4860:4860:0:0:0:0:8888"}) != "google" {
		t.Error("providerOf known")
	}
	if providerOf([]string{"1.1.1.1", "8.8.8.8"}) != "" || providerOf(nil) != "" || providerOf([]string{"192.168.1.1"}) != "" {
		t.Error("providerOf mixed or own")
	}
	if got := splitServers("1.1.1.1,1.0.0.1 2606:4700:4700::1111;bad"); strings.Join(got, "|") != "1.1.1.1|1.0.0.1|2606:4700:4700::1111" {
		t.Errorf("splitServers %v", got)
	}
	if got := splitServers(""); got == nil || len(got) != 0 {
		t.Errorf("splitServers empty %v", got)
	}
}

// fakeDNS answers on 127.0.0.1 after delay (or never, when silent).
func fakeDNS(t *testing.T, delay time.Duration, silent bool) string {
	t.Helper()
	pc, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no UDP here:", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if silent || n < 12 {
				continue
			}
			r := append([]byte{}, buf[:n]...)
			r[2] |= 0x80
			go func() { time.Sleep(delay); _, _ = pc.WriteTo(r, addr) }()
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).String()
}

// The speed test over real UDP to local servers: the times come out in
// order, a server that never answers is last with -1, and nothing waits
// longer than the timeout.
func TestDNSBench(t *testing.T) {
	fast := fakeDNS(t, 5*time.Millisecond, false)
	slow := fakeDNS(t, 60*time.Millisecond, false)
	dead := fakeDNS(t, 0, true)
	// the bench asks "1.1.1.1" etc.; here each name is routed to a local server
	route := map[string]string{}
	ex := func(server string, q []byte, id uint16, timeout time.Duration) (time.Duration, error) {
		host, port, _ := net.SplitHostPort(route[server])
		return udpExchangePort(host, port, q, id, timeout)
	}
	for i, p := range dnsProviders {
		route[p.V4[0]] = []string{slow, fast, dead}[i%3]
	}
	route["192.168.1.1"] = fast
	start := time.Now()
	res := dnsBench(ex, []string{"192.168.1.1", "192.168.1.1", "fe80::1", "garbage"}, 300*time.Millisecond)
	took := time.Since(start)
	if len(res) != len(dnsProviders)+1 {
		t.Fatalf("%d results: %+v", len(res), res)
	}
	if res[0].Ms < 0 || res[0].Ms > 40 {
		t.Errorf("fastest %+v", res[0])
	}
	last := res[len(res)-1]
	if last.Ms != -1 || last.Lost != last.Of {
		t.Errorf("a silent server must be last with no time: %+v", last)
	}
	prev := -1
	for _, r := range res {
		if r.Ms >= 0 {
			if r.Ms < prev {
				t.Errorf("not in order: %+v", res)
			}
			prev = r.Ms
		}
	}
	own := 0
	for _, r := range res {
		if r.Server == "192.168.1.1" {
			own++
			if r.ID != "" {
				t.Errorf("the PC's own server got a provider: %+v", r)
			}
		}
	}
	if own != 1 {
		t.Errorf("the PC's own server %d times (twice in the list, IPv6 and junk skipped)", own)
	}
	// all servers run side by side: the silent ones cost one round of timeouts, not one per server
	if limit := time.Duration(len(dnsTestNames)+1) * 300 * time.Millisecond * 2; took > limit {
		t.Errorf("took %v (limit %v)", took, limit)
	}
	t.Logf("%+v in %v", res, took)
	// a reply with another id is not taken as the answer
	q, _ := dnsQuery(7, "x.com")
	other := append([]byte{}, q...)
	binary.BigEndian.PutUint16(other, 8)
	if dnsAnswered(other, 7) {
		t.Error("another id")
	}
}
