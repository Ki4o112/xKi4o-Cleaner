//go:build windows

package main

// NVIDIA driver settings (the global "Base Profile" that the NVIDIA Control
// Panel edits), through NVAPI's driver settings functions. Interface IDs,
// structure layout and setting IDs come from NVIDIA's public NVAPI SDK
// (github.com/NVIDIA/nvapi, MIT). Before a change the current value of each
// setting is saved (or the fact that it was the driver default), and turning
// the tweak off puts exactly that back.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	nvapiDLL = windows.NewLazySystemDLL("nvapi64.dll")
	nvQI     = nvapiDLL.NewProc("nvapi_QueryInterface")
	nvMu     sync.Mutex
)

const (
	nvInitialize                      = 0x0150e828
	nvDRSCreateSession                = 0x0694d52e
	nvDRSDestroySession               = 0xdad9cff8
	nvDRSLoadSettings                 = 0x375dbd6b
	nvDRSSaveSettings                 = 0xfcbc7e14
	nvDRSGetBaseProfile               = 0xda8466a0
	nvDRSSetSetting                   = 0x577dd202
	nvDRSGetSetting                   = 0x73bf8338
	nvDRSRestoreProfileDefaultSetting = 0x53f0381e

	nvSettingNotFound = -160
)

// NVDRS_SETTING_V1 (pack 4): 4 + 4096 + 5*4 + 4100 + 4100 = 12320 bytes.
type nvSetting struct {
	version         uint32
	name            [2048]uint16
	id              uint32
	typ             uint32
	location        uint32
	isCurPredefined uint32
	isPredefValid   uint32
	predefined      [4100]byte
	current         [4100]byte
}

const nvSettingVer = uint32(unsafe.Sizeof(nvSetting{})) | 1<<16

// compile-time size check
var _ = [1]struct{}{}[unsafe.Sizeof(nvSetting{})-12320]

// The settings this tweak changes, with the value it sets.
var nvTweak = []struct{ id, val uint32 }{
	{0x1057EB71, 1},          // Power management mode: prefer maximum performance
	{0x00AC8497, 0xffffffff}, // Shader cache size: unlimited
	{0x00CE2691, 0x14},       // Texture filtering - quality: high performance
	{0x00E73211, 1},          // Texture filtering - anisotropic sample optimization: on
	{0x20C1221E, 1},          // Threaded optimization: on
}

func nvFunc(id uint32) uintptr {
	if nvQI.Find() != nil {
		return 0
	}
	r, _, _ := nvQI.Call(uintptr(id))
	return r
}

func nvCall(id uint32, args ...uintptr) int32 {
	f := nvFunc(id)
	if f == 0 {
		return -3 // NVAPI_NO_IMPLEMENTATION
	}
	r, _, _ := syscall.SyscallN(f, args...)
	return int32(r)
}

type nvSession struct {
	h    *uintptr
	prof *uintptr
}

var errNoNVIDIA = errors.New("NVIDIA driver not available")

func nvOpen() (*nvSession, error) {
	if nvFunc(nvInitialize) == 0 {
		return nil, errNoNVIDIA
	}
	if nvCall(nvInitialize) != 0 {
		return nil, errNoNVIDIA
	}
	s := &nvSession{h: new(uintptr), prof: new(uintptr)}
	if nvCall(nvDRSCreateSession, uintptr(unsafe.Pointer(s.h))) != 0 {
		return nil, errNoNVIDIA
	}
	if nvCall(nvDRSLoadSettings, *s.h) != 0 || nvCall(nvDRSGetBaseProfile, *s.h, uintptr(unsafe.Pointer(s.prof))) != 0 {
		s.close()
		return nil, errNoNVIDIA
	}
	return s, nil
}

func (s *nvSession) close() {
	nvCall(nvDRSDestroySession, *s.h)
	runtime.KeepAlive(s)
}

func (s *nvSession) get(id uint32) (uint32, bool) {
	v, _, ok := s.getFull(id)
	return v, ok
}

// getFull also says whether the value is the driver's own default.
func (s *nvSession) getFull(id uint32) (val uint32, isDefault bool, ok bool) {
	st := new(nvSetting)
	st.version = nvSettingVer
	r := nvCall(nvDRSGetSetting, *s.h, *s.prof, uintptr(id), uintptr(unsafe.Pointer(st)))
	runtime.KeepAlive(st)
	if r == nvSettingNotFound {
		return 0, true, true // not set in the profile: the driver default applies
	}
	if r != 0 {
		return 0, false, false
	}
	return binary.LittleEndian.Uint32(st.current[:4]), st.isCurPredefined != 0, true
}

