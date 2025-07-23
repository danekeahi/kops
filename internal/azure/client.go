// TAKEN FROM WENXUAN'S REPO!! MAY NEED TO CHANGE!!
package azure

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/rest"
)

// OperationStatus represents the status of an AKS operation
type OperationStatus struct {
	InProgress    bool
	OperationType string
	Status        string
}

// Client wraps the Azure Container Service client
type Client struct {
	aksClient         *armcontainerservice.ManagedClustersClient
	subscriptionID    string
	resourceGroupName string
	clusterName       string
}

func GetDefaultAzureCredential() (*azidentity.ManagedIdentityCredential, error) {
	cred, err := azidentity.NewManagedIdentityCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create default Azure credential: %w", err)
	}
	return cred, nil
}

func GetAKSClient(subscriptionID string, resourceGroupName string, clusterName string, cred *azidentity.ManagedIdentityCredential) (*Client, error) {
	aksClient, err := armcontainerservice.NewManagedClustersClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create AKS client: %w", err)
	}
	
	// Return a Client struct with the AKS client
	// Note: resourceGroupName and clusterName will need to be set later
	// or this function should be modified to accept them as parameters
	return &Client{
		aksClient:         aksClient,
		subscriptionID:    subscriptionID,
		resourceGroupName: resourceGroupName,
		clusterName:       clusterName,
	}, nil
}

func GetKubeRestConfig(ctx context.Context, aksClient *Client, resourceGroup, clusterName string) (*rest.Config, error) {
	client := aksClient.aksClient
	resp, err := client.ListClusterAdminCredentials(ctx, resourceGroup, clusterName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster admin credentials: %w", err)
	}
	if len(resp.Kubeconfigs) == 0 {
		return nil, fmt.Errorf("no kubeconfigs returned for cluster %s", clusterName)
	}

	kubeconfig := resp.Kubeconfigs[0].Value
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}
	restCfg.Timeout = 30 * time.Second

	return restCfg, nil
}

func GetTypedClient(restCfg *rest.Config) (*kubernetes.Clientset, error) {
	return kubernetes.NewForConfig(restCfg)
}

func GetDynamicClient(restCfg *rest.Config) (dynamic.Interface, error) {
	return dynamic.NewForConfig(restCfg)
}


// GetClusterOperationStatus checks if there's an ongoing operation on the cluster
func (c *Client) GetClusterOperationStatus(ctx context.Context) (*OperationStatus, error) {
	// Get cluster information
	cluster, err := c.aksClient.Get(ctx, c.resourceGroupName, c.clusterName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	status := &OperationStatus{
		InProgress:    false,
		OperationType: "",
		Status:        "",
	}

	// Check provisioning state
	if cluster.Properties != nil && cluster.Properties.ProvisioningState != nil {
		provisioningState := *cluster.Properties.ProvisioningState
		status.Status = provisioningState

		// Determine if operation is in progress
		switch provisioningState {
		case "Upgrading", "Updating", "Scaling", "Creating", "Deleting":
			status.InProgress = true
			status.OperationType = provisioningState
		case "Succeeded", "Failed", "Canceled":
			status.InProgress = false
		default:
			// Unknown state, assume not in progress
			status.InProgress = false
		}
	}

	return status, nil
}

// AbortClusterOperation attempts to abort the ongoing cluster operation using Azure AKS API
// This method uses the Azure SDK's BeginAbortLatestOperation which:
// - Aborts the currently running operation on the managed cluster
// - Moves the cluster to a Canceling state and eventually to a Canceled state when cancellation finishes
// - Returns a 409 error code if the operation completes before cancellation can take place
// - May not be able to abort all types of operations (some may complete too quickly)
func (c *Client) AbortClusterOperation(ctx context.Context, operationType string) error {
	// Use the Azure SDK's BeginAbortLatestOperation method
	// This method aborts the currently running operation on the managed cluster
	poller, err := c.aksClient.BeginAbortLatestOperation(ctx, c.resourceGroupName, c.clusterName, nil)
	if err != nil {
		// Check for specific Azure error responses
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "Conflict") {
			return fmt.Errorf("operation completed before abort could take effect (operation was too fast): %w", err)
		}
		return fmt.Errorf("failed to initiate abort operation: %w", err)
	}

	// Wait for the abort operation to complete
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("abort operation failed: %w", err)
	}

	return nil
}

// GetClusterInfo returns basic information about the cluster
func (c *Client) GetClusterInfo(ctx context.Context) (map[string]interface{}, error) {
	cluster, err := c.aksClient.Get(ctx, c.resourceGroupName, c.clusterName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	info := map[string]interface{}{
		"name":     c.clusterName,
		"location": *cluster.Location,
	}

	if cluster.Properties != nil {
		info["provisioningState"] = cluster.Properties.ProvisioningState
		info["kubernetesVersion"] = cluster.Properties.KubernetesVersion

		if cluster.Properties.AgentPoolProfiles != nil && len(cluster.Properties.AgentPoolProfiles) > 0 {
			info["nodeCount"] = cluster.Properties.AgentPoolProfiles[0].Count
		}
	}

	return info, nil
}