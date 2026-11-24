package imgfetch

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"

	"kindleiot/internal/config"
	"kindleiot/internal/mqtt"
)

//go:embed chmi_border.png
var chmiBorderPNG []byte

const chmiUnderlayURL = "https://produkty.chmi.cz/radar/und/pacz2gmaps9.und3.png"

var (
	chmiBorderOnce sync.Once
	chmiBorderImg  image.Image
)

func loadCHMIBorder() image.Image {
	chmiBorderOnce.Do(func() {
		if len(chmiBorderPNG) == 0 {
			return
		}
		img, _, err := image.Decode(bytes.NewReader(chmiBorderPNG))
		if err != nil {
			return
		}
		// Pre-pool 2720x1840 (4x resolution) down to crisp 680x460 1px dark lines so thin borders
		// are never washed out by bilinear downsampling on e-ink.
		b := img.Bounds()
		if b.Dx() >= 1360 && b.Dy() >= 920 {
			stepX := maxInt(1, b.Dx()/680)
			stepY := maxInt(1, b.Dy()/460)
			dw := b.Dx() / stepX
			dh := b.Dy() / stepY
			pooled := image.NewRGBA(image.Rect(0, 0, dw, dh))
			for y := 0; y < dh; y++ {
				for x := 0; x < dw; x++ {
					var bestA uint32
					var bestY uint32 = 255
					for sy := 0; sy < stepY; sy++ {
						for sx := 0; sx < stepX; sx++ {
							r, g, bl, a := img.At(b.Min.X+x*stepX+sx, b.Min.Y+y*stepY+sy).RGBA()
							if a > 0x4000 {
								lum := (299*r + 587*g + 114*bl) / 1000 >> 8
								if a > bestA {
									bestA = a
								}
								if lum < bestY {
									bestY = lum
								}
							}
						}
					}
					if bestA > 0x4000 {
						// Make Czech borders crisp dark gray/black for high contrast on e-ink
						v := uint8(minInt(60, int(bestY)))
						pooled.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
					}
				}
			}
			chmiBorderImg = pooled
			return
		}
		chmiBorderImg = img
	})
	return chmiBorderImg
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type cachedEntry struct {
	widget        config.WidgetConfig
	decoded       image.Image
	fetchedAt     time.Time
	lastAttemptAt time.Time
	lastError     string

	rendered     *image.Gray
	renderW      int
	renderH      int
	renderFit    string
	renderDither bool
	renderedAt   time.Time
}

// Service periodically or on-demand downloads images (weather radar, cameras, photos)
// and scales + Floyd-Steinberg dithers them to e-ink grayscale.
type Service struct {
	mu            sync.RWMutex
	cfg           *config.Config
	primaryBroker string
	client        *http.Client
	entries       map[string]*cachedEntry
	chmiUnderlay  image.Image
	chmiUndTried  bool
	onUpdate      func()
	stopCh        chan struct{}
}

// NewService creates an e-ink image fetching & dithering service.
func NewService(cfg *config.Config, onUpdate func()) *Service {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    mqtt.RootCAPool(),
			MinVersion: tls.VersionTLS12,
		},
	}
	s := &Service{
		cfg: cfg,
		client: &http.Client{
			Transport: tr,
			Timeout:   15 * time.Second,
		},
		entries:  make(map[string]*cachedEntry),
		onUpdate: onUpdate,
	}
	if cfg != nil {
		s.primaryBroker = cfg.PrimaryBrokerID()
	} else {
		s.primaryBroker = "default"
	}
	s.syncWidgetsLocked(cfg)
	return s
}

// SetHTTPClient overrides the HTTP client (primarily for unit tests).
func (s *Service) SetHTTPClient(c *http.Client) {
	if c == nil {
		return
	}
	s.mu.Lock()
	s.client = c
	s.mu.Unlock()
}

func collectImageWidgets(cfg *config.Config) []config.WidgetConfig {
	if cfg == nil {
		return nil
	}
	var out []config.WidgetConfig
	for _, p := range cfg.Pages {
		for _, w := range p.Widgets {
			if w.Type == "image" && w.ID != "" {
				out = append(out, w)
			}
		}
		if p.Floorplan != nil {
			for _, pin := range p.Floorplan.Pins {
				if pin.Widget.Type == "image" && pin.Widget.ID != "" {
					out = append(out, pin.Widget)
				}
			}
		}
	}
	return out
}

