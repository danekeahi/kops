package azure

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"kops/config"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"k8s.io/klog/v2"
)

// OperationStatus represents the status of an AKS operation
type OperationStatus struct {
	InProgress  bool
	Type        string
	Status      string
	OperationID string
	StartTime   *time.Time
	EndTime     *time.Time
	Error       string
}

// Client wraps the Azure Container Service client
type Client struct {
	azureClient       *armcontainerservice.ManagedClustersClient // ← Renamed from aksClient
	subscriptionID    string
	resourceGroupName string
	clusterName       string
}

// for easy test, use DefaultAzureCredential
//func NewClient(azureConfig config.AzureConfig) (*Client, error) {
//klog.InfoS("Creating Azure client")

//cred, err := azidentity.NewDefaultAzureCredential(nil)
//if err != nil {
//	return nil, fmt.Errorf("failed to obtain Azure credential: %w", err)
//	}

func NewClient(azureConfig config.AzureConfig) (*Client, error) {
	klog.InfoS("Creating Azure client using Managed Identity")

	clientID := os.Getenv("AZURE_CLIENT_ID")
	opts := &azidentity.ManagedIdentityCredentialOptions{}

	if clientID != "" {
		klog.InfoS("Using User Assigned Managed Identity", "clientID", clientID)
		opts.ID = azidentity.ClientID(clientID)
	} else {
		klog.InfoS("Using System Assigned Managed Identity")
	}

	cred, err := azidentity.NewManagedIdentityCredential(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create Managed Identity credential: %w", err)
	}

	azureClient, err := armcontainerservice.NewManagedClustersClient(azureConfig.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure client: %w", err)
	}

	client := &Client{
		azureClient:       azureClient,
		subscriptionID:    azureConfig.SubscriptionID,
		resourceGroupName: azureConfig.ResourceGroupName,
		clusterName:       azureConfig.ClusterName,
	}

	klog.InfoS("Azure client created successfully",
		"cluster", azureConfig.ClusterName,
		"resourceGroup", azureConfig.ResourceGroupName)

	return client, nil
}

// TestConnection validates Azure connectivity and permissions
func (c *Client) TestConnection(ctx context.Context) error {
	_, err := c.azureClient.Get(ctx, c.resourceGroupName, c.clusterName, nil) // ← Updated reference
	if err != nil {
		return fmt.Errorf("failed to connect to cluster - check Managed Identity permissions: %w", err)
	}
	return nil
}

// GetClusterOperationStatus checks if there's an ongoing operation on the cluster
func (c *Client) GetClusterOperationStatus(ctx context.Context) (OperationStatus, error) {
	// Get cluster information using Azure client
	cluster, err := c.azureClient.Get(ctx, c.resourceGroupName, c.clusterName, nil) // ← Updated reference
	if err != nil {
		return OperationStatus{}, fmt.Errorf("failed to get cluster: %w", err)
	}

	status := OperationStatus{
		InProgress:  false,
		Type:        "ClusterOperation",
		Status:      "Unknown",
		OperationID: fmt.Sprintf("%s-unknown", c.clusterName),
		StartTime:   nil,
		EndTime:     nil,
		Error:       "",
	}

	// Check provisioning state
	if cluster.Properties != nil && cluster.Properties.ProvisioningState != nil {
		provisioningState := string(*cluster.Properties.ProvisioningState)
		status.Status = provisioningState
		status.OperationID = fmt.Sprintf("%s-%s-%d", c.clusterName, provisioningState, time.Now().Unix())

		// Determine operation type based on cluster state
		status.Type = c.determineOperationType(cluster)

		// Determine if operation is in progress
		status.InProgress = c.isOperationInProgress(provisioningState)

		klog.V(2).InfoS("Cluster operation status",
			"cluster", c.clusterName,
			"status", status.Status,
			"type", status.Type,
			"inProgress", status.InProgress)
	}

	return status, nil
}

