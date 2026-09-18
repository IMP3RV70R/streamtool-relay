//go:build gstreamer

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"streamtool-relay/internal/config"
)

type gstBranch struct {
	spec                                                      DestinationSpec
	videoGuard, videoQueue, audioGuard, audioQueue, mux, sink gst.Element
	videoPad, audioPad                                        gst.Pad
	videoMuxPad, audioMuxPad                                  gst.Pad
	armed, reconnecting                                       bool
}

type gstRuntime struct {
	raw                *rawSwitcher
	spec               Spec
	mu                 sync.Mutex
	pipeline           gst.Pipeline
	videoTee, audioTee gst.Element
	branches           map[string]*gstBranch
	events             chan RuntimeEvent
	cancel             context.CancelFunc
	fallbackDone       chan struct{}
	stopped            atomic.Bool
}

func NewRuntime(spec Spec) (Runtime, error) {
	g, err := Plan(spec)
	if err != nil {
		return nil, err
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return &gstRuntime{spec: spec, branches: map[string]*gstBranch{}, events: make(chan RuntimeEvent, 128)}, nil
}

func (r *gstRuntime) Events() <-chan RuntimeEvent { return r.events }

func (r *gstRuntime) Start(parent context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	gst.Init()
	if r.spec.FallbackImage == "" && r.spec.FallbackVideo == "" {
		r.spec.FallbackImage = "/usr/local/share/streamtool/offline.png"
	}
	if r.spec.MediaProfile.Width == 0 {
		r.spec.MediaProfile = config.DefaultMediaProfile()
	}
	return r.startFallback(parent)
}

func makeElement(factory, name string) (gst.Element, error) {
	e := gst.ElementFactoryMake(factory, name)
	if e == nil {
		return nil, fmt.Errorf("required GStreamer element %q is unavailable", factory)
	}
	return e, nil
}

func safeName(id string) string {
	var b strings.Builder
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (r *gstRuntime) watchBus(ctx context.Context) {
	for msg := range r.pipeline.GetBus().Messages(ctx) {
		source := msg.Source()
		name := ""
		if source != nil {
			name = source.GetName()
		}
		switch msg.Type() {
		case gst.MessageError:
			debug, err := msg.ParseError()
			wrapped := fmt.Errorf("%w (%s)", err, debug)
			r.mu.Lock()
			branch := r.branchByElementLocked(name)
			if branch != nil && branch.armed {
				branch.armed = false
				branch.reconnecting = true
			}
			r.mu.Unlock()
			if branch != nil {
				r.publish(RuntimeEvent{Type: RuntimeDestinationError, DestinationID: branch.spec.ID, Generation: branch.spec.Generation, Err: wrapped})
			} else {
				r.publish(RuntimeEvent{Type: RuntimeInputError, Err: wrapped})
			}
		case gst.MessageEOS:
			if !r.anyReconnecting() {
				r.publish(RuntimeEvent{Type: RuntimeEOS, Err: errors.New("input end of stream")})
			}
		case gst.MessageStateChanged:
			r.mu.Lock()
			branch := r.branchByElementLocked(name)
			if branch != nil && name == branch.sink.GetName() {
				_, state, _ := msg.ParseStateChanged()
				if state == gst.StatePlaying {
					branch.reconnecting = false
					branch.armed = true
					spec := branch.spec
					r.mu.Unlock()
					r.publish(RuntimeEvent{Type: RuntimeDestinationStreaming, DestinationID: spec.ID, Generation: spec.Generation})
					continue
				}
			}
			r.mu.Unlock()
		}
	}
}

func (r *gstRuntime) branchByElementLocked(name string) *gstBranch {
	for _, b := range r.branches {
		for _, e := range []gst.Element{b.videoGuard, b.videoQueue, b.audioGuard, b.audioQueue, b.mux, b.sink} {
			if e != nil && e.GetName() == name {
				return b
			}
		}
	}
	return nil
}
func (r *gstRuntime) anyReconnecting() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.branches {
		if b.reconnecting {
			return true
		}
	}
	return false
}
func (r *gstRuntime) publish(e RuntimeEvent) {
	select {
	case r.events <- e:
	default:
	}
}

func (r *gstRuntime) ApplyDestinations(_ context.Context, desired []DestinationSpec) error {
	if len(desired) > 8 {
		return errors.New("at most eight outputs allowed")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	want := map[string]DestinationSpec{}
	for _, d := range desired {
		if d.ID == "" || d.Location == "" || d.Generation == 0 {
			return fmt.Errorf("invalid destination %q", d.ID)
		}
		if _, exists := want[d.ID]; exists {
			return fmt.Errorf("duplicate destination %q", d.ID)
		}
		want[d.ID] = d
		if current := r.branches[d.ID]; current != nil && d.Generation < current.spec.Generation {
			return fmt.Errorf("stale destination %s generation %d < %d", d.ID, d.Generation, current.spec.Generation)
		}
	}
	for id, current := range r.branches {
		d, ok := want[id]
		if !ok || d.Generation > current.spec.Generation {
			if err := r.removeBranchLocked(id); err != nil {
				return err
			}
		}
	}
	for id, d := range want {
		if r.branches[id] == nil {
			if err := r.addBranchLocked(d); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *gstRuntime) ApplySlatePolicy(_ context.Context, policy SlatePolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.raw == nil {
		return errors.New("slate control is unavailable")
	}
	r.spec.SlatePolicy = policy
	r.raw.mu.Lock()
	r.raw.policy = policy
	r.raw.mu.Unlock()
	return nil
}

func (r *gstRuntime) ReconnectDestination(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.branches[id]
	if b == nil {
		return fmt.Errorf("destination %q not found", id)
	}
	spec := b.spec
	if err := r.removeBranchLocked(id); err != nil {
		return err
	}
	return r.addBranchLocked(spec)
}

func (r *gstRuntime) addBranchLocked(spec DestinationSpec) error {
	n := "destination_" + safeName(spec.ID)
	b := &gstBranch{spec: spec, armed: true}
	var err error
	for _, item := range []struct {
		factory, suffix string
		target          *gst.Element
	}{
		{"errorignore", "_video_guard", &b.videoGuard}, {"queue", "_video_queue", &b.videoQueue}, {"errorignore", "_audio_guard", &b.audioGuard}, {"queue", "_audio_queue", &b.audioQueue}, {"flvmux", "_mux", &b.mux}, {"rtmpsink", "_sink", &b.sink},
	} {
		*item.target, err = makeElement(item.factory, n+item.suffix)
		if err != nil {
			return err
		}
	}
	for _, g := range []gst.Element{b.videoGuard, b.audioGuard} {
		g.SetObjectProperty("ignore-error", true)
		g.SetObjectProperty("ignore-notnegotiated", true)
		g.SetObjectProperty("convert-to", int32(0))
	}
	for _, q := range []gst.Element{b.videoQueue, b.audioQueue} {
		r.configureQueue(q)
	}
	b.mux.SetObjectProperty("streamable", true)
	b.sink.SetObjectProperty("location", spec.Location)
	b.sink.SetObjectProperty("sync", false)
	if !r.pipeline.AddMany(b.videoGuard, b.videoQueue, b.audioGuard, b.audioQueue, b.mux, b.sink) {
		return errors.New("add destination branch")
	}
	for _, link := range []struct {
		name     string
		from, to gst.Element
	}{{"video guard to queue", b.videoGuard, b.videoQueue}, {"audio guard to queue", b.audioGuard, b.audioQueue}} {
		if !link.from.Link(link.to) {
			return fmt.Errorf("link destination branch: %s", link.name)
		}
	}
	b.videoPad = r.videoTee.RequestPadSimple("src_%u")
	b.audioPad = r.audioTee.RequestPadSimple("src_%u")
	if b.videoPad == nil || b.audioPad == nil || b.videoPad.Link(b.videoGuard.GetStaticPad("sink")) != gst.PadLinkOK || b.audioPad.Link(b.audioGuard.GetStaticPad("sink")) != gst.PadLinkOK {
		return errors.New("link tee request pads")
	}
	b.videoMuxPad = b.mux.RequestPadSimple("video")
	b.audioMuxPad = b.mux.RequestPadSimple("audio")
	if b.videoMuxPad == nil || b.audioMuxPad == nil || b.videoQueue.GetStaticPad("src").Link(b.videoMuxPad) != gst.PadLinkOK || b.audioQueue.GetStaticPad("src").Link(b.audioMuxPad) != gst.PadLinkOK {
		return errors.New("link destination queues to mux request pads")
	}
	if !b.mux.Link(b.sink) {
		return errors.New("link destination mux to sink")
	}
	r.branches[spec.ID] = b
	// Parent pending state matters during dynamic multi-branch attachment.
	if r.cancel != nil {
		for _, e := range []gst.Element{b.videoGuard, b.videoQueue, b.audioGuard, b.audioQueue, b.mux, b.sink} {
			if !e.SyncStateWithParent() {
				return errors.New("activate destination branch")
			}
		}
	}
	return nil
}

func (r *gstRuntime) removeBranchLocked(id string) error {
	b := r.branches[id]
	if b == nil {
		return nil
	}
	for _, e := range []gst.Element{b.sink, b.mux, b.videoQueue, b.videoGuard, b.audioQueue, b.audioGuard} {
		e.SetState(gst.StateNull)
	}
	if b.videoPad != nil {
		b.videoPad.Unlink(b.videoGuard.GetStaticPad("sink"))
		r.videoTee.ReleaseRequestPad(b.videoPad)
	}
	if b.audioPad != nil {
		b.audioPad.Unlink(b.audioGuard.GetStaticPad("sink"))
		r.audioTee.ReleaseRequestPad(b.audioPad)
	}
	if b.videoMuxPad != nil {
		b.videoQueue.GetStaticPad("src").Unlink(b.videoMuxPad)
		b.mux.ReleaseRequestPad(b.videoMuxPad)
	}
	if b.audioMuxPad != nil {
		b.audioQueue.GetStaticPad("src").Unlink(b.audioMuxPad)
		b.mux.ReleaseRequestPad(b.audioMuxPad)
	}
	r.pipeline.RemoveMany(b.sink, b.mux, b.videoQueue, b.videoGuard, b.audioQueue, b.audioGuard)
	delete(r.branches, id)
	r.publish(RuntimeEvent{Type: RuntimeDestinationStopped, DestinationID: id, Generation: b.spec.Generation})
	return nil
}

func (r *gstRuntime) configureQueue(q gst.Element) {
	q.SetObjectProperty("max-size-bytes", r.spec.QueueMaxBytes)
	q.SetObjectProperty("max-size-buffers", uint32(0))
	q.SetObjectProperty("max-size-time", uint64(0))
	q.SetObjectProperty("leaky", int32(2))
}

func (r *gstRuntime) Stop(ctx context.Context) error {
	if !r.stopped.CompareAndSwap(false, true) {
		return nil
	}
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	if r.fallbackDone != nil {
		select {
		case <-r.fallbackDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan gst.StateChangeReturn, 1)
	go func() { done <- r.pipeline.BlockSetState(gst.StateNull, gst.ClockTime(10*time.Second)) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-done:
		if result == gst.StateChangeFailure {
			return errors.New("set pipeline NULL")
		}
		return nil
	}
}
