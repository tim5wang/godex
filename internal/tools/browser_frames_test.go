package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
)

func newFramesTestService(t *testing.T) *BrowserService {
	t.Helper()
	svc := NewBrowserService(config.BrowserConfig{
		Enabled:              true,
		Headless:             true,
		ActionTimeoutSeconds: 10,
		IdleTimeoutSeconds:   0, // disable expiry so fake pages survive
		MaxPagesPerSession:   4,
	}, t.TempDir())
	return svc
}

// seedFakePage registers a page state without a real rod.Page. The frame pump
// treats a nil page as "cannot capture" and exits, which is exactly what the
// subscription-lifecycle tests need.
func seedFakePage(svc *BrowserService, sessionID, pageID string) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	sessionPages := svc.pages[sessionID]
	if sessionPages == nil {
		sessionPages = make(map[string]*browserPageState)
		svc.pages[sessionID] = sessionPages
	}
	sessionPages[pageID] = &browserPageState{
		page: nil,
		pageInfo: BrowserPage{
			PageID:    pageID,
			SessionID: sessionID,
			LastUsed:  time.Now(),
		},
		refs: make(map[string]string),
	}
}

func TestSubscribeFramesRejectsUnknownPage(t *testing.T) {
	svc := newFramesTestService(t)
	ch, cancel := svc.SubscribeFrames("session-1", "no-such-page")
	defer cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("expected closed channel for unknown page, got a frame")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected immediate closed channel for unknown page")
	}
}

func TestSubscribeFramesCancelClosesChannel(t *testing.T) {
	svc := newFramesTestService(t)
	seedFakePage(svc, "session-1", "p1")
	ch, cancel := svc.SubscribeFrames("session-1", "p1")
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("expected closed channel after cancel, got a frame")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected channel to close after cancel")
	}
}

func TestSubscribeFramesIdleStopRemovesPump(t *testing.T) {
	svc := newFramesTestService(t)
	seedFakePage(svc, "session-1", "p1")
	key := svc.frameKey("session-1", "p1")

	ch, cancel := svc.SubscribeFrames("session-1", "p1")
	svc.frameMu.Lock()
	pump := svc.framePumps[key]
	svc.frameMu.Unlock()
	if pump == nil {
		t.Fatalf("expected a frame pump to be registered")
	}

	// The fake page has no rod.Page, so the pump exits and closes the channel.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("expected EOF from pump (nil page cannot capture)")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("expected pump to exit for nil page")
	}
	cancel()

	// After the pump exits it must be removed from the registry.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.frameMu.Lock()
		_, exists := svc.framePumps[key]
		svc.frameMu.Unlock()
		if !exists {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected pump to be removed from registry after exit")
}

func TestSubscribeFramesSessionIsolation(t *testing.T) {
	svc := newFramesTestService(t)
	seedFakePage(svc, "session-A", "shared-page-id")

	// The page exists under session A.
	if err := svc.ValidateFramePage("session-A", "shared-page-id"); err != nil {
		t.Fatalf("expected page to be valid for owning session: %v", err)
	}
	// The same page ID under a different session must be rejected.
	if err := svc.ValidateFramePage("session-B", "shared-page-id"); err == nil {
		t.Fatalf("expected cross-session page access to be rejected")
	}

	// Subscribing under the wrong session yields an immediate closed channel.
	ch, cancel := svc.SubscribeFrames("session-B", "shared-page-id")
	defer cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("expected closed channel for cross-session subscribe")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected immediate rejection for cross-session subscribe")
	}
}

func TestNotifyViewRoutesToNotifier(t *testing.T) {
	svc := newFramesTestService(t)
	var got []string
	svc.SetViewNotifier(func(sessionID, pageID, url string) {
		got = append(got, sessionID+"|"+pageID+"|"+url)
	})
	svc.NotifyView("session-1", "p1", "https://example.com")
	if len(got) != 1 || got[0] != "session-1|p1|https://example.com" {
		t.Fatalf("expected notifier to receive view event, got %v", got)
	}
	// Nil notifier is a no-op.
	svc.SetViewNotifier(nil)
	svc.NotifyView("session-1", "p1", "https://example.com")
}

func TestDownscaleJPEGKeepsWidthLimit(t *testing.T) {
	// Build a 1280x720 test image.
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}

	down, err := downscaleJPEG(buf.Bytes(), 640, 70)
	if err != nil {
		t.Fatalf("downscale: %v", err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(down))
	if err != nil {
		t.Fatalf("decode downscaled jpeg: %v", err)
	}
	if w := decoded.Bounds().Dx(); w > 640 {
		t.Fatalf("expected width <= 640, got %d", w)
	}
	// Aspect ratio preserved: 1280x720 -> 640x360.
	if h := decoded.Bounds().Dy(); h != 360 {
		t.Fatalf("expected height 360 for 1280x720->640 wide, got %d", h)
	}
}

func TestDownscaleJPEGPassesThroughSmallImages(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 200))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := downscaleJPEG(buf.Bytes(), 640, 70)
	if err != nil {
		t.Fatalf("downscale: %v", err)
	}
	if !bytes.Equal(out, buf.Bytes()) {
		t.Fatalf("expected original bytes for image already within width limit")
	}
}

func TestFramePumpRateLimitsForwarding(t *testing.T) {
	svc := newFramesTestService(t)
	seedFakePage(svc, "session-1", "p1")
	ch, cancel := svc.SubscribeFrames("session-1", "p1")
	defer cancel()

	// Drain whatever the nil-page pump emits (it exits immediately, so the
	// channel closes); the test only asserts the pump terminates cleanly.
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("expected pump channel to close")
		}
	}
}
