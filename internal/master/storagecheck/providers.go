package storagecheck

import (
	"encoding/json"
	"os/exec"
	"sync"
)

type S3Provider struct {
	Value string `json:"value"`
	Help  string `json:"help"`
}

func ParseS3Providers(data []byte) ([]S3Provider, error) {
	var result struct {
		Providers []struct {
			Name    string `json:"Name"`
			Options []struct {
				Name     string `json:"Name"`
				Examples []struct {
					Value string `json:"Value"`
					Help  string `json:"Help"`
				} `json:"Examples"`
			} `json:"Options"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	for _, backend := range result.Providers {
		if backend.Name != "s3" {
			continue
		}
		for _, opt := range backend.Options {
			if opt.Name != "provider" {
				continue
			}
			providers := make([]S3Provider, len(opt.Examples))
			for i, ex := range opt.Examples {
				providers[i] = S3Provider{Value: ex.Value, Help: ex.Help}
			}
			return providers, nil
		}
	}
	return nil, nil
}

type ProviderLoader struct {
	RunFunc func() ([]byte, error)

	once      sync.Once
	cached    []S3Provider
	cachedErr error
}

func NewProviderLoader() *ProviderLoader {
	return &ProviderLoader{
		RunFunc: defaultRcloneProviders,
	}
}

func (l *ProviderLoader) Load() ([]S3Provider, error) {
	l.once.Do(func() {
		data, err := l.RunFunc()
		if err != nil {
			l.cached = fallbackS3Providers
			return
		}
		l.cached, l.cachedErr = ParseS3Providers(data)
		if l.cached == nil && l.cachedErr == nil {
			l.cached = fallbackS3Providers
		}
	})
	return l.cached, l.cachedErr
}

func defaultRcloneProviders() ([]byte, error) {
	return exec.Command("rclone", "config", "providers").Output()
}

// fallbackS3Providers is used when rclone is not available.
// Based on rclone v1.69 (2025-05).
var fallbackS3Providers = []S3Provider{
	{Value: "AWS", Help: "Amazon Web Services (AWS) S3"},
	{Value: "Alibaba", Help: "Alibaba Cloud Object Storage System (OSS)"},
	{Value: "ArvanCloud", Help: "Arvan Cloud Object Storage (AOS)"},
	{Value: "Ceph", Help: "Ceph Object Storage"},
	{Value: "ChinaMobile", Help: "China Mobile Ecloud Elastic Object Storage (EOS)"},
	{Value: "Cloudflare", Help: "Cloudflare R2 Storage"},
	{Value: "DigitalOcean", Help: "DigitalOcean Spaces"},
	{Value: "Dreamhost", Help: "Dreamhost DreamObjects"},
	{Value: "GCS", Help: "Google Cloud Storage"},
	{Value: "HuaweiOBS", Help: "Huawei Object Storage Service"},
	{Value: "IBMCOS", Help: "IBM COS S3"},
	{Value: "IDrive", Help: "IDrive e2"},
	{Value: "IONOS", Help: "IONOS Cloud"},
	{Value: "Leviia", Help: "Leviia Object Storage"},
	{Value: "Liara", Help: "Liara Object Storage"},
	{Value: "Linode", Help: "Linode Object Storage"},
	{Value: "LyveCloud", Help: "Seagate Lyve Cloud"},
	{Value: "Magalu", Help: "Magalu Object Storage"},
	{Value: "Minio", Help: "Minio Object Storage"},
	{Value: "Netease", Help: "Netease Object Storage (NOS)"},
	{Value: "OVHcloud", Help: "OVHcloud Object Storage"},
	{Value: "Petabox", Help: "Petabox Object Storage"},
	{Value: "Qiniu", Help: "Qiniu Object Storage (Kodo)"},
	{Value: "RackCorp", Help: "RackCorp Object Storage"},
	{Value: "Scaleway", Help: "Scaleway Object Storage"},
	{Value: "SeaweedFS", Help: "SeaweedFS S3"},
	{Value: "Selectel", Help: "Selectel Object Storage"},
	{Value: "Storj", Help: "Storj (S3 Compatible Gateway)"},
	{Value: "Synology", Help: "Synology C2 Object Storage"},
	{Value: "TencentCOS", Help: "Tencent Cloud Object Storage (COS)"},
	{Value: "Wasabi", Help: "Wasabi Object Storage"},
	{Value: "Other", Help: "Any other S3 compatible provider"},
}
