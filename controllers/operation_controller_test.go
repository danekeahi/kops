package controllers_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/danekeahi/kops/controllers"
	"github.com/danekeahi/kops/internal/azure"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// AzureClientInterface defines the interface that the Azure client should implement
type AzureClientInterface interface {
	GetClusterOperationStatus(ctx context.Context) (*azure.OperationStatus, error)
}

// fakeAzureClient mocks the Azure client behavior
type fakeAzureClient struct {
	fakeStatus *azure.OperationStatus
}

func (f *fakeAzureClient) GetClusterOperationStatus(ctx context.Context) (*azure.OperationStatus, error) {
	return f.fakeStatus, nil
}

// fakeAzureClientWithError mocks the Azure client that returns an error
type fakeAzureClientWithError struct {
	err error
}

func (f *fakeAzureClientWithError) GetClusterOperationStatus(ctx context.Context) (*azure.OperationStatus, error) {
	return nil, f.err
}

func TestReconcile_CreateOperationCRWhenInProgress(t *testing.T) {
	scheme := runtime.NewScheme()

	// Fake Kubernetes client
	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).Build()

	// Fake Azure client returns an in-progress state
	azureClient := &fakeAzureClient{
		fakeStatus: &azure.OperationStatus{
			InProgress:    true,
			OperationType: "Updating",
			Status:        "Updating",
			OperationID:   "test-cluster-Updating",
		},
	}

	reconciler := &controllers.OperationReconciler{
		Client:        k8sClient,
		Azure:         azureClient,
		ResourceGroup: "test-rg",
		ClusterName:   "test-cluster",
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "op-test-cluster-updating", // Use generated name in lowercase
			Namespace: "default",
		},
	}

	// run reconcile
	result, err := reconciler.Reconcile(context.TODO(), req)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{RequeueAfter: 30 * time.Second}, result, "Expected requeue after 30s for continuous monitoring")

	// fetch the created object
	created := &unstructured.Unstructured{}
	created.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})

	err = k8sClient.Get(context.TODO(), req.NamespacedName, created)
	require.NoError(t, err, "Expected Operation CR to be created")

	// Verify CR content
	spec, found, err := unstructured.NestedMap(created.Object, "spec")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "Updating", spec["operationStatus"])
	require.Equal(t, "test-cluster", spec["clusterName"])
}

func TestReconcile_DeleteOperationCRWhenNotInProgress(t *testing.T) {
	scheme := runtime.NewScheme()

	// existing CR to be deleted
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})
	existing.SetName("op-test-cluster-Succeeded")
	existing.SetNamespace("default")

	k8sClient := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing). // inject pre-existing CR
		Build()

	azureClient := &fakeAzureClient{
		fakeStatus: &azure.OperationStatus{
			InProgress:    false,
			OperationType: "",
			Status:        "Succeeded",
			OperationID:   "test-cluster-Succeeded",
		},
	}

	reconciler := &controllers.OperationReconciler{
		Client:        k8sClient,
		Azure:         azureClient,
		ResourceGroup: "test-rg",
		ClusterName:   "test-cluster",
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "op-test-cluster-Succeeded",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.TODO(), req)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{RequeueAfter: 30 * time.Second}, result, "Expected requeue after 30s for continuous monitoring")

	// fetch again, should be deleted
	created := &unstructured.Unstructured{}
	created.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})
	err = k8sClient.Get(context.TODO(), req.NamespacedName, created)
	require.Error(t, err, "Expected Operation CR to be deleted")
}

func TestReconcile_AzureClientError(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).Build()

	// Azure client that returns an error
	azureClient := &fakeAzureClientWithError{
		err: fmt.Errorf("azure connection failed"),
	}

	reconciler := &controllers.OperationReconciler{
		Client:        k8sClient,
		Azure:         azureClient,
		ResourceGroup: "test-rg",
		ClusterName:   "test-cluster",
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-op",
			Namespace: "default",
		},
	}

	// Should return error when Azure client fails and requeue after 30s
	result, err := reconciler.Reconcile(context.TODO(), req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "azure connection failed")
	require.Equal(t, ctrl.Result{RequeueAfter: 30 * time.Second}, result, "Expected requeue after 30s even on error")
}

func TestReconcile_OperationAlreadyExistsWhenInProgress(t *testing.T) {
	scheme := runtime.NewScheme()

	// Pre-existing CR
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})
	existing.SetName("op-test-cluster-updating")
	existing.SetNamespace("default")

	k8sClient := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing).
		Build()

	// Azure client returns in-progress state
	azureClient := &fakeAzureClient{
		fakeStatus: &azure.OperationStatus{
			InProgress:    true,
			OperationType: "Updating",
			Status:        "Updating",
			OperationID:   "test-cluster-Updating",
		},
	}

	reconciler := &controllers.OperationReconciler{
		Client:        k8sClient,
		Azure:         azureClient,
		ResourceGroup: "test-rg",
		ClusterName:   "test-cluster",
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "op-test-cluster-updating",
			Namespace: "default",
		},
	}

	// Should not error when CR already exists and operation is in progress
	result, err := reconciler.Reconcile(context.TODO(), req)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{RequeueAfter: 30 * time.Second}, result, "Expected requeue after 30s for continuous monitoring")

	// Verify the CR still exists
	fetched := &unstructured.Unstructured{}
	fetched.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})
	err = k8sClient.Get(context.TODO(), req.NamespacedName, fetched)
	require.NoError(t, err)
}

