//go:build windows

package main

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// sysInfoScript reads everything in one PowerShell run (CIM). Each part
// that fails is left empty; serial numbers and MAC addresses are not read.
const sysInfoScript = `$ErrorActionPreference = 'SilentlyContinue'
function siQ($c, $ns) { if ($ns) { @(Get-CimInstance -Namespace $ns -ClassName $c -ErrorAction SilentlyContinue) } else { @(Get-CimInstance -ClassName $c -ErrorAction SilentlyContinue) } }
function siD($d) { if ($d) { try { ([datetime]$d).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ') } catch { '' } } else { '' } }
function siS($v) { if ($null -eq $v) { '' } else { ([string]$v).Trim() } }
function siN($v) { if ($v -is [byte[]]) { if ($v.Length -ge 8) { [BitConverter]::ToInt64($v, 0) } elseif ($v.Length -ge 4) { [int64][BitConverter]::ToUInt32($v, 0) } else { [int64]0 } } elseif ($v) { [int64]$v } else { [int64]0 } }
function siA($a) { if ($a) { (@($a) | Where-Object { $_ -ne 0 } | ForEach-Object { [char][int]$_ }) -join '' } else { '' } }
$o = [ordered]@{}
$os = siQ Win32_OperatingSystem | Select-Object -First 1
$cv = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
$dv = if ($cv.DisplayVersion) { $cv.DisplayVersion } else { $cv.ReleaseId }
$o.os = @{ name = siS $os.Caption; ver = siS $dv; build = siS $os.BuildNumber; ubr = [int]$cv.UBR; arch = siS $os.OSArchitecture; installed = siD $os.InstallDate; boot = siD $os.LastBootUpTime; pc = siS $env:COMPUTERNAME }
$cs = siQ Win32_ComputerSystem | Select-Object -First 1
$hv = [bool]$cs.HypervisorPresent
$o.cpu = @(siQ Win32_Processor | ForEach-Object { @{ name = siS $_.Name; cores = [int]$_.NumberOfCores; threads = [int]$_.NumberOfLogicalProcessors; mhz = [int]$_.MaxClockSpeed; l2 = [int]$_.L2CacheSize; l3 = [int]$_.L3CacheSize; socket = siS $_.SocketDesignation; virt = ([bool]$_.VirtualizationFirmwareEnabled -or $hv) } })
$vram = @{}
Get-ChildItem 'HKLM:\SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}' -ErrorAction SilentlyContinue | ForEach-Object {
  $p = Get-ItemProperty $_.PSPath -ErrorAction SilentlyContinue
  if ($p -and $p.DriverDesc) { $q = siN $p.'HardwareInformation.qwMemorySize'; if (-not $q) { $q = siN $p.'HardwareInformation.MemorySize' }; if ($q -gt 0) { $vram[[string]$p.DriverDesc] = $q } }
}
$o.gpu = @(siQ Win32_VideoController | ForEach-Object { $r = [int64]$_.AdapterRAM; if ($vram.ContainsKey([string]$_.Name)) { $r = $vram[[string]$_.Name] }; @{ name = siS $_.Name; driver = siS $_.DriverVersion; date = siD $_.DriverDate; ram = $r; w = [int]$_.CurrentHorizontalResolution; h = [int]$_.CurrentVerticalResolution; hz = [int]$_.CurrentRefreshRate } })
$o.ram = @(siQ Win32_PhysicalMemory | ForEach-Object { @{ size = [int64]$_.Capacity; mhz = [int]$_.Speed; cfg = [int]$_.ConfiguredClockSpeed; maker = siS $_.Manufacturer; part = siS $_.PartNumber; slot = siS $_.DeviceLocator; typeN = [int]$_.SMBIOSMemoryType } })
$o.slots = [int](siQ Win32_PhysicalMemoryArray | Where-Object { $_.Use -eq 3 } | Measure-Object -Property MemoryDevices -Sum).Sum
$bb = siQ Win32_BaseBoard | Select-Object -First 1
$bi = siQ Win32_BIOS | Select-Object -First 1
$o.board = @{ maker = siS $bb.Manufacturer; model = siS $bb.Product; pcMaker = siS $cs.Manufacturer; pcModel = siS $cs.Model; laptop = ([int]$cs.PCSystemType -eq 2); total = [int64]$cs.TotalPhysicalMemory; bios = siS $bi.SMBIOSBIOSVersion; biosMaker = siS $bi.Manufacturer; biosDate = siD $bi.ReleaseDate }
$sb = ''; try { if (Confirm-SecureBootUEFI -ErrorAction Stop) { $sb = 'on' } else { $sb = 'off' } } catch { }
$tpm = Get-CimInstance -Namespace 'root\cimv2\Security\MicrosoftTpm' -ClassName Win32_Tpm -ErrorAction SilentlyContinue | Select-Object -First 1
$tv = ''; if ($tpm) { $tv = ([string]$tpm.SpecVersion).Split(',')[0].Trim() }
$dg = Get-CimInstance -Namespace 'root\Microsoft\Windows\DeviceGuard' -ClassName Win32_DeviceGuard -ErrorAction SilentlyContinue | Select-Object -First 1
$o.security = @{ firmware = siS $env:firmware_type; secureBoot = $sb; tpm = $tv; vbs = ([int]$dg.VirtualizationBasedSecurityStatus -eq 2); hvci = (@($dg.SecurityServicesRunning) -contains 2); av = @(siQ AntiVirusProduct 'root\SecurityCenter2' | ForEach-Object { siS $_.displayName }) }
$sz = @{}
siQ WmiMonitorBasicDisplayParams 'root\wmi' | ForEach-Object { $w = [double]$_.MaxHorizontalImageSize; $h = [double]$_.MaxVerticalImageSize; if ($w -gt 0 -and $h -gt 0) { $sz[[string]$_.InstanceName] = [math]::Round([math]::Sqrt($w * $w + $h * $h) / 2.54, 1) } }
$o.monitors = @(siQ WmiMonitorID 'root\wmi' | ForEach-Object { @{ name = siA $_.UserFriendlyName; maker = siA $_.ManufacturerName; year = [int]$_.YearOfManufacture; inch = [double]$sz[[string]$_.InstanceName] } })
$o.audio = @(siQ Win32_SoundDevice | ForEach-Object { siS $_.Name })
$o.net = @(Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object { $_.Status -eq 'Up' } | ForEach-Object { @{ name = siS $_.Name; desc = siS $_.InterfaceDescription; speed = [int64]$_.ReceiveLinkSpeed; wifi = ([int]$_.NdisPhysicalMedium -eq 9) } })
$o.disks = @(Get-PhysicalDisk -ErrorAction SilentlyContinue | Sort-Object { [int]$_.DeviceId } | ForEach-Object { @{ name = siS $_.FriendlyName; media = siS $_.MediaType; bus = siS $_.BusType; size = [int64]$_.Size } })
$bat = siQ Win32_Battery | Select-Object -First 1
if ($bat) {
  $full = (siQ BatteryFullChargedCapacity 'root\wmi' | Select-Object -First 1).FullChargedCapacity
  $des = (siQ BatteryStaticData 'root\wmi' | Select-Object -First 1).DesignedCapacity
  $o.battery = @{ name = siS $bat.Name; charge = [int]$bat.EstimatedChargeRemaining; full = [int64]$full; design = [int64]$des }
}
$o | ConvertTo-Json -Depth 6 -Compress`

