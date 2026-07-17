//! Aisotropy sleep-cycle worker (ADR-0005): an offline, batch Rust process the
//! Go core invokes as a subprocess. It reads a newline-delimited JSON export of
//! facts, runs two DataFusion consolidation passes over them — a low-signal
//! NOISE prune (importance threshold) followed by a DEDUPLICATION — writes the
//! surviving set as Parquet, and prints a one-line JSON summary to stdout. A
//! failure (including a panic) is surfaced to Go as a non-zero exit code.
//!
//! Contract (kept explicit and tested on the Go side, internal/sleepcycle):
//!   argv:   --input <facts.ndjson> --output <survivors.parquet>
//!           [--min-importance <f>]   (optional; <=0 or absent disables the
//!                                     noise prune, so the default behavior is
//!                                     dedup-only and byte-for-byte compatible
//!                                     with the pre-noise contract)
//!   stdout: {"rows_in":N,"rows_out":M,"dropped":D,"noise_dropped":ND,
//!            "dropped_ids":[...]}
//!           (only on success; dropped_ids lists EVERY id removed — noise-pruned
//!           OR de-duplicated — which Go deletes from the live store.
//!           noise_dropped is the sub-count removed by the noise pass, so
//!           dedup-dropped = dropped - noise_dropped.)
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
    flag_in(&args, name)
}

/// Pure argv scan for `--name value`, split out so the contract is unit-testable
/// without touching the process-global env::args().
fn flag_in(args: &[String], name: &str) -> Option<String> {
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

    // Optional low-signal noise threshold. Absent or <= 0 disables the pass, so
    // the default worker is dedup-only and identical to the pre-noise contract.
    let min_importance: f64 = match flag("--min-importance") {
        Some(s) => s
            .parse::<f64>()
            .map_err(|e| DataFusionError::Execution(format!("bad --min-importance {s:?}: {e}")))?,
        None => 0.0,
    };

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

    // Pass 1 — NOISE prune: drop low-signal rows below the importance floor.
    // These are trivial chatter with no cognitive value (the "hi"/"thanks" the
    // sleep cycle is meant to scrub). A <= 0 floor keeps every row, so the WHERE
    // is a no-op and this collapses to the historical dedup-only behavior.
    let noise_where = if min_importance > 0.0 {
        format!("WHERE importance >= {min_importance}")
    } else {
        String::new()
    };

    // Count how many rows the noise pass removes, for the summary sub-count.
    // (0 whenever the floor is disabled.)
    let noise_dropped = if min_importance > 0.0 {
        ctx.sql(&format!(
            "SELECT count(*) AS n FROM facts WHERE importance < {min_importance}"
        ))
        .await?
        .collect()
        .await?
        .first()
        .and_then(|b| {
            b.column(0)
                .as_any()
                .downcast_ref::<datafusion::arrow::array::Int64Array>()
                .map(|c| c.value(0) as usize)
        })
        .unwrap_or(0)
    } else {
        0
    };

    // Pass 2 — DEDUPLICATE the noise-survivors: keep one row per
    // (tenant_id, user_id, lower(content)), preferring higher importance, then
    // the more recent update. This is the cold consolidation the request path
    // can't afford (docs/components/datafusion-sleep-cycle.md). The noise WHERE
    // is applied INSIDE the window's input so a pruned row can never win its
    // partition.
    let survivors_sql = format!(
        "SELECT id, tenant_id, user_id, content, importance, created_at, updated_at \
         FROM (SELECT *, ROW_NUMBER() OVER ( \
                 PARTITION BY tenant_id, user_id, lower(content) \
                 ORDER BY importance DESC, updated_at DESC) AS rn \
               FROM facts {noise_where}) \
         WHERE rn = 1"
    );
    let survivors = ctx.sql(&survivors_sql).await?;

    let rows_out = survivors.clone().count().await?;
    survivors
        .write_parquet(&output, DataFrameWriteOptions::new(), None)
        .await?;

    // Every id NOT among the survivors was removed — by the noise prune OR the
    // dedup — and must be deleted from the live facts store. Emitting the exact
    // set (rather than a count) lets the Go coordinator delete precisely those
    // rows: the reinjection/cleanup half of the sleep cycle. The NOT IN subquery
    // must project a SINGLE column, so this mirrors survivors_sql but selects
    // only id.
    let survivor_ids_sql = format!(
        "SELECT id FROM (SELECT id, ROW_NUMBER() OVER ( \
                 PARTITION BY tenant_id, user_id, lower(content) \
                 ORDER BY importance DESC, updated_at DESC) AS rn \
               FROM facts {noise_where}) \
         WHERE rn = 1"
    );
    let dropped_df = ctx
        .sql(&format!(
            "SELECT id FROM facts WHERE id NOT IN ({survivor_ids_sql})"
        ))
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
        "{{\"rows_in\":{},\"rows_out\":{},\"dropped\":{},\"noise_dropped\":{},\"dropped_ids\":[{}]}}",
        rows_in,
        rows_out,
        dropped_ids.len(),
        noise_dropped,
        ids_json
    ))
}

#[cfg(test)]
mod tests {
    use super::flag_in;

    fn argv(parts: &[&str]) -> Vec<String> {
        parts.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn flag_reads_value_after_name() {
        let args = argv(&[
            "sleep-cycle",
            "--input",
            "facts.ndjson",
            "--output",
            "out.parquet",
            "--min-importance",
            "0.5",
        ]);
        assert_eq!(flag_in(&args, "--input").as_deref(), Some("facts.ndjson"));
        assert_eq!(flag_in(&args, "--output").as_deref(), Some("out.parquet"));
        assert_eq!(flag_in(&args, "--min-importance").as_deref(), Some("0.5"));
    }

    #[test]
    fn absent_flag_is_none() {
        // The dedup-only default: no --min-importance present.
        let args = argv(&["sleep-cycle", "--input", "f", "--output", "o"]);
        assert_eq!(flag_in(&args, "--min-importance"), None);
    }

    #[test]
    fn dangling_flag_without_value_is_none() {
        let args = argv(&["sleep-cycle", "--input"]);
        assert_eq!(flag_in(&args, "--input"), None);
    }
}