func TestReconcile_NoOperationWhenNotInProgress(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).Build()

	// Azure client returns not in-progress state
	azureClient := &fakeAzureClient{
		fakeStatus: &azure.OperationStatus{
			InProgress:    false,
			OperationType: "",
			Status:        "Succeeded",
			OperationID:   "test-cluster-Succeeded",
		},
	}

	reconciler := &controllers.OperationReconciler{
		Client:        k8sClient,
		Azure:         azureClient,
		ResourceGroup: "test-rg",
		ClusterName:   "test-cluster",
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "op-test-cluster-Succeeded",
			Namespace: "default",
		},
	}

	// Should not error when no CR exists and operation is not in progress
	result, err := reconciler.Reconcile(context.TODO(), req)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{RequeueAfter: 30 * time.Second}, result, "Expected requeue after 30s for continuous monitoring")

	// Verify no CR was created
	fetched := &unstructured.Unstructured{}
	fetched.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})
	err = k8sClient.Get(context.TODO(), req.NamespacedName, fetched)
	require.Error(t, err, "Expected no Operation CR to exist")
}

func TestReconcile_DifferentOperationStates(t *testing.T) {
	testCases := []struct {
		name             string
		status           string
		expectInProgress bool
	}{
		{"Running state", "Running", true},
		{"Updating state", "Updating", true},
		{"Succeeded state", "Succeeded", false},
		{"Failed state", "Failed", false},
		{"Unknown state", "SomeUnknownState", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).Build()

			azureClient := &fakeAzureClient{
				fakeStatus: &azure.OperationStatus{
					InProgress:    tc.expectInProgress,
					OperationType: tc.status,
					Status:        tc.status,
					OperationID:   fmt.Sprintf("test-cluster-%s", tc.status),
				},
			}

			reconciler := &controllers.OperationReconciler{
				Client:        k8sClient,
				Azure:         azureClient,
				ResourceGroup: "test-rg",
				ClusterName:   "test-cluster",
			}

			// Use generated operation name instead of request name (ensure lowercase)
			expectedOpName := strings.ToLower(fmt.Sprintf("op-test-cluster-%s", tc.status))
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      expectedOpName,
					Namespace: "default",
				},
			}

			_, err := reconciler.Reconcile(context.TODO(), req)
			require.NoError(t, err)

			// Check if CR exists based on expected state
			fetched := &unstructured.Unstructured{}
			fetched.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "core.kops.aks.microsoft.com",
				Version: "v1",
				Kind:    "Operation",
			})
			err = k8sClient.Get(context.TODO(), req.NamespacedName, fetched)

			if tc.expectInProgress {
				require.NoError(t, err, "Expected Operation CR to be created for in-progress state")
			} else {
				require.Error(t, err, "Expected no Operation CR for non-in-progress state")
			}
		})
	}
}

func TestReconcile_WithNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).Build()

	azureClient := &fakeAzureClient{
		fakeStatus: &azure.OperationStatus{
			InProgress:    true,
			OperationType: "Updating",
			Status:        "Updating",
			OperationID:   "test-cluster-Updating",
		},
	}

	reconciler := &controllers.OperationReconciler{
		Client:        k8sClient,
		Azure:         azureClient,
		ResourceGroup: "test-rg",
		ClusterName:   "test-cluster",
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "op-test-cluster-updating",
			Namespace: "test-namespace",
		},
	}

	// Should handle request with namespace gracefully
	_, err := reconciler.Reconcile(context.TODO(), req)
	require.NoError(t, err)

	// Verify the CR was created in default namespace (controller uses default)
	created := &unstructured.Unstructured{}
	created.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "core.kops.aks.microsoft.com",
		Version: "v1",
		Kind:    "Operation",
	})

	err = k8sClient.Get(context.TODO(), types.NamespacedName{
		Name:      "op-test-cluster-updating",
		Namespace: "default",
	}, created)
	require.NoError(t, err, "Expected Operation CR to be created")
}

func TestReconcile_RequeuesWhenNotInProgress(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient := clientfake.NewClientBuilder().WithScheme(scheme).Build()

	azureClient := &fakeAzureClient{
		fakeStatus: &azure.OperationStatus{
			InProgress:    false,
			OperationType: "",
			Status:        "Succeeded",
			OperationID:   "test-cluster-Succeeded",
		},
	}

	reconciler := &controllers.OperationReconciler{
		Client:        k8sClient,
		Azure:         azureClient,
		ResourceGroup: "test-rg",
		ClusterName:   "test-cluster",
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "op-test-cluster-Succeeded",
			Namespace: "default",
		},
	}

	// Should return RequeueAfter when not in progress
	result, err := reconciler.Reconcile(context.TODO(), req)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, result.RequeueAfter, "Expected RequeueAfter to be 30 seconds for continuous monitoring")
}
