package triton

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"sync"
	"time"

	triton "github.com/joyent/triton-go"
	"github.com/joyent/triton-go/authentication"
	"github.com/joyent/triton-go/compute"
	"github.com/virtual-kubelet/virtual-kubelet/manager"
	"github.com/y0ssar1an/q"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"
)

// TritonProvider implements the virtual-kubelet provider interface.
type TritonProvider struct {
	probes             map[string]bool
	resourceManager    *manager.ResourceManager
	nodeName           string
	operatingSystem    string
	internalIP         string
	daemonEndpointPort int32

	client *Client

	// Triton resources.
	capacity           capacity
	platformVersion    string
	lastTransitionTime time.Time
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

// NewTritonProvider creates a new Triton provider.
func NewTritonProvider(
	config string,
	rm *manager.ResourceManager,
	nodeName string,
	operatingSystem string,
	internalIP string,
	daemonEndpointPort int32) (*TritonProvider, error) {

	// Create the Triton provider.
	log.Println("Creating Triton provider.")

	keyID := os.Getenv("SDC_KEY_ID")
	accountName := os.Getenv("SDC_ACCOUNT")
	keyMaterial := os.Getenv("SDC_KEY_MATERIAL")
	userName := os.Getenv("SDC_USER")
	insecure := false
	if os.Getenv("SDC_INSECURE") != "" {
		insecure = true
	}

	var signer authentication.Signer
	var err error

	if keyMaterial == "" {
		input := authentication.SSHAgentSignerInput{
			KeyID:       keyID,
			AccountName: accountName,
			Username:    userName,
		}
		signer, err = authentication.NewSSHAgentSigner(input)
		if err != nil {
			log.Fatalf("Error Creating SSH Agent Signer: {{err}}", err)
		}
	} else {
		var keyBytes []byte
		if _, err = os.Stat(keyMaterial); err == nil {
			keyBytes, err = ioutil.ReadFile(keyMaterial)
			if err != nil {
				log.Fatalf("Error reading key material from %s: %s",
					keyMaterial, err)
			}
			block, _ := pem.Decode(keyBytes)
			if block == nil {
				log.Fatalf(
					"Failed to read key material '%s': no key found", keyMaterial)
			}

			if block.Headers["Proc-Type"] == "4,ENCRYPTED" {
				log.Fatalf(
					"Failed to read key '%s': password protected keys are\n"+
						"not currently supported. Please decrypt the key prior to use.", keyMaterial)
			}

		} else {
			keyBytes = []byte(keyMaterial)
		}

		input := authentication.PrivateKeySignerInput{
			KeyID:              keyID,
			PrivateKeyMaterial: keyBytes,
			AccountName:        accountName,
			Username:           userName,
		}
		signer, err = authentication.NewPrivateKeySigner(input)
		if err != nil {
			log.Fatalf("Error Creating SSH Private Key Signer: {{err}}", err)
		}
	}

	tritonConfig := &triton.ClientConfig{
		TritonURL:   os.Getenv("SDC_URL"),
		AccountName: accountName,
		Username:    userName,
		Signers:     []authentication.Signer{signer},
	}

	p := TritonProvider{
		probes:             make(map[string]bool),
		resourceManager:    rm,
		nodeName:           nodeName,
		operatingSystem:    operatingSystem,
		internalIP:         internalIP,
		daemonEndpointPort: daemonEndpointPort,
		client: &Client{
			config:                tritonConfig,
			insecureSkipTLSVerify: insecure,
			affinityLock:          &sync.RWMutex{},
		},
	}

	//Read the Triton provider configuration file.
	configErr := p.loadConfigFile(config)
	if configErr != nil {
		err = fmt.Errorf("failed to load configuration file %s: %v", config, err)
		return nil, err
	}

	log.Printf("Loaded provider Configuration file %s.", config)

	log.Printf("Created Triton provider: %+v.", p)

	return &p, nil
}

func (p *TritonProvider) Capacity(ctx context.Context) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:     resource.MustParse(p.capacity.cpu),
		corev1.ResourceMemory:  resource.MustParse(p.capacity.memory),
		corev1.ResourceStorage: resource.MustParse(p.capacity.storage),
		corev1.ResourcePods:    resource.MustParse(p.capacity.pods),
	}
}

