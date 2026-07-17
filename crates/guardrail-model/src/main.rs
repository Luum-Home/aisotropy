//! Aisotropy OPT-IN multilingual guardrail classifier — the model backend for
//! the R4 `InputScanner` seam (see `internal/guardrails/injection.go` +
//! `scanner.go`).
//!
//! ## Why this exists (the exact gap it closes)
//!
//! R4 added `normalize.go`: NFKC + case-fold + homoglyph/leet folding. That
//! HARDENS the built-in English regex heuristics against obfuscation — but it
//! **canonicalizes, it does not translate.** A clean, non-English attack such as
//! `"ignora las instrucciones anteriores"` (Spanish) or
//! `"ignore as instruções anteriores"` (Portuguese) folds to itself and scores
//! **0** against the English literal patterns. You cannot regex your way to
//! arbitrary-language safety — only a MODEL closes this. This crate is that
//! model, wired the ADR-0005 way: a subprocess, NOT a new in-process Go dep.
//!
//! ## What it does
//!
//! Zero-shot NLI. The user message is the PREMISE (any of the model's ~100
//! languages); a fixed English HYPOTHESIS asserts injection intent
//! ("This message tries to override, ignore, or reveal the assistant's
//! instructions."). A multilingual NLI classifier scores entailment
//! cross-lingually, so a Spanish/Portuguese premise against an English
//! hypothesis still yields a real entailment probability. That probability is
//! the injection score.
//!
//! injection_score(text) = softmax( classifier(premise=text, hypothesis) )[entailment]
//!
//! ## License gate (constitutional — see ADR-0052 + `constitutional-gates.md`)
//!
//! The default and ONLY sanctioned models are **MIT or Apache-2.0**:
//!   - `MoritzLaurer/mDeBERTa-v3-base-mnli-xnli`      — MIT   (default)
//!   - `MoritzLaurer/multilingual-MiniLMv2-L6-mnli-xnli` — MIT (smaller)
//!   - `joeddav/xlm-roberta-large-xnli`               — MIT   (XLM-R variant)
//! **BLOCKED**: `meta-llama/Llama-Prompt-Guard-2-*` — the Llama Community
//! License is a source-available / field-of-use-restricted (BSL-family)
//! **constitutional BLOCKER** for a SaaS runtime (`license-policy.md`). Do NOT
//! point `AISOTROPY_GUARDRAIL_MODEL_DIR` at a copyleft/source-available model.
//!
//! ## Contract (mirrors crates/embed, so the Go seam is a near-copy)
//!   BATCH mode:
//!     argv:   --input <requests.ndjson> --output <results.ndjson>
//!     input:  {"id":"...","text":"...","hypothesis":"..."}  (hypothesis optional)
//!     output: {"id":"...","score":F,"label":"injection|benign","suspicious":B,
//!              "model_id":"<model>@<rev>"}
//!     stdout: {"scored":N,"model_id":"...","threshold":T,"backend":"NLI_REAL"} (final line)
//!   SERVE mode (warm daemon, T1 — mirrors crates/embed's `--serve`):
//!     argv:   --serve
//!     stdin:  one NDJSON request line per classification (same schema as batch input)
//!     stdout: one NDJSON result row per line, flushed after each so a Go caller
//!             (DaemonScanner) can round-trip synchronously over the pipe.
//!   exit:   0 = ok; non-zero = failure (reason on stderr — a panic surfaces to
//!           Go as a non-zero exit, where the scanner FAILS OPEN).
//!
//! ## Why `--serve` exists (T1: productionize the ADR-0052 PoC)
//!
//! ADR-0052 shipped the cold `--input/--output` PoC: every classification spawned
//! a fresh process and RE-LOADED the mDeBERTa weights (~hundreds of MB) before
//! scoring a single line. That is fine for a batch job but is a per-request tax
//! for an inline guardrail on the chat path. `--serve` loads the model ONCE and
//! then services requests over a pipe, exactly as crates/embed's DaemonEmbedder
//! does for the semantic tier — the same fix, the same shape. The batch path is
//! unchanged, so existing callers are unaffected.
//!
//! Model dir (config.json + model.safetensors + tokenizer.json) is resolved from
//! `AISOTROPY_GUARDRAIL_MODEL_DIR`. Weights DOWNLOAD on first provisioning (a
//! one-time `huggingface-cli download <model>` into that dir); the crate itself
//! makes ZERO network calls — it only reads the local directory.
//!
//! ## HONEST scope (PoC vs production)
//!   - The NDJSON contract, the CLI, and the pure scoring glue (softmax,
//!     entailment-index resolution, threshold verdict) are REAL and covered by
//!     model-free unit tests that always run (`cargo test`).
//!   - The model forward pass reuses candle-transformers' MAINTAINED
//!     `DebertaV2SeqClassificationModel` (not hand-rolled). It is exercised only
//!     when a real model dir is present; it is NOT run in CI (no weights there),
//!     so end-to-end multilingual ACCURACY is PoC-not-yet-benchmarked. The Go
//!     seam it plugs into IS proven end-to-end (fake-worker subprocess tests).

