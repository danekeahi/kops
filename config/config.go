package config

import (
	"fmt"
	"os"
)

type AzureConfig struct {
	SubscriptionID    string
	ResourceGroupName string
	ClusterName       string
}

func LoadAzureConfig() (AzureConfig, error) {
	cfg := AzureConfig{
		SubscriptionID:    os.Getenv("AZURE_SUBSCRIPTION_ID"),
		ResourceGroupName: os.Getenv("AZURE_RESOURCE_GROUP"),
		ClusterName:       os.Getenv("AZURE_CLUSTER_NAME"),
	}

	if cfg.SubscriptionID == "" {
		return cfg, fmt.Errorf("AZURE_SUBSCRIPTION_ID is required")
	}
	if cfg.ResourceGroupName == "" {
		return cfg, fmt.Errorf("AZURE_RESOURCE_GROUP is required")
	}
	if cfg.ClusterName == "" {
		return cfg, fmt.Errorf("AZURE_CLUSTER_NAME is required")
	}

	return cfg, nil
}
