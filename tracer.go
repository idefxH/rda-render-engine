package render

import (
	"fmt"
	"io"
)

// TraceFunc is called at each projection step when tracing is enabled.
// phase is "phase0", "phase1", or "phase2".
// binding is the service binding name (empty for global phases).
// step is the projection step name (e.g. "values_mapping", "passthrough", "wiring").
// detail is a human-readable description of what happened.
type TraceFunc func(phase, binding, step, detail string)

// traceFunc is the global trace callback. nil = no tracing.
var traceFunc TraceFunc

// SetTracer installs a trace callback for the render pipeline.
// Pass nil to disable tracing. Not thread-safe — set once at startup.
func SetTracer(fn TraceFunc) {
	traceFunc = fn
}

// NewStderrTracer returns a TraceFunc that writes to w.
func NewStderrTracer(w io.Writer) TraceFunc {
	return func(phase, binding, step, detail string) {
		if binding != "" {
			fmt.Fprintf(w, "TRACE %s: %s %s %s\n", phase, binding, step, detail)
		} else {
			fmt.Fprintf(w, "TRACE %s: %s %s\n", phase, step, detail)
		}
	}
}

func trace(phase, binding, step, detail string) {
	if traceFunc != nil {
		traceFunc(phase, binding, step, detail)
	}
}
