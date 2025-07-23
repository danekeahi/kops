package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kops/internal/azure"
)

const (
	PollingInterval = 30 * time.Second
	MaxRetries      = 3
	RetryDelay      = 5 * time.Second
)

type OperationReconciler struct {
	Client        client.Client
	Azure         azure.AzureClientInterface
	Namespace     string
	ResourceGroup string
	ClusterName   string

	isRunning bool
	stopCh    chan struct{}
}

type Config struct {
	Namespace     string
	ResourceGroup string
	ClusterName   string
}

func NewOperationReconciler(client client.Client, azureClient azure.AzureClientInterface, config Config) (*OperationReconciler, error) {
	if client == nil {
		return nil, fmt.Errorf("client cannot be nil")
	}
	if azureClient == nil {
		return nil, fmt.Errorf("azure client cannot be nil")
	}
	if config.ResourceGroup == "" {
		return nil, fmt.Errorf("resource group cannot be empty")
	}
	if config.ClusterName == "" {
		return nil, fmt.Errorf("cluster name cannot be empty")
	}
	if config.Namespace == "" {
		config.Namespace = "default"
	}

	return &OperationReconciler{
		Client:        client,
		Azure:         azureClient,
		Namespace:     config.Namespace,
		ResourceGroup: config.ResourceGroup,
		ClusterName:   config.ClusterName,
		stopCh:        make(chan struct{}),
	}, nil
}

func (r *OperationReconciler) Start(ctx context.Context) error {
	if r.isRunning {
		return fmt.Errorf("already running")
	}

	klog.InfoS("Starting monitoring", "cluster", r.ClusterName, "namespace", r.Namespace)

	// Quick validation
	if err := r.validateConnections(ctx); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	r.isRunning = true
	go r.monitoringLoop(ctx)

	klog.InfoS("Monitoring started")
	return nil
}

func (r *OperationReconciler) Stop() {
	if !r.isRunning {
		return
	}

	klog.InfoS("Stopping monitoring")
	close(r.stopCh)
	r.isRunning = false
}

func (r *OperationReconciler) validateConnections(ctx context.Context) error {
	// Test Azure
	testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := r.Azure.GetClusterOperationStatus(testCtx); err != nil {
		return fmt.Errorf("azure test failed: %w", err)
	}

	// Test Kubernetes - try to list in namespace
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "OperationList",
	})

	if err := r.Client.List(testCtx, list, client.InNamespace(r.Namespace)); err != nil {
		return fmt.Errorf("kubernetes test failed: %w", err)
	}

	return nil
}

func (r *OperationReconciler) monitoringLoop(ctx context.Context) {
	ticker := time.NewTicker(PollingInterval)
	defer ticker.Stop()

	// Initial sync
	r.syncWithRetry(ctx)

	for {
		select {
		case <-ticker.C:
			r.syncWithRetry(ctx)
		case <-r.stopCh:
			klog.InfoS("Monitoring loop stopped")
			return
		case <-ctx.Done():
			klog.InfoS("Context done, stopping")
			return
		}
	}
}

func (r *OperationReconciler) syncWithRetry(ctx context.Context) {
	for attempt := 1; attempt <= MaxRetries; attempt++ {
		if err := r.syncOperations(ctx); err != nil {
			klog.ErrorS(err, "Sync failed", "attempt", attempt)
			if attempt < MaxRetries {
				time.Sleep(RetryDelay)
				continue
			}
		} else {
			return // Success
		}
	}
}

