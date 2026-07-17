# Spring AI Orbit arena — containerized smoke

Multi-stage Docker image (Maven build stage → slim JRE runtime stage) that runs
the arena's four demos (`SmokeMain`, `ProductIsolationMain`, `FeatureDemoMain`,
`StrengthsMain`) inside a production-shaped container and asserts every one exits
`0`, offline with the keyless `EchoChatModel`.

```bash
bash docker/run.sh          # build image -> run container (4 demos) -> assert exit 0 -> cleanup
```

## Official source

- **Spring Boot — Dockerfiles (layered jar):** <https://docs.spring.io/spring-boot/reference/packaging/container-images/dockerfiles.html>
- **Spring Boot — Efficient container images:** <https://docs.spring.io/spring-boot/reference/packaging/container-images/efficient-images.html>
- **Spring Boot — Packaging OCI images (buildpacks, `spring-boot:build-image`):** <https://docs.spring.io/spring-boot/maven-plugin/build-image.html>

## How this image relates to the official guidance

Spring's Official container practice targets a **bootable Spring Boot
application**: either `./mvnw spring-boot:build-image` (Cloud Native Buildpacks →
OCI image) or the Official layered-jar Dockerfile, which extracts an executable
jar with `java -Djarmode=tools -jar application.jar extract --layers` and boots
it via `ENTRYPOINT ["java","-jar","application.jar"]`.

This arena has **no bootable Spring Boot jar** — the `pom.xml` uses `packaging:
jar` + `exec-maven-plugin`, **not** `spring-boot-maven-plugin`, so there is no
`repackage` step, no embedded server, and nothing to `java -jar`. It is a plain
JVM library exercised through four `main` methods off a classpath. So the
layered-jar/buildpack recipe does not apply, and forcing it would misrepresent
what the arena is (a library, not a service).

We therefore keep a **variant** that still honors the two structural principles
the Official docs teach:

1. **Multi-stage split** — a Maven build-tooling stage feeding a slim
   `eclipse-temurin:21-jre` runtime stage (build tools never ship in the runtime
   image), mirroring the docs' builder → JRE two-stage layout.
2. **Cache-friendly layer separation** — dependencies are resolved keyed on
   `pom.xml` alone and copied as their own layer, separate from the app classes,
   which is exactly the "efficient images" intent (rarely-changing deps in one
   layer, fast-changing app code in another) — just over a classpath instead of
   an extracted fat jar.

Base image note: the Official Dockerfile example uses a Liberica OpenJRE base;
we use `eclipse-temurin:21-jre` (permissive, no adopter branding). Both are
JRE-only runtime bases. The top-of-`Dockerfile` note records the same rationale
inline.
