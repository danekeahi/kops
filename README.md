## 🧱 Project Structure

This repository contains three main folders, each representing a separate containerized component:

- `operation_controller/`: Watches and manages custom Operation CRs on the cluster.
- `metric_collector/`: Collects metrics and writes them to a shared ConfigMap.
- `health_controller/`: Monitors metrics and evaluates whether operations should be aborted.

> ⚠️ **Note**: Do **not** build and push the entire repository as one image.  
> Each folder should be **built and pushed individually**, as each represents its own container.

---

## 🚀 Getting Started

To deploy the system to your **monitoring cluster**, follow these steps:

### 1. Set Up a User-Assigned Managed Identity (UAMI)

Create a user-assigned managed identity in Azure and assign it to the monitoring cluster.

Make sure:
- Workload Identity is enabled on the cluster.
- The service account in your deployment is annotated with the UAMI's client ID.
- The UAMI has sufficient permissions to read the AKS admin credentials (e.g., `Azure Kubernetes Service RBAC Cluster Admin` role).

### 2. Apply the CustomResourceDefinition (CRD)

Apply the Operation CRD to the monitoring cluster:

```sh
kubectl apply -f config/crd/bases/core.kops.aks.microsoft.com_operations.yaml
```

### 3. Edit the Deployment Configuration

Open the file:

```sh
config/manager/deployment.yaml
```

Update the following fields:

- **Managed Identity Client ID**  
  Replace the annotation value for:

  ```yaml
  azure.workload.identity/client-id: <your-UAMI-client-id>
  ```

- **Azure Configuration Environment Variables**  
  In each container (`operation-controller`, `metric-collector`, `health-controller`), set:

  ```yaml
  AZURE_SUBSCRIPTION_ID: <your-subscription-id>
  AZURE_RESOURCE_GROUP: <your-resource-group>
  AZURE_CLUSTER_NAME: <target-cluster-name>
  ```

- **Thresholds for Health Monitoring** (optional)  
  In the `health-controller` container section, adjust thresholds like:

  ```yaml
  CPU_THRESHOLD: "90"
  MEMORY_THRESHOLD: "85"
  CRASH_THRESHOLD: "3"
  ```

### 4. Apply the Deployment

Deploy the system to the monitoring cluster:

```sh
kubectl apply -f config/manager/deployment.yaml
```

This will create a single pod running three containers (one for each component), all sharing the same Kubernetes client and monitoring logic.

### 5. Start a Long-Running Operation

Once the system is deployed, creating a new `Operation` custom resource will trigger monitoring.  
If any thresholds are breached, the `health-controller` will automatically abort the operation based on your configuration.

You can create an Operation CR like this:

```sh
kubectl apply -f examples/sample-operation.yaml
```

You should see log output from the controllers indicating that monitoring has started and actions are being taken as appropriate.

---
