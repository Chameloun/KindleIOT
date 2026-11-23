package ui

import (
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// X11Event represents a tap or close event from the X11 display server.
type X11Event struct {
	TapX, TapY int
	IsTap      bool
	Redraw     bool
	Closed     bool
}

// X11Window manages a pure-Go X11 connection and e-ink-friendly framebuffer blitting.
type X11Window struct {
	mu           sync.Mutex
	conn         *xgb.Conn
	win          xproto.Window
	gc           xproto.Gcontext
	depth        byte
	bpp          byte
	byteOrderLSB bool
	redMask      uint32
	greenMask    uint32
	blueMask     uint32
	width        uint16
	height       uint16
	orientation  string
	wmDelete     xproto.Atom
	closed       bool
}

// OpenX11Window connects to the X server, creates a window titled for Kindle Awesome WM, and maps it.
func OpenX11Window(width, height int, wmTitle string, fullscreen bool) (*X11Window, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connect x11: %w", err)
	}

	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)

	// Find visual masks matching RootVisual and bpp from PixmapFormats.
	var rMask, gMask, bMask uint32
	for _, d := range screen.AllowedDepths {
		if d.Depth != screen.RootDepth {
			continue
		}
		for _, v := range d.Visuals {
			if v.VisualId == screen.RootVisual {
				rMask = v.RedMask
				gMask = v.GreenMask
				bMask = v.BlueMask
				break
			}
		}
	}
	bpp := screen.RootDepth
	for _, pf := range setup.PixmapFormats {
		if pf.Depth == screen.RootDepth {
			bpp = pf.BitsPerPixel
			break
		}
	}

	winID, err := xproto.NewWindowId(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("alloc window id: %w", err)
	}

	w := uint16(width)
	h := uint16(height)
	mask := uint32(xproto.CwBackPixel | xproto.CwEventMask)
	vals := []uint32{
		screen.WhitePixel,
		xproto.EventMaskExposure | xproto.EventMaskButtonPress | xproto.EventMaskKeyPress | xproto.EventMaskStructureNotify,
	}

	if err := xproto.CreateWindowChecked(
		conn,
		screen.RootDepth,
		winID,
		screen.Root,
		0, 0, w, h,
		0,
		xproto.WindowClassInputOutput,
		screen.RootVisual,
		mask,
		vals,
	).Check(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("create window: %w", err)
	}

	// Set WM_NAME (used by Kindle Awesome WM for L:A_N:application_... routing) and _NET_WM_NAME.
	setAtomProp := func(propName, typeName string, data []byte) {
		pAtom, err1 := xproto.InternAtom(conn, false, uint16(len(propName)), propName).Reply()
		tAtom, err2 := xproto.InternAtom(conn, false, uint16(len(typeName)), typeName).Reply()
		if err1 == nil && err2 == nil {
			xproto.ChangeProperty(conn, xproto.PropModeReplace, winID, pAtom.Atom, tAtom.Atom, 8, uint32(len(data)), data)
		}
	}
	setAtomProp("WM_NAME", "STRING", []byte(wmTitle))
	setAtomProp("_NET_WM_NAME", "UTF8_STRING", []byte(wmTitle))

	// Register WM_DELETE_WINDOW.
	var wmDeleteAtom xproto.Atom
	if wmProto, err1 := xproto.InternAtom(conn, false, 12, "WM_PROTOCOLS").Reply(); err1 == nil {
		if del, err2 := xproto.InternAtom(conn, false, 16, "WM_DELETE_WINDOW").Reply(); err2 == nil {
			wmDeleteAtom = del.Atom
			buf := make([]byte, 4)
			xgb.Put32(buf, uint32(del.Atom))
			xproto.ChangeProperty(conn, xproto.PropModeReplace, winID, wmProto.Atom, xproto.AtomAtom, 32, 1, buf)
		}
	}

	if fullscreen {
		if wmState, err1 := xproto.InternAtom(conn, false, 13, "_NET_WM_STATE").Reply(); err1 == nil {
			if fsAtom, err2 := xproto.InternAtom(conn, false, 24, "_NET_WM_STATE_FULLSCREEN").Reply(); err2 == nil {
				buf := make([]byte, 4)
				xgb.Put32(buf, uint32(fsAtom.Atom))
				xproto.ChangeProperty(conn, xproto.PropModeReplace, winID, wmState.Atom, xproto.AtomAtom, 32, 1, buf)
			}
		}
	}

	gcID, err := xproto.NewGcontextId(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("alloc gc: %w", err)
	}
	if err := xproto.CreateGCChecked(conn, gcID, xproto.Drawable(winID), 0, nil).Check(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("create gc: %w", err)
	}

	xproto.MapWindow(conn, winID)
	conn.Sync()

	return &X11Window{
		conn:         conn,
		win:          winID,
		gc:           gcID,
		depth:        screen.RootDepth,
		bpp:          bpp,
		byteOrderLSB: setup.ImageByteOrder == xproto.ImageOrderLSBFirst,
		redMask:      rMask,
		greenMask:    gMask,
		blueMask:     bMask,
		width:        w,
		height:       h,
		wmDelete:     wmDeleteAtom,
	}, nil
}

