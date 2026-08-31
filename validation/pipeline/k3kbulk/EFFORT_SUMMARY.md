# K3k Bulk Automation Effort Summary

## Objective

Automate creating many K3k virtual clusters on Kubernetes host clusters and registering each virtual cluster with Rancher. The intended infrastructure separates responsibilities:

- Harvester supplies virtual machines.
- Guest RKE2 or K3s clusters run as K3k host clusters.
- K3k clusters run in shared mode on those guest clusters.
- Rancher manages the guest host clusters and the imported K3k clusters.

Bulk K3k workloads should not run directly on the Harvester management cluster.

## Implemented Automation

The reusable implementation is under `actions/k3k/`. The command and operator documentation are under `validation/pipeline/k3kbulk/`.

Implemented capabilities include:

- Create K3k clusters with `k3kcli`.
- Register every created cluster with Rancher as an imported downstream cluster.
- Discover eligible host clusters from Rancher by display-name prefix and cluster type.
- Support explicit host kubeconfigs in addition to Rancher discovery.
- Generate target kubeconfigs and select reachable external or internal host API addresses.
- Distribute clusters across hosts using weighted round-robin assignment.
- Limit concurrency globally and independently per host cluster.
- Persist lifecycle state atomically so interrupted or failed batches can resume.
- Reuse existing K3k and Rancher resources on a resumed create operation.
- Delete the Rancher registration first, wait for cleanup, and then delete the K3k cluster.
- Write per-cluster command logs and generated kubeconfigs to the artifacts directory.
- Validate configuration, target types, modes, persistence types, and concurrency values.
- Cover configuration, discovery, Rancher API, state, and lifecycle behavior with unit tests.

The lifecycle state tracks the target assignment, target type, namespace, artifacts, Rancher provisioning and management IDs, current stage, resume stage, and last error.

## Primary Files

| Path | Purpose |
| --- | --- |
| `actions/k3k/config.go` | Configuration defaults, validation, and path resolution |
| `actions/k3k/discovery.go` | Rancher host-cluster discovery and target resolution |
| `actions/k3k/rancher.go` | Rancher import, registration, readiness, and deletion operations |
| `actions/k3k/command.go` | `k3kcli` and `kubectl` command execution and logs |
| `actions/k3k/lifecycle.go` | Concurrent, resumable create and delete orchestration |
| `actions/k3k/state.go` | Persistent batch state |
| `validation/pipeline/k3kbulk/main.go` | Command entry point |
| `validation/pipeline/k3kbulk/README.md` | Complete configuration and usage reference |
| `validation/pipeline/k3kbulk/example-config.yaml` | Example pilot configuration |
| `validation/pipeline/k3kbulk/generate-config-from-dartboard.sh` | Generate explicit targets from Dartboard outputs and optionally ensure K3k is ready on every host |

## Pilot Configuration

The attempted pilot used:

- Batch: `k3k-ci`
- Count: 2
- K3k mode: `shared`
- One virtual server and zero virtual agents per cluster
- Ephemeral persistence
- Global parallelism: 2
- Per-host parallelism: 1
- Rancher discovery prefix: `downstream-`
- Rancher discovery type: `imported`
- Target: `downstream-0-0`
- Host K3s version: `v1.36.3+k3s1`
- K3k controller image observed during diagnosis: `rancher/k3k:v1.1.0`

The pilot demonstrated that Rancher authentication, target discovery, target kubeconfig generation, and initial host API access worked. Both K3k custom resources were created on the selected host. Neither virtual cluster reached readiness because the host cluster became unstable.

## Pilot Failures

`k3k-ci-001` entered a failed state while `k3kcli` waited for readiness:

```text
failed to wait for cluster to become ready (status: Failed): cluster creation failed: Failed
```

Controller-side investigation also observed API discovery failure similar to:

```text
failed to get server groups: http2: client connection lost
```

`k3k-ci-002` remained provisioning until the ten-minute timeout:

```text
failed to get resource: client rate limiter Wait returned an error: context deadline exceeded
```

Its virtual kubelet reported that the server was not ready, and the host logged a CrashLoopBackOff for the K3k kubelet pod. These were consequences of host control-plane instability rather than failures in target selection or bulk assignment.

## Downstream Cluster And VM Errors

The affected host was `downstream-0-0`, initially observed with these nodes:

| Node | Address | Intended role | Observed state |
| --- | --- | --- | --- |
| `downstream-0-0-server-0` | `192.168.82.79` | control-plane, etcd | Ready, but embedded etcd unhealthy |
| `downstream-0-0-agent-0` | `192.168.80.125` | agent | Ready |
| `downstream-0-0-agent-1` | `192.168.89.128` | agent | Became NotReady |

Two other addresses were present in the server TLS SAN configuration:

- `192.168.80.174`
- `192.168.79.199`

The latter repeatedly contacted the K3s bootstrap endpoint and attempted to join embedded etcd as a server. It was not present in the Kubernetes node list. Its etcd member ID was `50902dc640aec1ad`.

The server logs established the sequence:

```text
Serving HTTP bootstrap from datastore for 192.168.79.199
added-peer-is-learner: true
failed to send database snapshot
membership: too many learner members in cluster
rejecting promote learner: learner is not ready
failed to reach the peer URL https://192.168.79.199:2380
connect: connection refused
peer ... failed to find local node
```