// CreatePod takes a Kubernetes Pod and deploys it within the Triton provider.
func (p *TritonProvider) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	log.Printf("Received CreatePod request for %+v.\n", pod)

	PodSpec, _ := json.Marshal(pod)

	c, err := p.client.Compute()
	i, err := c.Instances().Create(ctx, &compute.CreateInstanceInput{
		Image:   pod.Spec.Containers[0].Image,
		Package: pod.ObjectMeta.Labels["package"],
		Name:    pod.Name,
		Tags: map[string]string{
			"PodName":           pod.Name,
			"NodeName":          pod.Spec.NodeName,
			"Namespace":         pod.Namespace,
			"UID":               string(pod.UID),
			"CreationTimestamp": pod.CreationTimestamp.String(),
			"PodSpec":           string(PodSpec),
		},
	})
	if err != nil {
		return err
	}
	fmt.Println("Created: " + i.Name)

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

	delete(p.probes, p.GetPodFullName(pod.Namespace, pod.Name))

	tags := make(map[string]interface{})
	tags["NodeName"] = pod.Spec.NodeName
	tags["Namespace"] = pod.Namespace

	c, _ := p.client.Compute()
	is, _ := c.Instances().List(ctx, &compute.ListInstancesInput{
		Name: pod.Name,
		Tags: tags,
	})

	for _, i := range is {
		return c.Instances().Delete(ctx, &compute.DeleteInstanceInput{ID: i.ID})
	}
	return nil
}

// GetPod retrieves a pod by name from the provider (can be cached).
func (p *TritonProvider) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	log.Printf("Received GetPod request for %s/%s.\n", namespace, name)

	tags := make(map[string]interface{})
	tags["NodeName"] = p.nodeName
	tags["Namespace"] = namespace

	c, _ := p.client.Compute()
	is, _ := c.Instances().List(ctx, &compute.ListInstancesInput{
		Name: name,
		Tags: tags,
	})

	for _, i := range is {
		return instanceToPod(i)
	}

	return nil, nil
}

// GetContainerLogs retrieves the logs of a container by name from the provider.
func (p *TritonProvider) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, tail int) (string, error) {
	log.Printf("Received GetContainerLogs request for %s/%s/%s.\n", namespace, podName, containerName)
	return "", nil
}

// GetPodFullName retrieves the full pod name as defined in the provider context.
func (p *TritonProvider) GetPodFullName(namespace string, pod string) string {
	return fmt.Sprintf("%s-%s", namespace, pod)
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

	pod, err := p.GetPod(ctx, namespace, name)

	if err != nil {
		return nil, err
	}

	if pod == nil {
		return nil, nil
	}

	if pod.Spec.Containers[0].LivenessProbe != nil || pod.Spec.Containers[0].ReadinessProbe != nil && pod.Status.PodIP != "" {
		p.RunProbes(ctx, pod)
	}

	return &pod.Status, nil
}

// GetPods retrieves a list of all pods running on the provider (can be cached).
func (p *TritonProvider) GetPods(ctx context.Context) ([]*corev1.Pod, error) {
	log.Println("Received GetPods request.")

	c, _ := p.client.Compute()
	is, _ := c.Instances().List(ctx, &compute.ListInstancesInput{})

	pods := make([]*corev1.Pod, 0, len(is))
	for _, i := range is {
		if i.Tags["NodeName"] == p.nodeName {
			p, _ := instanceToPod(i)
			pods = append(pods, p)
		}
	}

	return pods, nil
}

