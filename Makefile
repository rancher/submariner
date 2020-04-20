build_debug ?= false
restart ?= all

ifneq (,$(DAPPER_HOST_ARCH))

# Running in Dapper

include $(SHIPYARD_DIR)/Makefile.inc

TARGETS := $(shell ls -p scripts | grep -v -e / -e images -e reload-images)
CLUSTERS_ARGS = --cluster_settings $(DAPPER_SOURCE)/scripts/kind-e2e/cluster_settings

clusters: build images

e2e: # internally depends on deploy target, which will execute only if not already deployed
	./scripts/kind-e2e/e2e.sh

reload-images: build images
	./scripts/$@ --restart $(restart)

images: build
	./scripts/$@ $(images_flags)

$(TARGETS): vendor/modules.txt
	./scripts/$@ --build_debug $(build_debug)

vendor/modules.txt: go.mod
	go mod download
	go mod vendor

.PHONY: $(TARGETS)

else

# Not running in Dapper

include Makefile.dapper

endif

# Disable rebuilding Makefile
Makefile Makefile.dapper Makefile.inc: ;
