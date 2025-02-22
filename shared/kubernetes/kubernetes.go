// SPDX-FileCopyrightText: 2024 SUSE LLC
//
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	. "github.com/uyuni-project/uyuni-tools/shared/l10n"
	"github.com/uyuni-project/uyuni-tools/shared/utils"
)

// ClusterInfos represent cluster information.
type ClusterInfos struct {
	KubeletVersion string
	Ingress        string
}

// IsK3s is true if it's a K3s Cluster.
func (infos ClusterInfos) IsK3s() bool {
	return strings.Contains(infos.KubeletVersion, "k3s")
}

// IsRKE2 is true if it's a RKE2 Cluster.
func (infos ClusterInfos) IsRke2() bool {
	return strings.Contains(infos.KubeletVersion, "rke2")
}

// GetKubeconfig returns the path to the default kubeconfig file or "" if none.
func (infos ClusterInfos) GetKubeconfig() string {
	var kubeconfig string
	if infos.IsK3s() {
		// If the user didn't provide a KUBECONFIG value or file, use the k3s default
		kubeconfigPath := os.ExpandEnv("${HOME}/.kube/config")
		if os.Getenv("KUBECONFIG") == "" || !utils.FileExists(kubeconfigPath) {
			kubeconfig = "/etc/rancher/k3s/k3s.yaml"
		}
	}
	// Since even kubectl doesn't work without a trick on rke2, we assume the user has set kubeconfig
	return kubeconfig
}

// CheckCluster return cluster information.
func CheckCluster() (*ClusterInfos, error) {
	// Get the kubelet version
	out, err := utils.RunCmdOutput(zerolog.DebugLevel, "kubectl", "get", "node",
		"-o", "jsonpath={.items[0].status.nodeInfo.kubeletVersion}")
	if err != nil {
		return nil, fmt.Errorf(L("failed to get kubelet version: %s"), err)
	}

	var infos ClusterInfos
	infos.KubeletVersion = string(out)
	infos.Ingress, err = guessIngress()
	if err != nil {
		return nil, err
	}

	return &infos, nil
}

func guessIngress() (string, error) {
	// Check for a traefik resource
	err := utils.RunCmd("kubectl", "explain", "ingressroutetcp")
	if err == nil {
		return "traefik", nil
	} else {
		log.Debug().Err(err).Msg("No ingressroutetcp resource deployed")
	}

	// Look for a pod running the nginx-ingress-controller: there is no other common way to find out
	out, err := utils.RunCmdOutput(zerolog.DebugLevel, "kubectl", "get", "pod", "-A",
		"-o", "jsonpath={range .items[*]}{.spec.containers[*].args[0]}{.spec.containers[*].command}{end}")
	if err != nil {
		return "", fmt.Errorf(L("failed to get pod commands to look for nginx controller: %s"), err)
	}

	const nginxController = "/nginx-ingress-controller"
	if strings.Contains(string(out), nginxController) {
		return "nginx", nil
	}

	return "", nil
}

// Restart restarts the pod.
func Restart(filter string) error {
	if err := Stop(filter); err != nil {
		return fmt.Errorf(L("cannot stop %s: %s"), filter, err)
	}
	return Start(filter)
}

// Start starts the pod.
func Start(filter string) error {
	// if something is running, we don't need to set replicas to 1
	if _, err := GetNode(filter); err != nil {
		return ReplicasTo(filter, 1)
	}
	log.Debug().Msgf("Already running")
	return nil
}

// Stop stop the pod.
func Stop(filter string) error {
	return ReplicasTo(filter, 0)
}

func get(component string, componentName string, args ...string) ([]byte, error) {
	kubectlArgs := []string{
		"get",
		component,
		componentName,
	}

	kubectlArgs = append(kubectlArgs, args...)

	output, err := utils.RunCmdOutput(zerolog.DebugLevel, "kubectl", kubectlArgs...)
	if err != nil {
		return []byte{}, err
	}
	return output, nil
}

// GetConfigMap returns the value of a given config map.
func GetConfigMap(configMapName string, filter string) (string, error) {
	out, err := get("configMap", configMapName, filter)
	if err != nil {
		return "", fmt.Errorf(L("failed to kubectl get configMap %s %s")+": %s", configMapName, filter, err)
	}

	return string(out), nil
}

// GetSecret returns the value of a given secret.
func GetSecret(secretName string, filter string) (string, error) {
	out, err := get("secret", secretName, filter)
	if err != nil {
		return "", fmt.Errorf(L("failed to kubectl get secret %s %s")+": %s", secretName, filter, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(out))
	if err != nil {
		return "", fmt.Errorf(L("Failed to base64 decode configMap %s: %s"), secretName, err)
	}

	return string(decoded), nil
}
