package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"kindleiot/internal/calendar"
	"kindleiot/internal/config"
	"kindleiot/internal/kindle"
	"kindleiot/internal/mqtt"
	"kindleiot/internal/weather"
)

var (
	colWhite   = color.Gray{Y: 255}
	colLight   = color.Gray{Y: 232}
	colMidGray = color.Gray{Y: 165}
	colDark    = color.Gray{Y: 80}
	colBlack   = color.Gray{Y: 0}
)

// ActionType identifies a UI interaction triggered by a tap.
type ActionType int

const (
	ActionNone ActionType = iota
	ActionSelectPage
	ActionSubPagePrev
	ActionSubPageNext
	ActionToggleSettings
	ActionBrightnessDelta
	ActionToggleKeepAwake
	ActionFullRefresh
	ActionQuit
	ActionDismissNotice
	ActionWidgetToggle
	ActionWidgetButton
	ActionWidgetStep
	ActionWidgetSelect
	ActionEnterStandby
	ActionWakeStandby
	ActionOpenSensorDetail
	ActionCloseDetail
	ActionPinKey
	ActionPinBackspace
	ActionPinCancel
	ActionLockPin
	ActionTimerStart
	ActionTimerAdd
	ActionTimerCancel
	ActionOpenWeatherDetail
	ActionOpenCalendarDetail
	ActionToggleTheme
)

// HitAction describes an action bound to a screen rectangle.
type HitAction struct {
	Rect      image.Rectangle
	Type      ActionType
	PageIdx   int
	Widget    config.WidgetConfig
	StepDelta float64
	Payload   string
	IntVal    int
}

// Controller callbacks triggered when user taps a UI control.
type Controller struct {
	OnQuit          func()
	OnFullRefresh   func()
	OnBrightness    func(delta int)
	OnToggleAwake   func()
	OnWidgetChanged func()
}

type fontSet struct {
	small    font.Face
	regular  font.Face
	bold16   font.Face
	bold20   font.Face
	bold28   font.Face
	bold34   font.Face
	clock64  font.Face
}

type activeTimer struct {
	Widget    config.WidgetConfig
	StartedAt time.Time
	EndsAt    time.Time
	Duration  time.Duration
}

// Renderer draws the e-ink dashboard into an 8-bit grayscale framebuffer and tracks tap regions.
type Renderer struct {
	mu                 sync.Mutex
	cfg                *config.Config
	mq                 *mqtt.Manager
	hw                 *kindle.System
	weatherSvc         *weather.Service
	calSvc             *calendar.Service
	ctrl               Controller
	fonts              fontSet
	canvas             *image.Gray
	hits               []HitAction
	activePage         int
	subPage            int
	showSettings       bool
	standbyMode        bool
	keepAwake          bool
	detailWidget       *config.WidgetConfig
	weatherDetailOpen  bool
	calendarDetailOpen bool
	darkOverride       *bool
	activeTimers       map[string]activeTimer
	lastSchedFired     map[string]string
	pinModalOpen       bool
	pinInput           string
	pinError           bool
	pinUnlockedUntil   time.Time
	pendingPinAction   *HitAction
	noticeText         string
	noticeUntil        time.Time
	confirmID          string
	confirmUntil       time.Time
	lastError          string
	lastErrUntil       time.Time
	activeAlerts       map[string]bool
	renderCount        int
}

// NewRenderer creates an e-ink dashboard renderer with embedded DejaVu Sans fonts.
func NewRenderer(cfg *config.Config, mq *mqtt.Manager, hw *kindle.System, ctrl Controller) (*Renderer, error) {
	fs, err := loadFonts()
	if err != nil {
		return nil, err
	}
	w, h := cfg.Device.LogicalSize()
	return &Renderer{
		cfg:            cfg,
		mq:             mq,
		hw:             hw,
		ctrl:           ctrl,
		fonts:          fs,
		canvas:         image.NewGray(image.Rect(0, 0, w, h)),
		keepAwake:      cfg.Device.PreventSleep,
		activeAlerts:   make(map[string]bool),
		activeTimers:   make(map[string]activeTimer),
		lastSchedFired: make(map[string]string),
	}, nil
}

func loadFonts() (fontSet, error) {
	regTTF, err := opentype.Parse(fontRegularTTF)
	if err != nil {
		return fontSet{}, fmt.Errorf("parse regular font: %w", err)
	}
	boldTTF, err := opentype.Parse(fontBoldTTF)
	if err != nil {
		return fontSet{}, fmt.Errorf("parse bold font: %w", err)
	}
	mk := func(f *opentype.Font, sz float64) (font.Face, error) {
		return opentype.NewFace(f, &opentype.FaceOptions{
			Size:    sz,
			DPI:     72,
			Hinting: font.HintingFull,
		})
	}
	var fs fontSet
	if fs.small, err = mk(regTTF, 13); err != nil {
		return fs, err
	}
	if fs.regular, err = mk(regTTF, 16); err != nil {
		return fs, err
	}
	if fs.bold16, err = mk(boldTTF, 16); err != nil {
		return fs, err
	}
	if fs.bold20, err = mk(boldTTF, 20); err != nil {
		return fs, err
	}
	if fs.bold28, err = mk(boldTTF, 28); err != nil {
		return fs, err
	}
	if fs.bold34, err = mk(boldTTF, 34); err != nil {
		return fs, err
	}
	if fs.clock64, err = mk(boldTTF, 64); err != nil {
		return fs, err
	}
	return fs, nil
}

// Config returns the active configuration pointer safely.
func (r *Renderer) Config() *config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

// SetWeatherService attaches the Open-Meteo weather forecast provider to the renderer.
func (r *Renderer) SetWeatherService(ws *weather.Service) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.weatherSvc = ws
}

// SetCalendarService attaches the iCal feed provider to the renderer.
func (r *Renderer) SetCalendarService(cs *calendar.Service) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calSvc = cs
}

// SetStandby switches the standby screensaver mode on or off and reports whether the state changed.
func (r *Renderer) SetStandby(active bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.standbyMode == active {
		return false
	}
	r.standbyMode = active
	if active {
		r.showSettings = false
	}
	return true
}

// IsStandby reports whether the standby clock & sensors screensaver is currently displayed.
func (r *Renderer) IsStandby() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.standbyMode
}

// EvaluateAlerts checks all alert-enabled widgets against their min/max limits and returns newly triggered alert messages.
func (r *Renderer) EvaluateAlerts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activeAlerts == nil {
		r.activeAlerts = make(map[string]bool)
	}
	var triggeredMsgs []string
	checkW := func(w config.WidgetConfig) {
		if !w.AlertEnabled || w.ID == "" {
			return
		}
		tv, ok := r.mq.GetTopic(w.BrokerID, w.StateTopic)
		if !ok {
			return
		}
		raw := mqtt.ExtractValue(tv.Payload, w.StateJSONPath)
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return
		}
		if trig, limitStr := w.CheckAlert(f); trig {
			if !r.activeAlerts[w.ID] {
				r.activeAlerts[w.ID] = true
				msg := fmt.Sprintf(r.tr().AlertPrefix, w.Label, formatNumber(f, w.EffectivePrecision()), suffixUnit(w.Unit), limitStr, suffixUnit(w.Unit))
				r.noticeText = msg
				r.noticeUntil = time.Now().Add(2 * time.Minute)
				r.standbyMode = false
				triggeredMsgs = append(triggeredMsgs, msg)
			}
		} else {
			delete(r.activeAlerts, w.ID)
		}
	}
	for _, p := range r.cfg.Pages {
		for _, w := range p.Widgets {
			checkW(w)
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				checkW(pin.Widget)
			}
		}
	}
	return triggeredMsgs
}

func (r *Renderer) isWidgetAlerting(wd config.WidgetConfig) (bool, string) {
	if !wd.AlertEnabled {
		return false, ""
	}
	tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic)
	if !ok {
		return false, ""
	}
	raw := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return false, ""
	}
	return wd.CheckAlert(f)
}

func (r *Renderer) lang() string {
	if r.cfg == nil {
		return "en"
	}
	return config.NormalizeLanguage(r.cfg.Device.Language)
}

func (r *Renderer) tr() uiStrings {
	return stringsForLang(r.lang())
}

// visiblePageIndices returns all page indices where Hidden == false, preserving config order.
func (r *Renderer) visiblePageIndices() []int {
	if r.cfg == nil {
		return nil
	}
	var vis []int
	for i, p := range r.cfg.Pages {
		if !p.Hidden {
			vis = append(vis, i)
		}
	}
	return vis
}

func (r *Renderer) resolveActivePageLocked() (int, bool) {
	vis := r.visiblePageIndices()
	if len(vis) == 0 {
		return -1, false
	}
	if r.activePage >= 0 && r.activePage < len(r.cfg.Pages) && !r.cfg.Pages[r.activePage].Hidden {
		return r.activePage, true
	}
	r.activePage = vis[0]
	r.subPage = 0
	return r.activePage, true
}

// UpdateConfig hot-swaps the configuration cleanly.
func (r *Renderer) UpdateConfig(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lw, lh := cfg.Device.LogicalSize()
	if r.canvas.Bounds().Dx() != lw || r.canvas.Bounds().Dy() != lh {
		r.canvas = image.NewGray(image.Rect(0, 0, lw, lh))
	}
	r.cfg = cfg
	r.darkOverride = nil
	r.resolveActivePageLocked()
}

func (r *Renderer) isDarkModeLocked(now time.Time) bool {
	if r.darkOverride != nil {
		return *r.darkOverride
	}
	switch r.cfg.Device.ThemeMode {
	case "dark":
		return true
	case "auto_night":
		return r.cfg.Device.IsNightHour(now.Hour())
	default:
		return false
	}
}

func (r *Renderer) finishFrame(now time.Time, needFlash bool) (*image.Gray, bool) {
	out := image.NewGray(r.canvas.Bounds())
	copy(out.Pix, r.canvas.Pix)
	if r.isDarkModeLocked(now) {
		for i, v := range out.Pix {
			out.Pix[i] = 255 - v
		}
	}
	return out, needFlash
}

// ShowNotice displays a temporary banner message at the top of the panel.
func (r *Renderer) ShowNotice(msg string, duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.noticeText = msg
	r.noticeUntil = time.Now().Add(duration)
}

// Render draws the complete UI frame and returns a copy of the grayscale image and whether a full e-ink flash is recommended.
func (r *Renderer) Render() (*image.Gray, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.renderCount++
	needFlash := r.cfg.Device.FullRefreshEvery > 0 && r.renderCount%r.cfg.Device.FullRefreshEvery == 0

	w := r.canvas.Bounds().Dx()
	h := r.canvas.Bounds().Dy()
	fillRect(r.canvas, image.Rect(0, 0, w, h), colBgCanvas)
	r.hits = r.hits[:0]

	now := time.Now()
	snap := r.mq.Snapshot()
	hwStat := r.hw.ReadStatus()

	if r.pinModalOpen {
		topH := 62
		r.drawHeader(w, topH, now, snap, hwStat)
		r.drawPinModal(w, topH+10, h-16)
		return r.finishFrame(now, needFlash)
	}

	if r.standbyMode {
		r.drawStandbyScreen(w, h, now, snap, hwStat)
		return r.finishFrame(now, needFlash)
	}

	topH := 62
	r.drawHeader(w, topH, now, snap, hwStat)

	if r.weatherDetailOpen {
		r.drawWeatherDetailModal(w, topH+10, h-16, now)
		return r.finishFrame(now, needFlash)
	}
	if r.calendarDetailOpen {
		r.drawCalendarDetailModal(w, topH+10, h-16, now)
		return r.finishFrame(now, needFlash)
	}
	if r.detailWidget != nil {
		r.drawSensorDetailModal(w, topH+10, h-16, *r.detailWidget, now)
		return r.finishFrame(now, needFlash)
	}

	contentTop := topH + 10
	if r.showSettings {
		panelH := r.drawSettingsOverlay(w, topH+4, now, hwStat)
		contentTop = topH + panelH + 10
	} else if r.noticeText != "" && now.Before(r.noticeUntil) {
		r.drawBanner(w, topH+4, r.noticeText, false)
		contentTop = topH + 48
	} else if r.lastError != "" && now.Before(r.lastErrUntil) {
		r.drawBanner(w, topH+4, r.lastError, true)
		contentTop = topH + 48
	} else if !snap.AllConnected && snap.FirstError != "" {
		r.drawBanner(w, topH+4, r.tr().MQTTStatusLabel+snap.FirstError, true)
		contentTop = topH + 48
	}

	bottomH := 66
	contentBottom := h - bottomH - 8
	r.drawPageContent(w, contentTop, contentBottom, now)
	r.drawBottomTabs(w, h, bottomH)

	return r.finishFrame(now, needFlash)
}

func (r *Renderer) drawHeader(w, topH int, now time.Time, snap mqtt.ManagerSnapshot, hwStat kindle.Status) {
	tr := r.tr()
	fillRect(r.canvas, image.Rect(0, 0, w, topH), colWhite)
	fillRect(r.canvas, image.Rect(0, topH-3, w, topH-1), colBlack)
	fillRect(r.canvas, image.Rect(0, topH-1, w, topH), colMidGray)

	// Title + clock + connection status
	drawText(r.canvas, r.fonts.bold20, 14, 26, colBlack, truncateText(r.fonts.bold20, r.cfg.Device.Title, 190))

	statusLabel := tr.OfflineLabel
	if snap.TotalCount > 1 {
		statusLabel = fmt.Sprintf(tr.MQTTMulti, snap.ConnectedCount, snap.TotalCount)
	} else if snap.AllConnected {
		statusLabel = tr.MQTTOk
	}
	statusBox := image.Rect(14, 33, 108, 53)
	if snap.AllConnected {
		fillRoundedRect(r.canvas, statusBox, 9, colBlack)
		drawCenteredText(r.canvas, r.fonts.small, statusBox, colWhite, statusLabel)
	} else {
		fillRoundedRect(r.canvas, statusBox, 9, colLight)
		drawRoundedRect(r.canvas, statusBox, 9, 2, colBlack)
		drawCenteredText(r.canvas, r.fonts.small, statusBox, colBlack, statusLabel)
	}

	drawText(r.canvas, r.fonts.bold16, 118, 49, colBlack, now.Format("15:04"))
	if hwStat.Battery >= 0 {
		drawBatteryIcon(r.canvas, 176, 37, hwStat.Battery, hwStat.Charging)
		chg := ""
		if hwStat.Charging {
			chg = "+"
		}
		drawText(r.canvas, r.fonts.small, 208, 48, colDark, fmt.Sprintf("%d%%%s", hwStat.Battery, chg))
	}

	// Right-side action buttons: Settings/Light, Refresh, Quit
	btnW, btnH := 48, 44
	gap := 8
	y0 := 8

	quitRect := image.Rect(w-14-btnW, y0, w-14, y0+btnH)
	refRect := image.Rect(quitRect.Min.X-gap-btnW, y0, quitRect.Min.X-gap, y0+btnH)
	setRect := image.Rect(refRect.Min.X-gap-btnW, y0, refRect.Min.X-gap, y0+btnH)

	drawRoundedButton(r.canvas, setRect, 8, r.fonts.bold20, "☀", r.showSettings)
	drawRoundedButton(r.canvas, refRect, 8, r.fonts.bold20, "↻", false)
	drawRoundedButton(r.canvas, quitRect, 8, r.fonts.bold20, "✕", false)

	r.hits = append(r.hits,
		HitAction{Rect: setRect, Type: ActionToggleSettings},
		HitAction{Rect: refRect, Type: ActionFullRefresh},
		HitAction{Rect: quitRect, Type: ActionQuit},
	)
}

