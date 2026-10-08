#!/usr/bin/env bash
#
# Reproduce, with k3kcli and kubectl only, what k3kbulk does for a single cluster.
#
# Use this to decide whether a failure lives in k3kbulk or in the environment. Every step
# prints the command it runs, so each one can also be pasted individually.
#
# Rancher import is currently disabled in this script; the steps below stop at the point
# where the virtual cluster is verified. See the commented-out blocks at the end of the
# file for the import and registration sequence.
#
# create, in the same order k3kbulk performs it:
#   0. check the host cluster is reachable and k3k is installed
#   1. create the k3k virtual cluster with k3kcli, or apply a Cluster manifest
#   2. wait for the Cluster to report condition=Ready (authoritative)
#   3. inspect the Cluster object and obtain its kubeconfig
#   4. check the virtual cluster answers and has a Ready node
#   5. optionally hold the cluster up for a while (--hold)
#
# Step 2 is deliberately not left to k3kcli. k3kcli's wait aborts as soon as the Cluster
# phase reads Failed, and k3k sets that phase for any transient reconcile error, so a
# healthy cluster that is merely slow to start is reported as a failure. Waiting on the
# Ready condition instead, as k3kbulk does, tolerates those blips. --k3kcli-timeout still
# controls k3kcli's own wait for anyone who wants it back.
#
# --cluster-manifest replaces the k3kcli create call in step 1 with `kubectl apply` of a
# Cluster manifest, for cluster shapes k3kcli cannot express (per-role resources, agent
# counts in shared mode, and anything else the Cluster CRD supports but k3kcli does not
# expose a flag for). --servers, --agents, --mode and --persistence-type are then owned by
# the manifest and rejected if also passed on the command line. The manifest is a plain
# Cluster YAML with three placeholders substituted before apply:
#   __K3K_NAME__       cluster name
#   __K3K_NAMESPACE__  k3k-<name>
#   __K3K_TLS_SAN__    --kubeconfig-server, else the host kubeconfig's own server hostname
# See dartboard's scripts/k3kbulk_render_spec.py for a tool that renders one of these
# manifests from a higher-level profile.
#
# delete, mirroring k3kbulk's teardown:
#   0. check the host cluster is reachable
#   1. delete the k3k virtual cluster
#   2. wait for the Cluster object to disappear
#   3. delete its k3k-<name> namespace and wait for it to disappear
#
# Examples:
#   ./manual-k3kcli-repro.sh create \
#     --host-kubeconfig ./artifacts/k3k-ci/targets/downstream-0-0.yaml \
#     --kubeconfig-server 192.168.86.48 \
#     --name k3k-manual-1 --hold 30m
#
#   ./manual-k3kcli-repro.sh delete \
#     --host-kubeconfig ./artifacts/k3k-ci/targets/downstream-0-0.yaml \
#     --name k3k-manual-1

set -euo pipefail

command=
name=k3k-manual-1
host_kubeconfig=
kubeconfig_server=
rancher_host=
mode=shared
servers=1
agents=0
persistence_type=ephemeral
timeout=10m
k3kcli_timeout=1m
hold=0
kubectl=kubectl
k3kcli=k3kcli
do_import=false
keep=false
cluster_manifest=

# Tracks which k3kcli-spec flags the caller passed explicitly, as opposed to left at their
# default. --cluster-manifest owns the cluster spec, so it conflicts with these only when
# the caller actually set them, not merely because a default happens to be in scope.
mode_explicit=false
servers_explicit=false
agents_explicit=false
persistence_explicit=false

