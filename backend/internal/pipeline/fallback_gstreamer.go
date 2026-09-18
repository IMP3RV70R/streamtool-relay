//go:build gstreamer

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

// Raw selection feeds one continuous encoder. GStreamer owns the output clock,
// keyframes and timestamp continuity; uploaded clips are decoded directly.
type rawSwitcher struct {
	mu                           sync.Mutex
	policy                       SlatePolicy
	lastSource                   time.Time
	videoSelector, audioSelector gst.Element
}

func (r *gstRuntime) startFallback(parent context.Context) error {
	m := r.spec.MediaProfile
	if err := m.Validate(); err != nil {
		return err
	}
	caps := fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,framerate=%d/%d", m.Width, m.Height, m.FPSNum, m.FPSDen)
	queue := "queue max-size-bytes=8388608 max-size-time=300000000 max-size-buffers=4 leaky=downstream"
	output := fmt.Sprintf(`input-selector name=video_select sync-streams=true sync-mode=clock cache-buffers=true drop-backwards=true ! %s ! x264enc threads=2 tune=zerolatency speed-preset=veryfast bitrate=%d key-int-max=%d pass=cbr nal-hrd=cbr option-string=force-cfr=1 ! h264parse config-interval=-1 ! video/x-h264,stream-format=avc,alignment=au ! tee name=video_tee
 intervideosrc channel=fallback timeout=10000000000 ! %s ! %s ! video_select.sink_0
 intervideosrc channel=source timeout=3000000000 ! %s ! %s ! video_select.sink_1
 videotestsrc is-live=true pattern=black ! %s ! %s ! video_select.sink_2
 input-selector name=audio_select sync-streams=true sync-mode=clock cache-buffers=true drop-backwards=true ! audioconvert ! audio/x-raw,format=S16LE,rate=48000,channels=2 ! voaacenc bitrate=%d ! aacparse ! audio/mpeg,mpegversion=4,stream-format=raw ! tee name=audio_tee
 audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! queue max-size-time=300000000 max-size-bytes=1048576 max-size-buffers=32 leaky=downstream ! audio_select.sink_0
 interaudiosrc channel=source ! audio/x-raw,rate=48000,channels=2 ! queue max-size-time=300000000 max-size-bytes=1048576 max-size-buffers=32 leaky=downstream ! audio_select.sink_1`, queue, m.VideoKbps, (2*m.FPSNum+m.FPSDen-1)/m.FPSDen, caps, queue, caps, queue, caps, queue, m.AudioKbps*1000)
	e, err := gst.ParseLaunch(output)
	if err != nil {
		return errors.New("create common encoder")
	}
	p, ok := e.(gst.Pipeline)
	if !ok {
		return errors.New("create common encoder pipeline")
	}
	r.pipeline = p
	r.videoTee, r.audioTee = p.GetByName("video_tee"), p.GetByName("audio_tee")
	r.raw = &rawSwitcher{policy: r.spec.SlatePolicy, videoSelector: p.GetByName("video_select"), audioSelector: p.GetByName("audio_select")}
	r.raw.selectMode("slate")
	for _, d := range r.spec.Destinations {
		if err = r.addBranchLocked(d); err != nil {
			p.SetState(gst.StateNull)
			return err
		}
	}
	input := `filesrc name=asset ! pngdec ! imagefreeze is-live=true`
	path := r.spec.FallbackImage
	if r.spec.FallbackVideo != "" {
		input = `filesrc name=asset ! qtdemux ! h264parse ! openh264dec`
		path = r.spec.FallbackVideo
	}
	e, err = gst.ParseLaunch(fmt.Sprintf(`%s ! videoconvert ! videoscale ! videorate ! %s ! intervideosink channel=fallback sync=true`, input, caps))
	if err != nil {
		p.SetState(gst.StateNull)
		return errors.New("create fallback decoder")
	}
	fallback, ok := e.(gst.Pipeline)
	if !ok {
		p.SetState(gst.StateNull)
		return errors.New("create fallback pipeline")
	}
	fallback.GetByName("asset").SetObjectProperty("location", path)
	if fallback.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		fallback.SetState(gst.StateNull)
		p.SetState(gst.StateNull)
		return errors.New("start fallback decoder")
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	if p.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		cancel()
		fallback.SetState(gst.StateNull)
		p.SetState(gst.StateNull)
		return errors.New("start common encoder")
	}
	go r.watchBus(ctx)
	r.fallbackDone = make(chan struct{})
	go func() {
		defer close(r.fallbackDone)
		defer fallback.BlockSetState(gst.StateNull, gst.ClockTime(3*time.Second))
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); r.rawSourceLoop(ctx, caps) }()
		r.rawLoop(ctx, fallback)
		cancel()
		wg.Wait()
	}()
	return nil
}

