package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/eval"
)

// report is the full result of one harness run: the dataset it graded, the
// embedder it graded with, per-slice metrics, and the optional judge pass.
type report struct {
	gold        *goldSet
	dataPath    string
	backend     string
	roleAware   bool
	realQuality bool
	dim         int
	startedAt   time.Time
	finishedAt  time.Time
	slices      []sliceReport
	judge       *judgeReport
}

// sliceReport holds one slice's eval.RetrievalReport, scored once at the top
// cutoff (maxCutoff). The shallower recall cutoffs are derived from each case's
// returned ranked list, so the retriever is queried ONCE per slice, not once
// per cutoff — same numbers, a third of the embed calls.
type sliceReport struct {
	slice  goldSlice
	report eval.RetrievalReport
}

// metrics pulls the headline numbers out of the single top-cutoff report,
// deriving recall@1 and recall@5 from each case's ranked RetrievedIDs.
func (sr sliceReport) metrics() (r1, r5, r10, mrr10, ndcg10 float64, queries int) {
	var s1, s5 float64
	for _, c := range sr.report.Cases {
		s1 += eval.RecallAtK(c.RetrievedIDs, c.Case.RelevantIDs, 1)
		s5 += eval.RecallAtK(c.RetrievedIDs, c.Case.RelevantIDs, 5)
	}
	if n := float64(len(sr.report.Cases)); n > 0 {
		r1 = s1 / n
		r5 = s5 / n
	}
	r10 = sr.report.MeanRecall
	mrr10 = sr.report.MeanMRR
	ndcg10 = sr.report.MeanNDCG
	queries = len(sr.report.Cases)
	return
}

