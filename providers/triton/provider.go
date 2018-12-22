package triton

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	triton "github.com/joyent/triton-go"
	"github.com/joyent/triton-go/authentication"
	"github.com/joyent/triton-go/compute"
	"github.com/joyent/triton-go/network"
	"github.com/virtual-kubelet/virtual-kubelet/manager"
	"github.com/y0ssar1an/q"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"k8s.io/api/core/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/remotecommand"
)

// Triton Pod Struct
type TritonPod struct {
	shutdownCtx context.Context
	shutdown    context.CancelFunc
	pod         *corev1.Pod
	statusLock  sync.RWMutex
	createLock  sync.RWMutex
	probes      map[string]*TritonProbe
	fwrs        []*network.FirewallRule
	fn          string
	backoff     *Backoff
}

// Backoff Struct
type Backoff struct {
	max       time.Duration
	delay     int
	delayLock sync.RWMutex
	start     time.Time
	end       time.Time
}

// Triton Probe Struct
type TritonProbe struct {
	TargetIP            string
	Exec                *corev1.ExecAction
	HTTPGet             *corev1.HTTPGetAction
	TCPSocket           *corev1.TCPSocketAction
	InitialDelaySeconds int32
	Period              int32
	FailureThreshold    int32
	SuccessThreshold    int32
	TimeoutSeconds      int32
}

type TritonFWGroup struct {
	// Question:  Would it be preferable to reuse the *triton.Pod,  and point to that alot or just use a string?
	members     []string
	membersLock sync.RWMutex
	fwrs        []*network.FirewallRule
}

