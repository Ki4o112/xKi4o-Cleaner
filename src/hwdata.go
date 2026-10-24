package main

// Test data for the bottleneck check, from Tom's Hardware's GPU and CPU
// benchmark hierarchies (averages over many games, each card and processor
// tested the same way). hwdata_gpu.go and hwdata_cpu.go hold the numbers.
//
// The graphics side is frames per second at 1080p, 1440p and 4K (ultra
// settings) with the fastest gaming processor. The processor side is its
// gaming speed when it is the limit (Ryzen 7 9800X3D = 100), turned into
// frames per second with cpuFpsAt100.

import (
	"regexp"
	"strings"
)

const hwDataSource = "Tom's Hardware GPU & CPU benchmark hierarchies (2026)"

// cpuFpsAt100: frames per second the Ryzen 7 9800X3D (index 100) delivers in
// the same games when the graphics card is not the limit (the RTX 5090 test
// reached 204 fps at 1080p with it, and was still partly held back by it).
const cpuFpsAt100 = 215.0

// gpuRef1080: the RTX 3060 at 1080p, for "% of an RTX 3060".
const gpuRef1080 = 61.5

type gpuFPSRow struct {
	Name   string
	Pat    string
	FPS108 float64
	FPS144 float64
	FPS4K  float64
	Src    string
}

type cpuGameRow struct {
	Name  string
	Pat   string
	Index float64
	Src   string
}

type gpuData struct {
	Name string
	FPS  [3]float64
}

type cpuData struct {
	Name  string
	Index float64
}

var (
	gpuRes []*regexp.Regexp
	cpuRes []*regexp.Regexp
)

func init() {
	for _, g := range gpuFPSData {
		gpuRes = append(gpuRes, regexp.MustCompile(`(?i)`+g.Pat))
	}
	for _, c := range cpuGameData {
		cpuRes = append(cpuRes, regexp.MustCompile(`(?i)`+c.Pat))
	}
}

var reLaptopGPU = regexp.MustCompile(`(?i)laptop|mobile|max-q|\bmx\d`)

// gpuDataFor finds a graphics card in the data (the longest names are tried
// first, so "RTX 4070 Ti" is never read as "RTX 4070"). Laptop chips are not
// in it: they are much slower than the desktop card of the same name.
func gpuDataFor(name string) *gpuData {
	if name == "" || reLaptopGPU.MatchString(name) || isIntegrated(name) {
		return nil
	}
	for i, re := range gpuRes {
		if re.MatchString(name) {
			g := gpuFPSData[i]
			return &gpuData{Name: g.Name, FPS: [3]float64{g.FPS108, g.FPS144, g.FPS4K}}
		}
	}
	return nil
}

var reMobileCPU = regexp.MustCompile(`(?i)\d{4,5}(h|hs|hx|hk|u|p|y)\b|\d{3}(h|hx|u|v)\b`)

// cpuDataFor finds a processor in the data. Laptop processors (H, HX, U ...)
// are not in it.
func cpuDataFor(name string) *cpuData {
	n := cleanCPUName(name)
	if n == "" || reMobileCPU.MatchString(n) {
		return nil
	}
	for i, re := range cpuRes {
		if re.MatchString(n) {
			c := cpuGameData[i]
			return &cpuData{Name: c.Name, Index: c.Index}
		}
	}
	return nil
}

var reCPUJunk = regexp.MustCompile(`(?i)\((r|tm)\)|\s+cpu\s+@.*$|\s+(six|eight|twelve|sixteen|quad|\d+)-core.*$|\s+with radeon.*$|\d+(th|nd|rd|st) gen\s+`)

// cleanCPUName: "12th Gen Intel(R) Core(TM) i9-12900K" -> "Intel Core i9-12900K".
func cleanCPUName(name string) string {
	n := reCPUJunk.ReplaceAllString(name, "")
	return strings.Join(strings.Fields(n), " ")
}

func gpuClassOfFps(fps1080 float64) string {
	switch {
	case fps1080 >= 150:
		return "enthusiast"
	case fps1080 >= 110:
		return "high"
	case fps1080 >= 75:
		return "upper"
	case fps1080 >= 50:
		return "mid"
	case fps1080 >= 25:
		return "entry"
	}
	return "low"
}

// cpuRank: how many of the tested processors (in %) are slower in games, and of how many.
func cpuRank(index float64) (pct, of int) {
	slower := 0
	for _, r := range cpuGameData {
		if r.Index < index {
			slower++
		}
	}
	return rankPct(slower, len(cpuGameData)), len(cpuGameData)
}

// gpuRank: the same for graphics cards, by frames per second at 1080p.
func gpuRank(fps1080 float64) (pct, of int) {
	slower := 0
	for _, r := range gpuFPSData {
		if r.FPS108 < fps1080 {
			slower++
		}
	}
	return rankPct(slower, len(gpuFPSData)), len(gpuFPSData)
}

func rankPct(slower, of int) int {
	if of <= 1 {
		return 0
	}
	return min(100, slower*100/(of-1)) // the fastest one is faster than 100% of the others
}
