// Package controller reconciles Teams into guarded namespaces.
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	networkingv1ac "k8s.io/client-go/applyconfigurations/networking/v1"
	rbacv1ac "k8s.io/client-go/applyconfigurations/rbac/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tenancyv1alpha1 "github.com/kmvarma12133/team-operator/api/v1alpha1"
)

const (
	// FieldOwner is the server-side apply manager for everything the operator writes.
	FieldOwner = "team-operator"

	// LabelTeam marks every object the operator manages with the Team it belongs to.
	LabelTeam = "tenancy.kmvarma12133.github.io/team"
	// LabelOwner is the ownership label the platform's Gatekeeper policy requires.
	LabelOwner     = "owner"
	LabelCostCtr   = "cost-center"
	LabelManagedBy = "app.kubernetes.io/managed-by"

	// Object names inside a team namespace.
	QuotaName          = "team-quota"
	LimitRangeName     = "team-defaults"
	DenyAllPolicyName  = "default-deny"
	AllowLocalPolicy   = "allow-same-namespace-and-dns"
	MembersBindingName = "team-members"

	// MembersClusterRole is the built-in role team members get in their namespace.
	MembersClusterRole = "edit"

	// conflictRetry is how often a Team blocked by someone else's namespace is rechecked.
	conflictRetry = time.Minute
)

// NamespaceFor returns the namespace a Team owns.
func NamespaceFor(team string) string { return "team-" + team }

// TeamReconciler turns a Team into a namespace with a quota, container defaults,
// a default-deny network policy and a role binding for its members.
type TeamReconciler struct {
	client.Client
	// APIReader reads straight from the API server. The cache only holds objects the
	// operator labelled, so it cannot see a namespace that belongs to someone else.
	APIReader client.Reader
}

// +kubebuilder:rbac:groups=tenancy.kmvarma12133.github.io,resources=teams,verbs=get;list;watch
// +kubebuilder:rbac:groups=tenancy.kmvarma12133.github.io,resources=teams/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=tenancy.kmvarma12133.github.io,resources=teams/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups="",resources=resourcequotas;limitranges,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;patch
// The operator may bind "edit" without holding every permission in it.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind,resourceNames=edit