use std::env;
use std::fs::File;
use std::io::{BufRead, BufReader, BufWriter, Write};
use std::path::{Path, PathBuf};
use std::process;

use candle_core::{DType, Device, Tensor};
use candle_nn::VarBuilder;
use candle_transformers::models::debertav2::{Config, DebertaV2SeqClassificationModel, Id2Label};
use serde::Deserialize;
use tokenizers::Tokenizer;

// ---------------------------------------------------------------------------
// Pure scoring glue — unit-tested on hand-computable inputs with NO model
// weights required (the correctness evidence that always runs, mirroring
// crates/embed's model-free pooling tests).
// ---------------------------------------------------------------------------

/// Numerically stable softmax over a logit slice.
fn softmax(logits: &[f32]) -> Vec<f32> {
    if logits.is_empty() {
        return Vec::new();
    }
    let max = logits.iter().cloned().fold(f32::NEG_INFINITY, f32::max);
    let exps: Vec<f32> = logits.iter().map(|&l| (l - max).exp()).collect();
    let sum: f32 = exps.iter().sum();
    if sum == 0.0 {
        return vec![1.0 / logits.len() as f32; logits.len()];
    }
    exps.iter().map(|&e| e / sum).collect()
}

/// Resolve the index of the "entailment" class from the model's id2label map
/// (case-insensitive). NLI heads are conventionally ordered
/// [entailment, neutral, contradiction], so a map that lacks an explicit
/// "entailment" label (or an empty map) falls back to index 0.
fn entailment_index(id2label: &Id2Label) -> usize {
    for (id, label) in id2label {
        if label.to_lowercase() == "entailment" {
            return *id as usize;
        }
    }
    0
}

/// The injection score is the entailment probability. Guards an out-of-range
/// index (defensive — should never happen given a well-formed head).
fn injection_score(probs: &[f32], entailment_idx: usize) -> f32 {
    probs.get(entailment_idx).copied().unwrap_or(0.0)
}

/// A score at/above the threshold is an injection verdict.
fn verdict(score: f32, threshold: f32) -> (&'static str, bool) {
    if score >= threshold {
        ("injection", true)
    } else {
        ("benign", false)
    }
}

// ---------------------------------------------------------------------------
// Model.
// ---------------------------------------------------------------------------

/// Remap one safetensors tensor key from the HF sequence-classification export
/// layout to the layout candle's `DebertaV2SeqClassificationModel::load` expects
/// (T1 loader fix). The HF checkpoint prefixes every BASE-model tensor with
/// `deberta.` (e.g. `deberta.embeddings.word_embeddings.weight`) while leaving the
/// task head (`pooler.*`, `classifier.*`) at the root; candle wants the base model
/// at the root too. Stripping a single leading `deberta.` where present does
/// exactly that and leaves the already-root head tensors untouched. Pure and
/// unit-tested so the mapping cannot silently regress (the original bug was
/// invisible precisely because the model was never loaded in CI).
fn remap_base_key(key: &str) -> &str {
    key.strip_prefix("deberta.").unwrap_or(key)
}

fn load_config(dir: &Path) -> Result<Config, String> {
    let raw = std::fs::read_to_string(dir.join("config.json"))
        .map_err(|e| format!("read config.json: {e}"))?;
    serde_json::from_str(&raw).map_err(|e| format!("parse config.json: {e}"))
}

struct Classifier {
    model: DebertaV2SeqClassificationModel,
    tokenizer: Tokenizer,
    device: Device,
    entailment_idx: usize,
    model_id: String,
}

