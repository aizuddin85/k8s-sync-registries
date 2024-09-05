![CodeQL](https://github.com/aizuddin85/k8s-sync-registries/actions/workflows/codeql.yml/badge.svg)


## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.


## How to build locally

For GCR, ensure JSON key is provided and access to registry is properly configured.  

1. Ensure gpgme library install  
a. apt-get install libgpgme-dev  
b. dnf install gpgme-devel  

2. Update modules `go mod tidy`  

3. To run the directly, execute `go run main.go` 
 
4. To build the binary, execute `go build -o sync_registries`

## How to run

1. Ensure registries.yaml properly populated with source and destination as well as repo to sync.

2. If the registry required authenticaion, update secret.yaml with its authentication details.

2. Run `sync_registries` to begin sync.

## How to build container image

1. Execute `podman build -t <registry/repo/image:v1.0.0> .`
   NOTE: Ensure your build environment has internet connection.
   
2. To push to registry `podman push <registry/repo/image:v1.0.0>`, follow your registry authentication method if pushing to protected registry.


## Managing registries.yaml and secrets.yaml
1. Source and target registries also image are defined here.
   
2. The structure of the registries.yaml

```yaml
registries:
  - source_registry: "quay.io" <-- Source registry
    source_repository: "argoproj/argocd" <-- Source repo
    dest_registry: "europe-west3-docker.pkg.dev" <-- Target registry
    dest_repository: "$gcp_project/argocd/argocd" <-- Target repo
    tag_limit: 3 <-- how many newest tag(s) to include and discard the rest
    exclude_patterns: <-- a regex expression or list to exclude tags with specific tag identifiers.
      - "alpine"
      - "distroless"
      - "^sha"
      - "poc"
      - "release"
      - "latest"
      - "master
      - "rc"
```

3. Once we have populated registries.yaml, if the registry required authentication, it must be set in secrets.yaml
   
```yaml
secrets:
  - source_registry: "docker.io" <-- for source registry authentication
    source_type: "dockerhub" <-- Registry type against auth, support dockerhub, acr and gcr. Typicall username and password login should use "dockerhub" as type.
    username: "docker_user" <-- username for the registry
    password: "docker_pass" <-- password for the registry
  - dest_registry: "myregistry.azurecr.io"
    username: "acr_token_user" <--  Azure ACR, acr token user from ACR Token
    password: "acr_token_pass" <--  Azure ACR, acr token pass from ACR Token
    type: "acr" <-- Authenticate against ACR
  - dest_registry: "europe-west3-docker.pkg.dev"
    service_account_key: "/root/git/k8s-sync-registries/gcr.json" <-- GCP service account JSON key with proper GCR permission associated to it
    type: "gcr" <-- GCR need special oauth JWT token, code will authenticate to Google and obtain JWT.
```
