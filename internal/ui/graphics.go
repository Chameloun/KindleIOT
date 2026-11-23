package ui

import (
	"image"
	"image/color"
	"math"
	"strings"

	"golang.org/x/image/font"

	"kindleiot/internal/config"
)

var (
	colBgCanvas   = color.Gray{Y: 244}
	colCardShadow = color.Gray{Y: 50}
	colSubtleGray = color.Gray{Y: 215}
)

// fillRoundedRect fills a rectangle with rounded corners of radius r.
func fillRoundedRect(img *image.Gray, rect image.Rectangle, r int, c color.Gray) {
	if r <= 0 {
		fillRect(img, rect, c)
		return
	}
	if maxR := minInt(rect.Dx(), rect.Dy()) / 2; r > maxR {
		r = maxR
	}
	b := img.Bounds()
	x0, y0, x1, y1 := rect.Min.X, rect.Min.Y, rect.Max.X, rect.Max.Y
	r2 := r * r

	for y := maxInt(y0, b.Min.Y); y < minInt(y1, b.Max.Y); y++ {
		dy := 0
		if y < y0+r {
			dy = (y0 + r - 1) - y
		} else if y >= y1-r {
			dy = y - (y1 - r)
		}
		for x := maxInt(x0, b.Min.X); x < minInt(x1, b.Max.X); x++ {
			dx := 0
			if x < x0+r {
				dx = (x0 + r - 1) - x
			} else if x >= x1-r {
				dx = x - (x1 - r)
			}
			if dx > 0 && dy > 0 {
				if dx*dx+dy*dy > r2 {
					continue
				}
			}
			img.SetGray(x, y, c)
		}
	}
}

// drawRoundedRect strokes a rounded rectangle border of given thickness.
func drawRoundedRect(img *image.Gray, rect image.Rectangle, r, thickness int, c color.Gray) {
	if thickness <= 0 {
		return
	}
	if r <= 0 {
		drawRect(img, rect, thickness, c)
		return
	}
	if maxR := minInt(rect.Dx(), rect.Dy()) / 2; r > maxR {
		r = maxR
	}
	b := img.Bounds()
	x0, y0, x1, y1 := rect.Min.X, rect.Min.Y, rect.Max.X, rect.Max.Y
	ix0, iy0, ix1, iy1 := x0+thickness, y0+thickness, x1-thickness, y1-thickness
	ir := maxInt(0, r-thickness)
	r2 := r * r
	ir2 := ir * ir

	for y := maxInt(y0, b.Min.Y); y < minInt(y1, b.Max.Y); y++ {
		ody := 0
		if y < y0+r {
			ody = (y0 + r - 1) - y
		} else if y >= y1-r {
			ody = y - (y1 - r)
		}
		idy := 0
		if y < iy0+ir {
			idy = (iy0 + ir - 1) - y
		} else if y >= iy1-ir {
			idy = y - (iy1 - ir)
		}

		for x := maxInt(x0, b.Min.X); x < minInt(x1, b.Max.X); x++ {
			odx := 0
			if x < x0+r {
				odx = (x0 + r - 1) - x
			} else if x >= x1-r {
				odx = x - (x1 - r)
			}
			if odx > 0 && ody > 0 && odx*odx+ody*ody > r2 {
				continue
			}

			// Check if inside inner rounded rect
			if x >= ix0 && x < ix1 && y >= iy0 && y < iy1 {
				idx := 0
				if x < ix0+ir {
					idx = (ix0 + ir - 1) - x
				} else if x >= ix1-ir {
					idx = x - (ix1 - ir)
				}
				if !(idx > 0 && idy > 0 && idx*idx+idy*idy > ir2) {
					continue
				}
			}
			img.SetGray(x, y, c)
		}
	}
}

