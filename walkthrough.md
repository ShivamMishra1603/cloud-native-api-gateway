# Walkthrough - Milestone 11: Kubernetes Deployment

We have successfully built, deployed, and verified the API Gateway stack running in a live Kubernetes cluster using **StatefulSets and Headless Services** for L7 routing control.

---

## Summary of Changes

### 1. Fixed Configuration Issues (Legit Bugs Caught)
* **Configuration Syntax:** Corrected `resiliency.timeouts` typo to `resiliency.timeout` (matching our Go struct tags) inside:
  - [gateway.docker.yaml](file:///Users/shivammishra/Documents/Topics/System%20Design/cloud-native-api-gateway/configs/gateway.docker.yaml)
  - [gateway.k8s.yaml](file:///Users/shivammishra/Documents/Topics/System%20Design/cloud-native-api-gateway/configs/gateway.k8s.yaml)
  - [configmap.yaml](file:///Users/shivammishra/Documents/Topics/System%20Design/cloud-native-api-gateway/deploy/kubernetes/configmap.yaml)
* **Absolute Path Mounting:** Updated JWT validation key path to use absolute directories inside containers `/app/configs/keys/demo-public.pem` rather than relative paths, ensuring robustness under different working directories.

### 2. Kubernetes Configs & Pod Discovery
* **[configmap.yaml](file:///Users/shivammishra/Documents/Topics/System%20Design/cloud-native-api-gateway/deploy/kubernetes/configmap.yaml) & [secret.yaml](file:///Users/shivammishra/Documents/Topics/System%20Design/cloud-native-api-gateway/deploy/kubernetes/secret.yaml):**
  - Mapped gateway settings and loaded base64-encoded demo credentials dynamically.
* **[catalog.yaml](file:///Users/shivammishra/Documents/Topics/System%20Design/cloud-native-api-gateway/deploy/kubernetes/catalog.yaml) & [order.yaml](file:///Users/shivammishra/Documents/Topics/System%20Design/cloud-native-api-gateway/deploy/kubernetes/order.yaml):**
  - Configured Headless Services (`clusterIP: None`) to route traffic to StatefulSet pod ordinal DNS hostnames (`catalog-0.catalog`, `catalog-1.catalog`).
  - Utilized the Kubernetes **Downward API** (`metadata.name`) to dynamically set `SERVICE_NAME` for mock backend containers.
* **[gateway.yaml](file:///Users/shivammishra/Documents/Topics/System%20Design/cloud-native-api-gateway/deploy/kubernetes/gateway.yaml):**
  - Configured Deployment with **3 replicas** of the gateway container, mounting configs and secrets, exposing them via NodePort on port `30080`.

---

## Live Verification & Testing Logs

### 1. Cluster Status & Manifest Application
Applying the Kubernetes manifests to the local cluster:
```text
kubectl apply -f deploy/kubernetes/

service/catalog created
statefulset.apps/catalog created
configmap/gateway-config created
service/gateway created
deployment.apps/gateway created
service/order created
statefulset.apps/order created
secret/demo-keys created
```

Checking pod states confirms all replicas are ready and running:
```text
kubectl get pods

NAME                       READY   STATUS    RESTARTS   AGE
catalog-0                  1/1     Running   0          6s
catalog-1                  1/1     Running   0          5s
gateway-5c75c45db4-55dbn   1/1     Running   0          6s
gateway-5c75c45db4-nl2lz   1/1     Running   0          6s
gateway-5c75c45db4-zmd2l   1/1     Running   0          6s
order-0                    1/1     Running   0          6s
order-1                    1/1     Running   0          5s
```

Checking active endpoints validates headless service target discovery:
```text
kubectl get endpoints

NAME         ENDPOINTS                                      AGE
catalog      10.1.0.20:8081,10.1.0.26:8081                  10s
gateway      10.1.0.21:8080,10.1.0.22:8080,10.1.0.24:8080   10s
order        10.1.0.23:8082,10.1.0.25:8082                  10s
```

### 2. Testing L7 Load Balancing & Tracing
Curling the exposed NodePort on `30080` demonstrates round-robin load distribution alternating between the StatefulSet pods:

```json
# Request 1 (Routes to catalog-0 pod)
curl -s -H "X-API-Key: alice-secret-key" http://localhost:30080/catalog/items
{
  "service": "catalog-0",
  "path": "/items",
  "request_id": "39190db93c4fe55ad11a1187489850c1",
  "consumer": "alice"
}

# Request 2 (Routes to catalog-1 pod)
curl -s -H "X-API-Key: alice-secret-key" http://localhost:30080/catalog/items
{
  "service": "catalog-1",
  "path": "/items",
  "request_id": "9224b4bfebb978695aedcdadeca25ca9",
  "consumer": "alice"
}
```

### 3. Check Traced Logs
Checking the startup logs from the gateway pod verifies it resolves stable StatefulSet pod names:
```json
{"time":"2026-07-06T02:19:04.02038501Z","level":"INFO","msg":"config loaded successfully","port":8080}
{"time":"2026-07-06T02:19:04.020497176Z","level":"INFO","msg":"initializing load balancer","service":"catalog-service","strategy":"round_robin"}
{"time":"2026-07-06T02:19:04.021103593Z","level":"INFO","msg":"health checker: starting monitoring background tasks","services_count":2}
{"time":"2026-07-06T02:19:04.021196093Z","level":"INFO","msg":"health checker: worker started","service":"catalog-service","url":"http://catalog-0.catalog:8081"}
{"time":"2026-07-06T02:19:04.021226176Z","level":"INFO","msg":"health checker: worker started","service":"catalog-service","url":"http://catalog-1.catalog:8081"}
{"time":"2026-07-06T02:19:04.021129718Z","level":"INFO","msg":"gateway listening","addr":":8080"}
```