func (r *Renderer) drawSettingsOverlay(w, y int, now time.Time, hwStat kindle.Status) int {
	tr := r.tr()
	panelH := 142
	box := image.Rect(12, y, w-12, y+panelH)
	fillRect(r.canvas, box, colLight)
	drawRect(r.canvas, box, 3, colBlack)

	// Row 1: Frontlight brightness
	drawText(r.canvas, r.fonts.bold16, box.Min.X+12, box.Min.Y+30, colBlack,
		fmt.Sprintf(tr.BacklightLabel, max0(hwStat.Brightness)))

	minusR := image.Rect(box.Max.X-190, box.Min.Y+8, box.Max.X-128, box.Min.Y+48)
	plusR := image.Rect(box.Max.X-120, box.Min.Y+8, box.Max.X-58, box.Min.Y+48)
	closeR := image.Rect(box.Max.X-50, box.Min.Y+8, box.Max.X-8, box.Min.Y+48)

	drawButtonBox(r.canvas, minusR, r.fonts.bold20, "−", false)
	drawButtonBox(r.canvas, plusR, r.fonts.bold20, "+", false)
	drawButtonBox(r.canvas, closeR, r.fonts.bold16, tr.OK, true)

	// Row 2: Keep awake + Enter Standby + Theme toggle (+ PIN Lock when configured)
	row2Y0, row2Y1 := box.Min.Y+56, box.Min.Y+96
	availW := box.Dx() - 24
	hasPin := strings.TrimSpace(r.cfg.Device.PinCode) != ""
	awakeLabel := tr.AwakeOff
	if r.keepAwake {
		awakeLabel = tr.AwakeOn
	}
	darkOn := r.isDarkModeLocked(now)

	hits := []HitAction{
		{Rect: minusR, Type: ActionBrightnessDelta, IntVal: -3},
		{Rect: plusR, Type: ActionBrightnessDelta, IntVal: +3},
		{Rect: closeR, Type: ActionToggleSettings},
	}

	x := box.Min.X + 12
	if hasPin {
		w1 := (availW * 34) / 100
		w2 := (availW * 22) / 100
		w3 := (availW * 23) / 100
		w4 := availW - w1 - w2 - w3 - 24

		awakeR := image.Rect(x, row2Y0, x+w1, row2Y1)
		standbyR := image.Rect(awakeR.Max.X+8, row2Y0, awakeR.Max.X+8+w2, row2Y1)
		themeR := image.Rect(standbyR.Max.X+8, row2Y0, standbyR.Max.X+8+w3, row2Y1)
		lockR := image.Rect(themeR.Max.X+8, row2Y0, themeR.Max.X+8+w4, row2Y1)

		themeLabel := tr.ThemeLightShort
		if darkOn {
			themeLabel = tr.ThemeDarkShort
		}
		drawButtonBox(r.canvas, awakeR, r.fonts.bold16, awakeLabel, r.keepAwake)
		drawButtonBox(r.canvas, standbyR, r.fonts.bold16, tr.StandbyShort, false)
		drawButtonBox(r.canvas, themeR, r.fonts.bold16, themeLabel, darkOn)
		drawButtonBox(r.canvas, lockR, r.fonts.bold16, tr.LockBtn, false)

		hits = append(hits,
			HitAction{Rect: awakeR, Type: ActionToggleKeepAwake},
			HitAction{Rect: standbyR, Type: ActionEnterStandby},
			HitAction{Rect: themeR, Type: ActionToggleTheme},
			HitAction{Rect: lockR, Type: ActionLockPin},
		)
	} else {
		w1 := (availW * 38) / 100
		w2 := (availW * 28) / 100
		w3 := availW - w1 - w2 - 16

		awakeR := image.Rect(x, row2Y0, x+w1, row2Y1)
		standbyR := image.Rect(awakeR.Max.X+8, row2Y0, awakeR.Max.X+8+w2, row2Y1)
		themeR := image.Rect(standbyR.Max.X+8, row2Y0, standbyR.Max.X+8+w3, row2Y1)

		themeLabel := tr.ThemeLightFull
		if darkOn {
			themeLabel = tr.ThemeDarkFull
		}
		drawButtonBox(r.canvas, awakeR, r.fonts.bold16, awakeLabel, r.keepAwake)
		drawButtonBox(r.canvas, standbyR, r.fonts.bold16, tr.StandbyNow, false)
		drawButtonBox(r.canvas, themeR, r.fonts.bold16, themeLabel, darkOn)

		hits = append(hits,
			HitAction{Rect: awakeR, Type: ActionToggleKeepAwake},
			HitAction{Rect: standbyR, Type: ActionEnterStandby},
			HitAction{Rect: themeR, Type: ActionToggleTheme},
		)
	}

	// Row 3: Power saving summary + Web editor URL
	blInfo := tr.DimNever
	if r.cfg.Device.BacklightTimeoutSec > 0 {
		blInfo = fmt.Sprintf(tr.DimAfterSec, r.cfg.Device.BacklightTimeoutSec)
	}
	if r.cfg.Device.NightModeEnabled {
		blInfo += fmt.Sprintf(tr.NightWindowFmt, r.cfg.Device.NightStartHour, r.cfg.Device.NightEndHour, r.cfg.Device.NightBrightness)
	}
	drawText(r.canvas, r.fonts.small, box.Min.X+12, box.Min.Y+124, colDark, truncateText(r.fonts.small, blInfo, 320))

	webText := tr.WebOff
	if r.cfg.Web.Listen != "" {
		ip := hwStat.IP
		if ip == "" {
			ip = "kindle-ip"
		}
		port := r.cfg.Web.Listen
		if strings.HasPrefix(port, ":") {
			webText = "http://" + ip + port
		} else {
			webText = "http://" + port
		}
	}
	drawRightText(r.canvas, r.fonts.small, box.Max.X-12, box.Min.Y+124, colBlack, truncateText(r.fonts.small, webText, 220))

	r.hits = append(r.hits, hits...)
	return panelH
}

func (r *Renderer) drawBanner(w, y int, msg string, isError bool) {
	box := image.Rect(14, y, w-14, y+38)
	if isError {
		fillRect(r.canvas, box, colBlack)
		drawCenteredText(r.canvas, r.fonts.bold16, box, colWhite, truncateText(r.fonts.bold16, msg, box.Dx()-20))
	} else {
		fillRect(r.canvas, box, colLight)
		drawRect(r.canvas, box, 2, colBlack)
		drawCenteredText(r.canvas, r.fonts.bold16, box, colBlack, truncateText(r.fonts.bold16, msg, box.Dx()-20))
	}
	r.hits = append(r.hits, HitAction{Rect: box, Type: ActionDismissNotice})
}

func (r *Renderer) drawPageContent(w, top, bottom int, now time.Time) {
	pageIdx, ok := r.resolveActivePageLocked()
	if !ok {
		drawCenteredText(r.canvas, r.fonts.regular, image.Rect(14, top, w-14, bottom), colDark, r.tr().AllPagesHidden)
		return
	}
	page := r.cfg.Pages[pageIdx]
	if page.Type == "floorplan" {
		r.drawFloorplanPage(w, top, bottom, page, now)
		return
	}
	cols := page.Columns
	if cols <= 0 {
		cols = 2
	}

	padX := 14
	gapX := 12
	gapY := 12
	cardH := 130

	availH := bottom - top
	rowsPerPage := (availH + gapY) / (cardH + gapY)
	if rowsPerPage < 1 {
		rowsPerPage = 1
	}

	type placedWidget struct {
		w   config.WidgetConfig
		row int
		col int
	}

	// Flow widgets into grid rows.
	var placed []placedWidget
	curRow, curCol := 0, 0
	for _, wd := range page.Widgets {
		span := wd.Span
		if span <= 0 {
			span = 1
		}
		if span > cols {
			span = cols
		}
		if curCol+span > cols {
			curRow++
			curCol = 0
		}
		placed = append(placed, placedWidget{w: wd, row: curRow, col: curCol})
		curCol += span
		if curCol >= cols {
			curRow++
			curCol = 0
		}
	}

	totalRows := curRow
	if curCol > 0 {
		totalRows++
	}
	totalSubPages := (totalRows + rowsPerPage - 1) / rowsPerPage
	if totalSubPages < 1 {
		totalSubPages = 1
	}
	if r.subPage >= totalSubPages {
		r.subPage = totalSubPages - 1
	}

	// Reserve space for sub-page pager if needed.
	if totalSubPages > 1 {
		pagerH := 38
		availH -= pagerH + 6
		rowsPerPage = (availH + gapY) / (cardH + gapY)
		if rowsPerPage < 1 {
			rowsPerPage = 1
		}
		totalSubPages = (totalRows + rowsPerPage - 1) / rowsPerPage
		if r.subPage >= totalSubPages {
			r.subPage = totalSubPages - 1
		}
	}

	startRow := r.subPage * rowsPerPage
	endRow := startRow + rowsPerPage

	colW := (w - 2*padX - (cols-1)*gapX) / cols
	for _, item := range placed {
		if item.row < startRow || item.row >= endRow {
			continue
		}
		relRow := item.row - startRow
		span := item.w.Span
		if span <= 0 {
			span = 1
		}
		if span > cols {
			span = cols
		}
		x0 := padX + item.col*(colW+gapX)
		y0 := top + relRow*(cardH+gapY)
		cw := span*colW + (span-1)*gapX
		rect := image.Rect(x0, y0, x0+cw, y0+cardH)
		r.drawWidgetCard(rect, item.w, now)
	}

	if totalSubPages > 1 {
		tr := r.tr()
		py := bottom - 38
		prevR := image.Rect(padX, py, padX+110, bottom)
		nextR := image.Rect(w-padX-110, py, w-padX, bottom)
		lblR := image.Rect(prevR.Max.X+8, py, nextR.Min.X-8, bottom)

		drawButtonBox(r.canvas, prevR, r.fonts.bold16, tr.PagePrev, false)
		drawCenteredText(r.canvas, r.fonts.regular, lblR, colDark, fmt.Sprintf(tr.PageOf, r.subPage+1, totalSubPages))
		drawButtonBox(r.canvas, nextR, r.fonts.bold16, tr.PageNext, false)

		r.hits = append(r.hits,
			HitAction{Rect: prevR, Type: ActionSubPagePrev},
			HitAction{Rect: nextR, Type: ActionSubPageNext},
		)
	}
}

func (r *Renderer) drawWidgetCard(rect image.Rectangle, wd config.WidgetConfig, now time.Time) {
	switch wd.Type {
	case "switch":
		r.drawSwitchCard(rect, wd)
	case "button":
		r.drawButtonCard(rect, wd, now)
	case "stepper":
		r.drawStepperCard(rect, wd)
	case "sensor":
		r.drawSensorCard(rect, wd, now)
	case "gauge":
		r.drawGaugeCard(rect, wd)
	case "chart":
		r.drawChartCard(rect, wd)
	case "weather":
		r.drawWeatherCard(rect, wd, now)
	case "timer":
		r.drawTimerCard(rect, wd, now)
	case "calendar":
		r.drawCalendarCard(rect, wd, now)
	case "select":
		r.drawSelectCard(rect, wd)
	}
}

func pinPrefixedLabel(wd config.WidgetConfig) string {
	if wd.PinProtected {
		return "[PIN] " + wd.Label
	}
	return wd.Label
}

func (r *Renderer) drawSwitchCard(rect image.Rectangle, wd config.WidgetConfig) {
	tr := r.tr()
	isOn := false
	hasVal := false
	if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		hasVal = true
		val := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
		isOn = strings.EqualFold(val, wd.ValueOn)
	}

	face := drawCardFrame(r.canvas, rect, 10, isOn)
	fg := colBlack
	subCol := colDark
	statusText := tr.SwitchOff
	if !hasVal && wd.StateTopic != "" && !strings.HasPrefix(wd.StateTopic, "local/") {
		statusText = tr.SwitchUnknown
	}
	if isOn {
		fg = colWhite
		subCol = colLight
		statusText = tr.SwitchOn
	}

	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, face.Min.X+28, face.Min.Y+30, 16, icon, isOn)

	textX := face.Min.X + 54
	maxTextW := face.Dx() - 66
	drawText(r.canvas, r.fonts.bold20, textX, face.Min.Y+29, fg,
		truncateText(r.fonts.bold20, pinPrefixedLabel(wd), maxTextW))
	if wd.Subtitle != "" {
		drawText(r.canvas, r.fonts.small, textX, face.Min.Y+48, subCol,
			truncateText(r.fonts.small, wd.Subtitle, maxTextW))
	}

	// Bottom divider + status label on left + physical toggle switch on right
	divY := face.Max.Y - 46
	if isOn {
		drawDashedHLine(r.canvas, face.Min.X+12, face.Max.X-12, divY, 3, 3, colDark)
	} else {
		drawDashedHLine(r.canvas, face.Min.X+12, face.Max.X-12, divY, 3, 3, colSubtleGray)
	}

	drawText(r.canvas, r.fonts.bold16, face.Min.X+14, face.Max.Y-17, fg, statusText)
	swRect := image.Rect(face.Max.X-80, face.Max.Y-38, face.Max.X-14, face.Max.Y-10)
	drawToggleSwitch(r.canvas, swRect, isOn)

	r.hits = append(r.hits, HitAction{Rect: rect, Type: ActionWidgetToggle, Widget: wd})
}

func (r *Renderer) drawButtonCard(rect image.Rectangle, wd config.WidgetConfig, now time.Time) {
	tr := r.tr()
	confirming := r.confirmID == wd.ID && now.Before(r.confirmUntil)
	face := drawCardFrame(r.canvas, rect, 10, confirming)

	fg := colBlack
	subCol := colDark
	if confirming {
		fg = colWhite
		subCol = colLight
	}

	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, face.Min.X+28, face.Min.Y+30, 16, icon, confirming)

	textX := face.Min.X + 54
	maxTextW := face.Dx() - 66
	drawText(r.canvas, r.fonts.bold20, textX, face.Min.Y+29, fg,
		truncateText(r.fonts.bold20, pinPrefixedLabel(wd), maxTextW))

	sub := wd.Subtitle
	if sub == "" && len(wd.MacroActions) > 0 {
		sub = fmt.Sprintf(tr.MacroSub, len(wd.MacroActions))
	}
	if wd.StateTopic != "" {
		if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
			val := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
			if mapped, exists := wd.ValueMap[val]; exists {
				val = mapped
			}
			if val != "" {
				sub = val
			}
		}
	}
	if confirming {
		sub = tr.TapAgainConfirm
	}
	if sub != "" {
		drawText(r.canvas, r.fonts.small, textX, face.Min.Y+48, subCol,
			truncateText(r.fonts.small, sub, maxTextW))
	}

	pill := image.Rect(face.Min.X+14, face.Max.Y-40, face.Max.X-14, face.Max.Y-10)
	if confirming {
		fillRoundedRect(r.canvas, pill, 8, colWhite)
		drawCenteredText(r.canvas, r.fonts.bold16, pill, colBlack, tr.ConfirmAction)
	} else {
		btnTxt := tr.RunBtn
		if len(wd.MacroActions) > 0 {
			btnTxt = tr.RunSceneBtn
		}
		drawRoundedButton(r.canvas, pill, 8, r.fonts.bold16, btnTxt, false)
	}

	r.hits = append(r.hits, HitAction{Rect: rect, Type: ActionWidgetButton, Widget: wd})
}

