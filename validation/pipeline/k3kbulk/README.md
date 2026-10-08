# K3k Bulk Cluster Registration

`k3kbulk` creates K3k virtual clusters and registers each one as a Rancher downstream cluster.

Each cluster is created by applying a `k3k.io/v1beta1` `Cluster` manifest rendered from the `clusterSpec` block of the configuration file. `k3kcli` is still used to mint each cluster's kubeconfig, which requires the cluster CA.

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

clusterSpec:
  mode: shared
  servers: 1
  persistence:
    type: ephemeral
```

Generate a fully annotated starting point, in which every supported `clusterSpec` field is present and the ones you did not set are commented out:

```bash
go run ./validation/pipeline/k3kbulk generate-config \
  --batch k3k-ci --count 4 \
  --rancher-host rancher.example.com \
  --discover-prefix host- --discover-type nodedriver \
  --output ./validation/pipeline/k3kbulk/config.yaml
```

[example-config.yaml](example-config.yaml) is the output of that command.

Ready-to-run configurations for common scenarios are in [examples/](examples/README.md).

### Batch Fields

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `batch` | Yes | None | DNS-label prefix for generated cluster names. Cluster names are `<batch>-001`, `<batch>-002`, and so on. Maximum length is 58 characters to leave room for the numeric suffix. |
| `count` | Yes | None | Total number of k3k clusters to create and register. Must be greater than zero. |
| `parallelism` | No | `4` | Maximum number of complete cluster workflows running concurrently across all host targets. |
| `perTargetParallelism` | No | `2` | Default maximum concurrent create/delete operations against one host cluster. A target's `parallelism` overrides it. |
| `createTimeout` | No | `10m` | How long each cluster is given to reach `condition=Ready`. Uses Go duration syntax such as `30s`, `10m`, or `1h`. |
| `lbAddressTimeout` | No | `2m` | How long to wait for a LoadBalancer address before continuing without it. Only used when `clusterSpec.expose.loadBalancer` is set. |
| `registerTimeout` | No | `30m` | How long one cluster may take to register with Rancher and report ready. Importing deploys `cattle-cluster-agent` and waits for it to connect, which routinely takes minutes. |
| `artifactsDir` | No | `artifacts` | Root directory for state, generated kubeconfigs, rendered manifests, and per-cluster command logs. |
| `stateFile` | No | `<artifactsDir>/<batch>/state.json` | Persistent lifecycle state. Keep this file until deletion finishes; reruns use it to resume failed work. |
| `k3kcli` | No | `k3kcli` | Executable name in `PATH`, or an explicit path to the k3k CLI binary. |
| `kubectl` | No | `kubectl` | Executable name in `PATH`, or an explicit path to kubectl. |
| `mode` | No | `shared` | **Deprecated.** Alias for `clusterSpec.mode`. |
| `servers` | No | `1` | **Deprecated.** Alias for `clusterSpec.servers`. |
| `agents` | No | `0` | **Deprecated.** Alias for `clusterSpec.agents`. |
| `persistenceType` | No | `ephemeral` | **Deprecated.** Alias for `clusterSpec.persistence.type`. |

The four deprecated fields are folded into `clusterSpec` when the corresponding `clusterSpec` key is absent. Setting both a deprecated field and its `clusterSpec` equivalent is an error.

## Cluster Spec Fields

`clusterSpec` maps directly onto the k3k `Cluster` spec. Field names, shapes, and defaults match the [CRD reference](https://github.com/rancher/k3k/blob/main/docs/crds/crds.md). Unsupported fields are rejected at load time rather than silently ignored.

| Field | Description |
| --- | --- |
| `version` | K3s version, for example `v1.36.2-k3s1`. Defaults to the host node's Kubernetes version. A `+` is rewritten to `-` to form a valid image tag. |
| `mode` | `shared`, `virtual`, or `hcp`. Immutable after creation. |
| `servers` | Number of K3s server pods. Must be at least one. |
| `agents` | Number of K3s agent pods. Supported only with `mode: virtual`. |
| `persistence.type` | `dynamic` or `ephemeral`. |
| `persistence.storageClassName` | StorageClass for the PVC in `dynamic` mode. |
| `persistence.storageRequestSize` | Requested PVC size in `dynamic` mode, for example `30Gi`. Immutable after creation. |
| `expose.ingress` | Expose the API server through an Ingress: `ingressClassName` and `annotations`. |
| `expose.loadBalancer` | Expose the API server through a LoadBalancer Service: `serverPort` and `etcdPort`. |
| `expose.nodePort` | Expose the API server through a NodePort Service: `serverPort` and `etcdPort`. |
| `expose.annotations` | Annotations added to the generated Service. |
| `nodeSelector` | Node labels constraining server and agent placement. In `shared` mode this also applies to workloads. |
| `tlsSANs` | Subject alternative names for the server certificate. |
| `serverArgs` | Extra arguments for server pods, for example `["--tls-san=example.com"]`. |
| `agentArgs` | Extra arguments for agent pods, for example `["--node-name=my-agent-node"]`. |
| `serverEnvs` | Environment variables for server pods, as `name`/`value` pairs. |
| `agentEnvs` | Environment variables for agent pods, as `name`/`value` pairs. |
| `serverLimit` | Resource limits for server pods. |
| `workerLimit` | Resource limits for agent pods. |
| `serverResources` | Limits and requests for server pods. |
| `workerResources` | Limits and requests for agent pods. |
| `serverAffinity` | Node and pod affinity/anti-affinity rules for server pods. |
| `agentAffinity` | Node and pod affinity/anti-affinity rules for agent pods. |

`expose.ingress`, `expose.loadBalancer`, and `expose.nodePort` are mutually exclusive.

### Behavior Worth Knowing

**`expose` defaults to `nodePort`.** If you set no expose mode, k3kbulk adds `expose: {nodePort: {}}`. An unset `expose` produces a ClusterIP Service whose address is not routable from the machine running k3kbulk, so the generated kubeconfig would be unusable. This reproduces what `k3kcli cluster create` did implicitly.

**`tlsSANs[0]` is load bearing.** k3k builds the Ingress rule hosts from `tlsSANs`, and falls back to `tlsSANs[0]` when the resolved Service address is not present in the cluster status. k3kbulk therefore adds the target's `kubeconfigServer` in a mode-dependent position:

| Expose mode | Placement of `kubeconfigServer` |
| --- | --- |
| `nodePort` | Prepended, so `tlsSANs[0]` is the reachable host address. |
| `ingress` | Appended, so your hostname stays at index 0 and remains the Ingress rule host. |
| `loadBalancer` | Appended, so your hostname stays at index 0. |

**LoadBalancer addresses are discovered after the cluster is ready.** The address is allocated by the host cluster, so it cannot be supplied up front. k3kbulk polls the cluster namespace for the k3k server Service, then patches the address into `spec.tlsSANs`. If no address appears within `lbAddressTimeout`, the step is skipped with a warning rather than failing the cluster.

**`serverResources` overrides `serverLimit`.** k3k discards `serverLimit` when `serverResources` is set, so k3kbulk rejects a configuration that sets both. The same applies to `workerLimit` and `workerResources`.

**`serverResources` and `workerResources` need a feature gate.** k3k applies them as pod-level resources, which requires `PodLevelResources` on the host cluster. k3kbulk emits a warning when they are used.

## Template Variables

`clusterSpec` is a Go `text/template` evaluated once per cluster, so one block can produce per-cluster values. This matters whenever a value must be unique across the batch, such as an Ingress hostname or a fixed node port.

| Variable | Example |
| --- | --- |
| `.Name` | `k3k-ci-001` |
| `.Namespace` | `k3k-k3k-ci-001` |
| `.Batch` | `k3k-ci` |
| `.Index` / `.Index0` | `1` / `0` |
| `.Target` | `downstream-0-0` |
| `.TargetType` | `imported` |
| `.KubeconfigServer` | `192.0.2.10` |

Functions: `add`, `sub`, `mul`, `mod`, `lower`, `upper`.

```yaml
clusterSpec:
  tlsSANs: ["{{ .Name }}.k3k.example.com"]
  expose:
    ingress:
      ingressClassName: nginx
