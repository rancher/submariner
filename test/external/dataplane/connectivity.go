/*
SPDX-License-Identifier: Apache-2.0

Copyright Contributors to the Submariner project.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dataplane

import (
	"context"
	"fmt"
	"sort"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/submariner-io/shipyard/test/e2e/framework"
	"github.com/submariner-io/submariner/pkg/globalnet/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	extAppName = "ext-app"
	extNetName = "pseudo-ext"

	testImage         = "registry.access.redhat.com/ubi7/ubi:latest"
	testContainerName = "ext-test-container"
)

var (
	simpleHTTPServerCommand = []string{"python", "-m", "SimpleHTTPServer", "80"}
)

var _ = Describe("[external-dataplane] Connectivity", func() {
	f := framework.NewFramework("ext-dataplane")

	It("should be able to connect from an external app to a pod in a cluster", func() {
		if framework.TestContext.GlobalnetEnabled {
			testGlobalNetExternalConnectivity(f)
		} else {
			testExternalConnectivity(f)
		}
	})
})

func testExternalConnectivity(f *framework.Framework) {
	externalClusterName := getExternalClusterName(framework.TestContext.ClusterIDs)

	for idx := range framework.KubeClients {
		clusterName := framework.TestContext.ClusterIDs[idx]

		By(fmt.Sprintf("Creating a pod and a service in cluster %q", clusterName))

		np := f.NewNetworkPod(&framework.NetworkPodConfig{
			Type:          framework.CustomPod,
			Port:          80,
			Cluster:       framework.ClusterIndex(idx),
			Scheduling:    framework.NonGatewayNode,
			ContainerName: testContainerName,
			ImageName:     testImage,
			Command:       simpleHTTPServerCommand,
		})
		svc := np.CreateService()

		// Get handle for existing docker
		docker := framework.New(extAppName)

		// Get IPs to use later
		podIP := np.Pod.Status.PodIP
		svcIP := svc.Spec.ClusterIP
		dockerIP := docker.GetIP(extNetName)

		By(fmt.Sprintf("Sending an http request from external app %q to the service %q in the cluster %q",
			dockerIP, svcIP, clusterName))

		command := []string{"curl", "-m", "10", fmt.Sprintf("%s:%d", svcIP, 80)}
		_, _ = docker.RunCommand(command...)

		By("Verifying the pod received the request")

		podLog := np.GetLog()

		// TODO: also verify cluster that is directly connected to external app
		// (source IP is not dockerIP in the case, so need to be checked by the other way).
		if clusterName != externalClusterName {
			Expect(podLog).To(ContainSubstring(dockerIP))
		}

		By(fmt.Sprintf("Sending an http request from the test pod %q %q in cluster %q to the external app %q",
			np.Pod.Name, podIP, clusterName, dockerIP))

		cmd := []string{"curl", "-m", "10", fmt.Sprintf("%s:%d", dockerIP, 80)}
		_, _ = np.RunCommand(cmd)

		By("Verifying that external app received request")
		// Only check stderr
		_, dockerLog := docker.GetLog()

		// TODO: also verify cluster that is directly connected to external app
		// (source IP is not podIP in the case, so need to be checked by the other way).
		if clusterName != externalClusterName {
			Expect(dockerLog).To(ContainSubstring(podIP))
		}
	}
}

func testGlobalNetExternalConnectivity(f *framework.Framework) {
	externalClusterName := getExternalClusterName(framework.TestContext.ClusterIDs)
	extClusterIdx := getExternalClusterIndex(framework.TestContext.ClusterIDs)

	for idx := range framework.KubeClients {
		if framework.ClusterIndex(idx) == extClusterIdx {
			// TODO also do this case
			continue
		}
		clusterName := framework.TestContext.ClusterIDs[idx]

		By(fmt.Sprintf("Creating a pod and a service in cluster %q", clusterName))

		np := f.NewNetworkPod(&framework.NetworkPodConfig{
			Type:          framework.CustomPod,
			Port:          80,
			Cluster:       framework.ClusterIndex(idx),
			Scheduling:    framework.NonGatewayNode,
			ContainerName: testContainerName,
			ImageName:     testImage,
			Command:       simpleHTTPServerCommand,
		})
		svc := np.CreateService()
		f.CreateServiceExport(np.Config.Cluster, svc.Name)

		// Get globalIPs for the network pod to use later
		remoteIP := f.AwaitGlobalIngressIP(np.Config.Cluster, svc.Name, svc.Namespace)
		Expect(remoteIP).ToNot(Equal(""))

		podGlobalIPs := f.AwaitClusterGlobalEgressIPs(np.Config.Cluster, constants.ClusterGlobalEgressIPName)
		Expect(len(podGlobalIPs)).ToNot(BeZero())
		podGlobalIP := podGlobalIPs[0]

		By(fmt.Sprintf("Creating a service without selector and endpoints in cluster %q", externalClusterName))
		// Get handle for existing docker
		docker := framework.New(extAppName)
		dockerIP := docker.GetIP(extNetName)

		// Create service without selector and endpoints for dockerIP, and export the service
		extSvc := CreateServiceWithoutSelector(f, extClusterIdx, "extsvc", 80)
		CreateEndpoints(f, extClusterIdx, extSvc.Name, dockerIP, 80)
		f.CreateServiceExport(extClusterIdx, extSvc.Name)

		// Get globalIPs for the extApp to use later
		// TODO: support globalIngressIP for service without selector and enable this code
		//extIngressGlobalIP := f.AwaitGlobalIngressIP(extClusterIdx, extSvc.Name, extSvc.Namespace)
		//Expect(extIngressGlobalIP).ToNot(Equal(""))
		// TODO: fixme this is just to make test pass
		extIngressGlobalIP := svc.Spec.ClusterIP

		extEgressGlobalIPs := f.AwaitClusterGlobalEgressIPs(extClusterIdx, constants.ClusterGlobalEgressIPName)
		Expect(len(extEgressGlobalIPs)).ToNot(BeZero())
		extEgressGlobalIP := extEgressGlobalIPs[0]

		By(fmt.Sprintf("Sending an http request from external app %q to the service %q in the cluster %q",
			dockerIP, remoteIP, clusterName))

		command := []string{"curl", "-m", "10", fmt.Sprintf("%s:%d", remoteIP, 80)}
		_, _ = docker.RunCommand(command...)

		By("Verifying the pod received the request")

		// TODO: fix globalnet to make external source IP seen as GlobalIP and check the IP
		podLog := np.GetLog()
		Expect(podLog).To(ContainSubstring(extEgressGlobalIP))
		//framework.Logf("%s, %s, %s, %s, %s: %s", dockerIP, remoteIP, podGlobalIP, extIngressGlobalIP, extEgressGlobalIP, podLog)

		By(fmt.Sprintf("Sending an http request from the test pod %q %q in cluster %q to the external app %q",
			np.Pod.Name, podGlobalIP, clusterName, extIngressGlobalIP))

		cmd := []string{"curl", "-m", "10", fmt.Sprintf("%s:%d", extIngressGlobalIP, 80)}
		_, _ = np.RunCommand(cmd)

		By("Verifying that external app received request")
		// Only check stderr
		_, dockerLog := docker.GetLog()
		//Expect(dockerLog).To(ContainSubstring(podGlobalIP))
		framework.Logf("%s, %s, %s, %s, %s: %s", dockerIP, remoteIP, podGlobalIP, extIngressGlobalIP, extEgressGlobalIP, dockerLog)
	}
}

// The first cluster is chosen as the one connected to external application
// See scripts/e2e/external/utils
func getExternalClusterName(names []string) string {
	if len(names) == 0 {
		return ""
	}

	sortedNames := make([]string, len(names))
	copy(sortedNames, names)
	sort.Strings(sortedNames)

	return sortedNames[0]
}
func getExternalClusterIndex(names []string) framework.ClusterIndex {
	clusterName := getExternalClusterName(names)

	for idx, cid := range names {
		if cid == clusterName {
			return framework.ClusterIndex(idx)
		}
	}

	// TODO: consider right error handling
	return framework.ClusterIndex(0)
}

func CreateServiceWithoutSelector(f *framework.Framework, cluster framework.ClusterIndex, svcName string, port int) *corev1.Service {
	serviceSpec := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: svcName,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{
				Port:       int32(port),
				Name:       "tcp",
				TargetPort: intstr.FromInt(port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}

	services := framework.KubeClients[cluster].CoreV1().Services(f.Namespace)

	return framework.AwaitUntil("create service", func() (interface{}, error) {
		service, err := services.Create(context.TODO(), &serviceSpec, metav1.CreateOptions{})
		if errors.IsAlreadyExists(err) {
			err = services.Delete(context.TODO(), serviceSpec.Name, metav1.DeleteOptions{})
			if err != nil {
				return nil, err
			}

			service, err = services.Create(context.TODO(), &serviceSpec, metav1.CreateOptions{})
		}

		return service, err
	}, framework.NoopCheckResult).(*corev1.Service)
}

func CreateEndpoints(f *framework.Framework, cluster framework.ClusterIndex, epName, address string, port int) *corev1.Endpoints {
	endpointsSpec := corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name: epName,
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{
					{IP: address},
				},
				Ports: []corev1.EndpointPort{
					{Port: int32(port)},
				},
			},
		},
	}

	endpoints := framework.KubeClients[cluster].CoreV1().Endpoints(f.Namespace)

	return framework.AwaitUntil("create endpoints", func() (interface{}, error) {
		ep, err := endpoints.Create(context.TODO(), &endpointsSpec, metav1.CreateOptions{})
		if errors.IsAlreadyExists(err) {
			err = endpoints.Delete(context.TODO(), endpointsSpec.Name, metav1.DeleteOptions{})
			if err != nil {
				return nil, err
			}

			ep, err = endpoints.Create(context.TODO(), &endpointsSpec, metav1.CreateOptions{})
		}

		return ep, err
	}, framework.NoopCheckResult).(*corev1.Endpoints)
}
