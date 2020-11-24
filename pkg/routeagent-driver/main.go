package main

import (
	"flag"
	"os"

	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/submariner-io/submariner/pkg/event"
	"github.com/submariner-io/submariner/pkg/event/controller"
	"github.com/submariner-io/submariner/pkg/event/logger"
	kp "github.com/submariner-io/submariner/pkg/routeagent-driver/handlers/kubeproxy-iptables"
)

var (
	masterURL  string
	kubeconfig string
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	klog.Info("SGM: Starting submariner-route-agent using the event framework")
	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	eventHandlers := event.NewRegistry("routeagent-driver", os.Getenv("NETWORK_PLUGIN"))
	if err := eventHandlers.AddHandlers(logger.NewHandler(), kp.NewSyncHandler()); err != nil {
		klog.Fatalf("Error registering the handlers: %s", err.Error())
	}

	ctl, err := controller.New(&controller.Config{
		Registry:   &eventHandlers,
		MasterURL:  masterURL,
		Kubeconfig: kubeconfig})

	if err != nil {
		klog.Fatalf("Error creating controller for event handling %v", err)
	}

	err = ctl.Start(stopCh)
	if err != nil {
		klog.Fatalf("Error starting controller: %v", err)
	}

	<-stopCh
	ctl.Stop()

	klog.Info("All controllers stopped or exited. Stopping submariner-networkplugin-syncer")
}

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&masterURL, "master", "",
		"The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
}