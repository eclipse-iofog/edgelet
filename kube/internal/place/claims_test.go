package place

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/component-helpers/storage/volume"
)

func TestMarkClaimsAnnotatesWaitForFirstConsumer(t *testing.T) {
	waitMode := storagev1.VolumeBindingWaitForFirstConsumer
	nowMode := storagev1.VolumeBindingImmediate
	waitClass := "wait"
	nowClass := "now"
	client := fake.NewClientset(
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: waitClass}, VolumeBindingMode: &waitMode},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: nowClass}, VolumeBindingMode: &nowMode},
		claim("data", &waitClass),
		claim("fast", &nowClass),
		claim("unused", &waitClass),
	)
	pod := plainPod()
	pod.Spec.Volumes = []corev1.Volume{
		pvcVolume("data", "data"),
		pvcVolume("fast", "fast"),
	}
	if err := markClaims(context.Background(), client, "node-a", pod); err != nil {
		t.Fatal(err)
	}
	if got := annotation(t, client, "data"); got != "node-a" {
		t.Fatalf("data annotation %q", got)
	}
	if got := annotation(t, client, "fast"); got != "" {
		t.Fatalf("fast annotation %q", got)
	}
	if got := annotation(t, client, "unused"); got != "" {
		t.Fatalf("unused annotation %q", got)
	}
}

func TestMarkClaimsUsesDefaultClass(t *testing.T) {
	waitMode := storagev1.VolumeBindingWaitForFirstConsumer
	client := fake.NewClientset(
		&storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "local-path",
				Annotations: map[string]string{defaultClassAnnotation: "true"},
			},
			VolumeBindingMode: &waitMode,
		},
		claim("data", nil),
	)
	pod := plainPod()
	pod.Spec.Volumes = []corev1.Volume{pvcVolume("data", "data")}
	if err := markClaims(context.Background(), client, "node-a", pod); err != nil {
		t.Fatal(err)
	}
	if got := annotation(t, client, "data"); got != "node-a" {
		t.Fatalf("annotation %q", got)
	}
}

func TestMarkClaimsEphemeralVolume(t *testing.T) {
	waitMode := storagev1.VolumeBindingWaitForFirstConsumer
	className := "wait"
	client := fake.NewClientset(
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: className}, VolumeBindingMode: &waitMode},
		claim("app-scratch", &className),
	)
	pod := plainPod()
	pod.Spec.Volumes = []corev1.Volume{{
		Name: "scratch",
		VolumeSource: corev1.VolumeSource{
			Ephemeral: &corev1.EphemeralVolumeSource{
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{},
			},
		},
	}}
	if err := markClaims(context.Background(), client, "node-a", pod); err != nil {
		t.Fatal(err)
	}
	if got := annotation(t, client, "app-scratch"); got != "node-a" {
		t.Fatalf("annotation %q", got)
	}
}

func TestMarkClaimsWaitsForMissingClaim(t *testing.T) {
	pod := plainPod()
	pod.Spec.Volumes = []corev1.Volume{pvcVolume("data", "data")}
	err := markClaims(context.Background(), fake.NewClientset(), "node-a", pod)
	if !errors.Is(err, errClaimNotReady) {
		t.Fatalf("err %v", err)
	}
}

func TestAssignBindsAfterAnnotation(t *testing.T) {
	waitMode := storagev1.VolumeBindingWaitForFirstConsumer
	className := "local-path"
	client := fake.NewClientset(
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: className}, VolumeBindingMode: &waitMode},
		claim("data", &className),
		plainPod(),
	)
	var order []string
	client.PrependReactor("patch", "persistentvolumeclaims", func(action clienttesting.Action) (bool, runtime.Object, error) {
		order = append(order, "patch")
		return false, nil, nil
	})
	client.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "binding" {
			return false, nil, nil
		}
		order = append(order, "bind")
		create, ok := action.(clienttesting.CreateAction)
		if !ok {
			t.Fatalf("action %T", action)
		}
		binding, ok := create.GetObject().(*corev1.Binding)
		if !ok {
			t.Fatalf("object %T", create.GetObject())
		}
		if binding.Target.Name != "node-a" || binding.UID != plainPod().UID {
			t.Fatalf("binding %#v", binding)
		}
		return true, binding, nil
	})
	pod := plainPod()
	pod.Spec.Volumes = []corev1.Volume{pvcVolume("data", "data")}
	if err := assign(context.Background(), client, "node-a", readyNode(), nil, pod); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "patch" || order[1] != "bind" {
		t.Fatalf("order %v", order)
	}
	if got := annotation(t, client, "data"); got != "node-a" {
		t.Fatalf("annotation %q", got)
	}
}

func TestAssignLeavesSelectorUnbound(t *testing.T) {
	className := "local-path"
	waitMode := storagev1.VolumeBindingWaitForFirstConsumer
	client := fake.NewClientset(
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: className}, VolumeBindingMode: &waitMode},
		claim("data", &className),
	)
	client.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "binding" {
			t.Fatal("bound a pod that does not match")
		}
		return false, nil, nil
	})
	pod := plainPod()
	pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "other"}
	pod.Spec.Volumes = []corev1.Volume{pvcVolume("data", "data")}
	if err := assign(context.Background(), client, "node-a", readyNode(), nil, pod); err != nil {
		t.Fatal(err)
	}
	if got := annotation(t, client, "data"); got != "" {
		t.Fatalf("annotation %q", got)
	}
}

func claim(name string, class *string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: class},
	}
}

func pvcVolume(volumeName, claimName string) corev1.Volume {
	return corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName},
		},
	}
}

func annotation(t *testing.T, client kubernetes.Interface, name string) string {
	t.Helper()
	got, err := client.CoreV1().PersistentVolumeClaims("default").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return got.Annotations[volume.AnnSelectedNode]
}
