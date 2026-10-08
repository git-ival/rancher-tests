#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# validation/pipeline/k3kbulk -> repository root
repo_root=$(cd "$script_dir/../../.." && pwd)

usage() {
    cat <<'EOF'
Usage: generate-config-from-dartboard.sh [options]

Required:
  --config-dir DIR        Dartboard <workspace>_config directory
  --rancher-host HOST     Rancher URL or hostname reachable by K3k clusters
  --batch NAME            K3k cluster name prefix
  --count NUMBER          Number of K3k clusters to create

Options:
  --output FILE           Generated config path (default: ./config.yaml)
  --token-env NAME        Rancher token environment variable (default: RANCHER_TOKEN)
  --rancher-insecure      Disable Rancher TLS verification
  --rancher-ca FILE       Rancher HTTPS CA bundle
  --parallelism NUMBER    Global workflow limit (default: 4)
  --target-parallelism N  Per-host workflow limit (default: 1)
  --address TYPE          Host node address: external or internal (default: external)
  --allow-internal-address
                          Fall back to an internal address when external is absent
  --ensure-k3k            Install or upgrade K3k on every selected host
  --k3k-version VERSION   K3k Helm chart version (default: 1.1.0)
  --helm PATH             Helm executable (default: helm)
  --kubectl PATH          kubectl executable (default: kubectl)
  --install-timeout TIME  Helm and readiness timeout (default: 10m)
  -h, --help              Show this help

Cluster spec options (all optional; unset fields are written commented out):
  --mode MODE             clusterSpec.mode: shared, virtual, or hcp
  --servers NUMBER        clusterSpec.servers
  --agents NUMBER         clusterSpec.agents (virtual mode only)
  --k3s-version VERSION   clusterSpec.version, e.g. v1.36.2-k3s1
  --persistence-type TYPE clusterSpec.persistence.type: dynamic or ephemeral
  --storage-class-name N  clusterSpec.persistence.storageClassName
  --storage-request-size S
                          clusterSpec.persistence.storageRequestSize, e.g. 30Gi
  --spec-file FILE        YAML file supplying any other clusterSpec fields
  --set KEY=VALUE         clusterSpec value using a dotted key, repeatable

The script selects downstream-*.yaml kubeconfigs, embeds no credentials in the
generated file, and queries each host cluster for a reachable node address. Use
--ensure-k3k in the CI step immediately before k3kbulk create.

The configuration file itself is written by "k3kbulk generate-config", so every
supported clusterSpec field appears in the output: values supplied here are
written live, and everything else is written as a commented placeholder.
EOF
}

die() {
    echo "ERROR: $*" >&2
    exit 1
}

json_quote() {
    local value=$1
    value=${value//\\/\\\\}
    value=${value//\"/\\\"}
    printf '"%s"' "$value"
}

absolute_path() {
    local path=$1
    local dir
    dir=$(cd "$(dirname "$path")" && pwd)
    printf '%s/%s' "$dir" "$(basename "$path")"
}

positive_integer() {
    [[ $2 =~ ^[1-9][0-9]*$ ]] || die "$1 must be a positive integer"
}

config_dir=
rancher_host=
batch=
count=
output=./config.yaml
token_env=RANCHER_TOKEN
rancher_insecure=false
rancher_ca=
parallelism=4
target_parallelism=1
address=external
allow_internal_address=false
ensure_k3k=false
k3k_version=1.1.0
helm=helm
kubectl=kubectl
install_timeout=10m
mode=
servers=
agents=
k3s_version=
persistence_type=
storage_class_name=
storage_request_size=
spec_file=
declare -a spec_sets=()

while [[ $# -gt 0 ]]; do
    case $1 in
        --config-dir) config_dir=${2:?}; shift 2 ;;
        --mode) mode=${2:?}; shift 2 ;;
        --servers) servers=${2:?}; shift 2 ;;
        --agents) agents=${2:?}; shift 2 ;;
        --k3s-version) k3s_version=${2:?}; shift 2 ;;
        --persistence-type) persistence_type=${2:?}; shift 2 ;;
        --storage-class-name) storage_class_name=${2:?}; shift 2 ;;
        --storage-request-size) storage_request_size=${2:?}; shift 2 ;;
        --spec-file) spec_file=${2:?}; shift 2 ;;
        --set) spec_sets+=("${2:?}"); shift 2 ;;
        --rancher-host) rancher_host=${2:?}; shift 2 ;;
        --batch) batch=${2:?}; shift 2 ;;
        --count) count=${2:?}; shift 2 ;;
        --output) output=${2:?}; shift 2 ;;
        --token-env) token_env=${2:?}; shift 2 ;;
        --rancher-insecure) rancher_insecure=true; shift ;;
        --rancher-ca) rancher_ca=${2:?}; shift 2 ;;
        --parallelism) parallelism=${2:?}; shift 2 ;;
        --target-parallelism) target_parallelism=${2:?}; shift 2 ;;
        --address) address=${2:?}; shift 2 ;;
        --allow-internal-address) allow_internal_address=true; shift ;;
        --ensure-k3k) ensure_k3k=true; shift ;;
        --k3k-version) k3k_version=${2:?}; shift 2 ;;
        --helm) helm=${2:?}; shift 2 ;;
        --kubectl) kubectl=${2:?}; shift 2 ;;
        --install-timeout) install_timeout=${2:?}; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option: $1" ;;
    esac
done

