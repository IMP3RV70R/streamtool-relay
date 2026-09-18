package worker

import "time"

type EventType string

const (
	InputConnecting         EventType = "INPUT_CONNECTING"
	InputLive               EventType = "INPUT_LIVE"
	FallbackActive          EventType = "FALLBACK_ACTIVE"
	InputRejected           EventType = "INPUT_REJECTED"
	InputLost               EventType = "INPUT_LOST"
	InputUnavailable        EventType = "INPUT_UNAVAILABLE"
	DestinationConnecting   EventType = "DESTINATION_CONNECTING"
	DestinationStreaming    EventType = "DESTINATION_STREAMING"
	DestinationReconnecting EventType = "DESTINATION_RECONNECTING"
	DestinationFailed       EventType = "DESTINATION_FAILED"
	DestinationStopped      EventType = "DESTINATION_STOPPED"
	WorkerStopping          EventType = "WORKER_STOPPING"
	WorkerStopped           EventType = "WORKER_STOPPED"
)

type ErrorCode string

const (
	ErrInputUnavailable ErrorCode = "INPUT_UNAVAILABLE"
	ErrUnsupportedMedia ErrorCode = "UNSUPPORTED_MEDIA"
	ErrDestinationAuth  ErrorCode = "DESTINATION_AUTH"
	ErrDestinationNet   ErrorCode = "DESTINATION_NETWORK"
	ErrPipeline         ErrorCode = "PIPELINE_ERROR"
	ErrShutdownTimeout  ErrorCode = "SHUTDOWN_TIMEOUT"
)

type Event struct {
	Timestamp     time.Time      `json:"timestamp"`
	Type          EventType      `json:"event"`
	SessionID     string         `json:"session_id"`
	DestinationID string         `json:"destination_id,omitempty"`
	Generation    uint64         `json:"generation,omitempty"`
	Code          ErrorCode      `json:"error_code,omitempty"`
	Attempt       int            `json:"attempt,omitempty"`
	Fields        map[string]any `json:"fields,omitempty"`
}

type Sink interface{ Emit(Event) }
type SinkFunc func(Event)

func (f SinkFunc) Emit(e Event) { f(e) }
