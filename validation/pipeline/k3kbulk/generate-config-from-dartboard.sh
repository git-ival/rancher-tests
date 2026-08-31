#!/usr/bin/env bash

set -euo pipefail

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

The script selects downstream-*.yaml kubeconfigs, embeds no credentials in the
generated file, and queries each host cluster for a reachable node address. Use
--ensure-k3k in the CI step immediately before k3kbulk create.
EOF
}

die() {
    echo "ERROR: $*" >&2
    exit 1
}

yaml_quote() {
    local value=${1//\'/\'\'}
    printf "'%s'" "$value"
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

while [[ $# -gt 0 ]]; do
    case $1 in
        --config-dir) config_dir=${2:?}; shift 2 ;;
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
{
    echo "batch: $(yaml_quote "$batch")"
    echo "count: $count"
    echo "parallelism: $parallelism"
    echo "perTargetParallelism: $target_parallelism"
    echo "mode: shared"
    echo "servers: 1"
    echo "agents: 0"
    echo "persistenceType: ephemeral"
    echo "createTimeout: 10m"
    echo "artifactsDir: artifacts"
    echo
    echo "rancher:"
    echo "  host: $(yaml_quote "$rancher_host")"
    echo "  tokenEnv: $(yaml_quote "$token_env")"
    echo "  insecure: $rancher_insecure"
    if [[ -n $rancher_ca ]]; then
        echo "  caFile: $(yaml_quote "$rancher_ca")"
    fi
    echo "  workspace: fleet-default"
    echo
    echo "targets:"
    for i in "${!kubeconfigs[@]}"; do
        echo "  - name: $(yaml_quote "${target_names[$i]}")"
        echo "    type: imported"
        echo "    kubeconfig: $(yaml_quote "$(realpath "${kubeconfigs[$i]}")")"
        echo "    kubeconfigServer: $(yaml_quote "${target_servers[$i]}")"
        echo "    weight: 1"
        echo "    parallelism: $target_parallelism"
    done
} >"$output"

echo "Wrote $output with ${#kubeconfigs[@]} Dartboard downstream target(s)" >&2
