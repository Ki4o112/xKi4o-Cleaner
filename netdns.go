package main

// DNS: the public DNS servers people pick, a speed test that asks each one
// the same names and times the answers, and the small parts of the DNS
// wire format the test needs. Free of Windows calls (tested anywhere).

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

type dnsProvider struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	V4   []string `json:"v4"`
	V6   []string `json:"v6"`
	Note string   `json:"note"` // a text key: what it is good for
}

// The providers on offer. Each one is a free public service; the addresses
// are their published ones.
var dnsProviders = []dnsProvider{
	{ID: "cloudflare", Name: "Cloudflare", V4: []string{"1.1.1.1", "1.0.0.1"}, V6: []string{"2606:4700:4700::1111", "2606:4700:4700::1001"}, Note: "dns.n.cloudflare"},
	{ID: "google", Name: "Google", V4: []string{"8.8.8.8", "8.8.4.4"}, V6: []string{"2001:4860:4860::8888", "2001:4860:4860::8844"}, Note: "dns.n.google"},
	{ID: "quad9", Name: "Quad9", V4: []string{"9.9.9.9", "149.112.112.112"}, V6: []string{"2620:fe::fe", "2620:fe::9"}, Note: "dns.n.quad9"},
	{ID: "adguard", Name: "AdGuard", V4: []string{"94.140.14.14", "94.140.15.15"}, V6: []string{"2a10:50c0::ad1:ff", "2a10:50c0::ad2:ff"}, Note: "dns.n.adguard"},
	{ID: "opendns", Name: "OpenDNS", V4: []string{"208.67.222.222", "208.67.220.220"}, V6: []string{"2620:119:35::35", "2620:119:53::53"}, Note: "dns.n.opendns"},
	{ID: "cloudflare-family", Name: "Cloudflare Family", V4: []string{"1.1.1.3", "1.0.0.3"}, V6: []string{"2606:4700:4700::1113", "2606:4700:4700::1003"}, Note: "dns.n.family"},
}

func providerByID(id string) (dnsProvider, bool) {
	for _, p := range dnsProviders {
		if p.ID == id {
			return p, true
		}
	}
	return dnsProvider{}, false
}

// providerOf: which provider these DNS servers belong to ("" when none).
func providerOf(servers []string) string {
	if len(servers) == 0 {
		return ""
	}
	for _, p := range dnsProviders {
		all := map[string]bool{}
		for _, a := range append(append([]string{}, p.V4...), p.V6...) {
			all[normIP(a)] = true
		}
		mine := true
		for _, s := range servers {
			if !all[normIP(s)] {
				mine = false
				break
			}
		}
		if mine {
			return p.ID
		}
	}
	return ""
}

func normIP(s string) string {
	if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil {
		return ip.String()
	}
	return strings.TrimSpace(s)
}

// splitServers reads the DNS server list Windows keeps in the registry
// ("1.1.1.1,1.0.0.1" or space separated).
func splitServers(s string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if ip := net.ParseIP(f); ip != nil {
			out = append(out, ip.String())
		}
	}
	return out
}

// The names the speed test asks for: sites people open every day, so a
// good server answers them from its cache, as it does in real use.
var dnsTestNames = []string{"google.com", "youtube.com", "microsoft.com", "wikipedia.org", "github.com", "steampowered.com", "discord.com"}

// dnsQuery builds a standard query for an A record.
func dnsQuery(id uint16, name string) ([]byte, error) {
	b := make([]byte, 12, 64)
	binary.BigEndian.PutUint16(b[0:], id)
	binary.BigEndian.PutUint16(b[2:], 0x0100) // recursion desired
	binary.BigEndian.PutUint16(b[4:], 1)      // one question
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if l == "" || len(l) > 63 {
			return nil, errors.New("bad name")
		}
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	b = append(b, 0, 0, 1, 0, 1) // end of name, type A, class IN
	return b, nil
}

