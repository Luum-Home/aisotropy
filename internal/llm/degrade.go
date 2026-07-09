package llm

// This file implements the Phase 7 "model degradation at 95% quota" policy:
// instead of hard-rejecting a tenant once its quota is exhausted, route it to
// a cheaper model as it approaches the limit. The policy itself is decoupled
// from the rate limiter — it is a pure function of a pressure value in
// [0, 1] and a threshold, so it has no dependency on internal/memory.Limiter
// and no I/O. Callers compute pressure via memory.Limiter.Pressure (see
// internal/memory/quota_pressure.go for that convention: pressure is the
// fraction of the tenant's burst already consumed, so 1.0 means "at the
// limit") and pass it in here alongside the two candidate model names.

// DefaultDegradeThreshold is the pressure at which callers should switch to
// the cheap model, per the roadmap's "model degradation at 95% quota" phase.
const DefaultDegradeThreshold = 0.95

// ModelChoice is the outcome of a degradation decision: which model to use,
// and whether that choice represents a degradation from the caller's
// preferred (primary) model.
type ModelChoice struct {
	// Model is the model name to use for the next call.
	Model string
	// Degraded is true when Model is the cheap fallback rather than primary.
	Degraded bool
}

// DegradeAt decides which model to use given the tenant's current quota
// pressure (from memory.Limiter.Pressure, a fraction in [0, 1] where 1.0
// means the bucket is fully consumed) and a threshold (defaulting to
// DefaultDegradeThreshold = 0.95 per the roadmap).
//
// When pressure >= threshold, DegradeAt returns the cheap model with
// Degraded=true. Otherwise it returns primary with Degraded=false.
//
// If cheap is empty, DegradeAt never degrades — it always returns primary,
// regardless of pressure. This lets callers wire the policy in even when no
// cheaper fallback model has been configured yet, without special-casing the
// empty string at every call site.
func DegradeAt(pressure, threshold float64, primary, cheap string) ModelChoice {
	if cheap == "" {
		return ModelChoice{Model: primary, Degraded: false}
	}
	if pressure >= threshold {
		return ModelChoice{Model: cheap, Degraded: true}
	}
	return ModelChoice{Model: primary, Degraded: false}
}