usage() {
    # Print the header comment, stopping at the first line that is not a comment, so the
    # help text cannot drift out of sync with the header.
    awk 'NR>=3 && /^#/ { sub(/^# ?/, ""); print; next } NR>=3 { exit }' "$0"
    cat <<'EOF'

Usage: manual-k3kcli-repro.sh create|delete [options]

Options:
  --host-kubeconfig FILE   Kubeconfig for the k3k host cluster (required)
  --kubeconfig-server ADDR Reachable host address for the virtual cluster API. Optional;
                           when unset, k3kcli uses the host kubeconfig's own server
                           hostname. Set it when that address is not reachable or does
                           not expose the node port, such as an SSH-tunnelled endpoint
  --name NAME              Virtual cluster name (default: k3k-manual-1)
  --mode MODE              shared, virtual, or hcp (default: shared)
  --servers N              Number of servers (default: 1)
  --agents N               Number of agents, virtual mode only (default: 0)
  --persistence-type TYPE  dynamic or ephemeral (default: ephemeral)
  --cluster-manifest FILE  Apply this Cluster manifest instead of using k3kcli to create
                           the cluster. Owns the cluster spec, so it conflicts with
                           --servers, --agents, --mode and --persistence-type. See the
                           header comment for the placeholders it supports
  --timeout DURATION       Authoritative condition=Ready timeout, also used for the
                           deletion wait (default: 10m)
  --k3kcli-timeout DUR     Timeout handed to k3kcli's own wait during cluster create.
                           Its result is advisory; step 2 decides. Raise it to lean on
                           k3kcli again, and note it aborts early on a transient Failed
                           phase regardless of the value (default: 1m)
  --hold DURATION          After create verifies the cluster, sleep this long before
                           finishing, leaving the cluster up. 0 disables (default: 0)
  --keep                   Do not print the trailing teardown/cleanup instructions
  --kubectl PATH           kubectl executable (default: kubectl)
  --k3kcli PATH            k3kcli executable (default: k3kcli)

Durations accept a bare number of seconds or Go-style units, for example 90, 30s,
5m, or 1h30m.
EOF
}

die() { echo "ERROR: $*" >&2; exit 1; }

