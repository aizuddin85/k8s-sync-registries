package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"
	"strings"

	"github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/types"
	"github.com/blang/semver/v4"
	"golang.org/x/oauth2/google"
	"gopkg.in/yaml.v3"
)

type RegistryConfig struct {
	SourceRegistry   string   `yaml:"source_registry"`
	SourceRepository string   `yaml:"source_repository"`
	DestRegistry     string   `yaml:"dest_registry"`
	DestRepository   string   `yaml:"dest_repository"`
	TagLimit         int      `yaml:"tag_limit"`
	ExcludePatterns  []string `yaml:"exclude_patterns"`
}

type SecretConfig struct {
	DestRegistry      string `yaml:"dest_registry"`
	SourceRegistry    string `yaml:"source_registry,omitempty"`
	Type              string `yaml:"type"`           // Registry type, e.g., "gcr", "acr", "dockerhub"
	SourceType        string `yaml:"source_type"`    // Source registry type, e.g., "dockerhub"
	Username          string `yaml:"username,omitempty"`
	Password          string `yaml:"password,omitempty"`
	ServiceAccountKey string `yaml:"service_account_key,omitempty"`
}

type Config struct {
	Registries []RegistryConfig `yaml:"registries"`
}

type Secrets struct {
	Secrets []SecretConfig `yaml:"secrets"`
}