func (r *Renderer) drawStepperCard(rect image.Rectangle, wd config.WidgetConfig) {
	face := drawCardFrame(r.canvas, rect, 10, false)

	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, face.Min.X+26, face.Min.Y+24, 14, icon, false)
	drawText(r.canvas, r.fonts.bold20, face.Min.X+48, face.Min.Y+30, colBlack,
		truncateText(r.fonts.bold20, pinPrefixedLabel(wd), face.Dx()-160))

	valText := "—"
	var curVal float64
	hasVal := false
	if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		raw := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			curVal = f
			hasVal = true
			valText = formatNumber(f, wd.EffectivePrecision()) + suffixUnit(wd.Unit)
		} else if raw != "" {
			valText = raw + suffixUnit(wd.Unit)
		}
	}

	// Range hint in top right
	drawRightText(r.canvas, r.fonts.small, face.Max.X-14, face.Min.Y+28, colDark,
		fmt.Sprintf("%s–%s%s", formatNumber(wd.Min, wd.EffectivePrecision()), formatNumber(wd.Max, wd.EffectivePrecision()), suffixUnit(wd.Unit)))

	btnW := 74
	btnH := 66
	by0 := face.Max.Y - btnH - 10
	minusR := image.Rect(face.Min.X+12, by0, face.Min.X+12+btnW, by0+btnH)
	plusR := image.Rect(face.Max.X-12-btnW, by0, face.Max.X-12, by0+btnH)

	drawRoundedButton(r.canvas, minusR, 10, r.fonts.bold34, "−", false)
	drawRoundedButton(r.canvas, plusR, 10, r.fonts.bold34, "+", false)

	// Framed central display with mini progress bar
	valBox := image.Rect(minusR.Max.X+10, by0+2, plusR.Min.X-10, by0+btnH-2)
	fillRoundedRect(r.canvas, valBox, 8, colBgCanvas)
	drawRoundedRect(r.canvas, valBox, 8, 1, colMidGray)

	textArea := image.Rect(valBox.Min.X, valBox.Min.Y, valBox.Max.X, valBox.Max.Y-12)
	drawCenteredText(r.canvas, r.fonts.bold28, textArea, colBlack, truncateText(r.fonts.bold28, valText, valBox.Dx()-8))

	// Progress track inside valBox bottom
	bar := image.Rect(valBox.Min.X+12, valBox.Max.Y-11, valBox.Max.X-12, valBox.Max.Y-5)
	if bar.Dx() > 20 {
		fillRoundedRect(r.canvas, bar, 3, colWhite)
		drawRoundedRect(r.canvas, bar, 3, 1, colBlack)
		if hasVal && wd.Max > wd.Min {
			frac := (curVal - wd.Min) / (wd.Max - wd.Min)
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			fw := int(math.Round(frac * float64(bar.Dx())))
			if fw > 0 {
				fillRoundedRect(r.canvas, image.Rect(bar.Min.X, bar.Min.Y, bar.Min.X+fw, bar.Max.Y), 3, colBlack)
			}
		}
	}

	r.hits = append(r.hits,
		HitAction{Rect: minusR, Type: ActionWidgetStep, Widget: wd, StepDelta: -wd.Step},
		HitAction{Rect: plusR, Type: ActionWidgetStep, Widget: wd, StepDelta: +wd.Step},
	)
}

func (r *Renderer) drawSensorCard(rect image.Rectangle, wd config.WidgetConfig, now time.Time) {
	face := drawCardFrame(r.canvas, rect, 10, false)
	alerting, _ := r.isWidgetAlerting(wd)

	// Header strip with icon badge (inverted when in alert state)
	hdr := image.Rect(face.Min.X+3, face.Min.Y+3, face.Max.X-3, face.Min.Y+38)
	hdrBg := colLight
	hdrFg := colBlack
	if alerting {
		hdrBg = colBlack
		hdrFg = colWhite
	}
	fillRoundedRect(r.canvas, hdr, 7, hdrBg)
	fillRect(r.canvas, image.Rect(hdr.Min.X, hdr.Max.Y-1, hdr.Max.X, hdr.Max.Y+1), colBlack)

	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, hdr.Min.X+18, hdr.Min.Y+17, 13, icon, alerting)
	lblText := wd.Label
	if alerting {
		lblText = "! " + lblText
	}
	drawText(r.canvas, r.fonts.bold16, hdr.Min.X+37, hdr.Min.Y+23, hdrFg,
		truncateText(r.fonts.bold16, lblText, hdr.Dx()-64))
	if trend := r.sensorTrend(wd); trend != "" {
		drawRightText(r.canvas, r.fonts.bold16, hdr.Max.X-10, hdr.Min.Y+23, hdrFg, trend)
	}

	tr := r.tr()
	valText := "—"
	ageText := tr.Waiting
	stale := false

	if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		raw := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
		valText = formatWidgetValue(raw, wd)

		age := now.Sub(tv.UpdatedAt)
		if wd.StaleSeconds > 0 && age > time.Duration(wd.StaleSeconds)*time.Second {
			stale = true
		}
		ageText = "● " + formatAgeLang(age, tr)
		if stale {
			ageText = "⚠ " + formatAgeLang(age, tr)
		}
	}

	mainRect := image.Rect(face.Min.X+10, face.Min.Y+42, face.Max.X-10, face.Max.Y-32)
	drawCenteredText(r.canvas, r.fonts.bold34, mainRect, colBlack, truncateText(r.fonts.bold34, valText, mainRect.Dx()))

	if wd.SecondaryTopic != "" {
		secText := "—"
		if tv2, ok := r.mq.GetTopic(wd.BrokerID, wd.SecondaryTopic); ok {
			raw2 := mqtt.ExtractValue(tv2.Payload, wd.SecondaryJSONPath)
			secText = formatSecondaryValue(raw2, wd)
		}
		pillW := minInt(face.Dx()/2, font.MeasureString(r.fonts.bold16, secText).Ceil()+18)
		secPill := image.Rect(face.Min.X+10, face.Max.Y-28, face.Min.X+10+pillW, face.Max.Y-6)
		fillRoundedRect(r.canvas, secPill, 6, colLight)
		drawRoundedRect(r.canvas, secPill, 6, 1, colBlack)
		drawCenteredText(r.canvas, r.fonts.bold16, secPill, colBlack, truncateText(r.fonts.bold16, secText, pillW-8))
		drawRightText(r.canvas, r.fonts.small, face.Max.X-10, face.Max.Y-11, colDark, ageText)
	} else {
		drawCenteredText(r.canvas, r.fonts.small,
			image.Rect(face.Min.X+10, face.Max.Y-26, face.Max.X-10, face.Max.Y-5),
			colDark, ageText)
	}
	r.hits = append(r.hits, HitAction{Rect: rect, Type: ActionOpenSensorDetail, Widget: wd})
}

func (r *Renderer) drawGaugeCard(rect image.Rectangle, wd config.WidgetConfig) {
	face := drawCardFrame(r.canvas, rect, 10, false)
	alerting, _ := r.isWidgetAlerting(wd)

	hdr := image.Rect(face.Min.X+3, face.Min.Y+3, face.Max.X-3, face.Min.Y+34)
	hdrBg := colLight
	hdrFg := colBlack
	if alerting {
		hdrBg = colBlack
		hdrFg = colWhite
	}
	fillRoundedRect(r.canvas, hdr, 7, hdrBg)
	fillRect(r.canvas, image.Rect(hdr.Min.X, hdr.Max.Y-1, hdr.Max.X, hdr.Max.Y+1), colBlack)

	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, hdr.Min.X+17, hdr.Min.Y+15, 12, icon, alerting)
	lblText := wd.Label
	if alerting {
		lblText = "! " + lblText
	}
	drawText(r.canvas, r.fonts.bold16, hdr.Min.X+35, hdr.Min.Y+21, hdrFg,
		truncateText(r.fonts.bold16, lblText, hdr.Dx()-42))

	minV, maxV := wd.Min, wd.Max
	if maxV <= minV {
		maxV = minV + 100
	}

	var curVal float64
	hasVal := false
	valText := "—"
	if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		raw := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			curVal = f
			hasVal = true
			valText = formatNumber(f, wd.EffectivePrecision()) + suffixUnit(wd.Unit)
		} else if raw != "" {
			valText = raw + suffixUnit(wd.Unit)
		}
	}

	cx := (face.Min.X + face.Max.X) / 2
	cy := face.Max.Y - 26
	radius := face.Dx()/2 - 30
	if maxR := face.Dy() - 68; radius > maxR {
		radius = maxR
	}
	if radius < 24 {
		radius = 24
	}

	// Scale tick marks at 0%, 25%, 50%, 75%, 100%
	for i := 0; i <= 4; i++ {
		ang := math.Pi * (1.0 - float64(i)*0.25)
		x1 := cx + int(math.Round(float64(radius+7)*math.Cos(ang)))
		y1 := cy - int(math.Round(float64(radius+7)*math.Sin(ang)))
		x2 := cx + int(math.Round(float64(radius+12)*math.Cos(ang)))
		y2 := cy - int(math.Round(float64(radius+12)*math.Sin(ang)))
		drawThickLine(r.canvas, x1, y1, x2, y2, 2, colBlack)
	}

	// Background semicircular track (pi -> 0)
	drawArc(r.canvas, cx, cy, radius, 12, math.Pi, 0, colLight)
	drawArc(r.canvas, cx, cy, radius+6, 2, math.Pi, 0, colBlack)
	drawArc(r.canvas, cx, cy, radius-6, 2, math.Pi, 0, colBlack)

	if hasVal {
		frac := (curVal - minV) / (maxV - minV)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		endAngle := math.Pi * (1.0 - frac)
		drawArc(r.canvas, cx, cy, radius, 12, math.Pi, endAngle, colBlack)
		// Tapered needle + bullseye pivot
		nx := cx + int(math.Round(float64(radius-6)*math.Cos(endAngle)))
		ny := cy - int(math.Round(float64(radius-6)*math.Sin(endAngle)))
		drawThickLine(r.canvas, cx, cy, nx, ny, 3, colBlack)
		fillCircle(r.canvas, cx, cy, 6, colBlack)
		fillCircle(r.canvas, cx, cy, 2, colWhite)
	}

	// Framed digital value badge at bottom center
	valBox := image.Rect(cx-54, cy-20, cx+54, cy+4)
	fillRoundedRect(r.canvas, valBox, 5, colWhite)
	drawRoundedRect(r.canvas, valBox, 5, 1, colBlack)
	drawCenteredText(r.canvas, r.fonts.bold16, valBox, colBlack, truncateText(r.fonts.bold16, valText, valBox.Dx()-6))

	drawText(r.canvas, r.fonts.small, face.Min.X+10, face.Max.Y-7, colDark, formatNumber(minV, -1))
	drawRightText(r.canvas, r.fonts.small, face.Max.X-10, face.Max.Y-7, colDark, formatNumber(maxV, -1))
	r.hits = append(r.hits, HitAction{Rect: rect, Type: ActionOpenSensorDetail, Widget: wd})
}

func (r *Renderer) drawChartCard(rect image.Rectangle, wd config.WidgetConfig) {
	face := drawCardFrame(r.canvas, rect, 10, false)
	alerting, _ := r.isWidgetAlerting(wd)

	hdr := image.Rect(face.Min.X+3, face.Min.Y+3, face.Max.X-3, face.Min.Y+36)
	hdrBg := colLight
	hdrFg := colBlack
	if alerting {
		hdrBg = colBlack
		hdrFg = colWhite
	}
	fillRoundedRect(r.canvas, hdr, 7, hdrBg)
	fillRect(r.canvas, image.Rect(hdr.Min.X, hdr.Max.Y-1, hdr.Max.X, hdr.Max.Y+1), colBlack)

	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, hdr.Min.X+18, hdr.Min.Y+16, 12, icon, alerting)
	lblText := wd.Label
	if alerting {
		lblText = "! " + lblText
	}
	drawText(r.canvas, r.fonts.bold16, hdr.Min.X+36, hdr.Min.Y+22, hdrFg,
		truncateText(r.fonts.bold16, lblText, face.Dx()/2))

	pts := r.mq.GetHistory(wd.BrokerID, wd.StateTopic, wd.StateJSONPath, 60)
	valText := "—"
	if len(pts) > 0 {
		valText = formatNumber(pts[len(pts)-1].Value, wd.EffectivePrecision()) + suffixUnit(wd.Unit)
	} else if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		raw := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
		valText = formatWidgetValue(raw, wd)
	}

	// Highlighted live reading pill on right of header
	valW := font.MeasureString(r.fonts.bold16, valText).Ceil() + 16
	valPill := image.Rect(hdr.Max.X-valW-6, hdr.Min.Y+5, hdr.Max.X-6, hdr.Max.Y-5)
	if alerting {
		fillRoundedRect(r.canvas, valPill, 6, colWhite)
		drawCenteredText(r.canvas, r.fonts.bold16, valPill, colBlack, valText)
	} else {
		fillRoundedRect(r.canvas, valPill, 6, colBlack)
		drawCenteredText(r.canvas, r.fonts.bold16, valPill, colWhite, valText)
	}

	plot := image.Rect(face.Min.X+12, face.Min.Y+42, face.Max.X-12, face.Max.Y-22)
	fillRect(r.canvas, plot, colWhite)
	drawRect(r.canvas, plot, 1, colBlack)

	// Horizontal dashed reference grid lines (25%, 50%, 75%)
	for i := 1; i <= 3; i++ {
		gy := plot.Min.Y + (plot.Dy()*i)/4
		drawDashedHLine(r.canvas, plot.Min.X+2, plot.Max.X-2, gy, 4, 4, colSubtleGray)
	}

	if len(pts) == 0 {
		drawCenteredText(r.canvas, r.fonts.small, plot, colDark, r.tr().WaitingChart)
		return
	}

	minV, maxV := pts[0].Value, pts[0].Value
	for _, p := range pts[1:] {
		if p.Value < minV {
			minV = p.Value
		}
		if p.Value > maxV {
			maxV = p.Value
		}
	}
	if wd.Max > wd.Min {
		minV, maxV = wd.Min, wd.Max
	} else if math.Abs(maxV-minV) < 1e-6 {
		minV -= 1
		maxV += 1
	}

	pw := plot.Dx() - 8
	ph := plot.Dy() - 8
	coords := make([]image.Point, len(pts))
	for i, p := range pts {
		x := plot.Min.X + 4
		if len(pts) > 1 {
			x = plot.Min.X + 4 + i*pw/(len(pts)-1)
		}
		frac := (p.Value - minV) / (maxV - minV)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		y := plot.Max.Y - 4 - int(math.Round(frac*float64(ph)))
		coords[i] = image.Pt(x, y)
	}

	if len(coords) == 1 {
		fillCircle(r.canvas, coords[0].X, coords[0].Y, 5, colBlack)
		fillCircle(r.canvas, coords[0].X, coords[0].Y, 2, colWhite)
	} else {
		for i := 0; i < len(coords)-1; i++ {
			drawDitheredVerticalArea(r.canvas, coords[i], coords[i+1], plot.Max.Y-2)
		}
		for i := 0; i < len(coords)-1; i++ {
			drawThickLine(r.canvas, coords[i].X, coords[i].Y, coords[i+1].X, coords[i+1].Y, 3, colBlack)
		}
		for i := 0; i < len(coords)-1; i++ {
			fillCircle(r.canvas, coords[i].X, coords[i].Y, 2, colBlack)
		}
		last := coords[len(coords)-1]
		fillCircle(r.canvas, last.X, last.Y, 5, colBlack)
		fillCircle(r.canvas, last.X, last.Y, 2, colWhite)
	}

	tr := r.tr()
	drawText(r.canvas, r.fonts.small, plot.Min.X, face.Max.Y-6, colDark,
		fmt.Sprintf(tr.MinLabel, formatNumber(minV, wd.EffectivePrecision()), suffixUnit(wd.Unit)))
	drawRightText(r.canvas, r.fonts.small, plot.Max.X, face.Max.Y-6, colDark,
		fmt.Sprintf(tr.MaxLabel, formatNumber(maxV, wd.EffectivePrecision()), suffixUnit(wd.Unit)))
	r.hits = append(r.hits, HitAction{Rect: rect, Type: ActionOpenSensorDetail, Widget: wd})
}

