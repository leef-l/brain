package errors

import (
	"context"
	stderrors "errors"
	"io"
	"net"
	"syscall"
)

// FromGoError maps a standard-library or third-party Go error to a BrainError
// code from the appendix A reserved clause per 21-错误模型.md §4.3. The
// returned code can be passed to New or Wrap — callers almost always want
// Wrap so the original cause is preserved on the BrainError chain.
//
// Returns ("", false) when:
//   - err is nil
//   - err is context.Canceled (the §4.3 "not an error" special case; callers
//     MUST branch on cancellation themselves rather than build an error)
//   - err has no canonical mapping in §4.3
//
// The matcher ordering is specific → generic, using errors.Is for sentinels
// and errors.As for type assertions so wrapped errors still get matched.
// Callers SHOULD fall back to CodeUnknown for truly unclassified errors and
// raise a §7.2 alert — Unknown forces ClassInternalBug in the registry.
func FromGoError(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	// §4.3 "not an error" special case.
	if stderrors.Is(err, context.Canceled) {
		return "", false
	}

	// context.DeadlineExceeded → deadline_exceeded (Transient, retryable).
	if stderrors.Is(err, context.DeadlineExceeded) {
		return CodeDeadlineExceeded, true
	}

	// io.EOF — unexpected mid-frame EOF on a sidecar stdout stream.
	if stderrors.Is(err, io.EOF) {
		return CodeSidecarStdoutEOF, true
	}

	// syscall.EPIPE — writing to a sidecar whose stdin peer closed.
	if stderrors.Is(err, syscall.EPIPE) {
		return CodeSidecarStdinBrokenPipe, true
	}

	// net.OpError (dial / read / write on a live connection). §4.3 singles
	// out Op=="dial" but appendix A v1 has no dedicated dial_failed code —
	// dial failures fold into deadline_exceeded (same Class: Transient,
	// same retry semantics) until appendix A gains one. Documented on the
	// appendix A side as a known v1 fold.
	var opErr *net.OpError
	if stderrors.As(err, &opErr) {
		return CodeDeadlineExceeded, true
	}

	// Generic net.Error: Timeout()==true or Temporary()==true means the
	// caller should retry, which maps to the Transient bucket via
	// deadline_exceeded.
	var netErr net.Error
	if stderrors.As(err, &netErr) {
		if netErr.Timeout() || isTemporary(netErr) {
			return CodeDeadlineExceeded, true
		}
	}

	return "", false
}

// isTemporary probes an error for the optional Temporary() method that some
// net.Error implementations still expose. The method is deprecated in Go
// but it remains the only portable Temporary marker for legacy transport
// errors, so FromGoError still checks it as a hint.
func isTemporary(err error) bool {
	type temp interface{ Temporary() bool }
	t, ok := err.(temp)
	return ok && t.Temporary()
}
