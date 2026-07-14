# Bet 2 — capacity-ceiling model ladder (results)

Runner: `bet02_model_ladder.py` (thin subprocess loop; codec `bet02_lm_arithmetic_codec.py` unchanged).
Sample: default `CHAT_SAMPLE` (Spanish multi-turn chat, 3298 chars / 3367 UTF-8 bytes / 1158 gpt2 tokens).
Window: 512 tokens. Hardware: CPU (macOS), float32 forward / float64 quantization. Date: 2026-07-10.
Baselines on this sample: gzip -9 = 4.0558, bzip2 -9 = 4.0752, xz -9 = 4.3081 bits/char; raw UTF-8 ≈ 8.17.

GPT-2 family weights are MIT-licensed (OpenAI GPT-2) — HF `cardData.license` = `mit` for gpt2 / gpt2-medium / gpt2-large.

| model        | params | tokens | bits/char (LM+AC, packed) | lossless | vs gzip | < 1.0? | encode+decode |
|--------------|-------:|-------:|--------------------------:|:--------:|:-------:|:------:|--------------:|
| distilgpt2   |   82M  |  1158  | 2.3360                    | YES      | -42%    | no     | ~95s          |
| gpt2         |  124M  |  1158  | 2.2050                    | YES      | -46%    | no     | ~215s         |
| gpt2-medium  |  355M  |  1158  | 1.9770                    | YES      | -51%    | no     | ~590s         |
| gpt2-large   |  774M  |  1158  | 1.8751                    | YES      | -54%    | no     | ~1455s (630+825) |

## Trend (bits/char vs model size)

Monotonic descent, decelerating:

- 82M → 124M : Δ -0.131  (+42M params)
- 124M → 355M: Δ -0.228  (+231M params)
- 355M → 774M: Δ -0.102  (+419M params)
- Net 82M → 774M (~9.4× params, ~3.2 doublings): Δ -0.461 bits/char ≈ -0.14 per param-doubling.

Every step is lossless (byte-for-byte roundtrip: token ids, decoded text, and UTF-8 bytes all match).
Compression margin over gzip *widens* with size (-42% → -54%).

## Reading

- bits/char **decreases monotonically with model size** — scale IS the right axis; the method is not
  saturating on the small end. This confirms the bet-02 "capacity ceiling, not method failure" direction.
- But the descent is **slow / roughly logarithmic** and **plateaus far above 1.0**: the best affordable
  GPT-2-class model (gpt2-large, 774M) still sits at **1.875 bits/char**, ~1.9× the < 1.0 target.
- Naive extrapolation of ≈-0.14/doubling from 1.875 to 1.0 needs ~6 more param-doublings (~10^10–10^11
  params) — i.e. nncp/ts_zip territory, not GPT-2 family. < 1.0 is **plausible with scale but NOT
  demonstrated here**.

## Tokenizer-confound follow-up — DeepESP/gpt2-spanish (2026-07-11)

The ladder above is *English* GPT-2 on Spanish text (caveat 1 below). The pending language-matched
datapoint was closed on 2026-07-11 via `bet02_lm_arithmetic_codec.py` (whose default model is
`DeepESP/gpt2-spanish`, MIT — see ADR-0015) on the same `CHAT_SAMPLE`:

| model                | params | bits/char (LM+AC, packed) | lossless | note |
|----------------------|-------:|--------------------------:|:--------:|------|
| DeepESP/gpt2-spanish |  124M  | 1.97                      | YES      | language-matched tokenizer |

**Provenance:** the raw run log was not preserved; the number is as recorded in commit
`f6766c3` ("research(bet2): quantify the codec tokenizer confound — DeepESP 124M ≈ gpt2-medium
355M", 2026-07-11), which reports 1.97 bits/char, byte-for-byte lossless, on the same Spanish chat.

**Reading:** a 124M language-matched model lands where the 355M English model does (1.9770) —
a matched tokenizer is worth ~1.5 param-doublings. But 1.97 is still ~2× the < 1.0 target, so the
target needs nncp-scale models even with a matched tokenizer.

## Caveats (do not over-read the curve)

1. **Tokenizer/language mismatch inflates the floor.** The sample is Spanish; the ladder is *English*
   GPT-2. Fit is poor (2.85 chars/token → 1158 tokens for 3298 chars). A Spanish/multilingual or modern
   LM of equal size would likely land materially lower, so part of the >1.0 plateau is tokenizer penalty,
   not pure capacity. The absolute numbers here are a **lower bound on how good same-size models can do**.
   *Quantified 2026-07-11 — see the DeepESP follow-up section above.*
2. **One synthetic sample.** Single ~3.3 KB chat; no variance estimate.
3. **CPU cost is the binding constraint.** gpt2-large already takes ~24 min for one 3.3 KB sample
   (O(N) forwards × 2). gpt2-xl (1.5B) / modern LMs were not run — the trend is extrapolated, not measured,
   below 774M... above.
4. **gpt2-large needed `HF_HUB_DISABLE_XET=1`** — the Xet transfer aborted ("Background writer channel
   closed"); classic HTTP download succeeded. The ladder runner's in-loop gpt2-large attempt failed for
   this reason and was re-run standalone; the number above is from that clean standalone run.
