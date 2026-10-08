#!/usr/bin/env bash
#
# Run manual-k3kcli-repro.sh over many k3k virtual clusters, in batches.
#
# This is the Rancher-free, k3kcli-only equivalent of a k3kbulk run. It does not
# reimplement any of the per-cluster work; it only decides what to name each cluster, how
# many to work on at once, and what to report. Every cluster is created or deleted by the
# single-cluster script, so the two cannot drift apart.
#
# Work is done in batches. Given a target of T clusters (--count) and a batch size of N
# (--batch-size), the run performs ceil(T / N) batches. Every cluster in a batch is
# started together and the batch is finished before the next one starts, so the host is
# never asked for more than N clusters at a time. The final batch is short when T is not
# a multiple of N, and exactly T clusters are worked on overall, never more.
#
# --hold applies once per batch, after the batch finishes, which is the point of the
# batching: it gives the host time to settle before the next batch lands on it. A batch
# in which every cluster was skipped by --skip-done did no work, so it is not held.
#
# --skip-done skips whatever is already in the requested state, in both directions:
# on create the clusters that exist, on delete the clusters and their namespaces that do
# not. A Cluster CR can disappear before its k3k-<name> namespace is cleaned up, so a
# remaining namespace still runs the single-cluster delete path to remove that orphan.
# Either way the check runs in this script, so a skipped cluster costs one API call rather
# than a whole single-cluster invocation.
#
# Cluster names match k3kbulk's own scheme, <name>-001, <name>-002, and so on, so a run
# here can be inspected with the same names a k3kbulk run would have used.
#
# Each cluster's full output goes to its own log. Only one progress line per cluster is
# printed, plus a per-batch tally and a final summary. The exit status is non-zero if any
# cluster failed.
#
# Examples:
#   ./manual-k3kcli-bulk.sh create \
#     --host-kubeconfig ./host.yaml --kubeconfig-server 192.0.2.10 \
#     --name k3k-soak --count 50 --batch-size 10 --hold 2m
#
#   ./manual-k3kcli-bulk.sh delete \
#     --host-kubeconfig ./host.yaml --name k3k-soak --count 50 --batch-size 10

set -euo pipefail

command=
prefix=k3k-manual
count=1
start_index=1
batch_size=10
hold=0
skip_done=false
dry_run=false
keep=false
logs_dir=
repro=

host_kubeconfig=
kubeconfig_server=
mode=
servers=
agents=
persistence_type=
cluster_manifest=
timeout=
k3kcli_timeout=
kubectl=kubectl
k3kcli=

usage() {
    awk 'NR>=3 && /^#/ { sub(/^# ?/, ""); print; next } NR>=3 { exit }' "$0"
    cat <<'EOF'

Usage: manual-k3kcli-bulk.sh create|delete [options]

Batch options:
  --name PREFIX            Cluster name prefix, a DNS label (default: k3k-manual)
  --count T                Target number of clusters overall (default: 1)
  --batch-size N           Clusters per batch, all started together (default: 10)
  --start-index N          First numeric suffix (default: 1)
  --hold DURATION          Sleep this long after each batch finishes, before the next
                           batch starts. Batches that did no work are not held.
                           0 disables (default: 0)
  --skip-done              Skip clusters already in the requested state: on create the
                           ones that exist on the host, on delete the ones that do not.
                           A batch that was entirely skipped is not held
  --dry-run                Print the batch plan and the per-cluster commands, run nothing
  --logs-dir DIR           Per-cluster logs (default: ./bulk-<name>-<command>-logs)
  --repro PATH             Single-cluster script (default: alongside this one)
  --keep                   Do not print the trailing teardown/cleanup instructions

Passed through to manual-k3kcli-repro.sh:
  --host-kubeconfig FILE   Kubeconfig for the k3k host cluster (required)
  --kubeconfig-server ADDR Reachable host address for the virtual cluster API. Optional;
                           when unset, k3kcli uses the host kubeconfig's own server
                           hostname
  --mode MODE              shared, virtual, or hcp
  --servers N              Number of servers per cluster
  --agents N               Number of agents per cluster, virtual mode only
  --persistence-type TYPE  dynamic or ephemeral
  --cluster-manifest FILE  Apply this Cluster manifest instead of using k3kcli to create
                           each cluster, for shapes k3kcli cannot express (per-role
                           resources, agent counts in shared mode, etc). Owns the cluster
                           spec: conflicts with --servers, --agents, --mode and
                           --persistence-type. Every cluster in the run shares one
                           manifest; see manual-k3kcli-repro.sh's header comment for the
                           placeholders it supports, and dartboard's
                           scripts/k3kbulk_render_spec.py for a tool that renders one from
                           a higher-level profile
  --timeout DURATION       Per-cluster condition=Ready and deletion timeout
  --k3kcli-timeout DUR     Per-cluster timeout for k3kcli's own (advisory) wait
  --kubectl PATH           kubectl executable (default: kubectl)
  --k3kcli PATH            k3kcli executable

Unset pass-through options keep the single-cluster script's own defaults.

Durations accept a bare number of seconds or Go-style units, for example 90, 30s,
5m, or 1h30m.
EOF
}

