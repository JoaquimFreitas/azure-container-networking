// Copyright 2017 Microsoft. All rights reserved.
// MIT License

package networkcontainers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/types"
	"io/ioutil"
	"os"
	"os/exec"

	"github.com/Azure/azure-container-networking/cns"
	"github.com/Azure/azure-container-networking/log"
	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/invoke"
)

const (
	VersionStr string = "cniVersion"
	PluginsStr string = "plugins"
	NameStr    string = "name"
)

func createOrUpdateInterface(createNetworkContainerRequest cns.CreateNetworkContainerRequest) error {

	if createNetworkContainerRequest.NetworkContainerType == cns.WebApps {
		log.Printf("[Azure CNS] Operation not supported for WebApps Orchestrator.")
		return nil
	}

	exists, _ := interfaceExists(createNetworkContainerRequest.NetworkContainerid)
	if !exists {
		log.Printf("[Azure CNS] Only Update Operation is supported.")
		return nil
	}

	return createOrUpdateWithOperation(createNetworkContainerRequest, "UPDATE")
}

func setWeakHostOnInterface(ipAddress string) error {
	return nil
}

func createOrUpdateWithOperation(createNetworkContainerRequest cns.CreateNetworkContainerRequest, operation string) error {
	log.Printf("[Azure CNS] createOrUpdateWithOperation called with operation type %v", operation)

	if _, err := os.Stat("/opt/cni/bin/azure-vnet"); err != nil {
		if os.IsNotExist(err) {
			return errors.New("[Azure CNS] Unable to find azure-vnet under /opt/cni/bin/. Cannot continue")
		}
	}

	if createNetworkContainerRequest.IPConfiguration.IPSubnet.IPAddress == "" {
		return errors.New("[Azure CNS] IPAddress in IPConfiguration of createNetworkContainerRequest is nil")
	}

	var podInfo cns.KubernetesPodInfo
	err := json.Unmarshal(createNetworkContainerRequest.OrchestratorContext, &podInfo)
	if err != nil {
		log.Printf("[Azure CNS] Unmarshalling %s failed with error %v", createNetworkContainerRequest.NetworkContainerType, err)
		return err
	}

	log.Printf("[Azure CNS] Pod info %v", podInfo)

	// How to construct net namespace and container Id?
	rt, err := buildCNIRuntimeConf(podInfo.PodName, podInfo.PodNamespace, "", "", createNetworkContainerRequest.NetworkContainerid)
	if err != nil {
		log.Printf("[Azure CNS] Failed to build runtime configuration with error %v", err)
		return err
	}

	log.Printf("[Azure CNS] run time conf info %v", rt)

	// Hardcoded path ?
	netConf, err := getNetworkConf("/etc/cni/net.d/10-azure.conflist")
	if err != nil {
		log.Printf("[Azure CNS] Failed to build network configuration with error %v", err)
		return err
	}

	log.Printf("[Azure CNS] network configuration info %v", string(netConf))

	err = updateNetwork(rt, netConf)
	if err != nil {
		log.Printf("[Azure CNS] Failed to update network with error %v", err)
		return err
	}

	return nil
}

func deleteInterface(networkContainerID string) error {
	return nil
}

func buildCNIRuntimeConf(podName string, podNs string, podSandboxId string, podNetnsPath string, interfaceName string) (*libcni.RuntimeConf, error) {
	rt := &libcni.RuntimeConf{
		ContainerID: podSandboxId, // how to get this
		NetNS:       podNetnsPath, // how to retireve this
		IfName:      interfaceName,
		Args: [][2]string{
			{"K8S_POD_NAMESPACE", podNs},
			{"K8S_POD_NAME", podName},
		},
	}

	return rt, nil
}

func updateNetwork(rt *libcni.RuntimeConf, netconf []byte) error {
	environ := args("UPDATE", rt).AsEnv()

	log.Printf("[Azure CNS] CNI called with environ variables %v", environ)

	stdout := &bytes.Buffer{}
	c := exec.Command("/opt/cni/bin/azure-vnet")
	c.Env = environ
	c.Stdin = bytes.NewBuffer(netconf)
	c.Stdout = stdout
	c.Stderr = os.Stderr
	err := c.Run()
	return pluginErr(err, stdout.Bytes())
}

// Environment variables
func args(action string, rt *libcni.RuntimeConf) *invoke.Args {
	return &invoke.Args{
		Command:     action,
		ContainerID: rt.ContainerID,
		NetNS:       rt.NetNS,
		PluginArgs:  rt.Args,
		IfName:      rt.IfName,
		Path:        "/opt/cni/bin",
	}
}

// This function gets the flatened network configuration (compliant with azure cni) in bytes array format
func getNetworkConf(configFilePath string) ([]byte, error) {
	content, err := ioutil.ReadFile(configFilePath)
	if err != nil {
		return nil, err
	}

	var configMap map[string]interface{}
	err = json.Unmarshal(content, &configMap)
	if err != nil {
		return nil, err
	}

	// Get the plugins section
	pluginsSection := configMap[PluginsStr].([]interface{})
	flatNetConfigMap := pluginsSection[0].(map[string]interface{})

	// insert version and name fields
	flatNetConfigMap[VersionStr] = configMap[VersionStr].(string)
	flatNetConfigMap[NameStr] = configMap[NameStr].(string)

	// convert into bytes format
	netConfig, err := json.Marshal(flatNetConfigMap)
	if err != nil {
		return nil, err
	}

	return netConfig, nil
}

func pluginErr(err error, output []byte) error {
	if _, ok := err.(*exec.ExitError); ok {
		emsg := types.Error{}
		if perr := json.Unmarshal(output, &emsg); perr != nil {
			emsg.Msg = fmt.Sprintf("netplugin failed but error parsing its diagnostic message %q: %v", string(output), perr)
		}
		return &emsg
	}

	return err
}
