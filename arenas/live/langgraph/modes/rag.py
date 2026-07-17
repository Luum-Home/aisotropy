"""``rag`` mode — real retrieval-augmented generation over Qdrant `arena_rag`.

Pipeline (all real, no stubs), driven through a two-node LangGraph retrieval
graph (``retrieve`` -> ``generate``):

  question --(OpenAIEmbeddings against the REAL e5 embed server,
               model "multilingual-e5-small-query")-->
           --(langchain_qdrant.QdrantVectorStore.similarity_search_with_score
               against the REAL Qdrant `arena_rag` collection)-->
           top-k {doc_id, title, text, score}
           --(ChatOpenAI / agnes, grounded-only system prompt)--> answer

The system prompt hard-requires "answer only from the provided context, else
say you don't know" — this is appended regardless of any caller-supplied
``system`` text so the grounding contract cannot be silently dropped.
"""
from __future__ import annotations

import time
import traceback
from typing import Any, TypedDict

from . import common

MODE = "rag"

GROUNDING_INSTRUCTION = (
    "You are answering strictly from the CONTEXT documents provided below. "
    "If the answer is not contained in the context, say plainly that you "
    "don't know / the information is not available in the source — do not "
    "invent, guess, or use outside knowledge."
)


class RagState(TypedDict):
    question: str
    top_k: int
    context: list[dict[str, Any]]
    answer: str
    input_tokens: int
    output_tokens: int


def _build_vector_store(embed_url: str, qdrant_url: str, collection: str, api_key: str):
    from langchain_openai import OpenAIEmbeddings
    from langchain_qdrant import QdrantVectorStore
    from qdrant_client import QdrantClient

    embeddings = OpenAIEmbeddings(
        base_url=embed_url,
        api_key=common.DEFAULT_EMBED_API_KEY,
        model=common.DEFAULT_EMBED_MODEL_QUERY,
        check_embedding_ctx_length=False,
    )
    # check_compatibility=False: the real Qdrant server here (1.12.x) is one
    # minor behind the qdrant-client wheel; both speak the same REST
    # contract this adapter uses, so the version-skew warning is silenced
    # rather than left to print an unactionable UserWarning per request.
    client = QdrantClient(url=qdrant_url, check_compatibility=False)
    return QdrantVectorStore(
        client=client,
        collection_name=collection,
        embedding=embeddings,
        content_payload_key="text",
        validate_collection_config=False,
    )


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return err

    try:
        from langchain_core.messages import HumanMessage, SystemMessage
        from langchain_openai import ChatOpenAI
        from langgraph.graph import END, START, StateGraph
    except Exception as exc:  # pragma: no cover - import failure path
        return common.envelope(mode=MODE, model=model_name, error=f"import failure: {exc}")

    question = common.last_user_content(payload)
    if not question:
        return common.envelope(mode=MODE, model=model_name, error="no user message found in 'messages'")

    top_k = int(payload.get("top_k") or 3)
    embed_url = payload.get("embed_url") or common.DEFAULT_EMBED_URL
    qdrant_url = payload.get("qdrant_url") or common.DEFAULT_QDRANT_URL
    collection = payload.get("qdrant_collection") or common.DEFAULT_QDRANT_COLLECTION

    try:
        vector_store = _build_vector_store(embed_url, qdrant_url, collection, api_key)
        llm = ChatOpenAI(model=model_name, base_url=base_url, api_key=api_key)
        caller_system = (payload.get("system") or "").strip()

        def retrieve(state: RagState) -> dict[str, Any]:
            hits = vector_store.similarity_search_with_score(state["question"], k=state["top_k"])
            ids = [doc.metadata.get("_id") for doc, _ in hits]
            # QdrantVectorStore only surfaces content_payload_key +
            # metadata_payload_key (default "metadata", absent in our flat
            # {doc_id,title,text} payload) onto the Document. Re-fetch the
            # full payload by point id from the same real Qdrant collection
            # so `retrieved` can name the gold doc_id/title, not just text.
            points = vector_store.client.retrieve(
                collection_name=collection, ids=[i for i in ids if i is not None], with_payload=True
            )
            payload_by_id = {p.id: p.payload for p in points}
            context = []
            for doc, score in hits:
                pid = doc.metadata.get("_id")
                pt_payload = payload_by_id.get(pid, {})
                context.append(
                    {
                        "doc_id": pt_payload.get("doc_id", ""),
                        "title": pt_payload.get("title", ""),
                        "text": doc.page_content,
                        "score": float(score),
                    }
                )
            return {"context": context}

        def generate(state: RagState) -> dict[str, Any]:
            context_block = "\n\n".join(
                f"[{c['doc_id']}] {c['title']}\n{c['text']}" for c in state["context"]
            )
            system_parts = [GROUNDING_INSTRUCTION]
            if caller_system:
                system_parts.append(caller_system)
            system_text = "\n\n".join(system_parts) + f"\n\nCONTEXT:\n{context_block}"
            response = llm.invoke([SystemMessage(content=system_text), HumanMessage(content=state["question"])])
            content = response.content
            answer = content if isinstance(content, str) else str(content)
            inp, out = common.extract_usage(response)
            return {"answer": answer, "input_tokens": inp, "output_tokens": out}

        graph_builder = StateGraph(RagState)
        graph_builder.add_node("retrieve", retrieve)
        graph_builder.add_node("generate", generate)
        graph_builder.add_edge(START, "retrieve")
        graph_builder.add_edge("retrieve", "generate")
        graph_builder.add_edge("generate", END)
        graph = graph_builder.compile()

        start = time.monotonic()
        result = graph.invoke({"question": question, "top_k": top_k, "context": [], "answer": "", "input_tokens": -1, "output_tokens": -1})
        latency_ms = int((time.monotonic() - start) * 1000)

        retrieved = [
            {"doc_id": c["doc_id"], "title": c["title"], "score": c["score"]} for c in result["context"]
        ]

        return common.envelope(
            mode=MODE,
            model=model_name,
            reply=result["answer"],
            input_tokens=result["input_tokens"],
            output_tokens=result["output_tokens"],
            latency_ms=latency_ms,
            retrieved=retrieved,
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")