// render produces the human-readable, self-documenting report written to
// results.txt and stdout. It leads with the honesty banner so a reader can
// never mistake a harness-validation (Hash) run for a real e5-quality run.
func (rp *report) render() string {
	var b strings.Builder

	fmt.Fprintln(&b, "================================================================================")
	fmt.Fprintln(&b, " aisotropy — RAG retrieval-quality eval (bench/rag-quality)")
	fmt.Fprintln(&b, "================================================================================")
	fmt.Fprintf(&b, "run at         : %s\n", rp.finishedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "duration       : %s\n", rp.finishedAt.Sub(rp.startedAt).Round(time.Millisecond))
	fmt.Fprintf(&b, "embedder       : %s\n", rp.backend)
	fmt.Fprintf(&b, "vector dim     : %d\n", rp.dim)
	rolePrefixNote := "no (query:/passage: asymmetry NOT engaged — non-e5/Hash embedder)"
	if rp.roleAware {
		rolePrefixNote = "yes (query:/passage: asymmetry engaged — ADR-0054)"
	}
	fmt.Fprintf(&b, "e5 role prefix : %s\n", rolePrefixNote)
	fmt.Fprintln(&b)

	// Dataset provenance + license (the constitutional gate applies to data too).
	fmt.Fprintln(&b, "-- dataset -------------------------------------------------------------------")
	fmt.Fprintf(&b, "name           : %s\n", rp.gold.Name)
	fmt.Fprintf(&b, "source         : %s\n", rp.gold.Source)
	fmt.Fprintf(&b, "url            : %s\n", rp.gold.SourceURL)
	fmt.Fprintf(&b, "license        : %s (%s)\n", rp.gold.License, rp.gold.LicenseURL)
	fmt.Fprintf(&b, "corpus sizes   : %v paragraphs; %d aligned EN/ES pairs\n", rp.gold.CorpusSizes, rp.gold.AlignedPairs)
	fmt.Fprintln(&b)

	// The honesty banner.
	if rp.realQuality {
		fmt.Fprintln(&b, "-- interpretation ------------------------------------------------------------")
		fmt.Fprintln(&b, "REAL embedder: the numbers below are the SEMANTIC retrieval quality of this")
		fmt.Fprintln(&b, "model on a generic public benchmark. See the generic-vs-domain gap note below.")
	} else {
		fmt.Fprintln(&b, "-- interpretation (HARNESS VALIDATION — NOT SEMANTIC QUALITY) ----------------")
		fmt.Fprintln(&b, "!! This run used the DETERMINISTIC HashEmbedder (feature hashing), NOT a real")
		fmt.Fprintln(&b, "!! model. The numbers below prove the HARNESS runs end-to-end and the metrics")
		fmt.Fprintln(&b, "!! compute correctly. They are LEXICAL-OVERLAP artifacts, NOT the quality of")
		fmt.Fprintln(&b, "!! the multilingual e5 embedder. Expect the cross-lingual slices to score near")
		fmt.Fprintln(&b, "!! zero (a hash embedder shares no lexicon across languages). To measure real")
		fmt.Fprintln(&b, "!! e5 quality, set AISOTROPY_EMBED_BIN + AISOTROPY_EMBED_MODEL_DIR (see -help).")
	}
	fmt.Fprintln(&b)

	// Per-slice table.
	fmt.Fprintln(&b, "-- results (per slice) -------------------------------------------------------")
	fmt.Fprintf(&b, "%-16s %5s %7s %7s %7s %7s %7s\n", "slice", "N", "R@1", "R@5", "R@10", "MRR@10", "nDCG@10")
	fmt.Fprintln(&b, strings.Repeat("-", 78))

	var sr1, sr5, sr10, smrr, sndcg float64
	for _, sr := range rp.slices {
		r1, r5, r10, mrr10, ndcg10, n := sr.metrics()
		fmt.Fprintf(&b, "%-16s %5d %7.3f %7.3f %7.3f %7.3f %7.3f\n",
			sr.slice.Name, n, r1, r5, r10, mrr10, ndcg10)
		sr1 += r1
		sr5 += r5
		sr10 += r10
		smrr += mrr10
		sndcg += ndcg10
	}
	if n := float64(len(rp.slices)); n > 0 {
		fmt.Fprintln(&b, strings.Repeat("-", 78))
		fmt.Fprintf(&b, "%-16s %5s %7.3f %7.3f %7.3f %7.3f %7.3f\n",
			"MACRO-AVG", "", sr1/n, sr5/n, sr10/n, smrr/n, sndcg/n)
	}
	fmt.Fprintln(&b)

	// Slice legend + cross-lingual reading.
	fmt.Fprintln(&b, "-- slices --------------------------------------------------------------------")
	for _, sr := range rp.slices {
		fmt.Fprintf(&b, "  %-16s %s query -> %s corpus : %s\n",
			sr.slice.Name, sr.slice.QueryLang, sr.slice.CorpusLang, sr.slice.Description)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "The two cross_* slices are the multilingual proof: an EN query must find the")
	fmt.Fprintln(&b, "parallel ES paragraph (and vice-versa) with no lexical overlap to lean on. Only")
	fmt.Fprintln(&b, "an embedder that maps both languages into one shared space scores above chance.")
	fmt.Fprintln(&b)

	// Judge section.
	fmt.Fprintln(&b, "-- optional LLM-judge pass ---------------------------------------------------")
	fmt.Fprint(&b, rp.judge.render())
	fmt.Fprintln(&b)

	// Honest gap + reproduce.
	fmt.Fprintln(&b, "-- honest gap (generic public benchmark != aisotropy's real RAG) -------------")
	fmt.Fprintln(&b, "XQuAD is a Wikipedia QA benchmark: clean, single-relevant, professionally")
	fmt.Fprintln(&b, "translated paragraphs. aisotropy's real workload is multi-tenant conversational")
	fmt.Fprintln(&b, "MEMORY — noisy turns, distilled facts, per-user isolation, several relevant")
	fmt.Fprintln(&b, "chunks per query. These numbers measure the EMBEDDER + STORE on a generic task;")
	fmt.Fprintln(&b, "they do NOT yet measure retrieval over aisotropy's own conversational corpus. A")
	fmt.Fprintln(&b, "domain gold set (labeled tenant memories) is the next step. This moves retrieval")
	fmt.Fprintln(&b, "from UNMEASURED to MEASURED on a real, licensed, multilingual benchmark.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "-- reproduce -----------------------------------------------------------------")
	fmt.Fprintln(&b, "  # real multilingual-e5-small (sovereign local subprocess):")
	fmt.Fprintln(&b, "  AISOTROPY_EMBED_BIN=crates/embed/target/release/embed \\")
	fmt.Fprintln(&b, "  AISOTROPY_EMBED_MODEL_DIR=<dir with config.json+model.safetensors+tokenizer.json> \\")
	fmt.Fprintln(&b, "  AISOTROPY_EMBED_DIM=384 AISOTROPY_EMBED_MODE=subprocess AISOTROPY_EMBED_E5_PREFIXES=1 \\")
	fmt.Fprintln(&b, "  go run ./bench/rag-quality")
	fmt.Fprintln(&b, "  # deterministic harness validation (no model, no network):")
	fmt.Fprintln(&b, "  go run ./bench/rag-quality")
	fmt.Fprintln(&b, "================================================================================")

	return b.String()
}
