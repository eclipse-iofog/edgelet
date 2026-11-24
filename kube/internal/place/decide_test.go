package place

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestDecide(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pod     *corev1.Pod
		node    *corev1.Node
		others  []*corev1.Pod
		assign  bool
		reason  string
		message string
	}{
		{
			name:   "no constraints",
			pod:    plainPod(),
			node:   readyNode(),
			assign: true,
		},
		{
			name:   "empty scheduler name",
			pod:    plainPod(),
			node:   readyNode(),
			assign: true,
		},
		{
			name: "default scheduler",
			pod: func() *corev1.Pod {
				pod := plainPod()
				pod.Spec.SchedulerName = corev1.DefaultSchedulerName
				return pod
			}(),
			node:   readyNode(),
			assign: true,
		},
		{
			name: "other scheduler",
			pod: func() *corev1.Pod {
				pod := plainPod()
				pod.Spec.SchedulerName = "custom-scheduler"
				pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "other"}
				return pod
			}(),
			node: readyNode(),
		},
		{
			name: "scheduling gate",
			pod: func() *corev1.Pod {
				pod := plainPod()
				pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "example.com/hold"}}
				pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "other"}
				return pod
			}(),
			node:    readyNode(),
			reason:  reasonSchedulingGates,
			message: "example.com/hold",
		},
		{
			name:    "node missing",
			pod:     plainPod(),
			reason:  reasonNodeMissing,
			message: "node-a",
		},
		{
			name: "cordoned",
			pod:  plainPod(),
			node: func() *corev1.Node {
				node := readyNode()
				node.Spec.Unschedulable = true
				return node
			}(),
			reason:  reasonCordoned,
			message: "node-a",
		},
		{
			name: "no schedule taint",
			pod:  plainPod(),
			node: func() *corev1.Node {
				node := readyNode()
				node.Spec.Taints = []corev1.Taint{{
					Key: "dedicated", Value: "db", Effect: corev1.TaintEffectNoSchedule,
				}}
				return node
			}(),
			reason:  reasonTaint,
			message: "dedicated=db",
		},
		{
			name: "tolerated taint",
			pod: func() *corev1.Pod {
				pod := plainPod()
				pod.Spec.Tolerations = []corev1.Toleration{{
					Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "db", Effect: corev1.TaintEffectNoSchedule,
				}}
				return pod
			}(),
			node: func() *corev1.Node {
				node := readyNode()
				node.Spec.Taints = []corev1.Taint{{
					Key: "dedicated", Value: "db", Effect: corev1.TaintEffectNoSchedule,
				}}
				return node
			}(),
			assign: true,
		},
		{
			name: "prefer no schedule",
			pod:  plainPod(),
			node: func() *corev1.Node {
				node := readyNode()
				node.Spec.Taints = []corev1.Taint{{
					Key: "slow", Effect: corev1.TaintEffectPreferNoSchedule,
				}}
				return node
			}(),
			assign: true,
		},
		{
			name:    "memory pressure",
			pod:     plainPod(),
			node:    pressured(corev1.NodeMemoryPressure),
			reason:  reasonPressure,
			message: "MemoryPressure",
		},
		{
			name:    "disk pressure",
			pod:     plainPod(),
			node:    pressured(corev1.NodeDiskPressure),
			reason:  reasonPressure,
			message: "DiskPressure",
		},
		{
			name:    "pid pressure",
			pod:     plainPod(),
			node:    pressured(corev1.NodePIDPressure),
			reason:  reasonPressure,
			message: "PIDPressure",
		},
		{
			name: "other hostname",
			pod: func() *corev1.Pod {
				pod := plainPod()
				pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "other"}
				return pod
			}(),
			node:    readyNode(),
			reason:  reasonNodeSelector,
			message: "kubernetes.io/hostname=other",
		},
		{
			name: "matching selector",
			pod: func() *corev1.Pod {
				pod := plainPod()
				pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "node-a"}
				return pod
			}(),
			node:   readyNode(),
			assign: true,
		},
		{
			name:    "required node affinity",
			pod:     affinityPod("other"),
			node:    readyNode(),
			reason:  reasonNodeAffinity,
			message: "kubernetes.io/hostname",
		},
		{
			name:   "matching node affinity",
			pod:    affinityPod("node-a"),
			node:   readyNode(),
			assign: true,
		},
		{
			name:   "preferred affinity is ignored",
			pod:    preferredAffinityPod("other"),
			node:   readyNode(),
			assign: true,
		},
		{
			name:   "pod affinity is ignored",
			pod:    podAffinityPod(),
			node:   readyNode(),
			assign: true,
		},
		{
			name: "host port taken",
			pod:  hostPortPod("app", 80),
			node: readyNode(),
			others: []*corev1.Pod{
				placedPod("taken", "", 80),
			},
			reason:  reasonHostPort,
			message: "host port 80/TCP",
		},
		{
			name: "host port different protocol",
			pod: func() *corev1.Pod {
				pod := hostPortPod("app", 80)
				pod.Spec.Containers[0].Ports[0].Protocol = corev1.ProtocolUDP
				return pod
			}(),
			node: readyNode(),
			others: []*corev1.Pod{
				placedPod("taken", "", 80),
			},
			assign: true,
		},
		{
			name:    "cpu does not fit",
			pod:     requestedPod("2", ""),
			node:    readyNode(),
			reason:  reasonInsufficientResources,
			message: "insufficient cpu",
		},
		{
			name:    "memory does not fit",
			pod:     requestedPod("", "2Gi"),
			node:    readyNode(),
			reason:  reasonInsufficientResources,
			message: "insufficient memory",
		},
		{
			name: "cpu fits beside another pod",
			pod:  requestedPod("400m", ""),
			node: readyNode(),
			others: []*corev1.Pod{
				placedPod("neighbor", "500m", 0),
			},
			assign: true,
		},
		{
			name: "cpu does not fit beside another pod",
			pod:  requestedPod("600m", ""),
			node: readyNode(),
			others: []*corev1.Pod{
				placedPod("neighbor", "500m", 0),
			},
			reason:  reasonInsufficientResources,
			message: "insufficient cpu",
		},
		{
			name: "finished pod does not consume cpu",
			pod:  requestedPod("1", ""),
			node: readyNode(),
			others: []*corev1.Pod{
				func() *corev1.Pod {
					pod := placedPod("done", "1", 0)
					pod.Status.Phase = corev1.PodSucceeded
					return pod
				}(),
			},
			assign: true,
		},
		{
			name: "pod is not counted twice",
			pod:  requestedPod("600m", ""),
			node: readyNode(),
			others: []*corev1.Pod{
				requestedPod("600m", ""),
			},
			assign: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decide("node-a", tc.pod, tc.node, tc.others)
			if got.assign != tc.assign || got.reason != tc.reason {
				t.Fatalf("assign=%v reason=%s message=%s", got.assign, got.reason, got.message)
			}
			if tc.message != "" && !strings.Contains(got.message, tc.message) {
				t.Fatalf("message %q", got.message)
			}
		})
	}
}

func readyNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-a",
			Labels: map[string]string{
				"kubernetes.io/hostname": "node-a",
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
	}
}

func pressured(cond corev1.NodeConditionType) *corev1.Node {
	node := readyNode()
	node.Status.Conditions = []corev1.NodeCondition{{
		Type:   cond,
		Status: corev1.ConditionTrue,
	}}
	return node
}

func plainPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default", UID: "app"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "pause"}},
		},
	}
}

func requestedPod(cpu, memory string) *corev1.Pod {
	pod := plainPod()
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{}
	if cpu != "" {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if memory != "" {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse(memory)
	}
	return pod
}

func placedPod(name, cpu string, port int32) *corev1.Pod {
	pod := requestedPod(cpu, "")
	pod.Name = name
	pod.UID = types.UID(name)
	pod.Spec.NodeName = "node-a"
	if port > 0 {
		pod.Spec.Containers[0].Ports = []corev1.ContainerPort{{
			ContainerPort: port,
			HostPort:      port,
			Protocol:      corev1.ProtocolTCP,
		}}
	}
	return pod
}

func hostPortPod(name string, port int32) *corev1.Pod {
	pod := plainPod()
	pod.Name = name
	pod.UID = types.UID(name)
	pod.Spec.Containers[0].Ports = []corev1.ContainerPort{{
		ContainerPort: port,
		HostPort:      port,
		Protocol:      corev1.ProtocolTCP,
	}}
	return pod
}

func affinityPod(hostname string) *corev1.Pod {
	pod := plainPod()
	pod.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      "kubernetes.io/hostname",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{hostname},
					}},
				}},
			},
		},
	}
	return pod
}

func preferredAffinityPod(hostname string) *corev1.Pod {
	pod := plainPod()
	pod.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{
				Weight: 100,
				Preference: corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      "kubernetes.io/hostname",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{hostname},
					}},
				},
			}},
		},
	}
	return pod
}

func podAffinityPod() *corev1.Pod {
	pod := plainPod()
	pod.Spec.Affinity = &corev1.Affinity{
		PodAffinity: &corev1.PodAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				TopologyKey: "kubernetes.io/hostname",
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "missing"},
				},
			}},
		},
	}
	return pod
}
