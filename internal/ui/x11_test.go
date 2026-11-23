package ui

import (
	"image"
	"image/color"
	"testing"
)

func TestRotateGrayForDisplayAndTransformTap(t *testing.T) {
	const physW, physH = 600, 800

	// Portrait passthrough
	port := image.NewGray(image.Rect(0, 0, physW, physH))
	port.SetGray(12, 34, color.Gray{Y: 42})
	outP := RotateGrayForDisplay(port, physW, physH, "portrait")
	if outP.Bounds().Dx() != physW || outP.Bounds().Dy() != physH || outP.GrayAt(12, 34).Y != 42 {
		t.Fatalf("portrait rotation mismatch")
	}
	tx, ty := TransformTapFromDisplay(12, 34, physW, physH, "portrait")
	if tx != 12 || ty != 34 {
		t.Fatalf("expected portrait tap (12,34), got (%d,%d)", tx, ty)
	}

	// Logical landscape 800x600 with 4 distinct corner pixels
	land := image.NewGray(image.Rect(0, 0, physH, physW))
	corners := []struct {
		lx, ly int
		val    uint8
	}{
		{0, 0, 11},
		{physH - 1, 0, 22},
		{0, physW - 1, 33},
		{physH - 1, physW - 1, 44},
	}
	for _, c := range corners {
		land.SetGray(c.lx, c.ly, color.Gray{Y: c.val})
	}

	for _, orient := range []string{"landscape", "landscape_cw"} {
		rot := RotateGrayForDisplay(land, physW, physH, orient)
		if rot.Bounds().Dx() != physW || rot.Bounds().Dy() != physH {
			t.Fatalf("%s: expected physical bounds %dx%d, got %v", orient, physW, physH, rot.Bounds())
		}
		for _, c := range corners {
			var foundX, foundY int
			found := 0
			for py := 0; py < physH; py++ {
				for px := 0; px < physW; px++ {
					if rot.GrayAt(px, py).Y == c.val {
						foundX, foundY = px, py
						found++
					}
				}
			}
			if found != 1 {
				t.Fatalf("%s: expected unique rotated corner %+v, found %d times", orient, c, found)
			}
			ix, iy := TransformTapFromDisplay(foundX, foundY, physW, physH, orient)
			if ix != c.lx || iy != c.ly {
				t.Fatalf("%s: corner roundtrip (%d,%d) -> display (%d,%d) -> inverse (%d,%d)",
					orient, c.lx, c.ly, foundX, foundY, ix, iy)
			}
		}
	}
}
