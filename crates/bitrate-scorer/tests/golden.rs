//! Golden-vector equivalence test (ADR-0014 swap-time correctness check).
//!
//! The only legitimate cross-implementation comparison: run this Rust scorer and
//! the validated Python scorer on the SAME (text, context) pair and assert their
//! bits_per_token agree within a small tolerance (dtype / kernel differences are
//! expected). The Python golden numbers below were produced by
//! `spikes/compression/bitrate_scorer.py` (model distilgpt2, Apache-2.0) on the
//! exact inputs encoded here — see the crate README / component doc.
//!
//! This test requires the pinned model weights, so it is SKIPPED (passes trivially
//! with a printed notice) unless `AISOTROPY_BITRATE_MODEL_DIR` points at a
//! directory with config.json + model.safetensors + tokenizer.json. The
//! deterministic-logits unit tests in `src/main.rs` are the model-free correctness
//! evidence that always runs.

use std::io::Write;
use std::path::PathBuf;
use std::process::Command;

// Python golden reference (bits_per_token), from bitrate_scorer.py on distilgpt2.
const G1_TEXT: &str = "el usuario usa una computadora todos los dias";
const G1_CONTEXT: &str = "El usuario trabaja con computadoras.";
const G1_PY_BPT: f64 = 5.942273951046356;

const G2_TEXT: &str = "the quick brown fox jumps over the lazy dog";
const G2_CONTEXT: &str = "";
const G2_PY_BPT: f64 = 8.996184532293576;

// Tolerance: observed max delta was ~3.3e-5; 1e-2 leaves generous headroom for
// platform BLAS / dtype variation while still catching a real math regression.
const TOL: f64 = 1e-2;

fn model_dir() -> Option<PathBuf> {
    let d = std::env::var("AISOTROPY_BITRATE_MODEL_DIR").ok()?;
    let p = PathBuf::from(d);
    if p.join("model.safetensors").exists() && p.join("tokenizer.json").exists() {
        Some(p)
    } else {
        None
    }
}

fn bin() -> PathBuf {
    // The test binary lives in target/<profile>/deps; the scorer binary is two
    // levels up (target/<profile>/bitrate-scorer).
    let mut p = std::env::current_exe().unwrap();
    p.pop(); // deps
    p.pop(); // profile dir
    p.push("bitrate-scorer");
    p
}

#[derive(serde::Deserialize)]
struct Row {
    id: String,
    bits_per_token: f64,
    n_tokens: usize,
}

#[test]
fn golden_vector_equivalence_vs_python() {
    let Some(dir) = model_dir() else {
        eprintln!(
            "SKIP golden_vector_equivalence_vs_python: set AISOTROPY_BITRATE_MODEL_DIR \
             to the distilgpt2 snapshot dir to run the cross-impl parity check."
        );
        return;
    };

    let tmp = std::env::temp_dir();
    let input = tmp.join("bitrate_golden_in.ndjson");
    let output = tmp.join("bitrate_golden_out.ndjson");

    let mut f = std::fs::File::create(&input).unwrap();
    writeln!(
        f,
        "{}",
        serde_json::json!({"id":"g1","text":G1_TEXT,"context":G1_CONTEXT})
    )
    .unwrap();
    writeln!(
        f,
        "{}",
        serde_json::json!({"id":"g2","text":G2_TEXT,"context":G2_CONTEXT})
    )
    .unwrap();
    drop(f);

    let status = Command::new(bin())
        .env("AISOTROPY_BITRATE_MODEL_DIR", &dir)
        .args(["--input", input.to_str().unwrap(), "--output", output.to_str().unwrap()])
        .status()
        .expect("run bitrate-scorer");
    assert!(status.success(), "scorer exited non-zero");

    let out = std::fs::read_to_string(&output).unwrap();
    let rows: Vec<Row> = out
        .lines()
        .filter(|l| !l.trim().is_empty())
        .map(|l| serde_json::from_str(l).unwrap())
        .collect();
    assert_eq!(rows.len(), 2);

    for row in &rows {
        let py = match row.id.as_str() {
            "g1" => G1_PY_BPT,
            "g2" => G2_PY_BPT,
            other => panic!("unexpected id {other}"),
        };
        let delta = (row.bits_per_token - py).abs();
        eprintln!(
            "{}: rust_bpt={:.9} python_bpt={:.9} delta={:.3e} n_tokens={}",
            row.id, row.bits_per_token, py, delta, row.n_tokens
        );
        assert!(
            delta < TOL,
            "{}: bpt delta {delta:.3e} exceeds tolerance {TOL} (rust={} python={py})",
            row.id,
            row.bits_per_token
        );
    }
}