func (s *rawSwitcher) selectMode(mode string) {
	videoPad, audioPad := "sink_0", "sink_0"
	if mode == "source" {
		videoPad, audioPad = "sink_1", "sink_1"
	}
	if mode == "none" {
		videoPad = "sink_2"
	}
	s.videoSelector.SetObjectProperty("active-pad", s.videoSelector.GetStaticPad(videoPad))
	s.audioSelector.SetObjectProperty("active-pad", s.audioSelector.GetStaticPad(audioPad))
}
func (r *gstRuntime) rawLoop(ctx context.Context, fallback gst.Pipeline) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	mode := ""
	var previousLive, previousForced bool
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for msg := fallback.GetBus().Pop(); msg != nil; msg = fallback.GetBus().Pop() {
				if msg.Type() == gst.MessageError {
					r.publish(RuntimeEvent{Type: RuntimeInputError, Err: errors.New("fallback decoding failed")})
					return
				}
				if msg.Type() == gst.MessageEOS && !fallback.SeekSimple(gst.FormatTime, gst.SeekFlagFlush|gst.SeekFlagKeyUnit, 0) {
					r.publish(RuntimeEvent{Type: RuntimeInputError, Err: errors.New("fallback loop failed")})
					return
				}
			}
			r.raw.mu.Lock()
			live := !r.raw.lastSource.IsZero() && now.Sub(r.raw.lastSource) < 2*time.Second
			policy := r.raw.policy
			r.raw.mu.Unlock()
			next := "source"
			if policy.Forced || !live {
				next = "none"
				if policy.Forced || policy.OnSourceLoss {
					next = "slate"
				}
			}
			if next != mode || live != previousLive || policy.Forced != previousForced {
				r.raw.selectMode(next)
				kind := RuntimeInputLive
				switch next {
				case "slate":
					kind = RuntimeFallback
				case "none":
					kind = RuntimeInputUnavailable
				}
				r.publish(RuntimeEvent{Type: kind, Forced: policy.Forced, InputLive: live})
				mode, previousLive, previousForced = next, live, policy.Forced
			}
		}
	}
}
func (r *gstRuntime) rawSourceLoop(ctx context.Context, caps string) {
	for ctx.Err() == nil {
		r.rawSourceAttempt(ctx, caps)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func (r *gstRuntime) rawSourceAttempt(ctx context.Context, caps string) {
	defer func() { r.raw.mu.Lock(); r.raw.lastSource = time.Time{}; r.raw.mu.Unlock() }()
	dns, cancel := context.WithTimeout(ctx, 3*time.Second)
	location, err := resolveSource(dns, r.spec.SourceLocation, (&net.Resolver{PreferGo: true}).LookupIPAddr)
	cancel()
	if err != nil {
		return
	}
	e, err := gst.ParseLaunch(fmt.Sprintf(`srtsrc name=source_source ! tsdemux name=demux
 demux. ! video/x-h264 ! queue max-size-bytes=8388608 max-size-time=3000000000 ! h264parse name=bounded_video ! openh264dec ! videoconvert ! videoscale ! videorate ! %s ! identity name=fresh_video ! intervideosink channel=source sync=false async=false
 demux. ! audio/mpeg,mpegversion=4 ! queue max-size-bytes=1048576 max-size-time=3000000000 ! aacparse ! faad ! audioconvert ! audioresample ! audio/x-raw,rate=48000,channels=2 ! interaudiosink channel=source sync=false async=false`, caps))
	if err != nil {
		return
	}
	p, ok := e.(gst.Pipeline)
	if !ok {
		return
	}
	defer p.BlockSetState(gst.StateNull, gst.ClockTime(3*time.Second))
	p.GetByName("source_source").SetObjectProperty("uri", location)
	var rejected atomic.Bool
	reject := func() {
		if rejected.CompareAndSwap(false, true) {
			r.publish(RuntimeEvent{Type: RuntimeInputRejected})
		}
	}
	p.GetByName("demux").ConnectPadAdded(func(_ gst.Element, pad gst.Pad) {
		caps := pad.GetCurrentCaps()
		if caps == nil || caps.GetSize() == 0 {
			return
		}
		name := caps.GetStructure(0).GetName()
		if strings.HasPrefix(name, "video/") && name != "video/x-h264" {
			reject()
		}
	})
	// Inspect parsed SPS/caps before allocating decoded frames. Different supported
	// input/output dimensions are fine; oversized/unsupported inputs are rejected.
	p.GetByName("bounded_video").GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(pad gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		if rejected.Load() {
			return gst.PadProbeDrop
		}
		caps := pad.GetCurrentCaps()
		if caps == nil || caps.GetSize() == 0 {
			return gst.PadProbeDrop
		}
		structure := caps.GetStructure(0)
		width, wok := structure.GetInt("width")
		height, hok := structure.GetInt("height")
		numerator, denominator, fok := structure.GetFraction("framerate")
		if wok && hok && !supportedInputVideo(int(width), int(height), int(numerator), int(denominator), fok) {
			reject()
			return gst.PadProbeDrop
		}
		return gst.PadProbeOK
	})

	p.GetByName("fresh_video").GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		r.raw.mu.Lock()
		r.raw.lastSource = time.Now()
		r.raw.mu.Unlock()
		return gst.PadProbeOK
	})
	if p.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		return
	}
	started := time.Now()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if rejected.Load() {
				return
			}
			r.raw.mu.Lock()
			last := r.raw.lastSource
			r.raw.mu.Unlock()
			if now.Sub(maxTime(started, last)) > 5*time.Second {
				return
			}
			for msg := p.GetBus().Pop(); msg != nil; msg = p.GetBus().Pop() {
				if msg.Type() == gst.MessageError || msg.Type() == gst.MessageEOS {
					return
				}
			}
		}
	}
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
