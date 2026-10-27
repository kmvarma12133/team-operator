// Package v1alpha1 contains the Team API.
// +kubebuilder:object:generate=true
// +groupName=tenancy.kmvarma12133.github.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group and version of the Team API.
	GroupVersion = schema.GroupVersion{Group: "tenancy.kmvarma12133.github.io", Version: "v1alpha1"}

	// SchemeBuilder registers the Team types with a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the Team types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
