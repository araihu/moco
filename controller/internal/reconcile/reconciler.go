// Package reconcile manages Moco metadata and its authoritative authorization snapshot.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	v1 "github.com/araihu/moco/controller/api/v1alpha1"
	"github.com/araihu/moco/controller/internal/moco"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Config struct {
	Namespace, Secret, PublicURL, AdminURL, Subject string
	Interval                                        time.Duration
}

type Reconciler struct {
	client.Client
	// Reader bypasses the informer cache: only the configured credential Secret is fetched.
	Reader client.Reader
	Config Config
	Kind   string
}

func (r *Reconciler) Setup(mgr ctrl.Manager) error {
	var obj client.Object
	switch r.Kind {
	case "tenant":
		obj = &v1.MocoTenant{}
	case "vault":
		obj = &v1.MocoVault{}
	case "authorization":
		obj = &v1.MocoAuthorization{}
	default:
		return errors.New("unknown Moco resource kind")
	}
	return ctrl.NewControllerManagedBy(mgr).For(obj).Complete(r)
}

func (r *Reconciler) connection(ctx context.Context) (*moco.Client, error) {
	var secret corev1.Secret
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: r.Config.Namespace, Name: r.Config.Secret}, &secret); err != nil {
		return nil, errors.New("management credential Secret unavailable")
	}
	return moco.New(r.Config.PublicURL, r.Config.AdminURL, string(secret.Data["token"]), secret.Data["ca.pem"])
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Namespace != r.Config.Namespace {
		return ctrl.Result{}, nil
	}
	var obj client.Object
	var status *v1.ResourceStatus
	switch r.Kind {
	case "tenant":
		v := &v1.MocoTenant{}
		obj = v
		status = &v.Status
	case "vault":
		v := &v1.MocoVault{}
		obj = v
		status = &v.Status
	case "authorization":
		v := &v1.MocoAuthorization{}
		obj = v
		status = &v.Status
	default:
		return ctrl.Result{}, errors.New("unknown Moco resource kind")
	}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}
	before := obj.DeepCopyObject().(client.Object)
	remote, err := r.connection(ctx)
	if err == nil {
		defer remote.Close()
		switch v := obj.(type) {
		case *v1.MocoTenant:
			err = r.tenant(ctx, remote, v)
		case *v1.MocoVault:
			err = r.vault(ctx, remote, v)
		case *v1.MocoAuthorization:
			err = r.authorization(ctx, remote, v)
		}
	}
	ready, reason, message := metav1.ConditionTrue, "Reconciled", "Desired state is applied; deletion retains remote resources"
	if err != nil {
		ready, reason, message = metav1.ConditionFalse, "ReconcileFailed", err.Error()
	}
	status.ObservedGeneration = obj.GetGeneration()
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: "Ready", Status: ready, Reason: reason, Message: message, ObservedGeneration: obj.GetGeneration()})
	// A no-op must not create a status event/reconcile hot loop.
	if !reflect.DeepEqual(before, obj) {
		if updateErr := r.Status().Patch(ctx, obj, client.MergeFrom(before)); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
	}
	// Expected dependency/API failures are exposed without logging potentially sensitive bodies.
	return ctrl.Result{RequeueAfter: r.Config.Interval}, nil
}

func (r *Reconciler) tenant(ctx context.Context, remote *moco.Client, obj *v1.MocoTenant) error {
	var all v1.MocoTenantList
	if err := r.List(ctx, &all, client.InNamespace(obj.Namespace)); err != nil {
		return err
	}
	for _, other := range all.Items {
		if other.Name != obj.Name && other.Spec.ExternalID == obj.Spec.ExternalID {
			return errors.New("duplicate tenant externalId in this namespace")
		}
	}
	current, err := remote.Ensure(ctx, "/api/v1/tenants", obj.Spec.ExternalID, metadata(obj.Spec))
	if err == nil {
		obj.Status.ID = current.ID
		obj.Status.Revision = current.Revision
	}
	return err
}

func metadata(spec v1.ResourceSpec) moco.Metadata {
	var description *string
	if spec.Description != "" {
		description = &spec.Description
	}
	return moco.Metadata{Name: spec.Name, Description: description, Labels: spec.Labels}
}

func (r *Reconciler) tenantID(ctx context.Context, namespace, name string) (string, error) {
	var tenant v1.MocoTenant
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &tenant); err != nil {
		if apierrors.IsNotFound(err) {
			return "", errors.New("referenced MocoTenant does not exist")
		}
		return "", err
	}
	condition := meta.FindStatusCondition(tenant.Status.Conditions, "Ready")
	if !tenant.DeletionTimestamp.IsZero() || tenant.Status.ID == "" || condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != tenant.Generation {
		return "", errors.New("referenced MocoTenant is not ready at its current generation")
	}
	return tenant.Status.ID, nil
}

