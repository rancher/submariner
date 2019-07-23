package ipsec

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/bronze1man/goStrongswanVici"
	"github.com/kelseyhightower/envconfig"
	"github.com/submariner-io/submariner/pkg/types"
	"k8s.io/klog"
)

const (
	// DefaultReplayWindowSize specifies the replay window size for charon
	DefaultReplayWindowSize = "1024"

	// DefaultIkeSaRekeyInterval specifies the default rekey interval for IKE_SA
	DefaultIkeSaRekeyInterval = "4h"

	// DefaultChildSaRekeyInterval specifies the default rekey interval for CHILD_SA
	DefaultChildSaRekeyInterval = "1h"
)

type strongSwan struct {
	localSubnets              []string
	localEndpoint             types.SubmarinerEndpoint
	secretKey                 string
	replayWindowSize          string
	ipSecIkeSaRekeyInterval   string
	ipSecChildSaRekeyInterval string

	debug   bool
	logFile string
}

func NewStrongSwan(localSubnets []string, localEndpoint types.SubmarinerEndpoint) (Driver, error) {
	ipSecSpec := specification{}

	err := envconfig.Process("ce_ipsec", &ipSecSpec)
	if err != nil {
		return nil, fmt.Errorf("error processing environment config for ce_ipsec: %v", err)
	}

	return &strongSwan{
		replayWindowSize:          DefaultReplayWindowSize,
		ipSecIkeSaRekeyInterval:   DefaultIkeSaRekeyInterval,
		ipSecChildSaRekeyInterval: DefaultChildSaRekeyInterval,
		localEndpoint:             localEndpoint,
		localSubnets:              localSubnets,
		secretKey:                 ipSecSpec.PSK,
		debug:                     ipSecSpec.Debug,
		logFile:                   ipSecSpec.LogFile,
	}, nil
}

func (i *strongSwan) Init() error {
	if err := runCharon(i.debug, i.logFile); err != nil {
		return err
	}

	if err := i.loadConns(); err != nil {
		return fmt.Errorf("Failed to load connections from charon: %v", err)
	}
	return nil
}

func (i *strongSwan) ConnectToEndpoint(endpoint types.SubmarinerEndpoint) (string, error) {
	client, err := getClient()
	if err != nil {
		return "", err
	}
	defer client.Close()

	if err := i.loadSharedKey(endpoint, client); err != nil {
		return "", fmt.Errorf("Encountered issue while trying to load shared keys: %v", err)
	}

	var localEndpointIP, remoteEndpointIP string

	if endpoint.Spec.NATEnabled {
		localEndpointIP = i.localEndpoint.Spec.PublicIP.String()
		remoteEndpointIP = endpoint.Spec.PublicIP.String()
	} else {
		localEndpointIP = i.localEndpoint.Spec.PrivateIP.String()
		remoteEndpointIP = endpoint.Spec.PrivateIP.String()
	}

	var localTs, remoteTs, localAddr, remoteAddr []string
	localTs = append(localTs, fmt.Sprintf("%s/32", i.localEndpoint.Spec.PrivateIP.String()))
	localTs = append(localTs, i.localSubnets...)

	localAddr = append(localAddr, i.localEndpoint.Spec.PrivateIP.String())

	remoteTs = append(remoteTs, fmt.Sprintf("%s/32", endpoint.Spec.PrivateIP.String()))
	remoteTs = append(remoteTs, endpoint.Spec.Subnets...)

	remoteAddr = append(remoteAddr, remoteEndpointIP)
	// todo: make the ESP proposals configurable
	childSAConf := goStrongswanVici.ChildSAConf{
		Local_ts:      localTs,
		Remote_ts:     remoteTs,
		ESPProposals:  []string{"aes128gcm16-modp2048", "aes-modp2048"},
		StartAction:   "start",
		CloseAction:   "restart",
		Mode:          "tunnel",
		ReqID:         "0",
		RekeyTime:     i.ipSecChildSaRekeyInterval,
		InstallPolicy: "yes",
	}
	klog.V(6).Infof("Using ReplayWindowSize: %v", i.replayWindowSize)
	childSAConf.ReplayWindow = i.replayWindowSize
	authLConf := goStrongswanVici.AuthConf{
		ID:         localEndpointIP,
		AuthMethod: "psk",
	}
	authRConf := goStrongswanVici.AuthConf{
		ID:         remoteEndpointIP,
		AuthMethod: "psk",
	}
	ikeConf := goStrongswanVici.IKEConf{
		LocalAddrs:  localAddr,
		RemoteAddrs: remoteAddr,
		Proposals:   []string{"aes128gcm16-sha256-modp2048", "aes-sha1-modp2048"},
		Version:     "2",
		LocalAuth:   authLConf,
		RemoteAuth:  authRConf,
		RekeyTime:   i.ipSecIkeSaRekeyInterval,
		Encap:       "yes",
		Mobike:      "no",
	}
	ikeConf.Children = map[string]goStrongswanVici.ChildSAConf{
		"submariner-child-" + endpoint.Spec.CableName: childSAConf,
	}

	// Sometimes, the connection doesn't load, so try to load it multiple times.
	for i := 0; i < 6; i++ {
		err = client.LoadConn(&map[string]goStrongswanVici.IKEConf{
			endpoint.Spec.CableName: ikeConf,
		})
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("failed loading connection %s: %v", endpoint.Spec.CableName, err)
	}

	klog.V(2).Infof("Loaded connection: %v", endpoint.Spec.CableName)

	return remoteEndpointIP, nil
}

