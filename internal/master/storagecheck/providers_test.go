package storagecheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseS3Providers(t *testing.T) {
	input := []byte(`{
		"providers": [
			{
				"Name": "s3",
				"Options": [
					{
						"Name": "provider",
						"Help": "Choose your S3 provider.",
						"Examples": [
							{"Value": "AWS", "Help": "Amazon Web Services"},
							{"Value": "Minio", "Help": "Minio Object Storage"},
							{"Value": "Cloudflare", "Help": "Cloudflare R2 Storage"}
						]
					},
					{
						"Name": "access_key_id",
						"Help": "AWS Access Key ID."
					}
				]
			},
			{
				"Name": "drive",
				"Options": []
			}
		]
	}`)

	providers, err := ParseS3Providers(input)
	require.NoError(t, err)
	require.Len(t, providers, 3)
	assert.Equal(t, S3Provider{Value: "AWS", Help: "Amazon Web Services"}, providers[0])
	assert.Equal(t, S3Provider{Value: "Minio", Help: "Minio Object Storage"}, providers[1])
	assert.Equal(t, S3Provider{Value: "Cloudflare", Help: "Cloudflare R2 Storage"}, providers[2])
}

func TestParseS3ProvidersNoS3Backend(t *testing.T) {
	input := []byte(`{"providers": [{"Name": "drive", "Options": []}]}`)

	providers, err := ParseS3Providers(input)
	require.NoError(t, err)
	assert.Empty(t, providers)
}

func TestParseS3ProvidersInvalidJSON(t *testing.T) {
	_, err := ParseS3Providers([]byte(`not json`))
	require.Error(t, err)
}

func TestParseS3ProvidersNoProviderOption(t *testing.T) {
	input := []byte(`{
		"providers": [{
			"Name": "s3",
			"Options": [{"Name": "access_key_id", "Help": "key"}]
		}]
	}`)

	providers, err := ParseS3Providers(input)
	require.NoError(t, err)
	assert.Empty(t, providers)
}

func TestLoadS3ProvidersUsesRunnerOutput(t *testing.T) {
	called := false
	loader := &ProviderLoader{
		RunFunc: func() ([]byte, error) {
			called = true
			return []byte(`{
				"providers": [{
					"Name": "s3",
					"Options": [{
						"Name": "provider",
						"Examples": [
							{"Value": "AWS", "Help": "Amazon Web Services"},
							{"Value": "Minio", "Help": "Minio Object Storage"}
						]
					}]
				}]
			}`), nil
		},
	}

	providers, err := loader.Load()
	require.NoError(t, err)
	require.True(t, called)
	require.Len(t, providers, 2)
	assert.Equal(t, "AWS", providers[0].Value)
	assert.Equal(t, "Minio", providers[1].Value)
}

func TestLoadS3ProvidersCachesResult(t *testing.T) {
	callCount := 0
	loader := &ProviderLoader{
		RunFunc: func() ([]byte, error) {
			callCount++
			return []byte(`{
				"providers": [{
					"Name": "s3",
					"Options": [{
						"Name": "provider",
						"Examples": [{"Value": "AWS", "Help": "Amazon Web Services"}]
					}]
				}]
			}`), nil
		},
	}

	p1, err := loader.Load()
	require.NoError(t, err)
	p2, err := loader.Load()
	require.NoError(t, err)
	assert.Equal(t, p1, p2)
	assert.Equal(t, 1, callCount)
}

func TestLoadS3ProvidersRunErrorReturnsFallback(t *testing.T) {
	loader := &ProviderLoader{
		RunFunc: func() ([]byte, error) {
			return nil, assert.AnError
		},
	}

	providers, err := loader.Load()
	require.NoError(t, err)
	require.NotEmpty(t, providers)
	assert.Equal(t, "AWS", providers[0].Value)
	assert.Equal(t, "Other", providers[len(providers)-1].Value)
}

func TestNewProviderLoaderDefaultRunFunc(t *testing.T) {
	loader := NewProviderLoader()
	assert.NotNil(t, loader.RunFunc)
}
