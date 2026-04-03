package kernel

import (
	"github.com/akria/gak/pkg/logging"
	"github.com/akria/gak/pkg/metrics"
	"github.com/akria/gak/pkg/session"
)

// Option configures the kernel runner.
type Option func(*Runner)

// WithMaxTurns sets the maximum number of inference turns per run.
func WithMaxTurns(n int) Option {
	return func(r *Runner) {
		r.maxTurns = n
	}
}

// WithMaxTokens sets the maximum token count for LLM responses.
func WithMaxTokens(n int) Option {
	return func(r *Runner) {
		r.maxTokens = n
	}
}

// WithTemperature sets the LLM temperature.
func WithTemperature(t float64) Option {
	return func(r *Runner) {
		r.temperature = t
	}
}

// WithEventBufferSize sets the channel buffer size for events.
func WithEventBufferSize(n int) Option {
	return func(r *Runner) {
		r.eventBufferSize = n
	}
}

// WithMetrics enables runtime metrics collection.
func WithMetrics(collector *metrics.Collector) Option {
	return func(r *Runner) {
		r.metrics = collector
	}
}

// WithLogger enables structured logging.
func WithLogger(logger *logging.Logger) Option {
	return func(r *Runner) {
		r.logger = logger
	}
}

// WithSession enables session persistence and checkpoint/resume.
func WithSession(mgr *session.Manager) Option {
	return func(r *Runner) {
		r.session = mgr
	}
}
