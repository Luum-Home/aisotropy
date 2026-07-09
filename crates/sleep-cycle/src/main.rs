//! Aisotropy sleep-cycle worker (ADR-0005): an offline, batch Rust process the
//! Go core invokes as a subprocess. It reads a newline-delimited JSON export of
//! facts, runs a DataFusion deduplication over them, writes the deduplicated
//! result as Parquet, and prints a one-line JSON summary to stdout. A failure
//! (including a panic) is surfaced to Go as a non-zero exit code.
//!
//! Contract (kept explicit and tested on the Go side, internal/sleepcycle):
//!   argv:   --input <facts.ndjson> --output <deduped.parquet>
//!   stdout: {"rows_in":N,"rows_out":M,"dropped":D,"dropped_ids":[...]}
//!           (only on success; dropped_ids lists the ids removed as duplicates,
//!           which Go deletes from the live store)
//!   exit:   0 = success, non-zero = failure (stderr carries the reason)

use datafusion::arrow::array::{Array, StringArray};
use datafusion::dataframe::DataFrameWriteOptions;
use datafusion::error::DataFusionError;
use datafusion::prelude::*;
use std::env;
use std::process;

#[tokio::main]
async fn main() {
    match run().await {
        Ok(summary) => println!("{summary}"),
        Err(e) => {
            eprintln!("sleep-cycle: {e}");
            process::exit(1);
        }
    }
}

fn flag(name: &str) -> Option<String> {
    let args: Vec<String> = env::args().collect();
    args.iter()
        .position(|a| a == name)
        .and_then(|i| args.get(i + 1))
        .cloned()
}

async fn run() -> Result<String, DataFusionError> {
    let input = flag("--input")
        .ok_or_else(|| DataFusionError::Execution("missing --input".into()))?;
    let output = flag("--output")
        .ok_or_else(|| DataFusionError::Execution("missing --output".into()))?;

    let ctx = SessionContext::new();
    // DataFusion's NdJson reader filters candidate files by extension (default
    // ".json"); honor whatever extension the input actually uses (e.g.
    // ".ndjson") so a single explicit file is always read.
    let ext = std::path::Path::new(&input)
        .extension()
        .and_then(|e| e.to_str())
        .map(|e| format!(".{e}"))
        .unwrap_or_else(|| ".json".to_string());
    ctx.register_json(
        "facts",
        &input,
        NdJsonReadOptions::default().file_extension(&ext),
    )
    .await?;

    let rows_in = ctx.table("facts").await?.count().await?;

    // Deduplicate: keep one row per (tenant_id, user_id, lower(content)),
    // preferring higher importance, then the more recent update. This is the
    // cold consolidation the request path can't afford (docs/components/
    // datafusion-sleep-cycle.md).
    let deduped = ctx
        .sql(
            "SELECT id, tenant_id, user_id, content, importance, created_at, updated_at \
             FROM (SELECT *, ROW_NUMBER() OVER ( \
                     PARTITION BY tenant_id, user_id, lower(content) \
                     ORDER BY importance DESC, updated_at DESC) AS rn \
                   FROM facts) \
             WHERE rn = 1",
        )
        .await?;

    let rows_out = deduped.clone().count().await?;
    deduped
        .write_parquet(&output, DataFrameWriteOptions::new(), None)
        .await?;

    // The ids the dedup dropped (the non-canonical duplicates, rn > 1). Emitted
    // in the summary so the Go coordinator can delete exactly those rows from
    // the live facts store — the reinjection/cleanup half of the sleep cycle.
    let dropped_df = ctx
        .sql(
            "SELECT id FROM (SELECT id, ROW_NUMBER() OVER ( \
                     PARTITION BY tenant_id, user_id, lower(content) \
                     ORDER BY importance DESC, updated_at DESC) AS rn \
                   FROM facts) \
             WHERE rn > 1",
        )
        .await?;
    let mut dropped_ids: Vec<String> = Vec::new();
    for batch in dropped_df.collect().await? {
        let col = batch
            .column(0)
            .as_any()
            .downcast_ref::<StringArray>()
            .ok_or_else(|| DataFusionError::Execution("id column is not a string".into()))?;
        for i in 0..col.len() {
            dropped_ids.push(col.value(i).to_string());
        }
    }

    let ids_json = dropped_ids
        .iter()
        .map(|s| format!("\"{}\"", s.replace('\\', "\\\\").replace('"', "\\\"")))
        .collect::<Vec<_>>()
        .join(",");

    Ok(format!(
        "{{\"rows_in\":{},\"rows_out\":{},\"dropped\":{},\"dropped_ids\":[{}]}}",
        rows_in,
        rows_out,
        dropped_ids.len(),
        ids_json
    ))
}
