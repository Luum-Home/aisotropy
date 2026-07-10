//! Aisotropy bitrate scorer (Bet 6, Phase 2 — ADR-0014 Option B): a sovereign
//! Rust port of the validated Python bits-per-token scorer
//! (`spikes/compression/surprisal.py` + `bitrate_scorer.py`). It loads a pinned
//! distilgpt2 (Apache-2.0) from safetensors via candle, tokenizes with the model's
//! own tokenizer, runs a causal forward pass, and computes
//!
//!   bits(text | context) = − Σ log₂ P(token_i | context + preceding text tokens)
//!
//! over the `text` tokens only — the exact surprisal `surprisal.py::_surprisal_bits_lm`
//! computes, re-derived in Rust. It is a pure, tenant-agnostic surprisal calculator
//! invoked as an offline batch subprocess by the Go core (`internal/bitrate`,
//! ADR-0005 pattern). A failure (including a panic) surfaces to Go as a non-zero
//! exit code with a stderr diagnostic.
//!
//! Contract (identical to the Phase-1 Python scorer, so `internal/bitrate/worker.go`
//! works unchanged):
//!   argv:   --input <requests.ndjson> --output <results.ndjson>
//!   input:  {"id":"...","text":"...","context":"..."}
//!   output: {"id":"...","bits_total":F,"bits_per_token":F,"n_tokens":N,
//!            "model_id":"distilgpt2@<rev-or-pinned>","unit":"subword-distilgpt2"}
//!   stdout: {"scored":N,"model_id":"...","backend":"LM_REAL"}   (final line)
//!   exit:   0 = ok; non-zero = failure (reason on stderr).
//!
//! Model location: the model directory (config.json + model.safetensors +
//! tokenizer.json) is resolved from the `AISOTROPY_BITRATE_MODEL_DIR` env var —
//! the sovereign analogue of the Python scorer's HF cache. This keeps the Go
//! contract (only --input/--output on argv) identical to Phase 1.

use std::collections::HashMap;
use std::env;
use std::fs::File;
use std::io::{BufRead, BufReader, BufWriter, Write};
use std::path::{Path, PathBuf};
use std::process;

use candle_core::{DType, Device, Result as CResult, Tensor, D};
use serde::Deserialize;
use tokenizers::Tokenizer;

const LN2: f64 = std::f64::consts::LN_2;
const MASK_NEG: f32 = -1e30; // additive causal mask fill; exp(-1e30)=0 in f32.

// ---------------------------------------------------------------------------
// GPT-2 config (subset of HF config.json needed for the forward pass).
// ---------------------------------------------------------------------------

#[derive(Debug, Deserialize)]
struct Gpt2Config {
    n_layer: usize,
    n_head: usize,
    n_embd: usize,
    vocab_size: usize,
    #[serde(default = "default_eps")]
    layer_norm_epsilon: f64,
    #[serde(default = "default_bos")]
    bos_token_id: u32,
    #[serde(default = "default_bos")]
    eos_token_id: u32,
}

fn default_eps() -> f64 {
    1e-5
}
fn default_bos() -> u32 {
    50256
}

// ---------------------------------------------------------------------------
// Building blocks. HF GPT-2 uses Conv1D (weight stored [in, out], forward
// y = x @ W + b) rather than nn.Linear (which would need a transpose), so the
// linear layers here matmul the weight directly.
// ---------------------------------------------------------------------------

struct Conv1D {
    weight: Tensor, // [in, out]
    bias: Tensor,   // [out]
}

impl Conv1D {
    fn forward(&self, x: &Tensor) -> CResult<Tensor> {
        // x: [seq, in] -> [seq, out]
        x.matmul(&self.weight)?.broadcast_add(&self.bias)
    }
}

struct LayerNorm {
    weight: Tensor,
    bias: Tensor,
    eps: f64,
}

impl LayerNorm {
    fn forward(&self, x: &Tensor) -> CResult<Tensor> {
        // Biased variance over the last dim (matches torch.nn.LayerNorm).
        let mean = x.mean_keepdim(D::Minus1)?;
        let centered = x.broadcast_sub(&mean)?;
        let var = centered.sqr()?.mean_keepdim(D::Minus1)?;
        let denom = (var + self.eps)?.sqrt()?;
        let norm = centered.broadcast_div(&denom)?;
        norm.broadcast_mul(&self.weight)?.broadcast_add(&self.bias)
    }
}

