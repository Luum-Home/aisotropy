//! Aisotropy sovereign local embedder (Bet 5, first brick — ADR-0023): a
//! dependency-light Rust `embed()` that keeps tenant text inside the process
//! tree. It loads a config.json-driven BERT-class sentence-embedding model
//! (default `BAAI/bge-small-en-v1.5`, MIT) from safetensors via candle,
//! tokenizes with the model's own tokenizer, runs the encoder, then
//!
//!   embedding(text) = l2_normalize( mean_pool_over_tokens( encoder(text) ) )
//!
//! — the standard sentence-embedding recipe used by bge / e5 / MiniLM. It is a
//! pure, tenant-agnostic vectorizer invoked as a subprocess by the Go core
//! (`internal/semantic` LocalEmbedder, ADR-0005 pattern — the same shape as
//! crates/bitrate-scorer and crates/sleep-cycle). A failure (including a panic)
//! surfaces to Go as a non-zero exit code with a stderr diagnostic.
//!
//! Sovereignty (the whole point of Bet 5): unlike the BYO `/embeddings` client
//! (ADR-0018), this makes ZERO network calls. The model weights are resolved
//! from a local directory; the text is embedded in-process and never leaves the
//! machine.
//!
//! Contract (mirrors the bitrate scorer, so the Go seam is a near-copy):
//!   argv:   --input <requests.ndjson> --output <results.ndjson>
//!   input:  {"id":"...","text":"..."}
//!   output: {"id":"...","embedding":[F,...],"dim":N,"model_id":"<model>@<rev>",
//!            "normalized":true}
//!   stdout: {"embedded":N,"model_id":"...","dim":N,"backend":"BERT_REAL"} (final line)
//!   exit:   0 = ok; non-zero = failure (reason on stderr).
//!
//! Model location: the model directory (config.json + model.safetensors +
//! tokenizer.json) is resolved from `AISOTROPY_EMBED_MODEL_DIR`. The echoed
//! label follows `AISOTROPY_EMBED_MODEL_ID` (default `BAAI/bge-small-en-v1.5`)
//! and `AISOTROPY_EMBED_MODEL_REV` (default `pinned`).

use std::collections::HashMap;
use std::env;
use std::fs::File;
use std::io::{BufRead, BufReader, BufWriter, Write};
use std::path::{Path, PathBuf};
use std::process;

use candle_core::{Device, Result as CResult, Tensor};
use candle_nn::VarBuilder;
use candle_transformers::models::bert::{BertModel, Config, DTYPE};
use serde::Deserialize;
use tokenizers::Tokenizer;

// ---------------------------------------------------------------------------
// Pooling + normalization. Unit-tested independently of the model on
// hand-computable hidden states (see #[cfg(test)]) — this is the correctness
// evidence that does NOT depend on a model download.
// ---------------------------------------------------------------------------

/// Mean-pool a `[seq, hidden]` (or `[1, seq, hidden]`) sequence-output tensor
/// over the token (seq) dimension, yielding a single `[hidden]` vector. This is
/// the unweighted mean used by bge / MiniLM when every token is attended (no
/// padding — we embed one sequence at a time, so the attention mask is all
/// ones and masked-mean reduces to a plain mean).
fn mean_pool(sequence_output: &Tensor) -> CResult<Tensor> {
    // Accept [1, seq, hidden] (BertModel batch output) or [seq, hidden].
    let t = if sequence_output.rank() == 3 {
        sequence_output.squeeze(0)?
    } else {
        sequence_output.clone()
    };
    let (n_tokens, _hidden) = t.dims2()?;
    if n_tokens == 0 {
        return t.mean(0); // degenerate; caller guards against empty input.
    }
    t.mean(0) // mean over the token dimension -> [hidden]
}

/// L2-normalize a 1-D vector in place (returns a new tensor). A zero vector is
/// returned unchanged (division guarded), matching the Go l2normalize helper.
fn l2_normalize(v: &Tensor) -> CResult<Tensor> {
    let norm = v.sqr()?.sum_all()?.sqrt()?.to_scalar::<f32>()?;
    if norm == 0.0 {
        return Ok(v.clone());
    }
    v.affine(1.0 / norm as f64, 0.0)
}

