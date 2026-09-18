package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"streamtool-relay/internal/pipeline"
)

type Service struct {
	SessionID       string
	DestinationIDs  []string
	Runtime         pipeline.Runtime
	Events          Sink
	Clock           Clock
	Backoff         Backoff
	StableInterval  time.Duration
	ShutdownTimeout time.Duration
	MaxAttempts     int
}

type destinationState struct {
	attempt        int
	generation     uint64
	streamingSince time.Time
	waiting        bool
	terminal       bool
}
type retryRequest struct {
	id         string
	generation uint64
}

func (s *Service) Run(ctx context.Context) error {
	if s.Runtime == nil || s.Events == nil || s.Clock == nil || s.Backoff.Min <= 0 || s.Backoff.Max < s.Backoff.Min {
		return errors.New("invalid worker service dependencies")
	}
	s.emit(InputConnecting, "", 0, "", 0, nil)
	states := map[string]*destinationState{}
	for _, id := range s.DestinationIDs {
		states[id] = &destinationState{}
		s.emit(DestinationConnecting, id, 0, "", 0, nil)
	}
	if err := s.Runtime.Start(ctx); err != nil {
		s.emit(InputLost, "", 0, ErrPipeline, 0, map[string]any{"error": err.Error()})
		return fmt.Errorf("start pipeline: %w", err)
	}
	retries := make(chan retryRequest, 64)
	for {
		select {
		case <-ctx.Done():
			return s.stop()
		case request := <-retries:
			state := states[request.id]
			if state == nil || state.generation != request.generation || state.terminal {
				continue
			}
			state.waiting = false
			s.emit(DestinationConnecting, request.id, state.generation, "", state.attempt, nil)
			if err := s.Runtime.ReconnectDestination(ctx, request.id); err != nil {
				s.emit(DestinationReconnecting, request.id, state.generation, ErrPipeline, state.attempt, errorField(err))
			}
		case event, ok := <-s.Runtime.Events():
			if !ok {
				return s.stop()
			}
			switch event.Type {
			case pipeline.RuntimeInputLive:
				s.emit(InputLive, "", 0, "", 0, nil)
			case pipeline.RuntimeDestinationStreaming:
				state := ensureState(states, event.DestinationID)
				state.generation = event.Generation
				state.streamingSince = s.Clock.Now()
				state.waiting = false
				state.terminal = false
				s.emit(DestinationStreaming, event.DestinationID, event.Generation, "", state.attempt, nil)
			case pipeline.RuntimeDestinationStopped:
				s.emit(DestinationStopped, event.DestinationID, event.Generation, "", 0, nil)
				delete(states, event.DestinationID)
			case pipeline.RuntimeDestinationError:
				state := ensureState(states, event.DestinationID)
				if event.Generation < state.generation || state.waiting || state.terminal {
					continue
				}
				state.generation = event.Generation
				if !state.streamingSince.IsZero() && s.Clock.Now().Sub(state.streamingSince) >= s.StableInterval {
					state.attempt = 0
				}
				if ClassifyDestinationError(event.Err) == Permanent {
					state.terminal = true
					s.emit(DestinationFailed, event.DestinationID, event.Generation, ErrDestinationAuth, state.attempt, errorField(event.Err))
					continue
				}
				if s.MaxAttempts > 0 && state.attempt >= s.MaxAttempts {
					state.terminal = true
					s.emit(DestinationFailed, event.DestinationID, event.Generation, ErrDestinationNet, state.attempt, errorField(ErrRetriesExhausted))
					continue
				}
				delay := s.Backoff.Duration(state.attempt)
				state.attempt++
				state.waiting = true
				s.emit(DestinationReconnecting, event.DestinationID, event.Generation, ErrDestinationNet, state.attempt, map[string]any{"retry_in_ms": delay.Milliseconds(), "error": safeError(event.Err)})
				go func(req retryRequest, d time.Duration) {
					if s.Clock.Sleep(ctx, d) == nil {
						select {
						case retries <- req:
						case <-ctx.Done():
						}
					}
				}(retryRequest{event.DestinationID, event.Generation}, delay)
			case pipeline.RuntimeInputRejected:
				s.emit(InputRejected, "", 0, ErrUnsupportedMedia, 0, nil)
			case pipeline.RuntimeFallback:
				s.emit(FallbackActive, "", 0, ErrInputUnavailable, 0, map[string]any{"forced": event.Forced, "source_live": event.InputLive})
			case pipeline.RuntimeInputUnavailable:
				s.emit(InputUnavailable, "", 0, ErrInputUnavailable, 0, nil)
			case pipeline.RuntimeInputError, pipeline.RuntimeEOS:
				s.emit(InputLost, "", 0, ErrInputUnavailable, 0, errorField(event.Err))
				return s.stop()
			}
		}
	}
}

func ensureState(states map[string]*destinationState, id string) *destinationState {
	state := states[id]
	if state == nil {
		state = &destinationState{}
		states[id] = state
	}
	return state
}

func (s *Service) stop() error {
	s.emit(WorkerStopping, "", 0, "", 0, nil)
	timeout := s.ShutdownTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := s.Runtime.Stop(ctx)
	code := ErrorCode("")
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = ErrShutdownTimeout
	}
	s.emit(WorkerStopped, "", 0, code, 0, errorField(err))
	return err
}
func (s *Service) emit(t EventType, id string, generation uint64, code ErrorCode, attempt int, fields map[string]any) {
	s.Events.Emit(Event{Timestamp: s.Clock.Now().UTC(), Type: t, SessionID: s.SessionID, DestinationID: id, Generation: generation, Code: code, Attempt: attempt, Fields: fields})
}
func errorField(err error) map[string]any {
	if err == nil {
		return nil
	}
	return map[string]any{"error": safeError(err)}
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
