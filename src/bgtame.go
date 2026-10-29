package main

// Windows' heavy background work (search indexing, update installs,
// telemetry, Defender's scheduled scan, OneDrive sync) runs at the lowest
// priority while someone uses the PC or plays, and gets its usual priority
// back once the PC is left alone. Nothing is stopped or turned off: the work
// only waits for free time. Free of Windows calls.

import "strings"

// bgHeavy: the processes that do such work.
var bgHeavy = map[string]bool{
	"searchindexer.exe": true, "searchprotocolhost.exe": true, "searchfilterhost.exe": true,
	"tiworker.exe": true, "trustedinstaller.exe": true, "mousocoreworker.exe": true, "usoclient.exe": true, "wuauclt.exe": true,
	"compattelrunner.exe": true, "devicecensus.exe": true, "mpcmdrun.exe": true, "mrt.exe": true,
	"onedrive.exe": true, "filecoauth.exe": true, "dismhost.exe": true, "sdxhelper.exe": true, "officeclicktorun.exe": true,
	"googleupdate.exe": true, "microsoftedgeupdate.exe": true, "msedgeupdate.exe": true,
}

const bgIdleAfter = 3 * 60 // seconds without input: the PC is left alone

func isBgHeavy(name string) bool { return bgHeavy[strings.ToLower(name)] }

// bgShouldTame: hold the work back while the PC is used or a game runs.
func bgShouldTame(idleSecs int, playing bool) bool { return playing || idleSecs < bgIdleAfter }
