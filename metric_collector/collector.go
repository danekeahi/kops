package metric_collector

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

// MetricsData struct holds all the collected metrics which we will send to the ConfigMap
type MetricsData struct {
	Timestamp      string         `json:"timestamp"`
	PodMetrics     PodMetrics     `json:"pod_metrics"`
	NodeMetrics    NodeMetrics    `json:"node_metrics"`
	ContainerStats ContainerStats `json:"container_stats"`
	ResourceUsage  ResourceUsage  `json:"resource_usage"`
	ServiceHealth  ServiceHealth  `json:"serviceHealth"`
}

type PodMetrics struct {
	TotalPods       int     `json:"total_pods"`
	CrashingPods    int     `json:"crashing_pods"`
	PendingPods     int     `json:"pending_pods"`
	RunningPods     int     `json:"running_pods"`
	CrashingPercent float64 `json:"crashing_percent"`
	PendingPercent  float64 `json:"pending_percent"`
	RunningPercent  float64 `json:"running_percent"`
	TotalRestarts   int     `json:"total_restarts"`
	RestartPercent  float64 `json:"restart_percent"`
}

type NodeMetrics struct {
	TotalNodes      int     `json:"total_nodes"`
	ReadyNodes      int     `json:"ready_nodes"`
	NotReadyNodes   int     `json:"not_ready_nodes"`
	ReadyPercent    float64 `json:"ready_percent"`
	NotReadyPercent float64 `json:"not_ready_percent"`
}

type ContainerStats struct {
	TotalContainers     int     `json:"total_containers"`
	CrashLoopContainers int     `json:"crash_loop_containers"`
	CrashLoopPercent    float64 `json:"crash_loop_percent"`
}

