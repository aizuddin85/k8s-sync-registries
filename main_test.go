package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/containers/image/v5/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	content := []byte(`
registries:
  - source_registry: "docker.io"
    source_repository: "test/app"
    dest_registry: "private.registry.com"
    dest_repository: "test/app"
    tag_limit: 5
    insecure_tls: true
    exclude_patterns:
      - "alpha"
      - "beta"
    version_filters:
      - major: 1
        minor: 3
        get_latest: true
`)
	tmpfile, err := os.CreateTemp("", "config*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	_, err = tmpfile.Write(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	config, err := loadConfig(tmpfile.Name())
	assert.NoError(t, err)
	assert.NotNil(t, config)
	assert.Equal(t, 1, len(config.Registries))
	assert.Equal(t, "docker.io", config.Registries[0].SourceRegistry)
	assert.Equal(t, 5, config.Registries[0].TagLimit)
	assert.Equal(t, 2, len(config.Registries[0].ExcludePatterns))
	assert.Equal(t, 1, len(config.Registries[0].VersionFilters))
	assert.True(t, config.Registries[0].InsecureTLS)
}

func TestLoadSecrets(t *testing.T) {
	content := []byte(`
secrets:
  - dest_registry: "private.registry.com"
    type: "dockerhub"
    username: "testuser"
    password: "testpass"
    insecure_tls: true
`)
	tmpfile, err := os.CreateTemp("", "secrets*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	_, err = tmpfile.Write(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	secrets, err := loadSecrets(tmpfile.Name())
	assert.NoError(t, err)
	assert.NotNil(t, secrets)
	assert.Equal(t, 1, len(secrets.Secrets))
	assert.Equal(t, "private.registry.com", secrets.Secrets[0].DestRegistry)
	assert.Equal(t, "testuser", secrets.Secrets[0].Username)
	assert.True(t, secrets.Secrets[0].InsecureTLS)
}

func TestPullAndPushImageWithTLS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-dependent test in short mode")
	}

	ctx := context.Background()

	registry := RegistryConfig{
		SourceRegistry:   "quay.io",
		SourceRepository: "nonexistent/nonexistent-repo",
		DestRegistry:     "registry.apps.aizuddinzali.com:5000",
		DestRepository:   "unittest/argocd",
		InsecureTLS:      true,
	}

	sourceCtx := &types.SystemContext{
		DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
	}

	destSecret := SecretConfig{
		Username:    "admin",
		Password:    "admin123",
		InsecureTLS: true,
	}

	err := pullAndPushImage(ctx, registry, "nonexistent-tag", destSecret, sourceCtx)
	assert.Error(t, err)
}

func TestGetSecretConfigWithTLS(t *testing.T) {
	secrets := []SecretConfig{
		{
			DestRegistry: "registry1.com",
			Username:     "user1",
			Password:     "pass1",
			InsecureTLS:  true,
		},
		{
			DestRegistry: "registry2.com",
			Username:     "user2",
			Password:     "pass2",
			InsecureTLS:  false,
		},
		{
			SourceRegistry: "docker.io",
			SourceType:     "dockerhub",
			Username:       "srcuser",
			Password:       "srcpass",
		},
	}

	tests := []struct {
		name     string
		registry string
		want     SecretConfig
		found    bool
		tlsValue bool
	}{
		{
			name:     "existing registry with insecure TLS",
			registry: "registry1.com",
			want:     secrets[0],
			found:    true,
			tlsValue: true,
		},
		{
			name:     "existing registry with secure TLS",
			registry: "registry2.com",
			want:     secrets[1],
			found:    true,
			tlsValue: false,
		},
		{
			name:     "non-existing registry",
			registry: "nonexistent.com",
			want:     SecretConfig{},
			found:    false,
			tlsValue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := getSecretConfig(tt.registry, secrets)
			assert.Equal(t, tt.found, found)
			assert.Equal(t, tt.want, got)
			if found {
				assert.Equal(t, tt.tlsValue, got.InsecureTLS)
			}
		})
	}
}

func TestSourceAndDestSecretLookup(t *testing.T) {
	secrets := []SecretConfig{
		{
			SourceRegistry: "docker.io",
			SourceType:     "dockerhub",
			Username:       "src",
			Password:       "srcpass",
		},
		{
			DestRegistry: "gcr.io",
			Type:         "gcr",
			Username:     "dst",
			Password:     "dstpass",
		},
	}

	src, found := getSourceSecretConfig("docker.io", secrets)
	assert.True(t, found)
	assert.Equal(t, "src", src.Username)

	_, found = getSourceSecretConfig("gcr.io", secrets)
	assert.False(t, found)

	dst, found := getDestSecretConfig("gcr.io", secrets)
	assert.True(t, found)
	assert.Equal(t, "dst", dst.Username)

	_, found = getDestSecretConfig("docker.io", secrets)
	assert.False(t, found)
}

func TestFilterTags(t *testing.T) {
	tags := []string{"v1.2.3", "v1.2.3-alpha", "v1.2.3-beta", "latest"}

	filtered, err := filterTags(tags, []string{"alpha", "beta", "latest"})
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.2.3"}, filtered)

	_, err = filterTags(tags, []string{"[invalid"})
	assert.Error(t, err)
}

func TestSelectTagsToSync(t *testing.T) {
	tags := []string{
		"v1.2.0", "v1.2.1", "v1.2.2",
		"v1.3.0", "v1.3.1",
		"v1.2.1-alpha", "latest",
	}

	registry := RegistryConfig{
		TagLimit: 2,
		ExcludePatterns: []string{
			"alpha",
			"latest",
		},
		VersionFilters: []VersionRequirement{
			{Major: 1, Minor: 2, GetLatest: false},
			{Major: 1, Minor: 3, GetLatest: false},
		},
	}

	got, err := selectTagsToSync(tags, registry)
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.2.2", "v1.2.1", "v1.3.1", "v1.3.0"}, got)
}

