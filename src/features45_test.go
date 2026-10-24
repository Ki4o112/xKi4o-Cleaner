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
