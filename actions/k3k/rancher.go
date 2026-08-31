package k3k

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rancher/norman/types"
	provisioningv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/rancher/shepherd/clients/rancher"
	management "github.com/rancher/shepherd/clients/rancher/generated/management/v3"
	stevev1 "github.com/rancher/shepherd/clients/rancher/v1"
	"github.com/rancher/shepherd/extensions/defaults/stevetypes"
	"github.com/rancher/shepherd/pkg/clientbase"
	"github.com/rancher/shepherd/pkg/session"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const pollInterval = 2 * time.Second

type RancherAPI interface {
	CreateImportedCluster(ctx context.Context, item *ClusterState, labels map[string]string) error
	RegistrationManifest(ctx context.Context, managementID string) ([]byte, error)
	WaitImportedClusterReady(ctx context.Context, managementID string) error
	DeleteImportedCluster(ctx context.Context, item *ClusterState) error
}

type RancherClient struct {
	client *rancher.Client
	mu     sync.Mutex
}

func NewRancherClient(config RancherConfig) (*RancherClient, error) {
	insecure := config.Insecure
	cleanup := false
	var caPEM string
	if config.CAFile != "" {
		data, err := os.ReadFile(config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read Rancher CA: %w", err)
		}
		caPEM = string(data)
	}
	host := strings.TrimPrefix(strings.TrimPrefix(strings.TrimRight(config.Host, "/"), "https://"), "http://")
	if host == "" {
		return nil, fmt.Errorf("Rancher host is empty")
	}
	cfg := &rancher.Config{
		Host: host, AdminToken: config.Token, Insecure: &insecure, Cleanup: &cleanup,
		CAFile: config.CAFile, CACerts: caPEM,
	}
	client, err := rancher.NewClientForConfig(config.Token, cfg, session.NewSession())
	if err != nil {
		return nil, err
	}
	return &RancherClient{client: client}, nil
}

func (r *RancherClient) CreateImportedCluster(ctx context.Context, item *ClusterState, labels map[string]string) error {
	r.mu.Lock()
	if existing, err := r.client.Steve.SteveType(stevetypes.Provisioning).ByID(item.RancherProvisioningID); err == nil {
		r.mu.Unlock()
		return r.populateManagementID(ctx, item, existing)
	} else if !clientbase.IsNotFound(err) {
		r.mu.Unlock()
		return err
	}

	desired := &provisioningv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name: item.Name, Namespace: strings.SplitN(item.RancherProvisioningID, "/", 2)[0], Labels: labels,
	}}
	created, err := r.client.Steve.SteveType(stevetypes.Provisioning).Create(desired)
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("create Rancher provisioning cluster: %w", err)
	}
	return r.populateManagementID(ctx, item, created)
}

func (r *RancherClient) populateManagementID(ctx context.Context, item *ClusterState, object *stevev1.SteveAPIObject) error {
	return poll(ctx, func() (bool, error) {
		r.mu.Lock()
		current, err := r.client.Steve.SteveType(stevetypes.Provisioning).ByID(object.ID)
		r.mu.Unlock()
		if err != nil {
			return false, err
		}
		cluster := &provisioningv1.Cluster{}
		if err := stevev1.ConvertToK8sType(current, cluster); err != nil {
			return false, err
		}
		if cluster.Status.ClusterName == "" {
			return false, nil
		}
		item.RancherManagementID = cluster.Status.ClusterName
		return true, nil
	})
}

func (r *RancherClient) RegistrationManifest(ctx context.Context, managementID string) ([]byte, error) {
	token, err := r.waitRegistrationToken(ctx, managementID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	response, err := r.client.Management.Ops.RawHttpRequest(http.MethodGet, token.ManifestURL, nil, nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("registration manifest request returned HTTP %d", response.StatusCode)
	}
	return response.Body, nil
}

func (r *RancherClient) waitRegistrationToken(ctx context.Context, managementID string) (*management.ClusterRegistrationToken, error) {
	var token *management.ClusterRegistrationToken
	err := poll(ctx, func() (bool, error) {
		r.mu.Lock()
		collection, err := r.client.Management.ClusterRegistrationToken.ListAll(&types.ListOpts{Filters: map[string]interface{}{
			"clusterId": managementID,
		}})
		r.mu.Unlock()
		if err != nil {
			return false, err
		}
		for i := range collection.Data {
			if collection.Data[i].Token != "" && collection.Data[i].ManifestURL != "" {
				token = &collection.Data[i]
				return true, nil
			}
		}
		return false, nil
	})
	return token, err
}

func (r *RancherClient) WaitImportedClusterReady(ctx context.Context, managementID string) error {
	return poll(ctx, func() (bool, error) {
		r.mu.Lock()
		cluster, err := r.client.Management.Cluster.ByID(managementID)
		r.mu.Unlock()
		if err != nil {
			return false, err
		}
		for _, condition := range cluster.Conditions {
			if condition.Status == "False" && condition.Message != "" &&
				(condition.Type == "Ready" || condition.Type == "Connected" || condition.Type == "AgentDeployed") {
				return false, fmt.Errorf("Rancher cluster %s condition %s is false: %s", managementID, condition.Type, condition.Message)
			}
		}
		return importedClusterReady(cluster), nil
	})
}

func importedClusterReady(cluster *management.Cluster) bool {
	required := map[string]bool{
		"AgentDeployed": false, "Ready": false, "SystemAccountCreated": false, "ServiceAccountSecretsMigrated": false,
	}
	for _, condition := range cluster.Conditions {
		if _, found := required[condition.Type]; found && condition.Status == "True" {
			required[condition.Type] = true
		}
	}
	for _, ready := range required {
		if !ready {
			return false
		}
	}
	return true
}

func (r *RancherClient) DeleteImportedCluster(ctx context.Context, item *ClusterState) error {
	r.mu.Lock()
	object, err := r.client.Steve.SteveType(stevetypes.Provisioning).ByID(item.RancherProvisioningID)
	if err == nil {
		err = r.client.Steve.SteveType(stevetypes.Provisioning).Delete(object)
	}
	r.mu.Unlock()
	if err != nil && !clientbase.IsNotFound(err) {
		return err
	}

	return poll(ctx, func() (bool, error) {
		r.mu.Lock()
		_, provisioningErr := r.client.Steve.SteveType(stevetypes.Provisioning).ByID(item.RancherProvisioningID)
		var managementErr error
		if item.RancherManagementID != "" {
			_, managementErr = r.client.Management.Cluster.ByID(item.RancherManagementID)
		}
		r.mu.Unlock()
		if provisioningErr != nil && !clientbase.IsNotFound(provisioningErr) {
			return false, provisioningErr
		}
		if item.RancherManagementID != "" && managementErr != nil && !clientbase.IsNotFound(managementErr) {
			return false, managementErr
		}
		managementDeleted := item.RancherManagementID == "" || clientbase.IsNotFound(managementErr)
		return clientbase.IsNotFound(provisioningErr) && managementDeleted, nil
	})
}

func poll(ctx context.Context, condition func() (bool, error)) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		done, err := condition()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
