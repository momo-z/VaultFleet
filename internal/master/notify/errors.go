package notify

import "fmt"

type sanitizedSendError struct {
	op  string
	err error
}

func (e sanitizedSendError) Error() string {
	return fmt.Sprintf("%s: %s", e.op, sanitizedErrorContext(e.err))
}

func (e sanitizedSendError) Unwrap() error {
	return e.err
}

func sanitizedErrorContext(err error) string {
	if err == nil {
		return "request failed"
	}
	if timeoutErr, ok := err.(interface{ Timeout() bool }); ok && timeoutErr.Timeout() {
		return "request timed out"
	}
	return "request failed"
}