func main() {
	// Fetch config paths from environment variables
	registryConfigPath := os.Getenv("REGISTRY_CONFIG_PATH")
	secretsConfigPath := os.Getenv("SECRETS_CONFIG_PATH")

	if registryConfigPath == "" {
		log.Fatalf("REGISTRY_CONFIG_PATH environment variable is not set")
	}

	if secretsConfigPath == "" {
		log.Fatalf("SECRETS_CONFIG_PATH environment variable is not set")
	}

	log.Println("Starting the sync process...")

	// Load the YAML configuration file
	config, err := loadConfig(registryConfigPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	log.Println("Loaded configuration successfully.")

	// Load the secrets file
	secrets, err := loadSecrets(secretsConfigPath)
	if err != nil {
		log.Fatalf("Failed to load secrets: %v", err)
	}
	log.Println("Loaded secrets successfully.")

	// Loop through each registry configuration
	for _, registry := range config.Registries {
		log.Printf("Starting sync for registry: %s/%s to %s/%s", registry.SourceRegistry, registry.SourceRepository, registry.DestRegistry, registry.DestRepository)

		// Retrieve the credentials for the destination registry
		secret, _ := getSecretConfig(registry.DestRegistry, secrets.Secrets)

		if isGCR(secret) && secret.ServiceAccountKey != "" {
			// Authenticate using the service account key
			token, err := getGCRToken(secret.ServiceAccountKey)
			if err != nil {
				log.Fatalf("Failed to get GCR token: %v", err)
			}
			secret.Username = "oauth2accesstoken"
			secret.Password = token
		}

		if err := syncRegistryParallel(registry, secret.Username, secret.Password, secrets); err != nil {
			log.Printf("Failed to sync %s: %v", registry.SourceRepository, err)
		} else {
			log.Printf("Completed sync for %s/%s", registry.SourceRegistry, registry.SourceRepository)
		}
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

	// Get the token from the JWT config
	token, err := conf.TokenSource(context.Background()).Token()
	if err != nil {
		return "", fmt.Errorf("failed to retrieve OAuth token: %w", err)
	}

	return token.AccessToken, nil
}

func isGCR(secret SecretConfig) bool {
	return secret.Type == "gcr"
}

func syncRegistryParallel(registry RegistryConfig, username, password string, secrets *Secrets) error {
	ctx := context.Background()

	// Create a source image reference to fetch tags
	log.Printf("Fetching tags from source repository: %s/%s", registry.SourceRegistry, registry.SourceRepository)

	// Retrieve credentials for the source registry if necessary
	sourceSecret, hasSourceCredentials := getSecretConfig(registry.SourceRegistry, secrets.Secrets)

	var sourceCtx *types.SystemContext
	if hasSourceCredentials && sourceSecret.SourceType == "dockerhub" {
		// Setup source context with credentials for Docker Hub or other source registries
		sourceCtx = &types.SystemContext{
			DockerAuthConfig: &types.DockerAuthConfig{
				Username: sourceSecret.Username,
				Password: sourceSecret.Password,
			},
		}
	} else {
		sourceCtx = &types.SystemContext{}
	}

	sourceImage := fmt.Sprintf("%s/%s", registry.SourceRegistry, registry.SourceRepository)
	sourceRef, err := docker.ParseReference("//" + sourceImage)
	if err != nil {
		return fmt.Errorf("failed to parse source image reference for %s: %w", sourceImage, err)
	}

	// Fetch tags from the source repository
	tags, err := docker.GetRepositoryTags(ctx, sourceCtx, sourceRef)
	if err != nil {
		return fmt.Errorf("failed to get tags: %w", err)
	}
	log.Printf("Fetched %d tags from source repository.", len(tags))

	// Exclude tags based on patterns
	filteredTags := filterTags(tags, registry.ExcludePatterns)
	log.Printf("Filtered tags: %v", filteredTags)

	// Sort the tags using semantic versioning
	sortedTags := sortTags(filteredTags)
	log.Printf("Sorted tags: %v", sortedTags)

	// Take the latest tags based on the tag limit
	if len(sortedTags) > registry.TagLimit {
		sortedTags = sortedTags[:registry.TagLimit]
	}
	log.Printf("Selected %d latest tags for syncing: %v", len(sortedTags), sortedTags)

	var wg sync.WaitGroup
	for _, tag := range sortedTags {
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			if err := pullAndPushImage(ctx, registry, tag, username, password, sourceCtx); err != nil {
				log.Printf("Failed to sync image %s: %v", tag, err)
			}
		}(tag)
	}
	wg.Wait()

	return nil
}

// sortTags sorts the tags based on semantic versioning
func sortTags(tags []string) []string {
	var validVersions []semver.Version
	tagMap := make(map[string]string)

	for _, tag := range tags {
		// Remove any 'v' prefix for semantic version parsing
		trimmedTag := strings.TrimPrefix(tag, "v")

		// Attempt to parse the semantic version
		version, err := semver.Parse(trimmedTag)
		if err == nil {
			validVersions = append(validVersions, version)
			tagMap[version.String()] = tag // Keep the original tag mapping
		}
	}

	// Sort the versions
	sort.Slice(validVersions, func(i, j int) bool {
		return validVersions[i].GT(validVersions[j]) // Sort in descending order
	})

	// Rebuild the sorted tags list from the version map
	var sortedTags []string
	for _, version := range validVersions {
		sortedTags = append(sortedTags, tagMap[version.String()])
	}

	return sortedTags
}

func pullAndPushImage(ctx context.Context, registry RegistryConfig, tag, username, password string, sourceCtx *types.SystemContext) error {
	fullSourceImage := fmt.Sprintf("%s/%s:%s", registry.SourceRegistry, registry.SourceRepository, tag)
	fullDestImage := fmt.Sprintf("%s/%s:%s", registry.DestRegistry, registry.DestRepository, tag)

	log.Printf("Syncing image %s to %s", fullSourceImage, fullDestImage)

	// Parse the source reference again with the tag
	srcRef, err := docker.ParseReference("//" + fullSourceImage)
	if err != nil {
		return fmt.Errorf("Failed to parse source image reference for %s: %v", fullSourceImage, err)
	}

	destRef, err := docker.ParseReference("//" + fullDestImage)
	if err != nil {
		return fmt.Errorf("Failed to parse destination image reference for %s: %v", fullDestImage, err)
	}

	// Set up the destination context
	destCtx := &types.SystemContext{
		DockerAuthConfig: &types.DockerAuthConfig{
			Username: username,
			Password: password,
		},
	}

	// Copy the image from source to destination
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
