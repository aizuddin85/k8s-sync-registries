package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"regexp"
	"sort"
	"time"

	"github.com/briandowns/spinner"
	"github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/types"
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
	Type              string `yaml:"type"` // Registry type, e.g., "gcr", "acr"
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
	log.Println("Starting the sync process...")

	// Load the YAML configuration file
	config, err := loadConfig("registries.yaml")
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	log.Println("Loaded configuration successfully.")

	// Load the secrets file
	secrets, err := loadSecrets("secrets.yaml")
	if err != nil {
		log.Fatalf("Failed to load secrets: %v", err)
	}
	log.Println("Loaded secrets successfully.")

	// Loop through each registry configuration
	for _, registry := range config.Registries {
		log.Printf("Starting sync for registry: %s/%s to %s/%s", registry.SourceRegistry, registry.SourceRepository, registry.DestRegistry, registry.DestRepository)

		// Retrieve the credentials for the destination registry
		secret := getSecretConfig(registry.DestRegistry, secrets.Secrets)

		if isGCR(secret) && secret.ServiceAccountKey != "" {
			// Authenticate using the service account key
			token, err := getGCRToken(secret.ServiceAccountKey)
			if err != nil {
				log.Fatalf("Failed to get GCR token: %v", err)
			}
			secret.Username = "oauth2accesstoken"
			secret.Password = token
		}

		if err := syncRegistry(registry, secret.Username, secret.Password); err != nil {
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

func getSecretConfig(destRegistry string, secrets []SecretConfig) SecretConfig {
	for _, secret := range secrets {
		if secret.DestRegistry == destRegistry {
			return secret
		}
	}
	return SecretConfig{}
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

func syncRegistry(registry RegistryConfig, username, password string) error {
	ctx := context.Background()

	// Create a source image reference to fetch tags
	log.Printf("Fetching tags from source repository: %s/%s", registry.SourceRegistry, registry.SourceRepository)
	sourceCtx := &types.SystemContext{}
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

	// Sort the tags (assuming semantic versioning)
	log.Println("Sorting tags to determine the latest ones.")
	sort.Slice(filteredTags, func(i, j int) bool {
		return filteredTags[i] > filteredTags[j]
	})

	// Take the latest tags based on the tag limit
	if len(filteredTags) > registry.TagLimit {
		filteredTags = filteredTags[:registry.TagLimit]
	}
	log.Printf("Selected %d latest tags for syncing: %v", len(filteredTags), filteredTags)

	// Set up the destination context
	var destCtx *types.SystemContext
	if username != "" {
		// Use credentials if provided
		destCtx = &types.SystemContext{
			DockerAuthConfig: &types.DockerAuthConfig{
				Username: username,
				Password: password,
			},
		}
	} else {
		// No credentials
		destCtx = &types.SystemContext{}
	}

	for _, tag := range filteredTags {
		fullSourceImage := fmt.Sprintf("%s/%s:%s", registry.SourceRegistry, registry.SourceRepository, tag)
		fullDestImage := fmt.Sprintf("%s/%s:%s", registry.DestRegistry, registry.DestRepository, tag)

		log.Printf("Syncing image %s to %s", fullSourceImage, fullDestImage)

		// Parse the source reference again with the tag
		srcRef, err := docker.ParseReference("//" + fullSourceImage)
		if err != nil {
			log.Printf("Failed to parse source image reference for %s: %v", fullSourceImage, err)
			continue
		}

		destRef, err := docker.ParseReference("//" + fullDestImage)
		if err != nil {
			log.Printf("Failed to parse destination image reference for %s: %v", fullDestImage, err)
			continue
		}

		// Initialize spinner
		s := spinner.New(spinner.CharSets[14], 100*time.Millisecond) // Choose spinner style and speed
		s.Start()

		// Copy the image from source to destination
		policyContext, err := signature.NewPolicyContext(&signature.Policy{
			Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()},
		})
		if err != nil {
			s.Stop()
			return fmt.Errorf("failed to create policy context: %w", err)
		}
		defer policyContext.Destroy()

		start := time.Now()
		_, err = copy.Image(ctx, policyContext, destRef, srcRef, &copy.Options{
			SourceCtx:      sourceCtx,
			DestinationCtx: destCtx,
		})
		s.Stop()
		duration := time.Since(start)

		if err != nil {
			log.Printf("Failed to sync image %s to %s: %v", fullSourceImage, fullDestImage, err)
		} else {
			log.Printf("Successfully synced image %s to %s in %v", fullSourceImage, fullDestImage, duration)
		}
	}

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

