package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tenancyv1alpha1 "github.com/kmvarma12133/team-operator/api/v1alpha1"
)

func newTeam(name string, members ...rbacv1.Subject) *tenancyv1alpha1.Team {
	if len(members) == 0 {
		members = []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: name + "-devs"}}
	}
	return &tenancyv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       tenancyv1alpha1.TeamSpec{Members: members},
	}
}

func create(t *testing.T, obj client.Object) {
	t.Helper()
	if err := k8sClient.Create(context.Background(), obj); err != nil {
		t.Fatalf("create %s: %v", obj.GetName(), err)
	}
}

func get(name, namespace string, obj client.Object) error {
	return k8sClient.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, obj)
}

// waitReady waits for the Team's Ready condition to have the given status and reason.
func waitReady(t *testing.T, name string, status metav1.ConditionStatus, reason string) *tenancyv1alpha1.Team {
	t.Helper()
	var team tenancyv1alpha1.Team
	eventually(t, fmt.Sprintf("Team %s Ready=%s/%s", name, status, reason), func() error {
		if err := get(name, "", &team); err != nil {
			return err
		}
		c := meta.FindStatusCondition(team.Status.Conditions, tenancyv1alpha1.ConditionReady)
		if c == nil || c.Status != status || c.Reason != reason || c.ObservedGeneration != team.Generation {
			return fmt.Errorf("condition is %+v", c)
		}
		return nil
	})
	return &team
}

func TestProvisionsGuardedNamespace(t *testing.T) {
	team := newTeam("payments",
		rbacv1.Subject{Kind: rbacv1.GroupKind, Name: "payments-devs"},
		rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "deployer"},
	)
	team.Spec.CostCenter = "cc-1042"
	create(t, team)

	got := waitReady(t, "payments", metav1.ConditionTrue, tenancyv1alpha1.ReasonProvisioned)
	ns := NamespaceFor("payments")
	if got.Status.Namespace != ns {
		t.Errorf("status.namespace = %q, want %q", got.Status.Namespace, ns)
	}

	// The API server filled in the defaults from the CRD schema.
	if got.Spec.DeletionPolicy != tenancyv1alpha1.DeletionPolicyRetain {
		t.Errorf("deletionPolicy defaulted to %q, want Retain", got.Spec.DeletionPolicy)
	}

	var namespace corev1.Namespace
	if err := get(ns, "", &namespace); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		LabelTeam:                            "payments",
		LabelOwner:                           "payments",
		LabelCostCtr:                         "cc-1042",
		LabelManagedBy:                       FieldOwner,
		"pod-security.kubernetes.io/enforce": "restricted",
	} {
		if v := namespace.Labels[k]; v != want {
			t.Errorf("namespace label %s = %q, want %q", k, v, want)
		}
	}
	if len(namespace.OwnerReferences) != 0 {
		t.Errorf("Retain namespace has owner references %v", namespace.OwnerReferences)
	}

	var quota corev1.ResourceQuota
	if err := get(QuotaName, ns, &quota); err != nil {
		t.Fatal(err)
	}
	for res, want := range map[corev1.ResourceName]string{
		corev1.ResourceRequestsCPU:    "4",
		corev1.ResourceRequestsMemory: "8Gi",
		corev1.ResourcePods:           "20",
	} {
		if got := quota.Spec.Hard[res]; got.Cmp(resource.MustParse(want)) != 0 {
			t.Errorf("quota %s = %s, want %s", res, got.String(), want)
		}
	}
	if quota.Labels[LabelOwner] != "payments" {
		t.Errorf("quota is missing the owner label: %v", quota.Labels)
	}

	var lr corev1.LimitRange
	if err := get(LimitRangeName, ns, &lr); err != nil {
		t.Fatal(err)
	}
	if len(lr.Spec.Limits) != 1 || lr.Spec.Limits[0].Type != corev1.LimitTypeContainer {
		t.Fatalf("limit range = %+v", lr.Spec.Limits)
	}
	if got := lr.Spec.Limits[0].Default[corev1.ResourceMemory]; got.Cmp(resource.MustParse("512Mi")) != 0 {
		t.Errorf("default memory limit = %s, want 512Mi", got.String())
	}

	var deny networkingv1.NetworkPolicy
	if err := get(DenyAllPolicyName, ns, &deny); err != nil {
		t.Fatal(err)
	}
	if len(deny.Spec.PodSelector.MatchLabels) != 0 || len(deny.Spec.Ingress) != 0 || len(deny.Spec.Egress) != 0 ||
		len(deny.Spec.PolicyTypes) != 2 {
		t.Errorf("default-deny policy is not deny-all: %+v", deny.Spec)
	}
	var allow networkingv1.NetworkPolicy
	if err := get(AllowLocalPolicy, ns, &allow); err != nil {
		t.Fatal(err)
	}
	if len(allow.Spec.Egress) != 2 || len(allow.Spec.Egress[1].Ports) != 2 {
		t.Errorf("allow policy should permit same-namespace and DNS egress: %+v", allow.Spec.Egress)
	}

	var rb rbacv1.RoleBinding
	if err := get(MembersBindingName, ns, &rb); err != nil {
		t.Fatal(err)
	}
	if rb.RoleRef.Kind != "ClusterRole" || rb.RoleRef.Name != MembersClusterRole {
		t.Errorf("roleRef = %+v", rb.RoleRef)
	}
	want := []rbacv1.Subject{
		{Kind: rbacv1.GroupKind, APIGroup: rbacv1.GroupName, Name: "payments-devs"},
		// A service account with no namespace means one in the team's namespace.
		{Kind: rbacv1.ServiceAccountKind, Name: "deployer", Namespace: ns},
	}
	if fmt.Sprint(rb.Subjects) != fmt.Sprint(want) {
		t.Errorf("subjects = %+v, want %+v", rb.Subjects, want)
	}
}

