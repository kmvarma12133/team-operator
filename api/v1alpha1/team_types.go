package v1alpha1

import (
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeletionPolicy says what happens to a team's namespace when its Team is deleted.
// +kubebuilder:validation:Enum=Retain;Delete
type DeletionPolicy string

const (
	// DeletionPolicyRetain keeps the namespace, its workloads and its guardrails.
	DeletionPolicyRetain DeletionPolicy = "Retain"
	// DeletionPolicyDelete lets garbage collection delete the namespace with the Team.
	DeletionPolicyDelete DeletionPolicy = "Delete"
)

// TeamSpec is what a team asks for: who is in it and how much it may use.
type TeamSpec struct {
	// Members are bound to the built-in "edit" ClusterRole in the team's namespace.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	// +listType=atomic
	Members []rbacv1.Subject `json:"members"`

	// CostCenter is copied to the namespace as a label for cost allocation.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`
	// +optional
	CostCenter string `json:"costCenter,omitempty"`

	// Quota caps the namespace's total requests and pod count.
	// +kubebuilder:default={}
	// +optional
	Quota Quota `json:"quota,omitempty"`

	// ContainerDefaults are applied by a LimitRange to containers that set none.
	// +kubebuilder:default={}
	// +optional
	ContainerDefaults ContainerDefaults `json:"containerDefaults,omitempty"`

	// DeletionPolicy defaults to Retain, so deleting a Team never deletes workloads by accident.
	// +kubebuilder:default=Retain
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// Quota becomes the namespace's ResourceQuota.
type Quota struct {
	// +kubebuilder:default="4"
	// +optional
	CPU resource.Quantity `json:"cpu,omitempty"`
	// +kubebuilder:default="8Gi"
	// +optional
	Memory resource.Quantity `json:"memory,omitempty"`
	// +kubebuilder:default=20
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=500
	// +optional
	Pods int32 `json:"pods,omitempty"`
}

// ContainerDefaults becomes the namespace's LimitRange.
// +kubebuilder:validation:XValidation:rule="!has(self.cpuRequest) || !has(self.cpuLimit) || quantity(string(self.cpuRequest)).compareTo(quantity(string(self.cpuLimit))) <= 0",message="cpuRequest must not exceed cpuLimit"
// +kubebuilder:validation:XValidation:rule="!has(self.memoryRequest) || !has(self.memoryLimit) || quantity(string(self.memoryRequest)).compareTo(quantity(string(self.memoryLimit))) <= 0",message="memoryRequest must not exceed memoryLimit"
type ContainerDefaults struct {
	// +kubebuilder:default="100m"
	// +optional
	CPURequest resource.Quantity `json:"cpuRequest,omitempty"`
	// +kubebuilder:default="500m"
	// +optional
	CPULimit resource.Quantity `json:"cpuLimit,omitempty"`
	// +kubebuilder:default="128Mi"
	// +optional
	MemoryRequest resource.Quantity `json:"memoryRequest,omitempty"`
	// +kubebuilder:default="512Mi"
	// +optional
	MemoryLimit resource.Quantity `json:"memoryLimit,omitempty"`
}

// TeamStatus reports the namespace the team got and whether it is ready.
type TeamStatus struct {
	// Namespace is the name of the team's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// ObservedGeneration is the spec generation the conditions describe.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types and reasons.
const (
	ConditionReady = "Ready"

	ReasonProvisioned       = "Provisioned"
	ReasonNamespaceConflict = "NamespaceConflict"
	ReasonNamespaceDeleting = "NamespaceDeleting"
	ReasonApplyFailed       = "ApplyFailed"
)

// Team is a self-service request for a guarded namespace.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 58 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="name must be a DNS label of at most 58 characters, so team-<name> is a valid namespace"
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Team struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TeamSpec   `json:"spec"`
	Status TeamStatus `json:"status,omitempty"`
}

// TeamList is a list of Teams.
// +kubebuilder:object:root=true
type TeamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Team `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Team{}, &TeamList{})
}