// TritonProvider implements the virtual-kubelet provider interface.
type TritonProvider struct {
	//pods map[*corev1.Pod]map[string]*TritonProbe
	daemonEndpointPort int32
	internalIP         string
	nodeName           string
	operatingSystem    string
	k8sClient          *kubernetes.Clientset
	recorder           record.EventRecorder
	resourceManager    *manager.ResourceManager

	// Triton Specific
	tclient *Client
	dclient *docker.Client
	fwgs    map[string]*TritonFWGroup
	pods    map[string]*TritonPod

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

func (p *TritonProvider) NewTritonFWGroup() *TritonFWGroup {
	fwg := &TritonFWGroup{
		members: make([]string, 0),
		fwrs:    make([]*network.FirewallRule, 0),
	}
	return fwg
}

func (p *TritonProvider) GetInstStatus(tp *TritonPod) {
	for {
		select {
		case <-tp.shutdownCtx.Done():
			return
		default:
			c, err := p.tclient.Compute()
			if err != nil {
				return
			}
			i, err := c.Instances().Get(tp.shutdownCtx, &compute.GetInstanceInput{ID: tp.pod.Status.ContainerStatuses[0].ContainerID})
			if err != nil {
				return
			}

			tp.statusLock.Lock()
			// Handle Pod Phase
			tp.pod.Status.Phase = instanceStateToPodPhase(i.State)
			// Handle The Container Level State
			tp.pod.Status.ContainerStatuses[0].State = instanceStateToContainerState(i.State)

			// Handle Readiness if Probe is nil
			if tp.probes["readiness"] == nil {
				tp.pod.Status.ContainerStatuses[0].Ready = instanceStateToPodPhase(i.State) == corev1.PodRunning
			}
			tp.statusLock.Unlock()

			// Poll time for Instance State
			time.Sleep(5 * time.Second)
		}
	}
}

//  Restart Instance and Bump the Count
func (p *TritonProvider) RestartInstance(tp *TritonPod) {
	ContainerID := tp.pod.Status.ContainerStatuses[0].ContainerID
	// Convert Metav1.Time to time.Time
	LastTerminate := tp.pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.StartedAt.Format(time.RFC3339)
	LastTerm, _ := time.Parse(time.RFC3339, LastTerminate)

	c, err := p.tclient.Compute()
	if err != nil {
		return
	}

	if tp.pod.Spec.RestartPolicy != "Never" {

		// Reset the Window if we've passed the success window (5 minutes) without having to fire a restart
		if tp.backoff.end.Add(5 * time.Minute).Before(LastTerm) {
			tp.backoff.start = time.Time{}
		}
		// Set the Window
		if (tp.backoff.start == time.Time{}) {
			fmt.Println("Setting the Backoff Window Start and End")
			tp.backoff.start = time.Now()
			tp.backoff.end = tp.backoff.start.Add(tp.backoff.max)
		}
		// Explcitly Mark the Instance Not Ready.
		tp.pod.Status.ContainerStatuses[0].Ready = false
		// See if we need to Reschedule.  If LastTerm is outside of the Window we should set the phase to Failed.  In a replica set,  this will force a reschedule.
		if tp.backoff.end.Before(LastTerm) {
			p.FailInstance(tp, LastTerm)
			return
		}
		// Get Instance State
		i, err := c.Instances().Get(tp.shutdownCtx, &compute.GetInstanceInput{ID: ContainerID})
		if err != nil {
			fmt.Println("TODO, Think about this.")
		}

		if i.State == "running" || i.State == "provisioning" {
			// Restart the Instance
			c.Instances().Reboot(tp.shutdownCtx, &compute.RebootInstanceInput{InstanceID: ContainerID})
		}
		if i.State == "stopped" || i.State == "failed" {
			// Start the instance
			c.Instances().Start(tp.shutdownCtx, &compute.StartInstanceInput{InstanceID: ContainerID})
		}
		// Bump The Restart Count
		tp.pod.Status.ContainerStatuses[0].RestartCount++
		// Bump the Delay up
		tp.backoff.delayLock.Lock()
		tp.backoff.delay = tp.backoff.delay * 2
		tp.backoff.delayLock.Unlock()
		// Sleep The Delay
		time.Sleep(time.Duration(tp.backoff.delay) * time.Second)
		// Restart Probes
		// Liveness
		if tp.pod.Spec.Containers[0].LivenessProbe != nil {
			go p.RunLiveness(tp)
		}
		// Readiness
		if tp.pod.Spec.Containers[0].ReadinessProbe != nil {
			go p.RunReadiness(tp)
		}
	}
}

func (p *TritonProvider) FailInstance(tp *TritonPod, LastTerm time.Time) {
	ContainerID := tp.pod.Status.ContainerStatuses[0].ContainerID

	c, err := p.tclient.Compute()
	if err != nil {
		return
	}
	// Mark Pod Failed in K8S,  Forces Reschedule for Replicasets
	tp.pod.Status.Phase = instanceStateToPodPhase("failed")

	// Stop The Instance and Add a failed tag with the time.
	c.Instances().Stop(tp.shutdownCtx, &compute.StopInstanceInput{InstanceID: ContainerID})
	c.Instances().AddTags(tp.shutdownCtx, &compute.AddTagsInput{
		ID: ContainerID,
		Tags: map[string]string{
			"k8s_failed": LastTerm.Format(time.RFC3339),
		},
	})
	tp.shutdown()
}

func (p *TritonProvider) NewTritonProbe(ip string, probe *corev1.Probe) (*TritonProbe, error) {
	tprobe := &TritonProbe{
		TargetIP:            ip,
		Exec:                probe.Handler.Exec,
		HTTPGet:             probe.Handler.HTTPGet,
		TCPSocket:           probe.Handler.TCPSocket,
		InitialDelaySeconds: probe.InitialDelaySeconds,
		TimeoutSeconds:      probe.TimeoutSeconds,
		Period:              probe.PeriodSeconds,
		SuccessThreshold:    probe.SuccessThreshold,
		FailureThreshold:    probe.FailureThreshold,
	}

	return tprobe, nil
}

func (p *TritonProvider) RunProbe(probe *TritonProbe) error {
	// TODO: Wire Up A probe to talk to ContainerPilot
	// TODO: Handle Exec,  Exploring the use of SSH clients in Go.
	// Handle TCP
	if probe.TCPSocket != nil {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(probe.TargetIP, probe.TCPSocket.Port.String()), time.Duration(probe.TimeoutSeconds)*time.Second)
		if err != nil {
			return err
		}
		if c != nil {
			c.Close()
		}
	}
	// Handle HTTP
	if probe.HTTPGet != nil {
		client := http.Client{
			Timeout: time.Duration(time.Duration(probe.TimeoutSeconds) * time.Second),
		}
		r, err := client.Get(fmt.Sprintf("http://%s:%d%s", probe.TargetIP, probe.HTTPGet.Port.IntVal, probe.HTTPGet.Path))
		if err != nil {
			return err
		}
		if r.StatusCode >= 200 && r.StatusCode < 400 {
			return err
		}
		if probe.HTTPGet.HTTPHeaders != nil {
			if !(r.Header.Get(probe.HTTPGet.HTTPHeaders[0].Name) == probe.HTTPGet.HTTPHeaders[0].Value) {
				return err
			}
		}
	}
	return nil
}

// Liveness
func (p *TritonProvider) RunLiveness(tp *TritonPod) {
	//Set Cleaner Var
	l := tp.probes["liveness"]
	//Perform Initial Liveness Delay
	time.Sleep(time.Duration(l.InitialDelaySeconds) * time.Second)
	//Set Failure Count.
	failcount := 0
	for {
		select {
		case <-tp.shutdownCtx.Done():
			return
		default:
			err := p.RunProbe(l)
			if err != nil {
				failcount++
			}
			if failcount == int(l.FailureThreshold) {
				fmt.Println("Liveness FailureThreshold Hit.  Restarting the Container")
				tp.pod.Status.ContainerStatuses[0].State = instanceStateToContainerState("failed")
				tp.pod.Status.ContainerStatuses[0].LastTerminationState = instanceStateToContainerState("failed")
				p.RestartInstance(tp)
				return
			}
		}
		time.Sleep(time.Duration(l.Period) * time.Second)
	}
}

