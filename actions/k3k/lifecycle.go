package k3k

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
)

type LifecycleCLI interface {
	// Create must be safe to repeat: a resumed run calls it again for any cluster that did not
	// reach the k3k-ready stage, or whose kubeconfig is missing.
	Create(ctx context.Context, item *ClusterState, target Target, config *Config) error
	Register(ctx context.Context, item *ClusterState, manifest []byte) error
	Delete(ctx context.Context, item *ClusterState, target Target) error
	WaitDeleted(ctx context.Context, item *ClusterState, target Target) error
}

type Orchestrator struct {
	Config  *Config
	State   *BatchState
	CLI     LifecycleCLI
	Rancher RancherAPI
}

func (o *Orchestrator) Create(ctx context.Context) error {
	return o.run(ctx, false)
}

func (o *Orchestrator) Delete(ctx context.Context) error {
	return o.run(ctx, true)
}

func (o *Orchestrator) run(ctx context.Context, deleting bool) error {
	global := make(chan struct{}, o.Config.Parallelism)
	targetLimits := map[string]chan struct{}{}
	targets := map[string]Target{}
	for _, target := range o.Config.Targets {
		targets[target.Name] = target
		targetLimits[target.Name] = make(chan struct{}, target.Parallelism)
	}

	names := make([]string, 0, len(o.State.Clusters))
	for name := range o.State.Clusters {
		names = append(names, name)
	}
	sort.Strings(names)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var result error
	for _, name := range names {
		item := o.State.Clusters[name]
		if deleting && item.Stage == StageDeleted {
			continue
		}
		if !deleting && item.Stage == StageRegistered {
			continue
		}
		target, found := targets[item.Target]
		if !found {
			result = errors.Join(result, fmt.Errorf("cluster %s references unknown target %s", name, item.Target))
			continue
		}

		wg.Add(1)
		go func(item *ClusterState, target Target) {
			defer wg.Done()
			select {
			case global <- struct{}{}:
				defer func() { <-global }()
			case <-ctx.Done():
				mu.Lock()
				result = errors.Join(result, ctx.Err())
				mu.Unlock()
				return
			}

			var err error
			if deleting {
				err = o.deleteOne(ctx, item, target, targetLimits[target.Name])
			} else {
				err = o.createOne(ctx, item, target, targetLimits[target.Name])
			}
			if err != nil {
				_ = o.State.Update(item.Name, StageFailed, err)
				mu.Lock()
				result = errors.Join(result, fmt.Errorf("%s: %w", item.Name, err))
				mu.Unlock()
			}
		}(item, target)
	}
	wg.Wait()
	return result
}

func (o *Orchestrator) createOne(ctx context.Context, item *ClusterState, target Target, targetLimit chan struct{}) error {
	stage := item.Stage
	if stage == StageFailed {
		stage = item.ResumeStage
	}

	// CLI.Create applies a manifest, waits for readiness, and regenerates the kubeconfig, so
	// it is safe to repeat. It is re-run when the k3k stage was never reached, and also when
	// the kubeconfig is gone, which happens when the artifacts directory was cleaned while the
	// state file was kept.
	if !k3kCreated(stage) || !fileExists(item.Kubeconfig) {
		if err := o.State.Update(item.Name, StageK3KCreating, nil); err != nil {
			return err
		}
		Progress("%s: creating k3k cluster on %s (timeout %s)", item.Name, target.Name, o.Config.CreateTimeout)
		select {
		case targetLimit <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		err := o.CLI.Create(ctx, item, target, o.Config)
		<-targetLimit
		if err != nil {
			return err
		}
		Progress("%s: k3k cluster ready, kubeconfig written", item.Name)
		if _, err := os.Stat(item.Kubeconfig); err != nil {
			return fmt.Errorf("generated kubeconfig: %w", err)
		}
		if err := o.State.Update(item.Name, StageK3KReady, nil); err != nil {
			return err
		}
	}

	if item.RancherManagementID == "" {
		if err := o.State.Update(item.Name, StageRancherCreating, nil); err != nil {
			return err
		}
		Progress("%s: creating Rancher imported cluster (version management: %s)",
			item.Name, o.Config.Rancher.VersionManagement)
		labels := map[string]string{"provider.cattle.io": "k3k", "k3k-bulk.io/batch": o.Config.Batch, "k3k-bulk.io/target": target.Name}
		annotations := map[string]string{versionManagementAnnotation: o.Config.Rancher.VersionManagement}
		managementID, err := o.Rancher.CreateImportedCluster(ctx, item, labels, annotations)
		if err != nil {
			return err
		}
		Progress("%s: Rancher cluster is %s", item.Name, managementID)
		// Recorded under the state lock, because sibling goroutines serialize the whole state
		// file while this runs.
		if err := o.State.SetManagementID(item.Name, managementID); err != nil {
			return err
		}
		if err := o.State.Update(item.Name, StageRancherCreated, nil); err != nil {
			return err
		}
	}

	manifest, err := o.Rancher.RegistrationManifest(ctx, item.RancherManagementID)
	if err != nil {
		return err
	}
	if err := o.State.Update(item.Name, StageRegistering, nil); err != nil {
		return err
	}
	Progress("%s: applying Rancher registration manifest", item.Name)
	if err := o.CLI.Register(ctx, item, manifest); err != nil {
		return err
	}

	// Registration is bounded separately so one unreachable cluster reports a useful reason
	// instead of consuming the whole batch timeout.
	registerCtx := ctx
	if o.Config.RegisterTimeout > 0 {
		var cancel context.CancelFunc
		registerCtx, cancel = context.WithTimeout(ctx, o.Config.RegisterTimeout)
		defer cancel()
	}
	Progress("%s: waiting up to %s for the cluster agent to connect to Rancher",
		item.Name, o.Config.RegisterTimeout)
	if err := o.Rancher.WaitImportedClusterReady(registerCtx, item.RancherManagementID); err != nil {
		return err
	}
	Progress("%s: registered with Rancher", item.Name)
	return o.State.Update(item.Name, StageRegistered, nil)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func k3kCreated(stage Stage) bool {
	switch stage {
	case StageK3KReady, StageRancherCreating, StageRancherCreated, StageRegistering, StageRegistered:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) deleteOne(ctx context.Context, item *ClusterState, target Target, targetLimit chan struct{}) error {
	if err := o.State.Update(item.Name, StageDeletingRancher, nil); err != nil {
		return err
	}
	if err := o.Rancher.DeleteImportedCluster(ctx, item); err != nil {
		return err
	}
	if err := o.State.Update(item.Name, StageDeletingK3K, nil); err != nil {
		return err
	}
	select {
	case targetLimit <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := o.CLI.Delete(ctx, item, target)
	if err == nil {
		err = o.CLI.WaitDeleted(ctx, item, target)
	}
	<-targetLimit
	if err != nil {
		return err
	}
	return o.State.Update(item.Name, StageDeleted, nil)
}
