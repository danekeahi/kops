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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// OperationStatus represents the status of an AKS operation
type OperationStatus struct {
	InProgress    bool
	OperationType string
	Status        string
}

// Client wraps the Azure Container Service client
// This struct encapsulates all the necessary information to interact with a specific AKS cluster
type Client struct {
	aksClient         *armcontainerservice.ManagedClustersClient // Azure SDK client for AKS operations
	subscriptionID    string                                     // Azure subscription ID where the cluster resides
	resourceGroupName string                                     // Resource group containing the AKS cluster
	clusterName       string                                     // Name of the AKS cluster
}

// GetDefaultAzureCredential creates and returns a managed identity credential
// This function is used when running in Azure with a managed identity (e.g., in a pod with workload identity)
// Returns:
//   - ManagedIdentityCredential: Used to authenticate with Azure services
//   - error: If credential creation fails
func GetDefaultAzureCredential() (*azidentity.ManagedIdentityCredential, error) {
	cred, err := azidentity.NewManagedIdentityCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create default Azure credential: %w", err)
	}
	return cred, nil
}

// GetAKSClient creates a new Azure AKS client with the provided credentials and cluster information
// This is the main factory function for creating a Client instance
// Parameters:
//   - subscriptionID: Azure subscription ID
//   - resourceGroupName: Name of the resource group containing the cluster
//   - clusterName: Name of the AKS cluster
//   - cred: Azure credentials (typically managed identity)
//
// Returns:
//   - Client: Configured client for AKS operations
//   - error: If client creation fails
func GetAKSClient(subscriptionID string, resourceGroupName string, clusterName string, cred *azidentity.ManagedIdentityCredential) (*Client, error) {
	// Create the Azure SDK client for AKS
	aksClient, err := armcontainerservice.NewManagedClustersClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create AKS client: %w", err)
	}

	// Wrap the SDK client in our Client struct with cluster information
	return &Client{
		aksClient:         aksClient,
		subscriptionID:    subscriptionID,
		resourceGroupName: resourceGroupName,
		clusterName:       clusterName,
	}, nil
}

// GetKubeRestConfig retrieves the Kubernetes configuration for connecting to an AKS cluster (used if need to connect to the target cluster)
// This function fetches admin credentials from Azure and converts them to a Kubernetes REST config
// Parameters:
//   - ctx: Context for the operation
//   - aksClient: The Azure client
//   - resourceGroup: Resource group name (redundant with aksClient.resourceGroupName)
//   - clusterName: Cluster name (redundant with aksClient.clusterName)
//
// Returns:
//   - rest.Config: Kubernetes REST client configuration
//   - error: If credentials cannot be retrieved or parsed
func GetKubeRestConfig(ctx context.Context, aksClient *Client, resourceGroup, clusterName string) (*rest.Config, error) {
	client := aksClient.aksClient
	// Fetch admin credentials from Azure - requires appropriate RBAC permissions
	resp, err := client.ListClusterAdminCredentials(ctx, resourceGroup, clusterName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster admin credentials: %w", err)
	}

	// Ensure we received at least one kubeconfig
	if len(resp.Kubeconfigs) == 0 {
		return nil, fmt.Errorf("no kubeconfigs returned for cluster %s", clusterName)
	}

	// Extract the kubeconfig data (first one if multiple exist)
	kubeconfig := resp.Kubeconfigs[0].Value

	// Parse the kubeconfig into a REST client configuration
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	// Set a reasonable timeout for API calls
	restCfg.Timeout = 30 * time.Second

	return restCfg, nil
}

// GetTypedClient creates a typed Kubernetes client from a REST configuration
// This client is used for standard Kubernetes resources (pods, services, etc.)
// Parameters:
//   - restCfg: Kubernetes REST configuration
//
// Returns:
//   - kubernetes.Clientset: Typed client for Kubernetes API
//   - error: If client creation fails
func GetTypedClient(restCfg *rest.Config) (*kubernetes.Clientset, error) {
	return kubernetes.NewForConfig(restCfg)
}

