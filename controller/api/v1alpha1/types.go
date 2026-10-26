// Package v1alpha1 defines the declarative Moco management API.
// +kubebuilder:object:generate=true
// +groupName=moco.araihu.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "moco.araihu.io", Version: "v1alpha1"}
var SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &MocoTenant{}, &MocoTenantList{}, &MocoVault{}, &MocoVaultList{}, &MocoAuthorization{}, &MocoAuthorizationList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
})
var AddToScheme = SchemeBuilder.AddToScheme

// ResourceSpec describes mutable metadata and an explicit, stable adoption key.
type ResourceSpec struct {
	// ExternalID is immutable. An existing Moco resource with this key is adopted.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="externalId is immutable"
	ExternalID string `json:"externalId"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=1024
	Description string `json:"description,omitempty"`
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, size(self[k]) <= 256)",message="label values must be at most 256 characters"
	Labels map[string]string `json:"labels,omitempty"`
}

// ResourceStatus contains metadata only, never credentials or secret contents.
type ResourceStatus struct {
	ID                 string `json:"id,omitempty"`
	TenantID           string `json:"tenantId,omitempty"`
	Revision           int64  `json:"revision,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type MocoTenant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ResourceSpec   `json:"spec"`
	Status            ResourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MocoTenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MocoTenant `json:"items"`
}

type VaultSpec struct {
	ResourceSpec `json:",inline"`
	// TenantRef names a MocoTenant in this namespace. Moving a vault is forbidden.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tenantRef is immutable"
	TenantRef string `json:"tenantRef"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type MocoVault struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VaultSpec      `json:"spec"`
	Status            ResourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MocoVaultList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MocoVault `json:"items"`
}

// Domain selects a literal domain or resolves a same-namespace tenant reference.
// +kubebuilder:validation:XValidation:rule="has(self.domain) != has(self.tenantRef)",message="set exactly one of domain or tenantRef"
type Domain struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Domain string `json:"domain,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	TenantRef string `json:"tenantRef,omitempty"`
}

type Policy struct {
	Domain `json:",inline"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Subject string `json:"subject"`
	// +kubebuilder:validation:MinLength=7
	// +kubebuilder:validation:MaxLength=512
	Path string `json:"path"`
	// +kubebuilder:validation:Enum=GET;POST;PUT;DELETE;HEAD;OPTIONS;PATCH;*
	Method string `json:"method"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	SecretPathPrefix string `json:"secretPathPrefix,omitempty"`
}

type RoleBinding struct {
	Domain `json:",inline"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Principal string `json:"principal"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Role string `json:"role"`
}

type AuthorizationSpec struct {
	// AdoptRevision explicitly approves replacing the initial full snapshot at this revision.
	// After status loss, a changed snapshot requires renewed approval at its current revision.
	// +kubebuilder:validation:Minimum=0
	AdoptRevision int64 `json:"adoptRevision"`
	// Policies is the complete desired policy set, plus the controller's reserved management permissions.
	// +kubebuilder:validation:MaxItems=9900
	Policies []Policy `json:"policies"`
	// +kubebuilder:validation:MaxItems=10000
	RoleBindings []RoleBinding `json:"roleBindings"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'moco'",message="the authoritative snapshot must be named moco"
// +kubebuilder:printcolumn:name="Revision",type=integer,JSONPath=`.status.revision`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type MocoAuthorization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AuthorizationSpec `json:"spec"`
	Status            ResourceStatus    `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MocoAuthorizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MocoAuthorization `json:"items"`
}
