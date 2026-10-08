package k3k

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	sigsyaml "sigs.k8s.io/yaml"
)

// CommandRunner executes an external command, streaming its output to a log file.
type CommandRunner interface {
	Run(ctx context.Context, dir, name string, args []string, stdin []byte, logPath string) error
	// Output behaves like Run but also returns the command's standard output, for commands
	// whose result must be parsed rather than only recorded.
	Output(ctx context.Context, dir, name string, args []string, stdin []byte, logPath string) ([]byte, error)
}

type ExecRunner struct{}

func (r ExecRunner) Run(ctx context.Context, dir, name string, args []string, stdin []byte, logPath string) error {
	_, err := r.run(ctx, dir, name, args, stdin, logPath, false)
	return err
}

func (r ExecRunner) Output(ctx context.Context, dir, name string, args []string, stdin []byte, logPath string) ([]byte, error) {
	return r.run(ctx, dir, name, args, stdin, logPath, true)
}

func (ExecRunner) run(ctx context.Context, dir, name string, args []string, stdin []byte, logPath string, capture bool) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	var captured bytes.Buffer
	if capture {
		cmd.Stdout = io.MultiWriter(logFile, &captured)
	} else {
		cmd.Stdout = logFile
	}
	cmd.Stderr = io.MultiWriter(logFile, os.Stderr)

	if err := cmd.Run(); err != nil {
		return captured.Bytes(), fmt.Errorf("%s failed: %w (log: %s)", name, err, logPath)
	}
	return captured.Bytes(), nil
}

type CLI struct {
	K3KCLI  string
	Kubectl string
	Runner  CommandRunner
}

// Create provisions one k3k cluster and writes its kubeconfig.
//
// The k3k Cluster object is applied as a manifest rather than created through
// "k3kcli cluster create", because the CLI exposes no flags for expose, nodeSelector,
// serverLimit, workerLimit, serverResources, workerResources, serverAffinity, or
// agentAffinity, and it unconditionally overwrites spec.expose with a NodePort configuration.
// k3kcli is still used to mint the kubeconfig, which requires the cluster CA.
func (c CLI) Create(ctx context.Context, item *ClusterState, target Target, config *Config) error {
	if err := os.MkdirAll(item.ArtifactDir, 0o755); err != nil {
		return err
	}

	manifest, err := config.RenderCluster(item, target)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(item.ArtifactDir, "cluster.yaml"), manifest, 0o600); err != nil {
		return err
	}

	applyArgs := []string{"--kubeconfig", target.Kubeconfig, "apply", "-f", "-"}
	if err := c.Runner.Run(ctx, item.ArtifactDir, c.Kubectl, applyArgs, manifest, filepath.Join(item.ArtifactDir, "apply.log")); err != nil {
		return err
	}

	Progress("%s: waiting up to %s for cluster.k3k.io/%s to become Ready",
		item.Name, config.CreateTimeout, item.Name)
	waitArgs := []string{
		"--kubeconfig", target.Kubeconfig,
		"wait", "--for=condition=Ready",
		"--namespace", item.Namespace,
		"--timeout", config.CreateTimeout.String(),
		"cluster.k3k.io/" + item.Name,
	}
	if err := c.Runner.Run(ctx, item.ArtifactDir, c.Kubectl, waitArgs, nil, filepath.Join(item.ArtifactDir, "wait-ready.log")); err != nil {
		return err
	}

	spec, err := config.SpecFor(item, target)
	if err != nil {
		return err
	}
	if spec.Expose != nil && spec.Expose.LoadBalancer != nil {
		if err := c.reconcileLoadBalancerSAN(ctx, item, target, config); err != nil {
			return err
		}
	}

	kubeconfigArgs := []string{
		"--kubeconfig", target.Kubeconfig,
		"kubeconfig", "generate",
		"--namespace", item.Namespace,
		"--name", item.Name,
		"--kubeconfig-server", target.KubeconfigServer,
		"--config-name", filepath.Base(item.Kubeconfig),
	}
	return c.Runner.Run(ctx, item.ArtifactDir, c.K3KCLI, kubeconfigArgs, nil, filepath.Join(item.ArtifactDir, "kubeconfig.log"))
}

// reconcileLoadBalancerSAN adds the allocated LoadBalancer address to spec.tlsSANs.
//
// The address is assigned by the host cluster's load balancer provider, so it cannot be
// supplied up front. Without it in tlsSANs, k3k falls back to tlsSANs[0] when building the
// kubeconfig server URL and hands out a kubeconfig pointing at the wrong host.
//
// Discovery is best effort: if no address appears within the configured window the step is
// skipped with a warning rather than failing the cluster, since some environments never
// allocate one.
func (c CLI) reconcileLoadBalancerSAN(ctx context.Context, item *ClusterState, target Target, config *Config) error {
	logPath := filepath.Join(item.ArtifactDir, "lb-sans.log")
	address, err := c.discoverLoadBalancerAddress(ctx, item, target, config.LBAddressTimeout, logPath)
	if err != nil {
		return err
	}
	if address == "" {
		Progress("%s: no LoadBalancer address found within %s; continuing without adding it to tlsSANs",
			item.Name, config.LBAddressTimeout)
		return nil
	}
	Progress("%s: discovered LoadBalancer address %s", item.Name, address)

	// Read the SANs from the live object rather than from the config, so the patch preserves
	// anything already present and stays correct on a resumed run.
	current, err := c.currentTLSSANs(ctx, item, target, logPath)
	if err != nil {
		return err
	}
	for _, san := range current {
		if san == address {
			return nil
		}
	}

	patch, err := sigsyaml.Marshal(map[string]any{
		"spec": map[string]any{"tlsSANs": append(append([]string{}, current...), address)},
	})
	if err != nil {
		return err
	}
	patchJSON, err := sigsyaml.YAMLToJSON(patch)
	if err != nil {
		return err
	}

	patchArgs := []string{
		"--kubeconfig", target.Kubeconfig,
		"patch", "cluster.k3k.io/" + item.Name,
		"--namespace", item.Namespace,
		"--type", "merge",
		"--patch", string(patchJSON),
	}
	if err := c.Runner.Run(ctx, item.ArtifactDir, c.Kubectl, patchArgs, nil, logPath); err != nil {
		return err
	}

	waitArgs := []string{
		"--kubeconfig", target.Kubeconfig,
		"wait", "--for=jsonpath={.status.tlsSANs}=" + address,
		"--namespace", item.Namespace,
		"--timeout", config.LBAddressTimeout.String(),
		"cluster.k3k.io/" + item.Name,
	}
	if err := c.Runner.Run(ctx, item.ArtifactDir, c.Kubectl, waitArgs, nil, logPath); err != nil {
		Progress("%s: LoadBalancer address %s did not appear in status.tlsSANs; continuing", item.Name, address)
	}
	return nil
}

