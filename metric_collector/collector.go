package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// MetricsData struct holds all the collected metrics which we will send to the ConfigMap
type MetricsData struct {
	Timestamp      string         `json:"timestamp"`
	PodMetrics     PodMetrics     `json:"pod_metrics"`
	NodeMetrics    NodeMetrics    `json:"node_metrics"`
	ContainerStats ContainerStats `json:"container_stats"`
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

// CollectAndStoreMetrics collects all Kubernetes metrics and stores them in the ConfigMap
func CollectAndStoreMetrics(kubeClient kubernetes.Interface) error {
	fmt.Println("Starting metrics collection...")

	// Collect all metrics
	metrics, err := collectMetrics(kubeClient)
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

	return nil
}

func collectMetrics(client kubernetes.Interface) (*MetricsData, error) {
	metrics := &MetricsData{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
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
		return fmt.Errorf("failed to get ConfigMap: %v", err)
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

	// Keep only the last 100 entries to prevent ConfigMap from growing too large
	// (ConfigMaps have a size limit of ~1MB)
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
