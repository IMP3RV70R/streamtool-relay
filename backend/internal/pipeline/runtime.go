package pipeline

import (
	"context"
	"errors"
)

type RuntimeEventType int

const (
	RuntimeInputLive RuntimeEventType = iota
	RuntimeDestinationStreaming
	RuntimeDestinationError
	RuntimeDestinationStopped
	RuntimeInputError
	RuntimeEOS
	RuntimeFallback
	RuntimeInputUnavailable
	RuntimeInputRejected
)

type DestinationSpec struct {
	ID         string `json:"id"`
	Generation uint64 `json:"generation"`
	Location   string `json:"location"`
}

type SlatePolicy struct {
	OnSourceLoss bool `json:"on_source_loss"`
	Forced       bool `json:"forced"`
}

type RuntimeEvent struct {
	Type          RuntimeEventType
	DestinationID string
	Generation    uint64
	Forced        bool
	InputLive     bool
	Err           error
}

type Runtime interface {
	Start(context.Context) error
	Events() <-chan RuntimeEvent
	ApplyDestinations(context.Context, []DestinationSpec) error
	ApplySlatePolicy(context.Context, SlatePolicy) error
	ReconnectDestination(context.Context, string) error
	Stop(context.Context) error
}

var ErrGStreamerUnavailable = errors.New("GStreamer backend is unavailable; build with -tags gstreamer")
