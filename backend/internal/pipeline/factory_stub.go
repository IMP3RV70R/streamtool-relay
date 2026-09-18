//go:build !gstreamer

package pipeline

func NewRuntime(Spec) (Runtime, error) { return nil, ErrGStreamerUnavailable }
