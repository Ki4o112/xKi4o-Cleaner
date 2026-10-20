package main

// Saved Wi-Fi networks and their passwords, read from the profile XML that
// "netsh wlan export profile key=clear" writes. The XML is the same in every
// Windows language, unlike netsh's own text. Free of Windows calls.

import (
	"encoding/xml"
	"sort"
	"strings"
)

type wifiNet struct {
	Name string `json:"name"`
	Auth string `json:"auth"`          // WPA2PSK, WPA3SAE, open, WPA2 (enterprise)…
	Key  string `json:"key,omitempty"` // empty: an open network or one with a sign-in (enterprise)
	Open bool   `json:"open,omitempty"`
}

type wlanProfileXML struct {
	Name string `xml:"name"`
	SSID string `xml:"SSIDConfig>SSID>name"`
	Auth string `xml:"MSM>security>authEncryption>authentication"`
	Key  string `xml:"MSM>security>sharedKey>keyMaterial"`
	Prot string `xml:"MSM>security>sharedKey>protected"`
}

// parseWlanProfile reads one exported profile.
func parseWlanProfile(b []byte) (wifiNet, bool) {
	var p wlanProfileXML
	if xml.Unmarshal(b, &p) != nil || (p.Name == "" && p.SSID == "") {
		return wifiNet{}, false
	}
	n := wifiNet{Name: p.SSID, Auth: p.Auth}
	if n.Name == "" {
		n.Name = p.Name
	}
	if strings.EqualFold(p.Prot, "false") { // "true" is still encrypted: no admin rights, never shown
		n.Key = p.Key
	}
	n.Open = strings.EqualFold(p.Auth, "open")
	return n, true
}

func sortWifi(ns []wifiNet) {
	sort.Slice(ns, func(i, j int) bool { return strings.ToLower(ns[i].Name) < strings.ToLower(ns[j].Name) })
}
