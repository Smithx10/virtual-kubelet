package triton

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/virtual-kubelet/virtual-kubelet/manager"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"
)

// TritonProvider implements the virtual-kubelet provider interface.
type TritonProvider struct {
	resourceManager    *manager.ResourceManager
	nodeName           string
	operatingSystem    string
	internalIP         string
	daemonEndpointPort int32

	// Triton resources.
	region         string
	subnets        []string
	securityGroups []string

	// Triton resources.
	//cluster                 *fargate.Cluster
	clusterName     string
	capacity        capacity
	platformVersion string
}

// Capacity represents the provisioned capacity on a Triton cluster.
type capacity struct {
	cpu     string
	memory  string
	storage string
	pods    string
}

var (
	errNotImplemented = fmt.Errorf("not implemented by Triton provider")
)

// NewTritonProvider creates a new Fargate provider.
func NewTritonProvider(
	config string,
	rm *manager.ResourceManager,
	nodeName string,
	operatingSystem string,
	internalIP string,
	daemonEndpointPort int32) (*TritonProvider, error) {

	// Create the Triton provider.
	log.Println("Creating Triton provider.")

	p := TritonProvider{
		resourceManager:    rm,
		nodeName:           nodeName,
		operatingSystem:    operatingSystem,
		internalIP:         internalIP,
		daemonEndpointPort: daemonEndpointPort,
	}

	log.Printf("Created Triton provider: %+v.", p)

	return &p, nil
}

func (p *TritonProvider) Capacity(ctx context.Context) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:     resource.MustParse("100"),
		corev1.ResourceMemory:  resource.MustParse("100Gi"),
		corev1.ResourceStorage: resource.MustParse("100Gi"),
		corev1.ResourcePods:    resource.MustParse("100"),
	}
}

// CreatePod takes a Kubernetes Pod and deploys it within the Fargate provider.
func (p *TritonProvider) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	log.Printf("Received CreatePod request for %+v.\n", pod)

	return nil
}

// UpdatePod takes a Kubernetes Pod and updates it within the provider.
func (p *TritonProvider) UpdatePod(ctx context.Context, pod *corev1.Pod) error {
	log.Printf("Received UpdatePod request for %s/%s.\n", pod.Namespace, pod.Name)
	return errNotImplemented
}

// DeletePod takes a Kubernetes Pod and deletes it from the provider.
func (p *TritonProvider) DeletePod(ctx context.Context, pod *corev1.Pod) error {
	log.Printf("Received DeletePod request for %s/%s.\n", pod.Namespace, pod.Name)

	return nil
}

// GetPod retrieves a pod by name from the provider (can be cached).
func (p *TritonProvider) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	log.Printf("Received GetPod request for %s/%s.\n", namespace, name)

	return nil, nil
}

// GetContainerLogs retrieves the logs of a container by name from the provider.
func (p *TritonProvider) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, tail int) (string, error) {
	log.Printf("Received GetContainerLogs request for %s/%s/%s.\n", namespace, podName, containerName)
	return "", nil
}

// GetPodFullName retrieves the full pod name as defined in the provider context.
func (p *TritonProvider) GetPodFullName(namespace string, pod string) string {
	return ""
}

// ExecInContainer executes a command in a container in the pod, copying data
// between in/out/err and the container's stdin/stdout/stderr.
func (p *TritonProvider) ExecInContainer(
	name string, uid types.UID, container string, cmd []string, in io.Reader, out, err io.WriteCloser,
	tty bool, resize <-chan remotecommand.TerminalSize, timeout time.Duration) error {
	log.Printf("Received ExecInContainer request for %s.\n", container)
	return errNotImplemented
}

// GetPodStatus retrieves the status of a pod by name from the provider.
func (p *TritonProvider) GetPodStatus(ctx context.Context, namespace, name string) (*corev1.PodStatus, error) {
	log.Printf("Received GetPodStatus request for %s/%s.\n", namespace, name)

	return nil, nil
}

// GetPods retrieves a list of all pods running on the provider (can be cached).
func (p *TritonProvider) GetPods(ctx context.Context) ([]*corev1.Pod, error) {
	log.Println("Received GetPods request.")

	return nil, nil
}

// NodeConditions returns a list of conditions (Ready, OutOfDisk, etc), which is polled
// periodically to update the node status within Kubernetes.
func (p *TritonProvider) NodeConditions(ctx context.Context) []corev1.NodeCondition {
	log.Println("Received NodeConditions request.")

	// Return static thumbs-up values for all conditions.
	return nil
}

// NodeAddresses returns a list of addresses for the node status within Kubernetes.
func (p *TritonProvider) NodeAddresses(ctx context.Context) []corev1.NodeAddress {
	log.Println("Received NodeAddresses request.")

	return nil
}

// NodeDaemonEndpoints returns NodeDaemonEndpoints for the node status within Kubernetes.
func (p *TritonProvider) NodeDaemonEndpoints(ctx context.Context) *corev1.NodeDaemonEndpoints {
	log.Println("Received NodeDaemonEndpoints request.")

	return nil
}

// OperatingSystem returns the operating system the provider is for.
func (p *TritonProvider) OperatingSystem() string {
	log.Println("Received OperatingSystem request.")

	return p.operatingSystem
}