The VM at `192.168.79.199` remained reachable at layer 2 and periodically retried bootstrap, but its etcd peer endpoint either refused connections or reset them. This left the single healthy server continuously processing failed learner synchronization and membership operations.

API tests from `downstream-0-0-agent-1` showed synchronized failures through both paths:

- Direct server API: `https://192.168.82.79:6443`
- Local K3s supervisor proxy: `https://127.0.0.1:6444`

Both repeatedly followed the same pattern: fast `/api` and `/apis` responses, a 32-second discovery read timeout, a roughly 13-second failed retry, and then temporary recovery. Because the local proxy and direct path failed together, the issue was upstream of the agent proxy.

Network checks did not indicate an MTU or basic connectivity problem:

- Both agents had direct on-link routes to the server.
- Twenty ICMP requests completed with zero loss and sub-millisecond latency.
- Don't-fragment packets with a 1472-byte payload, corresponding to a 1500-byte IP packet, succeeded.
- `tracepath` was inconclusive because intermediate replies were unavailable.

The control-plane symptoms propagated across the cluster:

- Kubernetes API discovery and watches timed out or lost HTTP/2 connections.
- `downstream-0-0-agent-1` became NotReady.
- Token projection, Fleet, Traefik, K3k, and Rancher agents experienced API failures.
- K3k server and virtual-kubelet readiness failed.
- The `system-agent-upgrader` plan repeatedly failed with `DeadlineExceeded`.
- The upgrade plan had previously run with high concurrency; scaling the controller deployment to one replica did not repair the underlying etcd membership issue.

Before repair was considered, a local etcd snapshot was successfully saved as:

```text
before-etcd-repair-downstream-0-0-server-0-1787701634
```

The old cluster will be discarded instead of repaired.

## Root Cause Assessment

The primary blocker was malformed or unintended K3s server provisioning on guest VMs. A VM at `192.168.79.199` repeatedly joined as an etcd learner but could not receive or apply the datastore snapshot and never became promotable. The configured TLS SAN list also referenced `192.168.80.174`, indicating that server addresses or machine roles did not match the intended one-server/two-agent topology.

This was not diagnosed as a K3k capacity problem. Available memory and disk on the host nodes were sufficient for the two-cluster pilot. It was also not diagnosed as a normal MTU failure.

## Recreation Checklist

Before rerunning the pilot on rebuilt upstream and downstream clusters:

1. Decide and enforce the downstream control-plane topology. For a one-server/two-agent cluster, only the server VM may run `k3s server`; both worker VMs must run `k3s agent`.
2. Build TLS SANs from intended stable API endpoints only. Do not include old DHCP addresses or worker addresses unless clients must use them as API endpoints.
3. Confirm every VM has a unique hostname, machine identity, K3s node name, and data directory.
4. Ensure cloned VM images do not contain `/var/lib/rancher/k3s` state from another machine.
5. Confirm the server's etcd member list contains exactly the intended server nodes and no stuck learners.
6. Confirm all downstream nodes remain Ready and API watches are stable for several minutes.
7. Confirm the Rancher system-agent upgrade plan completes or pause upgrade activity during the initial K3k pilot.
8. Install and verify the K3k controller on the downstream host.
9. Use a new `batch` name and a clean artifacts directory, or deliberately remove the old pilot state after preserving it for diagnostics.
10. Start with one shared, ephemeral K3k cluster and per-target parallelism of one. Scale only after creation, registration, watch stability, and deletion all pass.

Suggested stability check from each agent:

```bash
timeout 300 k3s kubectl \
  --kubeconfig /var/lib/rancher/k3s/agent/kubelet.kubeconfig \
  --server https://127.0.0.1:6444 \
  get nodes --watch --request-timeout=0
```

Suggested first automation run:

```bash
export RANCHER_TOKEN='token-id:secret'
./validation/pipeline/k3kbulk/generate-config-from-dartboard.sh \
  --config-dir /path/to/tofu/main/aws/workspace_config \
  --rancher-host rancher.example.com \
  --batch k3k-ci-2 \
  --count 1 \
  --address internal \
  --ensure-k3k \
  --output ./validation/pipeline/k3kbulk/config.yaml
go run ./validation/pipeline/k3kbulk \
  --config ./validation/pipeline/k3kbulk/config.yaml \
  create
```

After confirming Rancher registration and K3k readiness, verify the reverse lifecycle:

```bash
go run ./validation/pipeline/k3kbulk \
  --config ./validation/pipeline/k3kbulk/example-config.yaml \
  delete
```

## Operational Notes

- Keep Rancher credentials in `RANCHER_TOKEN`; do not place token values directly in configuration or source files.
- Revoke any token that was exposed during testing.
- Prefer a valid Rancher CA bundle. Use `rancher.insecure: true` only in controlled test environments.
- Preserve `state.json` until the corresponding batch has been deleted successfully.
- Do not reuse the old `k3k-ci` state against newly created infrastructure because it contains old target assignments, Rancher IDs, and absolute artifact paths.
- The old artifacts remain useful as failure evidence under `validation/pipeline/k3kbulk/artifacts/k3k-ci/`.

## Current Status

The bulk automation implementation is complete enough for another controlled pilot. The previous end-to-end validation was blocked by the downstream host's embedded-etcd learner failure. Recreating the upstream and downstream clusters provides a clean environment to validate create, Rancher registration, readiness, resume, and ordered deletion independently of that infrastructure fault.
