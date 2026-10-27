// Package v1alpha1 contains the Team API.
// +kubebuilder:object:generate=true
// +groupName=tenancy.kmvarma12133.github.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group and version of the Team API.
	GroupVersion = schema.GroupVersion{Group: "tenancy.kmvarma12133.github.io", Version: "v1alpha1"}

	// SchemeBuilder registers the Team types with a scheme. It uses apimachinery
	// only, so importing the API does not pull in controller-runtime.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the Team types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &Team{}, &TeamList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
