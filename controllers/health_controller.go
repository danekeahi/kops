package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicinformer "k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"kops/internal/azure"
)

var (
	operationMonitors = make(map[string]chan struct{}) // Map from CR name to stopChan (one per CR)
	metricsUpdated    chan struct{}                    // Used to signal when metrics are updated
)

func StartHealthMonitoring(azureClient *azure.Client, typedClient kubernetes.Interface, dynClient dynamic.Interface, baseClient kubernetes.Interface) error {
	// Initialize channel for metrics updates
	metricsUpdated = make(chan struct{}, 1) // Buffered so it won't block

	// Create an informer factory for your CRD
	dynFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynClient, time.Minute*10, "default", nil)

	// Create a typed informer factory for config maps
	typedFactory := informers.NewSharedInformerFactoryWithOptions(typedClient, time.Minute*10, informers.WithNamespace("default"))

	// Get the informer for your Operation CR
	gvr := schema.GroupVersionResource{
		Group:    "core.kops.aks.microsoft.com",
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
			if _, exists := operationMonitors[name]; exists {
				fmt.Printf("Monitoring already running for %s. Skipping.\n", name)
				return
			}

			// Start monitoring in a goroutine
			stop := make(chan struct{})
			operationMonitors[name] = stop // Store the stop channel for this CR
			fmt.Printf("Starting monitoring for operation: %s\n", name)
			go monitorOperation(name, stop, azureClient, typedClient, baseClient)
		},

		DeleteFunc: func(obj interface{}) {
			u := obj.(*unstructured.Unstructured)
			name := u.GetName()
			fmt.Printf("Operation CR deleted: %s\n", name)

			// Signal the monitoring goroutine to stop
			if stop, exists := operationMonitors[name]; exists {
				close(stop)
				delete(operationMonitors, name) // Remove from map
				fmt.Printf("Stopped monitoring for operation: %s\n", name)
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
func monitorOperation(opName string, stopChan <-chan struct{}, azureClient *azure.Client, typedClient kubernetes.Interface, baseClient kubernetes.Interface) {
	fmt.Printf("Started monitoring operation: %s\n", opName)

	// Initial check
	if checkAndAbortIfUnhealthy(opName, azureClient, typedClient, baseClient) {
		return
	}

	for {
		select {
		case <-stopChan:
			fmt.Println("Received stop signal. Stopping monitoring.")
			return
		case <-metricsUpdated:
			// This case will be triggered when metrics are updated
			if checkAndAbortIfUnhealthy(opName, azureClient, typedClient, baseClient) {
				return
			}
		}
	}
}

// ThresholdViolation represents a metric that exceeded its threshold
type ThresholdViolation struct {
	Metric    string
	Current   float64
	Threshold float64
	Reason    string
}

func checkAndAbortIfUnhealthy(opName string, azureClient *azure.Client, typedClient kubernetes.Interface, baseClient kubernetes.Interface) bool {
	// Check the health of the operation and abort if unhealthy
	metricCM, err := typedClient.CoreV1().ConfigMaps("default").Get(context.TODO(), "metrics-store", metav1.GetOptions{})
	if err != nil {
		fmt.Printf("Failed to fetch metrics-store: %v\n", err)
		return false
	}

	// Parse the current_metrics.json from the ConfigMap
	rawMetrics := metricCM.Data["current_metrics.json"]
	if rawMetrics == "" {
		fmt.Println("current_metrics.json not found in ConfigMap.")
		return false
	}

	// Unmarshal the JSON data
	var parsed map[string]interface{}
	err = json.Unmarshal([]byte(rawMetrics), &parsed)
	if err != nil {
		fmt.Printf("Failed to parse current_metrics.json: %v\n", err)
		return false
	}

	// Fetch metric-thresholds ConfigMap from base cluster
	thresholdCM, err := baseClient.CoreV1().ConfigMaps("default").Get(context.TODO(), "metric-thresholds", metav1.GetOptions{})
	if err != nil {
		fmt.Printf("Failed to fetch metric-thresholds ConfigMap: %v\n", err)
		return false
	}

	// Parse the thresholds
	rawThresholds := thresholdCM.Data["thresholds.json"]
	if rawThresholds == "" {
		fmt.Println("thresholds.json not found in metric-thresholds ConfigMap.")
		return false
	}

	// Parse the thresholds JSON
	var thresholds map[string]interface{}
	err = json.Unmarshal([]byte(rawThresholds), &thresholds)
	if err != nil {
		fmt.Printf("Failed to parse thresholds.json: %v\n", err)
		return false
	}

	// Extract metrics sections
	podMetrics, _ := parsed["pod_metrics"].(map[string]interface{})
	nodeMetrics, _ := parsed["node_metrics"].(map[string]interface{})
	containerStats, _ := parsed["container_stats"].(map[string]interface{})
	serviceHealth, _ := parsed["serviceHealth"].(map[string]interface{})
	resourceUsage, _ := parsed["resource_usage"].(map[string]interface{})
	thresholdsMap, _ := thresholds["thresholds"].(map[string]interface{})

	// Validate required sections exist
	if err := validateMetricSections(podMetrics, nodeMetrics, containerStats, serviceHealth, resourceUsage, thresholdsMap); err != nil {
		fmt.Printf("Metrics validation failed: %v\n", err)
		return false
	}

	// Collect all threshold violations
	violations := checkAllThresholds(podMetrics, nodeMetrics, containerStats, serviceHealth, resourceUsage, thresholdsMap)

	// If any violations found, report them all and abort
	if len(violations) > 0 {
		reportViolations(violations)
		// Create a summary reason for the abort
		reason := fmt.Sprintf("multiple_threshold_violations_%d", len(violations))
		return abort(azureClient, opName, reason)
	}

	fmt.Println("All metrics healthy.")
	return false
}

// validateMetricSections ensures all required metric sections are present
func validateMetricSections(podMetrics, nodeMetrics, containerStats, serviceHealth, resourceUsage, thresholdsMap map[string]interface{}) error {
	if podMetrics == nil {
		return fmt.Errorf("no pod metrics found")
	}
	if nodeMetrics == nil {
		return fmt.Errorf("no node metrics found")
	}
	if containerStats == nil {
		return fmt.Errorf("no container stats found")
	}
	if serviceHealth == nil {
		return fmt.Errorf("no service health found")
	}
	if resourceUsage == nil {
		return fmt.Errorf("no resource usage found")
	}
	if thresholdsMap == nil {
		return fmt.Errorf("no thresholds found")
	}
	return nil
}

// checkAllThresholds evaluates all metrics against their thresholds
func checkAllThresholds(podMetrics, nodeMetrics, containerStats, serviceHealth, resourceUsage, thresholdsMap map[string]interface{}) []ThresholdViolation {
	violations := []ThresholdViolation{}

	// Helper to get a threshold value
	getThreshold := func(key string) (float64, bool) {
		val, ok := thresholdsMap[key]
		if !ok {
			return 0, false
		}
		return val.(float64), true
	}

	// Service health check (special case - boolean)
	if healthy, ok := serviceHealth["healthy"].(bool); ok && !healthy {
		violations = append(violations, ThresholdViolation{
			Metric:    "service_health",
			Current:   0,
			Threshold: 1,
			Reason:    "service_health_false",
		})
	}

	// Check node metrics
	violations = append(violations, checkNodeMetrics(nodeMetrics, getThreshold)...)

	// Check container metrics
	violations = append(violations, checkContainerMetrics(containerStats, getThreshold)...)

	// Check pod metrics
	violations = append(violations, checkPodMetrics(podMetrics, getThreshold)...)

	violations = append(violations, checkResourceUsage(resourceUsage, getThreshold)...)

	return violations
}

// checkNodeMetrics checks all node-related thresholds
func checkNodeMetrics(nodeMetrics map[string]interface{}, getThreshold func(string) (float64, bool)) []ThresholdViolation {
	violations := []ThresholdViolation{}

	// not_ready_nodes_percent
	if threshold, ok := getThreshold("not_ready_nodes_percent"); ok {
		if notReadyPercent, exists := nodeMetrics["not_ready_percent"].(float64); exists && notReadyPercent >= threshold {
			violations = append(violations, ThresholdViolation{
				Metric:    "not_ready_nodes_percent",
				Current:   notReadyPercent,
				Threshold: threshold,
				Reason:    "not_ready_nodes_threshold_exceeded",
			})
		}
	}

	return violations
}

// checkContainerMetrics checks all container-related thresholds
func checkContainerMetrics(containerStats map[string]interface{}, getThreshold func(string) (float64, bool)) []ThresholdViolation {
	violations := []ThresholdViolation{}

	// crash_loop_percent
	if threshold, ok := getThreshold("crash_loop_percent"); ok {
		if crashLoopPercent, exists := containerStats["crash_loop_percent"].(float64); exists && crashLoopPercent >= threshold {
			violations = append(violations, ThresholdViolation{
				Metric:    "crash_loop_percent",
				Current:   crashLoopPercent,
				Threshold: threshold,
				Reason:    "crash_loop_threshold_exceeded",
			})
		}
	}

	return violations
}

// checkPodMetrics checks all pod-related thresholds
func checkPodMetrics(podMetrics map[string]interface{}, getThreshold func(string) (float64, bool)) []ThresholdViolation {
	violations := []ThresholdViolation{}

	// crashing_pods_percent
	if threshold, ok := getThreshold("crashing_pods_percent"); ok {
		if crashingPercent, exists := podMetrics["crashing_percent"].(float64); exists && crashingPercent >= threshold {
			violations = append(violations, ThresholdViolation{
				Metric:    "crashing_pods_percent",
				Current:   crashingPercent,
				Threshold: threshold,
				Reason:    "crashing_pods_threshold_exceeded",
			})
		}
	}

	// restart_percent
	if threshold, ok := getThreshold("restart_percent"); ok {
		if restartPercent, exists := podMetrics["restart_percent"].(float64); exists && restartPercent >= threshold {
			violations = append(violations, ThresholdViolation{
				Metric:    "restart_percent",
				Current:   restartPercent,
				Threshold: threshold,
				Reason:    "restart_percent_exceeded",
			})
		}
	}

	// restart_count
	if threshold, ok := getThreshold("restart_count"); ok {
		if restartCount, exists := podMetrics["total_restarts"].(float64); exists && restartCount >= threshold {
			violations = append(violations, ThresholdViolation{
				Metric:    "restart_count",
				Current:   restartCount,
				Threshold: threshold,
				Reason:    "restart_count_threshold_exceeded",
			})
		}
	}

	// pending_pods_percent
	if threshold, ok := getThreshold("pending_pods_percent"); ok {
		if pendingPercent, exists := podMetrics["pending_percent"].(float64); exists && pendingPercent >= threshold {
			violations = append(violations, ThresholdViolation{
				Metric:    "pending_pods_percent",
				Current:   pendingPercent,
				Threshold: threshold,
				Reason:    "pending_pods_threshold_exceeded",
			})
		}
	}

	return violations
}

func checkResourceUsage(resourceUsage map[string]interface{}, getThreshold func(string) (float64, bool)) []ThresholdViolation {
	violations := []ThresholdViolation{}

	// cpu_usage_percent
	if threshold, ok := getThreshold("cpu_usage_percent"); ok {
		if cpuPercent, exists := resourceUsage["cpu_usage_percent"].(float64); exists && cpuPercent >= threshold {
			violations = append(violations, ThresholdViolation{
				Metric:    "cpu_usage_percent",
				Current:   cpuPercent,
				Threshold: threshold,
				Reason:    "cpu_usage_threshold_exceeded",
			})
		}
	}

	// memory_usage_percent
	if threshold, ok := getThreshold("memory_usage_percent"); ok {
		if memPercent, exists := resourceUsage["memory_usage_percent"].(float64); exists && memPercent >= threshold {
			violations = append(violations, ThresholdViolation{
				Metric:    "memory_usage_percent",
				Current:   memPercent,
				Threshold: threshold,
				Reason:    "memory_usage_threshold_exceeded",
			})
		}
	}

	return violations
}

// reportViolations prints all threshold violations in a formatted way
func reportViolations(violations []ThresholdViolation) {
	fmt.Printf("\n=== THRESHOLD VIOLATIONS DETECTED (%d) ===\n", len(violations))
	for i, v := range violations {
		fmt.Printf("%d. %s: current=%.2f, threshold=%.2f (reason: %s)\n",
			i+1, v.Metric, v.Current, v.Threshold, v.Reason)
	}
	fmt.Printf("==========================================\n\n")
}

func abort(azureClient *azure.Client, opName string, reason string) bool {
	fmt.Printf("Unhealthy metrics detected for operation %s! Aborting.\n", opName)
	// Call Azure client to abort
	ctx := context.Background()
	err := azureClient.AbortClusterOperation(ctx, reason)
	if err != nil {
		fmt.Printf("Failed to abort operation: %v\n", err)
	} else {
		fmt.Println("Abort request sent successfully.")
	}
	return true
}