// GetDynamicClient creates a dynamic Kubernetes client from a REST configuration
// This client is used for custom resources and resources not known at compile time
// Parameters:
//   - restCfg: Kubernetes REST configuration
//
// Returns:
//   - dynamic.Interface: Dynamic client for Kubernetes API
//   - error: If client creation fails
func GetDynamicClient(restCfg *rest.Config) (dynamic.Interface, error) {
	return dynamic.NewForConfig(restCfg)
}

// GetClusterOperationStatus checks if there's an ongoing operation on the cluster
// This is useful for determining if it's safe to perform operations or if we should wait/abort
// Returns:
//   - OperationStatus: Current status including whether an operation is in progress
//   - error: If status cannot be retrieved
func (c *Client) GetClusterOperationStatus(ctx context.Context) (*OperationStatus, error) {
	// Fetch current cluster state from Azure
	cluster, err := c.aksClient.Get(ctx, c.resourceGroupName, c.clusterName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	// Initialize status struct
	status := &OperationStatus{
		InProgress:    false,
		OperationType: "",
		Status:        "",
	}

	// Check the provisioning state to determine operation status
	if cluster.Properties != nil && cluster.Properties.ProvisioningState != nil {
		provisioningState := *cluster.Properties.ProvisioningState
		status.Status = provisioningState

		// Determine if an operation is currently in progress based on state
		switch provisioningState {
		case "Upgrading", "Updating", "Scaling", "Creating", "Deleting":
			// These states indicate active operations
			status.InProgress = true
			status.OperationType = provisioningState
		case "Succeeded", "Failed", "Canceled":
			// These are terminal states - no operation in progress
			status.InProgress = false
		default:
			// Unknown state, assume not in progress for safety
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
// Parameters:
//   - ctx: Context for the operation
//   - operationType: Type of operation being aborted (for logging/tracking purposes)
//
// Returns:
//   - error: nil if abort successful, error otherwise
func (c *Client) AbortClusterOperation(ctx context.Context, operationType string) error {
	// Initiate the abort operation through Azure API
	// This returns a poller for long-running operations
	poller, err := c.aksClient.BeginAbortLatestOperation(ctx, c.resourceGroupName, c.clusterName, nil)
	if err != nil {
		// Handle specific error cases
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "Conflict") {
			// 409 Conflict means the operation already completed
			return fmt.Errorf("operation completed before abort could take effect (operation was too fast): %w", err)
		}
		return fmt.Errorf("failed to initiate abort operation: %w", err)
	}

	// Wait for the abort operation to complete
	// This blocks until the abort is finished or fails
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("abort operation failed: %w", err)
	}

	return nil
}

// GetClusterInfo returns basic information about the cluster
// This is a utility function for retrieving cluster metadata
// Returns:
//   - map[string]interface{}: Cluster information including name, location, state, version, etc.
//   - error: If cluster information cannot be retrieved
func (c *Client) GetClusterInfo(ctx context.Context) (map[string]interface{}, error) {
	// Fetch cluster details from Azure
	cluster, err := c.aksClient.Get(ctx, c.resourceGroupName, c.clusterName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	// Build basic info map
	info := map[string]interface{}{
		"name":     c.clusterName,
		"location": *cluster.Location,
	}

	// Add additional properties if available
	if cluster.Properties != nil {
		info["provisioningState"] = cluster.Properties.ProvisioningState
		info["kubernetesVersion"] = cluster.Properties.KubernetesVersion

		// Include node count from the first agent pool (if exists)
		if cluster.Properties.AgentPoolProfiles != nil && len(cluster.Properties.AgentPoolProfiles) > 0 {
			info["nodeCount"] = cluster.Properties.AgentPoolProfiles[0].Count
		}
	}

	return info, nil
}