func (i *strongSwan) DisconnectFromEndpoint(endpoint types.SubmarinerEndpoint) error {
	client, err := getClient()
	if err != nil {
		return err

	}
	defer client.Close()

	cableID := endpoint.Spec.CableName
	klog.Infof("Unloading connection %s", cableID)
	err = client.UnloadConn(&goStrongswanVici.UnloadConnRequest{
		Name: cableID,
	})
	if err != nil {
		return fmt.Errorf("Error when unloading connection %s : %v", cableID, err)
	}

	connections, err := client.ListConns("")
	if err != nil {
		klog.Errorf("Error while retrieving connections active after delete")
	} else {
		for _, conn := range connections {
			klog.V(6).Infof("Found connection %v", conn)
		}
	}

	saDeleted := false
	count := 0
	for {
		if saDeleted {
			break
		}
		if count > 2 {
			klog.Infof("Waited for connection terminate for 2 iterations, triggering a force delete of the IKE")
			err = client.Terminate(&goStrongswanVici.TerminateRequest{
				Ike:   cableID,
				Force: "yes",
			})
			if err != nil {
				klog.Errorf("error when terminating ike connection %s : %v", cableID, err)
			}
		}
		sas, err := client.ListSas("", "")
		if err != nil {
			klog.Errorf("error while retrieving sas active after delete")
		} else {
			found := false
			for _, samap := range sas {
				klog.V(6).Infof("Found SA %v", samap)
				sa, stillExists := samap[cableID]
				if stillExists && (sa.State == "DELETING" || sa.State == "CONNECTING") {
					found = true
					break
				} else if sa.State == "ESTABLISHED" {
					// what should we do here in this case? it could be perfectly legitimate that the connection was re-established,
					// i.e. datastore expired a good connection that was dead temporarily... in this case the sa shows up the way we want
					// if a true failover happens there should never be an established connection, but perhaps we should force delete
					// the connection anyway
					klog.V(4).Infof("It appears the peer became healthy again, and this connection was established")
					saDeleted = true
					break
				}
			}
			if found {
				klog.V(6).Infof("SA is still in deleting state; waiting 5 seconds before looking again")
				count++
				time.Sleep(5 * time.Second)
			} else {
				saDeleted = true
			}
		}
	}
	klog.Infof("Removed connection %s", cableID)
	return nil
}

func (i *strongSwan) GetActiveConnections(clusterID string) ([]string, error) {
	client, err := getClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	var connections []string
	prefix := fmt.Sprintf("submariner-cable-%s-", clusterID)

	conns, err := client.ListConns("")
	if err != nil {
		return nil, err
	}

	for _, conn := range conns {
		for k := range conn {
			if strings.HasPrefix(k, prefix) {
				klog.V(4).Infof("Found existing connection: %s", k)
				connections = append(connections, k)
			}
		}
	}
	return connections, nil
}

func (i *strongSwan) loadSharedKey(endpoint types.SubmarinerEndpoint, client *goStrongswanVici.ClientConn) error {
	klog.Infof("Loading shared key for endpoint")
	var identities []string
	var publicIP, privateIP string
	privateIP = endpoint.Spec.PrivateIP.String()
	identities = append(identities, privateIP)
	if endpoint.Spec.NATEnabled {
		publicIP = endpoint.Spec.PublicIP.String()
		if publicIP != privateIP {
			identities = append(identities, publicIP)
		}
	}
	sharedKey := &goStrongswanVici.Key{
		Typ:    "IKE",
		Data:   i.secretKey,
		Owners: identities,
	}

	err := client.LoadShared(sharedKey)
	if err != nil {
		klog.Infof("Failed to load pre-shared key for %s: %v", privateIP, err)
		if endpoint.Spec.NATEnabled {
			klog.Infof("Failed to load pre-shared key for %s: %v", publicIP, err)
		}
		return err
	}
	return nil
}

func runCharon(debug bool, logFile string) error {
	klog.Infof("Starting Charon")
	// Ignore error
	os.Remove("/var/run/charon.vici")

	args := []string{}
	for _, i := range strings.Split("dmn|mgr|ike|chd|cfg|knl|net|asn|tnc|imc|imv|pts|tls|esp|lib", "|") {
		args = append(args, "--debug-"+i)
		if debug {
			args = append(args, "3")
		} else {
			args = append(args, "1")
		}
	}

	cmd := exec.Command("charon", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	var outputFile *os.File
	if logFile != "" {
		out, err := os.OpenFile(logFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			return fmt.Errorf("Failed to open log file %s: %v", logFile, err)
		}

		cmd.Stdout = out
		cmd.Stderr = out
		outputFile = out
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGTERM,
	}

	if err := cmd.Start(); err != nil {
		// Note - Close handles nil receiver
		outputFile.Close()
		return fmt.Errorf("error starting the charon process wih args %v: %v", args, err)
	}

	go func() {
		defer outputFile.Close()
		klog.Fatalf("charon exited: %v", cmd.Wait())
	}()

	return nil
}

func (i *strongSwan) loadConns() error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	conns, err := client.ListConns("")
	if err != nil {
		return err
	}

	for _, conn := range conns {
		for k := range conn {
			if strings.HasPrefix(k, "submariner-conn-") {
				klog.Infof("Found existing connection: %s", k)
			}
		}
	}
	return nil
}

func getClient() (*goStrongswanVici.ClientConn, error) {
	var err error
	for i := 0; i < 3; i++ {
		var client *goStrongswanVici.ClientConn
		client, err = goStrongswanVici.NewClientConnFromDefaultSocket()
		if err == nil {
			return client, nil
		}

		if i > 0 {
			klog.Errorf("Failed to connect to charon: %v", err)
		}
		time.Sleep(1 * time.Second)
	}

	return nil, err
}
