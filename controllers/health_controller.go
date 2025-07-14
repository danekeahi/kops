package controllers

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	dynamicinformer "k8s.io/client-go/dynamic/dynamicinformer"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kops/internal/azure"
	"kops/pkg/kopsconfig"
)

var (
	stopChan          chan struct{} // Used to signal the monitoring goroutine to stop
	monitoringRunning bool          // Prevents multiple goroutines from being started
    metricsUpdated chan struct{}   // Used to signal when metrics are updated
)

func StartHealthMonitoring(kubeConfigPath string, azureCfg kopsconfig.AzureConfig) error {
	// Load in-cluster config (use clientcmd for local dev)
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		return fmt.Errorf("error loading kubeconfig: %w", err)
	}

	// Create a dynamic client for operations
	dynClient, err := dynamic.NewForConfig(kubeConfig)
	if err != nil {
		return fmt.Errorf("error creating dynamic client: %w", err)
	}
	// Create a typed client for config maps
	typedClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return fmt.Errorf("error creating typed client: %w", err)
	}

    azureClient, err := azure.NewClient(azureCfg)
    if err != nil {
		return fmt.Errorf("Failed to create Azure client: %v\n", err)
    }
	
	// Initialize channel for metrics updates
	metricsUpdated = make(chan struct{}, 1) // Buffered so it won't block
	stopChan = make(chan struct{})

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
	opInformer := dynFactory.ForResource(gvr).Informer()
	// Register event handlers
	opInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
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
			monitoringRunning = true
			go monitorOperation(name, stopChan, azureClient, typedClient)
		},

		DeleteFunc: func(obj interface{}) {
			fmt.Printf("Operation CR deleted.\n")

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
			if cm.Name == "metrics-store" { // change to metrics-store later to match tanamutu's
                // Signal that metrics were updated
                select {
                case metricsUpdated <- struct{}{}:
                default:
                    // Do not block if a signal is already pending
                }
			}
		},
	})

	// Start the informers
	stop := make(chan struct{})
	go dynFactory.Start(stop)
	go typedFactory.Start(stop)

	if !cache.WaitForCacheSync(stop, opInformer.HasSynced, cmInformer.HasSynced) {
		return fmt.Errorf("failed to sync caches")
	}

	return nil
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
	cm, err := typedClient.CoreV1().ConfigMaps("default").Get(context.TODO(), "metrics-store", metav1.GetOptions{})
	if err != nil {
		fmt.Printf("Failed to fetch metrics-store: %v\n", err)
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
