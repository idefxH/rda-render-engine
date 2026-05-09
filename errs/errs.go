// Package errs provides sentinel error values used by the render engine.
// This is a minimal subset of github.com/idefxH/rda/internal/errs,
// containing only the sentinels referenced by the render package.
package errs

import "errors"

var (
	ErrInvocation                  = errors.New("ERR_INVOCATION")
	ErrCapabilityBackendUnsupported = errors.New("ERR_CAPABILITY_BACKEND_UNSUPPORTED")
	ErrCapabilityBackendUnknown     = errors.New("ERR_CAPABILITY_BACKEND_UNKNOWN")
	ErrPassthroughSurnested        = errors.New("ERR_PASSTHROUGH_SURNESTED")
)