func TestSelectTagsWithoutVersionFilters(t *testing.T) {
	tags := []string{"v1.0.0", "v1.1.0", "v1.2.0", "latest", "v1.1.0-rc"}
	registry := RegistryConfig{
		TagLimit:        2,
		ExcludePatterns: []string{"rc", "latest"},
	}

	got, err := selectTagsToSync(tags, registry)
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.2.0", "v1.1.0"}, got)
}

func TestSortTagsWithMultipleMinors(t *testing.T) {
	tags := []string{
		"v1.2.0", "v1.2.1", "v1.2.2",
		"v1.3.0", "v1.3.1", "v1.3.2",
		"v1.4.0", "v1.4.1",
		"v2.0.0", "v2.0.1",
		"v1.2-alpha", "v1.3-beta",
	}

	tests := []struct {
		name           string
		versionFilters []VersionRequirement
		want           []string
		description    string
	}{
		{
			name: "get latest patches from multiple minors",
			versionFilters: []VersionRequirement{
				{Major: 1, Minor: 2, GetLatest: true},
				{Major: 1, Minor: 3, GetLatest: true},
				{Major: 1, Minor: 4, GetLatest: true},
			},
			want:        []string{"v1.4.1", "v1.3.2", "v1.2.2"},
			description: "Should get the latest patch from each minor version",
		},
		{
			name: "get all patches from specific minors",
			versionFilters: []VersionRequirement{
				{Major: 1, Minor: 2, GetLatest: false},
				{Major: 1, Minor: 3, GetLatest: false},
			},
			want: []string{
				"v1.3.2", "v1.3.1", "v1.3.0",
				"v1.2.2", "v1.2.1", "v1.2.0",
			},
			description: "Should get all patches from specified minor versions",
		},
		{
			name: "mix of latest and all patches",
			versionFilters: []VersionRequirement{
				{Major: 1, Minor: 2, GetLatest: true},
				{Major: 1, Minor: 3, GetLatest: false},
			},
			want: []string{
				"v1.3.2", "v1.3.1", "v1.3.0",
				"v1.2.2",
			},
			description: "Should get latest from 1.2 and all from 1.3",
		},
		{
			name: "non-existent minor version",
			versionFilters: []VersionRequirement{
				{Major: 1, Minor: 5, GetLatest: true},
				{Major: 1, Minor: 2, GetLatest: true},
			},
			want:        []string{"v1.2.2"},
			description: "Should gracefully handle non-existent minor versions",
		},
		{
			name: "wrong major version",
			versionFilters: []VersionRequirement{
				{Major: 3, Minor: 1, GetLatest: true},
			},
			want:        []string{},
			description: "Should return empty list for non-existent major version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortTags(tags, tt.versionFilters)
			assert.Equal(t, tt.want, got, tt.description)

			if len(tt.want) > 0 {
				for i := 1; i < len(got); i++ {
					prev := got[i-1]
					curr := got[i]
					prevVer, _ := semver.Parse(strings.TrimPrefix(prev, "v"))
					currVer, _ := semver.Parse(strings.TrimPrefix(curr, "v"))
					assert.True(t, prevVer.GT(currVer),
						"Version ordering incorrect: %s should be greater than %s", prev, curr)
				}
			}
		})
	}
}

func TestSortTagsEdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		tags           []string
		versionFilters []VersionRequirement
		want           []string
		description    string
	}{
		{
			name: "invalid version tags",
			tags: []string{
				"latest", "main", "v1.2.3", "invalid-version",
				"v1.2.x", "v1.2", "1.2", "v1.2.3-alpha",
			},
			versionFilters: []VersionRequirement{
				{Major: 1, Minor: 2, GetLatest: true},
			},
			want:        []string{"v1.2.3"},
			description: "Should handle invalid version tags gracefully",
		},
		{
			name: "empty tag list",
			tags: []string{},
			versionFilters: []VersionRequirement{
				{Major: 1, Minor: 2, GetLatest: true},
			},
			want:        []string{},
			description: "Should handle empty tag list",
		},
		{
			name:           "empty filters falls back to all semver tags",
			tags:           []string{"v1.2.3", "v1.2.4", "latest"},
			versionFilters: []VersionRequirement{},
			want:           []string{"v1.2.4", "v1.2.3"},
			description:    "Should return newest semver tags when filters are empty",
		},
		{
			name: "duplicate versions",
			tags: []string{
				"v1.2.3", "v1.2.3", "v1.2.4", "v1.2.4",
			},
			versionFilters: []VersionRequirement{
				{Major: 1, Minor: 2, GetLatest: true},
			},
			want:        []string{"v1.2.4"},
			description: "Should handle duplicate versions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortTags(tt.tags, tt.versionFilters)
			assert.Equal(t, tt.want, got, tt.description)
		})
	}
}

func TestSyncConcurrencyDefault(t *testing.T) {
	t.Setenv("SYNC_CONCURRENCY", "")
	assert.Equal(t, defaultSyncConcurrency, syncConcurrency())

	t.Setenv("SYNC_CONCURRENCY", "5")
	assert.Equal(t, 5, syncConcurrency())

	t.Setenv("SYNC_CONCURRENCY", "0")
	assert.Equal(t, defaultSyncConcurrency, syncConcurrency())

	t.Setenv("SYNC_CONCURRENCY", "bogus")
	assert.Equal(t, defaultSyncConcurrency, syncConcurrency())
}
