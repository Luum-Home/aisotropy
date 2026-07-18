# syntax=docker/dockerfile:1
#
# Container runtime for the Spring AI (Java) live-arena adapter.
#
# The image NEVER bakes in OPENAI_API_KEY (or any credential): there is no
# ARG/ENV for it below, no COPY of the repo-root .env, and no secret is written
# into any layer. Every credential + infra endpoint is a RUNTIME env var
# injected by run-docker.sh / `docker run -e`.
#
# Build context is THIS directory:
#   docker build -t spring-ai-live-arena:latest arenas/live/spring-ai
#
# --------------------------------------------------------------------------
# PACKAGING CHOICE — JVM uber-jar (this Dockerfile) vs. GraalVM native-image.
#
# For footprint parity with the Go/Python adapters a GraalVM native-image is
# preferable (no JVM, ~tens of MB, ~ms cold start). It is DEFERRED here and
# documented as the follow-up because a Spring AI native build needs GraalVM +
# reachability metadata for Spring's reflection/proxy usage (the
# `org.graalvm.buildtools:native-maven-plugin` "native" profile + AOT hints);
# that is a real, separate verification step (no GraalVM is installed in this
# session — see README "What remains to verify"). This JVM image is the honest,
# runnable default; the native image is the optimization, not a fake claim.
#
# The GraalVM path, once the `native` Maven profile is added, is:
#   FROM ghcr.io/graalvm/native-image-community:21 AS nativebuild
#   ... mvn -Pnative native:compile ...  -> /out/spring-ai-live-arena (binary)
#   FROM gcr.io/distroless/base-debian12:nonroot
#   COPY --from=nativebuild /out/spring-ai-live-arena /spring-ai-live-arena
#   ENTRYPOINT ["/spring-ai-live-arena"]
# --------------------------------------------------------------------------

# ---- build stage: compile the uber-jar with Maven -----------------------
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src

# Cache dependency downloads on their own layer.
COPY pom.xml .
RUN mvn -q -B -DskipTests dependency:go-offline || true

COPY src ./src
RUN mvn -q -B -DskipTests package

# ---- runtime stage: JRE + the uber-jar ----------------------------------
FROM eclipse-temurin:21-jre
WORKDIR /app
COPY --from=build /src/target/spring-ai-live-arena.jar /app/spring-ai-live-arena.jar

# Real-infra host reachability defaults: from inside the container "localhost"
# is the container itself, so point at host.docker.internal (overridable at
# `docker run` time via -e).
ENV EMBED_URL=http://host.docker.internal:8899
ENV QDRANT_URL=http://host.docker.internal:6333
ENV RQLITE_URL=http://host.docker.internal:4001

ENTRYPOINT ["java", "-jar", "/app/spring-ai-live-arena.jar"]
