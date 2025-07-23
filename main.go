package main

import (
	"context"
	"fmt"
	"os"

	"kops/controllers"
	"kops/internal/azure"
)

func main() {
	ctx := context.Background()

	// Load Azure configuration
	SubscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
	ResourceGroupName := os.Getenv("AZURE_RESOURCE_GROUP")
	ClusterName := os.Getenv("AZURE_CLUSTER_NAME")

	// Authenticate using default Azure credentials
	cred, err := azure.GetDefaultAzureCredential()
	if err != nil {
		fmt.Printf("Error initializing Azure credentials: %v\n", err)
		return
	}
	
	// Create AKS client
	aksClient, err := azure.GetAKSClient(SubscriptionID, ResourceGroupName, ClusterName, cred)
	if err != nil {
		fmt.Printf("Error creating AKS client: %v\n", err)
		return
	}
	
	// Fetch kubeconfig from target AKS cluster
	restCfg, err := azure.GetKubeRestConfig(ctx, aksClient, ResourceGroupName, ClusterName)
	if err != nil {
		fmt.Printf("Error retrieving kubeconfig: %v\n", err)
		return
	}
	
	// Create typed Kubernetes client
	typedClient, err := azure.GetTypedClient(restCfg)
	if err != nil {
		fmt.Printf("Error creating typed Kubernetes client: %v\n", err)
		return
	}

	// Create dynamic Kubernetes client
	dynClient, err := azure.GetDynamicClient(restCfg)
	if err != nil {
		fmt.Printf("Error creating dynamic Kubernetes client: %v\n", err)
		return
	}

	// Start health monitoring
	err = controllers.StartHealthMonitoring(aksClient, typedClient, dynClient)
	if err != nil {
		fmt.Printf("Failed to start health monitoring: %v\n", err)
		return
	}

	fmt.Println("Health monitoring started successfully.")

	// Keep the pod running so informers stay active
	select {}
}