func TestRevertsManualChanges(t *testing.T) {
	create(t, newTeam("search"))
	waitReady(t, "search", metav1.ConditionTrue, tenancyv1alpha1.ReasonProvisioned)
	ns := NamespaceFor("search")

	// Someone raises the quota by hand.
	var quota corev1.ResourceQuota
	if err := get(QuotaName, ns, &quota); err != nil {
		t.Fatal(err)
	}
	quota.Spec.Hard[corev1.ResourceRequestsCPU] = resource.MustParse("100")
	if err := k8sClient.Update(context.Background(), &quota); err != nil {
		t.Fatal(err)
	}

	eventually(t, "quota reverted to 4 CPUs", func() error {
		if err := get(QuotaName, ns, &quota); err != nil {
			return err
		}
		if got := quota.Spec.Hard[corev1.ResourceRequestsCPU]; got.Cmp(resource.MustParse("4")) != 0 {
			return fmt.Errorf("requests.cpu is %s", got.String())
		}
		return nil
	})

	// Someone deletes the default-deny policy.
	if err := k8sClient.Delete(context.Background(), &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: DenyAllPolicyName, Namespace: ns},
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "default-deny policy recreated", func() error {
		return get(DenyAllPolicyName, ns, &networkingv1.NetworkPolicy{})
	})
}

func TestSpecChangesAreApplied(t *testing.T) {
	create(t, newTeam("ledger",
		rbacv1.Subject{Kind: rbacv1.UserKind, Name: "alice@example.com"},
		rbacv1.Subject{Kind: rbacv1.UserKind, Name: "bob@example.com"},
	))
	waitReady(t, "ledger", metav1.ConditionTrue, tenancyv1alpha1.ReasonProvisioned)
	ns := NamespaceFor("ledger")

	var team tenancyv1alpha1.Team
	if err := get("ledger", "", &team); err != nil {
		t.Fatal(err)
	}
	team.Spec.Members = team.Spec.Members[:1] // bob leaves
	team.Spec.Quota.Pods = 50
	if err := k8sClient.Update(context.Background(), &team); err != nil {
		t.Fatal(err)
	}

	eventually(t, "bob removed from the binding", func() error {
		var rb rbacv1.RoleBinding
		if err := get(MembersBindingName, ns, &rb); err != nil {
			return err
		}
		if len(rb.Subjects) != 1 || rb.Subjects[0].Name != "alice@example.com" {
			return fmt.Errorf("subjects are %+v", rb.Subjects)
		}
		return nil
	})
	eventually(t, "pod quota raised to 50", func() error {
		var quota corev1.ResourceQuota
		if err := get(QuotaName, ns, &quota); err != nil {
			return err
		}
		if got := quota.Spec.Hard[corev1.ResourcePods]; got.Value() != 50 {
			return fmt.Errorf("pods is %s", got.String())
		}
		return nil
	})
	waitReady(t, "ledger", metav1.ConditionTrue, tenancyv1alpha1.ReasonProvisioned)
}