func (s *Service) syncWidgetsLocked(cfg *config.Config) []string {
	if cfg != nil {
		s.primaryBroker = cfg.PrimaryBrokerID()
	}
	widgets := collectImageWidgets(cfg)
	active := make(map[string]struct{}, len(widgets))
	var fetchNow []string

	for _, w := range widgets {
		active[w.ID] = struct{}{}
		cur, ok := s.entries[w.ID]
		if !ok {
			s.entries[w.ID] = &cachedEntry{widget: w}
			if strings.TrimSpace(w.ImageURL) != "" {
				fetchNow = append(fetchNow, w.ID)
			}
			continue
		}
		urlChanged := cur.widget.ImageURL != w.ImageURL ||
			cur.widget.ImageAuthUser != w.ImageAuthUser ||
			cur.widget.ImageAuthPass != w.ImageAuthPass
		cur.widget = w
		if urlChanged {
			cur.decoded = nil
			cur.rendered = nil
			cur.lastError = ""
			if strings.TrimSpace(w.ImageURL) != "" {
				fetchNow = append(fetchNow, w.ID)
			}
		}
	}
	for id := range s.entries {
		if _, ok := active[id]; !ok {
			delete(s.entries, id)
		}
	}
	return fetchNow
}

// Start launches background periodic image fetching (checked every 5 seconds).
func (s *Service) Start() {
	s.mu.Lock()
	if s.stopCh != nil {
		s.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	s.stopCh = stopCh
	var initialIDs []string
	for id, ent := range s.entries {
		if strings.TrimSpace(ent.widget.ImageURL) != "" {
			initialIDs = append(initialIDs, id)
		}
	}
	s.mu.Unlock()

	go func() {
		for _, id := range initialIDs {
			s.fetchWidget(id)
		}
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-tick.C:
				s.pollDue(time.Now())
			}
		}
	}()
}

// Stop shuts down background image fetching.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	s.mu.Unlock()
}

// Reconfigure updates tracked image widgets and immediately fetches any new or changed URLs.
func (s *Service) Reconfigure(cfg *config.Config) {
	s.mu.Lock()
	s.cfg = cfg
	fetchNow := s.syncWidgetsLocked(cfg)
	s.mu.Unlock()

	for _, id := range fetchNow {
		id := id
		go s.fetchWidget(id)
	}
}

