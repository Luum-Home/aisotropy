# Minimal containerized LangGraph arena app.
#
# Upstream (official) packaging: https://docs.langchain.com/langsmith/deploy-standalone-server
#   — minimal variant because LangGraph's official image is built by the
#   langgraph-cli (`langgraph build`) as a full Agent Server that REQUIRES two
#   backing services at runtime: Redis (pub-sub broker for streaming background
#   runs) and Postgres (assistants/threads/runs, thread state, long-term memory,
#   and the "exactly once" task queue). Standing up that 3-service stack would
#   defeat this smoke's purpose: proving containerized tenant isolation OFFLINE
#   and KEYLESS. This single-process variant serves the same HTTP surface backed
#   by LangGraph's own real InMemoryStore instead. See docker/README.md.
#
# Proves the framework's minimal app boots and serves HTTP in a container with
# the keyless stub (no OPENAI/OPENROUTER key). Runs orbit.server (FastAPI +
# uvicorn) on port 4112, backed by LangGraph's real InMemoryStore so the
# isolation smoke can be driven over real HTTP against this process.
#
# Python 3.13 is deliberate: LangGraph has no 3.14 wheels yet (the arena's
# documented "Python 3.14 wall").
FROM python:3.13-slim

WORKDIR /app

# Minimal dependency set for the keyless stub serve + isolation store.
# langchain-openai is intentionally omitted — it is only needed for the live
# LLM mode, not the containerized stub smoke. Pins match the arena's tested
# toolchain (langgraph 1.2.9).
RUN pip install --no-cache-dir \
        "langgraph==1.2.9" \
        "fastapi==0.139.0" \
        "uvicorn==0.51.0" \
        "pydantic==2.13.4"

# Copy only the server package (the app under test).
COPY orbit/ ./orbit/

EXPOSE 4112

# Keyless stub by construction: no API key is set, so the arena stays offline.
CMD ["uvicorn", "orbit.server:app", "--host", "0.0.0.0", "--port", "4112"]
