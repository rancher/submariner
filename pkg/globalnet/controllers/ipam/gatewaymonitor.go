package ipam

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/coreos/go-iptables/iptables"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	v1 "github.com/submariner-io/submariner/pkg/apis/submariner.io/v1"
	submarinerClientset "github.com/submariner-io/submariner/pkg/client/clientset/versioned"
	submarinerInformers "github.com/submariner-io/submariner/pkg/client/informers/externalversions"
)

const defaultResync = 60 * time.Second

func NewGatewayMonitor(spec *SubmarinerIpamControllerSpecification, cfg *rest.Config, stopCh <-chan struct{}) (*GatewayMonitor, error) {
	gatewayMonitor := &GatewayMonitor{
		clusterID:         spec.ClusterID,
		endpointWorkqueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Endpoints"),
		ipamSpec:          spec,
		stopProcessing:    nil,
		isGatewayNode:     false,
		syncMutex:         &sync.Mutex{},
	}

	clientSet, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("error building k8s clientset: %s", err.Error())
	}

	submarinerClient, err := submarinerClientset.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("error building submariner clientset: %s", err.Error())
	}

	submarinerInformerFactory := submarinerInformers.NewSharedInformerFactoryWithOptions(submarinerClient,
		time.Second*30, submarinerInformers.WithNamespace(spec.Namespace))
	EndpointInformer := submarinerInformerFactory.Submariner().V1().Endpoints()

	iptableHandler, err := iptables.New()
	if err != nil {
		return nil, err
	}

	gatewayMonitor.ipt = iptableHandler
	gatewayMonitor.kubeClientSet = clientSet
	gatewayMonitor.submarinerClientSet = submarinerClient
	gatewayMonitor.endpointsSynced = EndpointInformer.Informer().HasSynced

	EndpointInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			gatewayMonitor.enqueueEndpoint(obj)
		},
		UpdateFunc: func(old, newObj interface{}) {
			gatewayMonitor.enqueueEndpoint(newObj)
		},
		DeleteFunc: gatewayMonitor.handleRemovedEndpoint,
	}, handlerResync)

	submarinerInformerFactory.Start(stopCh)
	return gatewayMonitor, nil
}

func (i *GatewayMonitor) Run(stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()

	klog.Info("Starting GatewayMonitor to monitor the active Gateway node in the cluster.")

	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, i.endpointsSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	if err := CreateGlobalNetMarkingChain(i.ipt); err != nil {
		return fmt.Errorf("error while calling createGlobalNetMarkingChain: %v", err)
	}

	klog.Info("Starting endpoint worker.")
	go wait.Until(i.runEndpointWorker, time.Second, stopCh)
	<-stopCh
	klog.Info("Shutting down endpoint worker.")
	ClearGlobalNetMarkingChain(i.ipt)
	return nil
}

func (r *GatewayMonitor) runEndpointWorker() {
	for r.processNextEndpoint() {
	}
}

func (i *GatewayMonitor) processNextEndpoint() bool {
	obj, shutdown := i.endpointWorkqueue.Get()
	if shutdown {
		return false
	}
	err := func() error {
		defer i.endpointWorkqueue.Done(obj)
		key := obj.(string)
		ns, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			i.endpointWorkqueue.Forget(obj)
			return fmt.Errorf("error while splitting meta namespace key %s: %v", key, err)
		}
		endpoint, err := i.submarinerClientSet.SubmarinerV1().Endpoints(ns).Get(name, metav1.GetOptions{})
		if err != nil {
			i.endpointWorkqueue.Forget(obj)
			return fmt.Errorf("error retrieving submariner endpoint object %s: %v", name, err)
		}

		if endpoint.Spec.ClusterID != i.clusterID {
			klog.V(4).Infof("Endpoint %s belongs to a remote cluster", endpoint.Spec.Hostname)
			for _, remoteSubnet := range endpoint.Spec.Subnets {
				MarkRemoteClusterTraffic(i.ipt, remoteSubnet, AddRules)
			}
			i.endpointWorkqueue.Forget(obj)
			return nil
		}

		hostname, err := os.Hostname()
		if err != nil {
			klog.Fatalf("Unable to determine hostname: %v", err)
		}

		// If the endpoint hostname matches with our hostname, it implies we are on gateway node
		if endpoint.Spec.Hostname == hostname {
			klog.V(4).Infof("We are now on GatewayNode %s", endpoint.Spec.PrivateIP)

			clusterList, err := i.submarinerClientSet.SubmarinerV1().Clusters(i.ipamSpec.Namespace).List(metav1.ListOptions{})
			if err != nil {
				i.endpointWorkqueue.AddRateLimited(key)
				return fmt.Errorf("error while retrieving the cluster info: %v", err)
			}

			globalCIDR := ""
			for _, cluster := range clusterList.Items {
				if cluster.Spec.ClusterID == i.clusterID {
					klog.V(4).Infof("GlobalCidr configured in %s is %s", i.clusterID, cluster.Spec.GlobalCIDR)
					if cluster.Spec.GlobalCIDR != nil && len(cluster.Spec.GlobalCIDR) > 0 {
						// TODO: Revisit when support for more than one globalCIDR is implemented.
						globalCIDR = cluster.Spec.GlobalCIDR[0]
					}
					break
				}
			}

			i.syncMutex.Lock()
			if !i.isGatewayNode {
				i.isGatewayNode = true
				if globalCIDR != "" {
					i.initializeIpamController(globalCIDR)
				} else {
					klog.Errorf("Cluster %s is not configured to use globalCidr", i.clusterID)
				}
			}
			i.syncMutex.Unlock()
		} else {
			klog.V(4).Infof("We are on non-gatewayNode. GatewayNode ip is %s", endpoint.Spec.PrivateIP)
			i.syncMutex.Lock()
			if i.isGatewayNode {
				i.stopIpamController()
				i.isGatewayNode = false
			}
			i.syncMutex.Unlock()
		}

		i.endpointWorkqueue.Forget(obj)
		return nil
	}()

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}
	return true
}