// TriggerFetch immediately downloads one image widget (or all when widgetID is empty).
func (s *Service) TriggerFetch(widgetID string) {
	s.mu.RLock()
	var ids []string
	if widgetID != "" {
		if _, ok := s.entries[widgetID]; ok {
			ids = append(ids, widgetID)
		}
	} else {
		for id := range s.entries {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()

	for _, id := range ids {
		id := id
		go s.fetchWidget(id)
	}
}

// HandleTopicMessage refreshes any image widget whose ImageTriggerTopic matches topic.
func (s *Service) HandleTopicMessage(brokerID, topic, _ string) {
	if topic == "" {
		return
	}
	s.mu.RLock()
	primary := s.primaryBroker
	var ids []string
	for id, ent := range s.entries {
		trig := strings.TrimSpace(ent.widget.ImageTriggerTopic)
		if trig == "" || trig != topic {
			continue
		}
		wBroker := strings.TrimSpace(ent.widget.BrokerID)
		if wBroker == "" {
			wBroker = primary
		}
		bMatch := brokerID == "" || brokerID == wBroker
		if bMatch {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()

	for _, id := range ids {
		id := id
		go s.fetchWidget(id)
	}
}

func (s *Service) pollDue(now time.Time) {
	s.mu.RLock()
	var due []string
	for id, ent := range s.entries {
		if strings.TrimSpace(ent.widget.ImageURL) == "" {
			continue
		}
		interval := ent.widget.ImageIntervalSec
		if interval <= 0 {
			interval = 300
		}
		if ent.lastAttemptAt.IsZero() || now.Sub(ent.lastAttemptAt) >= time.Duration(interval)*time.Second {
			due = append(due, id)
		}
	}
	s.mu.RUnlock()

	for _, id := range due {
		s.fetchWidget(id)
	}
}

func (s *Service) fetchWidget(widgetID string) {
	s.mu.Lock()
	ent, ok := s.entries[widgetID]
	if !ok {
		s.mu.Unlock()
		return
	}
	w := ent.widget
	urlStr := strings.TrimSpace(w.ImageURL)
	if urlStr == "" {
		s.mu.Unlock()
		return
	}
	ent.lastAttemptAt = time.Now()
	client := s.client
	cb := s.onUpdate
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		s.setWidgetError(widgetID, err.Error(), cb)
		return
	}
	req.Header.Set("User-Agent", "KindleIOT/1.0")
	if w.ImageAuthUser != "" || w.ImageAuthPass != "" {
		req.SetBasicAuth(w.ImageAuthUser, w.ImageAuthPass)
	}

	resp, err := client.Do(req)
	if err != nil {
		s.setWidgetError(widgetID, err.Error(), cb)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.setWidgetError(widgetID, fmt.Sprintf("HTTP %d", resp.StatusCode), cb)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		s.setWidgetError(widgetID, err.Error(), cb)
		return
	}
	resolvedURL := urlStr
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") || bytes.HasPrefix(bytes.TrimSpace(raw), []byte("<")) {
		var rerr error
		resolvedURL, rerr = resolveDirectoryIndexImage(resp.Request.URL, raw)
		if rerr != nil {
			s.setWidgetError(widgetID, rerr.Error(), cb)
			return
		}
		req2, err := http.NewRequestWithContext(ctx, http.MethodGet, resolvedURL, nil)
		if err != nil {
			s.setWidgetError(widgetID, err.Error(), cb)
			return
		}
		req2.Header.Set("User-Agent", "KindleIOT/1.0")
		if w.ImageAuthUser != "" || w.ImageAuthPass != "" {
			req2.SetBasicAuth(w.ImageAuthUser, w.ImageAuthPass)
		}
		resp2, err := client.Do(req2)
		if err != nil {
			s.setWidgetError(widgetID, err.Error(), cb)
			return
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			s.setWidgetError(widgetID, fmt.Sprintf("HTTP %d", resp2.StatusCode), cb)
			return
		}
		raw, err = io.ReadAll(io.LimitReader(resp2.Body, 8<<20))
		if err != nil {
			s.setWidgetError(widgetID, err.Error(), cb)
			return
		}
	}
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		s.setWidgetError(widgetID, "decode: "+err.Error(), cb)
		return
	}
	decoded = downscaleCap(decoded, 800)
	if isCHMIRadarURL(urlStr, resolvedURL) {
		decoded = s.composeCHMIRadar(ctx, client, decoded)
	}

	s.mu.Lock()
	if cur, exists := s.entries[widgetID]; exists {
		cur.decoded = decoded
		cur.fetchedAt = time.Now()
		cur.lastError = ""
		cur.rendered = nil
	}
	s.mu.Unlock()

	if cb != nil {
		cb()
	}
}

func (s *Service) setWidgetError(widgetID, msg string, cb func()) {
	s.mu.Lock()
	if cur, exists := s.entries[widgetID]; exists {
		cur.lastError = msg
	}
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// GetRendered returns a cached e-ink grayscale (optionally Floyd-Steinberg dithered) image sized dstW x dstH.
func (s *Service) GetRendered(widgetID string, dstW, dstH int, fit string, dither bool) (*image.Gray, time.Time, string) {
	if dstW <= 0 || dstH <= 0 {
		return nil, time.Time{}, ""
	}
	if !strings.EqualFold(fit, "cover") {
		fit = "contain"
	} else {
		fit = "cover"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ent, ok := s.entries[widgetID]
	if !ok {
		return nil, time.Time{}, ""
	}
	if ent.decoded == nil {
		return nil, ent.fetchedAt, ent.lastError
	}
	if ent.rendered != nil &&
		ent.renderW == dstW &&
		ent.renderH == dstH &&
		ent.renderFit == fit &&
		ent.renderDither == dither &&
		ent.renderedAt.Equal(ent.fetchedAt) {
		return ent.rendered, ent.fetchedAt, ent.lastError
	}
	gray := RenderToGray(ent.decoded, dstW, dstH, fit, dither)
	ent.rendered = gray
	ent.renderW = dstW
	ent.renderH = dstH
	ent.renderFit = fit
	ent.renderDither = dither
	ent.renderedAt = ent.fetchedAt
	return gray, ent.fetchedAt, ent.lastError
}

func downscaleCap(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim || w <= 0 || h <= 0 {
		return img
	}
	var nw, nh int
	if w >= h {
		nw = maxDim
		nh = int(math.Round(float64(h) * float64(maxDim) / float64(w)))
	} else {
		nh = maxDim
		nw = int(math.Round(float64(w) * float64(maxDim) / float64(h)))
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// RenderToGray scales src to dstW x dstH using contain or cover mode and optionally applies
// 4-level Floyd-Steinberg dithering ({0, 85, 170, 255}) tuned for e-ink panels.
func RenderToGray(src image.Image, dstW, dstH int, fit string, dither bool) *image.Gray {
	if src == nil || dstW <= 0 || dstH <= 0 {
		return image.NewGray(image.Rect(0, 0, maxInt(1, dstW), maxInt(1, dstH)))
	}
	srcB := src.Bounds()
	srcW, srcH := srcB.Dx(), srcB.Dy()
	if srcW <= 0 || srcH <= 0 {
		out := image.NewGray(image.Rect(0, 0, dstW, dstH))
		for i := range out.Pix {
			out.Pix[i] = 255
		}
		return out
	}

	rgba := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	xdraw.Draw(rgba, rgba.Bounds(), &image.Uniform{C: color.RGBA{R: 255, G: 255, B: 255, A: 255}}, image.Point{}, xdraw.Src)

	srcAspect := float64(srcW) / float64(srcH)
	dstAspect := float64(dstW) / float64(dstH)

	if strings.EqualFold(fit, "cover") {
		crop := srcB
		if srcAspect > dstAspect {
			newW := int(math.Round(float64(srcH) * dstAspect))
			if newW < 1 {
				newW = 1
			}
			xOff := (srcW - newW) / 2
			crop = image.Rect(srcB.Min.X+xOff, srcB.Min.Y, srcB.Min.X+xOff+newW, srcB.Max.Y)
		} else if srcAspect < dstAspect {
			newH := int(math.Round(float64(srcW) / dstAspect))
			if newH < 1 {
				newH = 1
			}
			yOff := (srcH - newH) / 2
			crop = image.Rect(srcB.Min.X, srcB.Min.Y+yOff, srcB.Max.X, srcB.Min.Y+yOff+newH)
		}
		xdraw.BiLinear.Scale(rgba, rgba.Bounds(), src, crop, xdraw.Over, nil)
	} else {
		var drawW, drawH int
		if srcAspect > dstAspect {
			drawW = dstW
			drawH = int(math.Round(float64(dstW) / srcAspect))
		} else {
			drawH = dstH
			drawW = int(math.Round(float64(dstH) * srcAspect))
		}
		if drawW < 1 {
			drawW = 1
		}
		if drawH < 1 {
			drawH = 1
		}
		ox := (dstW - drawW) / 2
		oy := (dstH - drawH) / 2
		dr := image.Rect(ox, oy, ox+drawW, oy+drawH)
		xdraw.BiLinear.Scale(rgba, dr, src, srcB, xdraw.Over, nil)
	}

	buf := make([]float64, dstW*dstH)
	for y := 0; y < dstH; y++ {
		rowStart := y * rgba.Stride
		for x := 0; x < dstW; x++ {
			off := rowStart + x*4
			r := float64(rgba.Pix[off])
			g := float64(rgba.Pix[off+1])
			b := float64(rgba.Pix[off+2])
			buf[y*dstW+x] = 0.299*r + 0.587*g + 0.114*b
		}
	}

	out := image.NewGray(image.Rect(0, 0, dstW, dstH))
	if !dither {
		for y := 0; y < dstH; y++ {
			for x := 0; x < dstW; x++ {
				v := clampFloat(buf[y*dstW+x], 0, 255)
				out.SetGray(x, y, color.Gray{Y: uint8(math.Round(v))})
			}
		}
		return out
	}

	// 4-level Floyd-Steinberg error diffusion -> {0, 85, 170, 255}
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			idx := y*dstW + x
			old := clampFloat(buf[idx], 0, 255)
			quant := math.Round(old/85.0) * 85.0
			quant = clampFloat(quant, 0, 255)
			out.SetGray(x, y, color.Gray{Y: uint8(quant)})

			err := old - quant
			if x+1 < dstW {
				buf[idx+1] += err * (7.0 / 16.0)
			}
			if y+1 < dstH {
				nextRow := (y + 1) * dstW
				if x-1 >= 0 {
					buf[nextRow+x-1] += err * (3.0 / 16.0)
				}
				buf[nextRow+x] += err * (5.0 / 16.0)
				if x+1 < dstW {
					buf[nextRow+x+1] += err * (1.0 / 16.0)
				}
			}
		}
	}
	return out
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var imgHrefRe = regexp.MustCompile(`(?i)href=["']([^"']+\.(?:png|jpe?g|gif))(?:\?[^"']*)?["']`)

// resolveDirectoryIndexImage parses an HTTP directory listing (such as opendata.chmi.cz)
// and returns the absolute URL of the latest (lexicographically last) image file.
func resolveDirectoryIndexImage(base *url.URL, html []byte) (string, error) {
	matches := imgHrefRe.FindAllSubmatch(html, -1)
	if len(matches) == 0 {
		return "", fmt.Errorf("no image links found in directory index")
	}
	links := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		href := strings.TrimSpace(string(m[1]))
		if href == "" || strings.HasPrefix(href, "../") {
			continue
		}
		links = append(links, href)
	}
	if len(links) == 0 {
		return "", fmt.Errorf("no image links found in directory index")
	}
	sort.Strings(links)
	latest := links[len(links)-1]
	if base == nil {
		return latest, nil
	}
	ref, err := url.Parse(latest)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

func isCHMIRadarURL(origURL, resolvedURL string) bool {
	u := strings.ToLower(origURL + " " + resolvedURL)
	return strings.Contains(u, "opendata.chmi.cz") || strings.Contains(u, "pacz2gmaps")
}

func (s *Service) getCHMIUnderlay(ctx context.Context, client *http.Client) image.Image {
	s.mu.RLock()
	tried := s.chmiUndTried
	cached := s.chmiUnderlay
	s.mu.RUnlock()
	if tried {
		return cached
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chmiUnderlayURL, nil)
	var decoded image.Image
	if err == nil && client != nil {
		req.Header.Set("User-Agent", "KindleIOT/1.0")
		if resp, derr := client.Do(req); derr == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 2<<20)); rerr == nil {
					if img, _, ierr := image.Decode(bytes.NewReader(raw)); ierr == nil {
						decoded = img
					}
				}
			}
		}
	}

	s.mu.Lock()
	s.chmiUndTried = true
	if decoded != nil {
		s.chmiUnderlay = decoded
	}
	out := s.chmiUnderlay
	s.mu.Unlock()
	return out
}