impl Classifier {
    fn new(dir: &Path, model_id: String) -> Result<Classifier, String> {
        let cfg = load_config(dir)?;
        let id2label = cfg.id2label.clone().ok_or_else(|| {
            "config.json has no id2label; cannot locate the entailment class".to_string()
        })?;
        let entailment_idx = entailment_index(&id2label);

        let device = Device::Cpu;
        let st = dir.join("model.safetensors");

        // Tensor-name remap (the fix that makes the REAL checkpoint load — T1).
        // candle's DebertaV2SeqClassificationModel::load expects the base-model
        // tensors at the ROOT (`embeddings.*`, `encoder.*`), with `pooler.*` and
        // `classifier.*` also at root. But the HF sequence-classification export
        // `MoritzLaurer/mDeBERTa-v3-base-mnli-xnli` prefixes every base-model
        // tensor with `deberta.` (e.g. `deberta.embeddings.word_embeddings.weight`)
        // while leaving pooler/classifier at root. Loading it verbatim fails with
        // "cannot find tensor embeddings.word_embeddings.weight". We therefore load
        // the tensors, strip a single leading `deberta.` where present (leaving the
        // already-root pooler/classifier untouched), normalize to F32, and build a
        // VarBuilder from the remapped map. This is a safe API (no mmap/unsafe) and
        // costs one in-memory load, paid once at daemon start — exactly the warm
        // path's design point.
        let raw = candle_core::safetensors::load(&st, &device)
            .map_err(|e| format!("load safetensors {}: {e}", st.display()))?;
        let mut tensors: std::collections::HashMap<String, Tensor> =
            std::collections::HashMap::with_capacity(raw.len());
        for (k, v) in raw {
            let key = remap_base_key(&k).to_string();
            let v = v
                .to_dtype(DType::F32)
                .map_err(|e| format!("cast tensor {key} to f32: {e}"))?;
            tensors.insert(key, v);
        }
        let vb = VarBuilder::from_tensors(tensors, DType::F32, &device);

        let model = DebertaV2SeqClassificationModel::load(vb, &cfg, Some(id2label))
            .map_err(|e| format!("build debertav2 seq-classification model: {e}"))?;
        let tokenizer = Tokenizer::from_file(dir.join("tokenizer.json"))
            .map_err(|e| format!("load tokenizer.json: {e}"))?;

        Ok(Classifier {
            model,
            tokenizer,
            device,
            entailment_idx,
            model_id,
        })
    }

    /// Score one (premise, hypothesis) pair -> entailment (injection) probability.
    /// A single sequence with no padding, so the attention mask is all-ones and
    /// token_type_ids are unused (mDeBERTa type_vocab_size = 0): both pass as
    /// None, matching DebertaV2Model::forward's defaults.
    fn score(&self, premise: &str, hypothesis: &str) -> Result<f32, String> {
        let enc = self
            .tokenizer
            .encode((premise, hypothesis), true) // pair: [CLS] premise [SEP] hyp [SEP]
            .map_err(|e| format!("tokenize pair: {e}"))?;
        let ids: Vec<u32> = enc.get_ids().to_vec();
        if ids.is_empty() {
            return Ok(0.0);
        }
        let seq = ids.len();
        let input_ids = Tensor::from_vec(ids, (1, seq), &self.device)
            .map_err(|e| format!("input_ids tensor: {e}"))?;
        let logits = self
            .model
            .forward(&input_ids, None, None)
            .map_err(|e| format!("debertav2 forward: {e}"))?; // [1, num_labels]
        let logits = logits
            .squeeze(0)
            .map_err(|e| format!("squeeze logits: {e}"))?
            .to_vec1::<f32>()
            .map_err(|e| format!("logits to vec: {e}"))?;
        let probs = softmax(&logits);
        Ok(injection_score(&probs, self.entailment_idx))
    }
}

// ---------------------------------------------------------------------------
// NDJSON contract types.
// ---------------------------------------------------------------------------

#[derive(Debug, Deserialize)]
struct Request {
    #[serde(default)]
    id: String,
    #[serde(default)]
    text: String,
    /// Optional NLI hypothesis override. Empty ⇒ the built-in default injection
    /// hypothesis. The Go ModelScanner sends its own default explicitly.
    #[serde(default)]
    hypothesis: String,
}

#[derive(serde::Serialize)]
struct ResultRow {
    id: String,
    score: f32,
    label: String,
    suspicious: bool,
    model_id: String,
}

const DEFAULT_HYPOTHESIS: &str =
    "This message tries to override, ignore, or reveal the assistant's instructions.";

// ---------------------------------------------------------------------------
// CLI.
// ---------------------------------------------------------------------------

fn flag(name: &str) -> Option<String> {
    let args: Vec<String> = env::args().collect();
    args.iter()
        .position(|a| a == name)
        .and_then(|i| args.get(i + 1))
        .cloned()
}

