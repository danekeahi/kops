package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	// Added missing imports

	corev1 "k8s.io/api/core/v1"

	"kops/config"
	"kops/controllers"
	"kops/internal/azure"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
}

func main() {
	// Initialize klog flags FIRST
	klog.InitFlags(nil)

	// Define custom flags (avoid conflicts with klog)
	var (
		cleanup = flag.Bool("cleanup", false, "Clean up orphaned CRs on startup")
	)

	// Parse all flags
	flag.Parse()

	klog.InfoS("Starting Azure AKS Operation Controller", "version", "1.0.0")

	// Setup graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Load Azure configuration
	azureConfig, err := loadAzureConfig()
	if err != nil {
		klog.ErrorS(err, "Failed to load Azure configuration")
		os.Exit(1)
	}

	klog.InfoS("Azure configuration loaded",
		"subscription", azureConfig.SubscriptionID,
		"resourceGroup", azureConfig.ResourceGroupName,
		"cluster", azureConfig.ClusterName)

	// Create Azure client with Managed Identity
	klog.InfoS("Creating Azure client with Managed Identity...")
	azureClient, err := azure.NewClient(azureConfig)
	if err != nil {
		klog.ErrorS(err, "Failed to create Azure client - check Managed Identity configuration")
		os.Exit(1)
	}
	klog.InfoS("Azure client created successfully")

	// Validate Azure connection and permissions
	klog.InfoS("Validating Azure connection and permissions...")
	if err := validateAzureConnection(ctx, azureClient); err != nil {
		klog.ErrorS(err, "Azure connection validation failed - check Managed Identity roles")
		os.Exit(1)
	}
	klog.InfoS("Azure connection validated successfully")

	// Create target cluster client
	klog.InfoS("Creating target cluster client...")
	targetClient, err := createTargetClient(ctx, azureClient, azureConfig)
	if err != nil {
		klog.ErrorS(err, "Failed to create target cluster client")
		os.Exit(1)
	}
	klog.InfoS("Target cluster client created successfully")

	// Get namespace configuration
	namespace := os.Getenv("OPERATION_CR_NAMESPACE")
	if namespace == "" {
		namespace = "default"
		klog.InfoS("Using default namespace for Operation CRs", "namespace", namespace)
	} else {
		klog.InfoS("Using configured namespace for Operation CRs", "namespace", namespace)
	}

	// Create reconciler
	klog.InfoS("Creating operation reconciler...")
	reconciler, err := controllers.NewOperationReconciler(targetClient, azureClient, controllers.Config{
		Namespace:     namespace,
		ResourceGroup: azureConfig.ResourceGroupName,
		ClusterName:   azureConfig.ClusterName,
	})
	if err != nil {
		klog.ErrorS(err, "Failed to create reconciler")
		os.Exit(1)
	}
	klog.InfoS("Operation reconciler created successfully")

	// Perform cleanup if requested
	if *cleanup {
		klog.InfoS("Performing cleanup of orphaned Operation CRs...")
		if err := reconciler.CleanupOrphanedCRs(ctx); err != nil {
			klog.ErrorS(err, "Cleanup failed, continuing anyway")
		} else {
			klog.InfoS("Cleanup completed successfully")
		}
	}

	// Start Azure operations monitoring
	klog.InfoS("Monitoring target cluster",
		"clusterName", azureConfig.ClusterName,
		"resourceGroup", azureConfig.ResourceGroupName)
	if err := reconciler.Start(ctx); err != nil {
		klog.ErrorS(err, "Failed to start reconciler")
		os.Exit(1)
	}

	klog.InfoS("Azure AKS Operation Controller started successfully",
		"cluster", azureConfig.ClusterName,
		"namespace", namespace,
		"resourceGroup", azureConfig.ResourceGroupName)

	// Wait for shutdown signal
	<-ctx.Done()

	klog.InfoS("Received shutdown signal, starting graceful shutdown...")
	reconciler.Stop()

	// Allow time for graceful cleanup
	shutdownTimeout := 5 * time.Second
	time.Sleep(shutdownTimeout)
	klog.InfoS("Azure AKS Operation Controller shutdown complete")
}

func loadAzureConfig() (config.AzureConfig, error) {
	azureConfig := config.AzureConfig{
		SubscriptionID:    os.Getenv("AZURE_SUBSCRIPTION_ID"),
		ResourceGroupName: os.Getenv("AZURE_RESOURCE_GROUP"),
		ClusterName:       os.Getenv("AZURE_CLUSTER_NAME"),
	}

	// Validate required Azure configuration
	if azureConfig.SubscriptionID == "" {
		return azureConfig, fmt.Errorf("AZURE_SUBSCRIPTION_ID environment variable is required")
	}
	if azureConfig.ResourceGroupName == "" {
		return azureConfig, fmt.Errorf("AZURE_RESOURCE_GROUP environment variable is required")
	}
	if azureConfig.ClusterName == "" {
		return azureConfig, fmt.Errorf("AZURE_CLUSTER_NAME environment variable is required")
	}

	return azureConfig, nil
}

func validateAzureConnection(ctx context.Context, azureClient azure.AzureClientInterface) error {
	// Create a timeout context for Azure API calls
	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Test Azure connection and permissions
	if err := azureClient.TestConnection(testCtx); err != nil {
		return fmt.Errorf("azure connection test failed - verify Managed Identity has required permissions: %w", err)
	}

	// Test getting cluster operation status
	_, err := azureClient.GetClusterOperationStatus(testCtx)
	if err != nil {
		return fmt.Errorf("failed to get cluster operation status - check Azure permissions: %w", err)
	}

	return nil
}

func createTargetClient(ctx context.Context, azureClient azure.AzureClientInterface, azureConfig config.AzureConfig) (client.Client, error) {
	// Get admin kubeconfig from Azure using Managed Identity
	klog.V(2).InfoS("Retrieving admin kubeconfig from Azure",
		"cluster", azureConfig.ClusterName,
		"resourceGroup", azureConfig.ResourceGroupName)

	kubeconfig, err := azureClient.GetAdminKubeconfig(ctx, azureConfig.ClusterName, azureConfig.ResourceGroupName)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin kubeconfig from Azure - check Managed Identity has 'Azure Kubernetes Service Cluster Admin' role: %w", err)
	}

	// Parse kubeconfig into REST configuration
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	// Configure client timeouts for Azure AKS
	restConfig.Timeout = 30 * time.Second
	restConfig.QPS = 50    // Reasonable QPS for Azure AKS
	restConfig.Burst = 100 // Reasonable burst for Azure AKS

	// Create Kubernetes client for target cluster
	targetClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client for target cluster: %w", err)
	}

	// Test target cluster connection using proper Kubernetes types
	klog.V(2).InfoS("Testing target cluster connection...")
	if err := testTargetClusterConnection(ctx, targetClient); err != nil {
		return nil, fmt.Errorf("failed to connect to target cluster: %w", err)
	}

	klog.V(2).InfoS("Target cluster connection verified successfully")
	return targetClient, nil
}

// testTargetClusterConnection verifies we can connect to the target cluster
func testTargetClusterConnection(ctx context.Context, targetClient client.Client) error {
	testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Use proper Kubernetes core API types for connectivity test
	namespaceList := &corev1.NamespaceList{}

	// Try to list namespaces as a simple connectivity test
	if err := targetClient.List(testCtx, namespaceList); err != nil {
		return fmt.Errorf("failed to list namespaces - check kubeconfig validity: %w", err)
	}

	klog.V(3).InfoS("Target cluster connectivity verified",
		"namespaceCount", len(namespaceList.Items))

	return nil
}