// drawCardFrame renders an elevated rounded card with an e-ink drop shadow and return its inner face rectangle.
func drawCardFrame(img *image.Gray, rect image.Rectangle, radius int, inverted bool) image.Rectangle {
	shadow := image.Rect(rect.Min.X+3, rect.Min.Y+3, rect.Max.X, rect.Max.Y)
	face := image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X-3, rect.Max.Y-3)

	fillRoundedRect(img, shadow, radius, colBlack)
	if inverted {
		fillRoundedRect(img, face, radius, colBlack)
		drawRoundedRect(img, face, radius, 2, colBlack)
		inner := image.Rect(face.Min.X+3, face.Min.Y+3, face.Max.X-3, face.Max.Y-3)
		drawRoundedRect(img, inner, maxInt(2, radius-3), 1, colMidGray)
	} else {
		fillRoundedRect(img, face, radius, colWhite)
		drawRoundedRect(img, face, radius, 2, colBlack)
	}
	return face
}

// drawRoundedButton renders a rounded tactile button with subtle 2px 3D shadow.
func drawRoundedButton(img *image.Gray, r image.Rectangle, radius int, face font.Face, text string, inverted bool) {
	if r.Dx() < 16 || r.Dy() < 16 {
		drawButtonBox(img, r, face, text, inverted)
		return
	}
	shadow := image.Rect(r.Min.X+2, r.Min.Y+2, r.Max.X, r.Max.Y)
	btn := image.Rect(r.Min.X, r.Min.Y, r.Max.X-2, r.Max.Y-2)
	fillRoundedRect(img, shadow, radius, colBlack)
	if inverted {
		fillRoundedRect(img, btn, radius, colBlack)
		drawRoundedRect(img, btn, radius, 2, colBlack)
		inner := image.Rect(btn.Min.X+2, btn.Min.Y+2, btn.Max.X-2, btn.Max.Y-2)
		drawRoundedRect(img, inner, maxInt(2, radius-2), 1, colWhite)
		drawCenteredText(img, face, btn, colWhite, text)
	} else {
		fillRoundedRect(img, btn, radius, colWhite)
		drawRoundedRect(img, btn, radius, 2, colBlack)
		drawCenteredText(img, face, btn, colBlack, text)
	}
}

// fillDitheredRect fills a rectangle with a crisp e-ink stipple dot pattern.
func fillDitheredRect(img *image.Gray, rect image.Rectangle, step int, fg, bg color.Gray) {
	b := img.Bounds().Intersect(rect)
	if step < 2 {
		step = 2
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if (x%step == 0) && (y%step == 0) {
				img.SetGray(x, y, fg)
			} else {
				img.SetGray(x, y, bg)
			}
		}
	}
}

// drawDashedHLine draws a horizontal dashed grid/divider line.
func drawDashedHLine(img *image.Gray, x0, x1, y, dashLen, gapLen int, c color.Gray) {
	b := img.Bounds()
	if y < b.Min.Y || y >= b.Max.Y {
		return
	}
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	period := dashLen + gapLen
	if period <= 0 {
		period = 6
	}
	for x := maxInt(x0, b.Min.X); x < minInt(x1, b.Max.X); x++ {
		if (x-x0)%period < dashLen {
			img.SetGray(x, y, c)
		}
	}
}

// drawToggleSwitch renders a physical-style pill toggle switch (track + sliding circular knob).
func drawToggleSwitch(img *image.Gray, rect image.Rectangle, isOn bool) {
	r := rect.Dy() / 2
	if isOn {
		// On state inside an inverted black card: white pill track, black knob on right
		fillRoundedRect(img, rect, r, colWhite)
		drawRoundedRect(img, rect, r, 2, colWhite)
		knobR := r - 4
		if knobR < 4 {
			knobR = 4
		}
		kx := rect.Max.X - r
		ky := (rect.Min.Y + rect.Max.Y) / 2
		fillCircle(img, kx, ky, knobR, colBlack)
		// Small 'I' indicator on left side of track
		drawThickLine(img, rect.Min.X+r-2, ky-5, rect.Min.X+r-2, ky+5, 2, colBlack)
	} else {
		// Off state inside a white card: light gray track with 2px black border, black knob on left
		fillRoundedRect(img, rect, r, colLight)
		drawRoundedRect(img, rect, r, 2, colBlack)
		knobR := r - 5
		if knobR < 4 {
			knobR = 4
		}
		kx := rect.Min.X + r
		ky := (rect.Min.Y + rect.Max.Y) / 2
		fillCircle(img, kx, ky, knobR, colWhite)
		drawCircleOutline(img, kx, ky, knobR, 2, colBlack)
		// Small 'O' ring on right side of track
		drawCircleOutline(img, rect.Max.X-r+1, ky, 4, 2, colDark)
	}
}