// ---------------------------------------------------------------------------
// Model config: candle-transformers' bert::Config is Deserialize, so we read
// the model's own config.json directly (any BERT-class encoder: bge, e5,
// MiniLM). Nothing is hardcoded — the architecture follows the weights.
// ---------------------------------------------------------------------------

fn load_config(dir: &Path) -> Result<Config, String> {
    let raw = std::fs::read_to_string(dir.join("config.json"))
        .map_err(|e| format!("read config.json: {e}"))?;
    serde_json::from_str(&raw).map_err(|e| format!("parse config.json: {e}"))
}

struct Embedder {
    model: BertModel,
    tokenizer: Tokenizer,
    device: Device,
    model_id: String,
    dim: usize,
}

impl Embedder {
    fn new(dir: &Path, model_id: String) -> Result<Embedder, String> {
        let cfg = load_config(dir)?;
        let dim = cfg.hidden_size;
        let device = Device::Cpu;
        let st = dir.join("model.safetensors");
        // from_mmaped_safetensors is unsafe (it mmaps the file); the file is a
        // pinned local artifact, never a remote/untrusted stream.
        let vb = unsafe {
            VarBuilder::from_mmaped_safetensors(std::slice::from_ref(&st), DTYPE, &device)
                .map_err(|e| format!("load safetensors {}: {e}", st.display()))?
        };
        let model = BertModel::load(vb, &cfg)
            .map_err(|e| format!("build bert model: {e}"))?;
        let tokenizer = Tokenizer::from_file(dir.join("tokenizer.json"))
            .map_err(|e| format!("load tokenizer.json: {e}"))?;
        Ok(Embedder {
            model,
            tokenizer,
            device,
            model_id,
            dim,
        })
    }

