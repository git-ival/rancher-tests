# K3k Bulk Cluster Registration

`k3kbulk` creates K3k virtual clusters with `k3kcli` and registers each one as a Rancher downstream cluster.

See [EFFORT_SUMMARY.md](EFFORT_SUMMARY.md) for the implementation handoff, pilot results, downstream VM failures, and clean-environment rerun checklist.

## Requirements

- `k3kcli` and `kubectl` in `PATH`
- Rancher access to generate host-cluster kubeconfigs, or explicit kubeconfigs
- A Rancher API token with permission to create and delete downstream clusters
- Network access from each K3k cluster to the Rancher server

## Configuration

Paths are resolved relative to the configuration file. The following example uses Rancher target discovery:

```yaml
batch: k3k-ci
count: 4
parallelism: 4
perTargetParallelism: 2
mode: shared
servers: 1
agents: 0
persistenceType: ephemeral
createTimeout: 10m
artifactsDir: artifacts

rancher:
  host: rancher.example.com
  tokenEnv: RANCHER_TOKEN
  insecure: false
  workspace: fleet-default

discoverTargets:
  prefix: host-
  type: nodedriver
  address: external
  allowInternalAddress: false
  weight: 1
  parallelism: 2
```

### Batch Fields

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `batch` | Yes | None | DNS-label prefix for generated cluster names. Cluster names are `<batch>-001`, `<batch>-002`, and so on. Maximum length is 58 characters to leave room for the numeric suffix. |
| `count` | Yes | None | Total number of k3k clusters to create and register. Must be greater than zero. |
| `parallelism` | No | `4` | Maximum number of complete cluster workflows running concurrently across all host targets. |
| `perTargetParallelism` | No | `2` | Default maximum concurrent `k3kcli` create/delete operations against one host cluster. A target's `parallelism` overrides it. |
| `mode` | No | `shared` | k3k mode passed to `k3kcli`: `shared`, `virtual`, or `hcp`. |
| `servers` | No | `1` | Number of k3s server pods in each virtual cluster. Must be at least one. |
| `agents` | No | `0` | Number of k3s agent pods. Nonzero values are supported only with `mode: virtual`. |
| `persistenceType` | No | `ephemeral` | Server data mode passed to `k3kcli`: `ephemeral` or `dynamic`. Dynamic mode requires suitable storage on every selected host. |
| `createTimeout` | No | `10m` | How long each `k3kcli cluster create` waits for its cluster to become ready. Uses Go duration syntax such as `30s`, `10m`, or `1h`. |
| `artifactsDir` | No | `artifacts` | Root directory for state, generated kubeconfigs, and per-cluster command logs. |
| `stateFile` | No | `<artifactsDir>/<batch>/state.json` | Persistent lifecycle state. Keep this file until deletion finishes; reruns use it to resume failed work. |
| `k3kcli` | No | `k3kcli` | Executable name in `PATH`, or an explicit path to the k3k CLI binary. |
| `kubectl` | No | `kubectl` | Executable name in `PATH`, or an explicit path to kubectl. |

### Rancher Fields

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `rancher.host` | Yes | None | Rancher hostname, optionally with `https://`. |
| `rancher.token` | Conditional | None | Rancher bearer token in `<token-id>:<secret>` form. Prefer `tokenEnv` so the token is not stored in YAML. |
| `rancher.tokenEnv` | Conditional | None | Environment variable containing the Rancher token. Used when `token` is empty. One of `token` or `tokenEnv` must resolve to a token. |
| `rancher.insecure` | No | `false` | Skip Rancher TLS certificate verification. Use only in controlled test environments. |
| `rancher.caFile` | No | System trust | PEM CA bundle used to verify Rancher. |
| `rancher.workspace` | No | `fleet-default` | Fleet namespace in which imported Rancher provisioning Cluster resources are created. |

Do not put token values in `tokenEnv`; it must contain an environment variable name such as `RANCHER_TOKEN`.

## Target Discovery

Targets may be listed explicitly or discovered from Rancher by cluster-name prefix and type:

```yaml
discoverTargets:
  prefix: host-
  types: [harvester, imported, hosted, nodedriver, custom]
  address: external
  allowInternalAddress: false
```

`type` may be used instead of `types` for one value. Supported values are `harvester`, `imported`, `hosted`, `nodedriver`, `custom`, `local`, `unknown`, `aks`, `eks`, `gke`, and `ali`. `hosted` includes AKS, EKS, GKE, and Ali clusters.

Discovery generates a Rancher kubeconfig for each selected host under `artifacts/<batch>/targets/`. For `kubeconfigServer`, it selects an active node's external address by default. Set `address: internal` or `allowInternalAddress: true` only when those addresses are reachable from the machine running this command.

Only active Rancher clusters are selected. Clusters previously created by this tool are excluded from discovery.

### Discovery Fields

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `discoverTargets.prefix` | No | Empty | Select only Rancher cluster display names beginning with this prefix. Empty matches every name. |
| `discoverTargets.type` | No | None | Select one cluster type. Combined with `types` if both are supplied. |
| `discoverTargets.types` | No | All supported non-local types | Select any of several cluster types. Supported values are listed below. |
| `discoverTargets.address` | No | `external` | Rancher node address used for `--kubeconfig-server`: `external` selects `externalIpAddress`; `internal` selects `ipAddress`. |
| `discoverTargets.allowInternalAddress` | No | `false` | With `address: external`, fall back to an internal node address when no external address exists. |
| `discoverTargets.weight` | No | `1` | Assignment weight applied to every discovered target. A target with weight `2` appears twice as often as one with weight `1`. |
| `discoverTargets.parallelism` | No | `perTargetParallelism` | Concurrent `k3kcli` operation limit applied to every discovered target. |