```

```yaml
clusterSpec:
  expose:
    nodePort:
      serverPort: {{ add 30000 .Index }}
```

`.Index` is derived from the persisted cluster name, so a resumed run renders an identical manifest. An unknown variable is an error rather than an empty string. Templates are validated against a synthetic first-cluster context when the config is loaded, so mistakes surface immediately.

With `count` greater than one, k3kbulk rejects an `expose.ingress` configuration or fixed node ports that contain no template expression, because every cluster would otherwise claim the same hostname or port.

### Rancher Fields

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `rancher.host` | Yes | None | Rancher hostname, optionally with `https://`. |
| `rancher.token` | Conditional | None | Rancher bearer token in `<token-id>:<secret>` form. Prefer `tokenEnv` so the token is not stored in YAML. |
| `rancher.tokenEnv` | Conditional | None | Environment variable containing the Rancher token. Used when `token` is empty. One of `token` or `tokenEnv` must resolve to a token. |
| `rancher.insecure` | No | `false` | Skip Rancher TLS certificate verification. Use only in controlled test environments. |
| `rancher.caFile` | No | System trust | PEM CA bundle used to verify Rancher. |
| `rancher.workspace` | No | `fleet-default` | Fleet namespace in which imported Rancher provisioning Cluster resources are created. |
| `rancher.versionManagement` | No | `false` | Rancher imported-cluster version management: `false`, `true`, or `system-default`. See below. |