// composeCHMIRadar composites the Czech Republic districts/cities underlay and official crisp border
// beneath/above the transparent ČHMÚ radar precipitation overlay so cloud positions are easy to read.
func (s *Service) composeCHMIRadar(ctx context.Context, client *http.Client, radar image.Image) image.Image {
	b := radar.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return radar
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.RGBA{R: 255, G: 255, B: 255, A: 255}}, image.Point{}, xdraw.Src)

	if und := s.getCHMIUnderlay(ctx, client); und != nil {
		xdraw.BiLinear.Scale(dst, dst.Bounds(), und, und.Bounds(), xdraw.Over, nil)
		// Soften underlay slightly toward white so radar clouds stay high-contrast on e-ink
		for i := 0; i < len(dst.Pix); i += 4 {
			dst.Pix[i] = uint8((int(dst.Pix[i]) + 255) / 2)
			dst.Pix[i+1] = uint8((int(dst.Pix[i+1]) + 255) / 2)
			dst.Pix[i+2] = uint8((int(dst.Pix[i+2]) + 255) / 2)
		}
	}

	xdraw.Draw(dst, dst.Bounds(), radar, b.Min, xdraw.Over)

	if border := loadCHMIBorder(); border != nil {
		xdraw.NearestNeighbor.Scale(dst, dst.Bounds(), border, border.Bounds(), xdraw.Over, nil)
	}
	return dst
}