func (r *Renderer) drawFloorplanPage(w, top, bottom int, page config.PageConfig, now time.Time) {
	tr := r.tr()
	padX := 14
	outer := image.Rect(padX, top, w-padX, bottom)
	area := drawCardFrame(r.canvas, outer, 10, false)

	fp := page.Floorplan
	if fp == nil || (len(fp.Shapes) == 0 && len(fp.Pins) == 0) {
		drawCenteredText(r.canvas, r.fonts.regular, area, colDark, tr.EmptyFloorplan)
		return
	}

	mapX := func(nx int) int {
		return area.Min.X + (nx*area.Dx())/1000
	}
	mapY := func(ny int) int {
		return area.Min.Y + (ny*area.Dy())/1000
	}

	// 1. Draw rooms with subtle e-ink floor stipple texture + architectural walls + room tag pill.
	for _, s := range fp.Shapes {
		switch s.Kind {
		case "room":
			rx1, ry1 := mapX(s.X), mapY(s.Y)
			rx2, ry2 := mapX(s.X+s.W), mapY(s.Y+s.H)
			roomR := image.Rect(minInt(rx1, rx2), minInt(ry1, ry2), maxInt(rx1, rx2), maxInt(ry1, ry2)).Intersect(area)
			fillDitheredRect(r.canvas, roomR, 3, colMidGray, colWhite)
			drawRect(r.canvas, roomR, 4, colBlack)
			if s.Label != "" {
				lbl := truncateText(r.fonts.bold16, s.Label, roomR.Dx()-20)
				tw := font.MeasureString(r.fonts.bold16, lbl).Ceil() + 14
				tagR := image.Rect(roomR.Min.X+8, roomR.Min.Y+8, minInt(roomR.Max.X-6, roomR.Min.X+8+tw), roomR.Min.Y+30)
				fillRoundedRect(r.canvas, tagR, 4, colWhite)
				drawRoundedRect(r.canvas, tagR, 4, 1, colBlack)
				drawCenteredText(r.canvas, r.fonts.bold16, tagR, colBlack, lbl)
			}
		}
	}

	// 2. Draw walls, doors, and text annotations.
	for _, s := range fp.Shapes {
		switch s.Kind {
		case "wall":
			drawThickLine(r.canvas, mapX(s.X), mapY(s.Y), mapX(s.X2), mapY(s.Y2), 5, colBlack)
		case "door":
			x1, y1, x2, y2 := mapX(s.X), mapY(s.Y), mapX(s.X2), mapY(s.Y2)
			drawThickLine(r.canvas, x1, y1, x2, y2, 8, colWhite)
			drawThickLine(r.canvas, x1, y1, x2, y2, 2, colDark)
			fillCircle(r.canvas, x1, y1, 3, colBlack)
			fillCircle(r.canvas, x2, y2, 3, colBlack)
		case "text":
			if s.Label != "" {
				drawText(r.canvas, r.fonts.bold16, mapX(s.X), mapY(s.Y)+14, colBlack, s.Label)
			}
		}
	}

	// 3. Draw placed IoT pins as elevated rounded badges with icons.
	for _, pin := range fp.Pins {
		cx := mapX(pin.X)
		cy := mapY(pin.Y)
		wd := pin.Widget

		badgeW, badgeH := 136, 52
		bx := clampInt(cx-badgeW/2, area.Min.X+6, area.Max.X-badgeW-6)
		by := clampInt(cy-badgeH/2, area.Min.Y+6, area.Max.Y-badgeH-6)
		badge := image.Rect(bx, by, bx+badgeW, by+badgeH)
		icon := resolveWidgetIcon(wd)

		if wd.Type == "switch" || wd.Type == "button" {
			isOn := false
			confirming := wd.Type == "button" && r.confirmID == wd.ID && now.Before(r.confirmUntil)
			if wd.Type == "switch" {
				if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
					isOn = strings.EqualFold(mqtt.ExtractValue(tv.Payload, wd.StateJSONPath), wd.ValueOn)
				}
			}
			active := isOn || confirming
			face := drawCardFrame(r.canvas, badge, 8, active)
			fg := colBlack
			status := tr.SwitchOffShort
			if wd.Type == "button" {
				status = tr.RunShort
			}
			if active {
				fg = colWhite
				if isOn {
					status = tr.SwitchOnShort
				} else {
					status = tr.ConfirmShort
				}
			}
			drawIconBadge(r.canvas, face.Min.X+19, (face.Min.Y+face.Max.Y)/2, 12, icon, active)
			drawText(r.canvas, r.fonts.small, face.Min.X+36, face.Min.Y+18, fg, truncateText(r.fonts.small, wd.Label, face.Dx()-42))
			drawText(r.canvas, r.fonts.bold16, face.Min.X+36, face.Max.Y-8, fg, status)

			act := ActionWidgetButton
			if wd.Type == "switch" {
				act = ActionWidgetToggle
			}
			r.hits = append(r.hits, HitAction{Rect: badge, Type: act, Widget: wd})
		} else {
			valText := "—"
			if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
				raw := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
				valText = formatWidgetValue(raw, wd)
			}
			alerting, _ := r.isWidgetAlerting(wd)
			face := drawCardFrame(r.canvas, badge, 8, alerting)
			fgLbl := colDark
			fgVal := colBlack
			if alerting {
				fgLbl = colLight
				fgVal = colWhite
				valText = "! " + valText
			}
			drawIconBadge(r.canvas, face.Min.X+19, (face.Min.Y+face.Max.Y)/2, 12, icon, alerting)
			drawText(r.canvas, r.fonts.small, face.Min.X+36, face.Min.Y+18, fgLbl, truncateText(r.fonts.small, wd.Label, face.Dx()-42))
			drawText(r.canvas, r.fonts.bold16, face.Min.X+36, face.Max.Y-8, fgVal, truncateText(r.fonts.bold16, valText, face.Dx()-42))
		}
	}
}

func (r *Renderer) drawSelectCard(rect image.Rectangle, wd config.WidgetConfig) {
	face := drawCardFrame(r.canvas, rect, 10, false)

	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, face.Min.X+26, face.Min.Y+24, 14, icon, false)
	drawText(r.canvas, r.fonts.bold20, face.Min.X+48, face.Min.Y+30, colBlack,
		truncateText(r.fonts.bold20, pinPrefixedLabel(wd), face.Dx()-60))

	curVal := ""
	if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		curVal = mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
	}

	n := len(wd.Options)
	if n == 0 {
		return
	}
	padX := 12
	gap := 8
	optW := (face.Dx() - 2*padX - (n-1)*gap) / n
	by0 := face.Min.Y + 48
	by1 := face.Max.Y - 10

	for i, opt := range wd.Options {
		x0 := face.Min.X + padX + i*(optW+gap)
		optR := image.Rect(x0, by0, x0+optW, by1)
		active := curVal != "" && strings.EqualFold(curVal, opt.Payload)
		lbl := opt.Label
		if active {
			lbl = "● " + lbl
		}
		drawRoundedButton(r.canvas, optR, 8, r.fonts.bold16, truncateText(r.fonts.bold16, lbl, optW-10), active)
		r.hits = append(r.hits, HitAction{
			Rect:    optR,
			Type:    ActionWidgetSelect,
			Widget:  wd,
			Payload: opt.Payload,
		})
	}
}

func (r *Renderer) drawBottomTabs(w, h, bottomH int) {
	y0 := h - bottomH
	fillRect(r.canvas, image.Rect(0, y0, w, h), colWhite)
	fillRect(r.canvas, image.Rect(0, y0, w, y0+2), colBlack)

	vis := r.visiblePageIndices()
	n := len(vis)
	if n == 0 {
		return
	}
	padX := 14
	gap := 10
	tabW := (w - 2*padX - (n-1)*gap) / n
	for pos, pageIdx := range vis {
		p := r.cfg.Pages[pageIdx]
		x0 := padX + pos*(tabW+gap)
		tabR := image.Rect(x0, y0+9, x0+tabW, h-9)
		active := pageIdx == r.activePage
		lbl := p.Title
		if active {
			lbl = "● " + lbl
		}
		drawRoundedButton(r.canvas, tabR, 8, r.fonts.bold20, truncateText(r.fonts.bold20, lbl, tabW-14), active)
		r.hits = append(r.hits, HitAction{
			Rect:    tabR,
			Type:    ActionSelectPage,
			PageIdx: pageIdx,
		})
	}
}

// HandleTap processes a screen touch/click at (x, y) and returns true if a redraw is needed.
func (r *Renderer) HandleTap(x, y int) bool {
	pt := image.Pt(x, y)

	r.mu.Lock()
	var matched *HitAction
	for i := len(r.hits) - 1; i >= 0; i-- {
		if pt.In(r.hits[i].Rect) {
			h := r.hits[i]
			matched = &h
			break
		}
	}
	if matched == nil {
		r.mu.Unlock()
		return false
	}

	now := time.Now()
	if r.requiresPinLocked(*matched, now) {
		actCopy := *matched
		r.pendingPinAction = &actCopy
		r.pinModalOpen = true
		r.pinInput = ""
		r.pinError = false
		r.mu.Unlock()
		return true
	}

	return r.dispatchActionLocked(*matched, now)
}

func (r *Renderer) requiresPinLocked(h HitAction, now time.Time) bool {
	pin := strings.TrimSpace(r.cfg.Device.PinCode)
	if pin == "" || now.Before(r.pinUnlockedUntil) {
		return false
	}
	if h.Type == ActionWakeStandby && r.cfg.Device.PinLockStandby {
		return true
	}
	switch h.Type {
	case ActionWidgetToggle, ActionWidgetButton, ActionWidgetStep, ActionWidgetSelect, ActionTimerStart, ActionTimerAdd, ActionTimerCancel:
		return h.Widget.PinProtected
	}
	return false
}

func (r *Renderer) dispatchActionLocked(matched HitAction, now time.Time) bool {
	switch matched.Type {
	case ActionSelectPage:
		if matched.PageIdx >= 0 && matched.PageIdx < len(r.cfg.Pages) && !r.cfg.Pages[matched.PageIdx].Hidden {
			r.activePage = matched.PageIdx
		} else {
			r.resolveActivePageLocked()
		}
		r.subPage = 0
		r.confirmID = ""
		r.detailWidget = nil
		r.weatherDetailOpen = false
		r.calendarDetailOpen = false
		r.mu.Unlock()
		return true

	case ActionSubPagePrev:
		if r.subPage > 0 {
			r.subPage--
		}
		r.mu.Unlock()
		return true

	case ActionSubPageNext:
		r.subPage++
		r.mu.Unlock()
		return true

	case ActionToggleSettings:
		r.showSettings = !r.showSettings
		r.mu.Unlock()
		return true

	case ActionEnterStandby:
		r.standbyMode = true
		r.showSettings = false
		r.detailWidget = nil
		r.weatherDetailOpen = false
		r.calendarDetailOpen = false
		r.mu.Unlock()
		if r.ctrl.OnFullRefresh != nil {
			r.ctrl.OnFullRefresh()
		}
		return true

	case ActionWakeStandby:
		r.standbyMode = false
		r.mu.Unlock()
		if r.ctrl.OnFullRefresh != nil {
			r.ctrl.OnFullRefresh()
		}
		return true

	case ActionOpenSensorDetail:
		wdCopy := matched.Widget
		r.detailWidget = &wdCopy
		r.weatherDetailOpen = false
		r.calendarDetailOpen = false
		r.showSettings = false
		r.mu.Unlock()
		return true

	case ActionOpenWeatherDetail:
		r.weatherDetailOpen = true
		r.calendarDetailOpen = false
		r.detailWidget = nil
		r.showSettings = false
		r.mu.Unlock()
		return true

	case ActionOpenCalendarDetail:
		r.calendarDetailOpen = true
		r.weatherDetailOpen = false
		r.detailWidget = nil
		r.showSettings = false
		r.mu.Unlock()
		return true

	case ActionCloseDetail:
		r.detailWidget = nil
		r.weatherDetailOpen = false
		r.calendarDetailOpen = false
		r.mu.Unlock()
		return true

	case ActionToggleTheme:
		nextDark := !r.isDarkModeLocked(now)
		r.darkOverride = &nextDark
		r.mu.Unlock()
		return true

	case ActionTimerStart:
		mins := matched.IntVal
		if mins <= 0 {
			mins = 5
		}
		dur := time.Duration(mins) * time.Minute
		r.activeTimers[matched.Widget.ID] = activeTimer{
			Widget:    matched.Widget,
			StartedAt: now,
			EndsAt:    now.Add(dur),
			Duration:  dur,
		}
		r.mu.Unlock()
		return true

	case ActionTimerAdd:
		if t, ok := r.activeTimers[matched.Widget.ID]; ok {
			add := time.Duration(matched.IntVal) * time.Minute
			t.EndsAt = t.EndsAt.Add(add)
			t.Duration += add
			r.activeTimers[matched.Widget.ID] = t
		}
		r.mu.Unlock()
		return true

	case ActionTimerCancel:
		delete(r.activeTimers, matched.Widget.ID)
		r.mu.Unlock()
		return true

	case ActionLockPin:
		r.pinUnlockedUntil = time.Time{}
		r.pinModalOpen = true
		r.pinInput = ""
		r.pinError = false
		r.pendingPinAction = nil
		r.showSettings = false
		r.mu.Unlock()
		return true

	case ActionPinKey:
		r.pinError = false
		r.pinInput += matched.Payload
		targetPin := strings.TrimSpace(r.cfg.Device.PinCode)
		if r.pinInput == targetPin {
			r.pinModalOpen = false
			r.pinInput = ""
			r.pinUnlockedUntil = now.Add(60 * time.Second)
			pending := r.pendingPinAction
			r.pendingPinAction = nil
			if pending != nil {
				return r.dispatchActionLocked(*pending, now)
			}
			r.mu.Unlock()
			return true
		}
		if len(r.pinInput) >= len(targetPin) && len(targetPin) > 0 {
			r.pinError = true
			r.pinInput = ""
		}
		r.mu.Unlock()
		return true

	case ActionPinBackspace:
		r.pinError = false
		if len(r.pinInput) > 0 {
			r.pinInput = r.pinInput[:len(r.pinInput)-1]
		}
		r.mu.Unlock()
		return true

	case ActionPinCancel:
		r.pinModalOpen = false
		r.pinInput = ""
		r.pinError = false
		r.pendingPinAction = nil
		r.mu.Unlock()
		return true

	case ActionDismissNotice:
		r.noticeText = ""
		r.lastError = ""
		r.mu.Unlock()
		return true

	case ActionToggleKeepAwake:
		r.keepAwake = !r.keepAwake
		r.mu.Unlock()
		if r.ctrl.OnToggleAwake != nil {
			r.ctrl.OnToggleAwake()
		}
		return true

	case ActionBrightnessDelta:
		delta := matched.IntVal
		r.mu.Unlock()
		if r.ctrl.OnBrightness != nil {
			r.ctrl.OnBrightness(delta)
		}
		return true

	case ActionFullRefresh:
		r.mu.Unlock()
		if r.ctrl.OnFullRefresh != nil {
			r.ctrl.OnFullRefresh()
		}
		return true

	case ActionQuit:
		r.mu.Unlock()
		if r.ctrl.OnQuit != nil {
			r.ctrl.OnQuit()
		}
		return true

	case ActionWidgetToggle:
		wd := matched.Widget
		r.mu.Unlock()
		r.executeSwitchToggle(wd)
		return true

	case ActionWidgetButton:
		wd := matched.Widget
		if wd.Confirm && !(r.confirmID == wd.ID && now.Before(r.confirmUntil)) {
			r.confirmID = wd.ID
			r.confirmUntil = now.Add(5 * time.Second)
			r.mu.Unlock()
			return true
		}
		r.confirmID = ""
		r.mu.Unlock()
		if len(wd.MacroActions) > 0 {
			r.executeMacro(wd)
		}
		if strings.TrimSpace(wd.CommandTopic) != "" {
			_ = r.publishWithFeedback(wd.BrokerID, wd.CommandTopic, wd.QoS, wd.Retain, wd.Payload)
		}
		return true

	case ActionWidgetStep:
		wd := matched.Widget
		delta := matched.StepDelta
		r.mu.Unlock()
		r.executeStepper(wd, delta)
		return true

	case ActionWidgetSelect:
		wd := matched.Widget
		payload := matched.Payload
		r.mu.Unlock()
		if err := r.publishWithFeedback(wd.BrokerID, wd.CommandTopic, wd.QoS, wd.Retain, payload); err == nil && wd.Optimistic && wd.StateTopic != "" {
			r.mq.SetLocalValue(wd.BrokerID, wd.StateTopic, payload)
		}
		return true
	}

	r.mu.Unlock()
	return false
}