struct Block {
    ln_1: LayerNorm,
    c_attn: Conv1D,
    c_proj: Conv1D,
    ln_2: LayerNorm,
    mlp_c_fc: Conv1D,
    mlp_c_proj: Conv1D,
    n_head: usize,
    head_dim: usize,
}

impl Block {
    fn attn(&self, x: &Tensor, mask: &Tensor) -> CResult<Tensor> {
        let (seq, embd) = x.dims2()?;
        let qkv = self.c_attn.forward(x)?; // [seq, 3*embd]
        let q = qkv.narrow(1, 0, embd)?;
        let k = qkv.narrow(1, embd, embd)?;
        let v = qkv.narrow(1, 2 * embd, embd)?;

        // [seq, embd] -> [n_head, seq, head_dim]
        let split = |t: &Tensor| -> CResult<Tensor> {
            t.reshape((seq, self.n_head, self.head_dim))?
                .transpose(0, 1)?
                .contiguous()
        };
        let q = split(&q)?;
        let k = split(&k)?;
        let v = split(&v)?;

        let scale = 1.0 / (self.head_dim as f64).sqrt();
        // att: [n_head, seq, seq]
        let att = (q.matmul(&k.transpose(1, 2)?)? * scale)?;
        let att = att.broadcast_add(mask)?; // causal mask [seq, seq] broadcast over heads
        let att = candle_nn::ops::softmax(&att, D::Minus1)?;
        let ctx = att.matmul(&v)?; // [n_head, seq, head_dim]
        let ctx = ctx.transpose(0, 1)?.contiguous()?.reshape((seq, embd))?;
        self.c_proj.forward(&ctx)
    }

    fn mlp(&self, x: &Tensor) -> CResult<Tensor> {
        // gelu_new == candle's tanh-approx gelu.
        let h = self.mlp_c_fc.forward(x)?.gelu()?;
        self.mlp_c_proj.forward(&h)
    }

    fn forward(&self, x: &Tensor, mask: &Tensor) -> CResult<Tensor> {
        // Pre-LN residual blocks.
        let x = (x + self.attn(&self.ln_1.forward(x)?, mask)?)?;
        let x = (&x + self.mlp(&self.ln_2.forward(&x)?)?)?;
        Ok(x)
    }
}

struct Gpt2 {
    wte: Tensor, // [vocab, embd]
    wpe: Tensor, // [n_positions, embd]
    blocks: Vec<Block>,
    ln_f: LayerNorm,
    device: Device,
    cfg_bos: u32,
    cfg_eos: u32,
}

impl Gpt2 {
    fn load(dir: &Path) -> Result<Gpt2, String> {
        let cfg: Gpt2Config = {
            let raw = std::fs::read_to_string(dir.join("config.json"))
                .map_err(|e| format!("read config.json: {e}"))?;
            serde_json::from_str(&raw).map_err(|e| format!("parse config.json: {e}"))?
        };
        let device = Device::Cpu;
        let st = dir.join("model.safetensors");
        let w = candle_core::safetensors::load(&st, &device)
            .map_err(|e| format!("load safetensors {}: {e}", st.display()))?;

        let get = |name: &str| -> Result<Tensor, String> {
            w.get(name)
                .ok_or_else(|| format!("missing weight {name}"))
                .and_then(|t| {
                    t.to_dtype(DType::F32)
                        .map_err(|e| format!("cast {name}: {e}"))
                })
        };
        let ln = |prefix: &str| -> Result<LayerNorm, String> {
            Ok(LayerNorm {
                weight: get(&format!("{prefix}.weight"))?,
                bias: get(&format!("{prefix}.bias"))?,
                eps: cfg.layer_norm_epsilon,
            })
        };
        let conv = |prefix: &str| -> Result<Conv1D, String> {
            Ok(Conv1D {
                weight: get(&format!("{prefix}.weight"))?,
                bias: get(&format!("{prefix}.bias"))?,
            })
        };

        let head_dim = cfg.n_embd / cfg.n_head;
        let mut blocks = Vec::with_capacity(cfg.n_layer);
        for i in 0..cfg.n_layer {
            let p = format!("transformer.h.{i}");
            blocks.push(Block {
                ln_1: ln(&format!("{p}.ln_1"))?,
                c_attn: conv(&format!("{p}.attn.c_attn"))?,
                c_proj: conv(&format!("{p}.attn.c_proj"))?,
                ln_2: ln(&format!("{p}.ln_2"))?,
                mlp_c_fc: conv(&format!("{p}.mlp.c_fc"))?,
                mlp_c_proj: conv(&format!("{p}.mlp.c_proj"))?,
                n_head: cfg.n_head,
                head_dim,
            });
        }

        let wte = get("transformer.wte.weight")?;
        let wpe = get("transformer.wpe.weight")?;
        // sanity: vocab dimension
        if wte.dim(0).map_err(|e| e.to_string())? != cfg.vocab_size {
            return Err("wte vocab dim != config.vocab_size".into());
        }

        Ok(Gpt2 {
            wte,
            wpe,
            blocks,
            ln_f: ln("transformer.ln_f")?,
            device,
            cfg_bos: cfg.bos_token_id,
            cfg_eos: cfg.eos_token_id,
        })
    }

