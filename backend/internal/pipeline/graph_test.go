package pipeline

import (
	"fmt"
	"testing"
)

func TestPlanUsesOneContinuousEncoderAndBoundedQueues(t *testing.T) {
	destinations := make([]DestinationSpec, 8)
	for i := range destinations {
		destinations[i] = DestinationSpec{ID: fmt.Sprintf("output%d", i), Generation: 1, Location: "rtmp://sink/live/key"}
	}
	g, err := Plan(Spec{SourceLocation: "srt://edge:8890", Destinations: destinations, QueueMaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	factories := map[string]Element{}
	for _, e := range g.Elements {
		factories[e.Factory] = e
	}
	for _, required := range []string{"srtsrc", "tsdemux", "h264parse", "aacparse", "tee", "flvmux", "rtmpsink"} {
		if _, ok := factories[required]; !ok {
			t.Errorf("missing %s", required)
		}
	}
	for _, required := range []string{"openh264dec", "x264enc", "voaacenc", "input-selector"} {
		if _, ok := factories[required]; !ok {
			t.Errorf("missing %s", required)
		}
	}
	encoders, sinks := 0, 0
	for _, e := range g.Elements {
		if e.Factory == "rtmpsink" {
			sinks++
		}
		if e.Factory == "x264enc" {
			encoders++
		}
	}
	if sinks != 8 {
		t.Fatal("expected eight independent sinks")
	}
	if encoders != 1 {
		t.Fatal("expected exactly one video encoder")
	}
	if factories["queue"].Properties["max-size-bytes"] != uint64(4096) {
		t.Fatal("queue is not bounded")
	}
	destinations = append(destinations, DestinationSpec{ID: "ninth", Generation: 1, Location: "rtmp://sink/live/ninth"})
	if _, err = Plan(Spec{SourceLocation: "srt://edge:8890", Destinations: destinations, QueueMaxBytes: 4096}); err == nil {
		t.Fatal("ninth output admitted")
	}
}

func TestPlanRejectsDuplicateDestinations(t *testing.T) {
	_, err := Plan(Spec{SourceLocation: "srt://edge", QueueMaxBytes: 1, Destinations: []DestinationSpec{{ID: "same", Generation: 1, Location: "rtmp://one"}, {ID: "same", Generation: 1, Location: "rtmp://two"}}})
	if err == nil {
		t.Fatal("expected duplicate rejection")
	}
}
