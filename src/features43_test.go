package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseWlanProfile(t *testing.T) {
	x := `<?xml version="1.0"?>
<WLANProfile xmlns="http://www.microsoft.com/networking/WLAN/profile/v1">
	<name>Home 5G</name>
	<SSIDConfig><SSID><hex>486F6D65</hex><name>Home 5G</name></SSID></SSIDConfig>
	<MSM><security>
		<authEncryption><authentication>WPA2PSK</authentication><encryption>AES</encryption></authEncryption>
		<sharedKey><keyType>passPhrase</keyType><protected>false</protected><keyMaterial>s3cret pass</keyMaterial></sharedKey>
	</security></MSM>
</WLANProfile>`
	n, ok := parseWlanProfile([]byte(x))
	if !ok || n.Name != "Home 5G" || n.Key != "s3cret pass" || n.Auth != "WPA2PSK" || n.Open {
		t.Fatalf("%+v %v", n, ok)
	}
	// still encrypted (no admin rights): the key is never passed on
	n, _ = parseWlanProfile([]byte(strings.Replace(x, "<protected>false", "<protected>true", 1)))
	if n.Key != "" {
		t.Fatal("protected key shown")
	}
	open := `<WLANProfile><name>Cafe</name><MSM><security><authEncryption><authentication>open</authentication></authEncryption></security></MSM></WLANProfile>`
	if n, ok := parseWlanProfile([]byte(open)); !ok || !n.Open || n.Key != "" {
		t.Fatalf("%+v", n)
	}
	if _, ok := parseWlanProfile([]byte("not xml")); ok {
		t.Fatal("garbage accepted")
	}
}

func TestPwnedCheck(t *testing.T) {
	// SHA-1("password") = 5BAA61E4C9B93F3F0682250B6CF8331B7EE68FD8
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		if r.Header.Get("Add-Padding") != "true" {
			t.Error("no padding asked")
		}
		fmt.Fprint(w, "0018A45C4D1DEF81644B54AB7F969B88D65:0\r\n1E4C9B93F3F0682250B6CF8331B7EE68FD8:9659365\r\n")
	}))
	defer srv.Close()
	old := pwnedAPI
	pwnedAPI = srv.URL + "/range/"
	defer func() { pwnedAPI = old }()
	if r := pwnedCheck("password"); r.Count != 9659365 || r.Error != "" {
		t.Fatalf("%+v", r)
	}
	if asked != "/range/5BAA6" {
		t.Fatalf("sent %q: only 5 characters of the hash may leave the PC", asked)
	}
	if r := pwnedCheck("a much less common phrase 81!"); r.Count != 0 {
		t.Fatalf("%+v", r)
	}
	asked = ""
	if r := pwnedCheck(""); r.Count != 0 || asked != "" {
		t.Fatal("an empty password must not be sent")
	}
}

func TestParseBugcheckEvents(t *testing.T) {
	ev := func(at, data string) string {
		return `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='x'/><EventID>1001</EventID><TimeCreated SystemTime='` + at + `'/></System><EventData>` + data + `</EventData></Event>`
	}
	out := ev("2026-10-01T10:00:00.1234567Z", `<Data Name='param1'>0x00000116 (0xffffd00f, 0xfffff80, 0x0, 0x2)</Data><Data Name='param2'>C:\WINDOWS\Minidump\100126-1-01.dmp</Data><Data Name='param3'>abc</Data>`) +
		ev("2026-10-05T10:00:00Z", `<Data Name='param1'>0x1000007e (0xc0000005, 0x0, 0x0, 0x0)</Data><Data Name='param2'>C:\WINDOWS\MEMORY.DMP</Data>`) +
		ev("2026-10-06T10:00:00Z", `<Data Name='param1'>0x00000abc (0x0)</Data>`)
	es := parseBugcheckEvents(out, false)
	sortBsod(es)
	if len(es) != 3 {
		t.Fatalf("%+v", es)
	}
	if es[2].Code != "0x00000116" || es[2].Cat != "gpu" || es[2].Name != "VIDEO_TDR_FAILURE" || !strings.HasSuffix(es[2].Dump, "100126-1-01.dmp") {
		t.Errorf("%+v", es[2])
	}
	if es[1].Code != "0x1000007E" || es[1].Cat != "driver" {
		t.Errorf("%+v", es[1])
	}
	if es[0].Cat != "other" || es[0].Name != "" {
		t.Errorf("%+v", es[0])
	}
	power := ev("2026-10-07T10:00:00Z", `<Data Name='BugcheckCode'>0</Data><Data Name='PowerButtonTimestamp'>0</Data>`) +
		ev("2026-10-08T10:00:00Z", `<Data Name='BugcheckCode'>278</Data>`)
	if ps := parseBugcheckEvents(power, true); len(ps) != 1 || ps[0].Cat != "power" {
		t.Errorf("only power-offs without a stop code: %+v", ps)
	}
	if parseBugcheckEvents("", false) != nil {
		t.Error("no events")
	}
}

