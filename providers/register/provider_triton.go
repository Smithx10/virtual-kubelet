// +build !no_triton_provider

package register

import (
	"github.com/virtual-kubelet/virtual-kubelet/providers"
	"github.com/virtual-kubelet/virtual-kubelet/providers/triton"
)

func init() {
	register("triton", iniTriton)
}

func iniTriton(cfg InitConfig) (providers.Provider, error) {
	return triton.NewTritonProvider(cfg.ConfigPath, cfg.ResourceManager, cfg.NodeName, cfg.OperatingSystem, cfg.InternalIP, cfg.DaemonPort)
}
