package main

import (
    "context"
    "fmt"
    "io/ioutil"
    "log"
    "os"
    "regexp"
    "sort"
    "strings"
    "sync"
    "time"

    "github.com/blang/semver/v4"
    "github.com/containers/image/v5/copy"
    "github.com/containers/image/v5/docker"
    "github.com/containers/image/v5/signature"
    "github.com/containers/image/v5/types"
    "golang.org/x/oauth2/google"
    "gopkg.in/yaml.v3"
)

type VersionRequirement struct {
    Major     int  `yaml:"major"`
    Minor     int  `yaml:"minor"`
    GetLatest bool `yaml:"get_latest"`
}

type RegistryConfig struct {
    SourceRegistry    string              `yaml:"source_registry"`
    SourceRepository  string              `yaml:"source_repository"`
    DestRegistry      string              `yaml:"dest_registry"`
    DestRepository    string              `yaml:"dest_repository"`
    TagLimit          int                 `yaml:"tag_limit"`
    ExcludePatterns   []string            `yaml:"exclude_patterns"`
    VersionFilters    []VersionRequirement `yaml:"version_filters"`
    InsecureTLS       bool                `yaml:"insecure_tls"`
}

type SecretConfig struct {
    DestRegistry      string `yaml:"dest_registry"`
    SourceRegistry    string `yaml:"source_registry,omitempty"`
    Type             string `yaml:"type"`
    SourceType       string `yaml:"source_type"`
    Username         string `yaml:"username,omitempty"`
    Password         string `yaml:"password,omitempty"`
    ServiceAccountKey string `yaml:"service_account_key,omitempty"`
    InsecureTLS      bool   `yaml:"insecure_tls"`
}

type Config struct {
    Registries []RegistryConfig `yaml:"registries"`
}

type Secrets struct {
    Secrets []SecretConfig `yaml:"secrets"`
}

func main() {
    registryConfigPath := os.Getenv("REGISTRY_CONFIG_PATH")
    secretsConfigPath := os.Getenv("SECRETS_CONFIG_PATH")

    if registryConfigPath == "" {
        log.Fatalf("REGISTRY_CONFIG_PATH environment variable is not set")
    }

    if secretsConfigPath == "" {
        log.Fatalf("SECRETS_CONFIG_PATH environment variable is not set")
    }

    log.Println("Starting the sync process...")

    config, err := loadConfig(registryConfigPath)
    if err != nil {
        log.Fatalf("Failed to load configuration: %v", err)
    }
    log.Println("Loaded configuration successfully.")

    secrets, err := loadSecrets(secretsConfigPath)
    if err != nil {
        log.Fatalf("Failed to load secrets: %v", err)
    }
    log.Println("Loaded secrets successfully.")

    for _, registry := range config.Registries {
        log.Printf("Starting sync for registry: %s/%s to %s/%s",
            registry.SourceRegistry, registry.SourceRepository,
            registry.DestRegistry, registry.DestRepository)

        secret, _ := getSecretConfig(registry.DestRegistry, secrets.Secrets)

        if isGCR(secret) && secret.ServiceAccountKey != "" {
            token, err := getGCRToken(secret.ServiceAccountKey)
            if err != nil {
                log.Fatalf("Failed to get GCR token: %v", err)
            }
            secret.Username = "oauth2accesstoken"
            secret.Password = token
        }

        if err := syncRegistryParallel(registry, secret.Username, secret.Password, secrets); err != nil {
            log.Printf("Sync failed for registry %s/%s: %v",
                registry.SourceRegistry, registry.SourceRepository, err)
            continue
        }
        log.Printf("Completed sync for %s/%s", registry.SourceRegistry, registry.SourceRepository)
    }

    log.Println("Sync process completed.")
}

func loadConfig(filename string) (*Config, error) {
    log.Printf("Loading configuration from file: %s", filename)
    data, err := ioutil.ReadFile(filename)
    if err != nil {
        return nil, err
    }

    var config Config
    if err := yaml.Unmarshal(data, &config); err != nil {
        return nil, err
    }

    return &config, nil
}

func loadSecrets(filename string) (*Secrets, error) {
    log.Printf("Loading secrets from file: %s", filename)
    data, err := ioutil.ReadFile(filename)
    if err != nil {
        return nil, err
    }

    var secrets Secrets
    if err := yaml.Unmarshal(data, &secrets); err != nil {
        return nil, err
    }

    return &secrets, nil
}