### Version Management Must Stay Off

Rancher classifies a k3k virtual cluster as an imported K3s cluster. When version management is enabled it installs `system-upgrade-controller` into the virtual cluster and creates upgrade `Plan` resources for the cluster's nodes.

A virtual cluster has no real machines to upgrade, so the worker Plan never leaves `Applying`. Rancher then holds the `Upgraded` condition at `Unknown` with `worker node [<name>] being upgraded`, and the cluster never reaches `active` no matter how long you wait — even though `cattle-cluster-agent` connected successfully.

k3kbulk therefore sets `rancher.io/imported-cluster-version-management: "false"` as an annotation on every provisioning Cluster it creates; Rancher copies annotations onto the management cluster, which is where the switch is read. Control the K3s version with `clusterSpec.version` instead.

Existing clusters already stuck this way can be released with:

```bash
kubectl --kubeconfig <rancher-local> annotate cluster.management.cattle.io <c-m-id> \
  rancher.io/imported-cluster-version-management=false --overwrite
```

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
| `discoverTargets.parallelism` | No | `perTargetParallelism` | Concurrent create/delete operation limit applied to every discovered target. |

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
| `targets[].kubeconfig` | Yes | None | Host-cluster kubeconfig used for manifest apply, kubeconfig generation, and deletion verification. |
| `targets[].kubeconfigServer` | Yes | None | Reachable host node, load-balancer IP, or DNS name. It is added to `clusterSpec.tlsSANs` and passed to `k3kcli kubeconfig generate`. |
| `targets[].weight` | No | `1` | Weighted round-robin assignment frequency. |
| `targets[].parallelism` | No | `perTargetParallelism` | Concurrent create/delete operation limit for this target. |

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

### Create Sequence

For each cluster, `create` runs:

1. `kubectl apply -f -` with a rendered `Namespace` and `Cluster` manifest
2. `kubectl wait --for=condition=Ready cluster.k3k.io/<name>`
3. LoadBalancer address discovery and `tlsSANs` patch, only when `expose.loadBalancer` is set
4. `k3kcli kubeconfig generate` to write the cluster's kubeconfig
5. Rancher import, registration manifest apply, and readiness wait

### `generate-config`

Writes an annotated configuration file in which every supported `clusterSpec` field is present: values you supply are written live, and everything else is written as a commented placeholder with its description, default, and permitted values.

