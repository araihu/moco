package reconcile

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "github.com/araihu/moco/controller/api/v1alpha1"
	"github.com/araihu/moco/controller/internal/moco"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func reconciler(t *testing.T, kind string, handler http.HandlerFunc, objects ...client.Object) *Reconciler {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "test"}, Data: map[string][]byte{
		"token": []byte("token"), "ca.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}),
	}})
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1.MocoTenant{}, &v1.MocoVault{}, &v1.MocoAuthorization{}).WithObjects(objects...).Build()
	return &Reconciler{Client: kube, Reader: kube, Kind: kind, Config: Config{Namespace: "test", Secret: "credentials", PublicURL: server.URL, AdminURL: server.URL, Subject: "controller", Interval: time.Second}}
}

func reconcileOnce(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test", Name: name}})
	if err != nil || result.RequeueAfter != time.Second {
		t.Fatalf("reconcile: %+v %v", result, err)
	}
}

func TestTenantStatusRecoveryNoopAndDuplicateOwnership(t *testing.T) {
	obj := &v1.MocoTenant{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "test", Generation: 1}, Spec: v1.ResourceSpec{Name: "tenant", ExternalID: "stable"}}
	calls := 0
	r := reconciler(t, "tenant", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Error("adoption must not recreate or rewrite")
		}
		if r.URL.Path == "/api/v1/tenants" {
			_, _ = fmt.Fprint(w, `{"items":[{"id":"existing"}]}`)
			return
		}
		w.Header().Set("ETag", `"2"`)
		_, _ = fmt.Fprint(w, `{"id":"existing","externalId":"stable","name":"tenant","labels":{},"revision":2}`)
	}, obj)
	reconcileOnce(t, r, "a")
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
	if obj.Status.ID != "existing" || !meta.IsStatusConditionTrue(obj.Status.Conditions, "Ready") {
		t.Fatalf("adoption: %+v", obj.Status)
	}
	duplicate := obj.DeepCopy()
	duplicate.Name = "b"
	duplicate.ResourceVersion = ""
	duplicate.UID = ""
	duplicate.Status = v1.ResourceStatus{}
	if err := r.Create(t.Context(), duplicate); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, "a")
	if calls != 2 {
		t.Fatal("duplicate ownership reached Moco")
	}
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
	if meta.IsStatusConditionTrue(obj.Status.Conditions, "Ready") {
		t.Fatal("duplicate ownership accepted")
	}
}

func TestVaultWaitsForTenantAndCredentialsRecover(t *testing.T) {
	obj := &v1.MocoVault{ObjectMeta: metav1.ObjectMeta{Name: "vault", Namespace: "test", Generation: 1}, Spec: v1.VaultSpec{ResourceSpec: v1.ResourceSpec{Name: "vault", ExternalID: "vault-stable"}, TenantRef: "missing"}}
	r := reconciler(t, "vault", func(_ http.ResponseWriter, _ *http.Request) { t.Error("unready dependency reached Moco") }, obj)
	reconcileOnce(t, r, "vault")
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
	if meta.IsStatusConditionTrue(obj.Status.Conditions, "Ready") {
		t.Fatal("unready tenant accepted")
	}
	var secret corev1.Secret
	if err := r.Get(t.Context(), types.NamespacedName{Namespace: "test", Name: "credentials"}, &secret); err != nil {
		t.Fatal(err)
	}
	secret.Data["ca.pem"] = []byte("invalid")
	if err := r.Update(t.Context(), &secret); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, "vault")
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(obj.Status.Conditions, "Ready").Message != "invalid Moco CA bundle" {
		t.Fatal("credential rotation was not reread")
	}
}

func TestAuthoritativeSnapshotAdoptionConcurrencyAndRecovery(t *testing.T) {
	obj := &v1.MocoAuthorization{ObjectMeta: metav1.ObjectMeta{Name: "moco", Namespace: "test", Generation: 1}, Spec: v1.AuthorizationSpec{AdoptRevision: 2}}
	current := moco.Authorization{Revision: 3, Snapshot: moco.Snapshot{Policies: []moco.Policy{}, RoleBindings: []moco.RoleBinding{}}}
	puts := 0
	conflict := false
	r := reconciler(t, "authorization", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, current.Revision))
		if r.Method == "PUT" {
			puts++
			if r.Header.Get("If-Match") != `"3"` {
				t.Error("missing snapshot precondition")
			}
			if conflict {
				w.WriteHeader(412)
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&current.Snapshot); err != nil {
				t.Error(err)
			}
			current.Revision++
		}
		_ = json.NewEncoder(w).Encode(current)
	}, obj)
	reconcileOnce(t, r, "moco")
	if puts != 0 {
		t.Fatal("unapproved full snapshot was replaced")
	}
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
	obj.Spec.AdoptRevision = 3
	if err := r.Update(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
	conflict = true
	reconcileOnce(t, r, "moco")
	if current.Revision != 3 || puts != 1 {
		t.Fatal("412 overwrote state or replayed mutation")
	}
	conflict = false
	reconcileOnce(t, r, "moco")
	if current.Revision != 4 || !EqualSnapshot(current.Snapshot, moco.Snapshot{Policies: ManagementPolicies("controller")}) {
		t.Fatal("management policies lost")
	}
	// Losing Kubernetes status is harmless if the desired full snapshot already matches.
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
	obj.Status = v1.ResourceStatus{}
	if err := r.Status().Update(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, "moco")
	if puts != 2 {
		t.Fatal("matching snapshot caused another write")
	}
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
	if obj.Status.Revision != 4 || !meta.IsStatusConditionTrue(obj.Status.Conditions, "Ready") {
		t.Fatal("snapshot status not recovered")
	}
	// CR deletion retains the external snapshot, without any DELETE call.
	if err := r.Delete(t.Context(), obj); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}); err != nil {
		t.Fatal(err)
	}
	if puts != 2 {
		t.Fatal("delete mutated snapshot")
	}
}

func TestManagementPoliciesExcludeSecretData(t *testing.T) {
	for _, p := range ManagementPolicies("controller") {
		if p.Method == "*" || p.Method == "DELETE" || p.Path == "/api/v1/*" || p.Path == "/internal/v1/*" {
			t.Fatalf("excess privilege: %+v", p)
		}
	}
	if !EqualSnapshot(moco.Snapshot{}, moco.Snapshot{Policies: []moco.Policy{}, RoleBindings: []moco.RoleBinding{}}) {
		t.Fatal("nil and empty should be equal")
	}
}
