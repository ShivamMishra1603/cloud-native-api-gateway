# Walkthrough - Milestone 12: Benchmarking

We have completed the implementation of **Milestone 12 (Benchmarking)**. We evaluated our custom Go API Gateway under both Docker Compose and Kubernetes environments, analyzing how network layers and port-forwarding tunnels impact system throughput and request latency.

---

## Benchmark Comparison Table

The following table summarizes the metrics compiled from our Go load testing tool (`tests/load/benchmark.go`) running under the two different orchestrators with **20 concurrent workers** over a **5-second** run:

| Metric | Docker Compose Environment | Kubernetes Environment (via Port-Forward) |
| :--- | :--- | :--- |
| **Total Requests** | 20,009 | 4,403 |
| **Success Count** | 20,009 (100%) | 4,403 (100%) |
| **Error Count** | 0 | 0 |
| **Requests / Second** | **3,999.10** | **878.86** |
| **Mean Latency** | **4.99 ms** | **22.71 ms** |
| **p50 (Median)** | 3.79 ms | 20.88 ms |
| **p90** | 10.32 ms | 30.67 ms |
| **p95** | 12.65 ms | 35.11 ms |
| **p99** | 17.86 ms | 59.09 ms |
| **Max Latency** | 54.62 ms | 128.81 ms |

---

## Architectural Analysis: Why the Environment Matters

The benchmark results demonstrate that **Docker Compose** achieved nearly **4.5x higher throughput** and **4.5x lower mean latency** than the **Kubernetes** setup in our local development environment. 

### Why is Docker Compose Faster?
1. **Direct Host Port Mapping:** Docker Compose binds container ports directly to the host loopback device (`127.0.0.1`). This has negligible network routing overhead.
2. **Resource Allocation:** Containers in Compose share resources dynamically with the host system without the container runtime scheduler scheduling background control-plane tasks.

### Why is Kubernetes Slower in Local Development?
1. **Kubectl Port-Forwarding Tunnel Bottleneck:** Since NodePort mapping in local virtualized environments can be flaky, we routed traffic via `kubectl port-forward`. The kubectl port-forward tunnel is a single-threaded user-space TCP tunnel between the host machine and the K8s API Server. This tunnel becomes a major bottleneck under high concurrent request volumes, adding significant queuing latency.
2. **Kubernetes Control-Plane Overhead:** Inside the cluster, the gateway pods are concurrently answering liveness/readiness health probes scheduled by the Kubernetes control plane, consuming pod CPU.
3. **Network Virtualization:** Requests traverse multiple virtual networking layers (overlay networks) inside the single-node local Kubernetes cluster compared to Compose's simple network bridge.

> [!NOTE]
> In a production-grade Kubernetes cluster (e.g. running on bare metal or cloud providers like EKS/GKE), NodePorts and LoadBalancer IPs are mapped natively via the cloud controller manager, avoiding the `kubectl port-forward` bottleneck. Under those environments, Kubernetes throughput easily matches or exceeds Docker Compose performance due to cluster-wide auto-scaling and hardware-level network optimizations.
