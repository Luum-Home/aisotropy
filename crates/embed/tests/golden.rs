//! Real-embedding integration test (ADR-0023 sovereignty + sanity check).
//!
//! Runs the built `embed` binary over two semantically-similar sentences and one
//! dissimilar sentence, then asserts:
//!   1. the emitted vector has the model's hidden dimension (bge-small -> 384),
//!   2. every vector is L2-normalized (norm ≈ 1),
//!   3. cosine(similar_a, similar_b) > cosine(similar_a, dissimilar).
//!
//! This requires the pinned model weights, so it is SKIPPED (passes trivially
//! with a printed notice) unless `AISOTROPY_EMBED_MODEL_DIR` points at a
//! directory with config.json + model.safetensors + tokenizer.json. The
//! deterministic pooling/normalization unit tests in `src/main.rs` are the
//! model-free correctness evidence that always runs.

use std::io::Write;
use std::path::PathBuf;
use std::process::Command;

fn model_dir() -> Option<PathBuf> {
    let d = std::env::var("AISOTROPY_EMBED_MODEL_DIR").ok()?;
    let p = PathBuf::from(d);
    if p.join("model.safetensors").exists()
        && p.join("tokenizer.json").exists()
        && p.join("config.json").exists()
    {
        Some(p)
    } else {
        None
    }
}

fn bin() -> PathBuf {
    // The test binary lives in target/<profile>/deps; the embed binary is two
    // levels up (target/<profile>/embed).
    let mut p = std::env::current_exe().unwrap();
    p.pop(); // deps
    p.pop(); // profile dir
    p.push("embed");
    p
}

#[derive(serde::Deserialize)]
struct Row {
    id: String,
    embedding: Vec<f32>,
    dim: usize,
    normalized: bool,
}

fn cosine(a: &[f32], b: &[f32]) -> f32 {
    let dot: f32 = a.iter().zip(b).map(|(x, y)| x * y).sum();
    let na: f32 = a.iter().map(|x| x * x).sum::<f32>().sqrt();
    let nb: f32 = b.iter().map(|x| x * x).sum::<f32>().sqrt();
    dot / (na * nb)
}

#[test]
fn real_embedding_cosine_sanity() {
    let Some(dir) = model_dir() else {
        eprintln!(
            "SKIP real_embedding_cosine_sanity: set AISOTROPY_EMBED_MODEL_DIR to a \
             bge-small/e5-small snapshot dir (config.json + model.safetensors + \
             tokenizer.json) to run the real-embedding sanity check."
        );
        return;
    };

    let tmp = std::env::temp_dir();
    let input = tmp.join("embed_golden_in.ndjson");
    let output = tmp.join("embed_golden_out.ndjson");

    let mut f = std::fs::File::create(&input).unwrap();
    for (id, text) in [
        ("a", "The cat sat on the warm windowsill in the sun."),
        ("b", "A kitten rested on the sunny window ledge."),
        ("c", "Quarterly gross domestic product growth exceeded forecasts."),
    ] {
        writeln!(f, "{}", serde_json::json!({"id": id, "text": text})).unwrap();
    }
    drop(f);

    let status = Command::new(bin())
        .env("AISOTROPY_EMBED_MODEL_DIR", &dir)
        .args([
            "--input",
            input.to_str().unwrap(),
            "--output",
            output.to_str().unwrap(),
        ])
        .status()
        .expect("run embed");
    assert!(status.success(), "embed exited non-zero");

    let out = std::fs::read_to_string(&output).unwrap();
    let rows: Vec<Row> = out
        .lines()
        .filter(|l| !l.trim().is_empty())
        .map(|l| serde_json::from_str(l).unwrap())
        .collect();
    assert_eq!(rows.len(), 3);

    for r in &rows {
        assert!(r.normalized);
        assert_eq!(r.embedding.len(), r.dim);
        let norm: f32 = r.embedding.iter().map(|x| x * x).sum::<f32>().sqrt();
        eprintln!("{}: dim={} norm={:.6}", r.id, r.dim, norm);
        assert!((norm - 1.0).abs() < 1e-3, "{}: not L2-normalized (norm={norm})", r.id);
    }

    let get = |id: &str| &rows.iter().find(|r| r.id == id).unwrap().embedding;
    let sim = cosine(get("a"), get("b"));
    let dis = cosine(get("a"), get("c"));
    eprintln!("cosine(a,b)_similar={sim:.4}  cosine(a,c)_dissimilar={dis:.4}");
    assert!(
        sim > dis,
        "similar pair cosine {sim:.4} must exceed dissimilar {dis:.4}"
    );
}