// Reconcile brings a Team's namespace and its guardrails to the desired state.
func (r *TeamReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var team tenancyv1alpha1.Team
	if err := r.Get(ctx, req.NamespacedName, &team); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Garbage collection or the Retain policy decides what happens to the namespace.
	if !team.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	nsName := NamespaceFor(team.Name)

	var existing corev1.Namespace
	err := r.APIReader.Get(ctx, types.NamespacedName{Name: nsName}, &existing)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return ctrl.Result{}, err
	case existing.Labels[LabelTeam] != team.Name:
		// Never adopt a namespace someone else created: it may hold another team's workloads.
		msg := fmt.Sprintf("namespace %q already exists and is not managed by this Team", nsName)
		logger.Info("namespace conflict", "namespace", nsName)
		return ctrl.Result{RequeueAfter: conflictRetry},
			r.setReady(ctx, &team, "", metav1.ConditionFalse, tenancyv1alpha1.ReasonNamespaceConflict, msg)
	case !existing.DeletionTimestamp.IsZero():
		msg := fmt.Sprintf("namespace %q is still being deleted", nsName)
		return ctrl.Result{RequeueAfter: 5 * time.Second},
			r.setReady(ctx, &team, "", metav1.ConditionFalse, tenancyv1alpha1.ReasonNamespaceDeleting, msg)
	}

	if err := r.apply(ctx, &team, nsName); err != nil {
		// Report the failure on the Team, then return the error so the request is retried with backoff.
		if statusErr := r.setReady(ctx, &team, nsName, metav1.ConditionFalse, tenancyv1alpha1.ReasonApplyFailed, err.Error()); statusErr != nil {
			logger.Error(statusErr, "failed to record apply failure")
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.setReady(ctx, &team, nsName, metav1.ConditionTrue, tenancyv1alpha1.ReasonProvisioned,
		fmt.Sprintf("namespace %q and its guardrails are in place", nsName))
}

// apply server-side applies the namespace and everything in it.
func (r *TeamReconciler) apply(ctx context.Context, team *tenancyv1alpha1.Team, nsName string) error {
	labels := managedLabels(team.Name)

	nsLabels := managedLabels(team.Name)
	// Pods must meet the "restricted" Pod Security Standard.
	nsLabels["pod-security.kubernetes.io/enforce"] = "restricted"
	nsLabels["pod-security.kubernetes.io/audit"] = "restricted"
	nsLabels["pod-security.kubernetes.io/warn"] = "restricted"
	if team.Spec.CostCenter != "" {
		nsLabels[LabelCostCtr] = team.Spec.CostCenter
	}
	ns := corev1ac.Namespace(nsName).WithLabels(nsLabels)
	// Under Retain there is no owner reference, so deleting the Team leaves the namespace.
	// Server-side apply drops a reference this manager set earlier if the policy changes.
	if team.Spec.DeletionPolicy == tenancyv1alpha1.DeletionPolicyDelete {
		ns.WithOwnerReferences(metav1ac.OwnerReference().
			WithAPIVersion(tenancyv1alpha1.GroupVersion.String()).
			WithKind("Team").
			WithName(team.Name).
			WithUID(team.UID).
			WithController(true).
			WithBlockOwnerDeletion(true))
	}

	q, cd := team.Spec.Quota, team.Spec.ContainerDefaults
	objects := []struct {
		kind string
		obj  runtime.ApplyConfiguration
	}{
		{"Namespace", ns},
		{"ResourceQuota", corev1ac.ResourceQuota(QuotaName, nsName).
			WithLabels(labels).
			WithSpec(corev1ac.ResourceQuotaSpec().WithHard(corev1.ResourceList{
				corev1.ResourceRequestsCPU:    q.CPU,
				corev1.ResourceRequestsMemory: q.Memory,
				corev1.ResourcePods:           *resource.NewQuantity(int64(q.Pods), resource.DecimalSI),
			}))},
		{"LimitRange", corev1ac.LimitRange(LimitRangeName, nsName).
			WithLabels(labels).
			WithSpec(corev1ac.LimitRangeSpec().WithLimits(corev1ac.LimitRangeItem().
				WithType(corev1.LimitTypeContainer).
				WithDefaultRequest(corev1.ResourceList{corev1.ResourceCPU: cd.CPURequest, corev1.ResourceMemory: cd.MemoryRequest}).
				WithDefault(corev1.ResourceList{corev1.ResourceCPU: cd.CPULimit, corev1.ResourceMemory: cd.MemoryLimit})))},
		{"NetworkPolicy", networkingv1ac.NetworkPolicy(DenyAllPolicyName, nsName).
			WithLabels(labels).
			WithSpec(networkingv1ac.NetworkPolicySpec().
				WithPodSelector(metav1ac.LabelSelector()).
				WithPolicyTypes(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))},
		{"NetworkPolicy", allowLocalPolicy(nsName, labels)},
		{"RoleBinding", rbacv1ac.RoleBinding(MembersBindingName, nsName).
			WithLabels(labels).
			WithRoleRef(rbacv1ac.RoleRef().
				WithAPIGroup(rbacv1.GroupName).
				WithKind("ClusterRole").
				WithName(MembersClusterRole)).
			WithSubjects(subjects(team.Spec.Members, nsName)...)},
	}

	for _, o := range objects {
		if err := r.Apply(ctx, o.obj, client.FieldOwner(FieldOwner), client.ForceOwnership); err != nil {
			return fmt.Errorf("apply %s: %w", o.kind, err)
		}
	}
	return nil
}

// allowLocalPolicy lets pods talk to each other inside the namespace and resolve DNS.
// Everything else stays denied by the default-deny policy.
func allowLocalPolicy(nsName string, labels map[string]string) *networkingv1ac.NetworkPolicyApplyConfiguration {
	sameNamespace := networkingv1ac.NetworkPolicyPeer().WithPodSelector(metav1ac.LabelSelector())
	dns := networkingv1ac.NetworkPolicyPeer().
		WithNamespaceSelector(metav1ac.LabelSelector().
			WithMatchLabels(map[string]string{"kubernetes.io/metadata.name": "kube-system"})).
		WithPodSelector(metav1ac.LabelSelector().
			WithMatchLabels(map[string]string{"k8s-app": "kube-dns"}))
	port53 := intstr.FromInt32(53)

	return networkingv1ac.NetworkPolicy(AllowLocalPolicy, nsName).
		WithLabels(labels).
		WithSpec(networkingv1ac.NetworkPolicySpec().
			WithPodSelector(metav1ac.LabelSelector()).
			WithPolicyTypes(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress).
			WithIngress(networkingv1ac.NetworkPolicyIngressRule().WithFrom(sameNamespace)).
			WithEgress(
				networkingv1ac.NetworkPolicyEgressRule().WithTo(sameNamespace),
				networkingv1ac.NetworkPolicyEgressRule().WithTo(dns).WithPorts(
					networkingv1ac.NetworkPolicyPort().WithProtocol(corev1.ProtocolUDP).WithPort(port53),
					networkingv1ac.NetworkPolicyPort().WithProtocol(corev1.ProtocolTCP).WithPort(port53),
				),
			))
}

// subjects fills in the defaults RBAC expects: the RBAC API group for users and
// groups, and the team namespace for service accounts that name none.
func subjects(members []rbacv1.Subject, nsName string) []*rbacv1ac.SubjectApplyConfiguration {
	out := make([]*rbacv1ac.SubjectApplyConfiguration, 0, len(members))
	for _, m := range members {
		s := rbacv1ac.Subject().WithKind(m.Kind).WithName(m.Name)
		switch m.Kind {
		case rbacv1.ServiceAccountKind:
			ns := m.Namespace
			if ns == "" {
				ns = nsName
			}
			s.WithNamespace(ns)
		default:
			apiGroup := m.APIGroup
			if apiGroup == "" {
				apiGroup = rbacv1.GroupName
			}
			s.WithAPIGroup(apiGroup)
		}
		out = append(out, s)
	}
	return out
}

func managedLabels(team string) map[string]string {
	return map[string]string{
		LabelTeam:      team,
		LabelOwner:     team,
		LabelManagedBy: FieldOwner,
	}
}

// setReady records the Ready condition, skipping the write when nothing changed.
func (r *TeamReconciler) setReady(ctx context.Context, team *tenancyv1alpha1.Team, nsName string,
	status metav1.ConditionStatus, reason, message string) error {
	base := team.DeepCopy()
	team.Status.Namespace = nsName
	team.Status.ObservedGeneration = team.Generation
	changed := meta.SetStatusCondition(&team.Status.Conditions, metav1.Condition{
		Type:               tenancyv1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: team.Generation,
	})
	if !changed && base.Status.Namespace == nsName && base.Status.ObservedGeneration == team.Generation {
		return nil
	}
	return r.Status().Patch(ctx, team, client.MergeFrom(base))
}

// SetupWithManager reconciles a Team when its spec changes or when anything it
// manages changes, so manual edits to a quota or binding are reverted.
func (r *TeamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toTeam := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		team, ok := obj.GetLabels()[LabelTeam]
		if !ok {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: team}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&tenancyv1alpha1.Team{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Namespace{}, toTeam).
		Watches(&corev1.ResourceQuota{}, toTeam).
		Watches(&corev1.LimitRange{}, toTeam).
		Watches(&networkingv1.NetworkPolicy{}, toTeam).
		Watches(&rbacv1.RoleBinding{}, toTeam).
		Named("team").
		Complete(r)
}
