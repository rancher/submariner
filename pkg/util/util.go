/*
© 2021 Red Hat, Inc. and others

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
package util

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/rdegges/go-ipify"
	level "github.com/submariner-io/admiral/pkg/log"
	subv1 "github.com/submariner-io/submariner/pkg/apis/submariner.io/v1"
	"github.com/submariner-io/submariner/pkg/iptables"
	"github.com/submariner-io/submariner/pkg/types"
	"github.com/vishvananda/netlink"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/klog"
)

const tokenLength = 64

func getAPIIdentifier(token string) (string, error) {
	if len(token) != tokenLength {
		return "", fmt.Errorf("token %s length was not %d", token, tokenLength)
	}

	clusterID := token[0 : tokenLength/2]

	return clusterID, nil
}

func getConnectSecret(token string) (string, error) {
	if len(token) != tokenLength {
		return "", fmt.Errorf("token %s length was not %d", token, tokenLength)
	}

	connectSecret := token[tokenLength/2 : tokenLength]

	return connectSecret, nil
}

func ParseSecure(token string) (types.Secure, error) {
	secretKey, err := getConnectSecret(token)
	if err != nil {
		return types.Secure{}, err
	}

	apiKey, err := getAPIIdentifier(token)
	if err != nil {
		return types.Secure{}, err
	}

	return types.Secure{
		APIKey:    apiKey,
		SecretKey: secretKey,
	}, nil
}

func GetLocalIP() string {
	return GetLocalIPForDestination("8.8.8.8")
}

func GetLocalIPForDestination(dst string) string {
	conn, err := net.Dial("udp", dst+":53")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String()
}

func FlattenColors(colorCodes []string) string {
	if len(colorCodes) == 0 {
		return ""
	}

	flattenedColors := colorCodes[0]

	for k, v := range colorCodes {
		if k != 0 {
			flattenedColors = flattenedColors + "," + v
		}
	}

	return flattenedColors
}

func GetLocalEndpoint(submSpec types.SubmarinerSpecification, backendConfig map[string]string,
	privateIP string) (types.SubmarinerEndpoint, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return types.SubmarinerEndpoint{}, fmt.Errorf("error getting hostname: %v", err)
	}

	if backendConfig == nil {
		backendConfig = make(map[string]string)
	}

	var localSubnets []string

	globalnetEnabled := false

	if len(submSpec.GlobalCidr) > 0 {
		localSubnets = submSpec.GlobalCidr
		globalnetEnabled = true
	} else {
		localSubnets = append(localSubnets, submSpec.ServiceCidr...)
		localSubnets = append(localSubnets, submSpec.ClusterCidr...)
	}

	endpoint := types.SubmarinerEndpoint{
		Spec: subv1.EndpointSpec{
			CableName:        fmt.Sprintf("submariner-cable-%s-%s", submSpec.ClusterID, strings.ReplaceAll(privateIP, ".", "-")),
			ClusterID:        submSpec.ClusterID,
			Hostname:         hostname,
			PrivateIP:        privateIP,
			NATEnabled:       submSpec.NatEnabled,
			Subnets:          localSubnets,
			Backend:          submSpec.CableDriver,
			BackendConfig:    backendConfig,
			NATDiscoveryPort: nil,
			//TODO: NATDiscoveryPort is disabled by default as it doesn't work inside kind e2e yet, which then relies
			//      on the timeout. In the following patches we will allow setting the port number via a label in the node
		},
	}

	publicIP, err := ipify.GetIp()
	if err != nil {
		return types.SubmarinerEndpoint{}, fmt.Errorf("could not determine public IP: %v", err)
	}

	endpoint.Spec.PublicIP = publicIP

	if !globalnetEnabled {
		// When globalnet is enabled, HealthCheckIP will be the globalIP assigned to the Active GatewayNode.
		// In a fresh deployment, globalIP annotation for the node might take few seconds. So we listen on NodeEvents
		// and update the endpoint HealthCheckIP (to globalIP) in datastoreSyncer at a later stage. This will trigger
		// the HealthCheck between the clusters.
		endpoint.Spec.HealthCheckIP, err = getCNIInterfaceIPAddress(submSpec.ClusterCidr)
		if err != nil {
			return types.SubmarinerEndpoint{}, fmt.Errorf("error getting CNI Interface IP address: %v", err)
		}
	}

	return endpoint, nil
}

//TODO: to handle de-duplication of code/finding common parts with the route agent
func getCNIInterfaceIPAddress(clusterCIDRs []string) (string, error) {
	for _, clusterCIDR := range clusterCIDRs {
		_, clusterNetwork, err := net.ParseCIDR(clusterCIDR)
		if err != nil {
			return "", fmt.Errorf("unable to ParseCIDR %q : %v", clusterCIDR, err)
		}

		hostInterfaces, err := net.Interfaces()
		if err != nil {
			return "", fmt.Errorf("net.Interfaces() returned error : %v", err)
		}

		for _, iface := range hostInterfaces {
			addrs, err := iface.Addrs()
			if err != nil {
				return "", fmt.Errorf("for interface %q, iface.Addrs returned error: %v", iface.Name, err)
			}

			for i := range addrs {
				ipAddr, _, err := net.ParseCIDR(addrs[i].String())
				if err != nil {
					klog.Errorf("Unable to ParseCIDR : %q", addrs[i].String())
				} else if ipAddr.To4() != nil {
					klog.V(level.DEBUG).Infof("Interface %q has %q address", iface.Name, ipAddr)
					address := net.ParseIP(ipAddr.String())

					// Verify that interface has an address from cluster CIDR
					if clusterNetwork.Contains(address) {
						klog.V(level.DEBUG).Infof("Found CNI Interface %q that has IP %q from ClusterCIDR %q",
							iface.Name, ipAddr.String(), clusterCIDR)
						return ipAddr.String(), nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("unable to find CNI Interface on the host which has IP from %q", clusterCIDRs)
}

func GetClusterIDFromCableName(cableName string) string {
	// length is 11
	// 0           1    2   3    4    5       6   7  8 9  10
	// submariner-cable-my-super-long_cluster-id-172-16-32-5
	cableSplit := strings.Split(cableName, "-")
	clusterID := cableSplit[2]

	for i := 3; i < len(cableSplit)-4; i++ {
		clusterID = clusterID + "-" + cableSplit[i]
	}

	return clusterID
}

func GetEndpointCRDName(endpoint *types.SubmarinerEndpoint) (string, error) {
	return GetEndpointCRDNameFromParams(endpoint.Spec.ClusterID, endpoint.Spec.CableName)
}

func GetEndpointCRDNameFromParams(clusterID, cableName string) (string, error) {
	if clusterID == "" || cableName == "" {
		return "", fmt.Errorf("error, cluster ID or cable name was empty")
	}

	return fmt.Sprintf("%s-%s", clusterID, cableName), nil
}

func GetClusterCRDName(cluster *types.SubmarinerCluster) (string, error) {
	if cluster.Spec.ClusterID == "" {
		return "", fmt.Errorf("ClusterID was empty")
	}

	return cluster.Spec.ClusterID, nil
}

func CompareEndpointSpec(left, right subv1.EndpointSpec) bool {
	// maybe we have to use just reflect.DeepEqual(left, right), but in this case the subnets order will influence.
	return left.ClusterID == right.ClusterID && left.CableName == right.CableName && left.Hostname == right.Hostname &&
		left.Backend == right.Backend && equality.Semantic.DeepEqual(left.BackendConfig, right.BackendConfig)
}

func GetDefaultGatewayInterface() (*net.Interface, error) {
	routes, err := netlink.RouteList(nil, syscall.AF_INET)
	if err != nil {
		return nil, err
	}

	for _, route := range routes {
		if route.Dst == nil || route.Dst.String() == "0.0.0.0/0" {
			if route.LinkIndex == 0 {
				return nil, fmt.Errorf("default gateway interface could not be determined")
			}

			iface, err := net.InterfaceByIndex(route.LinkIndex)
			if err != nil {
				return nil, err
			}

			return iface, nil
		}
	}

	return nil, fmt.Errorf("unable to find default route")
}

func CreateChainIfNotExists(ipt iptables.Interface, table, chain string) error {
	existingChains, err := ipt.ListChains(table)
	if err != nil {
		return err
	}

	for _, val := range existingChains {
		if val == chain {
			// Chain already exists
			return nil
		}
	}

	return ipt.NewChain(table, chain)
}

func PrependUnique(ipt iptables.Interface, table, chain string, ruleSpec []string) error {
	rules, err := ipt.List(table, chain)
	if err != nil {
		return fmt.Errorf("error listing the rules in %s chain: %v", chain, err)
	}

	// Submariner requires certain iptable rules to be programmed at the beginning of an iptables Chain
	// so that we can preserve the sourceIP for inter-cluster traffic and avoid K8s SDN making changes
	// to the traffic.
	// In this API, we check if the required iptable rule is present at the beginning of the chain.
	// If the rule is already present and there are no stale[1] flows, we simply return. If not, we create one.
	// [1] Sometimes after we program the rule at the beginning of the chain, K8s SDN might insert some
	// new rules ahead of the rule that we programmed. In such cases, the rule that we programmed will
	// not be the first rule to hit and Submariner behavior might get affected. So, we query the rules
	// in the chain to see if the rule slipped its position, and if so, delete all such occurrences.
	// We then re-program a new rule at the beginning of the chain as required.

	isPresentAtRequiredPosition := false
	numOccurrences := 0
	for index, rule := range rules {
		if strings.Contains(rule, strings.Join(ruleSpec, " ")) {
			klog.V(level.DEBUG).Infof("In %s table, iptables rule \"%s\", exists at index %d.", table, strings.Join(ruleSpec, " "), index)
			numOccurrences++

			if index == 1 {
				isPresentAtRequiredPosition = true
			}
		}
	}

	// The required rule is present in the Chain, but either there are multiple occurrences or its
	// not at the desired location
	if numOccurrences > 1 || !isPresentAtRequiredPosition {
		for i := 0; i < numOccurrences; i++ {
			if err = ipt.Delete(table, chain, ruleSpec...); err != nil {
				return fmt.Errorf("error deleting stale iptable rule \"%s\": %v", strings.Join(ruleSpec, " "), err)
			}
		}
	}

	// The required rule is present only once and is at the desired location
	if numOccurrences == 1 && isPresentAtRequiredPosition {
		klog.V(level.DEBUG).Infof("In %s table, iptables rule \"%s\", already exists.", table, strings.Join(ruleSpec, " "))
		return nil
	} else if err := ipt.Insert(table, chain, 1, ruleSpec...); err != nil {
		return err
	}

	return nil
}
