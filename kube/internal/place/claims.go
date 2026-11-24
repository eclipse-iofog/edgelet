package place

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/component-helpers/storage/volume"
)

const (
	defaultClassAnnotation     = "storageclass.kubernetes.io/is-default-class"
	betaDefaultClassAnnotation = "storageclass.beta.kubernetes.io/is-default-class"
)

// errClaimNotReady means a mounted claim or its storage class is not in the API yet.
var errClaimNotReady = errors.New("persistentvolumeclaim is not created")

// markClaims writes the selected node onto each WaitForFirstConsumer claim pod mounts.
// Immediate claims are left alone.
func markClaims(ctx context.Context, client kubernetes.Interface, nodeName string, pod *corev1.Pod) error {
	for _, name := range claimNames(pod) {
		claim, err := client.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return errClaimNotReady
		}
		if err != nil {
			return err
		}
		if claim.Spec.VolumeName != "" {
			continue
		}
		wait, err := waitsForConsumer(ctx, client, claim)
		if err != nil {
			return err
		}
		if !wait {
			continue
		}
		if claim.Annotations[volume.AnnSelectedNode] == nodeName {
			continue
		}
		if err := patchSelectedNode(ctx, client, claim, nodeName); err != nil {
			return err
		}
	}
	return nil
}

func claimNames(pod *corev1.Pod) []string {
	seen := map[string]struct{}{}
	var names []string
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim != nil {
			add(vol.PersistentVolumeClaim.ClaimName)
		}
		if vol.Ephemeral != nil {
			add(pod.Name + "-" + vol.Name)
		}
	}
	return names
}

func waitsForConsumer(ctx context.Context, client kubernetes.Interface, claim *corev1.PersistentVolumeClaim) (bool, error) {
	class, err := claimClass(ctx, client, claim.Spec.StorageClassName)
	if err != nil || class == nil {
		return false, err
	}
	mode := storagev1.VolumeBindingImmediate
	if class.VolumeBindingMode != nil {
		mode = *class.VolumeBindingMode
	}
	return mode == storagev1.VolumeBindingWaitForFirstConsumer, nil
}

func claimClass(ctx context.Context, client kubernetes.Interface, name *string) (*storagev1.StorageClass, error) {
	if name == nil {
		return defaultClass(ctx, client)
	}
	if *name == "" {
		return nil, nil
	}
	class, err := client.StorageV1().StorageClasses().Get(ctx, *name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, errClaimNotReady
	}
	if err != nil {
		return nil, err
	}
	return class, nil
}

func defaultClass(ctx context.Context, client kubernetes.Interface) (*storagev1.StorageClass, error) {
	list, err := client.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var found *storagev1.StorageClass
	for i := range list.Items {
		class := &list.Items[i]
		if !isDefaultClass(class) {
			continue
		}
		if found != nil {
			return nil, nil
		}
		found = class
	}
	return found, nil
}

func isDefaultClass(class *storagev1.StorageClass) bool {
	return class.Annotations[defaultClassAnnotation] == "true" || class.Annotations[betaDefaultClassAnnotation] == "true"
}

func patchSelectedNode(ctx context.Context, client kubernetes.Interface, claim *corev1.PersistentVolumeClaim, nodeName string) error {
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				volume.AnnSelectedNode: nodeName,
			},
		},
	})
	if err != nil {
		return err
	}
	_, err = client.CoreV1().PersistentVolumeClaims(claim.Namespace).Patch(ctx, claim.Name, types.MergePatchType, body, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("annotate persistentvolumeclaim %s: %w", claim.Name, err)
	}
	return nil
}