// dnsAnswered: the reply is an answer to query id (any result code counts:
// the server answered).
func dnsAnswered(reply []byte, id uint16) bool {
	return len(reply) >= 12 && binary.BigEndian.Uint16(reply[0:]) == id && reply[2]&0x80 != 0
}

type dnsResult struct {
	Server string `json:"server"`
	ID     string `json:"id"` // the provider; "" for the PC's own server
	Ms     int    `json:"ms"` // median answer time; -1 when it did not answer
	Lost   int    `json:"lost"`
	Of     int    `json:"of"`
}

// dnsExchange sends one query over UDP and waits for its answer.
type dnsExchange func(server string, q []byte, id uint16, timeout time.Duration) (time.Duration, error)

func udpExchange(server string, q []byte, id uint16, timeout time.Duration) (time.Duration, error) {
	return udpExchangePort(server, "53", q, id, timeout)
}

func udpExchangePort(server, port string, q []byte, id uint16, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(server, port))
	if err != nil {
		return 0, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	start := time.Now()
	if _, err := c.Write(q); err != nil {
		return 0, err
	}
	buf := make([]byte, 1500)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return 0, err
		}
		if dnsAnswered(buf[:n], id) {
			return time.Since(start), nil
		}
	}
}

func randID() uint16 {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint16(b[:])
}

// dnsBenchServer asks one server every test name (one warm-up first, not
// counted) and gives the median time.
func dnsBenchServer(ex dnsExchange, server string, names []string, timeout time.Duration) dnsResult {
	r := dnsResult{Server: server, Of: len(names), Ms: -1}
	var times []time.Duration
	for i := -1; i < len(names); i++ {
		name := names[0]
		if i >= 0 {
			name = names[i]
		}
		id := randID()
		q, err := dnsQuery(id, name)
		if err != nil {
			continue
		}
		d, err := ex(server, q, id, timeout)
		if i < 0 {
			continue // the warm-up
		}
		if err != nil {
			r.Lost++
			continue
		}
		times = append(times, d)
	}
	if len(times) > 0 {
		sort.Slice(times, func(a, b int) bool { return times[a] < times[b] })
		m := times[len(times)/2]
		if len(times)%2 == 0 {
			m = (times[len(times)/2-1] + times[len(times)/2]) / 2
		}
		r.Ms = int((m + 500*time.Microsecond) / time.Millisecond)
		if r.Ms == 0 {
			r.Ms = 1
		}
	}
	return r
}

// dnsBench tests the providers and the PC's own servers side by side, the
// fastest first; servers that did not answer go last.
func dnsBench(ex dnsExchange, own []string, timeout time.Duration) []dnsResult {
	type job struct{ server, id string }
	var jobs []job
	seen := map[string]bool{}
	for _, s := range own {
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil && !seen[ip.String()] {
			seen[ip.String()] = true
			jobs = append(jobs, job{ip.String(), ""})
		}
	}
	for _, p := range dnsProviders {
		if !seen[p.V4[0]] {
			seen[p.V4[0]] = true
			jobs = append(jobs, job{p.V4[0], p.ID})
		} else {
			for i := range jobs {
				if jobs[i].server == p.V4[0] {
					jobs[i].id = p.ID
				}
			}
		}
	}
	res := make([]dnsResult, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			res[i] = dnsBenchServer(ex, j.server, dnsTestNames, timeout)
			res[i].ID = j.id
		}(i, j)
	}
	wg.Wait()
	// answered first, then the ones that lost at most one answer, each by
	// speed (a server that drops answers is not fast)
	bucket := func(r dnsResult) int {
		switch {
		case r.Ms < 0:
			return 2
		case r.Lost > 1:
			return 1
		}
		return 0
	}
	sort.SliceStable(res, func(a, b int) bool {
		if ba, bb := bucket(res[a]), bucket(res[b]); ba != bb {
			return ba < bb
		}
		return res[a].Ms < res[b].Ms
	})
	return res
}
