package main

// The live numbers of the PC health dashboard: processor load, memory,
// the graphics card (load, temperature and memory where the driver tells)
// and how fast the network is sending and receiving. Only read.

import "time"

type gpuLive struct {
	OK       bool   `json:"ok"`
	Name     string `json:"name,omitempty"`
	Util     int    `json:"util"` // %; -1 when unknown
	Temp     int    `json:"temp"` // °C; 0 when unknown
	MemUsed  uint64 `json:"memUsed"`
	MemTotal uint64 `json:"memTotal"`
	Src      string `json:"src,omitempty"` // nvml, pdh
}

type netLive struct {
	OK   bool    `json:"ok"`
	Name string  `json:"name,omitempty"`
	WiFi bool    `json:"wifi"`
	Rx   float64 `json:"rx"` // bytes per second
	Tx   float64 `json:"tx"`
	Link int64   `json:"link"` // bits per second
}

type liveData struct {
	CPU      float64 `json:"cpu"`
	MemLoad  uint32  `json:"mem"`
	MemUsed  uint64  `json:"memUsed"`
	MemTotal uint64  `json:"memTotal"`
	GPU      gpuLive `json:"gpu"`
	Net      netLive `json:"net"`
}

// netSample: the byte counters of the connections at one moment.
type netSample struct {
	at     time.Time
	rx, tx uint64
}

// netRate: bytes per second between two samples. A counter that went back
// (an adapter came and went) counts as nothing rather than as a huge jump.
func netRate(prev, cur netSample) (rx, tx float64) {
	d := cur.at.Sub(prev.at).Seconds()
	if prev.at.IsZero() || d <= 0.05 {
		return 0, 0
	}
	if cur.rx >= prev.rx {
		rx = float64(cur.rx-prev.rx) / d
	}
	if cur.tx >= prev.tx {
		tx = float64(cur.tx-prev.tx) / d
	}
	return rx, tx
}

// gpuSum: the engines' loads add up per process; the card is at most 100%.
func gpuSum(vals []float64) int {
	s := 0.0
	for _, v := range vals {
		if v > 0 {
			s += v
		}
	}
	if s > 100 {
		s = 100
	}
	return int(s + .5)
}