func (s *nvSession) set(id, val uint32) error {
	st := new(nvSetting)
	st.version = nvSettingVer
	st.id = id
	st.typ = 0 // NVDRS_DWORD_TYPE
	binary.LittleEndian.PutUint32(st.current[:4], val)
	r := nvCall(nvDRSSetSetting, *s.h, *s.prof, uintptr(unsafe.Pointer(st)))
	runtime.KeepAlive(st)
	if r != 0 {
		return errors.New(T("tw.err.nvidia", r))
	}
	return nil
}

func (s *nvSession) reset(id uint32) error {
	if r := nvCall(nvDRSRestoreProfileDefaultSetting, *s.h, *s.prof, uintptr(id)); r != 0 && r != nvSettingNotFound {
		return errors.New(T("tw.err.nvidia", r))
	}
	return nil
}

func (s *nvSession) save() error {
	if r := nvCall(nvDRSSaveSettings, *s.h); r != 0 {
		return errors.New(T("tw.err.nvidia", r))
	}
	return nil
}

func nvAvailable() bool {
	nvMu.Lock()
	defer nvMu.Unlock()
	s, err := nvOpen()
	if err != nil {
		return false
	}
	s.close()
	return true
}

func nvState() string {
	nvMu.Lock()
	defer nvMu.Unlock()
	s, err := nvOpen()
	if err != nil {
		return "off"
	}
	defer s.close()
	on := 0
	for _, t := range nvTweak {
		if v, ok := s.get(t.id); ok && v == t.val {
			on++
		}
	}
	switch on {
	case len(nvTweak):
		return "on"
	case 0:
		return "off"
	}
	return "partial"
}

func nvApply() error {
	nvMu.Lock()
	defer nvMu.Unlock()
	s, err := nvOpen()
	if err != nil {
		return errors.New(T("tw.na.nvidia"))
	}
	defer s.close()
	for _, t := range nvTweak {
		if err := s.set(t.id, t.val); err != nil {
			return err
		}
	}
	return s.save()
}

func nvRevert() error {
	nvMu.Lock()
	defer nvMu.Unlock()
	s, err := nvOpen()
	if err != nil {
		return errors.New(T("tw.na.nvidia"))
	}
	defer s.close()
	for _, t := range nvTweak {
		if err := s.reset(t.id); err != nil {
			return err
		}
	}
	return s.save()
}

// nvSnap records, for each setting the tweak changes, either "def" (the
// driver default is in effect) or the value the user had set.
func nvSnap() (string, error) {
	nvMu.Lock()
	defer nvMu.Unlock()
	s, err := nvOpen()
	if err != nil {
		return "", errors.New(T("tw.na.nvidia"))
	}
	defer s.close()
	var parts []string
	for _, t := range nvTweak {
		v, def, ok := s.getFull(t.id)
		if !ok {
			return "", fmt.Errorf("NVIDIA setting %08X could not be read", t.id)
		}
		if def {
			parts = append(parts, fmt.Sprintf("%08X=def", t.id))
		} else {
			parts = append(parts, fmt.Sprintf("%08X=%d", t.id, v))
		}
	}
	return strings.Join(parts, ","), nil
}

// nvRestore puts back what nvSnap recorded.
func nvRestore(snap string) error {
	want := map[uint32]string{}
	for _, p := range strings.Split(snap, ",") {
		k, v, ok := strings.Cut(p, "=")
		id, err := strconv.ParseUint(k, 16, 32)
		if !ok || err != nil {
			return fmt.Errorf("bad NVIDIA snapshot %q", p)
		}
		want[uint32(id)] = v
	}
	nvMu.Lock()
	defer nvMu.Unlock()
	s, err := nvOpen()
	if err != nil {
		return errors.New(T("tw.na.nvidia"))
	}
	defer s.close()
	for _, t := range nvTweak {
		v, ok := want[t.id]
		if !ok {
			continue
		}
		if v == "def" {
			if err := s.reset(t.id); err != nil {
				return err
			}
			continue
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return fmt.Errorf("bad NVIDIA value %q", v)
		}
		if err := s.set(t.id, uint32(n)); err != nil {
			return err
		}
	}
	return s.save()
}
