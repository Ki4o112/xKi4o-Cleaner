package main

import "testing"

func TestParseSDRConfig(t *testing.T) {
	b := []byte(`{"revision":1,"pops":{"fra":{"desc":"Frankfurt (Germany)","relays":[{"ipv4":"155.133.226.68","port_range":[27015,27060]}]},
		"ams":{"desc":"Amsterdam (Netherlands)","relays":[{"ipv4":"155.133.248.36"}]},"eat":{"desc":"Wenatchee","relays":[]},"xyz":{"relays":[{"ipv4":"1.2.3.4"}]}}}`)
	ts := parseSDRConfig(b)
	if len(ts) != 2 || ts[0].Code != "ams" || ts[1].Host != "155.133.226.68" {
		t.Fatalf("%+v", ts)
	}
	if parseSDRConfig([]byte("<html>")) != nil {
		t.Fatal("garbage")
	}
}

func TestMarkBest(t *testing.T) {
	rs := []gpResult{{Code: "a"}, {Code: "b"}, {Code: "c"}, {Code: "d"}}
	rs[0].Avg, rs[0].Loss = 40, 0
	rs[1].Avg, rs[1].Loss = 12, 60 // fast but loses most pings
	rs[2].Avg, rs[2].Loss = 25, 20
	rs[3].Avg = 0 // no answer
	markBest(rs)
	if !rs[2].Best || rs[0].Best || rs[1].Best || rs[3].Best {
		t.Fatalf("%+v", rs)
	}
}

func TestParseAVThreats(t *testing.T) {
	one := `{"id":"2147519003","name":"Virus:DOS/EICAR_Test_File","sev":5,"status":3,"active":false,"files":"file:_C:\\Users\\a\\Downloads\\eicar.com","at":1760000000000}`
	ts := parseAVThreats(one)
	if len(ts) != 1 || ts[0].Status != "done" || ts[0].Active || ts[0].Files[0] != `C:\Users\a\Downloads\eicar.com` {
		t.Fatalf("%+v", ts)
	}
	many := `[{"id":"1","name":"Trojan:Win32/X","sev":4,"status":1,"active":true,"files":["file:_C:\\a.exe","file:_C:\\b.dll"]},{"id":"2","name":"PUA:Win32/Y","sev":1,"status":103,"files":[]}]`
	ts = parseAVThreats(many)
	if len(ts) != 2 || !ts[0].Active || len(ts[0].Files) != 2 || ts[1].Status != "active" || !ts[1].Active {
		t.Fatalf("%+v", ts)
	}
	if ts := parseAVThreats(""); ts == nil || len(ts) != 0 {
		t.Fatal("empty")
	}
}

func TestHardwareRank(t *testing.T) {
	top, of := gpuRank(1e9)
	if top != 100 || of != len(gpuFPSData) {
		t.Fatalf("the fastest card: %d%% of %d", top, of)
	}
	if low, _ := gpuRank(0); low != 0 {
		t.Fatalf("the slowest card: %d%%", low)
	}
	g := gpuDataFor("NVIDIA GeForce RTX 3060 Ti")
	c := cpuDataFor("AMD Ryzen 5 2600 Six-Core Processor")
	if g == nil || c == nil {
		t.Fatal("test data missing")
	}
	gp, _ := gpuRank(g.FPS[0])
	cp, _ := cpuRank(c.Index)
	if gp <= 0 || gp >= 100 || cp < 0 || cp >= 100 {
		t.Fatalf("RTX 3060 Ti %d%%, Ryzen 5 2600 %d%%", gp, cp)
	}
}

func TestNewStartupKeys(t *testing.T) {
	if got := newStartupKeys(map[string]bool{}, []string{"a", "b"}); got != nil {
		t.Fatalf("the first look reports nothing: %v", got)
	}
	got := newStartupKeys(map[string]bool{"a": true}, []string{"c", "a", "b"})
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("%v", got)
	}
}

func TestGameSessionStats(t *testing.T) {
	s := gameSession{Game: "x", GPUMax: -1, GPUAvg: -1}
	s.add(40, 90, 70, 55)
	s.add(60, -1, 0, 60) // no graphics reading this time
	s.add(20, 70, 76, 50)
	if s.CPUMax != 60 || s.CPUAvg != 40 || s.GPUMax != 90 || s.GPUAvg != 80 || s.TempMax != 76 || s.MemMax != 60 {
		t.Fatalf("%+v", s)
	}
	var ss []gameSession
	for i := 0; i < 25; i++ {
		ss = keepSessions(ss, gameSession{Secs: i})
	}
	if len(ss) != 20 || ss[0].Secs != 24 {
		t.Fatalf("%d, newest %d", len(ss), ss[0].Secs)
	}
}
