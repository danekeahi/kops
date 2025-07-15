package config

// AzureConfig contains Azure authentication and cluster configuration
type AzureConfig struct {
	TenantID          string
	ClientID          string
	ClientSecret      string
	SubscriptionID    string
	ResourceGroupName string
	ClusterName       string
}