func (r *Renderer) executeSwitchToggle(wd config.WidgetConfig) {
	isOn := false
	if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		val := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
		isOn = strings.EqualFold(val, wd.ValueOn)
	}
	payload := wd.PayloadOn
	nextState := wd.ValueOn
	if isOn {
		payload = wd.PayloadOff
		nextState = wd.ValueOff
	}
	if strings.TrimSpace(wd.CommandTopic) == "" {
		// Local-only / Apple Home switch without MQTT command topic
		if wd.StateTopic != "" {
			r.mq.SetLocalValue(wd.BrokerID, wd.StateTopic, nextState)
		}
		if r.ctrl.OnWidgetChanged != nil {
			r.ctrl.OnWidgetChanged()
		}
		return
	}
	if (wd.Optimistic || wd.HomeKit) && wd.StateTopic != "" && wd.StateJSONPath == "" {
		r.mq.SetLocalValue(wd.BrokerID, wd.StateTopic, nextState)
		if r.ctrl.OnWidgetChanged != nil {
			r.ctrl.OnWidgetChanged()
		}
	}
	if err := r.publishWithFeedback(wd.BrokerID, wd.CommandTopic, wd.QoS, wd.Retain, payload); err == nil {
		if !(wd.Optimistic || wd.HomeKit) && wd.StateTopic != "" && wd.StateJSONPath == "" {
			r.mq.SetLocalValue(wd.BrokerID, wd.StateTopic, nextState)
		}
		if r.ctrl.OnWidgetChanged != nil {
			r.ctrl.OnWidgetChanged()
		}
	}
}

func (r *Renderer) executeStepper(wd config.WidgetConfig, delta float64) {
	cur := wd.Min
	if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		raw := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			cur = f
		}
	}
	next := cur + delta
	if next < wd.Min {
		next = wd.Min
	}
	if next > wd.Max {
		next = wd.Max
	}
	payload := mqtt.FormatCommand(wd.CommandTemplate, next, wd.EffectivePrecision())
	if err := r.publishWithFeedback(wd.BrokerID, wd.CommandTopic, wd.QoS, wd.Retain, payload); err == nil {
		if wd.Optimistic && wd.StateTopic != "" && wd.StateJSONPath == "" {
			r.mq.SetLocalValue(wd.BrokerID, wd.StateTopic, formatNumber(next, wd.EffectivePrecision()))
		}
	}
}

func (r *Renderer) publishWithFeedback(brokerID, topic string, qos byte, retain bool, payload string) error {
	err := r.mq.Publish(brokerID, topic, qos, retain, payload)
	if err != nil {
		r.mu.Lock()
		tr := stringsForLang(config.NormalizeLanguage(r.cfg.Device.Language))
		r.lastError = tr.SendErrPrefix + err.Error()
		r.lastErrUntil = time.Now().Add(4 * time.Second)
		r.mu.Unlock()
	}
	return err
}

// Drawing helpers

func fillRect(img *image.Gray, rect image.Rectangle, c color.Gray) {
	draw.Draw(img, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

func drawRect(img *image.Gray, r image.Rectangle, thickness int, c color.Gray) {
	fillRect(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+thickness), c)
	fillRect(img, image.Rect(r.Min.X, r.Max.Y-thickness, r.Max.X, r.Max.Y), c)
	fillRect(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+thickness, r.Max.Y), c)
	fillRect(img, image.Rect(r.Max.X-thickness, r.Min.Y, r.Max.X, r.Max.Y), c)
}

func drawButtonBox(img *image.Gray, r image.Rectangle, face font.Face, text string, inverted bool) {
	if inverted {
		fillRect(img, r, colBlack)
		drawCenteredText(img, face, r, colWhite, text)
	} else {
		fillRect(img, r, colWhite)
		drawRect(img, r, 2, colBlack)
		drawCenteredText(img, face, r, colBlack, text)
	}
}

func drawText(img *image.Gray, face font.Face, x, baselineY int, c color.Gray, text string) {
	d := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{C: c},
		Face: face,
		Dot:  fixed.P(x, baselineY),
	}
	d.DrawString(text)
}

func drawRightText(img *image.Gray, face font.Face, rightX, baselineY int, c color.Gray, text string) {
	w := font.MeasureString(face, text).Ceil()
	drawText(img, face, rightX-w, baselineY, c, text)
}

func drawCenteredText(img *image.Gray, face font.Face, r image.Rectangle, c color.Gray, text string) {
	m := face.Metrics()
	tw := font.MeasureString(face, text).Ceil()
	ascent := m.Ascent.Ceil()
	descent := m.Descent.Ceil()
	x := r.Min.X + (r.Dx()-tw)/2
	y := r.Min.Y + (r.Dy()+ascent-descent)/2
	drawText(img, face, x, y, c, text)
}

func truncateText(face font.Face, text string, maxWidth int) string {
	if maxWidth <= 10 || font.MeasureString(face, text).Ceil() <= maxWidth {
		return text
	}
	runes := []rune(text)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		candidate := string(runes) + "…"
		if font.MeasureString(face, candidate).Ceil() <= maxWidth {
			return candidate
		}
	}
	return "…"
}

func formatNumber(v float64, precision int) string {
	if precision >= 0 {
		if precision > 6 {
			precision = 6
		}
		return strconv.FormatFloat(v, 'f', precision, 64)
	}
	if math.Abs(v-math.Round(v)) < 1e-9 {
		return strconv.FormatInt(int64(math.Round(v)), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func formatWidgetValue(raw string, wd config.WidgetConfig) string {
	if mapped, exists := wd.ValueMap[raw]; exists {
		return mapped
	}
	if wd.Format != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return fmt.Sprintf(wd.Format, f) + suffixUnit(wd.Unit)
		}
		if raw != "" {
			return raw + suffixUnit(wd.Unit)
		}
		return "—"
	}
	if prec := wd.EffectivePrecision(); prec >= 0 {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return formatNumber(f, prec) + suffixUnit(wd.Unit)
		}
	}
	if raw != "" {
		return raw + suffixUnit(wd.Unit)
	}
	return "—"
}

func formatSecondaryValue(raw string, wd config.WidgetConfig) string {
	if raw == "" {
		return "—"
	}
	if prec := wd.EffectiveSecondaryPrecision(); prec >= 0 {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return formatNumber(f, prec) + suffixUnit(wd.SecondaryUnit)
		}
	}
	return raw + suffixUnit(wd.SecondaryUnit)
}

func suffixUnit(u string) string {
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "°") || strings.HasPrefix(u, "%") {
		return " " + u
	}
	return " " + u
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func fillCircle(img *image.Gray, cx, cy, r int, c color.Gray) {
	b := img.Bounds()
	r2 := r * r
	for dy := -r; dy <= r; dy++ {
		y := cy + dy
		if y < b.Min.Y || y >= b.Max.Y {
			continue
		}
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r2 {
				x := cx + dx
				if x >= b.Min.X && x < b.Max.X {
					img.SetGray(x, y, c)
				}
			}
		}
	}
}

func drawThickLine(img *image.Gray, x0, y0, x1, y1, thickness int, c color.Gray) {
	rad := thickness / 2
	if rad < 0 {
		rad = 0
	}
	dx := x1 - x0
	if dx < 0 {
		dx = -dx
	}
	dy := y1 - y0
	if dy < 0 {
		dy = -dy
	}
	sx := -1
	if x0 < x1 {
		sx = 1
	}
	sy := -1
	if y0 < y1 {
		sy = 1
	}
	err := dx - dy
	for {
		if rad == 0 {
			if image.Pt(x0, y0).In(img.Bounds()) {
				img.SetGray(x0, y0, c)
			}
		} else {
			fillCircle(img, x0, y0, rad, c)
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

func drawArc(img *image.Gray, cx, cy, radius, thickness int, startRad, endRad float64, c color.Gray) {
	steps := radius * 3
	if steps < 40 {
		steps = 40
	}
	rad := thickness / 2
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		ang := startRad + (endRad-startRad)*t
		px := cx + int(math.Round(float64(radius)*math.Cos(ang)))
		py := cy - int(math.Round(float64(radius)*math.Sin(ang)))
		if rad <= 0 {
			if image.Pt(px, py).In(img.Bounds()) {
				img.SetGray(px, py, c)
			}
		} else {
			fillCircle(img, px, py, rad, c)
		}
	}
}

func drawVerticalArea(img *image.Gray, p0, p1 image.Point, baseY int, c color.Gray) {
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
				img.SetGray(x, y, c)
			}
		}
	}
}

func (r *Renderer) sensorTrend(wd config.WidgetConfig) string {
	pts := r.mq.GetHistory(wd.BrokerID, wd.StateTopic, wd.StateJSONPath, 12)
	if len(pts) < 2 {
		return ""
	}
	diff := pts[len(pts)-1].Value - pts[0].Value
	if diff > 0.05 {
		return "▲"
	}
	if diff < -0.05 {
		return "▼"
	}
	return ""
}

func (r *Renderer) lookupWidgetByIDLocked(id string) (config.WidgetConfig, bool) {
	if id == "" {
		return config.WidgetConfig{}, false
	}
	for _, p := range r.cfg.Pages {
		for _, w := range p.Widgets {
			if w.ID == id {
				return w, true
			}
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				if pin.Widget.ID == id {
					return pin.Widget, true
				}
			}
		}
	}
	return config.WidgetConfig{}, false
}

func (r *Renderer) executeWidgetAction(widgetID, state, brokerID, topic, payload string, qos byte, retain bool) {
	if widgetID != "" {
		r.mu.Lock()
		target, ok := r.lookupWidgetByIDLocked(widgetID)
		r.mu.Unlock()
		if ok && target.Type == "switch" {
			st := strings.ToUpper(strings.TrimSpace(state))
			switch st {
			case "TOGGLE":
				r.executeSwitchToggle(target)
			default:
				turnOn := st != "OFF" && st != "0" && st != "VYP"
				nextState := target.ValueOff
				cmdPayload := target.PayloadOff
				if turnOn {
					nextState = target.ValueOn
					cmdPayload = target.PayloadOn
				}
				if target.StateTopic != "" && target.StateJSONPath == "" {
					r.mq.SetLocalValue(target.BrokerID, target.StateTopic, nextState)
				}
				if strings.TrimSpace(target.CommandTopic) != "" {
					_ = r.mq.Publish(target.BrokerID, target.CommandTopic, target.QoS, target.Retain, cmdPayload)
				}
			}
		}
	}
	if strings.TrimSpace(topic) != "" {
		_ = r.mq.Publish(brokerID, topic, qos, retain, payload)
	}
	if r.ctrl.OnWidgetChanged != nil {
		r.ctrl.OnWidgetChanged()
	}
}

func (r *Renderer) executeMacro(wd config.WidgetConfig) {
	for _, act := range wd.MacroActions {
		r.executeWidgetAction(act.WidgetID, act.State, act.BrokerID, act.Topic, act.Payload, wd.QoS, wd.Retain)
	}
	r.ShowNotice(r.tr().SceneTriggered+wd.Label, 4*time.Second)
}

// TickTimersAndSchedules advances running countdown timers and triggers any due daily schedules.
func (r *Renderer) TickTimersAndSchedules(now time.Time) (changed bool, wokeAlert bool) {
	type pendingAction struct {
		widgetID string
		state    string
		brokerID string
		topic    string
		payload  string
		qos      byte
		retain   bool
		notice   string
		wake     bool
	}
	var actions []pendingAction

	r.mu.Lock()
	tr := stringsForLang(config.NormalizeLanguage(r.cfg.Device.Language))
	if r.activeTimers == nil {
		r.activeTimers = make(map[string]activeTimer)
	}
	if r.lastSchedFired == nil {
		r.lastSchedFired = make(map[string]string)
	}

	for id, tm := range r.activeTimers {
		if !now.Before(tm.EndsAt) {
			delete(r.activeTimers, id)
			targetState := tm.Widget.TargetState
			if strings.TrimSpace(targetState) == "" {
				targetState = "OFF"
			}
			actions = append(actions, pendingAction{
				widgetID: tm.Widget.TargetWidgetID,
				state:    targetState,
				brokerID: tm.Widget.BrokerID,
				topic:    tm.Widget.CommandTopic,
				payload:  tm.Widget.Payload,
				qos:      tm.Widget.QoS,
				retain:   tm.Widget.Retain,
				notice:   tr.TimerFinished + tm.Widget.Label,
				wake:     true,
			})
		} else {
			changed = true
		}
	}

	minuteKey := now.Format("2006-01-02 15:04")
	isoDay := int(now.Weekday())
	if isoDay == 0 {
		isoDay = 7
	}
	for _, sc := range r.cfg.Schedules {
		if !sc.Enabled || sc.Hour != now.Hour() || sc.Minute != now.Minute() {
			continue
		}
		if len(sc.Days) > 0 && !intSliceContains(sc.Days, isoDay) {
			continue
		}
		if r.lastSchedFired[sc.ID] == minuteKey {
			continue
		}
		r.lastSchedFired[sc.ID] = minuteKey
		name := sc.Name
		if name == "" {
			name = fmt.Sprintf("%02d:%02d", sc.Hour, sc.Minute)
		}
		actions = append(actions, pendingAction{
			widgetID: sc.WidgetID,
			state:    sc.State,
			brokerID: sc.BrokerID,
			topic:    sc.Topic,
			payload:  sc.Payload,
			notice:   tr.ScheduleTriggered + name,
		})
	}
	if len(r.lastSchedFired) > 100 {
		for k, v := range r.lastSchedFired {
			if v != minuteKey {
				delete(r.lastSchedFired, k)
			}
		}
	}
	r.mu.Unlock()

	if len(actions) > 0 {
		changed = true
		for _, a := range actions {
			r.executeWidgetAction(a.widgetID, a.state, a.brokerID, a.topic, a.payload, a.qos, a.retain)
			r.mu.Lock()
			r.noticeText = a.notice
			r.noticeUntil = now.Add(15 * time.Second)
			if a.wake {
				r.standbyMode = false
				wokeAlert = true
			}
			r.mu.Unlock()
		}
	}
	return changed, wokeAlert
}

