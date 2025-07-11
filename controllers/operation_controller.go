package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/danekeahi/kops/internal/azure"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	RequeueInterval = 30 * time.Second // Changed to 30s as requested
)

// AzureClientInterface defines the interface for Azure operations
type AzureClientInterface interface {
	GetClusterOperationStatus(ctx context.Context) (*azure.OperationStatus, error)
}

type OperationReconciler struct {
	client.Client
	Azure         AzureClientInterface
	ResourceGroup string
	ClusterName   string
}

// SetupWithManager sets up the controller with the manager
func (r *OperationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	op := &unstructured.Unstructured{}
	op.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(op).
		Complete(r)
}

// Reconcile is triggered when a new Operation CR is created or updated
// This implements the logic: always observing for new ongoing operations,
// create CR when operation is running, delete CR when operation ends, resync every 30s
func (r *OperationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.Infof("Reconciling Operation CR: %s", req.Name)

	// Get current Azure operation status
	state, err := r.Azure.GetClusterOperationStatus(ctx)
	if err != nil {
		klog.Errorf("Failed to get Azure cluster operation status: %v", err)
		return ctrl.Result{RequeueAfter: RequeueInterval}, err
	}

	klog.Infof("Azure operation status: inProgress=%v, status=%s", state.InProgress, state.Status)

	// Generate operation name based on current state
	opName := r.generateOperationName(state)

	// Check if Operation CR exists
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})

	err = r.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, existing)
	exists := err == nil

	// Core checking logic implementation
	if state.InProgress {
		// Operation is ongoing - create CR if it doesn't exist
		if !exists {
			cr := r.createOperationCR(opName, state)
			if err := r.Create(ctx, cr); err != nil {
				klog.Errorf("Failed to create Operation CR '%s': %v", opName, err)
				return ctrl.Result{RequeueAfter: RequeueInterval}, err
			}
			klog.Infof("Created Operation CR '%s' for ongoing operation", opName)
		} else {
			klog.Infof("Operation CR '%s' already exists for ongoing operation", opName)
		}
		// Continue monitoring the ongoing operation
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	} else {
		// Operation completed or no operation running - delete any existing CRs
		if exists {
			if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
				klog.Errorf("Failed to delete Operation CR '%s': %v", opName, err)
				return ctrl.Result{RequeueAfter: RequeueInterval}, err
			}
			klog.Infof("Deleted Operation CR '%s' as operation completed", opName)
		}

		// Clean up any other operation CRs that might exist
		if err := r.cleanupAllOperationCRs(ctx); err != nil {
			klog.Errorf("Failed to cleanup operation CRs: %v", err)
		}

		// Resync after 30s to check for new operations
		klog.Infof("No operation in progress, requeuing after %v to check for new operations", RequeueInterval)
		return ctrl.Result{RequeueAfter: RequeueInterval}, nil
	}
}

// createOperationCR creates a new Operation CR
func (r *OperationReconciler) createOperationCR(name string, state *azure.OperationStatus) *unstructured.Unstructured {
	cr := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "core.kops.aks.microsoft.com/v1",
			"kind":       "Operation",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": "default",
				"labels": map[string]interface{}{
					"azure.cluster.name":     r.ClusterName,
					"azure.resource.group":   r.ResourceGroup,
					"azure.operation.status": state.Status,
				},
				"annotations": map[string]interface{}{
					"azure.operation.started": time.Now().Format(time.RFC3339),
				},
			},
			"spec": map[string]interface{}{
				"operationStatus": state.Status,
				"operationType":   state.OperationType,
				"clusterName":     r.ClusterName,
				"resourceGroup":   r.ResourceGroup,
				"inProgress":      state.InProgress,
			},
			"status": map[string]interface{}{
				"phase":       "Running",
				"lastChecked": time.Now().Format(time.RFC3339),
			},
		},
	}

	cr.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})

	return cr
}

// generateOperationName creates a consistent name for operation CRs
// Ensures the name follows Kubernetes RFC 1123 subdomain naming conventions
func (r *OperationReconciler) generateOperationName(state *azure.OperationStatus) string {
	// Convert cluster name to lowercase for Kubernetes compliance
	clusterName := strings.ToLower(r.ClusterName)

	var operationName string
	if state.OperationType != "" {
		// Ensure operation type is lowercase
		operationType := strings.ToLower(state.OperationType)
		operationName = fmt.Sprintf("op-%s-%s", clusterName, operationType)
	} else {
		// Ensure status is lowercase
		status := strings.ToLower(state.Status)
		operationName = fmt.Sprintf("op-%s-%s", clusterName, status)
	}

	// Additional safety: ensure the name is valid for Kubernetes RFC 1123
	// Replace any remaining invalid characters with hyphens
	operationName = strings.ReplaceAll(operationName, "_", "-")
	operationName = strings.ReplaceAll(operationName, " ", "-")
	operationName = strings.ReplaceAll(operationName, ".", "-")

	// Ensure the final name is lowercase (extra safety)
	operationName = strings.ToLower(operationName)

	return operationName
}

// cleanupAllOperationCRs removes all Operation CRs when no operations are running
func (r *OperationReconciler) cleanupAllOperationCRs(ctx context.Context) error {
	// List all Operation CRs
	operationList := &unstructured.UnstructuredList{}
	operationList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "OperationList",
	})

	if err := r.List(ctx, operationList, client.InNamespace("default")); err != nil {
		return fmt.Errorf("failed to list Operation CRs: %w", err)
	}

	// Delete all Operation CRs
	for i := range operationList.Items {
		op := &operationList.Items[i]
		if err := r.Delete(ctx, op); err != nil && !errors.IsNotFound(err) {
			klog.Errorf("Failed to delete Operation CR '%s': %v", op.GetName(), err)
			continue
		}
		klog.Infof("Cleaned up Operation CR '%s'", op.GetName())
	}

	return nil
}

// StartPolling starts a background goroutine that continuously monitors Azure for new operations
// This ensures the controller always observes for new ongoing operations
func (r *OperationReconciler) StartPolling(ctx context.Context) {
	klog.Infof("Starting Azure operation polling every %v", RequeueInterval)

	ticker := time.NewTicker(RequeueInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := r.discoverAndTriggerReconciliation(ctx); err != nil {
				klog.Errorf("Failed to discover operations: %v", err)
			}
		case <-ctx.Done():
			klog.Info("Stopping Azure operation polling")
			return
		}
	}
}

// discoverAndTriggerReconciliation checks for Azure operations and triggers reconciliation
func (r *OperationReconciler) discoverAndTriggerReconciliation(ctx context.Context) error {
	// Get current Azure operation status
	state, err := r.Azure.GetClusterOperationStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cluster operation status: %w", err)
	}

	klog.V(2).Infof("Polling Azure: inProgress=%v, status=%s, operationType=%s",
		state.InProgress, state.Status, state.OperationType)

	// Always trigger reconciliation to ensure proper CR management
	operationName := r.generateOperationName(state)
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      operationName,
			Namespace: "default",
		},
	}

	// Execute reconciliation
	_, err = r.Reconcile(ctx, req)
	if err != nil {
		klog.Errorf("Failed to reconcile operation '%s': %v", operationName, err)
	}

	return nil
}
