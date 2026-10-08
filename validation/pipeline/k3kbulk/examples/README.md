# k3kbulk Example Configurations

Runnable configurations covering the supported `clusterSpec` surface. Each file is validated by
`TestShippedExamplesAreValid` in `actions/k3k/examples_test.go`, which loads it and renders a
manifest for every cluster in its batch.

Replace `rancher.example.com`, the host prefixes, and the kubeconfig paths before use.

| File | Covers |
| --- | --- |
| [01-basic-shared.yaml](01-basic-shared.yaml) | Shared mode, ephemeral storage, Rancher host discovery, default NodePort expose |
| [02-ha-virtual.yaml](02-ha-virtual.yaml) | Virtual mode, 3 servers and 3 agents, pinned K3s version, dynamic persistence, resource limits, explicit weighted hosts |
| [03-ingress-templated.yaml](03-ingress-templated.yaml) | Ingress expose with a distinct hostname per cluster |
| [04-loadbalancer.yaml](04-loadbalancer.yaml) | LoadBalancer expose and post-readiness address discovery |
| [05-scheduling-and-resources.yaml](05-scheduling-and-resources.yaml) | Node selector, affinity and anti-affinity, pod-level resources, server and agent args and envs, templated node ports |

## Running

All commands are run from the repository root.

```bash
export RANCHER_TOKEN='token-id:secret'
```

### Create and delete a batch

```bash
go run ./validation/pipeline/k3kbulk \
  --config ./validation/pipeline/k3kbulk/examples/01-basic-shared.yaml \
  create

go run ./validation/pipeline/k3kbulk \
  --config ./validation/pipeline/k3kbulk/examples/01-basic-shared.yaml \
  delete
```

A partially failed `create` can be rerun; existing K3k and Rancher resources are reused. Keep
`artifacts/<batch>/state.json` until `delete` has finished.

### Longer batches

`--timeout` bounds target discovery plus the whole create or delete operation, and defaults to
`45m`. Raise it for larger batches:

```bash
go run ./validation/pipeline/k3kbulk \
  --config ./validation/pipeline/k3kbulk/examples/05-scheduling-and-resources.yaml \
  --timeout 2h \
  create
```

### Check a configuration without touching infrastructure

Loading validates the `clusterSpec` template, the schema, expose mutual exclusion, conflicting
resource fields, and per-cluster uniqueness. A batch that would have every cluster claim the
same Ingress host or node port is rejected here rather than halfway through a run.

```bash
go vet ./validation/pipeline/k3kbulk/
go test ./k3k/... -run TestShippedExamplesAreValid   # from ./actions
```

### Generate a new configuration

```bash
go run ./validation/pipeline/k3kbulk generate-config \
  --batch k3k-ci --count 4 \
  --rancher-host rancher.example.com \
  --discover-prefix downstream- --discover-type imported \
  --mode virtual --servers 3 --agents 3 \
  --persistence-type dynamic --storage-request-size 30Gi \
  --output ./validation/pipeline/k3kbulk/config.yaml
```

Fields you do not supply are written as commented placeholders, so the generated file doubles
as a reference. Anything the flags do not cover goes in `--spec-file` or `--set`:

```bash
go run ./validation/pipeline/k3kbulk generate-config \
  --batch k3k-ci --count 4 \
  --rancher-host rancher.example.com \
  --discover-prefix downstream- \
  --set 'nodeSelector={"disktype":"ssd"}' \
  --set 'tlsSANs=["{{ .Name }}.k3k.example.com"]' \
  --set 'expose.ingress.ingressClassName=nginx' \
  --spec-file ./validation/pipeline/k3kbulk/examples/05-scheduling-and-resources.yaml \
  --output ./config.yaml
```

Write to stdout with `--output -` to inspect the result without creating a file.

### Generate from Dartboard output

```bash
./validation/pipeline/k3kbulk/generate-config-from-dartboard.sh \
  --config-dir /path/to/tofu/main/aws/my-workspace_config \
  --rancher-host rancher.example.com \
  --batch k3k-ci --count 4 \
  --address internal \
  --ensure-k3k \
  --mode virtual --servers 3 --agents 3 \
  --output ./validation/pipeline/k3kbulk/config.yaml
```

`--ensure-k3k` installs or upgrades K3k on every selected host first. Set `K3KBULK` to a
prebuilt binary to skip `go run` in CI.

## Per-Cluster Values

One `clusterSpec` block is shared by every cluster in a batch, so any value that must be unique
has to be templated. Available variables are `.Name`, `.Namespace`, `.Batch`, `.Index`,
`.Index0`, `.Target`, `.TargetType`, and `.KubeconfigServer`; functions are `add`, `sub`, `mul`,
`mod`, `lower`, and `upper`.

```yaml
clusterSpec:
  tlsSANs: ["{{ .Name }}.k3k.example.com"]   # k3k-ing-001.k3k.example.com, k3k-ing-002...
  expose:
    nodePort:
      serverPort: {{ add 30400 .Index }}     # 30401, 30402, ...
```

Template expressions do not need to be quoted, including in numeric fields. `.Index` comes from
the persisted cluster name, so a resumed run renders an identical manifest.

## Inspecting What Was Applied

Each cluster's directory under `artifacts/<batch>/<cluster-name>/` holds the resolved
`cluster.yaml` that was applied, the generated kubeconfig, and the command logs
(`apply.log`, `wait-ready.log`, `lb-sans.log`, `kubeconfig.log`, `register.log`, `delete.log`,
`wait-delete.log`).

```bash
cat artifacts/k3k-basic/k3k-basic-001/cluster.yaml
export KUBECONFIG=$PWD/artifacts/k3k-basic/k3k-basic-001/k3k-k3k-basic-001-k3k-basic-001-kubeconfig.yaml
kubectl get nodes
```
