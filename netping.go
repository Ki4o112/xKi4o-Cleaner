package main

// The connection check on the Network page: a few pings to the router and
// to two big public servers, read as speed (ms), steadiness (jitter) and
// lost packets. It only sends pings; nothing is changed.

import "math"

type pingResult struct {
	ID     string  `json:"id"`   // router, cloudflare, google
	Host   string  `json:"host"` // the address pinged
	Sent   int     `json:"sent"`
	Lost   int     `json:"lost"`
	Loss   float64 `json:"loss"` // per cent
	Min    float64 `json:"min"`  // ms
	Avg    float64 `json:"avg"`
	Max    float64 `json:"max"`
	Jitter float64 `json:"jitter"` // mean change between two answers in a row
	Error  string  `json:"error,omitempty"`
}

// pingStats: rtts in ms, a negative one is a ping that got no answer.
func pingStats(rtts []float64) pingResult {
	r := pingResult{Sent: len(rtts)}
	var got []float64
	for _, v := range rtts {
		if v < 0 {
			r.Lost++
			continue
		}
		got = append(got, v)
	}
	if r.Sent > 0 {
		r.Loss = math.Round(float64(r.Lost)*1000/float64(r.Sent)) / 10
	}
	if len(got) == 0 {
		return r
	}
	r.Min, r.Max = got[0], got[0]
	sum := 0.0
	for i, v := range got {
		sum += v
		r.Min, r.Max = math.Min(r.Min, v), math.Max(r.Max, v)
		if i > 0 {
			r.Jitter += math.Abs(v - got[i-1])
		}
	}
	r.Avg = math.Round(sum/float64(len(got))*10) / 10
	if len(got) > 1 {
		r.Jitter = math.Round(r.Jitter/float64(len(got)-1)*10) / 10
	} else {
		r.Jitter = 0
	}
	return r
}
