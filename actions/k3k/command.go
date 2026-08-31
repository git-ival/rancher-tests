package k3k

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

type CommandRunner interface {
	Run(ctx context.Context, dir, name string, args []string, stdin []byte, logPath string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir, name string, args []string, stdin []byte, logPath string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stdout = logFile
	cmd.Stderr = io.MultiWriter(logFile, os.Stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w (log: %s)", name, err, logPath)
	}
	return nil
}

type CLI struct {
	K3KCLI  string
	Kubectl string
	Runner  CommandRunner
}

func (c CLI) Create(ctx context.Context, item *ClusterState, target Target, config *Config) error {
	if err := os.MkdirAll(item.ArtifactDir, 0o755); err != nil {
		return err
	}
	args := []string{
		"--kubeconfig", target.Kubeconfig,
		"cluster", "create",
		"--namespace", item.Namespace,
		"--mode", config.Mode,
		"--servers", fmt.Sprint(config.Servers),
		"--agents", fmt.Sprint(config.Agents),
		"--persistence-type", config.PersistenceType,
		"--timeout", config.CreateTimeout.String(),
		"--labels", "k3k-bulk.io/batch=" + config.Batch,
		"--labels", "k3k-bulk.io/target=" + target.Name,
		"--kubeconfig-server", target.KubeconfigServer,
		item.Name,
	}
	return c.Runner.Run(ctx, item.ArtifactDir, c.K3KCLI, args, nil, filepath.Join(item.ArtifactDir, "create.log"))
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