func TestFpsBook(t *testing.T) {
	b := newFpsBook()
	t0 := time.Unix(1000, 0)
	b.line("Application,ProcessID,SwapChainAddress,PresentRuntime,SyncInterval,MsBetweenPresents,MsInPresentAPI", t0)
	b.line("Started recording.", t0) // a message, not a frame
	// the game: 100 frames in the last second, 8 of them slow (25 ms)
	for i := 0; i < 100; i++ {
		ms := "9.2"
		if i%12 == 0 {
			ms = "25.0"
		}
		b.line("game.exe,4242,0x1,DXGI,0,"+ms+",0.1", t0.Add(time.Duration(i)*10*time.Millisecond))
		b.line("dwm.exe,77,0x2,Other,0,6.9,0.1", t0.Add(time.Duration(i)*10*time.Millisecond))
	}
	now := t0.Add(time.Second)
	v, ok := b.now(0, now)
	if !ok || v.App != "game.exe" || v.PID != 4242 {
		t.Fatalf("%+v", v)
	}
	if v.FPS < 90 || v.FPS > 95 || v.Low1 < 39 || v.Low1 > 41 {
		t.Errorf("fps %.1f, 1%% low %.1f", v.FPS, v.Low1)
	}
	// Windows' own compositor is never the game, even in front
	if v, _ := b.now(77, now); v.App != "game.exe" {
		t.Errorf("%+v", v)
	}
	// nothing new for over 10 seconds: closed
	if _, ok := b.now(0, now.Add(12*time.Second)); ok {
		t.Error("a closed game still counted")
	}
}

func TestWinfrArgs(t *testing.T) {
	a, bad := winfrArgs(recoverReq{Src: "c", Dst: "D:", Kinds: []string{"photos"}, Name: "holiday"})
	j := strings.Join(a, " ")
	if bad != "" || !strings.HasPrefix(j, "C: D: /regular /a /n *holiday* /n *.jpg") || !strings.Contains(j, "/n *.heic") {
		t.Fatalf("%q %s", j, bad)
	}
	if a, _ := winfrArgs(recoverReq{Src: "E", Dst: "C", Kinds: []string{"docs"}, Deep: true}); a[2] != "/extensive" {
		t.Error(a)
	}
	for _, r := range []recoverReq{
		{Src: "C", Dst: "C", Kinds: []string{"docs"}},
		{Src: "C", Dst: "D"},
		{Src: "CD", Dst: "E", Kinds: []string{"docs"}},
		{Src: "C", Dst: "D", Name: `x" /k`},
		{Src: "C", Dst: "D", Name: `..\..\x`},
	} {
		if _, bad := winfrArgs(r); bad == "" {
			t.Errorf("accepted %+v", r)
		}
	}
	if winfrPercent("Pass 1: Scanning disk:  45%") != 45 || winfrPercent("Files recovered: 3") != -1 {
		t.Error("percent")
	}
}

func TestWingetSource(t *testing.T) {
	for id, want := range map[string]string{winfrStoreID: "msstore", "XPDC2RH70K22MN": "msstore", "XP8K0HKJFRXGCK": "msstore", "XP8K0HKJFRXGC": "winget",
		"9NKSQGP7F2NH": "msstore", "Mozilla.Firefox": "winget", "7zip.7zip": "winget", "9n26s50ln705": "winget"} {
		if got := wingetSource(id); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
}
