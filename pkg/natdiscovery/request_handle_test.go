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
package natdiscovery

import (
	"net"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/submariner-io/submariner/pkg/types"
	"google.golang.org/protobuf/proto"

	natproto "github.com/submariner-io/submariner/pkg/natdiscovery/proto"
)

var _ = Describe("Request handling", func() {

	var localListener *natDiscovery
	var localUDPSent chan []byte
	var remoteListener *natDiscovery
	var remoteUDPSent chan []byte
	var localEndpoint types.SubmarinerEndpoint
	var remoteEndpoint types.SubmarinerEndpoint

	var remoteUDPAddr net.UDPAddr

	BeforeEach(func() {
		localEndpoint = createTestLocalEndpoint()
		remoteEndpoint = createTestRemoteEndpoint()

		localListener, localUDPSent, _ = createTestListener(&localEndpoint)
		localListener.findSrcIP = func(_ string) (string, error) { return testLocalPrivateIP, nil }
		remoteListener, remoteUDPSent, _ = createTestListener(&remoteEndpoint)
		remoteListener.findSrcIP = func(_ string) (string, error) { return testRemotePrivateIP, nil }

		remoteUDPAddr = net.UDPAddr{
			IP:   net.ParseIP(testRemotePrivateIP),
			Port: int(testRemoteNATPort),
		}
	})

	parseResponseInLocalListener := func(udpPacket []byte, remoteAddr *net.UDPAddr) *natproto.SubmarinerNatDiscoveryResponse {
		err := localListener.parseAndHandleMessageFromAddress(udpPacket, remoteAddr)
		Expect(err).NotTo(HaveOccurred())
		return parseProtocolResponse(awaitChan(localUDPSent))
	}

	requestResponseFromRemoteToLocal := func(remoteAddr *net.UDPAddr) []*natproto.SubmarinerNatDiscoveryResponse {
		err := remoteListener.sendCheckRequest(newRemoteEndpointNAT(&types.SubmarinerEndpoint{Spec: localEndpoint.Spec}))
		Expect(err).NotTo(HaveOccurred())
		return []*natproto.SubmarinerNatDiscoveryResponse{
			parseResponseInLocalListener(awaitChan(remoteUDPSent), remoteAddr), /* Private IP request */
			parseResponseInLocalListener(awaitChan(remoteUDPSent), remoteAddr), /* Public IP request */
		}
	}

	When("receiving a request with an unknown sender endpoint", func() {
		It("should respond with UNKNOWN_SRC_ENDPOINT", func() {
			response := requestResponseFromRemoteToLocal(&remoteUDPAddr)
			Expect(response[0].Response).To(Equal(natproto.ResponseType_UNKNOWN_SRC_ENDPOINT))
		})
	})

	When("receiving a request with a known sender endpoint", func() {
		It("should respond with OK", func() {
			localListener.AddEndpoint(&remoteEndpoint)
			response := requestResponseFromRemoteToLocal(&remoteUDPAddr)
			Expect(response[0].Response).To(Equal(natproto.ResponseType_OK))
			Expect(response[1].Response).To(Equal(natproto.ResponseType_OK))
		})

		Context("with a modified IP", func() {
			It("should respond with SRC_MODIFIED", func() {
				remoteUDPAddr.IP = net.ParseIP(testRemotePublicIP)
				localListener.AddEndpoint(&remoteEndpoint)
				response := requestResponseFromRemoteToLocal(&remoteUDPAddr)
				Expect(response[0].Response).To(Equal(natproto.ResponseType_SRC_MODIFIED))
			})
		})

		Context("with a modified port", func() {
			It("should respond with SRC_MODIFIED", func() {
				remoteUDPAddr.Port = int(testRemoteNATPort + 1)
				localListener.AddEndpoint(&remoteEndpoint)
				response := requestResponseFromRemoteToLocal(&remoteUDPAddr)
				Expect(response[0].Response).To(Equal(natproto.ResponseType_SRC_MODIFIED))
			})
		})
	})

	When("receiving a request with an unknown receiver endpoint ID", func() {
		It("should respond with UNKNOWN_DST_ENDPOINT", func() {
			localListener.AddEndpoint(&remoteEndpoint)
			localEndpoint.Spec.CableName = "invalid"
			response := requestResponseFromRemoteToLocal(&remoteUDPAddr)
			Expect(response[0].Response).To(Equal(natproto.ResponseType_UNKNOWN_DST_ENDPOINT))
		})
	})

	When("receiving a request with an unknown receiver cluster ID", func() {
		It("should respond with UNKNOWN_DST_CLUSTER", func() {
			localListener.AddEndpoint(&remoteEndpoint)
			localEndpoint.Spec.ClusterID = "invalid"
			response := requestResponseFromRemoteToLocal(&remoteUDPAddr)
			Expect(response[0].Response).To(Equal(natproto.ResponseType_UNKNOWN_DST_CLUSTER))
		})
	})

	When("receiving a request with a missing Sender", func() {
		It("should respond with MALFORMED", func() {
			request := createMalformedRequest(func(msg *natproto.SubmarinerNatDiscoveryMessage) {
				msg.GetRequest().Sender = nil
			})
			response := parseResponseInLocalListener(request, &remoteUDPAddr)
			Expect(response.Response).To(Equal(natproto.ResponseType_MALFORMED))
		})
	})

	When("receiving a malformed request with a missing Receiver", func() {
		It("should respond with MALFORMED", func() {
			request := createMalformedRequest(func(msg *natproto.SubmarinerNatDiscoveryMessage) {
				msg.GetRequest().Receiver = nil
			})
			response := parseResponseInLocalListener(request, &remoteUDPAddr)
			Expect(response.Response).To(Equal(natproto.ResponseType_MALFORMED))
		})
	})

	When("receiving a malformed request with a missing UsingDst", func() {
		It("should respond with MALFORMED", func() {
			request := createMalformedRequest(func(msg *natproto.SubmarinerNatDiscoveryMessage) {
				msg.GetRequest().UsingDst = nil
			})
			response := parseResponseInLocalListener(request, &remoteUDPAddr)
			Expect(response.Response).To(Equal(natproto.ResponseType_MALFORMED))
		})
	})

	When("receiving a malformed request with a missing UsingSrc", func() {
		It("should respond with MALFORMED", func() {
			request := createMalformedRequest(func(msg *natproto.SubmarinerNatDiscoveryMessage) {
				msg.GetRequest().UsingSrc = nil
			})
			response := parseResponseInLocalListener(request, &remoteUDPAddr)
			Expect(response.Response).To(Equal(natproto.ResponseType_MALFORMED))
		})
	})
})

func createMalformedRequest(mangleFunction func(*natproto.SubmarinerNatDiscoveryMessage)) []byte {
	request := natproto.SubmarinerNatDiscoveryRequest{
		RequestNumber: 1,
		Sender: &natproto.EndpointDetails{
			EndpointId: testRemoteEndpointName,
			ClusterId:  testRemoteClusterID,
		},
		Receiver: &natproto.EndpointDetails{
			EndpointId: testLocalEndpointName,
			ClusterId:  testLocalClusterID,
		},
		UsingSrc: &natproto.IPPortPair{
			IP:   testRemotePrivateIP,
			Port: uint32(natproto.DefaultPort),
		},
		UsingDst: &natproto.IPPortPair{
			IP:   testLocalPrivateIP,
			Port: uint32(natproto.DefaultPort),
		},
	}

	msgRequest := &natproto.SubmarinerNatDiscoveryMessage_Request{
		Request: &request,
	}

	message := natproto.SubmarinerNatDiscoveryMessage{
		Version: natproto.Version,
		Message: msgRequest,
	}

	mangleFunction(&message)

	buf, err := proto.Marshal(&message)
	Expect(err).NotTo(HaveOccurred())

	return buf
}