// Readiness
func (p *TritonProvider) RunReadiness(tp *TritonPod) {
	//Set Cleaner Var
	r := tp.probes["readiness"]
	//Perform Initial Readiness Delay
	time.Sleep(time.Duration(r.InitialDelaySeconds) * time.Second)
	//Set Success Count.
	successcount := 0
	//Set Failure Count.
	failcount := 0
	for {
		select {
		case <-tp.shutdownCtx.Done():
			return
		default:
			err := p.RunProbe(r)
			if err != nil {
				failcount++
			}
			if err == nil {
				successcount++
			}
			if failcount == int(r.FailureThreshold) {
				fmt.Println("Readiness FailureThreshold Hit.  Marking Container Not Ready")
				tp.statusLock.Lock()
				tp.pod.Status.ContainerStatuses[0].Ready = false
				tp.statusLock.Unlock()
				failcount = 0
			}
			if successcount == int(r.SuccessThreshold) {
				fmt.Println("Readiness SuccessThreshold Hit.  Marking Container Ready")
				tp.statusLock.Lock()
				tp.pod.Status.ContainerStatuses[0].Ready = true
				tp.statusLock.Unlock()
				successcount = 0
			}
		}
		time.Sleep(time.Duration(r.Period) * time.Second)
	}
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
	daemonEndpointPort int32,
	k8sClient *kubernetes.Clientset) (*TritonProvider, error) {

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

	// Triton Client Config
	tritonConfig := &triton.ClientConfig{
		TritonURL:   os.Getenv("SDC_URL"),
		AccountName: accountName,
		Username:    userName,
		Signers:     []authentication.Signer{signer},
	}

	dockerClient, err := docker.NewClientFromEnv()

	// Create an event broadcaster.
	eventBroadcaster := record.NewBroadcaster()
	//eventBroadcaster.StartLogging(log.L.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: k8sClient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: fmt.Sprintf("%s", nodeName)})

	p := TritonProvider{
		pods:               make(map[string]*TritonPod),
		fwgs:               make(map[string]*TritonFWGroup),
		resourceManager:    rm,
		nodeName:           nodeName,
		operatingSystem:    operatingSystem,
		internalIP:         internalIP,
		k8sClient:          k8sClient,
		daemonEndpointPort: daemonEndpointPort,
		recorder:           recorder,
		tclient: &Client{
			config:                tritonConfig,
			insecureSkipTLSVerify: insecure,
			affinityLock:          &sync.RWMutex{},
		},
		dclient: dockerClient,
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

func (p *TritonProvider) NewTritonPod(ctx context.Context, pod *corev1.Pod) (*TritonPod, error) {
	// Use Pod Namespace and Name for map key.
	fn := p.GetPodFullName(pod.Namespace, pod.Name)

	// Assign Probes to TritonPod Struct
	tprobes := make(map[string]*TritonProbe)

	if pod.Spec.Containers[0].LivenessProbe != nil {
		tprobe, err := p.NewTritonProbe(pod.Status.PodIP, pod.Spec.Containers[0].LivenessProbe)
		if err != nil {
			return nil, err
		}
		tprobes["liveness"] = tprobe
	}

	if pod.Spec.Containers[0].ReadinessProbe != nil {
		tprobe, err := p.NewTritonProbe(pod.Status.PodIP, pod.Spec.Containers[0].ReadinessProbe)
		if err != nil {
			return nil, err
		}
		tprobes["readiness"] = tprobe
	}

	// Create the Context for Terminating the GoRoutines which will UpdateState and Phase,  and Run Probes
	ctxTp, cancel := context.WithCancel(ctx)

	// Create BackoffPolicy
	backoff := &Backoff{
		max:   5 * time.Minute,
		delay: 10,
	}

	// Init the New Triton Pod Struct.
	tp := &TritonPod{
		shutdownCtx: ctxTp,
		shutdown:    cancel,
		pod:         pod,
		probes:      tprobes,
		fn:          fn,
		backoff:     backoff,
	}
	return tp, nil

}

func (p *TritonProvider) RunTritonPodLoops(tp *TritonPod) {

	// Kick Off Go Routine which Polls Triton every N seconds for instance status. (See triton.toml for Poll Rate). This Go Routine will update the Containers State, and Pod Phases.  DeletePod will clean up this Routine.
	go p.GetInstStatus(tp)

	// Liveness
	if tp.probes["liveness"] != nil {
		go p.RunLiveness(tp)
	}

	// Readiness
	if tp.probes["readiness"] != nil {
		go p.RunReadiness(tp)
	}

}

// CreatePod takes a Kubernetes Pod and deploys it within the Triton provider.
func (p *TritonProvider) CreatePod(ctx context.Context, pod *corev1.Pod) error {
	log.Printf("Received CreatePod request for %+v.\n", pod)

	p.recorder.Eventf(pod, corev1.EventTypeWarning, "InvalidEnvironmentVariableNames", "Keys [%s] from the EnvFrom configMap %s/%s were skipped since they are considered invalid environment variable names.", "foot", "bar", "baz")

	// Create a Triton Pod  We do this right away so if a delete comes in about this... its on the struct
	tp, _ := p.NewTritonPod(ctx, pod)
	// Add PodSpec to TritonPod
	p.pods[tp.fn] = tp

	tp.createLock.Lock()

	// Marshal the Pod.Spec that was recieved from the Masters and write store it on the instance.  In the event that Virtual Kubelet Crashes we can rehydrate from the tag.
	Pod, _ := json.Marshal(pod)

	if pod.ObjectMeta.Annotations["type"] == "docker" {
		q.Q("docker")
		return nil
	}

	var (
		configMaps = make(map[string]*v1.ConfigMap)
		err        error
		secrets    = make(map[string]*v1.Secret)
	)

	// Grab env and stick it in user_data
	var env_vars string
	key_values := make(map[string]string)

	// Handle EnvFrom
	for _, envFrom := range pod.Spec.Containers[0].EnvFrom {
		switch {
		case envFrom.ConfigMapRef != nil:
			cm := envFrom.ConfigMapRef
			name := cm.Name
			configMap, ok := configMaps[name]
			if !ok {
				optional := cm.Optional != nil && *cm.Optional
				configMap, err = p.resourceManager.GetConfigMap(name, pod.Namespace)
				if err != nil {
					if optional {
						// ignore error when marked optional
						continue
					}
					return err
				}
				configMaps[name] = configMap
			}
			invalidKeys := []string{}
			for k, v := range configMap.Data {
				if len(envFrom.Prefix) > 0 {
					k = envFrom.Prefix + k
				}
				if errMsgs := utilvalidation.IsEnvVarName(k); len(errMsgs) != 0 {
					invalidKeys = append(invalidKeys, k)
					continue
				}
				key_values[k] = v
			}
			if len(invalidKeys) > 0 {
				sort.Strings(invalidKeys)
				p.recorder.Eventf(pod, corev1.EventTypeWarning, "InvalidEnvironmentVariableNames", "Keys [%s] from the EnvFrom configMap %s/%s were skipped since they are considered invalid environment variable names.", strings.Join(invalidKeys, ", "), pod.Namespace, name)
				//fmt.Sprintf("InvalidEnvironmentVariableNames", "Keys [%s] from the EnvFrom configMap %s/%s were skipped since they are considered invalid environment variable names.", strings.Join(invalidKeys, ", "), pod.Namespace, name)
			}
		case envFrom.SecretRef != nil:
			s := envFrom.SecretRef
			name := s.Name
			secret, ok := secrets[name]
			if !ok {
				optional := s.Optional != nil && *s.Optional
				secret, err = p.resourceManager.GetSecret(name, pod.Namespace)
				if err != nil {
					if optional {
						// ignore error when marked optional
						continue
					}
					return err
				}
				secrets[name] = secret
			}

			invalidKeys := []string{}
			for k, v := range secret.Data {
				if len(envFrom.Prefix) > 0 {
					k = envFrom.Prefix + k
				}
				if errMsgs := utilvalidation.IsEnvVarName(k); len(errMsgs) != 0 {
					invalidKeys = append(invalidKeys, k)
					continue
				}
				key_values[k] = string(v)
			}
			if len(invalidKeys) > 0 {
				sort.Strings(invalidKeys)
				fmt.Sprintf("InvalidEnvironmentVariableNames", "Keys [%s] from the EnvFrom secret %s/%s were skipped since they are considered invalid environment variable names.", strings.Join(invalidKeys, ", "), pod.Namespace, name)
			}
		}
	}

	if pod.Spec.Containers[0].Env != nil {
		invalidKeys := []string{}
		for _, v := range pod.Spec.Containers[0].Env {
			if errMsgs := utilvalidation.IsEnvVarName(v.Name); len(errMsgs) != 0 {
				invalidKeys = append(invalidKeys, v.Name)
				continue
			}
			key_values[v.Name] = v.Value
		}
		environment, _ := json.Marshal(key_values)
		env_vars = string(environment)
		if len(invalidKeys) > 0 {
			sort.Strings(invalidKeys)
			fmt.Sprintf("InvalidEnvironmentVariableNames", "Keys [%s] from the EnvFrom secret %s/%s were skipped since they are considered invalid environment variable names.", strings.Join(invalidKeys, ", "), pod.Namespace, pod.Name)
		}
	} else {
		env_vars = "\"unset\""
	}

	// Build Metadata
	metadata := make(map[string]string)
	metadata["user-data"] = "{\"env_vars\": " + env_vars + "}"
	metadata["k8s_pod"] = string(Pod)

	// Iterate over Annotations Keys that  shouldn't be stored as Metadata on the Triton Instance
	for k, v := range pod.ObjectMeta.Annotations {
		if k != "fwenabled" && k != "fwgroup" && k != "networks" && k != "package" && k != "affinity" && k != "delprotect" {
			metadata[k] = v
		}
	}

	// Build Tags
	tags := make(map[string]string)
	if pod.ObjectMeta.Labels != nil {
		for k, v := range pod.ObjectMeta.Labels {
			tags[k] = v
		}
	}
	// Build Tags: Add *corev1.Pod to Pod
	tags["k8s_namespace"] = pod.Namespace
	tags["k8s_nodename"] = p.nodeName
	tags["k8s_uid"] = string(pod.UID)

	// Build Tags: firewall group
	if pod.ObjectMeta.Annotations["fwgroup"] != "" {
		tags["k8s_fwgroup"] = pod.ObjectMeta.Annotations["fwgroup"]
		tags[fmt.Sprintf("k8s_%s", pod.ObjectMeta.Annotations["fwgroup"])] = "true"

	}

	// Firewall Enabled
	var fwenabled bool
	if pod.ObjectMeta.Annotations["fwenabled"] == "true" {
		fwenabled = true
	}
	if pod.ObjectMeta.Annotations["fwenabled"] == "false" {
		fwenabled = false
	}
	if pod.ObjectMeta.Annotations["fwenabled"] == "" {
		fwenabled = false
	}

	// Build Networks
	var networks []string
	if pod.ObjectMeta.Annotations["networks"] != "" {
		r := csv.NewReader(strings.NewReader(pod.ObjectMeta.Annotations["networks"]))
		r.TrimLeadingSpace = true
		for {
			record, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				log.Fatal(err)
			}
			networks = record
		}
	}

	// Build Affinity
	var affinity []string
	if pod.ObjectMeta.Annotations["affinity"] != "" {
		r := csv.NewReader(strings.NewReader(pod.ObjectMeta.Annotations["affinity"]))
		r.TrimLeadingSpace = true
		for {
			record, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				log.Fatal(err)
			}
			affinity = record
		}
	}

	//  Reach out to Triton to create an Instance
	c, err := p.tclient.Compute()
	if err != nil {
		return err
	}
	i, err := c.Instances().Create(ctx, &compute.CreateInstanceInput{
		Image:           pod.Spec.Containers[0].Image,
		Package:         pod.ObjectMeta.Annotations["package"],
		Name:            pod.Name,
		Tags:            tags,
		Networks:        networks,
		Metadata:        metadata,
		Affinity:        affinity,
		FirewallEnabled: fwenabled,
	})
	if err != nil {
		delete(p.pods, tp.fn)
		tp.createLock.Unlock()
		return err
	}

	// Block Until Triton Creates an Instance and Cache first instToPod on the TritonPod.Pod Struct
	for {
		running, err := c.Instances().Get(ctx, &compute.GetInstanceInput{ID: i.ID})
		if err != nil {
			return err
		}

		if running.State == "failed" {
			delete(p.pods, tp.fn)
			tp.createLock.Unlock()
			return errors.New("Provisioning failed")
		}

		if running.State == "running" {
			// Add the Target Address for the Probes
			if tp.probes["liveness"] != nil {
				tp.probes["liveness"].TargetIP = running.PrimaryIP
			}
			if tp.probes["readiness"] != nil {
				tp.probes["readiness"].TargetIP = running.PrimaryIP
			}
			// Convert the Inst to Podspec
			converted, err := instanceToPod(running)
			if err != nil {
				return err
			}
			p.pods[tp.fn].pod = converted
			// Run the Routines
			p.RunTritonPodLoops(tp)
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Apply Deletion Protection if Specified
	var delprotect bool
	if pod.ObjectMeta.Annotations["delprotect"] == "true" {
		delprotect = true
	}
	if pod.ObjectMeta.Annotations["delprotect"] == "false" {
		delprotect = false
	}
	if pod.ObjectMeta.Annotations["delprotect"] == "" {
		delprotect = false
	}

	if delprotect == true {
		err := c.Instances().EnableDeletionProtection(ctx, &compute.EnableDeletionProtectionInput{InstanceID: i.ID})
		if err != nil {
			// implement events
			fmt.Println("Couldn't Apply Error Protection")
		}
	}

	// Apply Firewall Rules for Ports Specified
	for _, v := range pod.Spec.Containers[0].Ports {
		if v.Name == "" {
			v.Name = "unset"
		}
		if v.Protocol == "" {
			v.Protocol = "TCP"
		}
		// Create Client and Do work.
		n, err := p.tclient.Network()
		if err != nil {
			return err
		}
		rule, err := n.Firewall().CreateRule(ctx, &network.CreateRuleInput{
			Rule:        fmt.Sprintf("FROM any TO vm %s ALLOW %s PORT %d", i.ID, strings.ToLower(string(v.Protocol)), v.ContainerPort),
			Enabled:     true,
			Description: fmt.Sprintf("Set by K8S for service: %s", string(v.Name)),
		})
		tp.fwrs = append(tp.fwrs, rule)
	}

	// If first Pod in the fwgroup, Create the fwgroup Firewall Rules
	if pod.ObjectMeta.Annotations["fwgroup"] != "" {
		fwg := pod.ObjectMeta.Annotations["fwgroup"]

		n, err := p.tclient.Network()
		if err != nil {
			return err
		}

		// Create Firewall Group if Doesn't exist
		if p.fwgs[fwg] == nil {
			p.fwgs[fwg] = p.NewTritonFWGroup()

			// Create a TCP Rule for the FWG
			rule, err := n.Firewall().CreateRule(ctx, &network.CreateRuleInput{
				Rule:        fmt.Sprintf("FROM tag k8s_" + fwg + " TO tag k8s_" + fwg + " ALLOW tcp PORT all"),
				Enabled:     true,
				Description: fmt.Sprintf("Set by K8S for Pods in the FW Zone: k8s_", fwg),
			})
			if err != nil {
				return err
			}
			p.fwgs[fwg].fwrs = append(p.fwgs[fwg].fwrs, rule)

			// Create a UDP Rule for the FWG
			rule, err = n.Firewall().CreateRule(ctx, &network.CreateRuleInput{
				Rule:        fmt.Sprintf("FROM tag k8s_" + fwg + " TO tag k8s_" + fwg + " ALLOW udp PORT all"),
				Enabled:     true,
				Description: fmt.Sprintf("Set by K8S for Pods in the FW Zone: k8s_", fwg),
			})
			if err != nil {
				return err
			}
			p.fwgs[fwg].fwrs = append(p.fwgs[fwg].fwrs, rule)
		}

		// Add Pod as Member
		p.fwgs[fwg].members = append(p.fwgs[fwg].members, tp.fn)

	}

	tp.createLock.Unlock()

	fmt.Sprintf("Created: " + i.Name)
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
	fn := p.GetPodFullName(pod.Namespace, pod.Name)

	// initial check to see if CreatePod added the TritonPod
	tp, ok := p.pods[fn]
	if !ok {
		fmt.Sprintf("The instance: %s has already been deleted,  failed to provision properly, or is unknown to Virtual Kubelet")
		return nil
	}

	// Grab a lock if so
	tp.createLock.Lock()

	// If a provision fails in CreatePod, it will remove itself from the top p.pods.  So lets check again.
	tp, ok = p.pods[fn]
	if !ok {
		fmt.Sprintf("The instance: %s has already been deleted,  failed to provision properly, or is unknown to Virtual Kubelet")
		return nil
	}

	// Acquire the ContainerID that will be used for deletions :)
	ContainerID := tp.pod.Status.ContainerStatuses[0].ContainerID

	// Create the Connection
	c, err := p.tclient.Compute()
	if err != nil {
		return err
	}

	// Shutdown the Context
	tp.shutdown()

	// Delete Instance
	err = c.Instances().Delete(ctx, &compute.DeleteInstanceInput{ID: ContainerID})
	if err != nil {
		return err
	}

	// Confirm Deletion
	for {
		_, err := c.Instances().Get(ctx, &compute.GetInstanceInput{ID: ContainerID})
		if err != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Delete FW Rules
	n, err := p.tclient.Network()
	if err != nil {
		return err
	}

	// See if we need to delete the wgroup
	fwg := tp.pod.Annotations["fwgroup"]

	if fwg != "" {
		p.fwgs[fwg].membersLock.Lock()
		for k, v := range p.fwgs[fwg].members {
			if v == tp.fn {
				p.fwgs[fwg].members[k] = p.fwgs[fwg].members[len(p.fwgs[fwg].members)-1]
				p.fwgs[fwg].members[len(p.fwgs[fwg].members)-1] = ""
				p.fwgs[fwg].members = p.fwgs[fwg].members[:len(p.fwgs[fwg].members)-1]
			}
		}
		p.fwgs[fwg].membersLock.Unlock()

		if len(p.fwgs[fwg].members) == 0 {
			for _, v := range p.fwgs[fwg].fwrs {
				n.Firewall().DeleteRule(ctx, &network.DeleteRuleInput{ID: v.ID})
			}
			delete(p.fwgs, fwg)
		}
	}

	// Iterate over TritonPod Rules and delete them
	for _, v := range tp.fwrs {
		n.Firewall().DeleteRule(ctx, &network.DeleteRuleInput{ID: v.ID})
	}

	tp.createLock.Unlock()
	delete(p.pods, fn)

	return nil
}

// GetPod retrieves a pod by name from the provider (can be cached).
func (p *TritonProvider) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	log.Printf("Received GetPod request for %s/%s.\n", namespace, name)

	c, _ := p.tclient.Compute()
	i, err := c.Instances().List(ctx, &compute.ListInstancesInput{
		Name: name,
	})
	if err != nil {
		return nil, err
	}
	if len(i) == 0 {
		return nil, nil
	}

	return p.TagToPodSpec(fmt.Sprint(i[0].Metadata["k8s_pod"])), nil
}

// GetContainerLogs retrieves the logs of a container by name from the provider.
func (p *TritonProvider) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, tail int) (string, error) {
	log.Printf("Received GetContainerLogs request for %s/%s/%s.\n", namespace, podName, containerName)
	q.Q(namespace, podName, containerName, tail)

	client := &SSH{
		Ip:   "10.1.10.112",
		User: "ubuntu",
		Port: 22,
	}
	client.Connect(4)
	client.RunCmd("journalctl -u kubelet | tail -n 20")
	client.Close()

	return "Im Logging", nil
}

// GetPodFullName retrieves the full pod name as defined in the provider context.
func (p *TritonProvider) GetPodFullName(namespace string, pod string) string {
	return fmt.Sprintf("%s-%s", namespace, pod)
}

// ExecInContainer executes a command in a container in the pod, copying data
// between in/out/err and the container's stdin/stdout/stderr.
func (p *TritonProvider) ExecInContainer(name string, uid types.UID, container string, cmd []string, in io.Reader, out, errstream io.WriteCloser, tty bool, resize <-chan remotecommand.TerminalSize, timeout time.Duration) error {
	log.Printf("Received ExecInContainer request for %s.\n", container)

	return nil
}

// GetPodStatus retrieves the status of a pod by name from the provider.
func (p *TritonProvider) GetPodStatus(ctx context.Context, namespace, name string) (*corev1.PodStatus, error) {
	log.Printf("Received GetPodStatus request for %s/%s.\n", namespace, name)

	fn := p.GetPodFullName(namespace, name)
	if p.pods[fn] == nil {
		fmt.Sprintf("Pod Missing: %s, Returning Nil.  If the Pod Exists we will catch it on the next GetPodStatus", fn)
		return nil, nil
	}

	return &p.pods[fn].pod.Status, nil
}

// GetPods retrieves a list of all pods running on the provider (can be cached).
func (p *TritonProvider) GetPods(ctx context.Context) ([]*corev1.Pod, error) {
	log.Println("Received GetPods request.")

	// Get Instances created by k8s on triton to repopulate the triton pods struct
	c, err := p.tclient.Compute()
	if err != nil {
		return nil, err
	}
	is, err := c.Instances().List(ctx, &compute.ListInstancesInput{
		Tags: map[string]interface{}{
			"k8s_nodename": p.nodeName,
		},
	})
	if err != nil {
		return nil, err
	}

	n, err := p.tclient.Network()
	if err != nil {
		return nil, err
	}

	// Create Pods Array
	pods := make([]*corev1.Pod, 0, len(is))
	for _, i := range is {

		// Repopulate all the firewall groups
		if i.Tags["k8s_fwgroup"] != nil {
			fwg := fmt.Sprint(i.Tags["k8s_fwgroup"])
			if p.fwgs[fwg] == nil {
				p.fwgs[fwg] = p.NewTritonFWGroup()
			}
			p.fwgs[fwg].members = append(p.fwgs[fwg].members, fmt.Sprintf("%s-%s", i.Tags["k8s_namespace"], i.Name))
		}

		// Convert Triton Instance to Pod
		converted, err := instanceToPod(i)
		if err != nil {
			return nil, err
		}
		// New Triton Pod
		tp, _ := p.NewTritonPod(ctx, converted)
		p.pods[tp.fn] = tp
		// Put Converted Pod Back on Struct
		p.pods[tp.fn].pod = converted
		// Create Return for GetPods
		pods = append(pods, tp.pod)

		// Repopulate instance fwrules
		rules, err := n.Firewall().ListMachineRules(ctx, &network.ListMachineRulesInput{MachineID: i.ID})
		if err != nil {
			return nil, err
		}
		for _, r := range rules {
			p.pods[tp.fn].fwrs = append(p.pods[tp.fn].fwrs, r)
		}

		// Run Loops
		p.RunTritonPodLoops(tp)
	}

	// Get FWGroup Rules on triton to repopulate the triton fwgs struct

	rules, err := n.Firewall().ListRules(ctx, &network.ListRulesInput{})
	if err != nil {
		return nil, err
	}

	for k, _ := range p.fwgs {
		for _, r := range rules {
			if strings.Contains(r.Description, "Set by K8S for Pods in the FW Zone: k8s_") {
				p.fwgs[k].fwrs = append(p.fwgs[k].fwrs, r)
			}
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
	// Get CreatePod Spec from the Metadata
	bytes := []byte(fmt.Sprint(i.Metadata["k8s_pod"]))
	var tps *corev1.Pod
	json.Unmarshal(bytes, &tps)

	// Take Care of time
	var podCreationTimestamp metav1.Time

	podCreationTimestamp = metav1.NewTime(i.Created)
	// TODO Find a way to get this
	//containerStartTime := metav1.NewTime(time.Now())

	/*
	   Triton does not share Namespaces, so init Pod Groups or Patterns which encourage this aren't implemented.   This implementation Maps 1 instance to 1 pod.
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
		Ports: tps.Spec.Containers[0].Ports,
		//EnvFrom []EnvFromSource `json:"envFrom,omitempty" protobuf:"bytes,19,rep,name=envFrom"`
		//Env []EnvVar `json:"env,omitempty" patchStrategy:"merge" patchMergeKey:"name" protobuf:"bytes,7,rep,name=env"`
		Env: tps.Spec.Containers[0].Env,
		//Resources ResourceRequirements `json:"resources,omitempty" protobuf:"bytes,8,opt,name=resources"`
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory:  *resource.NewQuantity(int64(i.Memory), resource.DecimalSI),
				corev1.ResourceStorage: *resource.NewQuantity(int64(i.Disk), resource.DecimalSI),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory:  *resource.NewQuantity(int64(i.Memory), resource.DecimalSI),
				corev1.ResourceStorage: *resource.NewQuantity(int64(i.Disk), resource.DecimalSI),
			},
		},
		//VolumeMounts []VolumeMount `json:"volumeMounts,omitempty" patchStrategy:"merge" patchMergeKey:"mountPath" protobuf:"bytes,9,rep,name=volumeMounts"`
		//VolumeDevices []VolumeDevice `json:"volumeDevices,omitempty" patchStrategy:"merge" patchMergeKey:"devicePath" protobuf:"bytes,21,rep,name=volumeDevices"`
		//LivenessProbe *Probe `json:"livenessProbe,omitempty" protobuf:"bytes,10,opt,name=livenessProbe"`
		LivenessProbe: tps.Spec.Containers[0].LivenessProbe,
		//ReadinessProbe *Probe `json:"readinessProbe,omitempty" protobuf:"bytes,11,opt,name=readinessProbe"`
		ReadinessProbe: tps.Spec.Containers[0].ReadinessProbe,
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
		State: instanceStateToContainerState(fmt.Sprint(i.State)),
		//LastTerminationState ContainerState `json:"lastState,omitempty" protobuf:"bytes,3,opt,name=lastState"`
		//Ready bool `json:"ready" protobuf:"varint,4,opt,name=ready"`
		Ready: instanceStateToPodPhase(i.State) == corev1.PodRunning,
		//RestartCount int32 `json:"restartCount" protobuf:"varint,5,opt,name=restartCount"`
		//Image string `json:"image" protobuf:"bytes,6,opt,name=image"`
		Image: i.Image,
		//ImageID string `json:"imageID" protobuf:"bytes,7,opt,name=imageID"`
		ImageID: i.Image,
		//ContainerID string `json:"containerID,omitempty" protobuf:"bytes,8,opt,name=containerID"`
		ContainerID: i.ID,
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
			Namespace:         fmt.Sprint(i.Tags["k8s_namespace"]),
			UID:               types.UID(fmt.Sprint(i.Tags["k8s_uid"])),
			CreationTimestamp: podCreationTimestamp,
			Annotations:       tps.Annotations,
		},
		Spec: corev1.PodSpec{
			NodeName:      fmt.Sprint(i.Tags["k8s_nodename"]),
			Volumes:       []corev1.Volume{},
			Containers:    containers,
			RestartPolicy: tps.Spec.RestartPolicy,
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
		return corev1.PodPending
	case "stopping":
		return corev1.PodPending
	}
	return corev1.PodUnknown
}

func instanceStateToPodConditions(state string, transitiontime metav1.Time) []corev1.PodCondition {
	switch state {
	case "running":
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

func instanceStateToContainerState(state string) corev1.ContainerState {
	startTime := metav1.NewTime(time.Now())

	// Handle the case where the container is running.
	if state == "running" {
		return corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{
				StartedAt: startTime,
			},
		}
	}

	// Handle the case where the container failed.
	if state == "failed" {
		return corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode:   0,
				Reason:     state,
				Message:    state,
				StartedAt:  startTime,
				FinishedAt: metav1.NewTime(time.Now()),
			},
		}
	}

	if state == "" {
		state = "provisioning"
	}

	// Handle the case where the container is pending.
	// Which should be all other aci states.
	return corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{
			Reason:  state,
			Message: state,
		},
	}
}

func (p *TritonProvider) TagToPodSpec(tag string) *corev1.Pod {
	bytes := []byte(tag)
	var tps *corev1.Pod
	json.Unmarshal(bytes, &tps)
	return tps
}

const (
	CERT_PASSWORD        = 1
	CERT_PUBLIC_KEY_FILE = 2
	DEFAULT_TIMEOUT      = 3 // second
	SSH_AGENT            = 4
)

type SSH struct {
	Ip      string
	User    string
	Cert    string //password or key file path
	Port    int
	session *ssh.Session
	client  *ssh.Client
}

func SSHAgent() ssh.AuthMethod {
	if sshAgent, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
		return ssh.PublicKeysCallback(agent.NewClient(sshAgent).Signers)
	}
	return nil
}

func (ssh_client *SSH) readPublicKeyFile(file string) ssh.AuthMethod {
	buffer, err := ioutil.ReadFile(file)
	if err != nil {
		return nil
	}

	key, err := ssh.ParsePrivateKey(buffer)
	if err != nil {
		return nil
	}
	return ssh.PublicKeys(key)
}

func (ssh_client *SSH) Connect(mode int) {

	var ssh_config *ssh.ClientConfig
	var auth []ssh.AuthMethod
	if mode == CERT_PASSWORD {
		auth = []ssh.AuthMethod{ssh.Password(ssh_client.Cert)}
	} else if mode == CERT_PUBLIC_KEY_FILE {
		auth = []ssh.AuthMethod{ssh_client.readPublicKeyFile(ssh_client.Cert)}
	} else if mode == SSH_AGENT {
		auth = []ssh.AuthMethod{SSHAgent()}
	} else {
		log.Println("does not support mode: ", mode)
		return
	}

	ssh_config = &ssh.ClientConfig{
		User: ssh_client.User,
		Auth: auth,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil
		},
		Timeout: time.Second * DEFAULT_TIMEOUT,
	}

	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", ssh_client.Ip, ssh_client.Port), ssh_config)
	if err != nil {
		fmt.Println(err)
		return
	}

	session, err := client.NewSession()
	if err != nil {
		fmt.Println(err)
		client.Close()
		return
	}

	ssh_client.session = session
	ssh_client.client = client
}

func SendCommand(in io.WriteCloser, cmd string) error {
	if _, err := in.Write([]byte(cmd + "\n")); err != nil {
		return err
	}

	return nil
}

func (ssh_client *SSH) RunCmd(cmd string) {
	//out, err := ssh_client.session.CombinedOutput(cmd)
	//if err != nil {
	//fmt.Println(err)
	//}
	//fmt.Println(string(out))

	stdin, err := ssh_client.session.StdinPipe()
	if err != nil {
		log.Fatal(err)
	}
	defer stdin.Close()

	stdout, err := ssh_client.session.StdoutPipe()

	err = ssh_client.session.Start(cmd)
	fmt.Println(err)

	//SendCommand(stdin, cmd)

	s := bufio.NewScanner(stdout)
	for s.Scan() {
		m := s.Text()
		q.Q(m)
	}
}

func (ssh_client *SSH) Close() {
	ssh_client.session.Close()
	ssh_client.client.Close()
}