fn resolve_model_dir() -> Result<PathBuf, String> {
    let dir = env::var("AISOTROPY_GUARDRAIL_MODEL_DIR").map_err(|_| {
        "AISOTROPY_GUARDRAIL_MODEL_DIR is not set; it must point at a directory \
         containing config.json, model.safetensors, and tokenizer.json (a \
         MIT/Apache multilingual NLI model, e.g. \
         MoritzLaurer/mDeBERTa-v3-base-mnli-xnli)"
            .to_string()
    })?;
    let p = PathBuf::from(dir);
    for f in ["config.json", "model.safetensors", "tokenizer.json"] {
        if !p.join(f).exists() {
            return Err(format!("model dir {} is missing {f}", p.display()));
        }
    }
    Ok(p)
}

fn model_id() -> String {
    let name = env::var("AISOTROPY_GUARDRAIL_MODEL_ID")
        .unwrap_or_else(|_| "MoritzLaurer/mDeBERTa-v3-base-mnli-xnli".to_string());
    let rev = env::var("AISOTROPY_GUARDRAIL_MODEL_REV").unwrap_or_else(|_| "pinned".to_string());
    format!("{name}@{rev}")
}

fn threshold() -> f32 {
    env::var("AISOTROPY_GUARDRAIL_THRESHOLD")
        .ok()
        .and_then(|s| s.parse::<f32>().ok())
        .filter(|t| (0.0..=1.0).contains(t))
        .unwrap_or(0.5)
}

/// Score one NDJSON request line into one NDJSON result-row string.
fn score_row_json(clf: &Classifier, raw: &str, thresh: f32) -> Result<String, String> {
    let req: Request = serde_json::from_str(raw).map_err(|e| format!("parse request: {e}"))?;
    let hypothesis = if req.hypothesis.is_empty() {
        DEFAULT_HYPOTHESIS
    } else {
        &req.hypothesis
    };
    let score = clf.score(&req.text, hypothesis)?;
    let (label, suspicious) = verdict(score, thresh);
    let row = ResultRow {
        id: req.id,
        score,
        label: label.to_string(),
        suspicious,
        model_id: clf.model_id.clone(),
    };
    serde_json::to_string(&row).map_err(|e| format!("encode result: {e}"))
}

/// Long-lived daemon mode (`--serve`, T1 warm path — mirrors crates/embed's
/// `serve`): the model is already loaded ONCE by `run`, so here we only read
/// NDJSON requests from stdin line-by-line and write one result row per line to
/// stdout, flushing after each so a Go caller (DaemonScanner) can round-trip
/// synchronously over the pipe. An empty line is skipped. A malformed request
/// line is a hard error (non-zero exit) — the Go side tears the daemon down and
/// respawns, so the pipe is never left half-drained. The batch `--input/--output`
/// path is unchanged; the two modes are mutually exclusive.
fn serve(clf: &Classifier, thresh: f32) -> Result<(), String> {
    let stdin = std::io::stdin();
    let stdout = std::io::stdout();
    let mut out = stdout.lock();
    for line in stdin.lock().lines() {
        let line = line.map_err(|e| format!("read stdin: {e}"))?;
        let raw = line.trim();
        if raw.is_empty() {
            continue;
        }
        let js = score_row_json(clf, raw, thresh)?;
        writeln!(out, "{js}").map_err(|e| format!("write stdout: {e}"))?;
        out.flush().map_err(|e| format!("flush stdout: {e}"))?;
    }
    Ok(())
}

fn run() -> Result<(), String> {
    // The model load is common to both modes; do it once up front so `--serve`
    // pays it exactly once (the whole point of the warm daemon path).
    let dir = resolve_model_dir()?;
    let mid = model_id();
    let thresh = threshold();
    let clf = Classifier::new(&dir, mid.clone())?;

    // Serve mode is mutually exclusive with batch --input/--output.
    if env::args().any(|a| a == "--serve") {
        return serve(&clf, thresh);
    }

    let input = flag("--input").ok_or_else(|| "missing --input".to_string())?;
    let output = flag("--output").ok_or_else(|| "missing --output".to_string())?;

    let fin = File::open(&input).map_err(|e| format!("open input {input}: {e}"))?;
    let fout = File::create(&output).map_err(|e| format!("create output {output}: {e}"))?;
    let reader = BufReader::new(fin);
    let mut writer = BufWriter::new(fout);

    let mut scored = 0usize;
    for (lineno, line) in reader.lines().enumerate() {
        let line = line.map_err(|e| format!("read input line {}: {e}", lineno + 1))?;
        let raw = line.trim();
        if raw.is_empty() {
            continue;
        }
        let js = score_row_json(&clf, raw, thresh)
            .map_err(|e| format!("input line {}: {e}", lineno + 1))?;
        writeln!(writer, "{js}").map_err(|e| format!("write result: {e}"))?;
        scored += 1;
    }
    writer.flush().map_err(|e| format!("flush output: {e}"))?;

    println!(
        "{{\"scored\":{scored},\"model_id\":\"{mid}\",\"threshold\":{thresh},\"backend\":\"NLI_REAL\"}}"
    );
    Ok(())
}

