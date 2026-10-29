package main

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// a picture with shapes: a gradient, a bright disc and a dark bar
func testPicture(w, h int, shift int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			v := uint8(60 + 120*fx)
			dx, dy := fx-0.3-float64(shift)/100, fy-0.5
			if dx*dx+dy*dy < 0.04 {
				v = 240
			}
			if fx > 0.65 && fx < 0.8 {
				v = 20
			}
			img.Set(x, y, color.RGBA{v, uint8(int(v) * 3 / 4), uint8(255 - int(v)), 255})
		}
	}
	return img
}

func savePic(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if strings.HasSuffix(path, ".png") {
		err = png.Encode(f, img)
	} else {
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: 60})
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestSimilarPictures(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "a"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "b"), 0o755)
	savePic(t, filepath.Join(root, "a", "big.png"), testPicture(800, 600, 0))
	savePic(t, filepath.Join(root, "b", "small copy.jpg"), testPicture(320, 240, 0)) // resized, another format
	savePic(t, filepath.Join(root, "b", "other.jpg"), func() image.Image {           // something else
		img := image.NewRGBA(image.Rect(0, 0, 400, 300))
		for y := 0; y < 300; y++ {
			for x := 0; x < 400; x++ {
				img.Set(x, y, color.Gray{uint8((x / 20 % 2) * 255)})
			}
		}
		return img
	}())
	savePic(t, filepath.Join(root, "b", "icon.png"), testPicture(32, 32, 0)) // too small
	_ = os.WriteFile(filepath.Join(root, "b", "broken.jpg"), []byte("not a picture"), 0o644)
	var stop atomic.Bool
	st := &findStats{}
	g := findSimilar(findOpts{Roots: []string{root}}, testFS(t, t.TempDir()), &stop, st)
	if len(g) != 1 || len(g[0].Files) != 2 {
		t.Fatalf("groups %+v", g)
	}
	if !isSimilarGroup(g[0]) || filepath.Base(g[0].Files[0].Path) != "big.png" || g[0].Files[0].W != 800 {
		t.Fatalf("the biggest picture comes first: %+v", g[0].Files[0])
	}
	if !strings.HasPrefix(g[0].Files[1].Thumb, "data:image/jpeg;base64,") || g[0].Wasted != g[0].Files[1].Size {
		t.Fatalf("thumb / wasted %+v", g[0])
	}
	if p := st.progress(); p.Hashed != p.ToHash || p.ToHash == 0 {
		t.Fatalf("progress %+v", p)
	}
	// the pictures moved a little are still alike; a different picture is not
	if d := hamming(dHash(testPicture(400, 300, 0)), dHash(testPicture(400, 300, 2))); d > simMaxDist {
		t.Errorf("a small change: %d bits", d)
	}
	// deleting: the copy may go, the last one never (it is checked as unchanged, not by content)
	fs := testFS(t, t.TempDir())
	ok, refused := planDupDelete(g, []string{g[0].Files[1].Path}, fs)
	if len(ok) != 1 || len(refused) != 0 {
		t.Fatalf("one: %v %v", ok, refused)
	}
	ok, refused = planDupDelete(g, []string{g[0].Files[0].Path, g[0].Files[1].Path}, fs)
	if len(ok) != 0 || len(refused) != 2 {
		t.Fatalf("all: %v %v", ok, refused)
	}
}
