package main

import (
	"context"
	"log"
	"os"

	"github.com/danekeahi/kops/config"
	"github.com/danekeahi/kops/controllers"
	"github.com/danekeahi/kops/internal/azure"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	//you can add your own CRD schema here
	// e.g. safeopsv1.AddToScheme(scheme)
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	ctx := context.Background()

	// Load config (replace with real config or inject via environment variables)
	azureConfig := config.AzureConfig{
		TenantID: os.Getenv("AZURE_TENANT_ID"),
		ClientID: os.Getenv("AZURE_CLIENT_ID"),
		//ClientSecret:      os.Getenv("AZURE_CLIENT_SECRET"),
		SubscriptionID:    os.Getenv("AZURE_SUBSCRIPTION_ID"),
		ResourceGroupName: os.Getenv("AZURE_RESOURCE_GROUP"),
		ClusterName:       os.Getenv("AZURE_CLUSTER_NAME"),
	}

	azureClient, err := azure.NewClient(azureConfig)
	if err != nil {
		log.Fatalf("Failed to create Azure client: %v", err)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		log.Fatalf("Unable to start manager: %v", err)
	}

	// Create the controller with all required fields
	operationController := &controllers.OperationReconciler{
		Client:        mgr.GetClient(),
		Azure:         azureClient,
		ResourceGroup: azureConfig.ResourceGroupName,
		ClusterName:   azureConfig.ClusterName,
	}

	if err := operationController.SetupWithManager(mgr); err != nil {
		log.Fatalf("Unable to create controller: %v", err)
	}

	// Start the polling goroutine to continuously observe for new ongoing operations
	go operationController.StartPolling(ctx)

	log.Println("Starting manager with Azure operation monitoring...")
	log.Printf("Monitoring cluster: %s in resource group: %s", azureConfig.ClusterName, azureConfig.ResourceGroupName)
	log.Println("Controller will:")
	log.Println("  - Create CR when Azure operation is ongoing")
	log.Println("  - Delete CR when Azure operation ends")
	log.Println("  - Resync every 30 seconds to check for new operations")

	if err := mgr.Start(ctx); err != nil {
		log.Fatalf("Problem running manager: %v", err)
	}
}