fn main() {
    if let Err(e) = run() {
        eprintln!("guardrail-model: {e}");
        process::exit(1);
    }
}

// ---------------------------------------------------------------------------
// Unit tests: the pure scoring glue on hand-computable inputs, NO model weights
// required (always runs, mirroring crates/embed's model-free tests).
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    #[test]
    fn remap_base_key_strips_only_leading_deberta_prefix() {
        // Base-model tensors get `deberta.` stripped so candle finds them at root.
        assert_eq!(
            remap_base_key("deberta.embeddings.word_embeddings.weight"),
            "embeddings.word_embeddings.weight"
        );
        assert_eq!(
            remap_base_key("deberta.encoder.rel_embeddings.weight"),
            "encoder.rel_embeddings.weight"
        );
        // Task-head tensors are already at root and MUST be left untouched.
        assert_eq!(remap_base_key("classifier.weight"), "classifier.weight");
        assert_eq!(remap_base_key("pooler.dense.bias"), "pooler.dense.bias");
        // Only a LEADING prefix is stripped (never an interior occurrence).
        assert_eq!(remap_base_key("encoder.deberta.foo"), "encoder.deberta.foo");
    }

    #[test]
    fn softmax_sums_to_one_and_orders() {
        let p = softmax(&[2.0, 1.0, 0.0]);
        let sum: f32 = p.iter().sum();
        assert!((sum - 1.0).abs() < 1e-6, "sum={sum}");
        assert!(p[0] > p[1] && p[1] > p[2], "not monotonic: {p:?}");
    }

    #[test]
    fn softmax_empty_and_degenerate() {
        assert!(softmax(&[]).is_empty());
        // All-equal logits -> uniform.
        let p = softmax(&[3.0, 3.0, 3.0]);
        for v in p {
            assert!((v - 1.0 / 3.0).abs() < 1e-6);
        }
    }

    #[test]
    fn entailment_index_from_id2label_case_insensitive() {
        let mut m: Id2Label = HashMap::new();
        m.insert(0, "ENTAILMENT".to_string());
        m.insert(1, "neutral".to_string());
        m.insert(2, "contradiction".to_string());
        assert_eq!(entailment_index(&m), 0);

        // A head with a different order must be resolved by NAME, not position.
        let mut m2: Id2Label = HashMap::new();
        m2.insert(0, "contradiction".to_string());
        m2.insert(1, "neutral".to_string());
        m2.insert(2, "Entailment".to_string());
        assert_eq!(entailment_index(&m2), 2);
    }

    #[test]
    fn entailment_index_fallback_to_zero() {
        let empty: Id2Label = HashMap::new();
        assert_eq!(entailment_index(&empty), 0);
    }

    #[test]
    fn injection_score_reads_entailment_prob() {
        let probs = [0.7f32, 0.2, 0.1]; // entailment-heavy
        assert!((injection_score(&probs, 0) - 0.7).abs() < 1e-6);
        // Out-of-range index is guarded to 0.0, never a panic.
        assert_eq!(injection_score(&probs, 9), 0.0);
    }

    #[test]
    fn verdict_threshold_boundary() {
        assert_eq!(verdict(0.5, 0.5), ("injection", true)); // >= is inclusive
        assert_eq!(verdict(0.49, 0.5), ("benign", false));
        assert_eq!(verdict(0.9, 0.5), ("injection", true));
    }

    #[test]
    fn end_to_end_scoring_glue_on_synthetic_logits() {
        // Simulate a classifier that strongly entails injection: logits favor
        // the entailment class. This exercises softmax -> injection_score ->
        // verdict exactly as the model output flows, without weights.
        let mut id2label: Id2Label = HashMap::new();
        id2label.insert(0, "entailment".to_string());
        id2label.insert(1, "neutral".to_string());
        id2label.insert(2, "contradiction".to_string());
        let ent = entailment_index(&id2label);

        let injection_logits = [4.0f32, 0.5, -1.0];
        let score = injection_score(&softmax(&injection_logits), ent);
        assert!(
            score > 0.9,
            "expected strong injection entailment, got {score}"
        );
        assert_eq!(verdict(score, 0.5).0, "injection");

        let benign_logits = [-2.0f32, 0.2, 3.5]; // contradiction-heavy
        let score2 = injection_score(&softmax(&benign_logits), ent);
        assert!(
            score2 < 0.1,
            "expected low injection entailment, got {score2}"
        );
        assert_eq!(verdict(score2, 0.5).0, "benign");
    }
}