func drawCircleOutline(img *image.Gray, cx, cy, r, thickness int, c color.Gray) {
	if r <= 0 || thickness <= 0 {
		return
	}
	b := img.Bounds()
	rOut := float64(r) + 0.4
	rIn := float64(maxInt(0, r-thickness)) + 0.4
	rOut2 := rOut * rOut
	rIn2 := rIn * rIn

	for dy := -r - 1; dy <= r+1; dy++ {
		y := cy + dy
		if y < b.Min.Y || y >= b.Max.Y {
			continue
		}
		for dx := -r - 1; dx <= r+1; dx++ {
			d2 := float64(dx*dx + dy*dy)
			if d2 <= rOut2 && d2 >= rIn2 {
				x := cx + dx
				if x >= b.Min.X && x < b.Max.X {
					img.SetGray(x, y, c)
				}
			}
		}
	}
}

// drawBatteryIcon renders a 26x14 battery graphic with fill bar at (x, y).
func drawBatteryIcon(img *image.Gray, x, y, pct int, charging bool) {
	body := image.Rect(x, y, x+24, y+13)
	nub := image.Rect(x+24, y+3, x+27, y+10)
	fillRect(img, body, colWhite)
	drawRect(img, body, 2, colBlack)
	fillRect(img, nub, colBlack)

	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	fillW := (pct * 18) / 100
	if fillW > 0 {
		fillRect(img, image.Rect(x+3, y+3, x+3+fillW, y+10), colBlack)
	}
	if charging {
		// Tiny bolt indicator next to battery
		drawThickLine(img, x+13, y+2, x+10, y+7, 2, colWhite)
		drawThickLine(img, x+10, y+7, x+14, y+7, 2, colWhite)
	}
}

// resolveWidgetIcon chooses an icon identifier based on explicit config or Czech/English keywords.
func resolveWidgetIcon(wd config.WidgetConfig) string {
	if wd.Icon != "" && wd.Icon != "auto" {
		return wd.Icon
	}
	if wd.Type == "timer" {
		return "clock"
	}
	if wd.Type == "calendar" {
		return "calendar"
	}
	lbl := strings.ToLower(wd.Label + " " + wd.Subtitle + " " + wd.Unit)
	switch {
	case strings.Contains(lbl, "svět") || strings.Contains(lbl, "lamp") || strings.Contains(lbl, "light") || strings.Contains(lbl, "žárov"):
		return "light"
	case strings.Contains(lbl, "tepl") || strings.Contains(lbl, "°c") || strings.Contains(lbl, "termostat") || strings.Contains(lbl, "temp"):
		return "thermometer"
	case strings.Contains(lbl, "vlhk") || strings.Contains(lbl, "% rv") || strings.Contains(lbl, "hum") || strings.Contains(lbl, "vod"):
		return "drop"
	case strings.Contains(lbl, "výkon") || strings.Contains(lbl, "fve") || strings.Contains(lbl, "spotřeb") || strings.HasSuffix(strings.TrimSpace(wd.Unit), "W"):
		return "bolt"
	case strings.Contains(lbl, "zásuv") || strings.Contains(lbl, "plug") || strings.Contains(lbl, "socket"):
		return "plug"
	case strings.Contains(lbl, "vrat") || strings.Contains(lbl, "garáž") || strings.Contains(lbl, "dveř") || strings.Contains(lbl, "brán") || strings.Contains(lbl, "okn"):
		return "door"
	case strings.Contains(lbl, "režim") || strings.Contains(lbl, "dům") || strings.Contains(lbl, "doma") || strings.Contains(lbl, "obýv"):
		return "home"
	case strings.Contains(lbl, "ventil") || strings.Contains(lbl, "klim") || strings.Contains(lbl, "fan"):
		return "fan"
	case strings.Contains(lbl, "jas") || strings.Contains(lbl, "slun") || strings.Contains(lbl, "venku"):
		return "sun"
	case strings.Contains(lbl, "časovač") || strings.Contains(lbl, "minutk") || strings.Contains(lbl, "timer"):
		return "clock"
	case strings.Contains(lbl, "kalendář") || strings.Contains(lbl, "událost") || strings.Contains(lbl, "calendar"):
		return "calendar"
	}
	switch wd.Type {
	case "switch":
		return "light"
	case "button":
		return "power"
	case "stepper":
		return "thermometer"
	case "gauge", "chart":
		return "bolt"
	case "select":
		return "home"
	case "timer":
		return "clock"
	case "calendar":
		return "calendar"
	case "weather":
		return "sun"
	default:
		return "thermometer"
	}
}