type ServiceHealth struct {
	URL          string `json:"url"`
	Healthy      bool   `json:"healthy"`
	ResponseTime int64  `json:"response_time"`
	Timestamp    string `json:"timestamp"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type ResourceUsage struct {
	CPUUsagePercent    float64 `json:"cpu_usage_percent"`
	MemoryUsagePercent float64 `json:"memory_usage_percent"`
}

// CollectAndStoreMetrics collects all Kubernetes metrics and stores them in the ConfigMap
func CollectAndStoreMetrics(kubeClient kubernetes.Interface, metricsClient *metrics.Clientset) error {
	fmt.Println("Starting metrics collection...")

	// Collect all metrics
	metrics, err := collectMetrics(kubeClient, metricsClient)
	if err != nil {
		return fmt.Errorf("error collecting metrics: %v", err)
	}

	// Update the ConfigMap with the metrics
	err = updateMetricsConfigMap(kubeClient, metrics)
	if err != nil {
		return fmt.Errorf("error updating ConfigMap: %v", err)
	}

	fmt.Printf("Metrics collected and stored in ConfigMap successfully!\n")
	fmt.Printf("Summary:\n")
	fmt.Printf("- Pods: %d total, %.1f%% crashing, %.1f%% pending\n",
		metrics.PodMetrics.TotalPods,
		metrics.PodMetrics.CrashingPercent,
		metrics.PodMetrics.PendingPercent)
	fmt.Printf("- Nodes: %d total, %.1f%% ready, %.1f%% not ready\n",
		metrics.NodeMetrics.TotalNodes,
		metrics.NodeMetrics.ReadyPercent,
		metrics.NodeMetrics.NotReadyPercent)
	fmt.Printf("- Containers: %d total, %.1f%% in crash loop\n",
		metrics.ContainerStats.TotalContainers,
		metrics.ContainerStats.CrashLoopPercent)
	fmt.Printf("- Service Health: %s (%dms response time)\n",
		getHealthStatus(metrics.ServiceHealth.Healthy),
		metrics.ServiceHealth.ResponseTime)
	fmt.Printf("- Cluster Resource Usage: CPU=%.1f%%, Memory=%.1f%%\n",
		metrics.ResourceUsage.CPUUsagePercent,
		metrics.ResourceUsage.MemoryUsagePercent)

	return nil
}

func getHealthStatus(healthy bool) string {
	if healthy {
		return "HEALTHY"
	}
	return "UNHEALTHY"
}

func collectMetrics(client kubernetes.Interface, metricsClient *metrics.Clientset) (*MetricsData, error) {
	metrics := &MetricsData{
		Timestamp: time.Now().Format(time.RFC3339),
	}

	// Collect Pod Metrics
	podMetrics, err := collectPodMetrics(client)
	if err != nil {
		return nil, fmt.Errorf("failed to collect pod metrics: %v", err)
	}
	metrics.PodMetrics = *podMetrics

	// Collect Node Metrics
	nodeMetrics, err := collectNodeMetrics(client)
	if err != nil {
		return nil, fmt.Errorf("failed to collect node metrics: %v", err)
	}
	metrics.NodeMetrics = *nodeMetrics

	// Collect Container Stats
	containerStats, err := collectContainerStats(client)
	if err != nil {
		return nil, fmt.Errorf("failed to collect container stats: %v", err)
	}
	metrics.ContainerStats = *containerStats

	// Collect Service Health
	serviceHealth := CollectServiceHealth()
	metrics.ServiceHealth = *serviceHealth

	// Collect Resource Usage Metrics
	resourceUsage, err := collectResourceUsageMetrics(metricsClient, client)
	if err != nil {
		return nil, fmt.Errorf("failed to collect resource usage metrics: %v", err)
	}
	metrics.ResourceUsage = *resourceUsage

	return metrics, nil

}

func collectPodMetrics(client kubernetes.Interface) (*PodMetrics, error) {
	// Get all pods across all namespaces
	podList, err := client.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	metrics := &PodMetrics{
		TotalPods: len(podList.Items),
	}

	totalRestarts := 0

	for _, pod := range podList.Items {
		// Count pod states
		switch pod.Status.Phase {
		case corev1.PodPending:
			metrics.PendingPods++
		case corev1.PodRunning:
			metrics.RunningPods++
		case corev1.PodFailed:
			metrics.CrashingPods++
		}

		// Check for crashing pods based on container statuses
		for _, containerStatus := range pod.Status.ContainerStatuses {
			totalRestarts += int(containerStatus.RestartCount)

			// Check if pod is crashing (waiting with CrashLoopBackOff)
			if containerStatus.State.Waiting != nil &&
				containerStatus.State.Waiting.Reason == "CrashLoopBackOff" {
				metrics.CrashingPods++
			}
		}
	}

	// Calculate percentages
	if metrics.TotalPods > 0 {
		metrics.CrashingPercent = float64(metrics.CrashingPods) / float64(metrics.TotalPods) * 100
		metrics.PendingPercent = float64(metrics.PendingPods) / float64(metrics.TotalPods) * 100
		metrics.RunningPercent = float64(metrics.RunningPods) / float64(metrics.TotalPods) * 100
		metrics.RestartPercent = float64(totalRestarts) / float64(metrics.TotalPods) * 100
	}

	metrics.TotalRestarts = totalRestarts

	return metrics, nil
}

func collectNodeMetrics(client kubernetes.Interface) (*NodeMetrics, error) {
	nodeList, err := client.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	metrics := &NodeMetrics{
		TotalNodes: len(nodeList.Items),
	}

	for _, node := range nodeList.Items {
		// Check node conditions to determine if ready
		isReady := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				if condition.Status == corev1.ConditionTrue {
					isReady = true
				}
				break
			}
		}

		if isReady {
			metrics.ReadyNodes++
		} else {
			metrics.NotReadyNodes++
		}
	}

	// Calculate percentages
	if metrics.TotalNodes > 0 {
		metrics.ReadyPercent = float64(metrics.ReadyNodes) / float64(metrics.TotalNodes) * 100
		metrics.NotReadyPercent = float64(metrics.NotReadyNodes) / float64(metrics.TotalNodes) * 100
	}

	return metrics, nil
}

func collectContainerStats(client kubernetes.Interface) (*ContainerStats, error) {
	podList, err := client.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	stats := &ContainerStats{}

	for _, pod := range podList.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			stats.TotalContainers++

			// Check for crash loop back off
			if containerStatus.State.Waiting != nil &&
				containerStatus.State.Waiting.Reason == "CrashLoopBackOff" {
				stats.CrashLoopContainers++
			}

			// Also check if container has high restart count (indicating crash loops)
			if containerStatus.RestartCount > 5 {
				stats.CrashLoopContainers++
			}
		}
	}

	// Calculate percentages
	if stats.TotalContainers > 0 {
		stats.CrashLoopPercent = float64(stats.CrashLoopContainers) / float64(stats.TotalContainers) * 100
	}

	return stats, nil
}

func CollectServiceHealth() *ServiceHealth {
	healthURL := os.Getenv("CUSTOMER_HEALTH_URL")

	health := &ServiceHealth{
		URL:       healthURL,
		Healthy:   false,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// If no URL is configured, mark as healthy but with a note, that way we don't abort the controller
	if healthURL == "" {
		health.Healthy = true
		health.ErrorMessage = "No health URL configured"
		health.ResponseTime = 0
		return health
	}

	// Create HTTP client with 5-second timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	startTime := time.Now()

	resp, err := client.Get(healthURL)

	responseTime := time.Since(startTime).Milliseconds()
	health.ResponseTime = responseTime

	if err != nil {
		health.Healthy = false
		health.ErrorMessage = fmt.Sprintf("Request failed: %v", err)
		fmt.Printf("Health check failed for %s: %v (response time: %dms)\n", healthURL, err, responseTime)
		return health
	}
	defer resp.Body.Close()

	// Mark healthy if status code is 2xx or 3xx
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		health.Healthy = true
	} else {
		health.Healthy = false
		health.ErrorMessage = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	return health
}

func collectResourceUsageMetrics(metricsClient *metrics.Clientset, kubeClient kubernetes.Interface) (*ResourceUsage, error) {
	ctx := context.Background()

	// 1. Get total allocatable resources across all nodes
	nodeList, err := kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting nodes: %v", err)
	}

	var totalClusterCPU int64 = 0 // millicores
	var totalClusterMem int64 = 0 // bytes

	for _, node := range nodeList.Items {
		totalClusterCPU += node.Status.Allocatable.Cpu().MilliValue()
		totalClusterMem += node.Status.Allocatable.Memory().Value()
	}

	fmt.Printf("Total cluster allocatable resources: CPU=%d millicores, Memory=%d bytes\n",
		totalClusterCPU, totalClusterMem)

	// If allocatable resources are zero, we can't calculate usage percentages
	if totalClusterCPU == 0 || totalClusterMem == 0 {
		return nil, fmt.Errorf("cluster allocatable capacity is zero — check node metrics or RBAC permissions")
	}

	// 2. Get pod usage metrics from metrics-server
	podMetricsList, err := metricsClient.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting pod metrics: %v", err)
	}

	var totalCPUUsed int64 = 0
	var totalMemUsed int64 = 0

	// Sum CPU and memory usage for all pods/containers
	for _, podMetric := range podMetricsList.Items {
		for _, containerMetric := range podMetric.Containers {
			totalCPUUsed += containerMetric.Usage.Cpu().MilliValue() // millicores
			totalMemUsed += containerMetric.Usage.Memory().Value()   // bytes
		}
	}

	fmt.Printf("Total pod resource usage: CPU=%d millicores, Memory=%d bytes\n",
		totalCPUUsed, totalMemUsed)

	// 3. Calculate usage percentages relative to cluster allocatable capacity
	usage := &ResourceUsage{}
	usage.CPUUsagePercent = math.Round(float64(totalCPUUsed)/float64(totalClusterCPU)*100*100) / 100
	usage.MemoryUsagePercent = math.Round(float64(totalMemUsed)/float64(totalClusterMem)*100*100) / 100

	return usage, nil
}

func updateMetricsConfigMap(client kubernetes.Interface, newMetrics *MetricsData) error {
	configMapName := "metrics-store"
	namespace := "default"

	// Get the existing ConfigMap
	configMap, err := client.CoreV1().ConfigMaps(namespace).Get(
		context.Background(),
		configMapName,
		metav1.GetOptions{},
	)
	if err != nil {
		// If the ConfigMap does not exist, create it
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: namespace,
			},
			Data: map[string]string{},
		}

		_, err := client.CoreV1().ConfigMaps(namespace).Create(
			context.Background(),
			configMap,
			metav1.CreateOptions{},
		)
		if err != nil {
			return fmt.Errorf("failed to create ConfigMap: %v", err)
		}
	}

	// Initialize data map if it doesn't exist
	if configMap.Data == nil {
		configMap.Data = make(map[string]string)
	}

	// Get existing metrics history
	var metricsHistory []MetricsData
	if existingHistory, exists := configMap.Data["metrics_history.json"]; exists && existingHistory != "" {
		err = json.Unmarshal([]byte(existingHistory), &metricsHistory)
		if err != nil {
			// If we can't parse existing history, start fresh but log the issue
			fmt.Printf("Warning: Could not parse existing metrics history, starting fresh: %v\n", err)
			metricsHistory = []MetricsData{}
		}
	}

	// Append new metrics to history
	metricsHistory = append(metricsHistory, *newMetrics)

	// Keep only the last 100 entries to prevent ConfigMap from growing too large (ConfigMaps have a size limit of ~1MB)
	maxHistoryEntries := 100
	if len(metricsHistory) > maxHistoryEntries {
		// Keep the most recent entries
		metricsHistory = metricsHistory[len(metricsHistory)-maxHistoryEntries:]
		fmt.Printf("Trimmed metrics history to last %d entries\n", maxHistoryEntries)
	}

	// Convert history back to JSON
	historyJSON, err := json.MarshalIndent(metricsHistory, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metrics history: %v", err)
	}

	// Convert current metrics to JSON
	currentMetricsJSON, err := json.MarshalIndent(newMetrics, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal current metrics: %v", err)
	}

	// Update ConfigMap with both current metrics and history
	configMap.Data["current_metrics.json"] = string(currentMetricsJSON)
	configMap.Data["metrics_history.json"] = string(historyJSON)
	configMap.Data["last_updated"] = time.Now().UTC().Format(time.RFC3339)
	configMap.Data["total_collections"] = fmt.Sprintf("%d", len(metricsHistory))

	// Update the ConfigMap in the cluster
	_, err = client.CoreV1().ConfigMaps(namespace).Update(
		context.Background(),
		configMap,
		metav1.UpdateOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to update ConfigMap: %v", err)
	}

	fmt.Printf("Metrics appended to history (total collections: %d)\n", len(metricsHistory))
	return nil
}
