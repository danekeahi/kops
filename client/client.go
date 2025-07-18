package client

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// GetKubeClientForAKSCluster fetches the admin kubeconfig and returns a Kubernetes clientset
func GetKubeClientForAKSCluster(ctx context.Context, subscriptionID, resourceGroup, clusterName string) (*kubernetes.Clientset, error) {
	// Authenticate using Azure identity chain
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain Azure credential: %w", err)
	}

	// Create AKS client
	client, err := armcontainerservice.NewManagedClustersClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create AKS client: %w", err)
	}

	// Fetch kubeconfig
	res, err := client.ListClusterAdminCredentials(ctx, resourceGroup, clusterName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin kubeconfig: %w", err)
	}
	if len(res.Kubeconfigs) == 0 {
		return nil, fmt.Errorf("no kubeconfigs returned for cluster %s", clusterName)
	}

	kubeconfig := res.Kubeconfigs[0].Value

	// Convert to rest.Config
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	restCfg.Timeout = 30 * time.Second

	// Create Kubernetes clientset
	kubeClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	return kubeClient, nil
}
