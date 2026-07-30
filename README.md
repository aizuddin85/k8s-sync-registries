![CodeQL](https://github.com/aizuddin85/k8s-sync-registries/actions/workflows/codeql.yml/badge.svg) ![Docker Build](https://github.com/aizuddin85/k8s-sync-registries/actions/workflows/docker-build.yml/badge.svg)
[![Go Tests](https://github.com/aizuddin85/k8s-sync-registries/actions/workflows/unittest.yml/badge.svg)](https://github.com/aizuddin85/k8s-sync-registries/actions/workflows/unittest.yml)
[![codecov](https://codecov.io/gh/aizuddin85/k8s-sync-registries/branch/main/graph/badge.svg)](https://codecov.io/gh/aizuddin85/k8s-sync-registries)

Mirror container image tags from public registries into private registries, with semver filtering and optional Helm CronJob deployment.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## How to get the container image

Latest release image: `docker.io/mymzbe/k8s-sync-registries:latest`

## How to build locally

For GCR, ensure a JSON service-account key is provided and registry access is configured.

1. Install the gpgme development library:
   - Debian/Ubuntu: `apt-get install libgpgme-dev`
   - Fedora/RHEL: `dnf install gpgme-devel`
2. Update modules: `go mod tidy`
3. Run directly: `go run .`
4. Build the binary: `go build -o sync_registries`

## How to run

1. Populate `registries.yaml` with source/destination registries and repositories to sync.
2. If authentication is required, populate `secrets.yaml`.
3. Set environment variables and run:

```bash
export REGISTRY_CONFIG_PATH=./registries.yaml
export SECRETS_CONFIG_PATH=./secrets.yaml
# optional: limit parallel image copies (default 3)
export SYNC_CONCURRENCY=3
./sync_registries
```

The process exits non-zero if any registry sync fails.

## How to build container image

1. `podman build -t <registry/repo/image:v1.0.0> .`
2. `podman push <registry/repo/image:v1.0.0>`

## Managing registries.yaml and secrets.yaml

### registries.yaml

```yaml
registries:
  - source_registry: "quay.io"
    source_repository: "argoproj/argocd"
    dest_registry: "europe-west3-docker.pkg.dev"
    dest_repository: "$gcp_project/argocd/argocd"
    tag_limit: 3
    insecure_tls: false
    exclude_patterns:
      - "alpha"
      - "beta"
      - "rc"
    version_filters:
      - major: 1
        minor: 11
        get_latest: false
      - major: 1
        minor: 10
        get_latest: false
```

- `tag_limit`: max tags kept per minor version (or overall when `version_filters` is empty)
- `exclude_patterns`: regex patterns; matching tags are skipped
- `version_filters`: select major.minor lines; `get_latest: true` keeps only the newest patch
- When `version_filters` is omitted/empty, all valid `x.y.z` semver tags are considered (newest first), then `tag_limit` is applied

### secrets.yaml

```yaml
secrets:
  - source_registry: "docker.io"
    source_type: "dockerhub"
    username: "docker_user"
    password: "docker_pass"
    insecure_tls: false
  - dest_registry: "myregistry.azurecr.io"
    username: "acr_token_user"
    password: "acr_token_pass"
    type: "acr"
  - dest_registry: "europe-west3-docker.pkg.dev"
    service_account_key: "/gcr/gcr.json"
    type: "gcr"
```

Supported auth types: `dockerhub` (generic username/password), `acr`, and `gcr`.
