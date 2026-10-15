package main

import (
	"strings"
	"testing"
)

// pad lays out a table row the way winget does: each column padded to its width (in runes)
func pad(widths []int, cols ...string) string {
	var b strings.Builder
	for i, c := range cols {
		b.WriteString(c)
		if i < len(cols)-1 {
			b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(c))))
		}
	}
	return b.String() + "\r\n"
}

// what "winget upgrade" prints (spinner and all), English and Bulgarian headers
var (
	w1           = []int{39, 34, 15, 15}
	w2           = []int{10, 16, 9, 10}
	wingetSample = "\r   - \r   \\ \r   | \r                                                                                                                        \r" +
		pad(w1, "Name", "Id", "Version", "Available", "Source") +
		strings.Repeat("-", 111) + "\r\n" +
		pad(w1, "Mozilla Firefox (x64 en-US)", "Mozilla.Firefox", "118.0.1", "119.0", "winget") +
		pad(w1, "Microsoft Visual Studio Code (User)", "Microsoft.VisualStudioCode", "1.83.0", "1.84.2", "winget") +
		pad(w1, "Microsoft Visual C++ 2015-2022 Redist…", "Microsoft.VCRedist.2015+.x64", "14.36.32532.0", "14.38.33130.0", "winget") +
		pad(w1, "Някаква програма", "Microsoft.VisualStudio.2022.Comm…", "17.7.4", "17.8.0", "winget") +
		"4 upgrades available.\r\n" +
		"\r\n" +
		"The following packages have an upgrade available, but require explicit targeting for upgrade:\r\n" +
		pad(w2, "Name", "Id", "Version", "Available", "Source") +
		strings.Repeat("-", 51) + "\r\n" +
		pad(w2, "Discord", "Discord.Discord", "1.0.9013", "1.0.9020", "winget")
	wingetSampleBG = pad([]int{25, 20, 10, 10}, "Име", "ИД", "Версия", "Налично", "Източник") +
		strings.Repeat("-", 74) + "\r\n" +
		pad([]int{25, 20, 10, 10}, "7-Zip 23.01 (x64)", "7zip.7zip", "23.01", "24.08", "winget") +
		"Няма налични надстройки.\r\n"
)

func TestParseWingetUpgrade(t *testing.T) {
	got := parseWingetUpgrade(wingetSample)
	if len(got) != 5 {
		t.Fatalf("got %d rows: %+v", len(got), got)
	}
	want := []wgUpdate{
		{Name: "Mozilla Firefox (x64 en-US)", ID: "Mozilla.Firefox", Version: "118.0.1", Available: "119.0", Source: "winget"},
		{Name: "Microsoft Visual Studio Code (User)", ID: "Microsoft.VisualStudioCode", Version: "1.83.0", Available: "1.84.2", Source: "winget"},
		{Name: "Microsoft Visual C++ 2015-2022 Redist…", ID: "Microsoft.VCRedist.2015+.x64", Version: "14.36.32532.0", Available: "14.38.33130.0", Source: "winget"},
		{Name: "Някаква програма", ID: "Microsoft.VisualStudio.2022.Comm…", Version: "17.7.4", Available: "17.8.0", Source: "winget"},
		{Name: "Discord", ID: "Discord.Discord", Version: "1.0.9013", Available: "1.0.9020", Source: "winget", Explicit: true},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
	bg := parseWingetUpgrade(wingetSampleBG)
	if len(bg) != 1 || bg[0].ID != "7zip.7zip" || bg[0].Available != "24.08" {
		t.Errorf("Bulgarian table: %+v", bg)
	}
	if n := parseWingetUpgrade("No installed package found matching input criteria.\r\n"); len(n) != 0 {
		t.Errorf("no table: %+v", n)
	}
	// a table with no rows
	if n := parseWingetUpgrade("Name Id Version Available Source\n-------------------------------------\nNo applicable upgrade found.\n"); len(n) != 0 {
		t.Errorf("empty table: %+v", n)
	}
}

func TestFullIDs(t *testing.T) {
	ups := parseWingetUpgrade(wingetSample)
	export := `{"$schema":"x","Sources":[{"Packages":[{"PackageIdentifier":"Mozilla.Firefox","Version":"118.0.1"},
	 {"PackageIdentifier":"Microsoft.VisualStudio.2022.Community","Version":"17.7.4"}],"SourceDetails":{"Name":"winget"}}]}`
	ups = fullIDs(ups, []byte(export))
	if ups[3].ID != "Microsoft.VisualStudio.2022.Community" || ups[3].Cut {
		t.Errorf("cut id not completed: %+v", ups[3])
	}
	ups2 := fullIDs(parseWingetUpgrade(wingetSample), []byte(`{}`))
	if !ups2[3].Cut {
		t.Errorf("an id that cannot be completed must be marked: %+v", ups2[3])
	}
}

func TestWingetMessageAndIDs(t *testing.T) {
	out := "Found Mozilla Firefox [Mozilla.Firefox] Version 119.0\r\n  ██████████████████████████████  55.2 MB / 55.2 MB\r\nSuccessfully installed\r\n\r\n"
	if m := wingetMessage(out); m != "Successfully installed" {
		t.Errorf("message = %q", m)
	}
	for _, id := range []string{"Mozilla.Firefox", "Microsoft.VCRedist.2015+.x64", "7zip.7zip", "XP89DCGQ3K6VLD"} {
		if !validWingetID(id) {
			t.Errorf("%s should be valid", id)
		}
	}
	for _, id := range []string{"", "--all", "a b", "x;calc", `ARP\Machine\X64\{1}`, "a\"b", strings.Repeat("a", 200)} {
		if validWingetID(id) {
			t.Errorf("%q must be refused", id)
		}
	}
}