var sysInfoCache struct {
	sync.Mutex
	v  *sysInfo
	at time.Time
}

// systemInfo: what is in this PC. Read once and kept; refresh reads again.
func systemInfo(refresh bool) sysInfo {
	sysInfoCache.Lock()
	defer sysInfoCache.Unlock()
	if sysInfoCache.v != nil && !refresh {
		v := *sysInfoCache.v
		if t, err := time.Parse(time.RFC3339, v.OS.Boot); err == nil {
			v.OS.Uptime = int64(time.Since(t).Seconds())
		}
		return v
	}
	start := time.Now()
	si := readSysInfo()
	si.Took = time.Since(start).Milliseconds()
	sysInfoCache.v, sysInfoCache.at = &si, time.Now()
	return si
}

func readSysInfo() sysInfo {
	var si sysInfo
	out, _, err := runPS(90*time.Second, sysInfoScript)
	if jerr := json.Unmarshal([]byte(out), &si); jerr != nil || si.OS.Build == "" {
		si = sysInfo{} // nothing usable: what Go can read itself
		si.Error = T("si.err")
		if err != nil {
			si.Error += " (" + err.Error() + ")"
		}
		basicSysInfo(&si)
	}
	tidySysInfo(&si, time.Now())
	return si
}

// basicSysInfo: what can be read without PowerShell (the registry and
// Windows itself) when the full read did not work.
func basicSysInfo(si *sysInfo) {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		si.OS.Name, _, _ = k.GetStringValue("ProductName")
		si.OS.Build, _, _ = k.GetStringValue("CurrentBuild")
		if si.OS.Ver, _, _ = k.GetStringValue("DisplayVersion"); si.OS.Ver == "" {
			si.OS.Ver, _, _ = k.GetStringValue("ReleaseId")
		}
		if u, _, err := k.GetIntegerValue("UBR"); err == nil {
			si.OS.UBR = int(u)
		}
		k.Close()
	}
	// Windows 11 still says "Windows 10" in ProductName
	if b := atoiSafe(si.OS.Build); b >= 22000 {
		si.OS.Name = strings.Replace(si.OS.Name, "Windows 10", "Windows 11", 1)
	}
	si.OS.Arch = map[string]string{"amd64": "64-bit", "arm64": "ARM 64-bit", "386": "32-bit"}[runtime.GOARCH]
	si.OS.PC, _ = os.Hostname()
	if ms := windows.DurationSinceBoot(); ms > 0 {
		si.OS.Boot = time.Now().Add(-ms).UTC().Format(time.RFC3339)
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		c := siCPU{Threads: runtime.NumCPU()}
		c.Name, _, _ = k.GetStringValue("ProcessorNameString")
		if mhz, _, err := k.GetIntegerValue("~MHz"); err == nil {
			c.MHz = int(mhz)
		}
		k.Close()
		si.CPU = []siCPU{c}
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE); err == nil {
		si.Board.Maker, _, _ = k.GetStringValue("BaseBoardManufacturer")
		si.Board.Model, _, _ = k.GetStringValue("BaseBoardProduct")
		si.Board.PCMaker, _, _ = k.GetStringValue("SystemManufacturer")
		si.Board.PCModel, _, _ = k.GetStringValue("SystemProductName")
		si.Board.BIOS, _, _ = k.GetStringValue("BIOSVersion")
		si.Board.BIOSMaker, _, _ = k.GetStringValue("BIOSVendor")
		if d, _, _ := k.GetStringValue("BIOSReleaseDate"); d != "" { // MM/DD/YYYY
			if t, err := time.Parse("01/02/2006", d); err == nil {
				si.Board.BIOSDate = t.Format(time.RFC3339)
			}
		}
		k.Close()
	}
	si.Board.Total = int64(totalRAMKB()) * 1024
	si.Security.Firmware = os.Getenv("firmware_type")
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
