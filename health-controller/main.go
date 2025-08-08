package main

import (
	"fmt"
	"os"
	"k8s.io/client-go/rest"
	"kops/controllers"
	"kops/internal/azure"
)

func main() {

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
	
	// Fetch kubeconfig from Monitoring AKS cluster
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		fmt.Printf("Error getting in-cluster config: %v\n", err)
		return
	}
	
	// Create typed Kubernetes client (used for ConfigMap and accessing user-defined thresholds)
	typedClient, err := azure.GetTypedClient(restCfg)
	if err != nil {
		fmt.Printf("Error creating typed Kubernetes client: %v\n", err)
		return
	}

	// Create dynamic Kubernetes client (used for custom resources)
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