func getSecretConfig(registry string, secrets []SecretConfig) (SecretConfig, bool) {
    for _, secret := range secrets {
        if secret.DestRegistry == registry || secret.SourceRegistry == registry {
            return secret, true
        }
    }
    return SecretConfig{}, false
}

func getGCRToken(serviceAccountKeyPath string) (string, error) {
    data, err := ioutil.ReadFile(serviceAccountKeyPath)
    if err != nil {
        return "", fmt.Errorf("failed to read service account key file: %w", err)
    }

    conf, err := google.JWTConfigFromJSON(data, "https://www.googleapis.com/auth/devstorage.read_write")
    if err != nil {
        return "", fmt.Errorf("failed to create JWT config from JSON: %w", err)
    }

    token, err := conf.TokenSource(context.Background()).Token()
    if err != nil {
        return "", fmt.Errorf("failed to retrieve OAuth token: %w", err)
    }

    return token.AccessToken, nil
}

func isGCR(secret SecretConfig) bool {
    return secret.Type == "gcr"
}

func filterTags(tags []string, excludePatterns []string) []string {
    filteredTags := []string{}
    for _, tag := range tags {
        exclude := false
        for _, pattern := range excludePatterns {
            match, _ := regexp.MatchString(pattern, tag)
            if match {
                exclude = true
                break
            }
        }
        if !exclude {
            filteredTags = append(filteredTags, tag)
        }
    }
    return filteredTags
}

func sortTags(tags []string, versionFilters []VersionRequirement) []string {
    if len(tags) == 0 || len(versionFilters) == 0 {
        return make([]string, 0)
    }

    versionGroups := make(map[string][]semver.Version)
    tagMap := make(map[string]string)
    
    // Only process tags that match the base version format (e.g., 1.36.1, not 1.36.1-uclibc)
    baseVersionRegex := regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
    
    for _, tag := range tags {
        // Skip variant tags
        if !baseVersionRegex.MatchString(strings.TrimPrefix(tag, "v")) {
            continue
        }

        trimmedTag := strings.TrimPrefix(tag, "v")
        version, err := semver.Parse(trimmedTag)
        if err != nil {
            continue
        }

        key := fmt.Sprintf("%d.%d", version.Major, version.Minor)
        versionGroups[key] = append(versionGroups[key], version)
        tagMap[version.String()] = tag
    }

    var selectedVersions []semver.Version
    for _, filter := range versionFilters {
        key := fmt.Sprintf("%d.%d", filter.Major, filter.Minor)
        versions := versionGroups[key]
        
        if len(versions) == 0 {
            continue
        }

        sort.Slice(versions, func(i, j int) bool {
            return versions[i].GT(versions[j])
        })

        if filter.GetLatest {
            selectedVersions = append(selectedVersions, versions[0])
        } else {
            selectedVersions = append(selectedVersions, versions...)
        }
    }

    if len(selectedVersions) == 0 {
        return make([]string, 0)
    }

    sort.Slice(selectedVersions, func(i, j int) bool {
        return selectedVersions[i].GT(selectedVersions[j])
    })

    sortedTags := make([]string, 0, len(selectedVersions))
    for _, version := range selectedVersions {
        if tag, exists := tagMap[version.String()]; exists {
            sortedTags = append(sortedTags, tag)
        }
    }

    return sortedTags
}