func (r *Reconciler) vault(ctx context.Context, remote *moco.Client, obj *v1.MocoVault) error {
	tenant, err := r.tenantID(ctx, obj.Namespace, obj.Spec.TenantRef)
	if err != nil {
		return err
	}
	if obj.Status.TenantID != "" && obj.Status.TenantID != tenant {
		return errors.New("tenant identity changed; refusing to move an existing vault")
	}
	var all v1.MocoVaultList
	if err := r.List(ctx, &all, client.InNamespace(obj.Namespace)); err != nil {
		return err
	}
	for _, other := range all.Items {
		if other.Name != obj.Name && other.Spec.TenantRef == obj.Spec.TenantRef && other.Spec.ExternalID == obj.Spec.ExternalID {
			return errors.New("duplicate vault externalId in this tenant")
		}
	}
	current, err := remote.Ensure(ctx, "/api/v1/tenants/"+tenant+"/vaults", obj.Spec.ExternalID, metadata(obj.Spec.ResourceSpec))
	if err == nil {
		obj.Status.ID = current.ID
		obj.Status.TenantID = tenant
		obj.Status.Revision = current.Revision
	}
	return err
}

func (r *Reconciler) domain(ctx context.Context, namespace string, domain v1.Domain) (string, error) {
	if (domain.Domain == "") == (domain.TenantRef == "") {
		return "", errors.New("set exactly one of domain or tenantRef")
	}
	if domain.TenantRef != "" {
		return r.tenantID(ctx, namespace, domain.TenantRef)
	}
	return domain.Domain, nil
}

func (r *Reconciler) authorization(ctx context.Context, remote *moco.Client, obj *v1.MocoAuthorization) error {
	if obj.Name != "moco" {
		return errors.New("the authoritative snapshot must be named moco")
	}
	desired := moco.Snapshot{Policies: ManagementPolicies(r.Config.Subject), RoleBindings: []moco.RoleBinding{}}
	for _, p := range obj.Spec.Policies {
		domain, err := r.domain(ctx, obj.Namespace, p.Domain)
		if err != nil {
			return err
		}
		desired.Policies = append(desired.Policies, moco.Policy{Subject: p.Subject, Domain: domain, Path: p.Path, Method: p.Method, SecretPathPrefix: p.SecretPathPrefix})
	}
	for _, b := range obj.Spec.RoleBindings {
		domain, err := r.domain(ctx, obj.Namespace, b.Domain)
		if err != nil {
			return err
		}
		desired.RoleBindings = append(desired.RoleBindings, moco.RoleBinding{Principal: b.Principal, Role: b.Role, Domain: domain})
	}
	desired.Policies = unique(desired.Policies)
	desired.RoleBindings = unique(desired.RoleBindings)
	current, etag, err := remote.Authorization(ctx)
	if err != nil {
		return err
	}
	if !EqualSnapshot(current.Snapshot, desired) {
		if obj.Status.ID == "" && current.Revision != obj.Spec.AdoptRevision {
			return fmt.Errorf("initial snapshot adoption requires spec.adoptRevision=%d", current.Revision)
		}
		current, err = remote.ReplaceAuthorization(ctx, etag, desired)
		if err != nil {
			return err
		}
	}
	obj.Status.ID = "moco"
	obj.Status.Revision = current.Revision
	return nil
}

// ManagementPolicies preserve reconciliation access without permitting secret reads/writes.
// Subject must match the configured token principal in Moco's access.json.
func ManagementPolicies(subject string) []moco.Policy {
	var policies []moco.Policy
	for path, methods := range map[string][]string{
		"/api/v1/tenants":                             {"GET", "POST"},
		"/api/v1/tenants/{tenantId}":                  {"GET", "PUT"},
		"/api/v1/tenants/{tenantId}/vaults":           {"GET", "POST"},
		"/api/v1/tenants/{tenantId}/vaults/{vaultId}": {"GET", "PUT"},
		"/internal/v1/authorization":                  {"GET", "PUT"},
	} {
		for _, method := range methods {
			policies = append(policies, moco.Policy{Subject: subject, Domain: "*", Path: path, Method: method})
		}
	}
	return policies
}

// EqualSnapshot compares policy sets independent of ordering and duplicate entries.
func EqualSnapshot(a, b moco.Snapshot) bool {
	return reflect.DeepEqual(canonical(a.Policies), canonical(b.Policies)) && reflect.DeepEqual(canonical(a.RoleBindings), canonical(b.RoleBindings))
}

func canonical[T any](values []T) []string {
	result := make([]string, 0, len(values))
	for _, v := range values {
		data, _ := json.Marshal(v)
		result = append(result, string(data))
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func unique[T any](values []T) []T {
	result := make([]T, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		data, _ := json.Marshal(v)
		key := string(data)
		if !seen[key] {
			seen[key] = true
			result = append(result, v)
		}
	}
	return result
}
