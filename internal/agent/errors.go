package agent

import (
	"errors"
	"io"
)

var (
	ErrOperationPending = errors.New("another operation is already pending for this session")
	ErrInterruptPending = errors.New("another interrupt is already pending for this session")
	ErrBashPending      = errors.New("another bash is already pending for this session")
	ErrClientRetiring   = errors.New("Pi RPC client is restarting")
	ErrClientStarting   = errors.New("Pi RPC client is starting")
	ErrProcessExited    = errors.New("Pi RPC process exited")
	ErrStartFailed      = errors.New("Pi could not start")
	ErrInvalidResponse  = errors.New("the agent answered with an invalid response")
)

// RequestError is a request that the agent refused. Message is the agent's reason, which may be empty.
type RequestError struct{ Message string }

func (err *RequestError) Error() string {
	if err.Message == "" {
		return "the agent refused the request"
	}
	return err.Message
}

type RequestTimeoutError struct {
	Command  string
	Accepted bool
}

func (err *RequestTimeoutError) Error() string { return "Pi RPC command timed out: " + err.Command }

type StopResult struct {
	Forced   bool
	Stopping bool
}

func StopResultFor(err error) (StopResult, error) {
	if err == nil {
		return StopResult{}, nil
	}
	if errors.Is(err, ErrInterruptPending) || errors.Is(err, ErrClientRetiring) || errors.Is(err, ErrClientStarting) {
		return StopResult{Stopping: true}, nil
	}
	var timeout *RequestTimeoutError
	if errors.As(err, &timeout) || errors.Is(err, ErrProcessExited) || errors.Is(err, io.ErrClosedPipe) {
		return StopResult{Forced: true}, nil
	}
	return StopResult{}, err
}