[[ -n $config_dir ]] || die "--config-dir is required"
[[ -d $config_dir ]] || die "Dartboard config directory does not exist: $config_dir"
[[ -n $rancher_host ]] || die "--rancher-host is required"
[[ -n $batch ]] || die "--batch is required"
[[ $batch =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && ${#batch} -le 58 ]] || die "--batch must be a DNS label no longer than 58 characters"
[[ -n $count ]] || die "--count is required"
positive_integer "--count" "$count"
positive_integer "--parallelism" "$parallelism"
positive_integer "--target-parallelism" "$target_parallelism"
[[ $address == external || $address == internal ]] || die "--address must be external or internal"
command -v "$kubectl" >/dev/null 2>&1 || die "kubectl executable not found: $kubectl"
if [[ $ensure_k3k == true ]]; then
    command -v "$helm" >/dev/null 2>&1 || die "Helm executable not found: $helm"
fi
if [[ -n $rancher_ca ]]; then
    [[ -f $rancher_ca ]] || die "Rancher CA file does not exist: $rancher_ca"
    rancher_ca=$(realpath "$rancher_ca")
fi

config_dir=$(realpath "$config_dir")
mkdir -p "$(dirname "$output")"
output=$(absolute_path "$output")
mapfile -t kubeconfigs < <(compgen -G "$config_dir/downstream-*.yaml" | sort || true)
[[ ${#kubeconfigs[@]} -gt 0 ]] || die "no downstream-*.yaml kubeconfigs found in $config_dir"

declare -a target_names=()
declare -a target_servers=()
for kubeconfig in "${kubeconfigs[@]}"; do
    kubeconfig=$(realpath "$kubeconfig")
    name=$(basename "$kubeconfig" .yaml)
    addresses=$(
        "$kubectl" --kubeconfig "$kubeconfig" get nodes \
            -o 'jsonpath={range .items[*]}{.metadata.name}{"\t"}{range .status.addresses[*]}{.type}{"="}{.address}{","}{end}{"\n"}{end}'
    )
    server=$(printf '%s\n' "$addresses" | sort | sed -n "s/.*${address^}IP=\([^,]*\).*/\1/p" | head -n 1)
    if [[ -z $server && $address == external && $allow_internal_address == true ]]; then
        server=$(printf '%s\n' "$addresses" | sort | sed -n 's/.*InternalIP=\([^,]*\).*/\1/p' | head -n 1)
    fi
    [[ -n $server ]] || die "no $address node address is available in $kubeconfig"

    if [[ $ensure_k3k == true ]]; then
        echo "Ensuring K3k $k3k_version is ready on $name" >&2
        "$helm" repo add k3k https://rancher.github.io/k3k --force-update || die "helm repo add failed"
        "$helm" repo update k3k || die "helm repo update failed"
        "$helm" upgrade --install k3k k3k/k3k \
            --version "$k3k_version" \
            --namespace k3k-system \
            --create-namespace \
            --kubeconfig "$kubeconfig" \
            --wait \
            --timeout "$install_timeout"
        "$kubectl" --kubeconfig "$kubeconfig" wait \
            --for=condition=Established crd/clusters.k3k.io \
            --timeout="$install_timeout"
        "$kubectl" --kubeconfig "$kubeconfig" --namespace k3k-system \
            rollout status deployment/k3k --timeout="$install_timeout"
    fi

    target_names+=("$name")
    target_servers+=("$server")
done

umask 077

targets_json=$(
    {
        printf '['
        for i in "${!kubeconfigs[@]}"; do
            [[ $i -gt 0 ]] && printf ','
            printf '{"name":%s,"type":"imported","kubeconfig":%s,"kubeconfigServer":%s,"weight":1,"parallelism":%s}' \
                "$(json_quote "${target_names[$i]}")" \
                "$(json_quote "$(realpath "${kubeconfigs[$i]}")")" \
                "$(json_quote "${target_servers[$i]}")" \
                "$target_parallelism"
        done
        printf ']'
    }
)

# The configuration file is emitted by the Go generator so that every supported clusterSpec
# field is present, live when supplied here and commented out otherwise.
declare -a generate_args=(
    --batch "$batch"
    --count "$count"
    --parallelism "$parallelism"
    --per-target-parallelism "$target_parallelism"
    --create-timeout 10m
    --artifacts-dir artifacts
    --rancher-host "$rancher_host"
    --rancher-token-env "$token_env"
    --rancher-workspace fleet-default
    --targets-json -
    --output "$output"
)
[[ $rancher_insecure == true ]] && generate_args+=(--rancher-insecure)
[[ -n $rancher_ca ]] && generate_args+=(--rancher-ca "$rancher_ca")
[[ -n $mode ]] && generate_args+=(--mode "$mode")
[[ -n $servers ]] && generate_args+=(--servers "$servers")
[[ -n $agents ]] && generate_args+=(--agents "$agents")
[[ -n $k3s_version ]] && generate_args+=(--version "$k3s_version")
[[ -n $persistence_type ]] && generate_args+=(--persistence-type "$persistence_type")
[[ -n $storage_class_name ]] && generate_args+=(--storage-class-name "$storage_class_name")
[[ -n $storage_request_size ]] && generate_args+=(--storage-request-size "$storage_request_size")
[[ -n $spec_file ]] && generate_args+=(--spec-file "$spec_file")
for pair in "${spec_sets[@]+"${spec_sets[@]}"}"; do
    generate_args+=(--set "$pair")
done

if [[ -n ${K3KBULK:-} ]]; then
    printf '%s' "$targets_json" | "$K3KBULK" generate-config "${generate_args[@]}"
else
    printf '%s' "$targets_json" | (cd "$repo_root" && go run ./validation/pipeline/k3kbulk generate-config "${generate_args[@]}")
fi

echo "Wrote $output with ${#kubeconfigs[@]} Dartboard downstream target(s)" >&2