// currentTLSSANs reads spec.tlsSANs from the live Cluster object.
func (c CLI) currentTLSSANs(ctx context.Context, item *ClusterState, target Target, logPath string) ([]string, error) {
	args := []string{
		"--kubeconfig", target.Kubeconfig,
		"get", "cluster.k3k.io/" + item.Name,
		"--namespace", item.Namespace,
		"-o", "json",
	}
	output, err := c.Runner.Output(ctx, item.ArtifactDir, c.Kubectl, args, nil, logPath)
	if err != nil {
		return nil, err
	}

	var cluster struct {
		Spec struct {
			TLSSANs []string `json:"tlsSANs"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(output, &cluster); err != nil {
		return nil, fmt.Errorf("read tlsSANs for %s: %w", item.Name, err)
	}
	return cluster.Spec.TLSSANs, nil
}

// serviceList is the subset of "kubectl get svc -o json" that address discovery needs.
type serviceList struct {
	Items []struct {
		Spec struct {
			Type     string            `json:"type"`
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
		Status struct {
			LoadBalancer struct {
				Ingress []struct {
					IP       string `json:"ip"`
					Hostname string `json:"hostname"`
				} `json:"ingress"`
			} `json:"loadBalancer"`
		} `json:"status"`
	} `json:"items"`
}

// discoverLoadBalancerAddress polls the cluster namespace for the k3k server service address.
//
// The service is matched by type and selector rather than by name because k3k derives the
// name through a hash-truncating helper that is not worth reimplementing here. Every other
// service k3k creates in the namespace is a ClusterIP, so the match is unambiguous.
func (c CLI) discoverLoadBalancerAddress(ctx context.Context, item *ClusterState, target Target, timeout time.Duration, logPath string) (string, error) {
	args := []string{
		"--kubeconfig", target.Kubeconfig,
		"get", "svc",
		"--namespace", item.Namespace,
		"-o", "json",
	}

	deadline := time.Now().Add(timeout)
	for {
		output, err := c.Runner.Output(ctx, item.ArtifactDir, c.Kubectl, args, nil, logPath)
		if err == nil {
			if address := loadBalancerAddress(output); address != "" {
				return address, nil
			}
		}

		// Clamp the wait to whatever remains, so a short timeout does not pay for a full
		// polling interval before giving up.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", nil
		}
		interval := lbPollInterval
		if remaining < interval {
			interval = remaining
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}
}

// lbPollInterval is how often LoadBalancer address discovery re-reads the service list.
const lbPollInterval = 5 * time.Second

func loadBalancerAddress(output []byte) string {
	var services serviceList
	if err := json.Unmarshal(output, &services); err != nil {
		return ""
	}
	for _, service := range services.Items {
		if service.Spec.Type != "LoadBalancer" || service.Spec.Selector["role"] != "server" {
			continue
		}
		for _, ingress := range service.Status.LoadBalancer.Ingress {
			if ingress.IP != "" {
				return ingress.IP
			}
			if ingress.Hostname != "" {
				return ingress.Hostname
			}
		}
	}
	return ""
}

func (c CLI) Register(ctx context.Context, item *ClusterState, manifest []byte) error {
	args := []string{"--kubeconfig", item.Kubeconfig, "apply", "-f", "-"}
	return c.Runner.Run(ctx, item.ArtifactDir, c.Kubectl, args, manifest, filepath.Join(item.ArtifactDir, "register.log"))
}

func (c CLI) Delete(ctx context.Context, item *ClusterState, target Target) error {
	args := []string{
		"--kubeconfig", target.Kubeconfig,
		"cluster", "delete",
		"--namespace", item.Namespace,
		item.Name,
	}
	return c.Runner.Run(ctx, item.ArtifactDir, c.K3KCLI, args, nil, filepath.Join(item.ArtifactDir, "delete.log"))
}

func (c CLI) WaitDeleted(ctx context.Context, item *ClusterState, target Target) error {
	args := []string{
		"--kubeconfig", target.Kubeconfig,
		"wait", "--for=delete", "--timeout=10m",
		"--namespace", item.Namespace,
		"cluster.k3k.io/" + item.Name,
	}
	return c.Runner.Run(ctx, item.ArtifactDir, c.Kubectl, args, nil, filepath.Join(item.ArtifactDir, "wait-delete.log"))
}
