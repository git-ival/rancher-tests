package k3k

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ClusterSpec is the subset of the k3k.io/v1beta1 Cluster spec that k3kbulk supports.
//
// Field names and shapes mirror the CRD exactly, so a rendered manifest is accepted by the
// k3k API server without translation. JSON tags are used rather than YAML tags because the
// embedded corev1 types (EnvVar, ResourceList, ResourceRequirements, Affinity) and
// resource.Quantity only carry JSON tags and custom UnmarshalJSON implementations; the spec
// is therefore parsed with sigs.k8s.io/yaml rather than gopkg.in/yaml.v3.
//
// The type is deliberately a scoped subset rather than an import of v1beta1.ClusterSpec so
// that strict decoding rejects unsupported fields with a clear error instead of accepting
// configuration that k3kbulk does not exercise.
type ClusterSpec struct {
	// Version is the K3s version for the virtual cluster, e.g. v1.36.2-k3s1.
	Version string `json:"version,omitempty"`
	// Mode is the provisioning mode: shared, virtual, or hcp. Immutable after creation.
	Mode string `json:"mode,omitempty"`
	// Servers is the number of K3s server (control plane) pods.
	Servers *int32 `json:"servers,omitempty"`
	// Agents is the number of K3s agent pods. Only meaningful in virtual mode.
	Agents *int32 `json:"agents,omitempty"`
	// Persistence controls how etcd data is persisted.
	Persistence *PersistenceConfig `json:"persistence,omitempty"`
	// Expose controls how the virtual cluster API server is reachable.
	Expose *ExposeConfig `json:"expose,omitempty"`
	// NodeSelector constrains where server and agent pods are scheduled.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// TLSSANs are subject alternative names for the server certificate.
	TLSSANs []string `json:"tlsSANs,omitempty"`
	// ServerArgs are extra arguments passed to K3s server pods.
	ServerArgs []string `json:"serverArgs,omitempty"`
	// AgentArgs are extra arguments passed to K3s agent pods.
	AgentArgs []string `json:"agentArgs,omitempty"`
	// ServerEnvs are environment variables set on server pods.
	ServerEnvs []corev1.EnvVar `json:"serverEnvs,omitempty"`
	// AgentEnvs are environment variables set on agent pods.
	AgentEnvs []corev1.EnvVar `json:"agentEnvs,omitempty"`
	// ServerLimit sets resource limits for server pods.
	ServerLimit corev1.ResourceList `json:"serverLimit,omitempty"`
	// WorkerLimit sets resource limits for agent pods.
	WorkerLimit corev1.ResourceList `json:"workerLimit,omitempty"`
	// ServerResources sets limits and requests for server pods. Overrides ServerLimit.
	ServerResources *corev1.ResourceRequirements `json:"serverResources,omitempty"`
	// WorkerResources sets limits and requests for agent pods. Overrides WorkerLimit.
	WorkerResources *corev1.ResourceRequirements `json:"workerResources,omitempty"`
	// ServerAffinity sets node and pod affinity rules for server pods.
	ServerAffinity *corev1.Affinity `json:"serverAffinity,omitempty"`
	// AgentAffinity sets node and pod affinity rules for agent pods.
	AgentAffinity *corev1.Affinity `json:"agentAffinity,omitempty"`
}

// PersistenceConfig specifies options for persisting etcd data.
type PersistenceConfig struct {
	// Type is the persistence mode: dynamic or ephemeral.
	Type string `json:"type,omitempty"`
	// StorageClassName is the StorageClass used for the PVC in dynamic mode.
	StorageClassName *string `json:"storageClassName,omitempty"`
	// StorageRequestSize is the requested PVC size in dynamic mode, e.g. 30Gi.
	StorageRequestSize *resource.Quantity `json:"storageRequestSize,omitempty"`
}

