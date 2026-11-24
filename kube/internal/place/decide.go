package place

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelper "k8s.io/component-helpers/resource"
	schedcorev1 "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
)

const (
	reasonNodeSelector          = "NodeSelector"
	reasonNodeAffinity          = "NodeAffinity"
	reasonCordoned              = "Cordoned"
	reasonTaint                 = "Taint"
	reasonPressure              = "Pressure"
	reasonHostPort              = "HostPort"
	reasonInsufficientResources = "InsufficientResources"
	reasonSchedulingGates       = "SchedulingGates"
	reasonNodeMissing           = "NodeMissing"
)

const bindAllHostIP = "0.0.0.0"

type verdict struct {
	assign  bool
	reason  string
	message string
}

// decide reports whether pod can run on nodeName.
// node is nil when this node has not registered.
// scheduled includes pods already placed on the node; pods elsewhere are ignored.
func decide(nodeName string, pod *corev1.Pod, node *corev1.Node, scheduled []*corev1.Pod) verdict {
	if !schedulesHere(pod) {
		return verdict{}
	}
	if len(pod.Spec.SchedulingGates) > 0 {
		return verdict{reason: reasonSchedulingGates, message: gateMessage(pod)}
	}
	if node == nil {
		return verdict{reason: reasonNodeMissing, message: fmt.Sprintf("node %s is not registered", nodeName)}
	}
	if node.Spec.Unschedulable {
		return verdict{reason: reasonCordoned, message: fmt.Sprintf("node %s is unschedulable", node.Name)}
	}
	if taint, blocked := untoleratedNoSchedule(pod, node); blocked {
		return verdict{reason: reasonTaint, message: "taint " + formatTaint(taint)}
	}
	if cond, blocked := underPressure(node); blocked {
		return verdict{reason: reasonPressure, message: fmt.Sprintf("%s on %s", cond, node.Name)}
	}
	if key, value, blocked := selectorMismatch(pod, node); blocked {
		return verdict{reason: reasonNodeSelector, message: fmt.Sprintf("node selector %s=%s", key, value)}
	}
	if blocked, message := affinityMismatch(pod, node); blocked {
		return verdict{reason: reasonNodeAffinity, message: message}
	}
	if port, proto, blocked := hostPortTaken(pod, node.Name, scheduled); blocked {
		return verdict{reason: reasonHostPort, message: fmt.Sprintf("host port %d/%s", port, proto)}
	}
	if name, blocked := resourcesExceed(pod, node, scheduled); blocked {
		return verdict{reason: reasonInsufficientResources, message: "insufficient " + string(name)}
	}
	return verdict{assign: true}
}

func schedulesHere(pod *corev1.Pod) bool {
	name := pod.Spec.SchedulerName
	return name == "" || name == corev1.DefaultSchedulerName
}

func gateMessage(pod *corev1.Pod) string {
	names := make([]string, len(pod.Spec.SchedulingGates))
	for i, gate := range pod.Spec.SchedulingGates {
		names[i] = gate.Name
	}
	slices.Sort(names)
	return "scheduling gate " + strings.Join(names, ",")
}

func untoleratedNoSchedule(pod *corev1.Pod, node *corev1.Node) (corev1.Taint, bool) {
	return schedcorev1.FindMatchingUntoleratedTaint(
		klog.Background(),
		node.Spec.Taints,
		pod.Spec.Tolerations,
		func(taint *corev1.Taint) bool { return taint.Effect == corev1.TaintEffectNoSchedule },
		false,
	)
}

func formatTaint(taint corev1.Taint) string {
	if taint.Value == "" {
		return fmt.Sprintf("%s:%s", taint.Key, taint.Effect)
	}
	return fmt.Sprintf("%s=%s:%s", taint.Key, taint.Value, taint.Effect)
}

func underPressure(node *corev1.Node) (string, bool) {
	for _, cond := range node.Status.Conditions {
		switch cond.Type {
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			if cond.Status == corev1.ConditionTrue {
				return string(cond.Type), true
			}
		}
	}
	return "", false
}

func selectorMismatch(pod *corev1.Pod, node *corev1.Node) (string, string, bool) {
	keys := make([]string, 0, len(pod.Spec.NodeSelector))
	for key := range pod.Spec.NodeSelector {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		value := pod.Spec.NodeSelector[key]
		if node.Labels[key] != value {
			return key, value, true
		}
	}
	return "", "", false
}

