package main

// Similar photos on the Duplicates page: pictures that look the same but
// are not the same file (a resized copy, another format, a re-saved or
// slightly edited one). Each picture gets a 64-bit fingerprint of how its
// brightness changes (dHash); pictures whose fingerprints differ in only a
// few bits look alike. Free of Windows calls.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math/bits"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// simExts: the kinds of picture the fingerprint can read.
var simExts = map[string]bool{".jpg": true, ".jpeg": true, ".jfif": true, ".png": true, ".gif": true}

const (
	simMaxDist = 6  // bits of 64 that may differ
	simMinSide = 64 // smaller pictures (icons) are left out
	simThumb   = 120
)

// gray: the brightness of the picture at (x, y) of a w×h grid, as the mean
// of a few points of that cell (fast on a 12-megapixel photo).
func cellGray(img image.Image, x, y, w, h int) float64 {
	b := img.Bounds()
	cw, ch := float64(b.Dx())/float64(w), float64(b.Dy())/float64(h)
	var sum float64
	n := 0
	for sy := 0; sy < 3; sy++ {
		for sx := 0; sx < 3; sx++ {
			px := b.Min.X + int((float64(x)+(float64(sx)+0.5)/3)*cw)
			py := b.Min.Y + int((float64(y)+(float64(sy)+0.5)/3)*ch)
			r, g, bl, _ := img.At(px, py).RGBA()
			sum += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
			n++
		}
	}
	return sum / float64(n)
}

// dHash: bit set where a cell is brighter than the cell to its right
// (9×8 cells).
func dHash(img image.Image) uint64 {
	var g [8][9]float64
	for y := 0; y < 8; y++ {
		for x := 0; x < 9; x++ {
			g[y][x] = cellGray(img, x, y, 9, 8)
		}
	}
	var h uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			h <<= 1
			if g[y][x] > g[y][x+1] {
				h |= 1
			}
		}
	}
	return h
}

func hamming(a, b uint64) int { return bits.OnesCount64(a ^ b) }

type simItem struct {
	f    foundFile
	hash uint64
	w, h int
}

// simClusters: groups of pictures within simMaxDist of the group's first
// one (not chains: A~B and B~C do not put A and C together unless they are
// alike too).
func simClusters(items []simItem) [][]simItem {
	sort.Slice(items, func(i, j int) bool { return items[i].f.Path < items[j].f.Path })
	used := make([]bool, len(items))
	var out [][]simItem
	for i := range items {
		if used[i] {
			continue
		}
		g := []simItem{items[i]}
		for j := i + 1; j < len(items); j++ {
			if !used[j] && hamming(items[i].hash, items[j].hash) <= simMaxDist {
				g = append(g, items[j])
				used[j] = true
			}
		}
		if len(g) > 1 {
			used[i] = true
			out = append(out, g)
		}
	}
	return out
}

// thumbOf: a small JPEG of the picture as a data URL (for the page).
func thumbOf(img image.Image) string {
	b := img.Bounds()
	w, h := simThumb, simThumb
	if b.Dx() > b.Dy() {
		h = max(1, simThumb*b.Dy()/b.Dx())
	} else {
		w = max(1, simThumb*b.Dx()/b.Dy())
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.Black}, image.Point{}, draw.Src)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, y, img.At(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h))
		}
	}
	var buf bytes.Buffer
	if jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 72}) != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func decodePicture(path string) (img image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, errStopped // a broken file
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err = image.Decode(f)
	return img, err
}

// findSimilar: the Duplicates search for pictures that look alike.
func findSimilar(o findOpts, fs finderFS, stop *atomic.Bool, st *findStats) []dupGroup {
	o.Cats = []string{"images"}
	if o.MinSize <= 0 {
		o.MinSize = 1
	}
	st.phase.Store("walk")
	var mu sync.Mutex
	var files []foundFile
	walkFiles(o, fs, stop, st, func(f foundFile) {
		if simExts[strings.ToLower(filepath.Ext(f.Path))] {
			mu.Lock()
			files = append(files, f)
			mu.Unlock()
		}
	})
	if stop.Load() {
		return nil
	}
	st.phase.Store("hash")
	var total int64
	for _, f := range files {
		total += f.Size
	}
	st.toHash.Store(total)
	var items []simItem
	jobs := make(chan foundFile)
	var wg sync.WaitGroup
	for i := 0; i < max(2, runtime.NumCPU()); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if stop.Load() {
					continue
				}
				st.current.Store(f.Path)
				img, err := decodePicture(f.Path)
				st.hashed.Add(f.Size)
				if err != nil || img.Bounds().Dx() < simMinSide || img.Bounds().Dy() < simMinSide {
					continue
				}
				it := simItem{f: f, hash: dHash(img), w: img.Bounds().Dx(), h: img.Bounds().Dy()}
				mu.Lock()
				items = append(items, it)
				mu.Unlock()
			}
		}()
	}
	for _, f := range files {
		if stop.Load() {
			break
		}
		jobs <- f
	}
	close(jobs)
	wg.Wait()
	if stop.Load() {
		return nil
	}
	st.phase.Store("similar")
	var out []dupGroup
	for _, g := range simClusters(items) {
		// the biggest picture first: the one worth keeping
		sort.SliceStable(g, func(i, j int) bool {
			if a, b := g[i].w*g[i].h, g[j].w*g[j].h; a != b {
				return a > b
			}
			return g[i].f.Size > g[j].f.Size
		})
		dg := dupGroup{Hash: "sim:" + hex.EncodeToString([]byte{byte(g[0].hash >> 56), byte(g[0].hash >> 48), byte(g[0].hash >> 40), byte(g[0].hash >> 32), byte(g[0].hash >> 24), byte(g[0].hash >> 16), byte(g[0].hash >> 8), byte(g[0].hash)}), Size: g[0].f.Size}
		for i, it := range g {
			f := it.f
			f.W, f.H = it.w, it.h
			if img, err := decodePicture(f.Path); err == nil {
				f.Thumb = thumbOf(img)
			}
			dg.Files = append(dg.Files, f)
			if i > 0 {
				dg.Wasted += f.Size
			}
		}
		out = append(out, dg)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Wasted > out[j].Wasted })
	return out
}

// isSimilarGroup: a group of look-alike pictures (not of identical files).
func isSimilarGroup(g dupGroup) bool { return strings.HasPrefix(g.Hash, "sim:") }