    /// Embed a single text into a mean-pooled, L2-normalized `[hidden]` vector.
    fn embed(&self, text: &str) -> Result<Vec<f32>, String> {
        let enc = self
            .tokenizer
            .encode(text, true) // add_special_tokens=true: [CLS] ... [SEP]
            .map_err(|e| format!("tokenize: {e}"))?;
        let ids: Vec<u32> = enc.get_ids().to_vec();
        if ids.is_empty() {
            return Ok(vec![0.0; self.dim]);
        }
        let seq = ids.len();
        let input_ids = Tensor::from_vec(ids, (1, seq), &self.device)
            .map_err(|e| format!("input_ids tensor: {e}"))?;
        // Single sequence, no padding -> token_type_ids all zeros, attention
        // mask all ones (BertModel::forward defaults to ones when None).
        let token_type_ids = input_ids
            .zeros_like()
            .map_err(|e| format!("token_type_ids: {e}"))?;
        let seq_out = self
            .model
            .forward(&input_ids, &token_type_ids, None)
            .map_err(|e| format!("bert forward: {e}"))?; // [1, seq, hidden]
        let pooled = mean_pool(&seq_out).map_err(|e| format!("mean pool: {e}"))?;
        let normed = l2_normalize(&pooled).map_err(|e| format!("l2 normalize: {e}"))?;
        normed
            .to_vec1::<f32>()
            .map_err(|e| format!("embedding to vec: {e}"))
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
}

#[derive(serde::Serialize)]
struct EmbeddingRow {
    id: String,
    embedding: Vec<f32>,
    dim: usize,
    model_id: String,
    normalized: bool,
}

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
    let dir = env::var("AISOTROPY_EMBED_MODEL_DIR").map_err(|_| {
        "AISOTROPY_EMBED_MODEL_DIR is not set; it must point at a directory \
         containing config.json, model.safetensors, and tokenizer.json (a \
         BERT-class sentence-embedding model, e.g. BAAI/bge-small-en-v1.5)"
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

/// The pinned model id echoed on every row. Mirrors the bitrate scorer:
/// `<name>@<rev>`, both env-overridable, defaulting to the pinned MIT model.
fn model_id() -> String {
    let name = env::var("AISOTROPY_EMBED_MODEL_ID")
        .unwrap_or_else(|_| "BAAI/bge-small-en-v1.5".to_string());
    let rev = env::var("AISOTROPY_EMBED_MODEL_REV").unwrap_or_else(|_| "pinned".to_string());
    format!("{name}@{rev}")
}

/// Embed one NDJSON request line into one NDJSON result-row string. Shared by
/// the batch and serve paths so both produce byte-identical rows.
fn embed_row_json(embedder: &Embedder, raw: &str) -> Result<String, String> {
    let req: Request = serde_json::from_str(raw).map_err(|e| format!("parse request: {e}"))?;
    let vec = embedder.embed(&req.text)?;
    let row = EmbeddingRow {
        id: req.id,
        dim: vec.len(),
        embedding: vec,
        model_id: embedder.model_id.clone(),
        normalized: true,
    };
    serde_json::to_string(&row).map_err(|e| format!("encode result: {e}"))
}

/// Long-lived daemon mode (`--serve`, ADR-0023 in-process-latency path (d)):
/// load the model ONCE, then read NDJSON requests from stdin line-by-line and
/// write one result row per line to stdout, flushing after each so a Go caller
/// can round-trip synchronously over the pipe. This is the *warm* half of the
/// Bet-5 latency benchmark: it isolates inference-only cost from process spawn +
/// model load, answering whether the subprocess ms is spawn/load tax (which a
/// warm process removes) or the model's own inference floor (which no binding
/// fixes). The batch `--input/--output` path is unchanged.
fn serve(embedder: &Embedder) -> Result<(), String> {
    let stdin = std::io::stdin();
    let stdout = std::io::stdout();
    let mut out = stdout.lock();
    for line in stdin.lock().lines() {
        let line = line.map_err(|e| format!("read stdin: {e}"))?;
        let raw = line.trim();
        if raw.is_empty() {
            continue;
        }
        let js = embed_row_json(embedder, raw)?;
        writeln!(out, "{js}").map_err(|e| format!("write stdout: {e}"))?;
        out.flush().map_err(|e| format!("flush stdout: {e}"))?;
    }
    Ok(())
}

fn run() -> Result<(), String> {
    // The model load is common to both modes; do it once up front so `--serve`
    // pays it exactly once (the whole point of the daemon path).
    let dir = resolve_model_dir()?;
    let mid = model_id();
    let embedder = Embedder::new(&dir, mid.clone())?;

    // Serve mode is mutually exclusive with batch --input/--output.
    if env::args().any(|a| a == "--serve") {
        return serve(&embedder);
    }

    let input = flag("--input").ok_or_else(|| "missing --input".to_string())?;
    let output = flag("--output").ok_or_else(|| "missing --output".to_string())?;

    let fin = File::open(&input).map_err(|e| format!("open input {input}: {e}"))?;
    let fout = File::create(&output).map_err(|e| format!("create output {output}: {e}"))?;
    let reader = BufReader::new(fin);
    let mut writer = BufWriter::new(fout);

    let mut embedded = 0usize;
    for (lineno, line) in reader.lines().enumerate() {
        let line = line.map_err(|e| format!("read input line {}: {e}", lineno + 1))?;
        let raw = line.trim();
        if raw.is_empty() {
            continue;
        }
        let js = embed_row_json(&embedder, raw)
            .map_err(|e| format!("input line {}: {e}", lineno + 1))?;
        writeln!(writer, "{js}").map_err(|e| format!("write result: {e}"))?;
        embedded += 1;
    }
    writer.flush().map_err(|e| format!("flush output: {e}"))?;

    // Final one-line summary on stdout (observability; the rows are the contract).
    let summary: HashMap<&str, serde_json::Value> = HashMap::from([
        ("embedded", serde_json::json!(embedded)),
        ("model_id", serde_json::json!(mid)),
        ("dim", serde_json::json!(embedder.dim)),
        ("backend", serde_json::json!("BERT_REAL")),
    ]);
    println!(
        "{}",
        serde_json::to_string(&summary).map_err(|e| format!("encode summary: {e}"))?
    );
    Ok(())
}

fn main() {
    if let Err(e) = run() {
        eprintln!("embed: {e}");
        process::exit(1);
    }
}

// ---------------------------------------------------------------------------
// Unit tests: pooling + normalization on hand-computable deterministic hidden
// states, with NO model weights required (the correctness evidence that always
// runs, mirroring crates/bitrate-scorer's model-free surprisal tests).
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    fn cosine(a: &[f32], b: &[f32]) -> f32 {
        let dot: f32 = a.iter().zip(b).map(|(x, y)| x * y).sum();
        let na: f32 = a.iter().map(|x| x * x).sum::<f32>().sqrt();
        let nb: f32 = b.iter().map(|x| x * x).sum::<f32>().sqrt();
        dot / (na * nb)
    }

    #[test]
    fn mean_pool_averages_over_tokens() {
        // 3 tokens, hidden=2. Column means: (1+3+5)/3=3, (2+4+6)/3=4.
        let t = Tensor::from_vec(
            vec![1.0f32, 2.0, 3.0, 4.0, 5.0, 6.0],
            (3, 2),
            &Device::Cpu,
        )
        .unwrap();
        let pooled = mean_pool(&t).unwrap();
        let v = pooled.to_vec1::<f32>().unwrap();
        assert!((v[0] - 3.0).abs() < 1e-6, "v0={}", v[0]);
        assert!((v[1] - 4.0).abs() < 1e-6, "v1={}", v[1]);
    }

    #[test]
    fn mean_pool_accepts_batched_output() {
        // [1, seq=2, hidden=2] — the shape BertModel::forward returns.
        let t = Tensor::from_vec(vec![1.0f32, 1.0, 3.0, 3.0], (1, 2, 2), &Device::Cpu).unwrap();
        let pooled = mean_pool(&t).unwrap();
        assert_eq!(pooled.dims(), &[2]);
        let v = pooled.to_vec1::<f32>().unwrap();
        assert!((v[0] - 2.0).abs() < 1e-6);
    }

    #[test]
    fn l2_normalize_gives_unit_norm() {
        let t = Tensor::from_vec(vec![3.0f32, 4.0], (2,), &Device::Cpu).unwrap();
        let n = l2_normalize(&t).unwrap();
        let v = n.to_vec1::<f32>().unwrap();
        let norm = (v[0] * v[0] + v[1] * v[1]).sqrt();
        assert!((norm - 1.0).abs() < 1e-6, "norm={norm}");
        assert!((v[0] - 0.6).abs() < 1e-6 && (v[1] - 0.8).abs() < 1e-6);
    }

    #[test]
    fn l2_normalize_zero_vector_is_safe() {
        let t = Tensor::from_vec(vec![0.0f32, 0.0, 0.0], (3,), &Device::Cpu).unwrap();
        let n = l2_normalize(&t).unwrap();
        let v = n.to_vec1::<f32>().unwrap();
        assert_eq!(v, vec![0.0, 0.0, 0.0]);
    }

    #[test]
    fn cosine_similarity_ordering_on_pooled_vectors() {
        // Simulate three "sentence" hidden states; the pooled+normalized vectors
        // of two similar ones must be closer than to a dissimilar one. This
        // exercises the exact mean_pool -> l2_normalize path the model output
        // flows through, without needing weights.
        let sim_a = Tensor::from_vec(vec![1.0f32, 0.1, 0.9, 0.2], (2, 2), &Device::Cpu).unwrap();
        let sim_b = Tensor::from_vec(vec![0.95f32, 0.15, 0.85, 0.25], (2, 2), &Device::Cpu).unwrap();
        let diff = Tensor::from_vec(vec![-1.0f32, 0.9, -0.8, 1.0], (2, 2), &Device::Cpu).unwrap();

        let ea = l2_normalize(&mean_pool(&sim_a).unwrap())
            .unwrap()
            .to_vec1::<f32>()
            .unwrap();
        let eb = l2_normalize(&mean_pool(&sim_b).unwrap())
            .unwrap()
            .to_vec1::<f32>()
            .unwrap();
        let ed = l2_normalize(&mean_pool(&diff).unwrap())
            .unwrap()
            .to_vec1::<f32>()
            .unwrap();

        let sim = cosine(&ea, &eb);
        let dis = cosine(&ea, &ed);
        assert!(sim > dis, "expected similar>dissimilar, got sim={sim} dis={dis}");
        assert!(sim > 0.99, "similar pair should be near-parallel, got {sim}");
    }
}