# host_from_kubeconfig extracts the API server hostname from a kubeconfig's current-context
# server URL, mirroring k3kcli's own fallback (extractHost in cli/cmds/cluster_create.go),
# which uses url.Parse(...).Hostname() on the REST config host. Only used by
# --cluster-manifest, since k3kcli derives this itself otherwise.
host_from_kubeconfig() {
    local kcfg=$1 server
    server=$("$kubectl" --kubeconfig "$kcfg" config view --minify -o jsonpath='{.clusters[0].cluster.server}') || return 1
    [[ -n $server ]] || return 1
    server=${server#*://}
    server=${server%%/*}
    server=${server%%:*}
    [[ -n $server ]] || return 1
    echo "$server"
}

step() {
    echo
    echo "=============================================================================="
    echo "STEP $*"
    echo "=============================================================================="
}

run() {
    echo "\$ $*"
    "$@"
}

# parse_duration converts a bare number of seconds or a Go-style duration such as 30s,
# 5m, or 1h30m into whole seconds. BSD sleep does not accept unit suffixes, so the
# conversion happens here rather than being passed through to sleep.
parse_duration() {
    local input=$1 total=0 rest value unit

    [[ -n $input ]] || { echo 0; return 0; }
    if [[ $input =~ ^[0-9]+$ ]]; then
        echo "$input"
        return 0
    fi

    rest=$input
    while [[ -n $rest ]]; do
        [[ $rest =~ ^([0-9]+)([smh])(.*)$ ]] || return 1
        value=${BASH_REMATCH[1]}
        unit=${BASH_REMATCH[2]}
        rest=${BASH_REMATCH[3]}
        case $unit in
            s) total=$(( total + value )) ;;
            m) total=$(( total + value * 60 )) ;;
            h) total=$(( total + value * 3600 )) ;;
        esac
    done
    echo "$total"
}

while [[ $# -gt 0 ]]; do
    case $1 in
        create|delete)
            [[ -z $command ]] || die "command already given as '$command', got '$1'"
            command=$1; shift ;;
        --host-kubeconfig) host_kubeconfig=${2:?}; shift 2 ;;
        --kubeconfig-server) kubeconfig_server=${2:?}; shift 2 ;;
        --rancher-host) rancher_host=${2:?}; shift 2 ;;
        --name) name=${2:?}; shift 2 ;;
        --mode) mode=${2:?}; mode_explicit=true; shift 2 ;;
        --servers) servers=${2:?}; servers_explicit=true; shift 2 ;;
        --agents) agents=${2:?}; agents_explicit=true; shift 2 ;;
        --persistence-type) persistence_type=${2:?}; persistence_explicit=true; shift 2 ;;
        --cluster-manifest) cluster_manifest=${2:?}; shift 2 ;;
        --timeout) timeout=${2:?}; shift 2 ;;
        --k3kcli-timeout) k3kcli_timeout=${2:?}; shift 2 ;;
        --hold) hold=${2:?}; shift 2 ;;
        --import) do_import=true; shift ;;
        --keep) keep=true; shift ;;
        --kubectl) kubectl=${2:?}; shift 2 ;;
        --k3kcli) k3kcli=${2:?}; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option: $1" ;;
    esac
done

if [[ -z $command ]]; then
    usage >&2
    echo >&2
    die "a command is required: create or delete"
fi

[[ -n $host_kubeconfig ]] || die "--host-kubeconfig is required"
[[ -f $host_kubeconfig ]] || die "host kubeconfig does not exist: $host_kubeconfig"

if [[ -n $cluster_manifest ]]; then
    [[ -f $cluster_manifest ]] || die "cluster manifest does not exist: $cluster_manifest"

    conflicts=()
    [[ $mode_explicit == true ]] && conflicts+=(--mode)
    [[ $servers_explicit == true ]] && conflicts+=(--servers)
    [[ $agents_explicit == true ]] && conflicts+=(--agents)
    [[ $persistence_explicit == true ]] && conflicts+=(--persistence-type)
    if (( ${#conflicts[@]} > 0 )); then
        die "--cluster-manifest owns the cluster spec; remove: ${conflicts[*]}"
    fi
fi

# Rancher import is commented out below, so accepting these flags silently would produce
# a cluster that was never imported while appearing to have succeeded.
if [[ $do_import == true ]]; then
    die "--import is disabled: the Rancher import steps are commented out in this script"
fi
if [[ -n $rancher_host ]]; then
    die "--rancher-host is disabled: the Rancher steps are commented out in this script"
fi

hold_seconds=$(parse_duration "$hold") || die "--hold is not a valid duration: $hold"

host_kubeconfig=$(cd "$(dirname "$host_kubeconfig")" && pwd)/$(basename "$host_kubeconfig")
namespace="k3k-${name}"
workdir="./manual-${name}"
virtual_kubeconfig="$workdir/${namespace}-${name}-kubeconfig.yaml"

echo "Command         : $command"
echo "Virtual cluster : $name"
echo "Namespace       : $namespace"
echo "Host kubeconfig : $host_kubeconfig"
echo "Working dir     : $workdir"
if [[ $command == create ]]; then
    if [[ -n $kubeconfig_server ]]; then
        echo "API address     : $kubeconfig_server"
    elif [[ -n $cluster_manifest ]]; then
        echo "API address     : (unset, derived from the host kubeconfig)"
    else
        echo "API address     : (unset, k3kcli derives it from the host kubeconfig)"
    fi
    if [[ -n $cluster_manifest ]]; then
        echo "Cluster manifest: $cluster_manifest"
    fi
    if (( hold_seconds > 0 )); then
        echo "Hold            : ${hold} (${hold_seconds}s)"
    fi
fi

do_create() {
    mkdir -p "$workdir"

    # The exported kubeconfig name is deterministic (<namespace>-<name>-kubeconfig.yaml)
    # and workdir is deliberately retained for inspection. A prior create/delete/recreate
    # can therefore leave an old kubeconfig here whose NodePort no longer matches the new
    # Service's randomly allocated NodePort. Manifest creation does not invoke `k3kcli
    # cluster create`, so step 3 would otherwise see the stale file and skip generating a
    # new one. Remove only this generated credential, not the whole work directory, before
    # every create so the later existence check always refers to this Cluster instance.
    rm -f "$virtual_kubeconfig"

    # -------------------------------------------------------------------------
    step "0: host cluster reachable and k3k installed"
    # -------------------------------------------------------------------------
    run "$kubectl" --kubeconfig "$host_kubeconfig" get nodes -o wide
    run "$kubectl" --kubeconfig "$host_kubeconfig" -n k3k-system get deploy,pods
    run "$kubectl" --kubeconfig "$host_kubeconfig" get crd clusters.k3k.io

    local create_rc=0

    if [[ -n $cluster_manifest ]]; then
        # -------------------------------------------------------------------------
        step "1: create the virtual cluster from $cluster_manifest"
        # -------------------------------------------------------------------------
        # k3kcli creates the namespace itself (cli/cmds/cluster_create.go createNamespace);
        # replicated here so the manifest only has to describe the Cluster.
        "$kubectl" --kubeconfig "$host_kubeconfig" create namespace "$namespace" \
            --dry-run=client -o yaml | run "$kubectl" --kubeconfig "$host_kubeconfig" apply -f -

        local tls_san=$kubeconfig_server
        if [[ -z $tls_san ]]; then
            tls_san=$(host_from_kubeconfig "$host_kubeconfig") || \
                die "could not derive a TLS SAN from $host_kubeconfig; pass --kubeconfig-server"
        fi

        echo
        echo "Rendering $cluster_manifest (__K3K_NAME__=$name __K3K_NAMESPACE__=$namespace __K3K_TLS_SAN__=$tls_san)"
        echo "\$ sed ... $cluster_manifest | $kubectl --kubeconfig $host_kubeconfig apply -f -"
        if ! sed \
            -e "s/__K3K_NAME__/${name}/g" \
            -e "s/__K3K_NAMESPACE__/${namespace}/g" \
            -e "s/__K3K_TLS_SAN__/${tls_san}/g" \
            "$cluster_manifest" | "$kubectl" --kubeconfig "$host_kubeconfig" apply -f -
        then
            create_rc=$?
        fi
    else
        # -------------------------------------------------------------------------
        step "1: create the virtual cluster with k3kcli"
        # -------------------------------------------------------------------------
        # This is the k3kcli equivalent of the manifest k3kbulk applies. k3kcli always forces
        # expose.nodePort, which is why k3kbulk defaults to the same thing.
        #
        # --kubeconfig-server is omitted entirely when unset rather than passed empty, so
        # k3kcli applies its own fallback: the hostname from the host kubeconfig's server URL.
        #
        # k3kcli's own readiness wait is bounded by --k3kcli-timeout and is NOT authoritative;
        # see step 2 for why. Its exit status is captured rather than aborting the script.
        local create_args
        create_args=(
            --kubeconfig "$host_kubeconfig" cluster create
            --namespace "$namespace"
            --mode "$mode"
            --servers "$servers"
            --agents "$agents"
            --persistence-type "$persistence_type"
            --timeout "$k3kcli_timeout"
        )
        if [[ -n $kubeconfig_server ]]; then
            create_args+=(--kubeconfig-server "$kubeconfig_server")
        fi
        create_args+=("$name")

        (
            cd "$workdir"
            run "$k3kcli" "${create_args[@]}"
        ) || create_rc=$?

        if (( create_rc != 0 )); then
            echo
            echo "NOTE: k3kcli exited ${create_rc}. That is not treated as fatal here, because"
            echo "its wait aborts as soon as Cluster.Status.Phase reads Failed, and that phase"
            echo "is set by any transient reconcile error. Step 2 decides whether the cluster"
            echo "actually came up."
        fi
    fi

    # -------------------------------------------------------------------------
    step "2: wait for the Cluster to report condition=Ready"
    # -------------------------------------------------------------------------
    # This is the authoritative readiness check, and the same one k3kbulk uses in
    # CLI.WaitReady. Unlike k3kcli's wait it polls the Ready condition, so the transient
    # Failed phases that k3k sets while a server is still starting do not abort it.
    #
    # k3k sets Phase=Failed for any reconcile error that is neither a validation error nor
    # k3s.ErrServerNotReady, and only ECONNREFUSED is translated into ErrServerNotReady.
    # Once the server pod is up but slow to answer, the probe times out instead of being
    # refused, so a perfectly healthy cluster reports Failed for a few seconds.
    if ! run "$kubectl" --kubeconfig "$host_kubeconfig" wait --for=condition=Ready \
        --timeout="$timeout" \
        --namespace "$namespace" \
        "cluster.k3k.io/$name"; then
        echo
        echo "Cluster conditions:"
        "$kubectl" --kubeconfig "$host_kubeconfig" -n "$namespace" get cluster.k3k.io "$name" \
            -o 'jsonpath={range .status.conditions[*]}{.type}={.status} reason={.reason}{"\n"}  message={.message}{"\n"}{end}' 2>/dev/null || true
        echo
        echo "Recent warning events:"
        "$kubectl" --kubeconfig "$host_kubeconfig" -n "$namespace" get events \
            --field-selector type=Warning --sort-by=.lastTimestamp 2>/dev/null | tail -10 || true

        die "cluster did not reach condition=Ready within ${timeout} (create step exit was ${create_rc})"
    fi

    # -------------------------------------------------------------------------
    step "3: inspect the Cluster object and obtain its kubeconfig"
    # -------------------------------------------------------------------------
    run "$kubectl" --kubeconfig "$host_kubeconfig" -n "$namespace" get cluster.k3k.io "$name" -o wide
    run "$kubectl" --kubeconfig "$host_kubeconfig" -n "$namespace" get pods -o wide
    run "$kubectl" --kubeconfig "$host_kubeconfig" -n "$namespace" get svc

    # k3kcli writes the kubeconfig only after its own wait succeeds, so whenever that wait
    # gave up early the file is missing and has to be generated separately. The default
    # output name is <namespace>-<name>-kubeconfig.yaml, which is what is expected here.
    if [[ ! -f $virtual_kubeconfig ]]; then
        echo
        echo "k3kcli did not write the kubeconfig, generating it now."

        local kubeconfig_args
        kubeconfig_args=(
            --kubeconfig "$host_kubeconfig" kubeconfig generate
            --namespace "$namespace"
            --name "$name"
        )
        if [[ -n $kubeconfig_server ]]; then
            kubeconfig_args+=(--kubeconfig-server "$kubeconfig_server")
        fi

        (
            cd "$workdir"
            run "$k3kcli" "${kubeconfig_args[@]}"
        )
    fi

    [[ -f $virtual_kubeconfig ]] || die "no kubeconfig at $virtual_kubeconfig"
    echo
    echo "Virtual cluster kubeconfig: $virtual_kubeconfig"
    run grep -m1 'server:' "$virtual_kubeconfig"

    # -------------------------------------------------------------------------
    step "4: the virtual cluster answers and has a Ready node"
    # -------------------------------------------------------------------------
    # A failure here means the API address is not reachable from this machine, which on a
    # Harvester-hosted downstream usually means the VPN is down.
    run "$kubectl" --kubeconfig "$virtual_kubeconfig" version
    run "$kubectl" --kubeconfig "$virtual_kubeconfig" get nodes -o wide
    run "$kubectl" --kubeconfig "$virtual_kubeconfig" get pods -A

    echo
    echo "Node conditions:"
    "$kubectl" --kubeconfig "$virtual_kubeconfig" get nodes \
        -o 'jsonpath={range .items[*]}{.metadata.name}{"\t"}{range .status.conditions[*]}{.type}={.status}{" "}{end}{"\n"}{end}' || true

    # -------------------------------------------------------------------------
    # Optional hold, so the verified cluster can be used or inspected before teardown.
    # -------------------------------------------------------------------------
    if (( hold_seconds > 0 )); then
        step "5: holding the cluster up for ${hold} (${hold_seconds}s)"
        echo "Verification passed. Sleeping so the cluster can be inspected or used:"
        echo "  export KUBECONFIG=$virtual_kubeconfig"
        echo
        echo "Ctrl-C stops the wait early. The cluster is left running either way; remove"
        echo "it with the delete command below."
        sleep "$hold_seconds"
        echo "Hold elapsed."
    fi

    echo
    echo "Done."
    [[ $keep == true ]] || cat <<EOF

Teardown:
  $0 delete --host-kubeconfig $host_kubeconfig --name $name
EOF
}

do_delete() {
    # -------------------------------------------------------------------------
    step "0: host cluster reachable"
    # -------------------------------------------------------------------------
    run "$kubectl" --kubeconfig "$host_kubeconfig" get crd clusters.k3k.io

    # Teardown is made idempotent so it can be re-run after a partial failure, and so a
    # create that never got as far as the cluster does not leave delete erroring out.
    if ! "$kubectl" --kubeconfig "$host_kubeconfig" -n "$namespace" \
        get cluster.k3k.io "$name" >/dev/null 2>&1; then
        echo "Cluster $namespace/$name does not exist; deleting its namespace if it remains."
        delete_namespace
        return 0
    fi
    run "$kubectl" --kubeconfig "$host_kubeconfig" -n "$namespace" get cluster.k3k.io "$name" -o wide

    # -------------------------------------------------------------------------
    step "1: delete the virtual cluster with k3kcli"
    # -------------------------------------------------------------------------
    run "$k3kcli" --kubeconfig "$host_kubeconfig" cluster delete \
        --namespace "$namespace" \
        "$name"

    # -------------------------------------------------------------------------
    step "2: wait for the Cluster object to disappear"
    # -------------------------------------------------------------------------
    # Same wait k3kbulk performs in CLI.WaitDeleted.
    run "$kubectl" --kubeconfig "$host_kubeconfig" wait --for=delete \
        --timeout="$timeout" \
        --namespace "$namespace" \
        "cluster.k3k.io/$name"

    delete_namespace

    echo
    echo "Done."
    [[ $keep == true ]] || cat <<EOF

Cleanup not performed by this command:
  rm -rf $workdir
EOF
}

# delete_namespace removes the per-virtual-cluster namespace after its Cluster
# CR is gone. k3kcli deletes the CR but leaves k3k-<name> behind; at bulk scale
# those leaked namespaces retain workloads, PVCs, secrets and accounting state.
# --ignore-not-found keeps a retry and an orphaned/partially-created cluster
# idempotent, while --wait and the same timeout as the CR deletion make a stuck
# namespace finalizer visible to the caller instead of silently moving on.
delete_namespace() {
    # -------------------------------------------------------------------------
    step "3: delete the virtual cluster namespace"
    # -------------------------------------------------------------------------
    run "$kubectl" --kubeconfig "$host_kubeconfig" delete namespace "$namespace" \
        --ignore-not-found \
        --wait=true \
        --timeout="$timeout"
}

case $command in
    create) do_create ;;
    delete) do_delete ;;
esac

# # -----------------------------------------------------------------------------
# step "4: the virtual cluster can reach Rancher"
# # -----------------------------------------------------------------------------
# # cattle-cluster-agent runs inside the virtual cluster, so this is the connectivity that
# # actually decides whether an import succeeds.
# if [[ -n $rancher_host ]]; then
#     echo "Testing egress to https://${rancher_host}/ping from inside the virtual cluster."
#     run "$kubectl" --kubeconfig "$virtual_kubeconfig" run k3k-egress-check \
#         --rm -i --restart=Never --image=curlimages/curl:8.10.1 --timeout=120s -- \
#         curl -sS -k --max-time 20 "https://${rancher_host}/ping" || \
#         echo "EGRESS CHECK FAILED: the virtual cluster cannot reach Rancher. An import will never complete."
# else
#     echo "Skipped: pass --rancher-host to test egress to Rancher."
# fi

# if [[ $do_import != true ]]; then
#     echo
#     echo "Done. Re-run with --import to also exercise Rancher registration."
#     [[ $keep == true ]] || cat <<EOF

# Teardown:
#   $k3kcli --kubeconfig $host_kubeconfig cluster delete --namespace $namespace $name
# EOF
#     exit 0
# fi

# # -----------------------------------------------------------------------------
# step "5: import into Rancher"
# # -----------------------------------------------------------------------------
# api="https://${rancher_host}/v3"
# curl_auth=(-sS -k -H "Authorization: Bearer ${RANCHER_TOKEN}" -H 'Content-Type: application/json')

# echo "Creating the imported cluster in Rancher."
# cluster_id=$(
#     curl "${curl_auth[@]}" -X POST "${api}/clusters" \
#         -d "{\"type\":\"cluster\",\"name\":\"${name}\",\"labels\":{\"provider.cattle.io\":\"k3k\"}}" \
#         | sed -n 's/.*"id":"\(c-m-[^"]*\)".*/\1/p' | head -n1
# )
# [[ -n $cluster_id ]] || die "could not create the Rancher cluster; check RANCHER_TOKEN and --rancher-host"
# echo "Rancher cluster: $cluster_id"

# echo "Fetching the registration manifest."
# manifest_url=$(
#     curl "${curl_auth[@]}" "${api}/clusterregistrationtokens?clusterId=${cluster_id}" \
#         | tr ',' '\n' | sed -n 's/.*"manifestUrl":"\([^"]*\)".*/\1/p' | head -n1
# )
# [[ -n $manifest_url ]] || die "no registration manifest URL returned for $cluster_id"
# echo "Manifest URL: $manifest_url"

# run bash -c "curl -sS -k '${manifest_url}' | '${kubectl}' --kubeconfig '${virtual_kubeconfig}' apply -f -"

# # -----------------------------------------------------------------------------
# step "6: watch cattle-cluster-agent connect"
# # -----------------------------------------------------------------------------
# echo "The agent typically needs one to three minutes. Ctrl-C to stop watching."
# for attempt in $(seq 1 60); do
#     state=$(curl "${curl_auth[@]}" "${api}/clusters/${cluster_id}" \
#         | tr ',' '\n' | sed -n 's/.*"state":"\([^"]*\)".*/\1/p' | head -n1)
#     echo "[$attempt] Rancher state: ${state:-unknown}"
#     if [[ $state == active ]]; then
#         echo "Cluster is active."
#         break
#     fi
#     "$kubectl" --kubeconfig "$virtual_kubeconfig" -n cattle-system \
#         get pods -l app=cattle-cluster-agent --no-headers 2>/dev/null || true
#     sleep 10
# done

# echo
# echo "If the cluster never becomes active, read the agent log:"
# echo "  $kubectl --kubeconfig $virtual_kubeconfig -n cattle-system logs -l app=cattle-cluster-agent --tail=100"

# [[ $keep == true ]] || cat <<EOF

# Teardown:
#   curl -sS -k -H "Authorization: Bearer \$RANCHER_TOKEN" -X DELETE ${api}/clusters/${cluster_id}
#   $k3kcli --kubeconfig $host_kubeconfig cluster delete --namespace $namespace $name
# EOF
