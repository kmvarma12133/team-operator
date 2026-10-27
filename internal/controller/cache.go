package controller

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CacheOptions limits the informers for watched built-in kinds to objects the
// operator labelled. Without this the operator would cache every namespace,
// quota and role binding in the cluster. Writes use server-side apply and never
// read from the cache, so an object missing from it is never a problem.
func CacheOptions() cache.Options {
	req, err := labels.NewRequirement(LabelTeam, selection.Exists, nil)
	if err != nil {
		panic(err) // constant input; cannot fail
	}
	managed := cache.ByObject{Label: labels.NewSelector().Add(*req)}

	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Namespace{}:           managed,
			&corev1.ResourceQuota{}:       managed,
			&corev1.LimitRange{}:          managed,
			&networkingv1.NetworkPolicy{}: managed,
			&rbacv1.RoleBinding{}:         managed,
		},
	}
}