func intSliceContains(arr []int, target int) bool {
	for _, v := range arr {
		if v == target {
			return true
		}
	}
	return false
}

func formatTimerPreset(mins int) string {
	if mins >= 60 && mins%60 == 0 {
		return fmt.Sprintf("%dh", mins/60)
	}
	return fmt.Sprintf("%dm", mins)
}

func formatCountdown(d time.Duration) string {
	secs := int(math.Ceil(d.Seconds()))
	if secs < 0 {
		secs = 0
	}
	if secs >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", secs/3600, (secs%3600)/60, secs%60)
	}
	return fmt.Sprintf("%02d:%02d", secs/60, secs%60)
}

func (r *Renderer) drawTimerCard(rect image.Rectangle, wd config.WidgetConfig, now time.Time) {
	t, active := r.activeTimers[wd.ID]
	if active && now.Before(t.EndsAt) {
		face := drawCardFrame(r.canvas, rect, 10, true)
		drawIconBadge(r.canvas, face.Min.X+24, face.Min.Y+22, 13, "clock", true)
		drawText(r.canvas, r.fonts.bold16, face.Min.X+44, face.Min.Y+28, colWhite,
			truncateText(r.fonts.bold16, pinPrefixedLabel(wd), face.Dx()-54))

		rem := t.EndsAt.Sub(now)
		drawCenteredText(r.canvas, r.fonts.bold28,
			image.Rect(face.Min.X+10, face.Min.Y+38, face.Max.X-10, face.Max.Y-54),
			colWhite, formatCountdown(rem))

		bar := image.Rect(face.Min.X+14, face.Max.Y-50, face.Max.X-14, face.Max.Y-43)
		fillRoundedRect(r.canvas, bar, 3, colDark)
		drawRoundedRect(r.canvas, bar, 3, 1, colWhite)
		if t.Duration > 0 {
			frac := float64(rem) / float64(t.Duration)
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			fw := int(math.Round(frac * float64(bar.Dx())))
			if fw > 0 {
				fillRoundedRect(r.canvas, image.Rect(bar.Min.X, bar.Min.Y, bar.Min.X+fw, bar.Max.Y), 3, colWhite)
			}
		}

		btnY0, btnY1 := face.Max.Y-38, face.Max.Y-8
		gap := 8
		availW := face.Dx() - 24 - gap
		addW := (availW * 42) / 100
		addR := image.Rect(face.Min.X+12, btnY0, face.Min.X+12+addW, btnY1)
		canR := image.Rect(addR.Max.X+gap, btnY0, face.Max.X-12, btnY1)

		fillRoundedRect(r.canvas, addR, 7, colWhite)
		drawCenteredText(r.canvas, r.fonts.bold16, addR, colBlack, "+5m")
		fillRoundedRect(r.canvas, canR, 7, colWhite)
		drawCenteredText(r.canvas, r.fonts.bold16, canR, colBlack, r.tr().TimerCancel)

		r.hits = append(r.hits,
			HitAction{Rect: addR, Type: ActionTimerAdd, Widget: wd, IntVal: 5},
			HitAction{Rect: canR, Type: ActionTimerCancel, Widget: wd},
		)
		return
	}

	tr := r.tr()
	face := drawCardFrame(r.canvas, rect, 10, false)
	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, face.Min.X+26, face.Min.Y+26, 15, icon, false)
	drawText(r.canvas, r.fonts.bold20, face.Min.X+48, face.Min.Y+28, colBlack,
		truncateText(r.fonts.bold20, pinPrefixedLabel(wd), face.Dx()-58))

	sub := wd.Subtitle
	if sub == "" && wd.TargetWidgetID != "" {
		if tw, ok := r.lookupWidgetByIDLocked(wd.TargetWidgetID); ok {
			actVerb := tr.TimerTurnsOff
			if strings.EqualFold(wd.TargetState, "ON") {
				actVerb = tr.TimerTurnsOn
			} else if strings.EqualFold(wd.TargetState, "TOGGLE") {
				actVerb = tr.TimerToggles
			}
			sub = actVerb + ": " + tw.Label
		}
	}
	if sub == "" {
		sub = tr.TimerDefaultSub
	}
	drawText(r.canvas, r.fonts.small, face.Min.X+48, face.Min.Y+46, colDark,
		truncateText(r.fonts.small, sub, face.Dx()-58))

	presets := wd.TimerPresets
	if len(presets) == 0 {
		presets = []int{5, 15, 30}
	}
	if len(presets) > 4 {
		presets = presets[:4]
	}
	padX := 10
	gap := 8
	n := len(presets)
	btnW := (face.Dx() - 2*padX - (n-1)*gap) / n
	by0 := face.Max.Y - 48
	by1 := face.Max.Y - 10
	for i, mins := range presets {
		x0 := face.Min.X + padX + i*(btnW+gap)
		btnR := image.Rect(x0, by0, x0+btnW, by1)
		drawRoundedButton(r.canvas, btnR, 8, r.fonts.bold16, formatTimerPreset(mins), false)
		r.hits = append(r.hits, HitAction{
			Rect:   btnR,
			Type:   ActionTimerStart,
			Widget: wd,
			IntVal: mins,
		})
	}
}

func (r *Renderer) drawCalendarCard(rect image.Rectangle, wd config.WidgetConfig, now time.Time) {
	lang := r.lang()
	tr := r.tr()
	face := drawCardFrame(r.canvas, rect, 10, false)
	r.hits = append(r.hits, HitAction{Rect: rect, Type: ActionOpenCalendarDetail, Widget: wd})

	hdr := image.Rect(face.Min.X+3, face.Min.Y+3, face.Max.X-3, face.Min.Y+35)
	fillRoundedRect(r.canvas, hdr, 7, colLight)
	fillRect(r.canvas, image.Rect(hdr.Min.X, hdr.Max.Y-1, hdr.Max.X, hdr.Max.Y+1), colBlack)

	drawIconBadge(r.canvas, hdr.Min.X+18, hdr.Min.Y+16, 12, "calendar", false)
	title := wd.Label
	if title == "" {
		title = tr.CalendarTitle
	}
	drawText(r.canvas, r.fonts.bold16, hdr.Min.X+36, hdr.Min.Y+22, colBlack,
		truncateText(r.fonts.bold16, title, hdr.Dx()-110))

	if !r.cfg.Calendar.Enabled || r.calSvc == nil {
		drawCenteredText(r.canvas, r.fonts.regular,
			image.Rect(face.Min.X+10, face.Min.Y+42, face.Max.X-10, face.Max.Y-10),
			colDark, tr.CalendarEnableHint)
		return
	}

	evs := r.calSvc.Events()
	drawRightText(r.canvas, r.fonts.small, hdr.Max.X-8, hdr.Min.Y+21, colDark,
		fmt.Sprintf(tr.EventsCount, len(evs)))

	if len(evs) == 0 {
		drawCenteredText(r.canvas, r.fonts.regular,
			image.Rect(face.Min.X+10, face.Min.Y+42, face.Max.X-10, face.Max.Y-10),
			colDark, tr.CalendarEmptyHint)
		return
	}

	maxShow := 3
	if len(evs) < maxShow {
		maxShow = len(evs)
	}
	rowTop := face.Min.Y + 42
	rowH := (face.Max.Y - rowTop - 8) / 3
	if rowH < 24 {
		rowH = 24
	}
	whenColW := 128
	for i := 0; i < maxShow; i++ {
		ry := rowTop + i*rowH
		if i > 0 {
			drawDashedHLine(r.canvas, face.Min.X+10, face.Max.X-10, ry-2, 3, 3, colSubtleGray)
		}
		when := formatEventWhenLang(evs[i], now, lang)
		drawText(r.canvas, r.fonts.small, face.Min.X+12, ry+17, colDark,
			truncateText(r.fonts.small, when, whenColW))
		drawText(r.canvas, r.fonts.bold16, face.Min.X+12+whenColW+8, ry+18, colBlack,
			truncateText(r.fonts.bold16, localizedEventSummary(evs[i], tr), face.Dx()-30-whenColW))
	}
}

func (r *Renderer) drawWeatherCard(rect image.Rectangle, wd config.WidgetConfig, now time.Time) {
	lang := r.lang()
	tr := r.tr()
	face := drawCardFrame(r.canvas, rect, 10, false)
	r.hits = append(r.hits, HitAction{Rect: rect, Type: ActionOpenWeatherDetail, Widget: wd})

	var ws weather.Snapshot
	if r.weatherSvc != nil {
		ws = r.weatherSvc.Snapshot()
	}

	hdr := image.Rect(face.Min.X+3, face.Min.Y+3, face.Max.X-3, face.Min.Y+36)
	fillRoundedRect(r.canvas, hdr, 7, colLight)
	fillRect(r.canvas, image.Rect(hdr.Min.X, hdr.Max.Y-1, hdr.Max.X, hdr.Max.Y+1), colBlack)

	icon := "sun"
	if ws.Available && ws.Icon != "" {
		icon = ws.Icon
	} else if wd.Icon != "" {
		icon = wd.Icon
	}
	drawIconBadge(r.canvas, hdr.Min.X+18, hdr.Min.Y+16, 12, icon, false)

	title := wd.Label
	if ws.Available && ws.LocationName != "" {
		title = ws.LocationName
	}
	drawText(r.canvas, r.fonts.bold16, hdr.Min.X+36, hdr.Min.Y+22, colBlack,
		truncateText(r.fonts.bold16, title, hdr.Dx()-130))

	if hdrRight := formatWeatherHeaderRight(now, lang); hdrRight != "" {
		drawRightText(r.canvas, r.fonts.small, hdr.Max.X-8, hdr.Min.Y+21, colDark,
			truncateText(r.fonts.small, hdrRight, 125))
	}

	if !ws.Available {
		drawCenteredText(r.canvas, r.fonts.regular,
			image.Rect(face.Min.X+10, face.Min.Y+42, face.Max.X-10, face.Max.Y-10),
			colDark, tr.WeatherLoading)
		return
	}

	desc, _ := weather.DescribeWMOLang(ws.WeatherCode, lang)
	tempStr := fmt.Sprintf("%.1f °C", ws.Temperature)
	drawText(r.canvas, r.fonts.bold28, face.Min.X+14, face.Min.Y+72, colBlack, tempStr)
	drawText(r.canvas, r.fonts.bold16, face.Min.X+138, face.Min.Y+64, colBlack,
		truncateText(r.fonts.bold16, desc, face.Dx()-150))
	drawText(r.canvas, r.fonts.small, face.Min.X+138, face.Min.Y+81, colDark,
		fmt.Sprintf(tr.WindFmt, ws.WindSpeed))

	// 3-day mini forecast strip at bottom
	drawDashedHLine(r.canvas, face.Min.X+12, face.Max.X-12, face.Max.Y-34, 3, 3, colSubtleGray)
	dayLabels := [...]string{tr.Today, tr.Tomorrow, tr.DayAfterTomorrow}
	nDays := len(ws.Daily)
	if nDays > 3 {
		nDays = 3
	}
	if nDays > 0 {
		colW := (face.Dx() - 24) / nDays
		for i := 0; i < nDays; i++ {
			df := ws.Daily[i]
			cell := image.Rect(face.Min.X+12+i*colW, face.Max.Y-30, face.Min.X+12+(i+1)*colW, face.Max.Y-6)
			txt := fmt.Sprintf("%s %.0f°/%.0f°", dayLabels[i], df.TempMin, df.TempMax)
			drawCenteredText(r.canvas, r.fonts.small, cell, colBlack, truncateText(r.fonts.small, txt, colW-4))
		}
	}
}

func (r *Renderer) drawDitheredPlot(plot image.Rectangle, values []float64, minV, maxV float64, pad, ptRad int) {
	fillRect(r.canvas, plot, colWhite)
	drawRect(r.canvas, plot, 2, colBlack)
	for i := 1; i <= 3; i++ {
		gy := plot.Min.Y + (plot.Dy()*i)/4
		drawDashedHLine(r.canvas, plot.Min.X+2, plot.Max.X-2, gy, 4, 4, colSubtleGray)
	}
	if len(values) == 0 {
		return
	}
	if math.Abs(maxV-minV) < 1e-6 {
		minV -= 1
		maxV += 1
	}
	pw := plot.Dx() - 2*pad
	ph := plot.Dy() - 2*pad
	coords := make([]image.Point, len(values))
	for i, val := range values {
		x := plot.Min.X + pad
		if len(values) > 1 {
			x = plot.Min.X + pad + i*pw/(len(values)-1)
		}
		frac := (val - minV) / (maxV - minV)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		y := plot.Max.Y - pad - int(math.Round(frac*float64(ph)))
		coords[i] = image.Pt(x, y)
	}
	if len(coords) == 1 {
		fillCircle(r.canvas, coords[0].X, coords[0].Y, ptRad+3, colBlack)
		fillCircle(r.canvas, coords[0].X, coords[0].Y, 2, colWhite)
		return
	}
	for i := 0; i < len(coords)-1; i++ {
		drawDitheredVerticalArea(r.canvas, coords[i], coords[i+1], plot.Max.Y-3)
	}
	for i := 0; i < len(coords)-1; i++ {
		drawThickLine(r.canvas, coords[i].X, coords[i].Y, coords[i+1].X, coords[i+1].Y, 3, colBlack)
	}
	for i := 0; i < len(coords)-1; i++ {
		fillCircle(r.canvas, coords[i].X, coords[i].Y, ptRad, colBlack)
	}
	last := coords[len(coords)-1]
	fillCircle(r.canvas, last.X, last.Y, ptRad+3, colBlack)
	fillCircle(r.canvas, last.X, last.Y, 2, colWhite)
}