// NodeConditions returns a list of conditions (Ready, OutOfDisk, etc), which is polled
// periodically to update the node status within Kubernetes.
func (p *TritonProvider) NodeConditions(ctx context.Context) []corev1.NodeCondition {
	log.Println("Received NodeConditions request.")

	lastHeartbeatTime := metav1.Now()
	lastTransitionTime := metav1.NewTime(p.lastTransitionTime)
	lastTransitionReason := "Triton is ready"
	lastTransitionMessage := "ok"

	// Return static thumbs-up values for all conditions.
	return []corev1.NodeCondition{
		{
			Type:               corev1.NodeReady,
			Status:             corev1.ConditionTrue,
			LastHeartbeatTime:  lastHeartbeatTime,
			LastTransitionTime: lastTransitionTime,
			Reason:             lastTransitionReason,
			Message:            lastTransitionMessage,
		},
		{
			Type:               corev1.NodeOutOfDisk,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  lastHeartbeatTime,
			LastTransitionTime: lastTransitionTime,
			Reason:             lastTransitionReason,
			Message:            lastTransitionMessage,
		},
		{
			Type:               corev1.NodeMemoryPressure,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  lastHeartbeatTime,
			LastTransitionTime: lastTransitionTime,
			Reason:             lastTransitionReason,
			Message:            lastTransitionMessage,
		},
		{
			Type:               corev1.NodeDiskPressure,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  lastHeartbeatTime,
			LastTransitionTime: lastTransitionTime,
			Reason:             lastTransitionReason,
			Message:            lastTransitionMessage,
		},
		{
			Type:               corev1.NodeNetworkUnavailable,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  lastHeartbeatTime,
			LastTransitionTime: lastTransitionTime,
			Reason:             lastTransitionReason,
			Message:            lastTransitionMessage,
		},
		{
			Type:               "KubeletConfigOk",
			Status:             corev1.ConditionTrue,
			LastHeartbeatTime:  lastHeartbeatTime,
			LastTransitionTime: lastTransitionTime,
			Reason:             lastTransitionReason,
			Message:            lastTransitionMessage,
		},
	}
}

// NodeAddresses returns a list of addresses for the node status within Kubernetes.
func (p *TritonProvider) NodeAddresses(ctx context.Context) []corev1.NodeAddress {
	log.Println("Received NodeAddresses request.")

	return []corev1.NodeAddress{
		{
			Type:    corev1.NodeInternalIP,
			Address: p.internalIP,
		},
	}
}

// NodeDaemonEndpoints returns NodeDaemonEndpoints for the node status within Kubernetes.
func (p *TritonProvider) NodeDaemonEndpoints(ctx context.Context) *corev1.NodeDaemonEndpoints {
	log.Println("Received NodeDaemonEndpoints request.")

	return &corev1.NodeDaemonEndpoints{
		KubeletEndpoint: corev1.DaemonEndpoint{
			Port: p.daemonEndpointPort,
		},
	}
}

// OperatingSystem returns the operating system the provider is for.
func (p *TritonProvider) OperatingSystem() string {
	log.Println("Received OperatingSystem request.")

	return p.operatingSystem
}

