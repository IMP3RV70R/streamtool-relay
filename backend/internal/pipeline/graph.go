package pipeline

import (
	"errors"
	"fmt"
	"streamtool-relay/internal/config"
)

type MediaKind string

const (
	Video MediaKind = "video"
	Audio MediaKind = "audio"
)

type Element struct {
	Name, Factory string
	Properties    map[string]any
}
type Link struct {
	From, To string
	Dynamic  bool
	Kind     MediaKind
}
type Graph struct {
	Elements []Element
	Links    []Link
}

type Spec struct {
	MediaProfile   config.MediaProfile
	SourceLocation string
	FallbackImage  string
	FallbackVideo  string
	SlatePolicy    SlatePolicy
	Destinations   []DestinationSpec
	QueueMaxBytes  uint64
}

func Plan(spec Spec) (Graph, error) {
	if spec.SourceLocation == "" || len(spec.Destinations) < 1 || len(spec.Destinations) > 8 || spec.QueueMaxBytes == 0 {
		return Graph{}, errors.New("source, one to eight destinations, and bounded queue are required")
	}
	e := func(name, factory string, props map[string]any) Element {
		return Element{Name: name, Factory: factory, Properties: props}
	}
	media := spec.MediaProfile
	if media.Width == 0 {
		media = config.DefaultMediaProfile()
	}
	if err := media.Validate(); err != nil {
		return Graph{}, err
	}
	g := Graph{Elements: []Element{
		e("source", "srtsrc", map[string]any{"uri": spec.SourceLocation}), e("demux", "tsdemux", nil),
		e("video_parse", "h264parse", nil), e("video_decode", "openh264dec", nil),
		e("video_select", "input-selector", map[string]any{"drop-backwards": true}),
		e("video_encode", "x264enc", map[string]any{"threads": 2, "speed-preset": "veryfast", "bitrate": media.VideoKbps, "nal-hrd": "cbr"}), e("video_tee", "tee", nil),
		e("audio_parse", "aacparse", nil), e("audio_decode", "faad", nil), e("audio_select", "input-selector", nil),
		e("audio_encode", "voaacenc", map[string]any{"bitrate": media.AudioKbps * 1000}), e("audio_tee", "tee", nil),
		e("fallback_video", "intervideosrc", map[string]any{"channel": "fallback"}), e("fallback_audio", "audiotestsrc", map[string]any{"wave": "silence"}),
	}, Links: []Link{
		{From: "source", To: "demux"}, {From: "demux", To: "video_parse", Dynamic: true, Kind: Video}, {From: "video_parse", To: "video_decode"},
		{From: "video_decode", To: "video_select"}, {From: "fallback_video", To: "video_select"}, {From: "video_select", To: "video_encode"}, {From: "video_encode", To: "video_tee"},
		{From: "demux", To: "audio_parse", Dynamic: true, Kind: Audio}, {From: "audio_parse", To: "audio_decode"}, {From: "audio_decode", To: "audio_select"},
		{From: "fallback_audio", To: "audio_select"}, {From: "audio_select", To: "audio_encode"}, {From: "audio_encode", To: "audio_tee"},
	}}
	seen := map[string]bool{}
	for _, d := range spec.Destinations {
		if d.ID == "" || d.Location == "" || d.Generation == 0 || seen[d.ID] {
			return Graph{}, fmt.Errorf("invalid or duplicate destination %q", d.ID)
		}
		seen[d.ID] = true
		p := "destination_" + d.ID
		g.Elements = append(g.Elements,
			e(p+"_video_queue", "queue", map[string]any{"max-size-bytes": spec.QueueMaxBytes, "leaky": 2}),
			e(p+"_audio_queue", "queue", map[string]any{"max-size-bytes": spec.QueueMaxBytes, "leaky": 2}),
			e(p+"_mux", "flvmux", map[string]any{"streamable": true}), e(p+"_sink", "rtmpsink", map[string]any{"location": d.Location, "sync": false}))
		g.Links = append(g.Links,
			Link{From: "video_tee", To: p + "_video_queue", Kind: Video}, Link{From: p + "_video_queue", To: p + "_mux", Kind: Video},
			Link{From: "audio_tee", To: p + "_audio_queue", Kind: Audio}, Link{From: p + "_audio_queue", To: p + "_mux", Kind: Audio}, Link{From: p + "_mux", To: p + "_sink"})
	}
	return g, nil
}

func (g Graph) Validate() error {
	names := map[string]bool{}
	for _, e := range g.Elements {
		if names[e.Name] {
			return fmt.Errorf("duplicate element %q", e.Name)
		}
		names[e.Name] = true
	}
	for _, l := range g.Links {
		if !names[l.From] || !names[l.To] {
			return fmt.Errorf("link references unknown element: %+v", l)
		}
	}
	return nil
}