    fn causal_mask(&self, seq: usize) -> CResult<Tensor> {
        let mut data = vec![0f32; seq * seq];
        for i in 0..seq {
            for j in (i + 1)..seq {
                data[i * seq + j] = MASK_NEG;
            }
        }
        Tensor::from_vec(data, (seq, seq), &self.device)
    }

    /// Forward pass over a single token sequence, returning logits [seq, vocab].
    fn logits(&self, ids: &[u32]) -> CResult<Tensor> {
        let seq = ids.len();
        let ids_t = Tensor::from_vec(ids.to_vec(), (seq,), &self.device)?;
        let tok_emb = self.wte.index_select(&ids_t, 0)?; // [seq, embd]
        let positions: Vec<u32> = (0..seq as u32).collect();
        let pos_t = Tensor::from_vec(positions, (seq,), &self.device)?;
        let pos_emb = self.wpe.index_select(&pos_t, 0)?; // [seq, embd]
        let mut x = (tok_emb + pos_emb)?;

        let mask = self.causal_mask(seq)?;
        for block in &self.blocks {
            x = block.forward(&x, &mask)?;
        }
        let x = self.ln_f.forward(&x)?;
        // Tied LM head: logits = hidden @ wteᵀ  -> [seq, vocab]
        x.matmul(&self.wte.t()?)
    }
}

// ---------------------------------------------------------------------------
// Surprisal math (unit-tested independently of the model on hand-computable
// logits — see #[cfg(test)]).
// ---------------------------------------------------------------------------

/// Total bits = − Σ log₂ P(seq[i] | seq[<i]) for i in [n_ctx, n_ctx+n_text),
/// where the logit row at position i−1 predicts the token at position i.
/// `logits` is [seq, vocab] raw (pre-softmax) logits.
fn total_bits_from_logits(
    logits: &Tensor,
    seq: &[u32],
    n_ctx: usize,
    n_text: usize,
) -> CResult<f64> {
    let log_probs = candle_nn::ops::log_softmax(logits, D::Minus1)?; // natural log
    let mut total_bits = 0.0f64;
    // The logit row at position i−1 predicts the token at position i; score only
    // the `text` tokens (positions n_ctx .. n_ctx+n_text).
    for (offset, &tok) in seq[n_ctx..n_ctx + n_text].iter().enumerate() {
        let pred_pos = n_ctx + offset - 1;
        let token_id = tok as usize;
        let logp_natural = log_probs
            .get(pred_pos)?
            .get(token_id)?
            .to_scalar::<f32>()? as f64;
        total_bits += -logp_natural / LN2;
    }
    Ok(total_bits)
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
    #[serde(default)]
    context: String,
}

struct Scorer {
    model: Gpt2,
    tokenizer: Tokenizer,
    model_id: String,
    unit: String,
}

impl Scorer {
    fn new(dir: &Path, model_id: String) -> Result<Scorer, String> {
        let model = Gpt2::load(dir)?;
        let tokenizer = Tokenizer::from_file(dir.join("tokenizer.json"))
            .map_err(|e| format!("load tokenizer.json: {e}"))?;
        let unit = unit_for(&model_id);
        Ok(Scorer {
            model,
            tokenizer,
            model_id,
            unit,
        })
    }

