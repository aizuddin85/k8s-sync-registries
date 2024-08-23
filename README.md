![CodeQL](https://github.com/aizuddin85/k8s-sync-registries/actions/workflows/codeql.yml/badge.svg)


## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.


## How to build 

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
