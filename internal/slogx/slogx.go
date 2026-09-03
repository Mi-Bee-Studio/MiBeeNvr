// Package slogx provides loggers that survive a late slog.SetDefault.
//
// Package-level `var x = slog.Default().With(...)` loggers initialize before
// main() installs the configured handler. The pristine default handler they
// capture writes through the log package, which SetDefault has redirected
// into the new handler — every record then reappears wrapped as
// `level=INFO msg="ERROR <original line with attrs inlined>"` (wrong level,
// attrs unparseable). On the M5 production node this mangled ~32% of all log
// output (issue #685). Component resolves slog.Default() at CALL time, so
// the configured handler, level, and format always apply.
package slogx

import (
	"context"
	"log/slog"
)

// Component returns a logger tagged component=name that defers handler
// resolution to emit time. Drop-in for `slog.Default().With("component", n)`
// in package-level var declarations.
func Component(name string) *slog.Logger {
	return slog.New(&deferredHandler{attrs: []slog.Attr{slog.String("component", name)}})
}

// deferredHandler forwards records to slog.Default()'s CURRENT handler,
// prepending its accumulated attrs. Our loggers are never installed as the
// default, so Default() resolves to a real sink — no recursion.
type deferredHandler struct {
	attrs       []slog.Attr
	groupPrefix string
}

func (h *deferredHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return slog.Default().Handler().Enabled(ctx, level)
}

func (h *deferredHandler) Handle(ctx context.Context, r slog.Record) error {
	if len(h.attrs) > 0 {
		r.AddAttrs(h.attrs...)
	}
	return slog.Default().Handler().Handle(ctx, r)
}

func (h *deferredHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	for _, a := range attrs {
		if h.groupPrefix != "" {
			a.Key = h.groupPrefix + "." + a.Key
		}
		merged = append(merged, a)
	}
	return &deferredHandler{attrs: merged, groupPrefix: h.groupPrefix}
}

func (h *deferredHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	prefix := name
	if h.groupPrefix != "" {
		prefix = h.groupPrefix + "." + name
	}
	return &deferredHandler{attrs: h.attrs, groupPrefix: prefix}
}
