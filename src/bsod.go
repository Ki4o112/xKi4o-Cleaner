package main

// Blue screens explained: Windows writes an event after every crash (WER
// "BugCheck" 1001: the stop code and its dump file) and after every start
// that follows a sudden power-off (Kernel-Power 41). The events are read as
// XML, which is the same in every Windows language. Each stop code gets a
// group (driver, graphics, memory…); the page explains the group in plain
// words with what helps. Free of Windows calls.
//
// shortcut: the guilty driver's name is not read from the dump file (that
// needs a debugger engine); upgrade when the app can parse kernel minidumps.

import (
	"encoding/xml"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type bsodEvent struct {
	At   int64  `json:"at"`             // unix ms
	Code string `json:"code,omitempty"` // "0x0000009F"; empty for a sudden power-off
	Name string `json:"name,omitempty"` // DRIVER_POWER_STATE_FAILURE
	Cat  string `json:"cat"`            // driver, gpu, ram, hw, disk, system, sleep, manual, power, other
	Dump string `json:"dump,omitempty"`
}

type bsodList struct {
	Events []bsodEvent `json:"events"`
	Error  string      `json:"error,omitempty"`
}

var bugChecks = map[uint32][2]string{ // code: {name, group}
	0x0A: {"IRQL_NOT_LESS_OR_EQUAL", "driver"}, 0xD1: {"DRIVER_IRQL_NOT_LESS_OR_EQUAL", "driver"},
	0x1E: {"KMODE_EXCEPTION_NOT_HANDLED", "driver"}, 0x3B: {"SYSTEM_SERVICE_EXCEPTION", "driver"},
	0x7E: {"SYSTEM_THREAD_EXCEPTION_NOT_HANDLED", "driver"}, 0x8E: {"KERNEL_MODE_EXCEPTION_NOT_HANDLED", "driver"},
	0xC2: {"BAD_POOL_CALLER", "driver"}, 0xC4: {"DRIVER_VERIFIER_DETECTED_VIOLATION", "driver"},
	0xCE: {"DRIVER_UNLOADED_WITHOUT_CANCELLING_PENDING_OPERATIONS", "driver"}, 0x133: {"DPC_WATCHDOG_VIOLATION", "driver"},
	0x139: {"KERNEL_SECURITY_CHECK_FAILURE", "driver"}, 0x13A: {"KERNEL_MODE_HEAP_CORRUPTION", "driver"},
	0x19: {"BAD_POOL_HEADER", "driver"}, 0xFC: {"ATTEMPTED_EXECUTE_OF_NOEXECUTE_MEMORY", "driver"},
	0x116: {"VIDEO_TDR_FAILURE", "gpu"}, 0x117: {"VIDEO_TDR_TIMEOUT_DETECTED", "gpu"},
	0x119: {"VIDEO_SCHEDULER_INTERNAL_ERROR", "gpu"}, 0x113: {"VIDEO_DXGKRNL_FATAL_ERROR", "gpu"},
	0x10E: {"VIDEO_MEMORY_MANAGEMENT_INTERNAL", "gpu"}, 0x141: {"VIDEO_ENGINE_TIMEOUT_DETECTED", "gpu"},
	0x1A: {"MEMORY_MANAGEMENT", "ram"}, 0x4E: {"PFN_LIST_CORRUPT", "ram"}, 0x50: {"PAGE_FAULT_IN_NONPAGED_AREA", "ram"},
	0x124: {"WHEA_UNCORRECTABLE_ERROR", "hw"}, 0x101: {"CLOCK_WATCHDOG_TIMEOUT", "hw"}, 0x9C: {"MACHINE_CHECK_EXCEPTION", "hw"},
	0x7A: {"KERNEL_DATA_INPAGE_ERROR", "disk"}, 0x77: {"KERNEL_STACK_INPAGE_ERROR", "disk"},
	0x7B: {"INACCESSIBLE_BOOT_DEVICE", "disk"}, 0x154: {"UNEXPECTED_STORE_EXCEPTION", "disk"}, 0x24: {"NTFS_FILE_SYSTEM", "disk"},
	0xEF: {"CRITICAL_PROCESS_DIED", "system"}, 0xF4: {"CRITICAL_OBJECT_TERMINATION", "system"},
	0xC000021A: {"STATUS_SYSTEM_PROCESS_TERMINATED", "system"}, 0x109: {"CRITICAL_STRUCTURE_CORRUPTION", "system"},
	0x9F: {"DRIVER_POWER_STATE_FAILURE", "sleep"}, 0xA0: {"INTERNAL_POWER_ERROR", "sleep"},
	0xE2: {"MANUALLY_INITIATED_CRASH", "manual"}, 0xDEADDEAD: {"MANUALLY_INITIATED_CRASH1", "manual"},
}

// bugCheck names a stop code and its group. 0x1000007E and the like are the
// same crash as 0x7E (the high bit only says the dump has more parameters).
func bugCheck(code uint32) (name, cat string) {
	if v, ok := bugChecks[code]; ok {
		return v[0], v[1]
	}
	if v, ok := bugChecks[code&^0x10000000]; ok && code&0x10000000 != 0 {
		return v[0], v[1]
	}
	return "", "other"
}

type evXML struct {
	Events []struct {
		Time struct {
			At string `xml:"SystemTime,attr"`
		} `xml:"System>TimeCreated"`
		Data []struct {
			Name  string `xml:"Name,attr"`
			Value string `xml:",chardata"`
		} `xml:"EventData>Data"`
	} `xml:"Event"`
}

var reStop = regexp.MustCompile(`0x([0-9A-Fa-f]{1,8})\b`)

// parseBugcheckEvents reads "wevtutil qe /f:xml" output (events one after
// another, no root element). power: Kernel-Power 41 events, of which only the
// ones with no stop code are kept (a crash already has its 1001 event).
func parseBugcheckEvents(out string, power bool) []bsodEvent {
	var v evXML
	if xml.Unmarshal([]byte("<r>"+out+"</r>"), &v) != nil {
		return nil
	}
	var res []bsodEvent
	for _, e := range v.Events {
		t, err := time.Parse(time.RFC3339Nano, e.Time.At)
		if err != nil {
			continue
		}
		ev := bsodEvent{At: t.UnixMilli()}
		if power {
			code := ""
			for _, d := range e.Data {
				if d.Name == "BugcheckCode" {
					code = strings.TrimSpace(d.Value)
				}
			}
			if code != "0" {
				continue
			}
			ev.Cat = "power"
			res = append(res, ev)
			continue
		}
		for _, d := range e.Data {
			s := strings.TrimSpace(d.Value)
			if strings.HasSuffix(strings.ToLower(s), ".dmp") {
				ev.Dump = s
			} else if ev.Code == "" {
				if m := reStop.FindStringSubmatch(s); m != nil {
					if n, err := strconv.ParseUint(m[1], 16, 32); err == nil {
						ev.Code = "0x" + strings.ToUpper(strconv.FormatUint(n, 16))
						for len(ev.Code) < 10 {
							ev.Code = "0x0" + ev.Code[2:]
						}
						ev.Name, ev.Cat = bugCheck(uint32(n))
					}
				}
			}
		}
		if ev.Code != "" {
			res = append(res, ev)
		}
	}
	return res
}

func sortBsod(es []bsodEvent) {
	sort.Slice(es, func(i, j int) bool { return es[i].At > es[j].At })
}