// ExposeConfig specifies how the virtual cluster API server is exposed.
// Ingress, LoadBalancer, and NodePort are mutually exclusive.
type ExposeConfig struct {
	// Ingress exposes the API server through an Ingress.
	Ingress *IngressConfig `json:"ingress,omitempty"`
	// LoadBalancer exposes the API server through a LoadBalancer Service.
	LoadBalancer *LoadBalancerConfig `json:"loadBalancer,omitempty"`
	// NodePort exposes the API server through a NodePort Service.
	NodePort *NodePortConfig `json:"nodePort,omitempty"`
	// Annotations are added to the generated Service.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// IngressConfig specifies options for exposing the API server through an Ingress.
type IngressConfig struct {
	// Annotations are added to the generated Ingress.
	Annotations map[string]string `json:"annotations,omitempty"`
	// IngressClassName is the IngressClass used for the generated Ingress.
	IngressClassName string `json:"ingressClassName,omitempty"`
}

// LoadBalancerConfig specifies ports for a LoadBalancer Service.
type LoadBalancerConfig struct {
	// ServerPort is the K3s server port. Defaults to 443. Zero or negative disables it.
	ServerPort *int32 `json:"serverPort,omitempty"`
	// ETCDPort is the etcd port. Defaults to 2379. Zero or negative disables it.
	ETCDPort *int32 `json:"etcdPort,omitempty"`
}

// NodePortConfig specifies ports for a NodePort Service.
type NodePortConfig struct {
	// ServerPort is the node port for the K3s server. Random in 30000-32767 if unset.
	ServerPort *int32 `json:"serverPort,omitempty"`
	// ETCDPort is the node port for etcd. Random in 30000-32767 if unset.
	ETCDPort *int32 `json:"etcdPort,omitempty"`
}

// Cluster provisioning modes.
const (
	ModeShared  = "shared"
	ModeVirtual = "virtual"
	ModeHCP     = "hcp"
)

// Persistence modes.
const (
	PersistenceDynamic   = "dynamic"
	PersistenceEphemeral = "ephemeral"
)

// SpecDoc describes one configuration field for the annotated config generator.
//
// Doc, Default, and Enum drive the reflected skeleton. Example is set only for fields whose
// schemas are too large to expand usefully (Affinity is roughly 875 lines of CRD schema); for
// those the generator emits the curated Example block instead of recursing.
type SpecDoc struct {
	Doc     string
	Default string
	Enum    []string
	// Placeholder overrides the sample value emitted for a commented field. It is used where
	// the zero value would be misleading, such as a node port where 0 means "do not expose".
	Placeholder string
	// Example replaces reflection for this field when set. Lines are emitted verbatim,
	// each already indented relative to the field, and each prefixed as a comment.
	Example []string
	// Link is a reference URL appended to the comment block.
	Link string
}

const affinityLink = "https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#affinity-v1-core"
const resourcesLink = "https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#resourcerequirements-v1-core"

// specDocs documents every supported clusterSpec field, keyed by dotted JSON path.
// The generator walks the ClusterSpec type and looks each field up here.
var specDocs = map[string]SpecDoc{
	"version": {
		Doc:         "K3s version for the virtual cluster. Defaults to the host node's Kubernetes version. Only K3s distributions are supported.",
		Placeholder: "v1.36.2-k3s1",
	},
	"mode": {
		Doc:     "Cluster provisioning mode. Immutable after creation.",
		Default: "shared",
		Enum:    []string{"shared", "virtual", "hcp"},
	},
	"servers": {
		Doc:     "Number of K3s server (control plane) pods. Must be at least 1.",
		Default: "1",
	},
	"agents": {
		Doc:     "Number of K3s agent pods. Only supported when mode is virtual.",
		Default: "0",
	},
	"persistence": {
		Doc: "Options for persisting etcd data.",
	},
	"persistence.type": {
		Doc:     "Persistence mode. Ephemeral keeps no state across pod restarts.",
		Default: "dynamic",
		Enum:    []string{"dynamic", "ephemeral"},
	},
	"persistence.storageClassName": {
		Doc:         "StorageClass for the PVC. Only used when type is dynamic. Defaults to the cluster's default StorageClass.",
		Placeholder: "local-path",
	},
	"persistence.storageRequestSize": {
		Doc:     "Requested PVC size. Only used when type is dynamic. Immutable after creation.",
		Default: "2G",
	},
	"expose": {
		Doc: "How the virtual cluster API server is reached. ingress, loadBalancer, and nodePort are mutually exclusive; set at most one. When expose is omitted entirely, k3kbulk defaults to nodePort so the generated kubeconfig is reachable.",
	},
	"expose.ingress": {
		Doc: "Expose the API server through an Ingress. The Ingress rule hosts are taken from tlsSANs, and tlsSANs[0] becomes the kubeconfig server URL.",
	},
	"expose.ingress.annotations": {
		Doc:         "Annotations added to the generated Ingress.",
		Placeholder: `{ nginx.ingress.kubernetes.io/ssl-passthrough: "true" }`,
	},
	"expose.ingress.ingressClassName": {
		Doc:         "IngressClass used for the generated Ingress.",
		Placeholder: "nginx",
	},
	"expose.loadBalancer": {
		Doc: "Expose the API server through a LoadBalancer Service. After the cluster is ready k3kbulk discovers the allocated address and adds it to tlsSANs; if no address appears it is skipped.",
	},
	"expose.loadBalancer.serverPort": {
		Doc:     "Port for the K3s server. Zero or negative does not expose the port.",
		Default: "443",
	},
	"expose.loadBalancer.etcdPort": {
		Doc:     "Port for etcd. Zero or negative does not expose the port.",
		Default: "2379",
	},
	"expose.nodePort": {
		Doc: "Expose the API server through a NodePort Service. This is the k3kbulk default.",
	},
	"expose.nodePort.serverPort": {
		Doc:         "Node port for the K3s server. A random port in 30000-32767 is allocated when unset. Use a template expression when several clusters share a host.",
		Placeholder: "30443",
	},
	"expose.nodePort.etcdPort": {
		Doc:         "Node port for etcd. A random port in 30000-32767 is allocated when unset.",
		Placeholder: "32379",
	},
	"expose.annotations": {
		Doc:         "Annotations added to the generated Service.",
		Placeholder: `{ service.beta.kubernetes.io/aws-load-balancer-internal: "true" }`,
	},
	"nodeSelector": {
		Doc:         "Node labels constraining where server and agent pods are scheduled. In shared mode this also applies to workloads.",
		Placeholder: "{ disktype: ssd }",
	},
	"tlsSANs": {
		Doc:         "Subject alternative names for the server certificate. With expose.ingress these also become the Ingress rule hosts, and tlsSANs[0] is used as the kubeconfig server URL.",
		Placeholder: `["{{ .Name }}.k3k.example.com"]`,
	},
	"serverArgs": {
		Doc:         "Extra arguments for K3s server pods, for example --disable=traefik.",
		Placeholder: `["--disable=traefik"]`,
	},
	"agentArgs": {
		Doc:         "Extra arguments for K3s agent pods, for example --node-label=role=worker.",
		Placeholder: `["--node-label=role=worker"]`,
	},
	"serverEnvs": {
		Doc:         "Environment variables set on server pods.",
		Placeholder: `[{ name: HTTP_PROXY, value: "http://proxy.example.com" }]`,
	},
	"agentEnvs": {
		Doc:         "Environment variables set on agent pods.",
		Placeholder: `[{ name: HTTP_PROXY, value: "http://proxy.example.com" }]`,
	},
	"serverLimit": {
		Doc:         "Resource limits for server pods. Cannot be combined with serverResources.",
		Placeholder: `{ cpu: "1", memory: 2Gi }`,
	},
	"workerLimit": {
		Doc:         "Resource limits for agent pods. Cannot be combined with workerResources.",
		Placeholder: `{ cpu: "1", memory: 2Gi }`,
	},
	"serverResources": {
		Doc:  "Limits and requests for server pods. k3k applies these as pod-level resources, which requires the PodLevelResources feature gate on the host cluster. Cannot be combined with serverLimit.",
		Link: resourcesLink,
		Example: []string{
			"serverResources:",
			"  limits:",
			"    cpu: \"2\"",
			"    memory: 4Gi",
			"  requests:",
			"    cpu: \"1\"",
			"    memory: 2Gi",
		},
	},
	"workerResources": {
		Doc:  "Limits and requests for agent pods. k3k applies these as pod-level resources, which requires the PodLevelResources feature gate on the host cluster. Cannot be combined with workerLimit.",
		Link: resourcesLink,
		Example: []string{
			"workerResources:",
			"  limits:",
			"    cpu: \"2\"",
			"    memory: 4Gi",
			"  requests:",
			"    cpu: \"1\"",
			"    memory: 2Gi",
		},
	},
	"serverAffinity": {
		Doc:  "Affinity rules for server pods, including node affinity and pod affinity/anti-affinity.",
		Link: affinityLink,
		Example: []string{
			"serverAffinity:",
			"  nodeAffinity:",
			"    requiredDuringSchedulingIgnoredDuringExecution:",
			"      nodeSelectorTerms:",
			"        - matchExpressions:",
			"            - key: disktype",
			"              operator: In",
			"              values: [\"ssd\"]",
			"  podAntiAffinity:",
			"    preferredDuringSchedulingIgnoredDuringExecution:",
			"      - weight: 100",
			"        podAffinityTerm:",
			"          topologyKey: kubernetes.io/hostname",
			"          labelSelector:",
			"            matchLabels:",
			"              role: server",
		},
	},
	"agentAffinity": {
		Doc:  "Affinity rules for agent pods, including node affinity and pod affinity/anti-affinity.",
		Link: affinityLink,
		Example: []string{
			"agentAffinity:",
			"  nodeAffinity:",
			"    requiredDuringSchedulingIgnoredDuringExecution:",
			"      nodeSelectorTerms:",
			"        - matchExpressions:",
			"            - key: disktype",
			"              operator: In",
			"              values: [\"ssd\"]",
		},
	},
}