func instanceToPod(i *compute.Instance) (*corev1.Pod, error) {

	// Get Pod Spec from the Metadata
	bytes := []byte(fmt.Sprint(i.Tags["PodSpec"]))
	var podSpec *corev1.Pod
	json.Unmarshal(bytes, &podSpec)

	// Take Care of time
	var podCreationTimestamp metav1.Time

	podCreationTimestamp = metav1.NewTime(i.Created)
	// TODO Find a way to get this
	//containerStartTime := metav1.NewTime(time.Now())

	/*
	   On Triton we do not share Namespaces, so init Pod Groups or Patterns which encourage this aren't implement.   This implementation Maps 1 instance to 1 pod.
	*/
	container := corev1.Container{
		//Name string `json:"name" protobuf:"bytes,1,opt,name=name"`
		Name: i.Name,
		//Image string `json:"image,omitempty" protobuf:"bytes,2,opt,name=image"`
		Image: i.Image,
		//Command []string `json:"command,omitempty" protobuf:"bytes,3,rep,name=command"`
		//Args []string `json:"args,omitempty" protobuf:"bytes,4,rep,name=args"`
		//WorkingDir string `json:"workingDir,omitempty" protobuf:"bytes,5,opt,name=workingDir"`
		//Ports []ContainerPort `json:"ports,omitempty" patchStrategy:"merge" patchMergeKey:"containerPort" protobuf:"bytes,6,rep,name=ports"`
		//EnvFrom []EnvFromSource `json:"envFrom,omitempty" protobuf:"bytes,19,rep,name=envFrom"`
		//Env []EnvVar `json:"env,omitempty" patchStrategy:"merge" patchMergeKey:"name" protobuf:"bytes,7,rep,name=env"`
		//Resources ResourceRequirements `json:"resources,omitempty" protobuf:"bytes,8,opt,name=resources"`
		//VolumeMounts []VolumeMount `json:"volumeMounts,omitempty" patchStrategy:"merge" patchMergeKey:"mountPath" protobuf:"bytes,9,rep,name=volumeMounts"`
		//VolumeDevices []VolumeDevice `json:"volumeDevices,omitempty" patchStrategy:"merge" patchMergeKey:"devicePath" protobuf:"bytes,21,rep,name=volumeDevices"`
		//LivenessProbe *Probe `json:"livenessProbe,omitempty" protobuf:"bytes,10,opt,name=livenessProbe"`
		LivenessProbe: podSpec.Spec.Containers[0].LivenessProbe,
		//ReadinessProbe *Probe `json:"readinessProbe,omitempty" protobuf:"bytes,11,opt,name=readinessProbe"`
		//Lifecycle *Lifecycle `json:"lifecycle,omitempty" protobuf:"bytes,12,opt,name=lifecycle"`
		//TerminationMessagePath string `json:"terminationMessagePath,omitempty" protobuf:"bytes,13,opt,name=terminationMessagePath"`
		//TerminationMessagePolicy TerminationMessagePolicy `json:"terminationMessagePolicy,omitempty" protobuf:"bytes,20,opt,name=terminationMessagePolicy,casttype=TerminationMessagePolicy"`
		//ImagePullPolicy PullPolicy `json:"imagePullPolicy,omitempty" protobuf:"bytes,14,opt,name=imagePullPolicy,casttype=PullPolicy"`
		//SecurityContext *SecurityContext `json:"securityContext,omitempty" protobuf:"bytes,15,opt,name=securityContext"`
		//Stdin bool `json:"stdin,omitempty" protobuf:"varint,16,opt,name=stdin"`
		//StdinOnce bool `json:"stdinOnce,omitempty" protobuf:"varint,17,opt,name=stdinOnce"`
		//TTY bool `json:"tty,omitempty" protobuf:"varint,18,opt,name=tty"`
	}

	containerStatus := corev1.ContainerStatus{
		//Name string `json:"name" protobuf:"bytes,1,opt,name=name"`
		Name: i.Name,
		//State ContainerState `json:"state,omitempty" protobuf:"bytes,2,opt,name=state"`
		State: instanceStateToContainerState(i),
		//LastTerminationState ContainerState `json:"lastState,omitempty" protobuf:"bytes,3,opt,name=lastState"`
		//Ready bool `json:"ready" protobuf:"varint,4,opt,name=ready"`
		Ready: instanceStateToPodPhase(i.State) == corev1.PodRunning,
		//RestartCount int32 `json:"restartCount" protobuf:"varint,5,opt,name=restartCount"`
		//Image string `json:"image" protobuf:"bytes,6,opt,name=image"`
		//ImageID string `json:"imageID" protobuf:"bytes,7,opt,name=imageID"`
		//ContainerID string `json:"containerID,omitempty" protobuf:"bytes,8,opt,name=containerID"`
	}

	containers := make([]corev1.Container, 0, 1)
	containerStatuses := make([]corev1.ContainerStatus, 0, 1)

	containers = append(containers, container)
	containerStatuses = append(containerStatuses, containerStatus)
	p := corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              i.Name,
			Namespace:         fmt.Sprint(i.Tags["Namespace"]),
			UID:               types.UID(fmt.Sprint(i.Tags["UID"])),
			CreationTimestamp: podCreationTimestamp,
		},
		Spec: corev1.PodSpec{
			NodeName:   fmt.Sprint(i.Tags["NodeName"]),
			Volumes:    []corev1.Volume{},
			Containers: containers,
		},
		Status: corev1.PodStatus{
			Phase:      instanceStateToPodPhase(i.State),
			Conditions: instanceStateToPodConditions(i.State, podCreationTimestamp),
			Message:    "",
			Reason:     "",
			HostIP:     i.PrimaryIP,
			PodIP:      i.PrimaryIP,
			//StartTime:         &containerStartTime,
			ContainerStatuses: containerStatuses,
		},
	}

	return &p, nil
}