func pullAndPushImage(ctx context.Context, registry RegistryConfig, tag, username, password string, sourceCtx *types.SystemContext) error {
    fullSourceImage := fmt.Sprintf("%s/%s:%s", registry.SourceRegistry, registry.SourceRepository, tag)
    fullDestImage := fmt.Sprintf("%s/%s:%s", registry.DestRegistry, registry.DestRepository, tag)

    log.Printf("Syncing image %s to %s", fullSourceImage, fullDestImage)

    srcRef, err := docker.ParseReference("//" + fullSourceImage)
    if err != nil {
        return fmt.Errorf("Failed to parse source image reference for %s: %v", fullSourceImage, err)
    }

    destRef, err := docker.ParseReference("//" + fullDestImage)
    if err != nil {
        return fmt.Errorf("Failed to parse destination image reference for %s: %v", fullDestImage, err)
    }

    destCtx := &types.SystemContext{
        DockerAuthConfig: &types.DockerAuthConfig{
            Username: username,
            Password: password,
        },
    }

    if registry.InsecureTLS {
        destCtx.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue
    }

    policyContext, err := signature.NewPolicyContext(&signature.Policy{
        Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()},
    })
    if err != nil {
        return fmt.Errorf("failed to create policy context: %w", err)
    }
    defer policyContext.Destroy()

    start := time.Now()
    _, err = copy.Image(ctx, policyContext, destRef, srcRef, &copy.Options{
        SourceCtx:      sourceCtx,
        DestinationCtx: destCtx,
    })
    duration := time.Since(start)

    if err != nil {
        return fmt.Errorf("failed to sync image %s to %s: %v", fullSourceImage, fullDestImage, err)
    }

    log.Printf("Successfully synced image %s to %s in %v", fullSourceImage, fullDestImage, duration)
    return nil
}

func syncRegistryParallel(registry RegistryConfig, username, password string, secrets *Secrets) error {
    ctx := context.Background()

    log.Printf("Fetching tags from source repository: %s/%s",
        registry.SourceRegistry, registry.SourceRepository)

    sourceSecret, hasSourceCredentials := getSecretConfig(registry.SourceRegistry, secrets.Secrets)

    var sourceCtx *types.SystemContext
    if hasSourceCredentials && sourceSecret.SourceType == "dockerhub" {
        sourceCtx = &types.SystemContext{
            DockerAuthConfig: &types.DockerAuthConfig{
                Username: sourceSecret.Username,
                Password: sourceSecret.Password,
            },
        }
    } else {
        sourceCtx = &types.SystemContext{}
    }

    if registry.InsecureTLS || (hasSourceCredentials && sourceSecret.InsecureTLS) {
        sourceCtx.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue
    }

    sourceImage := fmt.Sprintf("%s/%s", registry.SourceRegistry, registry.SourceRepository)
    sourceRef, err := docker.ParseReference("//" + sourceImage)
    if err != nil {
        return fmt.Errorf("failed to parse source image reference for %s: %w", sourceImage, err)
    }

    tags, err := docker.GetRepositoryTags(ctx, sourceCtx, sourceRef)
    if err != nil {
        return fmt.Errorf("failed to get tags: %w", err)
    }
    log.Printf("Fetched tags: %v", tags)

    filteredTags := filterTags(tags, registry.ExcludePatterns)
    log.Printf("Filtered tags: %v", filteredTags)

    sortedTags := sortTags(filteredTags, registry.VersionFilters)
    log.Printf("Sorted tags: %v", sortedTags)

    // Group tags by minor version
    if registry.TagLimit > 0 {
        minorVersionGroups := make(map[string][]string)
        for _, tag := range sortedTags {
            trimmedTag := strings.TrimPrefix(tag, "v")
            version, err := semver.Parse(trimmedTag)
            if err != nil {
                continue
            }
            key := fmt.Sprintf("%d.%d", version.Major, version.Minor)
            minorVersionGroups[key] = append(minorVersionGroups[key], tag)
        }

        // Apply limit to each minor version group
        var limitedTags []string
        for _, filter := range registry.VersionFilters {
            key := fmt.Sprintf("%d.%d", filter.Major, filter.Minor)
            if tags, exists := minorVersionGroups[key]; exists {
                if len(tags) > registry.TagLimit {
                    tags = tags[:registry.TagLimit]
                }
                limitedTags = append(limitedTags, tags...)
            }
        }
        sortedTags = limitedTags
    }

    log.Printf("Selected %d tags for syncing: %v", len(sortedTags), sortedTags)

    var wg sync.WaitGroup
    errorChan := make(chan error, len(sortedTags))

    for _, tag := range sortedTags {
        wg.Add(1)
        go func(tag string) {
            defer wg.Done()
            if err := pullAndPushImage(ctx, registry, tag, username, password, sourceCtx); err != nil {
                log.Printf("Failed to sync image %s: %v", tag, err)
                errorChan <- err
            }
        }(tag)
    }

    wg.Wait()
    close(errorChan)

    var errors []error
    for err := range errorChan {
        errors = append(errors, err)
    }

    if len(errors) > 0 {
        return fmt.Errorf("some tags failed to sync: %v", errors)
    }

    return nil
}
