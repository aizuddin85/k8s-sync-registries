package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
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

const (
	defaultSyncConcurrency = 3
	defaultSyncTimeout     = 30 * time.Minute
)

var baseVersionRegex = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

type VersionRequirement struct {
	Major     int  `yaml:"major"`
	Minor     int  `yaml:"minor"`
	GetLatest bool `yaml:"get_latest"`
}

type RegistryConfig struct {
	SourceRegistry   string               `yaml:"source_registry"`
	SourceRepository string               `yaml:"source_repository"`
	DestRegistry     string               `yaml:"dest_registry"`
	DestRepository   string               `yaml:"dest_repository"`
	TagLimit         int                  `yaml:"tag_limit"`
	ExcludePatterns  []string             `yaml:"exclude_patterns"`
	VersionFilters   []VersionRequirement `yaml:"version_filters"`
	InsecureTLS      bool                 `yaml:"insecure_tls"`
}

type SecretConfig struct {
	DestRegistry      string `yaml:"dest_registry"`
	SourceRegistry    string `yaml:"source_registry,omitempty"`
	Type              string `yaml:"type"`
	SourceType        string `yaml:"source_type"`
	Username          string `yaml:"username,omitempty"`
	Password          string `yaml:"password,omitempty"`
	ServiceAccountKey string `yaml:"service_account_key,omitempty"`
	InsecureTLS       bool   `yaml:"insecure_tls"`
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

	concurrency := syncConcurrency()
	log.Printf("Using sync concurrency of %d", concurrency)

	var syncFailures []string
	for _, registry := range config.Registries {
		log.Printf("Starting sync for registry: %s/%s to %s/%s",
			registry.SourceRegistry, registry.SourceRepository,
			registry.DestRegistry, registry.DestRepository)

		secret, found := getDestSecretConfig(registry.DestRegistry, secrets.Secrets)
		if !found {
			log.Printf("Warning: no destination credentials found for %s; attempting unauthenticated push",
				registry.DestRegistry)
		}

		if isGCR(secret) && secret.ServiceAccountKey != "" {
			token, err := getGCRToken(secret.ServiceAccountKey)
			if err != nil {
				log.Fatalf("Failed to get GCR token: %v", err)
			}
			secret.Username = "oauth2accesstoken"
			secret.Password = token
		}

		if err := syncRegistryParallel(registry, secret, secrets, concurrency); err != nil {
			log.Printf("Sync failed for registry %s/%s: %v",
				registry.SourceRegistry, registry.SourceRepository, err)
			syncFailures = append(syncFailures, fmt.Sprintf("%s/%s: %v",
				registry.SourceRegistry, registry.SourceRepository, err))
			continue
		}
		log.Printf("Completed sync for %s/%s", registry.SourceRegistry, registry.SourceRepository)
	}

	if len(syncFailures) > 0 {
		log.Fatalf("Sync process completed with %d failure(s): %v", len(syncFailures), syncFailures)
	}

	log.Println("Sync process completed.")
}

func syncConcurrency() int {
	raw := os.Getenv("SYNC_CONCURRENCY")
	if raw == "" {
		return defaultSyncConcurrency
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		log.Printf("Invalid SYNC_CONCURRENCY=%q; using default %d", raw, defaultSyncConcurrency)
		return defaultSyncConcurrency
	}
	return n
}

func loadConfig(filename string) (*Config, error) {
	log.Printf("Loading configuration from file: %s", filename)
	data, err := os.ReadFile(filename)
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
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var secrets Secrets
	if err := yaml.Unmarshal(data, &secrets); err != nil {
		return nil, err
	}

	return &secrets, nil
}

// getSecretConfig looks up a secret matching either source or destination registry.
// Prefer getSourceSecretConfig / getDestSecretConfig for unambiguous lookups.
func getSecretConfig(registry string, secrets []SecretConfig) (SecretConfig, bool) {
	if secret, found := getDestSecretConfig(registry, secrets); found {
		return secret, true
	}
	return getSourceSecretConfig(registry, secrets)
}