    fn encode(&self, s: &str) -> Result<Vec<u32>, String> {
        // add_special_tokens=false to match the Python HF tokenizer default for
        // GPT-2 (no automatic BOS/EOS), verified against the golden token ids.
        let enc = self
            .tokenizer
            .encode(s, false)
            .map_err(|e| format!("tokenize: {e}"))?;
        Ok(enc.get_ids().to_vec())
    }

    /// The Rust port of `surprisal.py::_surprisal_bits_lm`.
    fn surprisal(&self, text: &str, context: &str) -> Result<(f64, f64, usize), String> {
        // Context tokens; empty context -> a single BOS/EOS token, exactly as the
        // Python scorer does (so the text's real first token is measured under a
        // genuine 1-token context, consistent across sentence lengths).
        let mut ctx_ids = if context.is_empty() {
            Vec::new()
        } else {
            self.encode(context)?
        };
        if ctx_ids.is_empty() {
            let bos = if self.model.cfg_bos != u32::MAX {
                self.model.cfg_bos
            } else {
                self.model.cfg_eos
            };
            ctx_ids = vec![bos];
        }
        let text_ids = self.encode(text)?;
        let n_ctx = ctx_ids.len();
        let n_text = text_ids.len();

        if n_text == 0 {
            return Ok((0.0, 0.0, 0));
        }

        let mut full = ctx_ids;
        full.extend_from_slice(&text_ids);

        let logits = self
            .model
            .logits(&full)
            .map_err(|e| format!("forward pass: {e}"))?;
        let total_bits = total_bits_from_logits(&logits, &full, n_ctx, n_text)
            .map_err(|e| format!("surprisal: {e}"))?;
        let bpt = total_bits / n_text as f64;
        Ok((total_bits, bpt, n_text))
    }
}

// ---------------------------------------------------------------------------
// CLI.
// ---------------------------------------------------------------------------

#[derive(serde::Serialize)]
struct Result_ {
    id: String,
    bits_total: f64,
    bits_per_token: f64,
    n_tokens: usize,
    model_id: String,
    unit: String,
}

fn flag(name: &str) -> Option<String> {
    let args: Vec<String> = env::args().collect();
    args.iter()
        .position(|a| a == name)
        .and_then(|i| args.get(i + 1))
        .cloned()
}

