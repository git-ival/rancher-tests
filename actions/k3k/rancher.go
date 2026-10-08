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
	// CreateImportedCluster returns the Rancher management cluster ID. It does not write to
	// item, because the caller records the ID through the batch state's lock.
	CreateImportedCluster(ctx context.Context, item *ClusterState, labels, annotations map[string]string) (string, error)
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

// CreateImportedCluster creates the Rancher provisioning Cluster, or adopts an existing one so
// that a resumed run does not fail or duplicate it.
func (r *RancherClient) CreateImportedCluster(ctx context.Context, item *ClusterState, labels, annotations map[string]string) (string, error) {
	r.mu.Lock()
	if existing, err := r.client.Steve.SteveType(stevetypes.Provisioning).ByID(item.RancherProvisioningID); err == nil {
		r.mu.Unlock()
		return r.managementID(ctx, existing)
	} else if !clientbase.IsNotFound(err) {
		r.mu.Unlock()
		return "", err
	}

	// Annotations set here are copied onto the management cluster Rancher derives from this
	// object, which is where the version management switch is read.
	desired := &provisioningv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name:        item.Name,
		Namespace:   strings.SplitN(item.RancherProvisioningID, "/", 2)[0],
		Labels:      labels,
		Annotations: annotations,
	}}
	created, err := r.client.Steve.SteveType(stevetypes.Provisioning).Create(desired)
	r.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("create Rancher provisioning cluster: %w", err)
	}
	return r.managementID(ctx, created)
}

// managementID waits for Rancher to assign the generated c-m-* management cluster name.
func (r *RancherClient) managementID(ctx context.Context, object *stevev1.SteveAPIObject) (string, error) {
	var managementID string
	err := poll(ctx, func() (bool, error) {
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
		managementID = cluster.Status.ClusterName
		return true, nil
	})
	return managementID, err
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

// WaitImportedClusterReady polls until a freshly imported cluster reports ready.
//
// A false Ready, Connected, or AgentDeployed condition is NOT treated as terminal. Rancher
// sets Ready=False with "Cluster agent is not connected" from the moment the cluster object
// exists until the cattle-cluster-agent deploys and dials home, which is the normal state for
// the first minutes after the registration manifest is applied. Failing on it aborts every
// import immediately.
//
// Transient API errors are tolerated for the same reason. The most recent reason is retained
// so that a genuine timeout reports why the cluster never became ready instead of a bare
// context deadline.
func (r *RancherClient) WaitImportedClusterReady(ctx context.Context, managementID string) error {
	var lastReason string
	started := time.Now()
	lastReport := started

	err := poll(ctx, func() (bool, error) {
		r.mu.Lock()
		cluster, err := r.client.Management.Cluster.ByID(managementID)
		r.mu.Unlock()
		if err != nil {
			lastReason = "querying Rancher failed: " + err.Error()
			return false, nil
		}
		lastReason = notReadyReason(cluster)
		if lastReason == "" {
			return true, nil
		}

		// This wait routinely lasts minutes while cattle-cluster-agent starts and connects.
		// Restate the reason periodically so it is distinguishable from a hang.
		if time.Since(lastReport) >= progressInterval {
			lastReport = time.Now()
			Progress("%s: waiting for Rancher import (%s elapsed): %s",
				managementID, time.Since(started).Round(time.Second), lastReason)
		}
		return false, nil
	})

	if err != nil && lastReason != "" {
		return fmt.Errorf("Rancher cluster %s did not become ready after %s: %s (%w)",
			managementID, time.Since(started).Round(time.Second), lastReason, err)
	}
	return err
}

// notReadyReason returns why a cluster is not yet ready, or an empty string when it is.
func notReadyReason(cluster *management.Cluster) string {
	if cluster.Transitioning == "error" && cluster.TransitioningMessage != "" {
		return "cluster entered error state: " + cluster.TransitioningMessage
	}

	// Report the most informative condition rather than the first missing one.
	for _, conditionType := range []string{"Ready", "Connected", "AgentDeployed"} {
		for _, condition := range cluster.Conditions {
			if condition.Type != conditionType || condition.Status == "True" {
				continue
			}
			detail := condition.Message
			if detail == "" {
				detail = condition.Reason
			}
			if detail == "" {
				detail = "status " + condition.Status
			}
			return fmt.Sprintf("condition %s is not true: %s", conditionType, detail)
		}
	}

	if missing := missingConditions(cluster); len(missing) > 0 {
		return "waiting for conditions: " + strings.Join(missing, ", ")
	}
	if cluster.State != "" && cluster.State != "active" {
		return "cluster state is " + cluster.State
	}
	return ""
}

// missingConditions lists the required readiness conditions that are not yet true.
func missingConditions(cluster *management.Cluster) []string {
	required := requiredConditions()
	for _, condition := range cluster.Conditions {
		if _, found := required[condition.Type]; found && condition.Status == "True" {
			required[condition.Type] = true
		}
	}

	var missing []string
	for _, name := range requiredConditionOrder {
		if !required[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// requiredConditionOrder lists the conditions an imported cluster must report as true. It
// matches shepherd's IsImportedClusterReady so both agree on what "imported and ready" means.
var requiredConditionOrder = []string{
	"AgentDeployed", "Ready", "SystemAccountCreated", "ServiceAccountSecretsMigrated",
}

func requiredConditions() map[string]bool {
	required := make(map[string]bool, len(requiredConditionOrder))
	for _, name := range requiredConditionOrder {
		required[name] = false
	}
	return required
}

func importedClusterReady(cluster *management.Cluster) bool {
	return len(missingConditions(cluster)) == 0
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
