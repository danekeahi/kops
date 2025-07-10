package main

import (
	// "context"
	"context"
	"fmt"
	"sync"
	"time"

	// metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	// "k8s.io/apimachinery/pkg/runtime"
	// "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"

	// for using local kubeconfig
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"
)

var (
	stopChan          chan struct{} // Used to signal the monitoring goroutine to stop
	monitoringRunning bool          // Prevents multiple goroutines from being started
	// wg                sync.WaitGroup    // Ensures we wait for the monitoring goroutine to finish
	metricsCache map[string]string // Stores latest metrics from ConfigMap
	mu           sync.Mutex        // Protects access to metricsCache
    metricsUpdated chan struct{}   // Used to signal when metrics are updated
)

func main() {
	// Load in-cluster config (use clientcmd for local dev)
	// CHANGED
	// config, err := rest.InClusterConfig()
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err)
	}

    // Initialize channel for metrics updates
    metricsUpdated = make(chan struct{}, 1) // Buffered so it won’t block

	// Create a clientset for your custom resource
	// CHANGED
	// crClient, err := clientset.NewForConfig(config)
	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	// Create an informer factory for your CRD
	// CHANGED
	// factory := informers.NewSharedInformerFactory(crClient, time.Minute*10)
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynClient, time.Minute*10, "default", nil)

	// Get the informer for your Operation CR
	// CHANGED
	// informer := factory.Yourgroup().V1().Operations().Informer()
	gvr := schema.GroupVersionResource{
		Group:    "yourgroup.yourdomain.com",
		Version:  "v1",
		Resource: "operations",
	}
	informer := factory.ForResource(gvr).Informer()
	// Register event handlers
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// CHANGED
			// op := obj.(*myv1.Operation)
			// fmt.Printf("Operation CR created: %s\n", op.Name)
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
			// wg.Add(1)
			go func() {
				// defer wg.Done()
				// monitorOperation(op, stopChan)
				monitorOperation(name, stopChan) // CHANGED
			}()
		},

		DeleteFunc: func(obj interface{}) {
			// CHANGED
			// op := obj.(*myv1.Operation)
			// fmt.Printf("Operation CR deleted: %s\n", op.Name)
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
	// cmInformer := factory.Core().V1().ConfigMaps().Informer()
	// CHANGED
	// Define the GroupVersionResource for ConfigMaps (core API group)
	cmGVR := schema.GroupVersionResource{
		Group:    "",           // Empty string for core API group
		Version:  "v1",         // Kubernetes API version
		Resource: "configmaps", // Plural resource name
	}
	// Create an informer for ConfigMaps using the dynamic client
	cmInformer := factory.ForResource(cmGVR).Informer()

	// Add event handler to react to ConfigMap changes
	cmInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			// cm := newObj.(*corev1.ConfigMap)
			// CHANGED
			// Convert the unstructured object to a typed ConfigMap
			unstructuredCM := newObj.(*unstructured.Unstructured)
			var cm corev1.ConfigMap
			// Use the runtime converter to transform unstructured data to ConfigMap
			err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredCM.Object, &cm)
			if err != nil {
				fmt.Printf("Failed to convert unstructured to ConfigMap: %v\n", err)
				return
			}

			// Check if this is the specific ConfigMap we're interested in
			if cm.Name == "metrics-config" {
				loadMetrics(dynClient) // Reload metrics into the cache when config changes

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

	factory.Start(stop)
	factory.WaitForCacheSync(stop)

	// Load initial metrics from ConfigMap
	loadMetrics(dynClient)

	fmt.Println("Informer started. Waiting for events...")

	// Wait for the monitoring goroutine to finish before exiting
	// wg.Wait()
	// fmt.Println("All monitoring finished. Exiting.")
	// CHANGED

	select {}
}

// This function runs in a goroutine and checks metrics periodically
// func monitorOperation(op *myv1.Operation, stopChan <-chan struct{}) {
func monitorOperation(opName string, stopChan <-chan struct{}) { // CHANGED
	fmt.Printf("Started monitoring operation: %s\n", opName)

    // Initial check
    if checkAndAbortIfUnhealthy(opName) {
        return
    }

	for {
		select {
		case <-stopChan:
			fmt.Println("Received stop signal. Stopping monitoring.")
			return
        case <-metricsUpdated:
            // This case will be triggered when metrics are updated
			if checkAndAbortIfUnhealthy(opName) {
                return
            }
		}
	}
}

func loadMetrics(dynClient dynamic.Interface) {
	// This function can be used to load initial metrics from ConfigMap
	cmRes := schema.GroupVersionResource{
		Group:    "",           // "" because ConfigMaps are core (not part of a named API group)
		Version:  "v1",         // API version
		Resource: "configmaps", // Plural name of the resource
	}

	// Use dynamic client to fetch the ConfigMap from the default namespace
	unstructuredCM, err := dynClient.Resource(cmRes).Namespace("default").Get(context.TODO(), "metrics-config", v1.GetOptions{})
	if err != nil {
		fmt.Printf("Warning: could not fetch initial metrics-config: %v\n", err)
		return
	}

	var cm corev1.ConfigMap
	// Convert from unstructured.Unstructured to typed *corev1.ConfigMap
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredCM.UnstructuredContent(), &cm)
	if err != nil {
		fmt.Printf("Failed to convert initial unstructured to ConfigMap: %v\n", err)
		return
	}

	// Store the initial ConfigMap data in the metrics cache
	mu.Lock()
	metricsCache = cm.Data
	mu.Unlock()
	fmt.Println("Metrics cache loaded.")
}

func checkAndAbortIfUnhealthy(opName string) bool {
	// Check the health of the operation and abort if unhealthy
    // Lock before reading shared metricsCache
	mu.Lock()
	metrics := metricsCache
	mu.Unlock()

    // Simulate metric evaluation
	if metrics["cpu"] == "high" {
		fmt.Printf("Unhealthy metrics detected for operation %s! Aborting.\n", opName)
		// TODO: Call Azure client to abort
        return true
	}

    fmt.Printf("Metrics healthy.")
    return false
}