| Flag | Default | Description |
| --- | --- | --- |
| `--output` | `config.yaml` | Output path, or `-` for stdout. |
| `--batch` | None | Batch name. Required. |
| `--count` | `1` | Number of k3k clusters. |
| `--parallelism` | `4` | Global workflow limit. |
| `--per-target-parallelism` | `1` | Per-host workflow limit. |
| `--create-timeout` | `10m` | Per-cluster readiness timeout. |
| `--artifacts-dir` | `artifacts` | Artifacts directory. |
| `--rancher-host` | None | Rancher hostname. Required. |
| `--rancher-token-env` | `RANCHER_TOKEN` | Environment variable holding the Rancher token. |
| `--rancher-insecure` | `false` | Skip Rancher TLS verification. |
| `--rancher-ca` | None | Rancher HTTPS CA bundle. |
| `--rancher-workspace` | `fleet-default` | Fleet workspace for imported clusters. |
| `--rancher-version-management` | `false` | Imported-cluster version management: `false`, `true`, or `system-default`. |
| `--targets-json` | None | Path to a JSON array of targets, or `-` for stdin. |
| `--discover-prefix` | None | Discover Rancher hosts with this name prefix. |
| `--discover-type` | None | Discover Rancher hosts of this type. |
| `--discover-address` | `external` | Host node address to use: `external` or `internal`. |
| `--discover-allow-internal-address` | `false` | Fall back to an internal host address. |
| `--discover-weight` | `1` | Assignment weight for discovered targets. |
| `--discover-parallelism` | `1` | Per-target limit for discovered targets. |
| `--mode`, `--servers`, `--agents`, `--version` | None | Corresponding `clusterSpec` values. |
| `--persistence-type`, `--storage-class-name`, `--storage-request-size` | None | Corresponding `clusterSpec.persistence` values. |
| `--spec-file` | None | YAML file supplying any other `clusterSpec` fields. |
| `--set` | None | A `clusterSpec` value as `key=value`, using dotted keys. Repeatable. |

```bash
go run ./validation/pipeline/k3kbulk generate-config \
  --batch k3k-ci --count 4 \
  --rancher-host rancher.example.com \
  --discover-prefix host- --discover-type nodedriver \
  --mode virtual --servers 3 --agents 3 \
  --set 'nodeSelector={"disktype":"ssd"}' \
  --spec-file ./affinity.yaml \
  --output ./config.yaml
```

Values passed through `--set` and `--spec-file` are validated against the supported `clusterSpec` fields, so a typo is reported when the config is generated rather than when a batch is first run.

### Resuming

`create` is idempotent and can be rerun after a partial failure. The state file under `artifacts/<batch>/state.json` records each cluster's stage, resume stage, target assignment, and Rancher IDs, so a rerun only redoes what did not finish.

| Situation on rerun | Behavior |
| --- | --- |
| Cluster reached `registered` | Skipped entirely. |
| Failed while waiting for the Rancher agent | k3k cluster and Rancher import are reused; only the registration manifest is reapplied and the readiness wait retried. |
| Failed during k3k creation | The manifest is reapplied. `kubectl apply` converges, so a half-created cluster is repaired rather than duplicated. |
| Kubeconfig missing but state kept | Create runs again to regenerate it. This is the case when the artifacts directory is cleaned but `state.json` is preserved. |
| Process died after the Rancher cluster was created | The management ID is read back from the state file; if it was never written, the existing provisioning cluster is adopted by name instead of creating a second one. |

Two things to know:

- **Editing `clusterSpec` between runs changes existing clusters.** A rerun applies the current manifest, so an edited spec is pushed to clusters that already exist. Changing an immutable field (`mode`, `clusterCIDR`, `serviceCIDR`, `clusterDNS`, `persistence.storageRequestSize`) makes the apply fail with a CRD validation error. Use a new `batch` name instead.
- **Keep the state file until deletion finishes.** It is the only record of which host each cluster landed on and which Rancher cluster it maps to.

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

The script discovers Dartboard's hosts and then delegates the file itself to
`k3kbulk generate-config`, so the result carries the same annotated
`clusterSpec` block as any other generated config. Cluster spec values can be
supplied directly:

```bash
./validation/pipeline/k3kbulk/generate-config-from-dartboard.sh \
  --config-dir /path/to/tofu/main/aws/my-workspace_config \
  --rancher-host rancher.example.com \
  --batch k3k-ci --count 4 --address internal --ensure-k3k \
  --mode virtual --servers 3 --agents 3 \
  --persistence-type dynamic --storage-request-size 30Gi \
  --set 'nodeSelector={"disktype":"ssd"}' \
  --spec-file ./affinity.yaml \
  --output ./validation/pipeline/k3kbulk/config.yaml
```

Set `K3KBULK` to a prebuilt binary to skip `go run` in CI.

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

## Isolating Failures From k3kbulk

`manual-k3kcli-repro.sh` performs the same sequence for one cluster using only `k3kcli` and `kubectl`, so an environment problem can be told apart from a k3kbulk problem. It prints every command it runs, so the steps can also be pasted individually.

The Rancher import and egress steps are currently commented out in the script, so it stops once the virtual cluster is verified. `--import` and `--rancher-host` are rejected rather than silently ignored. Creation and teardown are separate `create` and `delete` commands, matching k3kbulk's own `create|delete` argument.

### Readiness Is Decided By The Ready Condition, Not By k3kcli

`create` does not trust `k3kcli`'s readiness wait. After `k3kcli cluster create` returns, the script waits on `kubectl wait --for=condition=Ready cluster.k3k.io/<name>`, which is the same check k3kbulk performs in `CLI.WaitReady`.

The reason is a misclassification in k3k. `updateStatus` (`pkg/controller/cluster/status.go`) sets `Phase=Failed` for **any** reconcile error that is neither a validation error nor `k3s.ErrServerNotReady`, and `pkg/k3s/client.go` only translates `ECONNREFUSED` into `ErrServerNotReady`. `ECONNREFUSED` happens while nothing is listening yet; once the server pod is up but slow to answer, the 5-second probe of `/v1-k3s/config` times out instead, and `context deadline exceeded` is not translated. A healthy cluster therefore reports `Failed` for a few seconds precisely because its pod has become Ready. Optimistic-lock conflicts on the Cluster object produce the same effect.

`k3kcli`'s `waitForClusterReady` treats `Failed` as terminal after four observations, and the counter is never reset, so four transient blips anywhere in the wait abort it regardless of the timeout. In one 50-cluster run every cluster was reported as failed and every one was `Ready` 20-96 seconds later, median 66 seconds, against a 10 minute timeout that was never reached.

Consequences for the script:

- `k3kcli`'s exit status is advisory. A non-zero exit prints a note and the run continues to the condition wait.
- `--k3kcli-timeout` still controls `k3kcli`'s own wait, defaulting to `1m`. Raise it to lean on `k3kcli` again; it will still abort early on a transient `Failed` phase whatever the value.
- `--timeout` is the authoritative `condition=Ready` timeout, and is also used for the deletion wait.
- `k3kcli` writes the kubeconfig only after its own wait succeeds, so when it gives up early the script runs `k3kcli kubeconfig generate` to produce the same `<namespace>-<name>-kubeconfig.yaml`.
- If the condition wait really does time out, the script prints the Cluster's conditions and recent warning events before failing, which is where the actual reason lives. Neither `k3kcli` nor the phase string carries it.

```bash
./validation/pipeline/k3kbulk/manual-k3kcli-repro.sh create \
  --host-kubeconfig ./validation/pipeline/k3kbulk/artifacts/<batch>/targets/downstream-0-0.yaml \
  --kubeconfig-server 192.0.2.10 \
  --name k3k-manual-1

./validation/pipeline/k3kbulk/manual-k3kcli-repro.sh delete \
  --host-kubeconfig ./validation/pipeline/k3kbulk/artifacts/<batch>/targets/downstream-0-0.yaml \
  --name k3k-manual-1
```