func (i *GatewayMonitor) enqueueEndpoint(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	klog.V(4).Infof("Enqueueing endpoint in gatewayMonitor %v", obj)
	i.endpointWorkqueue.AddRateLimited(key)
}

func (i *GatewayMonitor) handleRemovedEndpoint(obj interface{}) {
	var object *v1.Endpoint
	var ok bool
	if object, ok = obj.(*v1.Endpoint); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.Errorf("Could not convert object %v to an Endpoint", obj)
			return
		}
		object, ok = tombstone.Obj.(*v1.Endpoint)
		if !ok {
			klog.Errorf("Could not convert object tombstone %v to an Endpoint", tombstone.Obj)
			return
		}
		klog.V(4).Infof("Recovered deleted object '%s' from tombstone", object.GetName())
	}

	klog.V(4).Infof("Informed of removed endpoint for gateway monitor: %v", object)
	hostname, err := os.Hostname()
	if err != nil {
		klog.Fatalf("Could not retrieve hostname: %v", err)
	}

	if object.Spec.Hostname == hostname && object.Spec.ClusterID == i.clusterID {
		i.syncMutex.Lock()
		if i.isGatewayNode {
			i.stopIpamController()
			i.isGatewayNode = false
		}
		i.syncMutex.Unlock()
	} else if object.Spec.ClusterID != i.clusterID {
		// Endpoint associated with remote cluster is removed, delete the associated flows.
		for _, remoteSubnet := range object.Spec.Subnets {
			MarkRemoteClusterTraffic(i.ipt, remoteSubnet, DeleteRules)
		}
	}
}

func (i *GatewayMonitor) initializeIpamController(globalCIDR string) {
	informerFactory := informers.NewSharedInformerFactoryWithOptions(i.kubeClientSet, defaultResync)

	informerConfig := InformerConfigStruct{
		KubeClientSet:   i.kubeClientSet,
		ServiceInformer: informerFactory.Core().V1().Services(),
		PodInformer:     informerFactory.Core().V1().Pods(),
	}

	klog.V(4).Infof("On Gateway Node, initializing ipamController.")
	ipamController, err := NewController(i.ipamSpec, &informerConfig, globalCIDR)
	if err != nil {
		klog.Fatalf("Error creating controller: %s", err.Error())
	}

	i.stopProcessing = make(chan struct{})

	go func() {
		if err = ipamController.Run(i.stopProcessing); err != nil {
			klog.Fatalf("Error running ipamController: %s", err.Error())
		}
	}()

	informerFactory.Start(i.stopProcessing)
	klog.V(4).Infof("Successfully initialized ipamController.")
}

func (i *GatewayMonitor) stopIpamController() {
	if i.stopProcessing != nil {
		klog.V(4).Infof("Submariner GatewayEngine migrated to a new node, stopping ipamController.")
		close(i.stopProcessing)
		i.stopProcessing = nil
	}
	klog.V(4).Infof("Notified ipamController to stop processing.")
}