die() { echo "ERROR: $*" >&2; exit 1; }

# plural returns "cluster" or "clusters" to match a count.
plural() { (( $1 == 1 )) && echo cluster || echo clusters; }

# parse_duration converts a bare number of seconds or a Go-style duration such as 30s,
# 5m, or 1h30m into whole seconds. BSD sleep does not accept unit suffixes.
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
        --name) prefix=${2:?}; shift 2 ;;
        --count) count=${2:?}; shift 2 ;;
        --batch-size) batch_size=${2:?}; shift 2 ;;
        --start-index) start_index=${2:?}; shift 2 ;;
        --hold) hold=${2:?}; shift 2 ;;
        --skip-done) skip_done=true; shift ;;
        --dry-run) dry_run=true; shift ;;
        --logs-dir) logs_dir=${2:?}; shift 2 ;;
        --repro) repro=${2:?}; shift 2 ;;
        --keep) keep=true; shift ;;
        --host-kubeconfig) host_kubeconfig=${2:?}; shift 2 ;;
        --kubeconfig-server) kubeconfig_server=${2:?}; shift 2 ;;
        --mode) mode=${2:?}; shift 2 ;;
        --servers) servers=${2:?}; shift 2 ;;
        --agents) agents=${2:?}; shift 2 ;;
        --persistence-type) persistence_type=${2:?}; shift 2 ;;
        --cluster-manifest) cluster_manifest=${2:?}; shift 2 ;;
        --timeout) timeout=${2:?}; shift 2 ;;
        --k3kcli-timeout) k3kcli_timeout=${2:?}; shift 2 ;;
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

    # Failed here rather than left to the first worker, so a bad combination is caught
    # before any cluster is touched instead of after the first batch fails.
    conflicts=()
    [[ -n $mode ]] && conflicts+=(--mode)
    [[ -n $servers ]] && conflicts+=(--servers)
    [[ -n $agents ]] && conflicts+=(--agents)
    [[ -n $persistence_type ]] && conflicts+=(--persistence-type)
    if (( ${#conflicts[@]} > 0 )); then
        die "--cluster-manifest owns the cluster spec; remove: ${conflicts[*]}"
    fi
fi

# The same DNS-label rule k3kbulk applies, so a name valid here is valid there.
[[ $prefix =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || die "--name must be a DNS label: $prefix"
(( ${#prefix} <= 58 )) || die "--name must be no longer than 58 characters"
if [[ ! $count =~ ^[0-9]+$ ]] || (( count < 1 )); then
    die "--count must be a positive integer, got: $count"
fi
if [[ ! $batch_size =~ ^[0-9]+$ ]] || (( batch_size < 1 )); then
    die "--batch-size must be a positive integer, got: $batch_size"
fi
if [[ ! $start_index =~ ^[0-9]+$ ]]; then
    die "--start-index must be a non-negative integer, got: $start_index"
fi

hold_seconds=$(parse_duration "$hold") || die "--hold is not a valid duration: $hold"

if [[ -z $repro ]]; then
    repro="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/manual-k3kcli-repro.sh"
fi
[[ -f $repro ]] || die "single-cluster script not found: $repro"
[[ -x $repro ]] || die "single-cluster script is not executable: $repro"

host_kubeconfig=$(cd "$(dirname "$host_kubeconfig")" && pwd)/$(basename "$host_kubeconfig")
if [[ -n $cluster_manifest ]]; then
    cluster_manifest=$(cd "$(dirname "$cluster_manifest")" && pwd)/$(basename "$cluster_manifest")
fi
[[ -n $logs_dir ]] || logs_dir="./bulk-${prefix}-${command}-logs"

# Options are forwarded only when set, so the single-cluster script keeps ownership of
# every default and this wrapper never has to restate them.
child_args=(--host-kubeconfig "$host_kubeconfig" --kubectl "$kubectl" --keep)
# Omitted when unset so k3kcli applies its own fallback, rather than forwarding an empty
# value that would be rejected.
if [[ $command == create && -n $kubeconfig_server ]]; then
    child_args+=(--kubeconfig-server "$kubeconfig_server")
fi
[[ -n $mode ]] && child_args+=(--mode "$mode")
[[ -n $servers ]] && child_args+=(--servers "$servers")
[[ -n $agents ]] && child_args+=(--agents "$agents")
[[ -n $persistence_type ]] && child_args+=(--persistence-type "$persistence_type")
if [[ $command == create && -n $cluster_manifest ]]; then
    child_args+=(--cluster-manifest "$cluster_manifest")
fi
[[ -n $timeout ]] && child_args+=(--timeout "$timeout")
[[ -n $k3kcli_timeout ]] && child_args+=(--k3kcli-timeout "$k3kcli_timeout")
[[ -n $k3kcli ]] && child_args+=(--k3kcli "$k3kcli")

# Exactly --count names are generated, so the batch loop below cannot overshoot the
# target no matter how the batches divide up.
names=()
for (( i = start_index; i < start_index + count; i++ )); do
    names+=("$(printf '%s-%03d' "$prefix" "$i")")
done

# Ceiling division: a target that is not a multiple of the batch size gets one extra,
# shorter batch rather than being rounded away.
batch_total=$(( (count + batch_size - 1) / batch_size ))
last_batch_size=$(( count - (batch_total - 1) * batch_size ))

echo "Command       : $command"
echo "Name prefix   : $prefix"
echo "Clusters      : ${#names[@]} (${names[0]} .. ${names[${#names[@]}-1]})"
if (( batch_total == 1 )); then
    echo "Batches       : 1 (size ${count})"
elif (( last_batch_size == batch_size )); then
    echo "Batches       : ${batch_total} (size ${batch_size})"
else
    echo "Batches       : ${batch_total} (size ${batch_size}, last ${last_batch_size})"
fi
echo "Host kubecfg  : $host_kubeconfig"
if [[ $command == create ]]; then
    if [[ -n $kubeconfig_server ]]; then
        echo "API address   : $kubeconfig_server"
    else
        echo "API address   : (unset, k3kcli derives it from the host kubeconfig)"
    fi
    if [[ -n $cluster_manifest ]]; then
        echo "Cluster spec  : $cluster_manifest (applied, not k3kcli)"
    fi
fi
echo "Single-cluster: $repro"
echo "Logs          : $logs_dir"
if (( hold_seconds > 0 )); then
    echo "Hold          : ${hold} (${hold_seconds}s) after each batch"
fi

if [[ $dry_run == true ]]; then
    echo
    echo "Dry run, nothing will be executed."
    for (( b = 1; b <= batch_total; b++ )); do
        offset=$(( (b - 1) * batch_size ))
        remaining=$(( count - offset ))
        size=$(( remaining < batch_size ? remaining : batch_size ))
        echo
        echo "Batch ${b}/${batch_total} (${size} $(plural "$size")):"
        for (( j = offset; j < offset + size; j++ )); do
            echo "  $repro $command --name ${names[$j]} ${child_args[*]}"
        done
        if (( hold_seconds > 0 )); then
            echo "  ... then hold ${hold_seconds}s"
        fi
    done
    exit 0
fi

mkdir -p "$logs_dir"
status_dir="$logs_dir/.status"
rm -rf "$status_dir"
mkdir -p "$status_dir"

# Job control is enabled so that each background worker becomes its own process group.
# Without it, an interrupt would only reap the worker subshells and leave the k3kcli and
# kubectl processes underneath them running, which at batch sizes of any interest means
# hundreds of stranded processes.
set -m

# Children are killed on interrupt so a Ctrl-C does not leave half a batch running
# detached in the background.
cleanup() {
    local pids pid
    pids=$(jobs -rp)
    if [[ -n $pids ]]; then
        echo >&2
        echo "Interrupted, stopping in-flight clusters..." >&2
        for pid in $pids; do
            # Signal the whole process group, so k3kcli and kubectl go down with the
            # worker. Fall back to the bare PID if the group is already gone.
            kill -TERM -- "-${pid}" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
        done
        wait 2>/dev/null || true
    fi
}
trap 'cleanup; exit 130' INT TERM

# cluster_exists reports whether the k3k Cluster object is on the host.
cluster_exists() {
    local name=$1
    "$kubectl" --kubeconfig "$host_kubeconfig" -n "k3k-${name}" \
        get "cluster.k3k.io/${name}" >/dev/null 2>&1
}

# namespace_exists reports whether the per-virtual-cluster namespace remains.
# The Cluster CR and namespace are cleaned independently, so the latter is part
# of the delete completion condition too.
namespace_exists() {
    local name=$1
    "$kubectl" --kubeconfig "$host_kubeconfig" get "namespace/k3k-${name}" >/dev/null 2>&1
}

# skip_reason echoes why a cluster is already in the state the command would put it in,
# or nothing when there is work to do. This is what lets --skip-done resume a partial
# run in either direction: a create skips what is already there, a delete skips what is
# already gone.
skip_reason() {
    local name=$1

    [[ $skip_done == true ]] || return 0

    if [[ $command == create ]]; then
        cluster_exists "$name" && echo "already exists"
    else
        # Delete is only done when both resources have disappeared. If k3kcli
        # removed the Cluster CR but left its namespace, invoke the repro
        # script so its idempotent namespace cleanup can finish the job.
        cluster_exists "$name" || namespace_exists "$name" || echo "does not exist"
    fi

    return 0
}

worker() {
    local name=$1
    local log="$logs_dir/${name}.log"
    local started=$SECONDS
    local rc=0
    local reason

    reason=$(skip_reason "$name")
    if [[ -n $reason ]]; then
        echo "skip" > "$status_dir/$name"
        echo "[skip] $name $reason"
        return 0
    fi

    "$repro" "$command" --name "$name" "${child_args[@]}" > "$log" 2>&1 || rc=$?
    echo "$rc" > "$status_dir/$name"

    if (( rc == 0 )); then
        echo "[ok]   $name ($(( SECONDS - started ))s)"
    else
        echo "[FAIL] $name (rc=$rc, $(( SECONDS - started ))s) see $log"
    fi
    return 0
}

# tally_range counts the recorded statuses for names[from..from+len-1].
# Sets batch_ok, batch_skipped and appends to the global failed array.
batch_ok=0
batch_skipped=0
failed=()
tally_range() {
    local from=$1 len=$2 j name status_file status
    batch_ok=0
    batch_skipped=0
    for (( j = from; j < from + len; j++ )); do
        name=${names[$j]}
        status_file="$status_dir/$name"
        if [[ ! -f $status_file ]]; then
            failed+=("$name (no status recorded)")
            continue
        fi
        status=$(<"$status_file")
        case $status in
            0) batch_ok=$(( batch_ok + 1 )) ;;
            skip) batch_skipped=$(( batch_skipped + 1 )) ;;
            *) failed+=("$name (rc=$status)") ;;
        esac
    done
}

run_started=$SECONDS
attempted=0
ok=0
skipped=0
held_skipped=0

for (( b = 1; b <= batch_total; b++ )); do
    offset=$(( (b - 1) * batch_size ))
    remaining=$(( count - offset ))
    # The final batch is short whenever the target is not a multiple of the batch size.
    size=$(( remaining < batch_size ? remaining : batch_size ))

    echo
    echo "=============================================================================="
    echo "BATCH ${b}/${batch_total}: ${size} $(plural "$size") (${names[$offset]} .. ${names[$(( offset + size - 1 ))]})"
    echo "=============================================================================="

    batch_started=$SECONDS
    for (( j = offset; j < offset + size; j++ )); do
        worker "${names[$j]}" &
    done
    # The whole batch finishes before the next one starts. That is the throttle: the host
    # is never asked for more than --batch-size clusters at a time.
    wait
    attempted=$(( attempted + size ))

    tally_range "$offset" "$size"
    ok=$(( ok + batch_ok ))
    skipped=$(( skipped + batch_skipped ))
    batch_failed=$(( size - batch_ok - batch_skipped ))
    echo "Batch ${b}/${batch_total} done in $(( SECONDS - batch_started ))s: ${batch_ok} ok, ${batch_skipped} skipped, ${batch_failed} failed"

    # Per batch, including the last, so the pause is predictable rather than conditional
    # on where a batch happens to fall.
    #
    # The exception is a batch that did no work at all: the hold exists to let the host
    # settle after clusters were created, so a batch that --skip-done turned into
    # nothing but skips has nothing to settle from and is not worth waiting on. This is
    # what makes resuming a large run cheap instead of re-paying the whole hold budget.
    batch_worked=$(( size - batch_skipped ))
    remaining=$(( count - attempted ))
    if (( hold_seconds > 0 && remaining > 0 )); then
        if (( batch_worked > 0 )); then
            echo "Holding ${hold} (${hold_seconds}s) before continuing. Ctrl-C stops the wait;"
            echo "clusters already worked on are left as they are."
            sleep "$hold_seconds"
        else
            held_skipped=$(( held_skipped + 1 ))
            echo "Batch ${b}/${batch_total} was entirely skipped, not holding."
        fi
    fi
done

run_elapsed=$(( SECONDS - run_started ))

echo
echo "=============================================================================="
echo "SUMMARY: $command $prefix"
echo "=============================================================================="
echo "Batches   : $batch_total"
echo "Attempted : $attempted of target $count"
echo "Succeeded : $ok"
(( skipped > 0 )) && echo "Skipped   : $skipped"
(( held_skipped > 0 )) && echo "Holds skip: $held_skipped batch(es) did no work"
echo "Failed    : ${#failed[@]}"
echo "Elapsed   : ${run_elapsed}s"

# The batch arithmetic is the whole point of this script, so it is asserted rather than
# trusted: overshooting the target would mean creating clusters the caller never asked for.
if (( attempted != count )); then
    echo
    die "internal error: attempted $attempted clusters but the target was $count"
fi

if (( ${#failed[@]} > 0 )); then
    echo
    echo "Failed clusters:"
    for entry in "${failed[@]}"; do
        echo "  $entry"
    done
    echo
    echo "Logs are in $logs_dir. Re-run the same command to retry; create is safe to"
    echo "repeat with --skip-done, and delete is idempotent."
    exit 1
fi

if [[ $command == create && $keep != true ]]; then
    cat <<EOF

Teardown:
  $0 delete --host-kubeconfig $host_kubeconfig --name $prefix --count $count \\
    --batch-size $batch_size --start-index $start_index
EOF
fi