func affinityMismatch(pod *corev1.Pod, node *corev1.Node) (bool, string) {
	affinity := pod.Spec.Affinity
	if affinity == nil || affinity.NodeAffinity == nil || affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return false, ""
	}
	required := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	match, err := nodeaffinity.NewLazyErrorNodeSelector(required).Match(node)
	if err == nil && match {
		return false, ""
	}
	return true, affinityMessage(required)
}

func affinityMessage(required *corev1.NodeSelector) string {
	for _, term := range required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			return requirementMessage(expr.Key, string(expr.Operator), expr.Values)
		}
		for _, expr := range term.MatchFields {
			return requirementMessage(expr.Key, string(expr.Operator), expr.Values)
		}
	}
	return "required node affinity"
}

func requirementMessage(key, operator string, values []string) string {
	if len(values) == 0 {
		return fmt.Sprintf("node affinity %s %s", key, operator)
	}
	return fmt.Sprintf("node affinity %s %s %s", key, operator, strings.Join(values, ","))
}

type portUse struct {
	ip       string
	protocol string
	port     int32
}

func hostPortTaken(pod *corev1.Pod, nodeName string, scheduled []*corev1.Pod) (int32, corev1.Protocol, bool) {
	used := map[portUse]struct{}{}
	for _, other := range scheduled {
		if other.Spec.NodeName != nodeName || !occupies(other) || samePod(other, pod) {
			continue
		}
		for _, port := range hostPorts(other) {
			used[portKey(port)] = struct{}{}
		}
	}
	seen := map[portUse]struct{}{}
	for _, port := range hostPorts(pod) {
		key := portKey(port)
		if _, ok := seen[key]; ok || portConflicts(key, used) {
			return port.HostPort, corev1.Protocol(key.protocol), true
		}
		seen[key] = struct{}{}
	}
	return 0, "", false
}

func portKey(port corev1.ContainerPort) portUse {
	ip := port.HostIP
	if ip == "" {
		ip = bindAllHostIP
	}
	protocol := string(port.Protocol)
	if protocol == "" {
		protocol = string(corev1.ProtocolTCP)
	}
	return portUse{ip: ip, protocol: protocol, port: port.HostPort}
}

func portConflicts(key portUse, used map[portUse]struct{}) bool {
	if key.ip == bindAllHostIP {
		for existing := range used {
			if existing.protocol == key.protocol && existing.port == key.port {
				return true
			}
		}
		return false
	}
	if _, ok := used[key]; ok {
		return true
	}
	_, ok := used[portUse{ip: bindAllHostIP, protocol: key.protocol, port: key.port}]
	return ok
}

func hostPorts(pod *corev1.Pod) []corev1.ContainerPort {
	var ports []corev1.ContainerPort
	collect := func(containers []corev1.Container) {
		for i := range containers {
			for _, port := range containers[i].Ports {
				if port.HostPort > 0 {
					ports = append(ports, port)
				}
			}
		}
	}
	collect(pod.Spec.Containers)
	collect(pod.Spec.InitContainers)
	for i := range pod.Spec.EphemeralContainers {
		collect([]corev1.Container{corev1.Container(pod.Spec.EphemeralContainers[i].EphemeralContainerCommon)})
	}
	return ports
}

func resourcesExceed(pod *corev1.Pod, node *corev1.Node, scheduled []*corev1.Pod) (corev1.ResourceName, bool) {
	used := map[corev1.ResourceName]resource.Quantity{}
	for _, other := range scheduled {
		if other.Spec.NodeName != node.Name || !occupies(other) || samePod(other, pod) {
			continue
		}
		addRequests(used, resourcehelper.PodRequests(other, resourcehelper.PodResourcesOptions{}))
	}
	need := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		want := need[name].DeepCopy()
		if extra, ok := used[name]; ok {
			added := extra.DeepCopy()
			want.Add(added)
		}
		if want.Cmp(node.Status.Allocatable[name]) > 0 {
			return name, true
		}
	}
	return "", false
}

func addRequests(dst map[corev1.ResourceName]resource.Quantity, src corev1.ResourceList) {
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		qty, ok := src[name]
		if !ok {
			continue
		}
		total := dst[name].DeepCopy()
		add := qty.DeepCopy()
		total.Add(add)
		dst[name] = total
	}
}

func occupies(pod *corev1.Pod) bool {
	return pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed
}

func samePod(a, b *corev1.Pod) bool {
	if a == nil || b == nil {
		return false
	}
	if a.UID != "" && b.UID != "" {
		return a.UID == b.UID
	}
	return a.Namespace == b.Namespace && a.Name != "" && a.Name == b.Name
}
