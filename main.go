package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"kops/client"
	"kops/metric_collector"

	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

func main() {

	// Start health endpoint in a goroutine (non-blocking)
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "OK")
		})

		port := "8080"
		log.Printf("Health endpoint running on :%s\n", port)
		err := http.ListenAndServe(":"+port, nil)
		if err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
	resourceGroup := os.Getenv("AZURE_RESOURCE_GROUP")
	clusterName := os.Getenv("AZURE_CLUSTER_NAME")

	kubeClient, err := client.GetKubeClientForAKSCluster(context.Background(), subscriptionID, resourceGroup, clusterName)
	if err != nil {
		fmt.Printf("Error creating clientset: %v\n", err)
		return
	}

	// Create a metrics client to collect resource usage
	metricsConfig, err := rest.InClusterConfig()
	if err != nil {
		fmt.Printf("Error creating in-cluster config: %v\n", err)
		return
	}
	metricsClient, err := metrics.NewForConfig(metricsConfig)
	if err != nil {
		fmt.Printf("Error creating metrics client: %v\n", err)
		return
	}

	// Config Map

	// Check if ConfigMap already exists and we create only if it doesn't
	configMapName := "metrics-store"
	namespace := "default"

	existingConfigMap, err := kubeClient.CoreV1().ConfigMaps(namespace).Get(
		context.Background(),
		configMapName,
		metav1.GetOptions{},
	)

	if err != nil {
		// ConfigMap doesn't exist so we have to create a new one
		fmt.Printf("ConfigMap '%s' not found, creating new one...\n", configMapName)

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: namespace,
			},
			Data: map[string]string{},
		}

		createdConfigMap, err := kubeClient.CoreV1().ConfigMaps(namespace).Create(
			context.Background(),
			configMap,
			metav1.CreateOptions{},
		)
		if err != nil {
			fmt.Printf("Error creating ConfigMap: %v\n", err)
			return
		}

		fmt.Printf("ConfigMap '%s' created successfully in namespace '%s'\n",
			createdConfigMap.Name, createdConfigMap.Namespace)
	} else {
		// ConfigMap already exists so we use the existing one
		fmt.Printf("Found existing ConfigMap '%s' in namespace '%s'\n",
			existingConfigMap.Name, existingConfigMap.Namespace)

		// Show some info about existing data. How many collections and when was the last update time
		if existingConfigMap.Data != nil {
			if totalCollections, exists := existingConfigMap.Data["total_collections"]; exists {
				fmt.Printf("Existing ConfigMap has %s previous collections\n", totalCollections)
			}
			if lastUpdated, exists := existingConfigMap.Data["last_updated"]; exists {
				fmt.Printf("Last updated: %s\n", lastUpdated)
			}
		}
	}

	// Set up metrics collection every 30 seconds
	fmt.Println("\nStarting continuous metrics collection (every 30 seconds)...")
	fmt.Println("Press Ctrl+C to stop")

	// Create a ticker that triggers every 30 seconds
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Collect metrics immediately on startup
	fmt.Println("\n=== Initial metrics collection ===")
	err = metric_collector.CollectAndStoreMetrics(kubeClient, metricsClient)
	if err != nil {
		fmt.Printf("Error collecting initial metrics: %v\n", err)
	} else {
		fmt.Printf("Initial collection completed at %s\n", time.Now().Format("15:04:05"))
	}

	// Start the continuous collection loop
	collectionCount := 1
	fmt.Printf("\n Next collection will be at %s\n", time.Now().Add(30*time.Second).Format("15:04:05"))

	for range ticker.C {
		collectionCount++
		fmt.Printf("\n=== Metrics collection #%d ===\n", collectionCount)
		fmt.Printf("Time: %s\n", time.Now().Format("2006-01-02 15:04:05"))

		err = metric_collector.CollectAndStoreMetrics(kubeClient, metricsClient)
		if err != nil {
			fmt.Printf("Error collecting metrics: %v\n", err)
			// Continue running even if one collection fails
		} else {
			fmt.Printf("Collection completed successfully\n")
		}

		// Show when next collection will happen
		nextTime := time.Now().Add(30 * time.Second)
		fmt.Printf("Next collection at %s\n", nextTime.Format("15:04:05"))
	}
}