// GetAdminKubeconfig retrieves the admin kubeconfig for the target cluster
func (c *Client) GetAdminKubeconfig(ctx context.Context, clusterName, resourceGroup string) (string, error) {
	klog.V(2).InfoS("Getting admin kubeconfig", "cluster", clusterName)

	// Get admin credentials using Azure client
	result, err := c.azureClient.ListClusterAdminCredentials(ctx, resourceGroup, clusterName, nil) // ← Updated reference
	if err != nil {
		return "", fmt.Errorf("failed to get admin credentials - check Managed Identity has 'Azure Kubernetes Service Cluster Admin' role: %w", err)
	}

	if len(result.Kubeconfigs) == 0 {
		return "", fmt.Errorf("no admin kubeconfig found for cluster %s", clusterName)
	}

	kubeconfig := string(result.Kubeconfigs[0].Value)
	klog.V(2).InfoS("Admin kubeconfig retrieved successfully")

	return kubeconfig, nil
}

// AbortClusterOperation attempts to abort the ongoing cluster operation
func (c *Client) AbortClusterOperation(ctx context.Context, reason string) error {
	klog.InfoS("Attempting to abort cluster operation", "cluster", c.clusterName, "reason", reason)

	// Use the Azure client's BeginAbortLatestOperation method
	poller, err := c.azureClient.BeginAbortLatestOperation(ctx, c.resourceGroupName, c.clusterName, nil) // ← Updated reference
	if err != nil {
		// Check for specific Azure error responses
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "Conflict") {
			return fmt.Errorf("operation completed before abort could take effect: %w", err)
		}
		return fmt.Errorf("failed to initiate abort operation: %w", err)
	}

	// Wait for the abort operation to complete
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("abort operation failed: %w", err)
	}

	klog.InfoS("Cluster operation aborted successfully", "cluster", c.clusterName)
	return nil
}

// GetClusterInfo returns basic information about the cluster
func (c *Client) GetClusterInfo(ctx context.Context) (map[string]interface{}, error) {
	cluster, err := c.azureClient.Get(ctx, c.resourceGroupName, c.clusterName, nil) // ← Updated reference
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	info := map[string]interface{}{
		"name":     c.clusterName,
		"location": *cluster.Location,
	}

	if cluster.Properties != nil {
		if cluster.Properties.ProvisioningState != nil {
			info["provisioningState"] = *cluster.Properties.ProvisioningState
		}
		if cluster.Properties.KubernetesVersion != nil {
			info["kubernetesVersion"] = *cluster.Properties.KubernetesVersion
		}

		if len(cluster.Properties.AgentPoolProfiles) > 0 {
			if cluster.Properties.AgentPoolProfiles[0].Count != nil {
				info["nodeCount"] = *cluster.Properties.AgentPoolProfiles[0].Count
			}
		}
	}

	return info, nil
}

// Helper methods remain the same...
func (c *Client) determineOperationType(cluster armcontainerservice.ManagedClustersClientGetResponse) string {
	if cluster.Properties == nil {
		return "ClusterOperation"
	}

	if cluster.Properties.KubernetesVersion != nil {
		return "ClusterUpgrade"
	}

	if cluster.Properties.AgentPoolProfiles != nil {
		for _, pool := range cluster.Properties.AgentPoolProfiles {
			if pool.ProvisioningState != nil && c.isOperationInProgress(string(*pool.ProvisioningState)) {
				return "NodePoolScale"
			}
		}
	}

	if cluster.Properties.AddonProfiles != nil {
		return "AddonUpdate"
	}

	return "ClusterUpdate"
}

func (c *Client) isOperationInProgress(provisioningState string) bool {
	inProgressStates := []string{
		"Upgrading", "Updating", "Scaling", "Creating", "Deleting", "Running", "InProgress",
	}

	for _, state := range inProgressStates {
		if strings.EqualFold(provisioningState, state) {
			return true
		}
	}

	return false
}

// AzureClientInterface defines the interface for Azure operations
type AzureClientInterface interface {
	GetClusterOperationStatus(ctx context.Context) (OperationStatus, error)
	GetAdminKubeconfig(ctx context.Context, clusterName, resourceGroup string) (string, error)
	TestConnection(ctx context.Context) error
	AbortClusterOperation(ctx context.Context, reason string) error
}