`--kubeconfig-server` is optional. When it is omitted the flag is not passed to `k3kcli` at all, so `k3kcli` applies its own fallback and uses the hostname from the host kubeconfig's server URL (`extractHost` in `cli/cmds/cluster_create.go`). Supply it when that hostname is not reachable from the machine running the script, or does not expose the virtual cluster's node port. A Dartboard kubeconfig is the case that matters: its API endpoint is an SSH tunnel, and the tunnel does not forward K3k's service port, which is why `generate-config-from-dartboard.sh` looks up a real node address instead of reusing the kubeconfig's own.

`--hold DURATION` keeps the cluster up after verification passes, sleeping before `create` returns, which is useful when the cluster is needed for manual work or for a soak. It accepts a bare number of seconds or Go-style units (`90`, `30s`, `5m`, `1h30m`) and is converted to seconds internally, since BSD `sleep` does not take unit suffixes. The default of `0` skips the step entirely. Interrupting the hold leaves the cluster running; nothing is torn down until `delete` is run.

`delete` runs the same `k3kcli cluster delete` and `kubectl wait --for=delete` pair as `CLI.Delete` and `CLI.WaitDeleted` in `actions/k3k/command.go`. It is idempotent: a cluster that is already gone reports so and exits zero. Like k3kbulk, it leaves the `k3k-<name>` namespace in place and prints the commands to remove it and the local working directory.

The checks are ordered so the first failure identifies the layer:

| Command | Step | A failure here means |
| --- | --- | --- |
| `create` | 0 | The host cluster is unreachable, or k3k is not installed on it. |
| `create` | 1-2 | k3k itself cannot provision the virtual cluster. |
| `create` | 3 | The virtual cluster API is not reachable from this machine. On a Harvester-hosted downstream this is usually the VPN. |
| `delete` | 1-2 | The cluster was created but cannot be removed; check the k3k controller. |

The commented-out blocks at the end of the script still hold the former steps 4-6, which checked egress to Rancher, created the imported cluster, and watched `cattle-cluster-agent` connect.

## Bulk Batches Without Rancher

`manual-k3kcli-bulk.sh` runs `manual-k3kcli-repro.sh` over many clusters, in batches. It is the Rancher-free equivalent of a k3kbulk run: no config file, no state file, no registration. It reimplements none of the per-cluster work, so the two scripts cannot drift apart.

Given a target of `T` clusters (`--count`) and a batch size of `N` (`--batch-size`), the run performs `ceil(T / N)` batches. Every cluster in a batch starts together and the batch finishes before the next one starts, so the host is never asked for more than `N` clusters at a time. The final batch is short when `T` is not a multiple of `N`, and exactly `T` clusters are worked on overall, never more.

```bash
# 50 clusters, 10 at a time, two minutes for the host to settle after each batch
./validation/pipeline/k3kbulk/manual-k3kcli-bulk.sh create \
  --host-kubeconfig ./host.yaml --kubeconfig-server 192.0.2.10 \
  --name k3k-soak --count 50 --batch-size 10 --hold 2m

./validation/pipeline/k3kbulk/manual-k3kcli-bulk.sh delete \
  --host-kubeconfig ./host.yaml --name k3k-soak --count 50 --batch-size 10
```

Cluster names use k3kbulk's own `<name>-001` scheme (`state.go:82`), so a run here carries the names a k3kbulk run would have used.

| Field | Default | Description |
| --- | --- | --- |
| `--name` | `k3k-manual` | Cluster name prefix. Validated as a DNS label of at most 58 characters, the same rule k3kbulk applies to `batch`. |
| `--count` | `1` | Target number of clusters overall, the `T` above. |
| `--batch-size` | `10` | Clusters per batch, all started together, the `N` above. |
| `--start-index` | `1` | First numeric suffix, for adding a further wave to an existing run without renaming it. |
| `--hold` | `0` | Sleep this long after each batch finishes. Batches that did no work are not held. |
| `--skip-done` | off | Skip clusters already in the requested state: on create the ones that exist, on delete the ones that do not. A batch that was entirely skipped is not held. |
| `--k3kcli-timeout` | `1m` | Per-cluster timeout for `k3kcli`'s own advisory wait. |
| `--dry-run` | off | Print the batch plan and the per-cluster commands, run nothing. |
| `--logs-dir` | `./bulk-<name>-<command>-logs` | Per-cluster logs. |
| `--repro` | alongside this script | Path to the single-cluster script. |