func (r *Renderer) drawWeatherDetailModal(w, top, bottom int, now time.Time) {
	lang := r.lang()
	tr := r.tr()
	outer := image.Rect(14, top, w-14, bottom)
	face := drawCardFrame(r.canvas, outer, 12, false)

	var ws weather.Snapshot
	if r.weatherSvc != nil {
		ws = r.weatherSvc.Snapshot()
	}

	hdr := image.Rect(face.Min.X+4, face.Min.Y+4, face.Max.X-4, face.Min.Y+52)
	fillRoundedRect(r.canvas, hdr, 8, colLight)
	fillRect(r.canvas, image.Rect(hdr.Min.X, hdr.Max.Y-2, hdr.Max.X, hdr.Max.Y), colBlack)

	icon := "sun"
	if ws.Available && ws.Icon != "" {
		icon = ws.Icon
	}
	locName := ws.LocationName
	if locName == "" {
		locName = tr.ForecastTitle
	}
	drawIconBadge(r.canvas, hdr.Min.X+24, hdr.Min.Y+24, 15, icon, false)
	drawText(r.canvas, r.fonts.bold20, hdr.Min.X+48, hdr.Min.Y+31, colBlack,
		truncateText(r.fonts.bold20, tr.WeatherModalPrefix+locName, hdr.Dx()-164))

	closeBtn := image.Rect(hdr.Max.X-108, hdr.Min.Y+6, hdr.Max.X-8, hdr.Max.Y-6)
	drawRoundedButton(r.canvas, closeBtn, 7, r.fonts.bold16, tr.CloseBtn, true)
	r.hits = append(r.hits, HitAction{Rect: closeBtn, Type: ActionCloseDetail})

	if !ws.Available {
		drawCenteredText(r.canvas, r.fonts.regular,
			image.Rect(face.Min.X+16, hdr.Max.Y+20, face.Max.X-16, face.Max.Y-20),
			colDark, tr.WeatherLoading)
		return
	}

	// Hero summary strip
	heroDesc, _ := weather.DescribeWMOLang(ws.WeatherCode, lang)
	heroY := hdr.Max.Y + 34
	drawText(r.canvas, r.fonts.bold28, face.Min.X+16, heroY, colBlack, fmt.Sprintf("%.1f °C", ws.Temperature))
	drawText(r.canvas, r.fonts.bold16, face.Min.X+134, heroY-10, colBlack, truncateText(r.fonts.bold16, heroDesc, 220))
	drawText(r.canvas, r.fonts.small, face.Min.X+134, heroY+8, colDark, fmt.Sprintf(tr.WindFmt, ws.WindSpeed))
	if hdrRight := formatWeatherHeaderRight(now, lang); hdrRight != "" {
		drawRightText(r.canvas, r.fonts.small, face.Max.X-16, heroY-4, colDark, truncateText(r.fonts.small, hdrRight, 190))
	}

	bodyTop := hdr.Max.Y + 52
	drawHourly := func(box image.Rectangle) {
		drawText(r.canvas, r.fonts.bold16, box.Min.X, box.Min.Y+16, colBlack, tr.HourlyTitle)
		if len(ws.Hourly) == 0 {
			drawCenteredText(r.canvas, r.fonts.small, image.Rect(box.Min.X, box.Min.Y+24, box.Max.X, box.Max.Y), colDark, tr.HourlyEmpty)
			return
		}
		vals := make([]float64, len(ws.Hourly))
		minV, maxV := ws.Hourly[0].Temperature, ws.Hourly[0].Temperature
		maxRain := 0
		for i, h := range ws.Hourly {
			vals[i] = h.Temperature
			if h.Temperature < minV {
				minV = h.Temperature
			}
			if h.Temperature > maxV {
				maxV = h.Temperature
			}
			if h.PrecipProb > maxRain {
				maxRain = h.PrecipProb
			}
		}
		drawRightText(r.canvas, r.fonts.small, box.Max.X, box.Min.Y+15, colDark, fmt.Sprintf(tr.RainMaxFmt, maxRain))
		plot := image.Rect(box.Min.X, box.Min.Y+24, box.Max.X, box.Max.Y-24)
		r.drawDitheredPlot(plot, vals, minV, maxV, 8, 2)

		drawText(r.canvas, r.fonts.small, plot.Min.X, box.Max.Y-6, colDark, ws.Hourly[0].HourLabel)
		mid := len(ws.Hourly) / 2
		drawCenteredText(r.canvas, r.fonts.small, image.Rect(plot.Min.X+60, box.Max.Y-20, plot.Max.X-60, box.Max.Y), colDark,
			fmt.Sprintf("%s  (%.0f°–%.0f°)", ws.Hourly[mid].HourLabel, minV, maxV))
		drawRightText(r.canvas, r.fonts.small, plot.Max.X, box.Max.Y-6, colDark, ws.Hourly[len(ws.Hourly)-1].HourLabel)
	}

	drawDaily := func(box image.Rectangle) {
		drawText(r.canvas, r.fonts.bold16, box.Min.X, box.Min.Y+16, colBlack, tr.WeeklyTitle)
		listTop := box.Min.Y + 24
		n := len(ws.Daily)
		if n > 7 {
			n = 7
		}
		if n == 0 {
			drawCenteredText(r.canvas, r.fonts.small, image.Rect(box.Min.X, listTop, box.Max.X, box.Max.Y), colDark, tr.DailyEmpty)
			return
		}
		rowH := (box.Max.Y - listTop) / 7
		if rowH < 26 {
			rowH = 26
		}
		for i := 0; i < n; i++ {
			df := ws.Daily[i]
			ry := listTop + i*rowH
			rowR := image.Rect(box.Min.X, ry+2, box.Max.X, ry+rowH-3)
			fillRoundedRect(r.canvas, rowR, 6, colBgCanvas)
			drawRoundedRect(r.canvas, rowR, 6, 1, colMidGray)

			dayTxt := df.Date
			if parsedD, err := time.Parse("2006-01-02", df.Date); err == nil {
				dayTxt = formatDailyRowDate(parsedD, i, lang)
			}
			dDesc, _ := weather.DescribeWMOLang(df.WeatherCode, lang)
			midY := ry + rowH/2 + 5
			drawText(r.canvas, r.fonts.bold16, rowR.Min.X+10, midY, colBlack, dayTxt)
			drawText(r.canvas, r.fonts.small, rowR.Min.X+128, midY-1, colDark,
				truncateText(r.fonts.small, fmt.Sprintf("%s (%d%%)", dDesc, df.PrecipProbMax), rowR.Dx()-218))
			drawRightText(r.canvas, r.fonts.bold16, rowR.Max.X-10, midY, colBlack,
				fmt.Sprintf("%.0f° / %.0f°", df.TempMin, df.TempMax))
		}
	}

	if w > bottom {
		// Landscape: Hourly plot left, 7-day forecast right
		midX := face.Min.X + (face.Dx()*50)/100
		drawHourly(image.Rect(face.Min.X+16, bodyTop, midX-8, face.Max.Y-12))
		drawDaily(image.Rect(midX+8, bodyTop, face.Max.X-16, face.Max.Y-12))
	} else {
		// Portrait: Hourly plot top, 7-day list below
		splitY := bodyTop + 210
		drawHourly(image.Rect(face.Min.X+16, bodyTop, face.Max.X-16, splitY))
		drawDaily(image.Rect(face.Min.X+16, splitY+8, face.Max.X-16, face.Max.Y-12))
	}
}

func (r *Renderer) drawCalendarDetailModal(w, top, bottom int, now time.Time) {
	lang := r.lang()
	tr := r.tr()
	outer := image.Rect(14, top, w-14, bottom)
	face := drawCardFrame(r.canvas, outer, 12, false)

	hdr := image.Rect(face.Min.X+4, face.Min.Y+4, face.Max.X-4, face.Min.Y+52)
	fillRoundedRect(r.canvas, hdr, 8, colLight)
	fillRect(r.canvas, image.Rect(hdr.Min.X, hdr.Max.Y-2, hdr.Max.X, hdr.Max.Y), colBlack)

	drawIconBadge(r.canvas, hdr.Min.X+24, hdr.Min.Y+24, 15, "calendar", false)
	drawText(r.canvas, r.fonts.bold20, hdr.Min.X+48, hdr.Min.Y+31, colBlack,
		truncateText(r.fonts.bold20, tr.CalendarModalTitle, hdr.Dx()-164))

	closeBtn := image.Rect(hdr.Max.X-108, hdr.Min.Y+6, hdr.Max.X-8, hdr.Max.Y-6)
	drawRoundedButton(r.canvas, closeBtn, 7, r.fonts.bold16, tr.CloseBtn, true)
	r.hits = append(r.hits, HitAction{Rect: closeBtn, Type: ActionCloseDetail})

	body := image.Rect(face.Min.X+16, hdr.Max.Y+12, face.Max.X-16, face.Max.Y-14)
	if !r.cfg.Calendar.Enabled || r.calSvc == nil {
		drawCenteredText(r.canvas, r.fonts.regular, body, colDark, tr.CalendarModalOff)
		return
	}
	evs := r.calSvc.Events()
	if len(evs) == 0 {
		drawCenteredText(r.canvas, r.fonts.regular, body, colDark, tr.CalendarModalEmpty)
		return
	}

	rowH := 56
	maxRows := body.Dy() / rowH
	if maxRows < 1 {
		maxRows = 1
	}
	if len(evs) < maxRows {
		maxRows = len(evs)
	}
	whenW := 196
	for i := 0; i < maxRows; i++ {
		ev := evs[i]
		ry := body.Min.Y + i*rowH
		rowR := image.Rect(body.Min.X, ry+2, body.Max.X, ry+rowH-4)
		fillRoundedRect(r.canvas, rowR, 8, colBgCanvas)
		drawRoundedRect(r.canvas, rowR, 8, 1, colBlack)

		whenPill := image.Rect(rowR.Min.X+8, rowR.Min.Y+8, rowR.Min.X+8+whenW, rowR.Max.Y-8)
		fillRoundedRect(r.canvas, whenPill, 6, colWhite)
		drawRoundedRect(r.canvas, whenPill, 6, 1, colBlack)
		drawCenteredText(r.canvas, r.fonts.bold16, whenPill, colBlack,
			truncateText(r.fonts.bold16, formatEventWhenLang(ev, now, lang), whenPill.Dx()-8))

		tx := whenPill.Max.X + 12
		maxTW := rowR.Max.X - tx - 10
		summary := localizedEventSummary(ev, tr)
		if ev.Location != "" {
			drawText(r.canvas, r.fonts.bold16, tx, rowR.Min.Y+22, colBlack, truncateText(r.fonts.bold16, summary, maxTW))
			drawText(r.canvas, r.fonts.small, tx, rowR.Min.Y+40, colDark, truncateText(r.fonts.small, ev.Location, maxTW))
		} else {
			drawText(r.canvas, r.fonts.bold16, tx, (rowR.Min.Y+rowR.Max.Y)/2+6, colBlack, truncateText(r.fonts.bold16, summary, maxTW))
		}
	}
}

func (r *Renderer) drawSensorDetailModal(w, top, bottom int, wd config.WidgetConfig, now time.Time) {
	outer := image.Rect(14, top, w-14, bottom)
	face := drawCardFrame(r.canvas, outer, 12, false)

	// Header strip with Close button
	hdr := image.Rect(face.Min.X+4, face.Min.Y+4, face.Max.X-4, face.Min.Y+54)
	fillRoundedRect(r.canvas, hdr, 8, colLight)
	fillRect(r.canvas, image.Rect(hdr.Min.X, hdr.Max.Y-2, hdr.Max.X, hdr.Max.Y), colBlack)

	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, hdr.Min.X+24, hdr.Min.Y+25, 15, icon, false)
	drawText(r.canvas, r.fonts.bold20, hdr.Min.X+48, hdr.Min.Y+32, colBlack,
		truncateText(r.fonts.bold20, wd.Label, hdr.Dx()-160))

	tr := r.tr()
	closeBtn := image.Rect(hdr.Max.X-108, hdr.Min.Y+7, hdr.Max.X-8, hdr.Max.Y-7)
	drawRoundedButton(r.canvas, closeBtn, 7, r.fonts.bold16, tr.CloseBtn, true)
	r.hits = append(r.hits, HitAction{Rect: closeBtn, Type: ActionCloseDetail})

	pts := r.mq.GetHistory(wd.BrokerID, wd.StateTopic, wd.StateJSONPath, 120)
	curText := "—"
	ageText := tr.NoData
	if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		raw := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
		curText = formatWidgetValue(raw, wd)
		ageText = tr.UpdatedPrefix + formatAgeLang(now.Sub(tv.UpdatedAt), tr)
	}

	trend := r.sensorTrend(wd)
	trendDesc := tr.TrendStable
	if trend == "▲" {
		trendDesc = tr.TrendRising
	} else if trend == "▼" {
		trendDesc = tr.TrendFalling
	}

	// Current value hero row
	drawText(r.canvas, r.fonts.bold34, face.Min.X+20, face.Min.Y+104, colBlack, curText)
	drawRightText(r.canvas, r.fonts.bold16, face.Max.X-20, face.Min.Y+86, colBlack, trendDesc)
	drawRightText(r.canvas, r.fonts.small, face.Max.X-20, face.Min.Y+106, colDark, ageText)

	// Min / Avg / Max boxes
	minStr, avgStr, maxStr := "—", "—", "—"
	var minV, maxV float64
	if len(pts) > 0 {
		minV, maxV = pts[0].Value, pts[0].Value
		sum := 0.0
		for _, p := range pts {
			if p.Value < minV {
				minV = p.Value
			}
			if p.Value > maxV {
				maxV = p.Value
			}
			sum += p.Value
		}
		avgV := sum / float64(len(pts))
		minStr = formatNumber(minV, wd.EffectivePrecision()) + suffixUnit(wd.Unit)
		avgStr = formatNumber(avgV, wd.EffectivePrecision()) + suffixUnit(wd.Unit)
		maxStr = formatNumber(maxV, wd.EffectivePrecision()) + suffixUnit(wd.Unit)
	}

	statY0 := face.Min.Y + 124
	statH := 64
	statGap := 12
	statW := (face.Dx() - 40 - 2*statGap) / 3
	stats := []struct{ title, val string }{
		{tr.StatMin, minStr},
		{tr.StatAvg, avgStr},
		{tr.StatMax, maxStr},
	}
	for i, st := range stats {
		sx := face.Min.X + 20 + i*(statW+statGap)
		sBox := image.Rect(sx, statY0, sx+statW, statY0+statH)
		fillRoundedRect(r.canvas, sBox, 8, colBgCanvas)
		drawRoundedRect(r.canvas, sBox, 8, 2, colBlack)
		drawCenteredText(r.canvas, r.fonts.small, image.Rect(sBox.Min.X, sBox.Min.Y+4, sBox.Max.X, sBox.Min.Y+24), colDark, st.title)
		drawCenteredText(r.canvas, r.fonts.bold20, image.Rect(sBox.Min.X, sBox.Min.Y+24, sBox.Max.X, sBox.Max.Y-4), colBlack,
			truncateText(r.fonts.bold20, st.val, statW-8))
	}

	// Large History Plot
	plot := image.Rect(face.Min.X+20, statY0+statH+18, face.Max.X-20, face.Max.Y-36)
	if len(pts) == 0 {
		r.drawDitheredPlot(plot, nil, 0, 1, 8, 3)
		drawCenteredText(r.canvas, r.fonts.regular, plot, colDark, tr.NoHistory)
		return
	}
	vals := make([]float64, len(pts))
	for i, p := range pts {
		vals[i] = p.Value
	}
	r.drawDitheredPlot(plot, vals, minV, maxV, 8, 3)

	drawText(r.canvas, r.fonts.small, plot.Min.X, face.Max.Y-12, colDark,
		fmt.Sprintf(tr.SampleCountFmt, len(pts)))
	drawRightText(r.canvas, r.fonts.small, plot.Max.X, face.Max.Y-12, colDark,
		fmt.Sprintf(tr.RangeLabelFmt, formatNumber(minV, wd.EffectivePrecision()), formatNumber(maxV, wd.EffectivePrecision()), suffixUnit(wd.Unit)))
}