Cluster type classification uses Rancher's management-v3 `info.machineProvider` with legacy field fallbacks:

| Type | Meaning |
| --- | --- |
| `harvester` | Registered Harvester management cluster. This classification takes precedence over `imported`. |
| `imported` | Existing Kubernetes cluster imported into Rancher, including imported hosted-provider clusters. |
| `hosted` | Aggregate selector matching `aks`, `eks`, `gke`, and `ali`. |
| `nodedriver` | Rancher-provisioned cluster using a machine/node driver such as EC2. |
| `custom` | Rancher custom cluster with user-provided nodes. |
| `local` | Rancher's local management cluster. Not selected by an empty `types` list. |
| `unknown` | Cluster for which Rancher exposes no recognized type. Not selected by an empty `types` list. |
| `aks`, `eks`, `gke`, `ali` | A specific hosted provider. |

Discovery fails if a matched cluster has no usable node address. `kubeconfigServer` controls virtual-cluster API connectivity and TLS SANs; it does not schedule k3k pods onto that node.

## Explicit Targets

Explicit targets can replace or be combined with discovered targets:

```yaml
targets:
  - name: downstream-a
    type: nodedriver
    kubeconfig: /path/to/downstream-a.yaml
    kubeconfigServer: 192.0.2.10
    weight: 1
    parallelism: 2
```

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `targets[].name` | Yes | None | Unique target name stored in batch state. |
| `targets[].type` | No | Empty | Informational type recorded in state. If set, it must be one of the supported discovery types. |
| `targets[].kubeconfig` | Yes | None | Host-cluster kubeconfig used by `k3kcli` and deletion verification. |
| `targets[].kubeconfigServer` | Yes | None | Reachable host node, load-balancer IP, or DNS name supplied to `k3kcli --kubeconfig-server`. |
| `targets[].weight` | No | `1` | Weighted round-robin assignment frequency. |
| `targets[].parallelism` | No | `perTargetParallelism` | Concurrent `k3kcli` operation limit for this target. |

## Usage

```bash
export RANCHER_TOKEN='token-id:secret'
go run ./validation/pipeline/k3kbulk --config ./validation/pipeline/k3kbulk/example-config.yaml create
go run ./validation/pipeline/k3kbulk --config ./validation/pipeline/k3kbulk/example-config.yaml delete
```

Command flags:

| Flag | Default | Description |
| --- | --- | --- |
| `--config` | `k3k-bulk.yaml` | Configuration file path. |
| `--timeout` | `45m` | Overall timeout for target discovery and the requested create/delete operation. |

The state file under `artifacts/<batch>/state.json` records target assignment and Rancher IDs. Keep it until deletion finishes. Creation can be rerun after a partial failure; existing K3k and Rancher resources are reused.

Deletion removes the Rancher registration first, waits for Rancher cleanup, and then deletes the K3k cluster.

`kubeconfigServer` is the reachable host address written into the generated K3k kubeconfig. It does not control pod scheduling.

## Dartboard Integration

Dartboard writes direct kubeconfigs for its OpenTofu-created clusters under
`<tofu-main-directory>/<workspace>_config/`. Generate an explicit-target
configuration from all `downstream-*.yaml` files with:

```bash
./validation/pipeline/k3kbulk/generate-config-from-dartboard.sh \
  --config-dir /path/to/tofu/main/aws/my-workspace_config \
  --rancher-host rancher.example.com \
  --batch k3k-ci \
  --count 4 \
  --address internal \
  --ensure-k3k \
  --output ./validation/pipeline/k3kbulk/config.yaml
```

The script queries each cluster for a host node address. It uses an external
address by default; use `--address internal` when the runner can reach
Dartboard's private network, or `--allow-internal-address` for fallback. It
intentionally does not reuse a kubeconfig's SSH-tunneled API endpoint because
that tunnel does not expose K3k's virtual-cluster service port. The script does
not select `upstream.yaml`, `tester.yaml`, or `clusters_state.yaml`.

Dartboard does not persist the Rancher API token generated during deployment,
and its upstream Kubernetes CA is not necessarily Rancher's HTTPS CA. Supply a
durable Rancher API token through `RANCHER_TOKEN`. Pass `--rancher-ca` when a
Rancher CA bundle is available, or `--rancher-insecure` only for an isolated
test environment.

`--ensure-k3k` runs a version-pinned Helm `upgrade --install` on every selected
host, then waits for the K3k Cluster CRD and controller Deployment. Run this
immediately before `k3kbulk create`; a failure prevents the bulk workload from
starting with only some usable hosts. The K3k chart defaults to version `1.1.0`
and can be changed with `--k3k-version`.

## Artifacts And State

For each cluster, the tool writes its generated kubeconfig and `create.log`, `register.log`, `delete.log`, and `wait-delete.log` as applicable. Files are grouped under `<artifactsDir>/<batch>/<cluster-name>/`.

The state file records host assignment, target type, k3k namespace, artifact paths, the Rancher provisioning ID (`namespace/name`), the Rancher management ID (`c-m-*`), current lifecycle stage, resume stage, and last error. It is written atomically with mode `0600`.
