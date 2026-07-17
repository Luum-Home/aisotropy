package guardrails

import (
	"log/slog"
	"os"
	"strconv"
)

// Environment variables for the opt-in multilingual model scanner. All are
// optional; with AISOTROPY_GUARDRAIL_MODEL_BIN unset the scanner is entirely
// dormant and the Detector runs regex-only (byte-for-byte identical to before).
const (
	// envModelBin points at the built guardrail-model worker
	// (crates/guardrail-model). Setting it is the ONLY switch that opts the
	// multilingual scanner in — the whole feature is off until this is set,
	// mirroring AISOTROPY_EMBED_BIN / AISOTROPY_SLEEPCYCLE_BIN.
	envModelBin = "AISOTROPY_GUARDRAIL_MODEL_BIN"
	// envModelDir points at the pinned classifier directory (config.json +
	// model.safetensors + tokenizer.json). Required when envModelBin is set.
	envModelDir = "AISOTROPY_GUARDRAIL_MODEL_DIR"
	// envModelThreshold overrides the scanner-local Suspicious cutoff (0..1).
	envModelThreshold = "AISOTROPY_GUARDRAIL_THRESHOLD"
	// envModelHypothesis overrides the zero-shot NLI hypothesis the premise is
	// scored against.
	envModelHypothesis = "AISOTROPY_GUARDRAIL_HYPOTHESIS"
	// envModelMode selects the worker form when the scanner is enabled: "" or
	// "daemon" -> DaemonScanner (one warm process, model loaded once — the T1
	// production path); "subprocess" -> ModelScanner (a fresh process per call,
	// the ADR-0052 cold PoC, panic-isolated). Mirrors AISOTROPY_EMBED_MODE.
	envModelMode = "AISOTROPY_GUARDRAIL_MODEL_MODE"
)

// ModelScannerFromEnv builds the opt-in multilingual model scanner from the
// environment, or reports that it is disabled.
//
// It returns (scanner, true) only when AISOTROPY_GUARDRAIL_MODEL_BIN is set AND
// the configuration is valid. In every other case — unset bin (the default),
// missing model dir, or a construction error — it returns (nil, false) after a
// warning (only when the operator clearly intended to enable it, i.e. the bin
// was set), so callers deterministically fall back to the regex-only Detector.
// It never fails closed on a misconfigured scanner.
//
// AISOTROPY_GUARDRAIL_MODEL_MODE selects the worker form (T1): "" or "daemon"
// yields a warm DaemonScanner (model loaded once — the production default);
// "subprocess" yields the ADR-0052 cold ModelScanner (a fresh process per call).
func ModelScannerFromEnv() (InputScanner, bool) {
	bin := os.Getenv(envModelBin)
	if bin == "" {
		return nil, false // OFF by default: the entire feature is dormant.
	}
	modelDir := os.Getenv(envModelDir)
	if modelDir == "" {
		slog.Warn("guardrails: "+envModelBin+" set without "+envModelDir+"; multilingual scanner disabled", "bin", bin)
		return nil, false
	}
	hypothesis := os.Getenv(envModelHypothesis)
	threshold := thresholdFromEnv()

	if os.Getenv(envModelMode) == "subprocess" {
		sc, err := NewModelScanner(ModelScannerConfig{
			BinPath:    bin,
			ModelDir:   modelDir,
			Hypothesis: hypothesis,
			Threshold:  threshold,
		})
		if err != nil {
			slog.Warn("guardrails: failed to build (subprocess) model scanner from env; disabled", "error", err)
			return nil, false
		}
		slog.Info("guardrails: multilingual model scanner enabled", "bin", bin, "model_dir", modelDir, "mode", "subprocess")
		return sc, true
	}

	sc, err := NewDaemonScanner(DaemonScannerConfig{
		BinPath:    bin,
		ModelDir:   modelDir,
		Hypothesis: hypothesis,
		Threshold:  threshold,
	})
	if err != nil {
		slog.Warn("guardrails: failed to build (daemon) model scanner from env; disabled", "error", err)
		return nil, false
	}
	slog.Info("guardrails: multilingual model scanner enabled", "bin", bin, "model_dir", modelDir, "mode", "daemon")
	return sc, true
}

// thresholdFromEnv parses AISOTROPY_GUARDRAIL_THRESHOLD as a 0..1 float. An
// unset or invalid value returns 0, which NewModelScanner reads as "use the
// default cutoff".
func thresholdFromEnv() float64 {
	raw := os.Getenv(envModelThreshold)
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 || v > 1 {
		slog.Warn("guardrails: "+envModelThreshold+" invalid, using default cutoff", "value", raw)
		return 0
	}
	return v
}

// DetectorFromEnv returns the injection Detector an integrator should wire into
// Guardrails, wrapping the opt-in multilingual scanner onto the built-in
// heuristics via Detector.WithScanner when — and only when — the environment
// enables it. With AISOTROPY_GUARDRAIL_MODEL_BIN unset it returns exactly
// NewDetector() (no backends, so Inspect is byte-for-byte identical to the
// regex-only path).
//
// Adoption (owned by the composition root, not touched here) is a one-liner:
//
//	guardrails.New(guardrails.Config{Detector: guardrails.DetectorFromEnv()})
//
// The zero Config keeps the existing default; the swap is purely additive and
// gated behind the env var, so shipping it dark changes nothing until an
// operator points the env at a built worker + model.
func DetectorFromEnv() *Detector {
	d := NewDetector()
	if sc, ok := ModelScannerFromEnv(); ok {
		return d.WithScanner(sc)
	}
	return d
}
