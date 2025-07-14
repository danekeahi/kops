package main

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/tools/cache"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	informers "k8s.io/client-go/informers"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"

	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"
	"kops/internal/azure"
	"kops/pkg/config"
)

var (
	stopChan          chan struct{} // Used to signal the monitoring goroutine to stop
	monitoringRunning bool          // Prevents multiple goroutines from being started
    metricsUpdated chan struct{}   // Used to signal when metrics are updated
)

func main() {
	// Load in-cluster config (use clientcmd for local dev)
	kubeConfigPath := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		panic(err)
	}

    // Initialize channel for metrics updates
    metricsUpdated = make(chan struct{}, 1) // Buffered so it won't block

	// Create a dynamic client for operations
	dynClient, err := dynamic.NewForConfig(kubeConfig)
	if err != nil {
		panic(err)
	}
	// Create a typed client for config maps
	typedClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		panic(err)
	}

    // Load Azure config from hardcoded values (CHANGE LATER)
    azureCfg := config.AzureConfig{
        SubscriptionID:    "8ecadfc9-d1a3-4ea4-b844-0d9f87e4d7c8",
        ResourceGroupName: "aks-health-rg",
        ClusterName:       "aks-health-cluster",
    }

    azureClient, err := azure.NewClient(azureCfg)
    if err != nil {
        fmt.Printf("Failed to create Azure client: %v\n", err)
        return
    }

	// Create an informer factory for your CRD
	dynFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynClient, time.Minute*10, "default", nil)
	
	// Create a typed informer factory for config maps
	typedFactory := informers.NewSharedInformerFactoryWithOptions(typedClient, time.Minute*10, informers.WithNamespace("default"))

	// Get the informer for your Operation CR
	gvr := schema.GroupVersionResource{
		Group:    "yourgroup.yourdomain.com",
		Version:  "v1",
		Resource: "operations",
	}
	informer := dynFactory.ForResource(gvr).Informer()
	// Register event handlers
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			u := obj.(*unstructured.Unstructured)
			name := u.GetName()
			fmt.Printf("Operation CR created: %s\n", name)

			// Prevent multiple monitoring goroutines
			if monitoringRunning {
				fmt.Println("Monitoring already running. Skipping.")
				return
			}

			// Start monitoring in a goroutine
			stopChan = make(chan struct{})
			monitoringRunning = true
			go func() {
				monitorOperation(name, stopChan, azureClient, typedClient)
			}()
		},

		DeleteFunc: func(obj interface{}) {
			u := obj.(*unstructured.Unstructured)
			name := u.GetName()
			fmt.Printf("Operation CR deleted: %s\n", name)

			// Signal the monitoring goroutine to stop
			if monitoringRunning && stopChan != nil {
				close(stopChan)
				monitoringRunning = false
				stopChan = nil
			}
		},
	})

	// Watch ConfigMap updates
	// Create an informer for ConfigMaps using the dynamic client
	cmInformer := typedFactory.Core().V1().ConfigMaps().Informer()

	// Add event handler to react to ConfigMap changes
	cmInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			cm := newObj.(*corev1.ConfigMap)

			// Check if this is the specific ConfigMap we're interested in
			if cm.Name == "metrics-config" { // change to metrics-store later to match tanamutu's
                // Signal that metrics were updated
                select {
                case metricsUpdated <- struct{}{}:
                default:
                    // Do not block if a signal is already pending
                }
			}
		},
	})

	// Start the informer
	stop := make(chan struct{})
	defer close(stop)

	dynFactory.Start(stop)
	typedFactory.Start(stop)

	dynFactory.WaitForCacheSync(stop)
	typedFactory.WaitForCacheSync(stop)

	select {}
}

// This function runs in a goroutine and checks metrics periodically
func monitorOperation(opName string, stopChan <-chan struct{}, azureClient *azure.Client, typedClient kubernetes.Interface) {
	fmt.Printf("Started monitoring operation: %s\n", opName)

    // Initial check
    if checkAndAbortIfUnhealthy(opName, azureClient, typedClient) {
        return
    }

	for {
		select {
		case <-stopChan:
			fmt.Println("Received stop signal. Stopping monitoring.")
			return
        case <-metricsUpdated:
            // This case will be triggered when metrics are updated
			if checkAndAbortIfUnhealthy(opName, azureClient, typedClient) {
                return
            }
		}
	}
}

func checkAndAbortIfUnhealthy(opName string, azureClient *azure.Client, typedClient kubernetes.Interface) bool {
	// Check the health of the operation and abort if unhealthy
	cm, err := typedClient.CoreV1().ConfigMaps("default").Get(context.TODO(), "metrics-config", v1.GetOptions{})
	if err != nil {
		fmt.Printf("Failed to fetch metrics-config: %v\n", err)
		return false
	}

    // Simulate metric evaluation
	if cm.Data["cpu"] == "high" {
		fmt.Printf("Unhealthy metrics detected for operation %s! Aborting.\n", opName)
		// Call Azure client to abort
        ctx := context.Background()
        err := azureClient.AbortClusterOperation(ctx, "cpu-unhealthy")
        if err != nil {
            fmt.Printf("Failed to abort operation: %v\n", err)
        } else {
            fmt.Println("Abort request sent successfully.")
        }
        return true
	}

    fmt.Printf("Metrics healthy.")
    return false
}
