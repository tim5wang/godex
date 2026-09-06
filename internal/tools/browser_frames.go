package tools

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"log"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// BrowserFrame is one downsampled JPEG frame of a browser page plus the page
// identity/metadata that goes with it. Sent over the frame-stream WebSocket.
type BrowserFrame struct {
	PageID string `json:"page_id"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
	JPEG   []byte `json:"jpeg"`
}

const (
	// frameMaxWidth is the downsampling target width for streamed frames.
	frameMaxWidth = 640
	// frameJPEGQuality is the JPEG quality used for streamed frames.
	frameJPEGQuality = 70
	// frameMinInterval is the minimum interval between forwarded frames
	// (rate limit: aim for ~1-2 fps, never spam the CDP bus).
	frameMinInterval = 500 * time.Millisecond
)

// defaultFrameIdleTimeout stops the capture loop when no subscriber has been
// attached for this long (idle auto-stop). Exposed as a var so tests can
// shorten it.
var defaultFrameIdleTimeout = 30 * time.Second

// framePump streams frames for one (session, page) pair to all attached
// subscribers. It prefers CDP screencast (Page.startScreencast, native JPEG
// frames) and falls back to a 1-2 fps captureScreenshot loop when screencast
// is unavailable.
type framePump struct {
	service   *BrowserService
	sessionID string
	pageID    string

	mu         sync.Mutex
	subs       map[chan BrowserFrame]struct{}
	lastSent   time.Time
	lastActive time.Time
	started    bool
	done       chan struct{}
}

func (s *BrowserService) frameKey(sessionID, pageID string) string {
	return sessionID + "\x00" + pageID
}

// SetViewNotifier installs a callback that receives browser.view events
// (sessionID, pageID, url) whenever the browser tool operates on a page.
// The backend wires this to the session event stream for frontend
// auto-activation of the Browser panel.
func (s *BrowserService) SetViewNotifier(fn func(sessionID, pageID, url string)) {
	s.mu.Lock()
	s.viewNotifier = fn
	s.mu.Unlock()
}

// ValidateFramePage reports whether a page exists in a session. It is the
// session-ownership gate for the frame-stream WS endpoint: a page belongs to
// exactly one session, so requesting another session's page ID fails here and
// prevents cross-session peeking.
func (s *BrowserService) ValidateFramePage(sessionID, pageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.pageStateLocked(sessionID, pageID)
	return err
}

// NotifyView reports that the browser tool operated on a page. It is called
// by the tool layer after successful page-affecting actions; the notifier is
// optional (nil is a no-op).
func (s *BrowserService) NotifyView(sessionID, pageID, url string) {
	s.mu.Lock()
	fn := s.viewNotifier
	s.mu.Unlock()
	if fn != nil {
		fn(sessionID, pageID, url)
	}
}

// SubscribeFrames returns a channel of downsampled JPEG frames for a page in a
// session, plus a cancel func. The stream is per (session, page): the caller
// must already be authorized for the session (session ownership is enforced at
// the WS layer). Frames stop automatically when idle for frameIdleTimeout.
func (s *BrowserService) SubscribeFrames(sessionID, pageID string) (<-chan BrowserFrame, func()) {
	s.mu.Lock()
	if _, err := s.pageStateLocked(sessionID, pageID); err != nil {
		s.mu.Unlock()
		// Invalid or unknown page: return a closed channel so consumers see
		// an immediate EOF instead of hanging.
		closed := make(chan BrowserFrame)
		close(closed)
		return closed, func() {}
	}
	s.mu.Unlock()

	key := s.frameKey(sessionID, pageID)
	s.frameMu.Lock()
	pump := s.framePumps[key]
	if pump != nil {
		select {
		case <-pump.done:
			pump = nil // stale pump (idle/closed): start a fresh one
		default:
		}
	}
	if pump == nil {
		pump = &framePump{
			service:   s,
			sessionID: sessionID,
			pageID:    pageID,
			subs:      make(map[chan BrowserFrame]struct{}),
			done:      make(chan struct{}),
		}
		s.framePumps[key] = pump
	}
	ch := make(chan BrowserFrame, 4)
	pump.mu.Lock()
	pump.subs[ch] = struct{}{}
	pump.lastActive = time.Now()
	start := !pump.started
	pump.started = true
	pump.mu.Unlock()
	if start {
		go pump.run()
	}
	s.frameMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.frameMu.Lock()
			pump.mu.Lock()
			if _, ok := pump.subs[ch]; ok {
				delete(pump.subs, ch)
				close(ch)
			}
			pump.mu.Unlock()
			s.frameMu.Unlock()
		})
	}
	return ch, cancel
}

// run drives the frame pump until idle-stop, page close, or service shutdown.
func (p *framePump) run() {
	defer func() {
		// Remove from the registry first, then close the remaining subscriber
		// channels: a concurrent SubscribeFrames either sees the pump (stale,
		// via done) or creates a fresh one after the registry removal.
		p.service.frameMu.Lock()
		delete(p.service.framePumps, p.frameKey())
		p.service.frameMu.Unlock()
		p.mu.Lock()
		for ch := range p.subs {
			close(ch)
		}
		p.subs = make(map[chan BrowserFrame]struct{})
		p.mu.Unlock()
		close(p.done)
	}()

	// Prefer CDP screencast (native, low overhead, headless-friendly).
	if p.runScreencast() {
		return
	}
	// Fallback: periodic captureScreenshot at ~1-2 fps.
	p.runScreenshotLoop()
}

func (p *framePump) frameKey() string { return p.service.frameKey(p.sessionID, p.pageID) }

// runScreencast starts Page.startScreencast and forwards native JPEG frames.
// Returns true when the stream ran to completion (or stopped), false when
// screencast could not be started and the caller should fall back.
func (p *framePump) runScreencast() bool {
	state, _, err := p.service.pageState(p.sessionID, p.pageID)
	if err != nil {
		return false
	}
	state.mu.Lock()
	page := state.page
	state.mu.Unlock()
	if page == nil {
		return false
	}

	quality := frameJPEGQuality
	maxWidth := frameMaxWidth
	start := proto.PageStartScreencast{
		Format:   proto.PageStartScreencastFormatJpeg,
		Quality:  &quality,
		MaxWidth: &maxWidth,
	}
	// Bound the screencast start: rod calls without a deadline can hang on
	// some headless builds, which would stall the pump before the first-frame
	// guard below ever runs. Mirror the per-action timeout used by every other
	// browser operation (state.page.Context(actionCtx)).
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := start.Call(page.Context(callCtx)); err != nil {
		log.Printf("framePump %s: startScreencast failed: %v (fallback to screenshots)", p.frameKey(), err)
		return false
	}
	defer func() { _ = proto.PageStopScreencast{}.Call(page) }()

	// Screencast frames arrive as Page.screencastFrame events. The channel is
	// closed by rod when the page is closed or the underlying browser session
	// ends, which is how we learn the page is gone.
	frameCh := page.Event()
	idleTicker := time.NewTicker(time.Second)
	defer idleTicker.Stop()
	// First-frame guard: some headless Chromium builds accept
	// Page.startScreencast but never deliver a single frame event. If the
	// first frame does not arrive within the grace period, stop screencast and
	// return false so the caller falls back to the screenshot loop — without
	// this guard the pump blocks forever and the frame stream never starts.
	firstFrameTimer := time.NewTimer(3 * time.Second)
	defer firstFrameTimer.Stop()
	for {
		select {
		case <-idleTicker.C:
			if p.idleExceeded() {
				log.Printf("framePump %s: screencast idle stop", p.frameKey())
				return true
			}
		case <-firstFrameTimer.C:
			// No frame within the grace period: screencast is not producing
			// frames on this browser — fall back to captureScreenshot.
			log.Printf("framePump %s: screencast no first frame within 3s, falling back", p.frameKey())
			return false
		case msg, ok := <-frameCh:
			if !ok {
				// Headless Chromium accepts Page.startScreencast but may never
				// deliver screencastFrame events — the rod event channel closes
				// immediately. That is NOT "page gone": treat it as screencast
				// unavailable and fall back to the screenshot loop (return false),
				// otherwise run() returns early and the frame stream dies with
				// a 1006 close right after the WS handshake.
				log.Printf("framePump %s: screencast event channel closed, falling back to screenshots", p.frameKey())
				return false
			}
			var frame proto.PageScreencastFrame
			if !msg.Load(&frame) {
				continue
			}
			// Ack is required for Chrome to keep delivering frames.
			_ = proto.PageScreencastFrameAck{SessionID: frame.SessionID}.Call(page)
			p.forward(BrowserFrame{
				PageID: p.pageID,
				JPEG:   frame.Data,
			})
			// First frame delivered: screencast works, disarm the guard.
			if !firstFrameTimer.Stop() {
				select {
				case <-firstFrameTimer.C:
				default:
				}
			}
		}
	}
}

// runScreenshotLoop captures a downsampled screenshot every ~500ms-1s.
func (p *framePump) runScreenshotLoop() {
	state, _, err := p.service.pageState(p.sessionID, p.pageID)
	if err != nil {
		log.Printf("framePump %s: screenshot loop pageState error: %v", p.frameKey(), err)
		return
	}
	state.mu.Lock()
	page := state.page
	state.mu.Unlock()
	if page == nil {
		log.Printf("framePump %s: screenshot loop page is nil", p.frameKey())
		return
	}

	ticker := time.NewTicker(frameMinInterval)
	defer ticker.Stop()
	var lastErrLog time.Time
	for {
		select {
		case <-ticker.C:
			if p.idleExceeded() {
				return
			}
			quality := frameJPEGQuality
			// Bound each capture like every other browser operation; a bare
			// page has no deadline and a wedged CDP screenshot would stall
			// the pump forever with no frames forwarded.
			shotCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			data, err := page.Context(shotCtx).Screenshot(false, &proto.PageCaptureScreenshot{
				Format:  proto.PageCaptureScreenshotFormatJpeg,
				Quality: &quality,
			})
			cancel()
			if err != nil {
				// Log at most once per 5s so a persistent failure is visible
				// in the service log without spamming every 500ms.
				if time.Since(lastErrLog) > 5*time.Second {
					log.Printf("framePump %s: screenshot failed: %v", p.frameKey(), err)
					lastErrLog = time.Now()
				}
				continue
			}
			down, err := downscaleJPEG(data, frameMaxWidth, frameJPEGQuality)
			if err != nil {
				continue
			}
			p.forward(BrowserFrame{PageID: p.pageID, JPEG: down})
		}
	}
}

// idleExceeded reports whether no subscriber has been attached for longer than
// the idle timeout (the pump should stop capturing).
func (p *framePump) idleExceeded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.subs) > 0 {
		return false
	}
	return time.Since(p.lastActive) > defaultFrameIdleTimeout
}

// forward pushes a frame to subscribers if the rate limit allows it.
func (p *framePump) forward(frame BrowserFrame) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.subs) == 0 {
		return
	}
	now := time.Now()
	if now.Sub(p.lastSent) < frameMinInterval {
		return
	}
	p.lastSent = now
	p.lastActive = now
	for ch := range p.subs {
		select {
		case ch <- frame:
		default:
			// Slow consumer: drop this frame rather than block the pump.
		}
	}
}

// downscaleJPEG decodes a JPEG, scales it down to at most maxWidth (keeping
// aspect ratio) and re-encodes at the given quality. Returns the original
// bytes untouched when the image is already within the width limit.
func downscaleJPEG(data []byte, maxWidth, quality int) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	if bounds.Dx() <= maxWidth {
		return data, nil
	}
	height := bounds.Dy() * maxWidth / bounds.Dx()
	if height < 1 {
		height = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, maxWidth, height))
	// Simple nearest-neighbour downscale (good enough for preview frames).
	sx := float64(bounds.Dx()) / float64(maxWidth)
	sy := float64(bounds.Dy()) / float64(height)
	for y := 0; y < height; y++ {
		srcY := bounds.Min.Y + int(float64(y)*sy)
		for x := 0; x < maxWidth; x++ {
			srcX := bounds.Min.X + int(float64(x)*sx)
			dst.Set(x, y, img.At(srcX, srcY))
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
