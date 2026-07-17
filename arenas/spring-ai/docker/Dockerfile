# Multi-stage build for the Spring AI Orbit arena — CONTAINERIZED smoke.
#
# HONEST FRAMING: this arena is a JVM *library* exercised through four `main`
# methods (SmokeMain / ProductIsolationMain / FeatureDemoMain / StrengthsMain,
# the `scripts/run.sh all` set), NOT an HTTP server. So the containerized smoke
# is "build the app with Maven, then run those demos inside a slim JRE and
# assert every one exits 0" — the offline, keyless EchoChatModel path — not a
# server-boot test. It proves the REAL Spring AI 2.0.0 memory/advisor/converter
# pipeline compiles and runs the same way inside a production-shaped container.
#
# Build context is the arena root (arenas/spring-ai/), NOT docker/:
#   docker build -f docker/Dockerfile -t spring-ai-arena:smoke .
#
# ── Stage 1: Maven build (compile Orbit + resolve the real Spring AI tree) ───
# eclipse-temurin 21 satisfies the pom's `maven.compiler.release=21`.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /build

# Resolve + copy the runtime dependency set first, keyed on pom.xml alone, so
# the (network-bound) Spring AI jar download is cached across source changes.
COPY pom.xml ./
RUN mvn -q -B dependency:copy-dependencies \
      -DoutputDirectory=target/lib -DincludeScope=runtime

# Then compile Orbit's sources into target/classes (mirrors scripts/run.sh,
# which runs off target/classes + the resolved classpath — no fat jar needed).
COPY src ./src
RUN mvn -q -B compile

# ── Stage 2: slim JRE runtime — run the demos, no Maven, no build tooling ────
FROM eclipse-temurin:21-jre
WORKDIR /app

# The compiled arena classes + the resolved Spring AI 2.0.0 jars. Together these
# are the exact classpath scripts/run.sh assembles ("target/classes:<deps>").
COPY --from=build /build/target/classes /app/classes
COPY --from=build /build/target/lib     /app/lib

# In-container demo runner: runs the four mains keylessly (EchoChatModel), prints
# each exit code, and fails the container (exit 1) if any demo does not exit 0.
# The isolation smoke's own exit code IS the assertion — SmokeMain/ProductIsolationMain
# System.exit(0) only when both postures behaved as claimed.
RUN cat > /app/run-demos.sh <<'EOF'
#!/bin/sh
# No `set -e`: we run all four demos and aggregate their exit codes ourselves.
set -u
CP="/app/classes:/app/lib/*"
fail=0
for main in orbit.SmokeMain orbit.ProductIsolationMain orbit.FeatureDemoMain orbit.StrengthsMain; do
  echo "############ ${main} ############"
  java -cp "${CP}" "${main}"
  rc=$?
  echo "exit=${rc}"
  [ "${rc}" -eq 0 ] || fail=1
  echo
done
if [ "${fail}" -eq 0 ]; then
  echo "CONTAINER SMOKE: PASS — all four demos exited 0 inside the JRE container."
  exit 0
fi
echo "CONTAINER SMOKE: FAIL — at least one demo exited non-zero." >&2
exit 1
EOF
RUN chmod +x /app/run-demos.sh

ENTRYPOINT ["/app/run-demos.sh"]