`--host-kubeconfig`, `--kubeconfig-server`, `--mode`, `--servers`, `--agents`, `--persistence-type`, `--timeout`, `--kubectl`, and `--k3kcli` are forwarded to the single-cluster script, and only when set, so that script keeps ownership of every default. `--kubeconfig-server` is optional here for the same reason it is optional there: unset means `k3kcli` derives the address from the host kubeconfig.

### Behavior Worth Knowing

**The batch is the throttle.** There is no separate parallelism setting: `--batch-size` is both how many clusters run at once and where the run checkpoints. Overloading the host is the documented way these batches fail, with k3s pods crashing, respawning, and cascading. The 5000-cluster Fleet experiment landed on roughly 50 vclusters per `c5d.9xlarge` node, creating 50 at a time and waiting for the last one before starting more.

**`--hold` is per batch, including the last one.** The pause is there to let the host settle before the next batch lands on it, so it is applied uniformly rather than being conditional on where a batch falls. Total hold time is therefore `ceil(T / N) × hold`; `--dry-run` prints the resulting plan.

**A batch that did no work is not held.** With `--skip-done`, a batch in which every cluster already existed has nothing for the host to settle from, so its hold is skipped and the summary reports how many holds were dropped. This is what keeps resuming a large run cheap: a resume that only has to repair a handful of clusters pays the hold for those batches alone instead of the whole `ceil(T / N) × hold` budget. A batch with even one real create still holds, and so does a batch whose clusters all failed, because the host did the work either way.

**Exactly `--count` clusters are worked on.** The names are generated once, up front, and the batch loop indexes into that list, so uneven divisions produce a short final batch rather than an overshoot. The script asserts the total at the end and fails loudly if it ever disagrees with the target.

**Failures do not stop the run.** Every cluster in every batch is attempted. Each one's full output goes to `<logs-dir>/<name>.log`, the console gets one line per cluster plus a per-batch tally, and the final summary lists the failures. The exit status is non-zero if there were any.

**Resuming a partial run is the normal case, in both directions.** `--skip-done` skips whatever is already in the state the command would produce: on `create` the clusters that exist, on `delete` the clusters that do not. Re-running either command therefore only touches what is left to do.

The check runs in the wrapper, so a skipped cluster costs a single API call rather than a whole single-cluster invocation, and it is reported as `Skipped` rather than inflating the success count. `delete` remains safe without the flag, because the single-cluster script exits zero when the cluster is already gone; the flag makes that case cheap and visible instead of merely harmless.

The flag name reads oddly for `delete`, where "existing" is the thing being skipped over rather than the thing skipped. It is kept for symmetry with `create` and with k3kbulk's resume behaviour.

**Interrupting kills the whole tree.** The script enables job control so each worker is its own process group, and the signal handler signals those groups. Without that, an interrupt would reap the worker subshells and strand every `k3kcli` and `kubectl` process beneath them.

## Artifacts And State

For each cluster, the tool writes the rendered `cluster.yaml`, its generated kubeconfig, and `apply.log`, `wait-ready.log`, `lb-sans.log`, `kubeconfig.log`, `register.log`, `delete.log`, and `wait-delete.log` as applicable. Files are grouped under `<artifactsDir>/<batch>/<cluster-name>/`.

`cluster.yaml` is the exact manifest that was applied, with templates resolved and k3kbulk's `expose` and `tlsSANs` adjustments in place, so a failed cluster can be reproduced by hand.

The state file records host assignment, target type, k3k namespace, artifact paths, the Rancher provisioning ID (`namespace/name`), the Rancher management ID (`c-m-*`), current lifecycle stage, resume stage, and last error. It is written atomically with mode `0600`.