func instanceStateToPodPhase(state string) corev1.PodPhase {
	switch state {
	case "provisioning":
		return corev1.PodPending
	case "running":
		return corev1.PodRunning
	case "failed":
		return corev1.PodFailed
	case "deleted":
		return corev1.PodFailed
	case "stopped":
		return corev1.PodFailed
	case "stopping":
		return corev1.PodPending
	}
	return corev1.PodUnknown
}

func instanceStateToPodConditions(state string, transitiontime metav1.Time) []corev1.PodCondition {
	switch state {
	case "Running":
		return []corev1.PodCondition{
			corev1.PodCondition{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: transitiontime,
			}, corev1.PodCondition{
				Type:               corev1.PodInitialized,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: transitiontime,
			}, corev1.PodCondition{
				Type:               corev1.PodScheduled,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: transitiontime,
			},
		}
	}
	return []corev1.PodCondition{}
}

func instanceStateToContainerState(i *compute.Instance) corev1.ContainerState {
	startTime := metav1.NewTime(time.Now())

	// Handle the case where the container is running.
	if i.State == "running" {
		return corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{
				StartedAt: startTime,
			},
		}
	}

	// Handle the case where the container failed.
	if i.State == "failed" {
		return corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   0,
				Reason:     i.State,
				Message:    i.State,
				StartedAt:  startTime,
				FinishedAt: metav1.NewTime(time.Now()),
			},
		}
	}

	state := i.State
	if state == "" {
		state = "provisioning"
	}

	// Handle the case where the container is pending.
	// Which should be all other aci states.
	return corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{
			Reason:  state,
			Message: i.State,
		},
	}
}

func (p *TritonProvider) RunProbes(ctx context.Context, pod *corev1.Pod) error {
	fullname := p.GetPodFullName(pod.Namespace, pod.Name)
	if p.probes[fullname] {
		return nil
	}

	p.probes[fullname] = true

	go func() {
		failcount := 0
		time.Sleep(time.Duration(pod.Spec.Containers[0].LivenessProbe.InitialDelaySeconds) * time.Second)
		for {
			q.Q(p.probes)
			if !p.probes[fullname] {
				break
			}

			time.Sleep(time.Duration(pod.Spec.Containers[0].LivenessProbe.PeriodSeconds) * time.Second)
			fmt.Println(pod.Name + ": Running TCP Check")
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(pod.Status.PodIP, fmt.Sprint(pod.Spec.Containers[0].LivenessProbe.Handler.TCPSocket.Port.IntVal)), time.Duration(pod.Spec.Containers[0].LivenessProbe.TimeoutSeconds)*time.Second)
			q.Q(fmt.Sprint(pod.Spec.Containers[0].LivenessProbe.Handler.TCPSocket.Port.IntVal))
			q.Q(pod.Status.PodIP)
			if err != nil {
				failcount++
				fmt.Println(pod.Name + ": Shit is broken " + fmt.Sprint(failcount))
			}
			if conn != nil {
				conn.Close()
				fmt.Println(pod.Name + ": Port 22 is listening")
			}

		}
	}()
	return nil
}