func TestDoesNotAdoptForeignNamespace(t *testing.T) {
	ns := NamespaceFor("legacy")
	create(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	create(t, newTeam("legacy"))

	got := waitReady(t, "legacy", metav1.ConditionFalse, tenancyv1alpha1.ReasonNamespaceConflict)
	if got.Status.Namespace != "" {
		t.Errorf("status.namespace = %q for a namespace the Team does not own", got.Status.Namespace)
	}
	if err := get(QuotaName, ns, &corev1.ResourceQuota{}); !apierrors.IsNotFound(err) {
		t.Errorf("quota in a foreign namespace: err = %v, want NotFound", err)
	}
	var namespace corev1.Namespace
	if err := get(ns, "", &namespace); err != nil {
		t.Fatal(err)
	}
	if _, ok := namespace.Labels[LabelTeam]; ok {
		t.Errorf("foreign namespace was labelled: %v", namespace.Labels)
	}
}

func TestDeletionPolicyControlsOwnerReference(t *testing.T) {
	team := newTeam("sandbox")
	team.Spec.DeletionPolicy = tenancyv1alpha1.DeletionPolicyDelete
	create(t, team)
	waitReady(t, "sandbox", metav1.ConditionTrue, tenancyv1alpha1.ReasonProvisioned)
	ns := NamespaceFor("sandbox")

	ownedBy := func(want bool) func() error {
		return func() error {
			var namespace corev1.Namespace
			if err := get(ns, "", &namespace); err != nil {
				return err
			}
			owned := len(namespace.OwnerReferences) == 1 && namespace.OwnerReferences[0].Name == "sandbox" &&
				namespace.OwnerReferences[0].Controller != nil && *namespace.OwnerReferences[0].Controller
			if owned != want {
				return fmt.Errorf("owner references are %+v", namespace.OwnerReferences)
			}
			return nil
		}
	}
	eventually(t, "namespace owned by the Team", ownedBy(true))

	// Switching to Retain must drop the reference, or deleting the Team would still delete the namespace.
	if err := get("sandbox", "", team); err != nil {
		t.Fatal(err)
	}
	team.Spec.DeletionPolicy = tenancyv1alpha1.DeletionPolicyRetain
	if err := k8sClient.Update(context.Background(), team); err != nil {
		t.Fatal(err)
	}
	eventually(t, "owner reference removed", ownedBy(false))
}

func TestReadoptsRetainedNamespace(t *testing.T) {
	create(t, newTeam("analytics"))
	waitReady(t, "analytics", metav1.ConditionTrue, tenancyv1alpha1.ReasonProvisioned)

	// Deleting a Retain team keeps its namespace; recreating the team takes it back.
	if err := k8sClient.Delete(context.Background(), newTeam("analytics")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Team deleted", func() error {
		if err := get("analytics", "", &tenancyv1alpha1.Team{}); !apierrors.IsNotFound(err) {
			return fmt.Errorf("still present: %v", err)
		}
		return nil
	})
	if err := get(NamespaceFor("analytics"), "", &corev1.Namespace{}); err != nil {
		t.Fatalf("retained namespace: %v", err)
	}

	create(t, newTeam("analytics"))
	waitReady(t, "analytics", metav1.ConditionTrue, tenancyv1alpha1.ReasonProvisioned)
}

func TestValidation(t *testing.T) {
	tooLong := strings.Repeat("a", 59)
	badDefaults := newTeam("bad-defaults")
	badDefaults.Spec.ContainerDefaults = tenancyv1alpha1.ContainerDefaults{
		CPURequest: resource.MustParse("2"),
		CPULimit:   resource.MustParse("1"),
	}
	badPolicy := newTeam("bad-policy")
	badPolicy.Spec.DeletionPolicy = "Orphan"

	cases := []struct {
		name    string
		team    *tenancyv1alpha1.Team
		message string
	}{
		{"name too long for a namespace", newTeam(tooLong), "DNS label of at most 58"},
		{"name with a dot", newTeam("a.b"), "DNS label of at most 58"},
		{"no members", &tenancyv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: "empty"}}, "spec.members"},
		{"request above limit", badDefaults, "cpuRequest must not exceed cpuLimit"},
		{"unknown deletion policy", badPolicy, "deletionPolicy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := k8sClient.Create(context.Background(), tc.team)
			if !apierrors.IsInvalid(err) {
				t.Fatalf("err = %v, want Invalid", err)
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error %q does not mention %q", err, tc.message)
			}
		})
	}
}