func (r *Renderer) drawPinModal(w, top, bottom int) {
	tr := r.tr()
	marginX := 40
	if w > bottom-top {
		marginX = 24
	}
	outer := image.Rect(marginX, top+10, w-marginX, bottom-10)
	face := drawCardFrame(r.canvas, outer, 14, false)

	targetLen := len(strings.TrimSpace(r.cfg.Device.PinCode))
	if targetLen < 4 {
		targetLen = 4
	}
	var masked strings.Builder
	for i := 0; i < targetLen; i++ {
		if i > 0 {
			masked.WriteString("   ")
		}
		if i < len(r.pinInput) {
			masked.WriteString("●")
		} else {
			masked.WriteString("○")
		}
	}
	dispText := masked.String()
	if r.pinError {
		dispText = tr.PinWrong
	}

	keys := []struct {
		label string
		act   ActionType
		val   string
	}{
		{"1", ActionPinKey, "1"}, {"2", ActionPinKey, "2"}, {"3", ActionPinKey, "3"},
		{"4", ActionPinKey, "4"}, {"5", ActionPinKey, "5"}, {"6", ActionPinKey, "6"},
		{"7", ActionPinKey, "7"}, {"8", ActionPinKey, "8"}, {"9", ActionPinKey, "9"},
		{tr.TimerCancel, ActionPinCancel, ""}, {"0", ActionPinKey, "0"}, {"⌫", ActionPinBackspace, ""},
	}

	var disp, gridRect image.Rectangle
	if w > bottom-top {
		// Landscape split: title + display left, keypad right
		leftX := face.Min.X + 22
		leftMaxX := face.Min.X + (face.Dx()*42)/100
		drawCenteredText(r.canvas, r.fonts.bold20,
			image.Rect(leftX, face.Min.Y+80, leftMaxX, face.Min.Y+124),
			colBlack, tr.PinTitle)
		disp = image.Rect(leftX+8, face.Min.Y+145, leftMaxX-8, face.Min.Y+215)
		gridRect = image.Rect(leftMaxX+20, face.Min.Y+20, face.Max.X-24, face.Max.Y-20)
	} else {
		drawCenteredText(r.canvas, r.fonts.bold20,
			image.Rect(face.Min.X+10, face.Min.Y+14, face.Max.X-10, face.Min.Y+48),
			colBlack, tr.PinPrompt)
		disp = image.Rect(face.Min.X+36, face.Min.Y+56, face.Max.X-36, face.Min.Y+116)
		gridRect = image.Rect(face.Min.X+36, face.Min.Y+134, face.Max.X-36, face.Max.Y-22)
	}

	fillRoundedRect(r.canvas, disp, 8, colBgCanvas)
	drawRoundedRect(r.canvas, disp, 8, 2, colBlack)
	drawCenteredText(r.canvas, r.fonts.bold28, disp, colBlack, dispText)

	gapX := 14
	gapY := 14
	btnW := (gridRect.Dx() - 2*gapX) / 3
	btnH := (gridRect.Dy() - 3*gapY) / 4

	for i, k := range keys {
		row := i / 3
		col := i % 3
		x0 := gridRect.Min.X + col*(btnW+gapX)
		y0 := gridRect.Min.Y + row*(btnH+gapY)
		btnR := image.Rect(x0, y0, x0+btnW, y0+btnH)
		f := r.fonts.bold28
		if k.act == ActionPinCancel {
			f = r.fonts.bold16
		}
		drawRoundedButton(r.canvas, btnR, 10, f, k.label, false)
		r.hits = append(r.hits, HitAction{
			Rect:    btnR,
			Type:    k.act,
			Payload: k.val,
		})
	}
}

func (r *Renderer) collectStandbyWidgets() []config.WidgetConfig {
	var explicit []config.WidgetConfig
	var fallback []config.WidgetConfig
	seen := make(map[string]bool)

	consider := func(w config.WidgetConfig) {
		if w.ID == "" || seen[w.ID] {
			return
		}
		seen[w.ID] = true
		if w.ShowInStandby {
			explicit = append(explicit, w)
			return
		}
		switch w.Type {
		case "sensor", "gauge", "chart", "stepper", "switch":
			fallback = append(fallback, w)
		}
	}

	for _, p := range r.cfg.Pages {
		for _, w := range p.Widgets {
			consider(w)
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				consider(pin.Widget)
			}
		}
	}

	out := explicit
	for _, w := range fallback {
		if len(out) >= 4 {
			break
		}
		out = append(out, w)
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func (r *Renderer) drawStandbyWidgetCard(rect image.Rectangle, wd config.WidgetConfig) {
	alerting, _ := r.isWidgetAlerting(wd)
	face := drawCardFrame(r.canvas, rect, 10, alerting)
	fg := colBlack
	subFg := colDark
	if alerting {
		fg = colWhite
		subFg = colLight
	}

	icon := resolveWidgetIcon(wd)
	drawIconBadge(r.canvas, face.Min.X+26, face.Min.Y+28, 15, icon, alerting)
	drawText(r.canvas, r.fonts.bold16, face.Min.X+50, face.Min.Y+26, fg,
		truncateText(r.fonts.bold16, wd.Label, face.Dx()-60))
	if wd.Subtitle != "" {
		drawText(r.canvas, r.fonts.small, face.Min.X+50, face.Min.Y+44, subFg,
			truncateText(r.fonts.small, wd.Subtitle, face.Dx()-60))
	}

	tr := r.tr()
	valText := "—"
	if wd.Type == "switch" {
		valText = tr.SwitchOff
		if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
			if strings.EqualFold(mqtt.ExtractValue(tv.Payload, wd.StateJSONPath), wd.ValueOn) {
				valText = tr.SwitchOnShort
			}
		}
	} else if tv, ok := r.mq.GetTopic(wd.BrokerID, wd.StateTopic); ok {
		raw := mqtt.ExtractValue(tv.Payload, wd.StateJSONPath)
		valText = formatWidgetValue(raw, wd)
	}
	if alerting {
		valText = "! " + valText
	}

	valArea := image.Rect(face.Min.X+12, face.Min.Y+54, face.Max.X-12, face.Max.Y-12)
	valFont := r.fonts.bold28
	if font.MeasureString(valFont, valText).Ceil() > valArea.Dx() {
		valFont = r.fonts.bold20
	}
	drawCenteredText(r.canvas, valFont, valArea, fg, truncateText(valFont, valText, valArea.Dx()))
}

func (r *Renderer) drawStandbyScreen(w, h int, now time.Time, snap mqtt.ManagerSnapshot, hwStat kindle.Status) {
	lang := r.lang()
	tr := r.tr()
	// Top header bar
	topH := 52
	fillRect(r.canvas, image.Rect(0, 0, w, topH), colWhite)
	fillRect(r.canvas, image.Rect(0, topH-2, w, topH), colBlack)

	drawText(r.canvas, r.fonts.bold16, 16, 32, colBlack, truncateText(r.fonts.bold16, r.cfg.Device.Title+tr.StandbyHeaderSuffix, w-240))

	if hwStat.Battery >= 0 {
		drawBatteryIcon(r.canvas, w-160, 19, hwStat.Battery, hwStat.Charging)
		drawText(r.canvas, r.fonts.small, w-126, 31, colDark, fmt.Sprintf("%d%%", hwStat.Battery))
	}
	mqttLbl := tr.OfflineLabel
	if snap.AllConnected {
		mqttLbl = "● MQTT"
	}
	drawRightText(r.canvas, r.fonts.small, w-16, 31, colDark, mqttLbl)

	var ws weather.Snapshot
	if r.weatherSvc != nil {
		ws = r.weatherSvc.Snapshot()
	}
	nextEvWhen := ""
	nextEvTitle := ""
	nextEvSub := ""
	if r.cfg.Calendar.Enabled && r.calSvc != nil {
		evs := r.calSvc.Events()
		for _, ev := range evs {
			endCmp := ev.End
			if endCmp.IsZero() {
				endCmp = ev.Start.Add(time.Hour)
			}
			if !endCmp.Before(now) {
				nextEvWhen = formatEventWhenLang(ev, now, lang)
				nextEvTitle = localizedEventSummary(ev, tr)
				if ev.Location != "" {
					nextEvSub = ev.Location
				}
				break
			}
		}
	}
	nextEvLine := ""
	if nextEvTitle != "" {
		nextEvLine = nextEvWhen + " • " + nextEvTitle
	}

	widgets := r.collectStandbyWidgets()
	contentBottom := h - 68
	wsDesc := ""
	if ws.Available {
		wsDesc, _ = weather.DescribeWMOLang(ws.WeatherCode, lang)
	}

	if w > h {
		// Landscape split layout: Clock/Weather/Calendar on the left, 2x2 sensors on the right
		leftMaxX := 366
		clockCard := image.Rect(18, 66, leftMaxX, contentBottom)
		clockFace := drawCardFrame(r.canvas, clockCard, 14, false)

		timeRect := image.Rect(clockFace.Min.X+10, clockFace.Min.Y+14, clockFace.Max.X-10, clockFace.Min.Y+138)
		drawCenteredText(r.canvas, r.fonts.clock64, timeRect, colBlack, now.Format("15:04"))

		drawDashedHLine(r.canvas, clockFace.Min.X+24, clockFace.Max.X-24, clockFace.Min.Y+148, 4, 4, colSubtleGray)

		dateRect := image.Rect(clockFace.Min.X+10, clockFace.Min.Y+158, clockFace.Max.X-10, clockFace.Min.Y+192)
		drawCenteredText(r.canvas, r.fonts.bold16, dateRect, colBlack,
			truncateText(r.fonts.bold16, formatDateLine(now, lang), dateRect.Dx()))

		infoY := clockFace.Min.Y + 204
		if ws.Available {
			wPill := image.Rect(clockFace.Min.X+12, infoY, clockFace.Max.X-12, infoY+96)
			fillRoundedRect(r.canvas, wPill, 8, colBgCanvas)
			drawRoundedRect(r.canvas, wPill, 8, 1, colSubtleGray)
			drawText(r.canvas, r.fonts.bold20, wPill.Min.X+14, wPill.Min.Y+36, colBlack,
				fmt.Sprintf("☀ %.1f °C", ws.Temperature))
			wDesc := ws.LocationName + " • " + wsDesc
			drawText(r.canvas, r.fonts.small, wPill.Min.X+14, wPill.Min.Y+60, colDark,
				truncateText(r.fonts.small, wDesc, wPill.Dx()-28))
			if len(ws.Daily) > 0 {
				drawText(r.canvas, r.fonts.small, wPill.Min.X+14, wPill.Min.Y+80, colDark,
					fmt.Sprintf(tr.StandbyWeatherSub, ws.Daily[0].TempMin, ws.Daily[0].TempMax, ws.Daily[0].PrecipProbMax))
			}
			infoY = wPill.Max.Y + 12
		}

		if nextEvTitle != "" && clockFace.Max.Y-infoY >= 58 {
			evPill := image.Rect(clockFace.Min.X+12, infoY, clockFace.Max.X-12, clockFace.Max.Y-12)
			fillRoundedRect(r.canvas, evPill, 8, colBgCanvas)
			drawRoundedRect(r.canvas, evPill, 8, 1, colSubtleGray)
			drawText(r.canvas, r.fonts.small, evPill.Min.X+14, evPill.Min.Y+24, colDark,
				truncateText(r.fonts.small, tr.NextEventPrefix+nextEvWhen, evPill.Dx()-28))
			drawText(r.canvas, r.fonts.bold16, evPill.Min.X+14, evPill.Min.Y+48, colBlack,
				truncateText(r.fonts.bold16, nextEvTitle, evPill.Dx()-28))
			if nextEvSub != "" && evPill.Dy() >= 76 {
				drawText(r.canvas, r.fonts.small, evPill.Min.X+14, evPill.Min.Y+70, colDark,
					truncateText(r.fonts.small, nextEvSub, evPill.Dx()-28))
			}
		}

		if len(widgets) > 0 {
			gridLeft := leftMaxX + 16
			gridRight := w - 18
			gapX := 14
			gapY := 14
			cols := 2
			rows := 2
			cardW := (gridRight - gridLeft - gapX) / cols
			cardH := (contentBottom - 66 - gapY) / rows
			for i, wd := range widgets {
				row := i / cols
				col := i % cols
				x0 := gridLeft + col*(cardW+gapX)
				y0 := 66 + row*(cardH+gapY)
				r.drawStandbyWidgetCard(image.Rect(x0, y0, x0+cardW, y0+cardH), wd)
			}
		}
	} else {
		// Portrait vertical layout
		clockCard := image.Rect(18, 66, w-18, 310)
		clockFace := drawCardFrame(r.canvas, clockCard, 14, false)

		timeRect := image.Rect(clockFace.Min.X+10, clockFace.Min.Y+4, clockFace.Max.X-10, clockFace.Min.Y+128)
		drawCenteredText(r.canvas, r.fonts.clock64, timeRect, colBlack, now.Format("15:04"))

		drawDashedHLine(r.canvas, clockFace.Min.X+30, clockFace.Max.X-30, clockFace.Min.Y+134, 4, 4, colSubtleGray)

		dateRect := image.Rect(clockFace.Min.X+10, clockFace.Min.Y+140, clockFace.Max.X-10, clockFace.Min.Y+170)
		drawCenteredText(r.canvas, r.fonts.bold20, dateRect, colBlack,
			truncateText(r.fonts.bold20, formatDateLine(now, lang), dateRect.Dx()))

		if ws.Available && nextEvLine != "" {
			wLine := fmt.Sprintf("☀ %s: %.1f °C • %s", ws.LocationName, ws.Temperature, wsDesc)
			if len(ws.Daily) > 0 {
				wLine += fmt.Sprintf(" (%.0f°/%.0f°)", ws.Daily[0].TempMin, ws.Daily[0].TempMax)
			}
			wRect := image.Rect(clockFace.Min.X+10, clockFace.Min.Y+172, clockFace.Max.X-10, clockFace.Min.Y+198)
			drawCenteredText(r.canvas, r.fonts.bold16, wRect, colDark, truncateText(r.fonts.bold16, wLine, wRect.Dx()))

			evRect := image.Rect(clockFace.Min.X+10, clockFace.Min.Y+198, clockFace.Max.X-10, clockFace.Max.Y-6)
			drawCenteredText(r.canvas, r.fonts.small, evRect, colDark, truncateText(r.fonts.small, "• "+nextEvLine, evRect.Dx()))
		} else if ws.Available {
			wLine := fmt.Sprintf("☀ %s: %.1f °C • %s", ws.LocationName, ws.Temperature, wsDesc)
			if len(ws.Daily) > 0 {
				wLine += fmt.Sprintf(" (%s %.0f° / %.0f°)", strings.ToLower(tr.Today), ws.Daily[0].TempMin, ws.Daily[0].TempMax)
			}
			wRect := image.Rect(clockFace.Min.X+10, clockFace.Min.Y+178, clockFace.Max.X-10, clockFace.Max.Y-8)
			drawCenteredText(r.canvas, r.fonts.bold16, wRect, colDark, truncateText(r.fonts.bold16, wLine, wRect.Dx()))
		} else if nextEvLine != "" {
			evRect := image.Rect(clockFace.Min.X+10, clockFace.Min.Y+178, clockFace.Max.X-10, clockFace.Max.Y-8)
			drawCenteredText(r.canvas, r.fonts.bold16, evRect, colDark, truncateText(r.fonts.bold16, "• "+nextEvLine, evRect.Dx()))
		}

		// 2x2 Summary Cards for key sensors / switches
		gridTop := clockCard.Max.Y + 14
		if len(widgets) > 0 && contentBottom > gridTop+100 {
			padX := 18
			gapX := 14
			gapY := 14
			cols := 2
			rows := 2
			cardW := (w - 2*padX - gapX) / cols
			cardH := (contentBottom - gridTop - gapY) / rows
			for i, wd := range widgets {
				row := i / cols
				col := i % cols
				x0 := padX + col*(cardW+gapX)
				y0 := gridTop + row*(cardH+gapY)
				r.drawStandbyWidgetCard(image.Rect(x0, y0, x0+cardW, y0+cardH), wd)
			}
		}
	}

	// Bottom wake prompt pill
	wakePill := image.Rect(18, h-54, w-18, h-16)
	fillRoundedRect(r.canvas, wakePill, 10, colWhite)
	drawRoundedRect(r.canvas, wakePill, 10, 2, colBlack)
	drawCenteredText(r.canvas, r.fonts.bold16, wakePill, colBlack, tr.StandbyWakePrompt)

	// Entire screen wakes standby
	r.hits = append(r.hits, HitAction{
		Rect: image.Rect(0, 0, w, h),
		Type: ActionWakeStandby,
	})
}
