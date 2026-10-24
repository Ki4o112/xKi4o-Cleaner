package main

// Ping to game servers (Network page): League of Legends from the addresses
// on the League wiki, Fortnite from Epic's regional ping hosts, Counter-Strike 2
// from Valve's own relay list (fetched live). Valorant is left out: Riot
// publishes no addresses to ping. Free of Windows calls.

import (
	"encoding/json"
	"sort"
)

type gpTarget struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Host string `json:"host"`
}

var gameTargets = map[string][]gpTarget{
	"lol": {{"EUNE", "Europe Nordic & East", "104.160.142.3"}, {"EUW", "Europe West", "104.160.141.3"}, {"NA", "North America", "104.160.131.3"},
		{"BR", "Brazil", "104.160.152.3"}, {"LAN", "Latin America North", "104.160.136.3"}, {"OCE", "Oceania", "104.160.156.1"}},
	"fortnite": {{"EU", "Europe", "ping-eu.ds.on.epicgames.com"}, {"NAE", "NA-East", "ping-nae.ds.on.epicgames.com"},
		{"NAC", "NA-Central", "ping-nac.ds.on.epicgames.com"}, {"NAW", "NA-West", "ping-naw.ds.on.epicgames.com"},
		{"BR", "Brazil", "ping-br.ds.on.epicgames.com"}, {"ME", "Middle East", "ping-me.ds.on.epicgames.com"},
		{"ASIA", "Asia", "ping-asia.ds.on.epicgames.com"}, {"OCE", "Oceania", "ping-oce.ds.on.epicgames.com"}},
}

const sdrConfigURL = "https://api.steampowered.com/ISteamApps/GetSDRConfig/v1/?appid=730"

// parseSDRConfig: one target per Valve point of presence (its first relay).
func parseSDRConfig(b []byte) []gpTarget {
	var v struct {
		Pops map[string]struct {
			Desc   string `json:"desc"`
			Relays []struct {
				IPv4 string `json:"ipv4"`
			} `json:"relays"`
		} `json:"pops"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var out []gpTarget
	for code, p := range v.Pops {
		if len(p.Relays) > 0 && p.Relays[0].IPv4 != "" && p.Desc != "" {
			out = append(out, gpTarget{Code: code, Name: p.Desc, Host: p.Relays[0].IPv4})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type gpResult struct {
	Code string `json:"code"`
	Name string `json:"name"`
	pingResult
	Best bool `json:"best,omitempty"`
}

// markBest: the region with the lowest average that lost no more than one ping in five.
func markBest(rs []gpResult) {
	best := -1
	for i, r := range rs {
		if r.Avg > 0 && r.Loss <= 20 && (best < 0 || r.Avg < rs[best].Avg) {
			best = i
		}
	}
	if best >= 0 {
		rs[best].Best = true
	}
}