fn resolve_model_dir() -> Result<PathBuf, String> {
    let dir = env::var("AISOTROPY_BITRATE_MODEL_DIR").map_err(|_| {
        "AISOTROPY_BITRATE_MODEL_DIR is not set; it must point at a directory \
         containing config.json, model.safetensors, and tokenizer.json (distilgpt2)"
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

/// The pinned model id echoed on every row. If AISOTROPY_BITRATE_MODEL_REV is
/// set (e.g. the HF snapshot revision), it is used; otherwise '@pinned', exactly
/// mirroring the Phase-1 Python scorer's '<model>@pinned'.
fn model_id() -> String {
    // The model NAME is config/env-provided, not hardcoded: the forward pass is
    // config.json-driven (any GPT-2-class model), so the echoed label must follow
    // whatever weights AISOTROPY_BITRATE_MODEL_DIR actually holds. Default is the
    // pinned eval model DeepESP/gpt2-spanish (MIT, Spanish-fluent — ADR-0015).
    let name =
        env::var("AISOTROPY_BITRATE_MODEL_ID").unwrap_or_else(|_| "DeepESP/gpt2-spanish".to_string());
    let rev = env::var("AISOTROPY_BITRATE_MODEL_REV").unwrap_or_else(|_| "pinned".to_string());
    format!("{name}@{rev}")
}

/// unit_for derives the subword-unit label from the model id, e.g.
/// "DeepESP/gpt2-spanish@pinned" -> "subword-gpt2-spanish".
fn unit_for(model_id: &str) -> String {
    let base = model_id.split('@').next().unwrap_or(model_id);
    let short = base.rsplit('/').next().unwrap_or(base);
    format!("subword-{short}")
}

fn run() -> Result<(), String> {
    let input = flag("--input").ok_or_else(|| "missing --input".to_string())?;
    let output = flag("--output").ok_or_else(|| "missing --output".to_string())?;
    let dir = resolve_model_dir()?;
    let mid = model_id();

    let scorer = Scorer::new(&dir, mid.clone())?;

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
        let req: Request = serde_json::from_str(raw)
            .map_err(|e| format!("input line {}: {e}", lineno + 1))?;
        let (bits_total, bpt, n) = scorer.surprisal(&req.text, &req.context)?;
        let row = Result_ {
            id: req.id,
            bits_total,
            bits_per_token: bpt,
            n_tokens: n,
            model_id: scorer.model_id.clone(),
            unit: scorer.unit.clone(),
        };
        let js = serde_json::to_string(&row).map_err(|e| format!("encode result: {e}"))?;
        writeln!(writer, "{js}").map_err(|e| format!("write result: {e}"))?;
        scored += 1;
    }
    writer.flush().map_err(|e| format!("flush output: {e}"))?;

    // Final one-line summary on stdout (observability; the result rows are the
    // contract). backend is always LM_REAL — this scorer has no n-gram fallback.
    let summary: HashMap<&str, serde_json::Value> = HashMap::from([
        ("scored", serde_json::json!(scored)),
        ("model_id", serde_json::json!(mid)),
        ("backend", serde_json::json!("LM_REAL")),
    ]);
    println!(
        "{}",
        serde_json::to_string(&summary).map_err(|e| format!("encode summary: {e}"))?
    );
    Ok(())
}

fn main() {
    if let Err(e) = run() {
        eprintln!("bitrate-scorer: {e}");
        process::exit(1);
    }
}

// ---------------------------------------------------------------------------
// Unit tests: the surprisal math on hand-computable deterministic logits, with
// NO model weights required (this is the correctness evidence that does not
// depend on a model download).
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // Build a [seq, vocab] logits tensor from a nested vec.
    fn logits(rows: &[&[f32]]) -> Tensor {
        let seq = rows.len();
        let vocab = rows[0].len();
        let flat: Vec<f32> = rows.iter().flat_map(|r| r.iter().copied()).collect();
        Tensor::from_vec(flat, (seq, vocab), &Device::Cpu).unwrap()
    }

    #[test]
    fn uniform_logits_give_log2_vocab_bits_per_token() {
        // vocab=2, uniform logits => P=0.5 => -log2(0.5)=1 bit for each scored token.
        // seq: [ctx=0, t1=1, t2=0]; score positions 1,2 (n_ctx=1, n_text=2).
        // Prediction rows: pos0 predicts t1, pos1 predicts t2. Rows uniform => 1 bit each.
        let l = logits(&[&[0.0, 0.0], &[0.0, 0.0], &[0.0, 0.0]]);
        let seq = [0u32, 1, 0];
        let total = total_bits_from_logits(&l, &seq, 1, 2).unwrap();
        assert!((total - 2.0).abs() < 1e-6, "total={total}");
        assert!((total / 2.0 - 1.0).abs() < 1e-6);
    }

    #[test]
    fn skewed_logits_match_hand_computed_bits() {
        // vocab=2. Prediction row = [ln(3), 0] => softmax = [3/4, 1/4].
        // Score two tokens: first is id 0 (-log2(3/4)), second is id 1 (-log2(1/4)=2).
        let ln3 = 3f32.ln();
        let l = logits(&[&[ln3, 0.0], &[ln3, 0.0], &[ln3, 0.0]]);
        // seq positions: [ctx, tok0=0, tok1=1]; n_ctx=1, n_text=2.
        let seq = [0u32, 0, 1];
        let total = total_bits_from_logits(&l, &seq, 1, 2).unwrap();
        let expected = -(0.75f64.log2()) + -(0.25f64.log2()); // ≈ 0.415 + 2.0
        assert!((total - expected).abs() < 1e-5, "total={total} expected={expected}");
    }

    #[test]
    fn single_token_high_confidence_low_bits() {
        // A near-one-hot prediction => very low bits for the correct token.
        let l = logits(&[&[100.0, 0.0, 0.0], &[100.0, 0.0, 0.0]]);
        let seq = [7u32 % 3, 0]; // ctx then token id 0 (the confident one)
        let total = total_bits_from_logits(&l, &seq, 1, 1).unwrap();
        assert!(total < 1e-3, "total={total} should be ~0 bits");
    }
}
