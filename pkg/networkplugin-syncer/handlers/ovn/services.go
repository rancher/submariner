package ovn

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog"
)

func (ovn *SyncHandler) ensureServiceLoadBalancersFrom(logicalSwitch string) error {
	lbIds, err := ovn.getK8sLoadBalancerIDsFor(logicalSwitch)
	if err != nil {
		return err
	}

	submarinerRouterId, err := ovn.getSubmarinerRouterId()
	if err != nil {
		return err
	}

	return ovn.nbctl.LrSetLoadBalancers(submarinerRouterId, lbIds)
}

func (ovn *SyncHandler) getSubmarinerRouterId() (string, error) {
	submarinerRouter, err := ovn.nbdb.LRGet(submarinerLogicalRouter)

	if err != nil {
		return "", errors.Wrapf(err, "error getting %q router ID", submarinerLogicalRouter)
	}

	if len(submarinerRouter) != 1 {
		return "", fmt.Errorf("Only one %q router expected, but found %#v", submarinerLogicalRouter, submarinerRouter)
	}

	return submarinerRouter[0].UUID, nil
}

func (ovn *SyncHandler) getK8sLoadBalancerIDsFor(logicalSwitch string) ([]string, error) {
	loadBalancers, err := ovn.nbdb.LSLBList(logicalSwitch)

	if err != nil {
		return nil, errors.Wrapf(err, "error listing load balancers for logical switch %q", logicalSwitch)
	}

	var lbIds []string

	for _, lb := range loadBalancers {
		for key := range lb.ExternalID {
			externalId, ok := key.(string)
			if !ok {
				klog.Warningf("Unable to extract key from load-balancer %q external ids %#v", lb.UUID, key)
				continue
			}

			if strings.Contains(externalId, "k8s-cluster-lb") {
				lbIds = append(lbIds, lb.UUID)
			}
		}
	}

	return lbIds, nil
}