func getDestSecretConfig(registry string, secrets []SecretConfig) (SecretConfig, bool) {
	for _, secret := range secrets {
		if secret.DestRegistry == registry {
			return secret, true
		}
	}
	return SecretConfig{}, false
}

func getSourceSecretConfig(registry string, secrets []SecretConfig) (SecretConfig, bool) {
	for _, secret := range secrets {
		if secret.SourceRegistry == registry {
			return secret, true
		}
	}
	return SecretConfig{}, false
}

func getGCRToken(serviceAccountKeyPath string) (string, error) {
	data, err := os.ReadFile(serviceAccountKeyPath)
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

func filterTags(tags []string, excludePatterns []string) ([]string, error) {
	compiled := make([]*regexp.Regexp, 0, len(excludePatterns))
	for _, pattern := range excludePatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid exclude pattern %q: %w", pattern, err)
		}
		compiled = append(compiled, re)
	}

	filteredTags := make([]string, 0, len(tags))
	for _, tag := range tags {
		exclude := false
		for _, re := range compiled {
			if re.MatchString(tag) {
				exclude = true
				break
			}
		}
		if !exclude {
			filteredTags = append(filteredTags, tag)
		}
	}
	return filteredTags, nil
}

func sortTags(tags []string, versionFilters []VersionRequirement) []string {
	if len(tags) == 0 {
		return []string{}
	}

	// When no version filters are configured, return all valid semver tags newest-first.
	if len(versionFilters) == 0 {
		return sortAllSemverTags(tags)
	}

	versionGroups := make(map[string][]semver.Version)
	tagMap := make(map[string]string)

	for _, tag := range tags {
		trimmedTag := strings.TrimPrefix(tag, "v")
		if !baseVersionRegex.MatchString(trimmedTag) {
			continue
		}

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
		return []string{}
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

func sortAllSemverTags(tags []string) []string {
	type taggedVersion struct {
		version semver.Version
		tag     string
	}

	parsed := make([]taggedVersion, 0, len(tags))
	seen := make(map[string]struct{})
	for _, tag := range tags {
		trimmedTag := strings.TrimPrefix(tag, "v")
		if !baseVersionRegex.MatchString(trimmedTag) {
			continue
		}
		version, err := semver.Parse(trimmedTag)
		if err != nil {
			continue
		}
		if _, ok := seen[version.String()]; ok {
			continue
		}
		seen[version.String()] = struct{}{}
		parsed = append(parsed, taggedVersion{version: version, tag: tag})
	}

	sort.Slice(parsed, func(i, j int) bool {
		return parsed[i].version.GT(parsed[j].version)
	})

	sorted := make([]string, 0, len(parsed))
	for _, item := range parsed {
		sorted = append(sorted, item.tag)
	}
	return sorted
}

func applyTagLimit(tags []string, versionFilters []VersionRequirement, tagLimit int) []string {
	if tagLimit <= 0 || len(tags) == 0 {
		return tags
	}

	if len(versionFilters) == 0 {
		if len(tags) > tagLimit {
			return tags[:tagLimit]
		}
		return tags
	}

	minorVersionGroups := make(map[string][]string)
	for _, tag := range tags {
		trimmedTag := strings.TrimPrefix(tag, "v")
		version, err := semver.Parse(trimmedTag)
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%d.%d", version.Major, version.Minor)
		minorVersionGroups[key] = append(minorVersionGroups[key], tag)
	}

	var limitedTags []string
	for _, filter := range versionFilters {
		key := fmt.Sprintf("%d.%d", filter.Major, filter.Minor)
		groupTags, exists := minorVersionGroups[key]
		if !exists {
			continue
		}
		if len(groupTags) > tagLimit {
			groupTags = groupTags[:tagLimit]
		}
		limitedTags = append(limitedTags, groupTags...)
	}
	return limitedTags
}

func selectTagsToSync(tags []string, registry RegistryConfig) ([]string, error) {
	filteredTags, err := filterTags(tags, registry.ExcludePatterns)
	if err != nil {
		return nil, err
	}

	sortedTags := sortTags(filteredTags, registry.VersionFilters)
	return applyTagLimit(sortedTags, registry.VersionFilters, registry.TagLimit), nil
}

func pullAndPushImage(ctx context.Context, registry RegistryConfig, tag string, destSecret SecretConfig, sourceCtx *types.SystemContext) error {
	fullSourceImage := fmt.Sprintf("%s/%s:%s", registry.SourceRegistry, registry.SourceRepository, tag)
	fullDestImage := fmt.Sprintf("%s/%s:%s", registry.DestRegistry, registry.DestRepository, tag)

	log.Printf("Syncing image %s to %s", fullSourceImage, fullDestImage)

	srcRef, err := docker.ParseReference("//" + fullSourceImage)
	if err != nil {
		return fmt.Errorf("failed to parse source image reference for %s: %w", fullSourceImage, err)
	}

	destRef, err := docker.ParseReference("//" + fullDestImage)
	if err != nil {
		return fmt.Errorf("failed to parse destination image reference for %s: %w", fullDestImage, err)
	}

	destCtx := &types.SystemContext{
		DockerAuthConfig: &types.DockerAuthConfig{
			Username: destSecret.Username,
			Password: destSecret.Password,
		},
	}

	if registry.InsecureTLS || destSecret.InsecureTLS {
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
		return fmt.Errorf("failed to sync image %s to %s: %w", fullSourceImage, fullDestImage, err)
	}

	log.Printf("Successfully synced image %s to %s in %v", fullSourceImage, fullDestImage, duration)
	return nil
}

func buildSourceContext(registry RegistryConfig, secrets *Secrets) *types.SystemContext {
	sourceSecret, hasSourceCredentials := getSourceSecretConfig(registry.SourceRegistry, secrets.Secrets)

	sourceCtx := &types.SystemContext{}
	if hasSourceCredentials && (sourceSecret.SourceType == "dockerhub" || sourceSecret.Username != "") {
		sourceCtx.DockerAuthConfig = &types.DockerAuthConfig{
			Username: sourceSecret.Username,
			Password: sourceSecret.Password,
		}
	}

	if registry.InsecureTLS || (hasSourceCredentials && sourceSecret.InsecureTLS) {
		sourceCtx.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue
	}

	return sourceCtx
}

func syncRegistryParallel(registry RegistryConfig, destSecret SecretConfig, secrets *Secrets, concurrency int) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultSyncTimeout)
	defer cancel()

	log.Printf("Fetching tags from source repository: %s/%s",
		registry.SourceRegistry, registry.SourceRepository)

	sourceCtx := buildSourceContext(registry, secrets)

	sourceImage := fmt.Sprintf("%s/%s", registry.SourceRegistry, registry.SourceRepository)
	sourceRef, err := docker.ParseReference("//" + sourceImage)
	if err != nil {
		return fmt.Errorf("failed to parse source image reference for %s: %w", sourceImage, err)
	}

	tags, err := docker.GetRepositoryTags(ctx, sourceCtx, sourceRef)
	if err != nil {
		return fmt.Errorf("failed to get tags: %w", err)
	}
	log.Printf("Fetched %d tags", len(tags))

	sortedTags, err := selectTagsToSync(tags, registry)
	if err != nil {
		return err
	}
	log.Printf("Selected %d tags for syncing: %v", len(sortedTags), sortedTags)

	if len(sortedTags) == 0 {
		log.Printf("No tags selected for %s/%s; skipping", registry.SourceRegistry, registry.SourceRepository)
		return nil
	}

	if concurrency < 1 {
		concurrency = defaultSyncConcurrency
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	errorChan := make(chan error, len(sortedTags))

	for _, tag := range sortedTags {
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := pullAndPushImage(ctx, registry, tag, destSecret, sourceCtx); err != nil {
				log.Printf("Failed to sync image %s: %v", tag, err)
				errorChan <- err
			}
		}(tag)
	}

	wg.Wait()
	close(errorChan)

	var syncErrors []error
	for err := range errorChan {
		syncErrors = append(syncErrors, err)
	}

	if len(syncErrors) > 0 {
		return fmt.Errorf("some tags failed to sync: %w", errors.Join(syncErrors...))
	}

	return nil
}