func (r *OperationReconciler) syncOperations(ctx context.Context) error {
	// Get Azure status
	azureCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	state, err := r.Azure.GetClusterOperationStatus(azureCtx)
	if err != nil {
		return fmt.Errorf("failed to get Azure status: %w", err)
	}

	klog.V(2).InfoS("Azure status", "inProgress", state.InProgress, "type", state.Type)

	// Generate CR name
	opName := r.generateOperationName(state)

	// Check if CR exists
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})

	err = r.Client.Get(ctx, types.NamespacedName{
		Name:      opName,
		Namespace: r.Namespace,
	}, existing)

	exists := err == nil
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check CR: %w", err)
	}

	// Core logic: simple binary state
	if state.InProgress && !exists {
		return r.createCR(ctx, opName, state)
	} else if !state.InProgress && exists {
		return r.deleteCR(ctx, existing)
	}

	return nil
}

func (r *OperationReconciler) createCR(ctx context.Context, name string, state azure.OperationStatus) error {
	klog.InfoS("Creating CR", "name", name)

	cr := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "core.kops.aks.microsoft.com/v1",
			"kind":       "Operation",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": r.Namespace,
				"labels": map[string]interface{}{
					"azure.cluster.name":     r.ClusterName,
					"azure.resource.group":   r.ResourceGroup,
					"azure.operation.type":   state.Type,
					"azure.operation.status": "in-progress",
				},
				"annotations": map[string]interface{}{
					"azure.operation.id":      state.OperationID,
					"azure.operation.started": time.Now().Format(time.RFC3339),
				},
			},
			"spec": map[string]interface{}{
				"clusterName":   r.ClusterName,
				"resourceGroup": r.ResourceGroup,
				"operationType": state.Type,
				"operationID":   state.OperationID,
				"azureStatus":   state.Status,
			},
			"status": map[string]interface{}{
				"phase":       "InProgress",
				"azureStatus": state.Status,
				"lastChecked": time.Now().Format(time.RFC3339),
			},
		},
	}

	if err := r.Client.Create(ctx, cr); err != nil {
		return fmt.Errorf("failed to create CR: %w", err)
	}

	klog.InfoS("CR created", "name", name)
	return nil
}

func (r *OperationReconciler) deleteCR(ctx context.Context, cr *unstructured.Unstructured) error {
	name := cr.GetName()
	klog.InfoS("Deleting CR", "name", name)

	if err := r.Client.Delete(ctx, cr); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete CR: %w", err)
	}

	klog.InfoS("CR deleted", "name", name)
	return nil
}

func (r *OperationReconciler) generateOperationName(state azure.OperationStatus) string {
	cluster := strings.ToLower(r.ClusterName)
	opType := strings.ToLower(state.Type)

	// Clean for Kubernetes
	cluster = strings.ReplaceAll(cluster, ".", "-")
	cluster = strings.ReplaceAll(cluster, "_", "-")
	opType = strings.ReplaceAll(opType, ".", "-")
	opType = strings.ReplaceAll(opType, "_", "-")

	name := fmt.Sprintf("azure-op-%s-%s", cluster, opType)

	// Truncate if too long
	if len(name) > 63 {
		name = name[:63]
	}

	return name
}

func (r *OperationReconciler) CleanupOrphanedCRs(ctx context.Context) error {
	klog.InfoS("Cleaning up orphaned CRs")

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "OperationList",
	})

	listOpts := []client.ListOption{
		client.InNamespace(r.Namespace),
		client.MatchingLabels{"azure.cluster.name": r.ClusterName},
	}

	if err := r.Client.List(ctx, list, listOpts...); err != nil {
		return fmt.Errorf("failed to list CRs: %w", err)
	}

	deleted := 0
	for _, item := range list.Items {
		if err := r.Client.Delete(ctx, &item); err != nil && !errors.IsNotFound(err) {
			klog.ErrorS(err, "Failed to delete CR", "name", item.GetName())
		} else {
			deleted++
		}
	}

	klog.InfoS("Cleanup complete", "deleted", deleted)
	return nil
}

func (r *OperationReconciler) GetStatus() map[string]interface{} {
	return map[string]interface{}{
		"running":         r.isRunning,
		"cluster":         r.ClusterName,
		"namespace":       r.Namespace,
		"pollingInterval": PollingInterval.String(),
	}
}
