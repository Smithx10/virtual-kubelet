package triton

import (
	"fmt"
	"io"
	"os"

	"github.com/virtual-kubelet/virtual-kubelet/providers"

	"github.com/BurntSushi/toml"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	// Provider configuration defaults.
	defaultPlatformVersion = "LATEST"
	defaultOperatingSystem = providers.OperatingSystemLinux

	defaultNodeName = "triton-vkubelet"

	// Default resource capacity advertised by Triton provider.
	// These are intentionally low to prevent any accidental overuse.
	defaultCPUCapacity     = "20"
	defaultMemoryCapacity  = "40Gi"
	defaultStorageCapacity = "40Gi"
	defaultPodCapacity     = "20"

	// Minimum resource capacity advertised by Triton provider.
	// These values correspond to the minimum Triton task size.
	minCPUCapacity    = "250m"
	minMemoryCapacity = "512Mi"
	minPodCapacity    = "1"
)

// ProviderConfig represents the contents of the provider configuration file.
type providerConfig struct {
	PlatformVersion string
	OperatingSystem string
	CPU             string
	Memory          string
	Storage         string
	Pods            string
}

// loadConfigFile loads the given Triton provider configuration file.
func (p *TritonProvider) loadConfigFile(filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	err = p.loadConfig(f)
	return err
}

// loadConfig loads the given Triton provider TOML configuration stream.
func (p *TritonProvider) loadConfig(r io.Reader) error {
	var config providerConfig
	var q resource.Quantity

	// Set defaults for optional fields.
	config.PlatformVersion = defaultPlatformVersion
	config.NodeName = defaultNodeName
	config.OperatingSystem = defaultOperatingSystem
	config.CPU = defaultCPUCapacity
	config.Memory = defaultMemoryCapacity
	config.Storage = defaultStorageCapacity
	config.Pods = defaultPodCapacity

	// Read the user-supplied configuration.
	_, err := toml.DecodeReader(r, &config)
	if err != nil {
		return err
	}

	if config.OperatingSystem != providers.OperatingSystemLinux {
		return fmt.Errorf("Fargate does not support operating system %v", config.OperatingSystem)
	}

	// Validate advertised capacity.
	if q, err = resource.ParseQuantity(config.CPU); err != nil {
		return fmt.Errorf("Invalid CPU value %v", config.CPU)
	}
	if q.Cmp(resource.MustParse(minCPUCapacity)) == -1 {
		return fmt.Errorf("CPU value %v is less than the minimum %v", config.CPU, minCPUCapacity)
	}
	if q, err = resource.ParseQuantity(config.Memory); err != nil {
		return fmt.Errorf("Invalid memory value %v", config.Memory)
	}
	if q.Cmp(resource.MustParse(minMemoryCapacity)) == -1 {
		return fmt.Errorf("Memory value %v is less than the minimum %v", config.Memory, minMemoryCapacity)
	}
	if q, err = resource.ParseQuantity(config.Storage); err != nil {
		return fmt.Errorf("Invalid storage value %v", config.Storage)
	}
	if q, err = resource.ParseQuantity(config.Pods); err != nil {
		return fmt.Errorf("Invalid pods value %v", config.Pods)
	}
	if q.Cmp(resource.MustParse(minPodCapacity)) == -1 {
		return fmt.Errorf("Pod value %v is less than the minimum %v", config.Pods, minPodCapacity)
	}

	p.platformVersion = config.PlatformVersion
	p.operatingSystem = config.OperatingSystem
	p.nodeName = config.NodeName
	p.capacity.cpu = config.CPU
	p.capacity.memory = config.Memory
	p.capacity.storage = config.Storage
	p.capacity.pods = config.Pods

	return nil
}
