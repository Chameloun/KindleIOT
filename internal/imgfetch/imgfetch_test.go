package imgfetch

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"kindleiot/internal/config"
)

func TestRenderToGrayDitheringAndContainCover(t *testing.T) {
	// Horizontal gradient 40x20
	src := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			v := uint8((x * 255) / 39)
			src.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}

	// Contain into square 40x40 -> top/bottom bars must remain white (255), pixels quantized to {0,85,170,255}
	gray := RenderToGray(src, 40, 40, "contain", true)
	if gray.Bounds().Dx() != 40 || gray.Bounds().Dy() != 40 {
		t.Fatalf("unexpected bounds: %v", gray.Bounds())
	}
	if gray.GrayAt(0, 0).Y != 255 {
		t.Fatalf("expected white letterbox padding in contain mode, got %d", gray.GrayAt(0, 0).Y)
	}
	for _, p := range gray.Pix {
		if p != 0 && p != 85 && p != 170 && p != 255 {
			t.Fatalf("expected Floyd-Steinberg quantized pixel in {0,85,170,255}, got %d", p)
		}
	}

	// Cover into 40x40 center-crops middle 20x20 slice (x=10..29 of 0..39 => ~65..190) with no letterbox padding
	coverGray := RenderToGray(src, 40, 40, "cover", false)
	left, right := coverGray.GrayAt(0, 0).Y, coverGray.GrayAt(39, 0).Y
	if left < 50 || left > 80 || right < 175 || right > 205 || coverGray.GrayAt(0, 39).Y != left {
		t.Fatalf("expected cover mode to center-crop gradient across entire height, got left=%d right=%d bottomLeft=%d",
			left, right, coverGray.GrayAt(0, 39).Y)
	}
}

func TestImageServiceFetchAuthAndTrigger(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 24, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 24; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 120, G: 120, B: 120, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatal(err)
	}
	var jpgBuf bytes.Buffer
	if err := jpeg.Encode(&jpgBuf, src, nil); err != nil {
		t.Fatal(err)
	}

	var (
		mu   sync.Mutex
		hits int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "cam" || p != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		hits++
		cur := hits
		mu.Unlock()
		if cur%2 == 1 {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBuf.Bytes())
		} else {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpgBuf.Bytes())
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		Pages: []config.PageConfig{
			{
				ID: "p1",
				Widgets: []config.WidgetConfig{
					{
						ID:                "cam1",
						Type:              "image",
						Label:             "Doorbell",
						ImageURL:          srv.URL,
						ImageAuthUser:     "cam",
						ImageAuthPass:     "secret",
						ImageTriggerTopic: "cam/ring",
						ImageIntervalSec:  600,
					},
				},
			},
		},
	}

	updCh := make(chan struct{}, 8)
	svc := NewService(cfg, func() {
		select {
		case updCh <- struct{}{}:
		default:
		}
	})
	svc.SetHTTPClient(srv.Client())
	svc.Start()
	defer svc.Stop()

	select {
	case <-updCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for initial image fetch")
	}

	img1, ts1, errStr := svc.GetRendered("cam1", 100, 80, "contain", true)
	if img1 == nil || ts1.IsZero() || errStr != "" {
		t.Fatalf("expected rendered image on first fetch, img=%v ts=%v err=%q", img1 != nil, ts1, errStr)
	}
	// Calling again with identical params returns same cached pointer.
	imgCached, _, _ := svc.GetRendered("cam1", 100, 80, "contain", true)
	if imgCached != img1 {
		t.Fatalf("expected cached rendered *image.Gray pointer reuse")
	}

	// MQTT trigger fetches fresh JPEG frame.
	svc.HandleTopicMessage("default", "cam/ring", "1")
	select {
	case <-updCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for MQTT-triggered image refresh")
	}
	mu.Lock()
	gotHits := hits
	mu.Unlock()
	if gotHits < 2 {
		t.Fatalf("expected at least 2 HTTP fetches after MQTT trigger, got %d", gotHits)
	}
}
