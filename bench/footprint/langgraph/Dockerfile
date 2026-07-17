# syntax=docker/dockerfile:1
# Minimal idiomatic LangGraph deployment image: the official python:3.12-slim
# base + pip install of the framework. This is the smallest STANDARD way to ship
# a Python framework app (slim, not full; no build toolchain kept). It is NOT
# distroless because the Python runtime + interpreter must ship in the image —
# that asymmetry vs the Go subjects is a real, honest language property, called
# out in README.md and the BENCHMARKS.md caveats.
FROM python:3.12-slim

WORKDIR /app

# Install deps into system site-packages (predictable path for dep-weight probe).
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py .

EXPOSE 8080
CMD ["python", "app.py"]