// Events blocks and emits X11 interaction events on the returned channel until closed.
func (x *X11Window) Events() <-chan X11Event {
	ch := make(chan X11Event, 32)
	go func() {
		defer close(ch)
		for {
			ev, err := x.conn.WaitForEvent()
			if ev == nil && err == nil {
				return
			}
			if err != nil {
				continue
			}
			switch e := ev.(type) {
			case xproto.ExposeEvent:
				if e.Count == 0 {
					ch <- X11Event{Redraw: true}
				}
			case xproto.ButtonPressEvent:
				if e.Detail == 1 {
					x.mu.Lock()
					orient := x.orientation
					pw, ph := int(x.width), int(x.height)
					x.mu.Unlock()
					tx, ty := TransformTapFromDisplay(int(e.EventX), int(e.EventY), pw, ph, orient)
					ch <- X11Event{TapX: tx, TapY: ty, IsTap: true}
				}
			case xproto.ClientMessageEvent:
				if len(e.Data.Data32) > 0 && xproto.Atom(e.Data.Data32[0]) == x.wmDelete {
					ch <- X11Event{Closed: true}
					return
				}
			case xproto.DestroyNotifyEvent:
				ch <- X11Event{Closed: true}
				return
			}
		}
	}()
	return ch
}

// SetOrientation configures software rotation between logical UI canvas and physical display.
func (x *X11Window) SetOrientation(orient string) {
	x.mu.Lock()
	x.orientation = orient
	x.mu.Unlock()
}

// RotateGrayForDisplay rotates a logical UI image (e.g., 800x600) onto physical display dimensions (e.g., 600x800).
func RotateGrayForDisplay(src *image.Gray, physW, physH int, orient string) *image.Gray {
	if src == nil {
		return nil
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == physW && sh == physH {
		return src
	}
	if sw != physH || sh != physW {
		return src
	}
	dst := image.NewGray(image.Rect(0, 0, physW, physH))
	switch orient {
	case "landscape":
		for ly := 0; ly < sh; ly++ {
			px := physW - 1 - ly
			rowOff := (b.Min.Y + ly) * src.Stride
			for lx := 0; lx < sw; lx++ {
				dst.Pix[lx*dst.Stride+px] = src.Pix[rowOff+b.Min.X+lx]
			}
		}
		return dst
	case "landscape_cw":
		for ly := 0; ly < sh; ly++ {
			px := ly
			rowOff := (b.Min.Y + ly) * src.Stride
			for lx := 0; lx < sw; lx++ {
				py := physH - 1 - lx
				dst.Pix[py*dst.Stride+px] = src.Pix[rowOff+b.Min.X+lx]
			}
		}
		return dst
	default:
		return src
	}
}

// TransformTapFromDisplay maps raw X11 display coordinates back to logical UI canvas coordinates.
func TransformTapFromDisplay(px, py, physW, physH int, orient string) (int, int) {
	if physW >= physH {
		return px, py
	}
	switch orient {
	case "landscape":
		return py, physW - 1 - px
	case "landscape_cw":
		return physH - 1 - py, px
	default:
		return px, py
	}
}

// FlashAndDraw clears e-ink ghosting by briefly flashing solid black then drawing the image.
func (x *X11Window) FlashAndDraw(img *image.Gray) error {
	x.mu.Lock()
	w, h := int(x.width), int(x.height)
	x.mu.Unlock()
	black := image.NewGray(image.Rect(0, 0, w, h))
	if err := x.Draw(black); err != nil {
		return err
	}
	time.Sleep(180 * time.Millisecond)
	return x.Draw(img)
}

// Draw uploads a grayscale image to the X11 window in small horizontal bands.
func (x *X11Window) Draw(img *image.Gray) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return nil
	}

	img = RotateGrayForDisplay(img, int(x.width), int(x.height), x.orientation)
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}

	const stripRows = 32
	bytesPerPx := int(x.bpp) / 8
	if bytesPerPx < 1 {
		bytesPerPx = 1
	}

	for y0 := 0; y0 < h; y0 += stripRows {
		rows := stripRows
		if y0+rows > h {
			rows = h - y0
		}
		buf := x.encodeStrip(img, w, y0, rows, bytesPerPx)
		err := xproto.PutImageChecked(
			x.conn,
			xproto.ImageFormatZPixmap,
			xproto.Drawable(x.win),
			x.gc,
			uint16(w),
			uint16(rows),
			0,
			int16(y0),
			0,
			x.depth,
			buf,
		).Check()
		if err != nil {
			return err
		}
	}
	x.conn.Sync()
	return nil
}

func (x *X11Window) encodeStrip(img *image.Gray, w, y0, rows, bytesPerPx int) []byte {
	buf := make([]byte, w*rows*bytesPerPx)
	off := 0
	for dy := 0; dy < rows; dy++ {
		rowStart := (y0 + dy) * img.Stride
		for dx := 0; dx < w; dx++ {
			g := img.Pix[rowStart+dx]
			switch x.bpp {
			case 8:
				buf[off] = g
			case 16:
				// RGB565: R(5) G(6) B(5)
				val := uint16(g>>3)<<11 | uint16(g>>2)<<5 | uint16(g>>3)
				if x.byteOrderLSB {
					buf[off] = byte(val)
					buf[off+1] = byte(val >> 8)
				} else {
					buf[off] = byte(val >> 8)
					buf[off+1] = byte(val)
				}
			case 32:
				if x.byteOrderLSB {
					buf[off] = g
					buf[off+1] = g
					buf[off+2] = g
					buf[off+3] = 0xff
				} else {
					buf[off] = 0xff
					buf[off+1] = g
					buf[off+2] = g
					buf[off+3] = g
				}
			case 24:
				buf[off] = g
				buf[off+1] = g
				buf[off+2] = g
			default:
				buf[off] = g
			}
			off += bytesPerPx
		}
	}
	return buf
}

// Close closes the X11 connection cleanly.
func (x *X11Window) Close() {
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.closed {
		x.closed = true
		x.conn.Close()
	}
}
