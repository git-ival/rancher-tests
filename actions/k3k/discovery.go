package k3k

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rancher/norman/types"
	management "github.com/rancher/shepherd/clients/rancher/generated/management/v3"
	"k8s.io/client-go/tools/clientcmd"
)

func (r *RancherClient) DiscoverTargets(ctx context.Context, config *Config) error {
	if config.DiscoverTargets == nil {
		return config.ValidateResolvedTargets()
	}

	r.mu.Lock()
	collection, err := r.client.Management.Cluster.ListAll(&types.ListOpts{})
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("list Rancher clusters: %w", err)
	}
	sort.Slice(collection.Data, func(i, j int) bool { return collection.Data[i].Name < collection.Data[j].Name })

	existing := map[string]bool{}
	for _, target := range config.Targets {
		existing[target.Name] = true
	}
	for i := range collection.Data {
		if err := ctx.Err(); err != nil {
			return err
		}
		cluster := &collection.Data[i]
		clusterType := classifyCluster(cluster)
		if !targetMatches(cluster, clusterType, config.DiscoverTargets) || existing[cluster.Name] {
			continue
		}
		target, err := r.targetFromCluster(cluster, clusterType, config)
		if err != nil {
			return fmt.Errorf("discover target %s (%s): %w", cluster.Name, clusterType, err)
		}
		config.Targets = append(config.Targets, *target)
		existing[target.Name] = true
	}
	return config.ValidateResolvedTargets()
}

func targetMatches(cluster *management.Cluster, clusterType string, discovery *TargetDiscovery) bool {
	if !strings.HasPrefix(cluster.Name, discovery.Prefix) {
		return false
	}
	if cluster.State != "" && cluster.State != "active" {
		return false
	}
	if cluster.Labels["provider.cattle.io"] == "k3k" {
		return false
	}
	if len(discovery.Types) == 0 {
		return clusterType != "local" && clusterType != "unknown"
	}
	for _, requested := range discovery.Types {
		if requested == clusterType || (requested == "hosted" && isHostedType(clusterType)) {
			return true
		}
	}
	return false
}

func classifyCluster(cluster *management.Cluster) string {
	if cluster.ID == "local" || cluster.Internal || machineProvider(cluster) == "local" {
		return "local"
	}
	if cluster.Labels["provider.cattle.io"] == "harvester" || cluster.Provider == "harvester" || machineProvider(cluster) == "harvester" {
		return "harvester"
	}
	provider := machineProvider(cluster)
	if hostedType := normalizedHostedType(provider); hostedType != "" {
		return hostedType
	}
	if provider == "imported" || hostedConfigImported(cluster) || cluster.Driver == "imported" {
		return "imported"
	}
	if provider == "custom" {
		return "custom"
	}
	if provider != "" {
		return "nodedriver"
	}
	if cluster.AKSConfig != nil {
		return hostedOrImported("aks", cluster.AKSConfig.Imported)
	}
	if cluster.EKSConfig != nil {
		return hostedOrImported("eks", cluster.EKSConfig.Imported)
	}
	if cluster.GKEConfig != nil {
		return hostedOrImported("gke", cluster.GKEConfig.Imported)
	}
	if cluster.AliConfig != nil {
		return hostedOrImported("ali", cluster.AliConfig.Imported)
	}
	if cluster.Info != nil && cluster.Info.ProvisioningClusterRef != nil {
		return "nodedriver"
	}
	return "unknown"
}

func machineProvider(cluster *management.Cluster) string {
	if cluster.Info == nil {
		return ""
	}
	return strings.ToLower(cluster.Info.MachineProvider)
}

func hostedConfigImported(cluster *management.Cluster) bool {
	return (cluster.AKSConfig != nil && cluster.AKSConfig.Imported) ||
		(cluster.EKSConfig != nil && cluster.EKSConfig.Imported) ||
		(cluster.GKEConfig != nil && cluster.GKEConfig.Imported) ||
		(cluster.AliConfig != nil && cluster.AliConfig.Imported)
}

func isHostedType(clusterType string) bool {
	switch clusterType {
	case "aks", "eks", "gke", "ali":
		return true
	default:
		return false
	}
}

func normalizedHostedType(provider string) string {
	provider = strings.ToLower(provider)
	for _, hostedType := range []string{"aks", "eks", "gke", "ali"} {
		if provider == hostedType || strings.Contains(provider, hostedType) {
			return hostedType
		}
	}
	return ""
}

func hostedOrImported(hostedType string, imported bool) string {
	if imported {
		return "imported"
	}
	return hostedType
}

func (r *RancherClient) targetFromCluster(cluster *management.Cluster, clusterType string, config *Config) (*Target, error) {
	r.mu.Lock()
	kubeconfig, err := r.client.Management.Cluster.ActionGenerateKubeconfig(cluster)
	if err == nil {
		if kubeconfig.Config == "" {
			err = fmt.Errorf("Rancher returned an empty kubeconfig")
		} else if _, parseErr := clientcmd.Load([]byte(kubeconfig.Config)); parseErr != nil {
			err = fmt.Errorf("parse generated kubeconfig: %w", parseErr)
		}
	}
	if err == nil {
		var nodes *management.NodeCollection
		nodes, err = r.client.Management.Node.ListAll(&types.ListOpts{Filters: map[string]interface{}{"clusterId": cluster.ID}})
		if err == nil {
			address := selectNodeAddress(nodes.Data, config.DiscoverTargets)
			if address == "" {
				err = fmt.Errorf("no %s node address is available", config.DiscoverTargets.Address)
			} else {
				dir := filepath.Join(config.ArtifactsDir, config.Batch, "targets")
				if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
					err = mkdirErr
				} else {
					path := filepath.Join(dir, cluster.Name+".yaml")
					if writeErr := os.WriteFile(path, []byte(kubeconfig.Config), 0o600); writeErr != nil {
						err = writeErr
					} else {
						r.mu.Unlock()
						return &Target{Name: cluster.Name, Type: clusterType, Kubeconfig: path, KubeconfigServer: address,
							Weight: config.DiscoverTargets.Weight, Parallelism: config.DiscoverTargets.Parallelism}, nil
					}
				}
			}
		}
	}
	r.mu.Unlock()
	return nil, err
}

func selectNodeAddress(nodes []management.Node, discovery *TargetDiscovery) string {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	for _, node := range nodes {
		if node.State != "" && node.State != "active" {
			continue
		}
		if discovery.Address == "internal" && node.IPAddress != "" {
			return node.IPAddress
		}
		if discovery.Address == "external" && node.ExternalIPAddress != "" {
			return node.ExternalIPAddress
		}
	}
	if discovery.AllowInternalAddress && discovery.Address == "external" {
		for _, node := range nodes {
			if node.IPAddress != "" {
				return node.IPAddress
			}
		}
	}
	return ""
}
