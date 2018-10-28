package triton

import (
	"fmt"
	"log"

	"github.com/virtual-kubelet/virtual-kubelet/manager"
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

// Capacity represents the provisioned capacity on a Fargate cluster.
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