// drawIconBadge renders a circular icon badge centered at (cx, cy).
func drawIconBadge(img *image.Gray, cx, cy, r int, icon string, invertedCard bool) {
	badgeBg := colWhite
	badgeBorder := colBlack
	fg := colBlack
	if invertedCard {
		badgeBg = colWhite
		badgeBorder = colWhite
		fg = colBlack
	}

	fillCircle(img, cx, cy, r, badgeBg)
	drawCircleOutline(img, cx, cy, r, 2, badgeBorder)

	switch icon {
	case "light":
		// Bulb globe + base + filament
		drawCircleOutline(img, cx, cy-3, 6, 2, fg)
		fillRect(img, image.Rect(cx-3, cy+3, cx+4, cy+6), fg)
		drawThickLine(img, cx-3, cy+8, cx+3, cy+8, 2, fg)
		// Rays
		drawThickLine(img, cx, cy-12, cx, cy-10, 2, fg)
		drawThickLine(img, cx-9, cy-3, cx-11, cy-3, 2, fg)
		drawThickLine(img, cx+9, cy-3, cx+11, cy-3, 2, fg)

	case "thermometer":
		// Stem + bottom bulb + scale ticks
		drawRoundedRect(img, image.Rect(cx-3, cy-10, cx+3, cy+4), 2, 2, fg)
		fillCircle(img, cx, cy+6, 5, fg)
		drawThickLine(img, cx, cy-4, cx, cy+5, 2, fg)
		drawThickLine(img, cx+4, cy-7, cx+7, cy-7, 1, fg)
		drawThickLine(img, cx+4, cy-3, cx+7, cy-3, 1, fg)
		drawThickLine(img, cx+4, cy+1, cx+7, cy+1, 1, fg)

	case "drop":
		// Water droplet
		fillCircle(img, cx, cy+4, 6, fg)
		drawThickLine(img, cx, cy-9, cx-5, cy+2, 2, fg)
		drawThickLine(img, cx, cy-9, cx+5, cy+2, 2, fg)
		fillRect(img, image.Rect(cx-3, cy-2, cx+4, cy+4), fg)
		fillCircle(img, cx-2, cy+4, 2, badgeBg)

	case "bolt":
		// Lightning bolt
		drawThickLine(img, cx+2, cy-9, cx-5, cy+1, 2, fg)
		drawThickLine(img, cx-5, cy+1, cx+2, cy+1, 2, fg)
		drawThickLine(img, cx+2, cy+1, cx-2, cy+9, 2, fg)

	case "plug":
		// Power plug body + prongs + cord
		fillRoundedRect(img, image.Rect(cx-6, cy-3, cx+7, cy+5), 3, fg)
		drawThickLine(img, cx-3, cy-8, cx-3, cy-3, 2, fg)
		drawThickLine(img, cx+3, cy-8, cx+3, cy-3, 2, fg)
		drawThickLine(img, cx, cy+5, cx, cy+10, 2, fg)

	case "door":
		// Door frame + panel + knob
		drawRect(img, image.Rect(cx-7, cy-9, cx+7, cy+10), 2, fg)
		drawThickLine(img, cx-9, cy+9, cx+9, cy+9, 2, fg)
		fillCircle(img, cx+3, cy+1, 2, fg)

	case "home":
		// Roof + walls + door
		drawThickLine(img, cx-10, cy-1, cx, cy-9, 2, fg)
		drawThickLine(img, cx, cy-9, cx+10, cy-1, 2, fg)
		drawRect(img, image.Rect(cx-7, cy-1, cx+7, cy+9), 2, fg)
		fillRect(img, image.Rect(cx-2, cy+3, cx+3, cy+9), fg)

	case "fan":
		fillCircle(img, cx, cy, 3, fg)
		fillCircle(img, cx, cy-6, 3, fg)
		fillCircle(img, cx, cy+6, 3, fg)
		fillCircle(img, cx-6, cy, 3, fg)
		fillCircle(img, cx+6, cy, 3, fg)

	case "sun":
		fillCircle(img, cx, cy, 5, fg)
		for i := 0; i < 8; i++ {
			ang := float64(i) * math.Pi / 4
			x1 := cx + int(math.Round(7*math.Cos(ang)))
			y1 := cy + int(math.Round(7*math.Sin(ang)))
			x2 := cx + int(math.Round(10*math.Cos(ang)))
			y2 := cy + int(math.Round(10*math.Sin(ang)))
			drawThickLine(img, x1, y1, x2, y2, 2, fg)
		}

	case "clock":
		drawCircleOutline(img, cx, cy, 9, 2, fg)
		drawThickLine(img, cx, cy, cx, cy-5, 2, fg)
		drawThickLine(img, cx, cy, cx+4, cy, 2, fg)
		fillCircle(img, cx, cy, 2, fg)

	case "calendar":
		drawRoundedRect(img, image.Rect(cx-8, cy-6, cx+8, cy+8), 2, 2, fg)
		drawThickLine(img, cx-4, cy-9, cx-4, cy-5, 2, fg)
		drawThickLine(img, cx+4, cy-9, cx+4, cy-5, 2, fg)
		fillRect(img, image.Rect(cx-8, cy-3, cx+9, cy-1), fg)
		fillRect(img, image.Rect(cx-4, cy+1, cx-1, cy+3), fg)
		fillRect(img, image.Rect(cx+2, cy+1, cx+5, cy+3), fg)
		fillRect(img, image.Rect(cx-4, cy+4, cx-1, cy+6), fg)
		fillRect(img, image.Rect(cx+2, cy+4, cx+5, cy+6), fg)

	default: // "power"
		drawArc(img, cx, cy+1, 7, 2, -0.3*math.Pi, 1.3*math.Pi, fg)
		drawThickLine(img, cx, cy-8, cx, cy+1, 2, fg)
	}
}

func drawDitheredVerticalArea(img *image.Gray, p0, p1 image.Point, baseY int) {
	if p1.X < p0.X {
		p0, p1 = p1, p0
	}
	dx := p1.X - p0.X
	b := img.Bounds()
	for x := p0.X; x <= p1.X; x++ {
		if x < b.Min.X || x >= b.Max.X {
			continue
		}
		yTop := p0.Y
		if dx > 0 {
			yTop = p0.Y + (p1.Y-p0.Y)*(x-p0.X)/dx
		}
		for y := yTop; y <= baseY; y++ {
			if y >= b.Min.Y && y < b.Max.Y {
				if (x+y)%2 == 0 {
					img.SetGray(x, y, colMidGray)
				} else {
					img.SetGray(x, y, colWhite)
				}
			}
		}
	}
}
