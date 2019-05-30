package routecontroller

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Route", func() {
	Describe("Function populateCidrBlockList", func() {
		Context("When input CIDR blocks are not present in the existing subnets", func() {
			It("Should append the CIDR blocks to subnets", func() {
				routeController := RouteController{subnets: []string{"192.168.1.0/24"}}
				routeController.populateCidrBlockList([]string{"10.10.10.0/24", "192.168.1.0/24"})
				want := []string{"192.168.1.0/24", "10.10.10.0/24"}
				Expect(routeController.subnets).To(Equal(want))
			})
		})
		Context("When input CIDR blocks are present in the existing subnets", func() {
			It("Should not append the CIDR blocks to subnets", func() {
				routeController := RouteController{subnets: []string{"10.10.10.0/24"}}
				routeController.populateCidrBlockList([]string{"10.10.10.0/24", "192.168.1.0/24"})
				want := []string{"10.10.10.0/24", "192.168.1.0/24"}
				Expect(routeController.subnets).To(Equal(want))
			})
		})
	})

	Describe("Function containsString", func() {
		Context("When the given array of strings contains specified string", func() {
			It("Should return true", func() {
				Expect(containsString([]string{"unit", "test"}, "unit")).To(BeTrue())
			})
		})
		Context("When the given array of strings does not contain specified string", func() {
			It("Should return false", func() {
				Expect(containsString([]string{"unit", "test"}, "ginkgo")).To(BeFalse())
			})

		})
	})
})
